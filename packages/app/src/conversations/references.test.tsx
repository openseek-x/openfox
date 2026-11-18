import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, expect, it } from "vitest";
import { QueryClientProvider, focusManager } from "@tanstack/react-query";
import { createApi, HttpClient, type MessagePage } from "@openfox/api";
import { I18nProvider } from "@openfox/i18n";

import { App, createQueryClient } from "../App";
import { ApiProvider, keys } from "../context";
import { createEventBatcher } from "../realtime/useOrgEvents";
import { apiError, renderSettings } from "../settings/testing";
import { backend, json } from "../tasks/test-backend";
import { ORG, chatBase, message, summary } from "./fixtures";
import { useMessages } from "./queries";
import { ReferenceCard } from "./ReferenceCard";

afterEach(() => {
  localStorage.clear();
  focusManager.setFocused(undefined);
});

it("forwards only on confirmation, pages/searches targets and retains a failed attempt key", async () => {
  let attempts = 0;
  const server = backend((call) => {
    if (call.path === `${ORG}/conversations`)
      return json(200, {
        conversations: call.query.get("cursor")
          ? [summary("c2", { offer_participant: true })]
          : [
              summary("c1", { offer_participant: true }),
              summary("view", { offer_participant: false }),
            ],
        next_cursor: call.query.get("cursor") ? null : "next",
      });
    if (call.path.endsWith("/references")) {
      attempts++;
      return attempts === 1
        ? apiError(503, "UNAVAILABLE")
        : json(201, message(3, { kind: "REFERENCE", conversation_id: "c2" }));
    }
    return undefined;
  });
  localStorage.setItem("openfox:onboarding:o1", "{}");
  const cache = createQueryClient();
  cache.setDefaultOptions({ queries: { retry: false } });
  render(
    <MemoryRouter initialEntries={["/o/o1/spaces/s1/tasks?task=OPEN-1"]}>
      <App api={server.api} queryClient={cache} locale="zh-CN" />
    </MemoryRouter>,
  );
  const user = userEvent.setup();
  const open = async () => {
    await user.click(await screen.findByRole("button", { name: "更多操作" }));
    await user.click(screen.getByRole("menuitem", { name: "转发到会话" }));
    return screen.findByRole("dialog", { name: "转发到会话" });
  };
  let dialog = await open();
  await user.click(within(dialog).getByRole("button", { name: "取消" }));
  expect(server.find("POST", "/references")).toHaveLength(0);
  dialog = await open();
  expect(
    await within(dialog).findByRole("radio", { name: /会话 view/ }),
  ).toBeDisabled();
  await user.click(
    within(dialog).getByRole("button", { name: "加载更多会话" }),
  );
  await user.click(
    await within(dialog).findByRole("radio", { name: "会话 c2" }),
  );
  await user.click(within(dialog).getByRole("button", { name: "确认转发" }));
  await within(dialog).findByRole("alert");
  await user.click(within(dialog).getByRole("button", { name: "确认转发" }));
  await waitFor(() =>
    expect(
      screen.queryByRole("dialog", { name: "转发到会话" }),
    ).not.toBeInTheDocument(),
  );
  const posts = server.find("POST", "/references");
  expect(posts).toHaveLength(2);
  expect(posts[0]!.body).toEqual(posts[1]!.body);
  expect(posts[0]!.body).toMatchObject({ type: "TASK", task_id: "task-1" });
  expect(Object.keys(posts[0]!.body as object).sort()).toEqual([
    "client_message_id",
    "task_id",
    "type",
  ]);
});

it("renders readable task links, hides unavailable sources and offers no quote or snapshot action", async () => {
  renderSettings(
    "/o/o1/chat/c1",
    chatBase((call) => {
      if (call.path === `${ORG}/conversations/c1`)
        return json(200, summary("c1"));
      if (call.path.endsWith("/messages"))
        return json(200, {
          messages: [
            message(1, {
              kind: "REFERENCE",
              body: "任务引用",
              reference: {
                type: "TASK",
                available: true,
                task_id: "t1",
                space_id: "s1",
                display_id: "OPEN-1",
                title: "Readable source",
              },
            }),
            message(3, {
              kind: "REFERENCE",
              body: "任务引用",
              reference: { type: "TASK", available: false },
            }),
          ],
          next_cursor: 0,
        });
      return undefined;
    }),
  );
  expect(
    await screen.findByRole("link", { name: /Readable source/ }),
  ).toHaveAttribute("href", "/o/o1/spaces/s1/tasks?task=OPEN-1");
  expect(screen.getByText("任务不可用")).toBeVisible();
  expect(
    screen.queryByRole("button", { name: "引用回复" }),
  ).not.toBeInTheDocument();
  expect(
    screen.queryByRole("button", { name: "分享这条消息" }),
  ).not.toBeInTheDocument();
});

it.each(["TASK", "DOC"] as const)(
  "clears derived titles before source ACL/task events refetch and leaves ordinary messages unchanged (%s)",
  async (type) => {
    const cache = createQueryClient();
    const key = keys.conversationMessages("o1", "c1");
    const original = message(1);
    const ref = message(3, {
      kind: "REFERENCE",
      reference: {
        type,
        available: true,
        title: "Private title",
        space_id: "secret",
        display_id: "SEC-1",
        doc_id: "d1",
      },
    });
    const batcher = createEventBatcher(cache, "o1", 0);
    for (const eventType of [
      "task.changed",
      "space.changed",
      "member.changed",
      "doc.changed",
    ]) {
      cache.setQueryData(key, {
        pages: [{ messages: [original, ref], next_cursor: 0 }],
        pageParams: [0],
      });
      await act(async () => {
        batcher.push(eventType, {
          type: eventType,
          payload: { task_ids: ["t1"] },
        });
        await new Promise((resolve) => setTimeout(resolve, 5));
      });
      const data = cache.getQueryData<{
        pages: { messages: (typeof ref)[] }[];
      }>(key)!;
      expect(data.pages[0]!.messages[0]).toEqual(original);
      expect(data.pages[0]!.messages[1]!.reference).toEqual({
        type,
        available: false,
      });
      expect(cache.getQueryState(key)?.isInvalidated).toBe(true);
    }
  },
);

it.each(["TASK", "DOC"] as const)(
  "does not restore a stale readable card after a permission event or failed revalidation (%s)",
  async (type) => {
    const cache = createQueryClient();
    cache.setDefaultOptions({ queries: { retry: false } });
    const key = keys.conversationMessages("o1", "c1");
    const visible = message(1, {
      kind: "REFERENCE",
      reference: {
        type,
        available: true,
        title: "Old private title",
        space_id: "s1",
        display_id: "OPEN-1",
        doc_id: "d1",
      },
    });
    const denied = {
      ...visible,
      reference: { type, available: false },
    };
    let finishOld!: (response: Response) => void;
    let calls = 0;
    const api = createApi(
      new HttpClient("/api", async () => {
        calls++;
        if (calls === 1)
          return json(200, { messages: [visible], next_cursor: 0 });
        if (calls === 2)
          return new Promise<Response>((resolve) => {
            finishOld = resolve;
          });
        if (calls === 3)
          return json(200, { messages: [denied], next_cursor: 0 });
        return apiError(503, "UNAVAILABLE");
      }),
    );
    function Reader() {
      const result = useMessages("o1", "c1");
      return (
        <>
          {result.data?.pages
            .flatMap((page) => page.messages)
            .map((item) => (
              <ReferenceCard key={item.id} message={item} />
            ))}
        </>
      );
    }
    render(
      <MemoryRouter>
        <I18nProvider initialLocale="zh-CN">
          <QueryClientProvider client={cache}>
            <ApiProvider api={api}>
              <Reader />
            </ApiProvider>
          </QueryClientProvider>
        </I18nProvider>
      </MemoryRouter>,
    );
    await screen.findByText("Old private title");
    act(() => {
      void cache.refetchQueries({ queryKey: key });
    });
    await waitFor(() => expect(calls).toBe(2));
    await screen.findByText(type === "DOC" ? "文档不可用" : "任务不可用");
    const batcher = createEventBatcher(cache, "o1", 0);
    act(() => {
      batcher.push(type === "DOC" ? "doc.changed" : "member.changed", {});
    });
    await waitFor(() => expect(calls).toBe(3));
    await waitFor(() => expect(cache.isFetching({ queryKey: key })).toBe(0));
    await act(async () => {
      finishOld(json(200, { messages: [visible], next_cursor: 0 }));
      await Promise.resolve();
    });
    expect(screen.queryByText("Old private title")).not.toBeInTheDocument();
    const current = cache.getQueryData<{ pages: MessagePage[] }>(key)!;
    expect(current.pages[0]!.messages[0]!.reference?.available).toBe(false);
    // A network failure also hides previously authorized cached fields.
    act(() => {
      cache.setQueryData(key, {
        pages: [{ messages: [visible], next_cursor: 0 }],
        pageParams: [0],
      });
    });
    await screen.findByText("Old private title");
    await act(async () => {
      await cache.refetchQueries({ queryKey: key });
    });
    await screen.findByText(type === "DOC" ? "文档不可用" : "任务不可用");
    expect(screen.queryByText("Old private title")).not.toBeInTheDocument();
  },
);

it.each(["TASK", "DOC"] as const)(
  "revalidates fresh reference caches on focus and remount without changing ordinary messages (%s)",
  async (type) => {
    const cache = createQueryClient();
    cache.setDefaultOptions({
      queries: { retry: false, staleTime: 30_000, refetchOnWindowFocus: false },
    });
    const ref = message(1, {
      kind: "REFERENCE",
      reference: {
        type,
        available: true,
        title: "Source",
        space_id: "s1",
        display_id: "OPEN-1",
        doc_id: "d1",
      },
    });
    let reads = 0;
    const api = createApi(
      new HttpClient("/api", async () => {
        reads++;
        return json(200, { messages: [ref], next_cursor: 0 });
      }),
    );
    const key = keys.conversationMessages("o1", "c1");
    const seed = (item: typeof ref) =>
      cache.setQueryData(key, {
        pages: [{ messages: [item], next_cursor: 0 }],
        pageParams: [0],
      });
    seed(ref);
    function Reader() {
      useMessages("o1", "c1");
      return null;
    }
    const tree = (visible: boolean) => (
      <QueryClientProvider client={cache}>
        <ApiProvider api={api}>{visible && <Reader />}</ApiProvider>
      </QueryClientProvider>
    );
    const view = render(tree(true));
    await waitFor(() => expect(reads).toBe(1));
    await waitFor(() => expect(cache.isFetching()).toBe(0));
    act(() => {
      focusManager.setFocused(false);
      focusManager.setFocused(true);
    });
    await waitFor(() => expect(reads).toBe(2));
    await waitFor(() => expect(cache.isFetching()).toBe(0));
    view.rerender(tree(false));
    view.rerender(tree(true));
    await waitFor(() => expect(reads).toBe(3));
    await waitFor(() => expect(cache.isFetching()).toBe(0));
    view.rerender(tree(false));
    seed(message(1));
    view.rerender(tree(true));
    await act(async () => {
      focusManager.setFocused(false);
      focusManager.setFocused(true);
      await Promise.resolve();
    });
    expect(reads).toBe(3);
  },
);

it("opens native documents in the workspace and keeps library links for other DOC kinds", async () => {
  renderSettings(
    "/o/o1/chat/c1",
    chatBase((call) => {
      if (call.path === `${ORG}/conversations/c1`)
        return json(200, summary("c1"));
      if (call.path.endsWith("/messages"))
        return json(200, {
          messages: ["doc", "sheet", "file"]
            .map((kind, i) =>
              message(i + 1, {
                author_kind: "USER",
                kind: "REFERENCE",
                body: "文档引用",
                reference: {
                  type: "DOC",
                  available: true,
                  doc_id: `d${i}`,
                  space_id: "s1",
                  doc_kind: kind as "doc" | "sheet" | "file",
                  title: i === 0 ? "" : `Source ${kind}`,
                },
              }),
            )
            .concat([
              message(5, {
                kind: "REFERENCE",
                body: "文档引用",
                reference: { type: "DOC", available: false },
              }),
            ]),
          next_cursor: 0,
        });
      return undefined;
    }),
  );
  expect(
    await screen.findByRole("button", { name: /未命名文档/ }),
  ).toBeVisible();
  expect(screen.getByRole("link", { name: /Source sheet/ })).toHaveAttribute(
    "href",
    "/o/o1/spaces/s1/docs/d1",
  );
  expect(screen.getByRole("link", { name: /Source file/ })).toHaveAttribute(
    "href",
    "/o/o1/spaces/s1/docs/d2",
  );
  expect(screen.getByText("文档不可用")).toBeVisible();
  expect(
    screen.queryByRole("button", { name: "引用回复" }),
  ).not.toBeInTheDocument();
  expect(
    screen.queryByRole("button", { name: "分享这条消息" }),
  ).not.toBeInTheDocument();
});
