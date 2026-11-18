import { cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it } from "vitest";
import { apiError, json, renderSettings, type Call } from "../settings/testing";
import { ORG, chatBase, message, summary } from "./fixtures";
import { workspaceStore } from "./workspaceStorage";
import { pendingKey, type PendingSend } from "./pendingSend";

const route = (extra: (call: Call) => Response | undefined) =>
  chatBase((call) => {
    const result = extra(call);
    if (result) return result;
    if (call.path === `${ORG}/conversations/c1/messages`)
      return json(200, { messages: [], next_cursor: 0 });
    if (call.path === `${ORG}/conversations/c1`)
      return json(200, summary("c1"));
    if (call.path === `${ORG}/conversations`)
      return json(200, { conversations: [summary("c1")], next_cursor: null });
    return undefined;
  });

it("reopens durable input after an accepted send lost its response, without sending twice or deleting newer input", async () => {
  let accepted: ReturnType<typeof message> | null = null;
  let posts = 0,
    lookups = 0;
  const handler = route((call) => {
    if (call.method === "POST" && call.path.endsWith("/messages")) {
      posts++;
      const body = call.body as { body: string; client_message_id: string };
      accepted = message(10, body);
      return apiError(503, "UNAVAILABLE"); // server committed; response was lost
    }
    if (call.path.endsWith("/message-receipt")) {
      lookups++;
      return json(200, { message: accepted });
    }
    if (call.path.endsWith("/messages") && call.method === "GET")
      return json(200, {
        messages: accepted ? [accepted] : [],
        next_cursor: 0,
      });
    return undefined;
  });
  renderSettings("/o/o1/chat/c1", handler);
  const box = await screen.findByRole("textbox", { name: "输入消息" });
  await userEvent.type(box, "第一份输入{Enter}");
  await screen.findByText("发送失败，请重试");
  fireEvent.change(box, { target: { value: "确认前继续写的新内容" } });
  const first = workspaceStore();
  await first.flush();
  expect(first.read<PendingSend>(pendingKey("c1"))?.request?.body).toBe(
    "第一份输入",
  );
  cleanup();
  await first.close();
  renderSettings("/o/o1/chat/c1", handler);
  const restored = await screen.findByRole("textbox", { name: "输入消息" });
  await waitFor(() =>
    expect(workspaceStore().read(pendingKey("c1"))).toBeUndefined(),
  );
  expect(restored).toHaveValue("确认前继续写的新内容");
  expect(posts).toBe(1);
  expect(lookups).toBe(1);
  expect(await screen.findByText("第一份输入")).toBeInTheDocument();
});

it("an uncertain receipt never replays; explicit retry after a missing receipt uses the frozen payload", async () => {
  let receiptFails = true;
  const posts: unknown[] = [];
  const handler = route((call) => {
    if (call.method === "POST" && call.path.endsWith("/messages")) {
      posts.push(call.body);
      return posts.length === 1
        ? apiError(503, "UNAVAILABLE")
        : json(201, message(11, call.body as object));
    }
    if (call.path.endsWith("/message-receipt"))
      return receiptFails
        ? apiError(503, "UNAVAILABLE")
        : json(200, { message: null });
    return undefined;
  });
  renderSettings("/o/o1/chat/c1", handler);
  const box = await screen.findByRole("textbox", { name: "输入消息" });
  await userEvent.type(box, "上次输入{Enter}");
  await screen.findByText("发送失败，请重试");
  fireEvent.change(box, { target: { value: "下次输入" } });
  const first = workspaceStore();
  await first.flush();
  cleanup();
  await first.close();
  renderSettings("/o/o1/chat/c1", handler);
  await screen.findByText("暂时无法确认发送结果，请稍后重试。");
  expect(posts).toHaveLength(1);
  expect(
    screen.queryByRole("button", { name: "重试上次发送" }),
  ).not.toBeInTheDocument();
  receiptFails = false;
  await userEvent.click(screen.getByRole("button", { name: "确认发送结果" }));
  await userEvent.click(
    await screen.findByRole("button", { name: "重试上次发送" }),
  );
  await waitFor(() =>
    expect(workspaceStore().read(pendingKey("c1"))).toBeUndefined(),
  );
  expect(posts).toHaveLength(2);
  expect(posts[1]).toEqual(posts[0]);
  expect(screen.getByRole("textbox", { name: "输入消息" })).toHaveValue(
    "下次输入",
  );
});
