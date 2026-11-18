import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, expect, it, vi } from "vitest";
import {
  createApi,
  HttpClient,
  type WorkspaceCommentThread,
  type WorkspaceCommentSource,
  type WorkspaceCommentMutation,
} from "@openfox/api";
import { I18nProvider } from "@openfox/i18n";
import { ApiProvider } from "../../context";
import { SaveComment, WorkspaceComments } from "./WorkspaceComments";
import { readDraft, resetDrafts } from "../drafts";

const source: WorkspaceCommentSource = {
  name: "报告.md",
  file: {
    id: "context",
    artifact_id: "artifact",
    sha256: "a".repeat(64),
    comments: [
      {
        thread_id: "thread",
        body: "原始意见",
        anchor: { kind: "text", start: 0, end: 3, quote: "abc" },
      },
    ],
  },
};
function fixture({
  failSave = false,
  loseUpdate = false,
  resolutionStatus = "exact",
  loseResolution = false,
} = {}) {
  let thread: WorkspaceCommentThread | undefined;
  const operations = new Map<string, WorkspaceCommentThread>();
  const requests: { path: string; method: string; body: any }[] = [];
  const saved = vi.fn();
  const transport = async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = new URL(String(input), "http://localhost").pathname,
      method = init?.method ?? "GET",
      body = init?.body ? JSON.parse(String(init.body)) : undefined;
    requests.push({ path, method, body });
    if (path.endsWith("/me")) return Response.json({ id: "u1" });
    if (path.includes("/resolutions/")) {
      if (loseResolution) {
        loseResolution = false;
        return Response.json(
          { error: { code: "FAILED", message: "lost" } },
          { status: 500 },
        );
      }
      const target = structuredClone(source);
      target.file!.artifact_id = "current";
      target.file!.sha256 = "b".repeat(64);
      target.file!.comments[0]!.anchor = {
        kind: "text",
        start: 4,
        end: 7,
        quote: "abc",
      };
      return Response.json({
        id: path.split("/").at(-1),
        thread_id: "thread",
        thread_version: 1,
        status: resolutionStatus,
        reason: "matched",
        ...(resolutionStatus === "exact" && {
          target,
          anchor: target.file!.comments[0]!.anchor,
        }),
      });
    }
    if (method === "PUT") {
      if (failSave) {
        failSave = false;
        return Response.json(
          { error: { code: "FAILED", message: "unavailable" } },
          { status: 500 },
        );
      }
      thread ??= {
        id: "thread",
        conversation_id: "c1",
        version: 1,
        status: "OPEN",
        source: structuredClone(body),
        comments: [
          {
            id: "thread",
            author_id: "u1",
            body: "原始意见",
            created_at: "2026-11-17T00:00:00Z",
          },
        ],
        updated_at: "2026-11-17T00:00:00Z",
      };
      return Response.json(thread);
    }
    if (method === "PATCH") {
      const m = body as WorkspaceCommentMutation;
      if (operations.has(m.operation_id))
        return Response.json(operations.get(m.operation_id));
      if (m.version !== thread!.version)
        return Response.json(
          { error: { code: "REVIEW_CONFLICT", message: "conflict" } },
          { status: 409 },
        );
      thread = structuredClone(thread!);
      thread.version++;
      if (m.action === "edit")
        thread.comments.find((p) => p.id === m.comment_id)!.body = m.body!;
      if (m.action === "reply")
        thread.comments.push({
          id: m.operation_id,
          author_id: "u1",
          body: m.body!,
          created_at: thread.updated_at,
        });
      if (m.action === "resolve") thread.status = "RESOLVED";
      if (m.action === "reopen") thread.status = "OPEN";
      operations.set(m.operation_id, structuredClone(thread));
      if (loseUpdate) {
        loseUpdate = false;
        throw new TypeError("lost response");
      }
      return Response.json(thread);
    }
    return Response.json({ threads: thread ? [thread] : [], next_cursor: "" });
  };
  const api = createApi(new HttpClient("/api", transport));
  const mount = () =>
    render(
      <I18nProvider initialLocale="zh-CN">
        <ApiProvider api={api}>
          <QueryClientProvider
            client={
              new QueryClient({ defaultOptions: { queries: { retry: false } } })
            }
          >
            <MemoryRouter>
              <SaveComment
                orgId="o1"
                conversationId="c1"
                id="thread"
                source={source}
                disabled={false}
                onSaved={saved}
              />
              <WorkspaceComments
                orgId="o1"
                conversationId="c1"
                disabled={false}
              />
            </MemoryRouter>
          </QueryClientProvider>
        </ApiProvider>
      </I18nProvider>,
    );
  return { ...mount(), mount, requests, saved, getThread: () => thread };
}
beforeEach(resetDrafts);
it("saves discussion without chat, copies a selected revision, and retains it after edits and remount", async () => {
  const f = fixture();
  await userEvent.click(screen.getByRole("button", { name: "保存为评论" }));
  const summary = await screen.findByText("原始意见", {
    selector: "summary span",
  });
  await userEvent.click(summary);
  await userEvent.click(
    await screen.findByRole("button", { name: "交给 Agent" }),
  );
  const selected = structuredClone(
    readDraft("c1")!.fileContexts![0]!.comments[0]!,
  );
  expect(selected).toMatchObject({
    body: "原始意见",
    discussion: { id: "thread", version: 1, comment_id: "thread" },
  });
  await userEvent.click(screen.getByRole("button", { name: "编辑" }));
  await userEvent.clear(screen.getByRole("textbox", { name: "编辑评论" }));
  await userEvent.type(
    screen.getByRole("textbox", { name: "编辑评论" }),
    "更新后的意见",
  );
  await userEvent.click(screen.getByRole("button", { name: "保存修改" }));
  await waitFor(() => expect(f.getThread()?.version).toBe(2));
  expect(readDraft("c1")!.fileContexts![0]!.comments[0]).toEqual(selected);
  await waitFor(() =>
    expect(screen.getByRole("button", { name: "解决" })).toBeEnabled(),
  );
  await userEvent.click(screen.getByRole("button", { name: "解决" }));
  await userEvent.click(await screen.findByRole("button", { name: "重开" }));
  await waitFor(() => expect(f.getThread()?.status).toBe("OPEN"));
  f.unmount();
  f.mount();
  await screen.findByText("更新后的意见", { selector: "summary span" });
  expect(
    f.requests.some(
      (r) => r.path.endsWith("/messages") || r.path.endsWith("/runs"),
    ),
  ).toBe(false);
  expect(f.saved).toHaveBeenCalledTimes(1);
});
it("retries saving with the same identity and does not consume unacknowledged input", async () => {
  const f = fixture({ failSave: true });
  await userEvent.click(screen.getByRole("button", { name: "保存为评论" }));
  await screen.findByRole("alert");
  expect(f.saved).not.toHaveBeenCalled();
  await userEvent.click(screen.getByRole("button", { name: "保存为评论" }));
  await waitFor(() => expect(f.saved).toHaveBeenCalledTimes(1));
  const puts = f.requests.filter((r) => r.method === "PUT");
  expect(puts).toHaveLength(2);
  expect(puts[0]).toEqual(puts[1]);
});
it("recovers a lost update receipt and retries its frozen operation without duplicating the reply", async () => {
  const f = fixture({ loseUpdate: true });
  await userEvent.click(screen.getByRole("button", { name: "保存为评论" }));
  await userEvent.click(
    await screen.findByText("原始意见", { selector: "summary span" }),
  );
  await userEvent.type(
    screen.getByRole("textbox", { name: "回复评论" }),
    "补充意见",
  );
  await userEvent.click(screen.getByRole("button", { name: "回复" }));
  await screen.findByRole("alert");
  f.unmount();
  f.mount();
  await userEvent.click(
    await screen.findByText("原始意见", { selector: "summary span" }),
  );
  await userEvent.click(screen.getByRole("button", { name: "重试上次更新" }));
  await waitFor(() =>
    expect(screen.queryByRole("button", { name: "重试上次更新" })).toBeNull(),
  );
  expect(f.getThread()!.comments).toHaveLength(2);
  const patches = f.requests.filter((r) => r.method === "PATCH");
  expect(patches).toHaveLength(2);
  expect(patches[0]).toEqual(patches[1]);
});

it("queues only the exact derived source, preserves the original and retries a lost resolution receipt", async () => {
  const f = fixture({ loseResolution: true });
  await userEvent.click(screen.getByRole("button", { name: "保存为评论" }));
  await userEvent.click(
    await screen.findByText("原始意见", { selector: "summary span" }),
  );
  await userEvent.click(screen.getByRole("button", { name: "定位到当前版本" }));
  await screen.findByRole("alert");
  f.unmount();
  f.mount();
  await userEvent.click(
    await screen.findByText("原始意见", { selector: "summary span" }),
  );
  await userEvent.click(screen.getByRole("button", { name: "定位到当前版本" }));
  await screen.findByText("已精确定位");
  await userEvent.click(screen.getByRole("button", { name: "交给 Agent" }));
  expect(readDraft("c1")!.fileContexts![0]).toMatchObject({
    artifact_id: "current",
    sha256: "b".repeat(64),
    comments: [
      {
        anchor: { start: 4, end: 7 },
        discussion: { id: "thread", resolution_id: expect.any(String) },
      },
    ],
  });
  expect(f.getThread()!.source).toEqual(source);
  const requests = f.requests.filter((r) => r.path.includes("/resolutions/"));
  expect(requests).toHaveLength(2);
  expect(requests[0]).toEqual(requests[1]);
});
for (const resolutionStatus of ["approximate", "ambiguous", "orphaned"])
  it(`does not queue ${resolutionStatus} as an edit`, async () => {
    const f = fixture({ resolutionStatus });
    await userEvent.click(screen.getByRole("button", { name: "保存为评论" }));
    await userEvent.click(
      await screen.findByText("原始意见", { selector: "summary span" }),
    );
    await userEvent.click(
      screen.getByRole("button", { name: "定位到当前版本" }),
    );
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "交给 Agent" })).toBeDisabled(),
    );
    expect(readDraft("c1")?.fileContexts ?? []).toHaveLength(0);
    expect(f.getThread()!.source).toEqual(source);
  });
