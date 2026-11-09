import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { apiError, json, renderSettings, type Call } from "../settings/testing";
import { chatBase, ORG, TS } from "./fixtures";
import type { DirectChatMessage } from "@openfox/api";
const chat = {
  id: "d1",
  peer: { id: "u2", name: "Carol" },
  last_seq: 0,
  read_seq: 0,
  unread_count: 0,
  last_body: "",
  updated_at: TS,
};
const base = `${ORG}/direct-chats`;
const msg = (seq: number, body: string, author = "u2"): DirectChatMessage => ({
  id: `m${seq}`,
  seq,
  body,
  author_id: author,
  created_at: TS,
  attachments: [],
});
function open(
  route: (c: Call) => Response | undefined = () => undefined,
  path = "/o/o1/chat?user=u2",
) {
  return renderSettings(
    path,
    chatBase((c) => {
      const response = route(c);
      if (response) return response;
      if (c.path === base)
        return c.method === "POST"
          ? json(200, chat)
          : json(200, { chats: [], next_before: "" });
      if (c.path === `${base}/d1`) return json(200, chat);
      if (c.path === `${base}/d1/messages`)
        return json(200, { messages: [], next_before: 0 });
      if (c.path === `${base}/d1/read`) return json(204);
      return undefined;
    }),
  );
}
afterEach(() => {
  localStorage.clear();
  vi.restoreAllMocks();
});
describe("human direct chat", () => {
  it("retains failed text and reuses its message ID on retry, without dispatching Agent runs", async () => {
    const messages: DirectChatMessage[] = [];
    let attempts = 0;
    const calls = open((c) => {
      if (c.path === `${base}/d1/messages`) {
        if (c.method === "GET") return json(200, { messages, next_before: 0 });
        if (++attempts === 1) return apiError(503, "UNAVAILABLE");
        const m = msg(1, (c.body as { body: string }).body, "u1");
        messages.push(m);
        return json(200, m);
      }
      return undefined;
    });
    const input = await screen.findByRole("textbox", { name: "发送消息…" });
    expect(screen.getByRole("button", { name: "发送消息" })).toBeDisabled();
    await userEvent.type(input, "hello{Enter}");
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "输入内容已保留",
    );
    expect(input).toHaveValue("hello");
    await userEvent.click(screen.getByRole("button", { name: "发送消息" }));
    await waitFor(() => expect(input).toHaveValue(""));
    expect(
      await within(screen.getByRole("log")).findByText("hello"),
    ).toBeInTheDocument();
    const sends = calls.filter(
      (c) => c.method === "POST" && c.path.endsWith("/messages"),
    );
    expect(sends).toHaveLength(2);
    expect(sends[0]!.body).toEqual(sends[1]!.body);
    expect(
      calls.some((c) => c.method === "POST" && c.path.includes("/runs")),
    ).toBe(false);
  });
  it("does not send during IME composition or Shift+Enter; sends attachment-only messages", async () => {
    const calls = open((c) =>
      c.path.endsWith("/attachments")
        ? json(201, {
            id: "f1",
            name: "note.txt",
            content_type: "text/plain",
            size_bytes: 3,
            previewable: false,
          })
        : c.path.endsWith("/messages") && c.method === "POST"
          ? json(200, msg(1, "", "u1"))
          : undefined,
    );
    const box = await screen.findByRole("textbox", { name: "发送消息…" });
    fireEvent.change(box, { target: { value: "中文" } });
    fireEvent.keyDown(box, { key: "Enter", isComposing: true });
    fireEvent.keyDown(box, { key: "Enter", shiftKey: true });
    expect(
      calls.filter((c) => c.method === "POST" && c.path.endsWith("/messages")),
    ).toHaveLength(0);
    fireEvent.change(box, { target: { value: "" } });
    await userEvent.upload(
      screen.getByLabelText("添加附件", { selector: "input" }),
      new File(["abc"], "note.txt", { type: "text/plain" }),
    );
    expect(await screen.findByText("note.txt")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "发送消息" }));
    await waitFor(() =>
      expect(
        calls.find((c) => c.method === "POST" && c.path.endsWith("/messages"))
          ?.body,
      ).toMatchObject({ body: "", attachment_ids: ["f1"] }),
    );
  });
  it("paginates older history and only marks read while the window is foreground", async () => {
    vi.spyOn(document, "hasFocus").mockReturnValue(false);
    const calls = open(
      (c) =>
        c.path.endsWith("/messages")
          ? json(
              200,
              c.query.get("before")
                ? { messages: [msg(1, "older")], next_before: 0 }
                : { messages: [msg(2, "latest")], next_before: 2 },
            )
          : undefined,
      "/o/o1/chat/direct/d1",
    );
    expect(await screen.findByText("latest")).toBeInTheDocument();
    expect(calls.some((c) => c.path.endsWith("/read"))).toBe(false);
    await userEvent.click(
      screen.getByRole("button", { name: "加载更早的内容" }),
    );
    expect(await screen.findByText("older")).toBeInTheDocument();
    expect(
      within(screen.getByRole("log"))
        .getAllByRole("article")
        .map((e) => e.textContent),
    ).toEqual([
      expect.stringContaining("older"),
      expect.stringContaining("latest"),
    ]);
    vi.spyOn(document, "hasFocus").mockReturnValue(true);
    fireEvent(window, new Event("focus"));
    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.path.endsWith("/read") && (c.body as { seq: number }).seq === 2,
        ),
      ).toBe(true),
    );
  });
  it("rejects self chat on send from the draft entry", async () => {
    open(
      (c) =>
        c.path === base && c.method === "POST"
          ? apiError(400, "DIRECT_CHAT_SELF")
          : undefined,
      "/o/o1/chat?user=u1",
    );
    const box = await screen.findByRole("textbox", { name: "发送消息…" });
    await userEvent.type(box, "self{Enter}");
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "不能和自己发起对话",
    );
    expect(box).toHaveValue("");
  });

  it("opens a human conversation from the new conversation picker", async () => {
    open(
      (c) =>
        c.path === `${ORG}/contacts`
          ? json(200, {
              groups: [
                {
                  kind: "HUMAN",
                  items: [
                    { kind: "user", id: "u1", name: "Me" },
                    { kind: "user", id: "u2", name: "Carol" },
                  ],
                },
              ],
              truncated: false,
            })
          : undefined,
      "/o/o1/chat",
    );
    await userEvent.click(
      await screen.findByRole("button", { name: "新建对话" }),
    );
    await userEvent.type(
      await screen.findByRole("textbox", { name: "搜索成员、Agent 或小队" }),
      "Carol",
    );
    await userEvent.click(
      await screen.findByRole("option", { name: /^Carol/ }),
    );
    expect(
      await screen.findByRole("textbox", { name: "发送消息…" }),
    ).toBeInTheDocument();
  });
});
