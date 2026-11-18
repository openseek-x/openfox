import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, test, vi } from "vitest";
import { json, renderSettings } from "../settings/testing";
import { ORG, TS, chatBase, message, summary } from "./fixtures";
import { searchConversationPath } from "../search/conversationResult";

test.each(["agent", "direct"] as const)(
  "opens the exact %s message without walking back from the latest page",
  async (kind) => {
    const direct = kind === "direct";
    const chatId = direct ? "d1" : "c1";
    const path = searchConversationPath("o1", {
      id: "m70",
      title: "hit",
      subtitle: "snippet",
      message_id: "m70",
      conversation_id: chatId,
      conversation_kind: kind,
    });
    const base = direct ? `${ORG}/direct-chats/d1` : `${ORG}/conversations/c1`;
    const centered = vi.fn();
    const original = HTMLElement.prototype.scrollIntoView;
    HTMLElement.prototype.scrollIntoView = function () {
      if (this.dataset.messageId) centered(this.dataset.messageId);
    };
    try {
      const calls = renderSettings(
        path,
        chatBase((c) => {
          if (c.path === base)
            return json(
              200,
              direct
                ? {
                    id: chatId,
                    peer: { id: "u2", name: "Carol" },
                    read_seq: 0,
                    last_seq: 2000,
                    last_body: "last",
                    updated_at: TS,
                    unread_count: 10,
                  }
                : summary("c1"),
            );
          if (c.path === base + "/messages") {
            const seqs = c.query.has("around")
              ? [69, 70, 71]
              : c.query.has("after")
                ? [72, 73]
                : [2000];
            const messages = seqs.map((i) =>
              direct
                ? {
                    id: "m" + i,
                    seq: i,
                    body: "消息 " + i,
                    author_id: "u2",
                    attachments: [],
                    created_at: TS,
                  }
                : message(i),
            );
            return json(200, {
              messages,
              next_cursor: 0,
              next_before: 0,
              next_after: 0,
            });
          }
          return undefined;
        }),
      );
      expect(await screen.findByText("消息 70")).toBeVisible();
      expect(centered).toHaveBeenCalledWith("m70");
      expect(
        calls
          .filter((c) => c.path === base + "/messages")
          .every((c) => c.query.get("around") === "m70"),
      ).toBe(true);
      expect(calls.some((c) => c.path === base + "/read")).toBe(false);
      await userEvent.click(
        screen.getByRole("button", { name: "加载更新的消息" }),
      );
      expect(await screen.findByText("消息 73")).toBeVisible();
      expect(screen.getByText("消息 70")).toBeVisible();
      expect(centered).toHaveBeenCalledTimes(1);
      await userEvent.click(screen.getByRole("button", { name: "回到底部" }));
      await waitFor(() =>
        expect(screen.queryByText("消息 70")).not.toBeInTheDocument(),
      );
      expect(await screen.findByText("消息 2000")).toBeVisible();
    } finally {
      HTMLElement.prototype.scrollIntoView = original;
    }
  },
);

test("task conversations keep the task panel when jumping to a message", () => {
  expect(
    searchConversationPath("o1", {
      id: "m1",
      title: "hit",
      subtitle: "snippet",
      message_id: "m1",
      conversation_kind: "agent",
      conversation_id: "c1",
      task_display_id: "OP-8",
    }),
  ).toBe("/o/o1/tasks/OP-8?view=chat&conversationId=c1&rail=chat&message=m1");
});
