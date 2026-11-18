import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";
import { agent } from "../agents/fixtures";
import { apiError, findCall, json, renderSettings } from "../settings/testing";
import { chatBase, message, ORG, run, summary, TS } from "./fixtures";

const assistant = agent("as1", {
  builtin_kind: "ASSISTANT",
  visibility: "PRIVATE",
});
const session = {
  id: "c1",
  space_id: "s1",
  agent_id: "as1",
  title: "待交接会话",
  lifecycle: "OPEN",
  version: 1,
  created_at: TS,
  updated_at: TS,
};
const conversation = summary("c1", { agent_id: "as1", title: "待交接会话" });

afterEach(() => localStorage.clear());

describe("小助手会话转让", () => {
  it.each(["u1", "u2"])(
    "历史失败运行不能由转让后的身份重试：%s",
    async (ownerId) => {
      const transferred = summary("c1", {
        owner_user_id: ownerId,
        agent_id: "as1",
        visibility: "SHARED",
        agent: {
          ...summary("c1").agent,
          id: "as1",
          space_id: "s2",
          builtin_kind: "ASSISTANT",
        },
      });
      const calls = renderSettings(
        "/o/o1/chat/c1",
        chatBase((call) => {
          if (call.path === `${ORG}/conversations/c1`)
            return json(200, transferred);
          if (call.path === `${ORG}/conversations/c1/messages`)
            return json(200, {
              messages: [
                message(1, { run_id: "old-run" }),
                message(2, {
                  author_kind: "SYSTEM",
                  run_id: "old-run",
                  body: "历史失败",
                }),
              ],
              next_cursor: 0,
            });
          if (call.path === `${ORG}/runs/old-run`)
            return json(
              200,
              run("old-run", {
                state: "FAILED",
                failure_code: "RATE_LIMITED",
                finished_at: TS,
                agent_id: "source-assistant",
              }),
            );
          if (call.path === `${ORG}/spaces/s2/agents/as1`)
            return json(200, {
              ...assistant,
              space_id: "s2",
              owner_user_id: ownerId,
            });
          return undefined;
        }),
      );
      await screen.findByText("历史失败");
      await screen.findByText("执行失败");
      expect(screen.queryByRole("button", { name: "重试" })).toBeNull();
      await waitFor(() =>
        expect(
          findCall(calls, "GET", `${ORG}/spaces/s2/agents/as1`),
        ).toBeTruthy(),
      );
      expect(
        findCall(calls, "GET", `${ORG}/spaces/s1/agents/as1`),
      ).toBeUndefined();
    },
  );

  it("候选人准备条件、搜索、确认与失败重试", async () => {
    let attempts = 0;
    const calls = renderSettings(
      "/o/o1/assistant?session=c1",
      chatBase((call) => {
        if (call.path === `${ORG}/assistant`) return json(200, { assistant });
        if (call.path === `${ORG}/assistant/sessions`)
          return json(200, { sessions: [session] });
        if (call.path === `${ORG}/conversations/c1`)
          return json(200, conversation);
        if (call.path.endsWith("/offer-candidates"))
          return json(
            200,
            call.query.get("q") === "none"
              ? []
              : [
                  { id: "u2", name: "Ready" },
                  {
                    id: "u3",
                    name: "Not ready",
                    unavailable_reason: "ASSISTANT_TRANSFER_NOT_READY",
                  },
                ],
          );
        if (call.path === `${ORG}/conversations/c1/offers`)
          return ++attempts === 1
            ? apiError(409, "ASSISTANT_TRANSFER_NOT_READY")
            : json(204);
        return undefined;
      }),
    );
    const user = userEvent.setup();
    await screen.findByRole("button", { name: "会话记录" });
    await user.click(await screen.findByRole("button", { name: "更多操作" }));
    await user.click(
      screen.getByRole("menuitem", { name: "转让该对话给其他人" }),
    );
    const dialog = await screen.findByRole("dialog", { name: "转让对话" });
    expect(
      await within(dialog).findByRole("button", { name: "Not ready" }),
    ).toBeDisabled();
    expect(
      within(dialog).getByRole("button", { name: "下一步" }),
    ).toBeDisabled();
    const search = within(dialog).getByRole("textbox", { name: "搜索成员" });
    await user.type(search, "none");
    await within(dialog).findByText("没有可转让的成员");
    await user.clear(search);
    await user.click(
      await within(dialog).findByRole("button", { name: "Ready" }),
    );
    await user.click(within(dialog).getByRole("button", { name: "下一步" }));
    expect(screen.queryByRole("checkbox")).toBeNull();
    const confirm = await screen.findByRole("dialog", { name: "确认转让" });
    expect(
      within(confirm).getByText(/电脑权限和工作目录不会转交/),
    ).toBeInTheDocument();
    await user.type(
      within(confirm).getByRole("textbox", { name: "附加消息（可不填）" }),
      "请接手",
    );
    await user.click(within(confirm).getByRole("button", { name: "转让" }));
    await waitFor(() => expect(attempts).toBe(1));
    expect(confirm).toBeInTheDocument();
    await user.click(within(confirm).getByRole("button", { name: "转让" }));
    await waitFor(() =>
      expect(screen.queryByRole("dialog", { name: "确认转让" })).toBeNull(),
    );
    expect(
      findCall(calls, "POST", `${ORG}/conversations/c1/offers`)?.body,
    ).toEqual({
      kind: "TRANSFER",
      recipient_user_id: "u2",
      note: "请接手",
      transfer_assignee: false,
    });
    const sends = calls.filter(
      (call) => call.path.endsWith("/offers") && call.method === "POST",
    );
    expect(sends).toHaveLength(2);
    expect(sends[1]!.headers.get("Idempotency-Key")).toBe(
      sends[0]!.headers.get("Idempotency-Key"),
    );
  });

  it.each([true, false])(
    "私聊请求接受=%s 后刷新卡片；接受后打开小助手会话",
    async (accept) => {
      let status = "PENDING";
      const chat = {
        id: "d1",
        peer: { id: "u2", name: "Sender" },
        last_seq: 1,
        read_seq: 0,
        unread_count: 1,
        last_body: "",
        updated_at: TS,
      };
      const calls = renderSettings(
        "/o/o1/chat/direct/d1",
        chatBase((call) => {
          if (call.path === `${ORG}/direct-chats/d1`) return json(200, chat);
          if (call.path === `${ORG}/direct-chats/d1/messages`)
            return json(200, {
              messages: [
                {
                  id: "offer1",
                  seq: 1,
                  body: "",
                  author_id: "u2",
                  created_at: TS,
                  attachments: [],
                  offer: {
                    id: "offer1",
                    kind: "TRANSFER",
                    status,
                    title: "待交接会话",
                    note: "",
                    transfer_assignee: false,
                    conversation_id: "c1",
                    task_display_id: "",
                    assistant: true,
                    can_open: status === "ACCEPTED",
                  },
                },
              ],
              next_before: 0,
            });
          if (call.path === `${ORG}/direct-chats/d1/read`) return json(204);
          if (call.path === `${ORG}/conversation-offers/offer1`) {
            status = accept ? "ACCEPTED" : "DECLINED";
            return json(204);
          }
          if (call.path === `${ORG}/assistant`) return json(200, { assistant });
          if (call.path === `${ORG}/assistant/sessions`)
            return json(200, { sessions: [session] });
          if (call.path === `${ORG}/conversations/c1`)
            return json(200, conversation);
          return undefined;
        }),
      );
      await userEvent.click(
        await screen.findByRole("button", { name: accept ? "接受" : "拒绝" }),
      );
      await waitFor(() =>
        expect(
          findCall(calls, "PUT", `${ORG}/conversation-offers/offer1`)?.body,
        ).toEqual({ accept }),
      );
      if (accept) {
        await userEvent.click(
          await screen.findByRole("button", { name: "打开对话" }),
        );
        await waitFor(() =>
          expect(
            findCall(calls, "GET", `${ORG}/assistant/sessions`),
          ).toBeTruthy(),
        );
        expect(
          await screen.findByRole("button", { name: "会话记录" }),
        ).toHaveTextContent("待交接会话");
      } else {
        await waitFor(() =>
          expect(screen.queryByRole("button", { name: "接受" })).toBeNull(),
        );
        expect(screen.queryByRole("button", { name: "打开对话" })).toBeNull();
      }
    },
  );
});
