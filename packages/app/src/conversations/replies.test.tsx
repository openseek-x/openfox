import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";

import { apiError, json, renderSettings } from "../settings/testing";
import { ORG, TS, chatBase, message, summary } from "./fixtures";
import { MessageReply, replyPreviewText } from "./ReplyPreview";
import { appendToDraft, quoteDraft, readDraft, saveDraft } from "./drafts";

describe("message quote replies", () => {
  it("quotes either author, cancels without clearing text, and retries the same reference/key", async () => {
    let attempts = 0;
    const calls = renderSettings(
      "/o/o1/chat/c1",
      chatBase((call) => {
        if (call.path.endsWith("/message-receipt"))
          return json(200, { message: null });
        if (call.path === `${ORG}/conversations/c1/messages`) {
          if (call.method === "POST") {
            attempts++;
            if (attempts === 1) return apiError(503, "UNAVAILABLE");
            return json(201, message(3, { body: "reply", reply_to_id: "m2" }));
          }
          return json(200, {
            messages: [message(1), message(2)],
            next_cursor: 0,
          });
        }
        if (call.path === `${ORG}/conversations/c1`)
          return json(200, summary("c1"));
        if (call.path === `${ORG}/conversations`)
          return json(200, {
            conversations: [summary("c1")],
            next_cursor: null,
          });
        return undefined;
      }),
    );
    const box = await screen.findByRole("textbox", { name: "输入消息" });
    fireEvent.change(box, { target: { value: "reply" } });
    const quotes = await screen.findAllByRole("button", { name: "引用回复" });
    await userEvent.click(quotes[0]!);
    expect(await screen.findByText("引用用户消息")).toBeInTheDocument();
    expect(box).toHaveValue("reply");
    await userEvent.click(screen.getByRole("button", { name: "取消引用" }));
    expect(screen.queryByText("引用用户消息")).not.toBeInTheDocument();
    expect(box).toHaveValue("reply");
    await userEvent.click(quotes[1]!);
    expect(await screen.findByText("引用 Agent 消息")).toBeInTheDocument();
    await userEvent.type(box, "{Enter}");
    expect(await screen.findByText("发送失败，请重试")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "取消引用" }),
    ).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "确认发送结果" }));
    await userEvent.click(
      await screen.findByRole("button", { name: "重试上次发送" }),
    );
    await waitFor(() => expect(box).toHaveValue(""));
    expect(
      screen.queryByRole("button", { name: "取消引用" }),
    ).not.toBeInTheDocument();
    const posts = calls
      .filter(
        (c) =>
          c.method === "POST" && c.path === `${ORG}/conversations/c1/messages`,
      )
      .map((c) => c.body as Record<string, unknown>);
    expect(posts).toHaveLength(2);
    expect(posts[0]).toEqual(posts[1]);
    expect(posts[0]?.reply_to_id).toBe("m2");
    expect(posts[0]).not.toHaveProperty("reply_snapshot");
  });

  it("keeps quote-only drafts isolated and preserves quoted selection when inserting a quick command", () => {
    saveDraft("c1", { body: "text", picked: [], files: [] });
    quoteDraft("c1", {
      id: "m1",
      body: "first",
      authorKind: "USER",
      truncated: false,
    });
    quoteDraft("c2", {
      id: "m2",
      body: "second",
      authorKind: "AGENT",
      truncated: false,
    });
    appendToDraft("c1", "command");
    expect(readDraft("c1")?.body).toBe("text\n\ncommand");
    expect(readDraft("c1")?.reply?.id).toBe("m1");
    expect(readDraft("c2")?.body).toBe("");
    expect(readDraft("c2")?.reply?.id).toBe("m2");
  });

  it("bounds Unicode previews, renders text safely, and distinguishes legacy and unavailable sources", () => {
    const preview = replyPreviewText("🦊".repeat(201));
    expect(Array.from(preview.body)).toHaveLength(200);
    expect(preview.truncated).toBe(true);
    const reply = {
      message_id: "m1",
      seq: 1,
      author_kind: "USER" as const,
      body: "<img src=x onerror=alert(1)>",
      created_at: TS,
      truncated: false,
      snapshot: false,
    };
    const view = render(
      <MessageReply message={message(2, { reply_to_id: "m1", reply })} />,
    );
    expect(screen.getByText(/历史引用 · 当前原文/)).toBeInTheDocument();
    expect(
      within(screen.getByLabelText("引用预览")).getByText(reply.body),
    ).toBeInTheDocument();
    expect(view.container.querySelector("img")).toBeNull();
    view.rerender(<MessageReply message={message(2, { reply_to_id: "m1" })} />);
    expect(screen.getByText("原消息不可用")).toBeInTheDocument();
    view.rerender(<MessageReply message={message(2)} />);
    expect(view.container).toBeEmptyDOMElement();
  });
});
