import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, useNavigate } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { App, createQueryClient } from "../App";
import { json as settingsJson, renderSettings } from "../settings/testing";
import { backend, json, ORG, type Override } from "../tasks/test-backend";
import {
  MAX_DRAFTS,
  clearDraft,
  newDraftKey,
  readDraft,
  resetDrafts,
  saveDraft,
} from "./drafts";
import { chatBase, message, summary } from "./fixtures";

// 输入框草稿按会话隔离，切换会话不会把 A 的草稿带到 B。

const o = `/v1/organizations/${ORG}`;
const page = `/o/${ORG}/tasks/OPEN-1`;
const chatOf = (id: string) => `${page}?view=chat&conversationId=${id}`;

/** 直接改地址里的 conversationId（等同于点会话标签 / 执行区块的会话行） */
function Switcher() {
  const navigate = useNavigate();
  return (
    <>
      <button type="button" onClick={() => navigate(chatOf("c9"))}>
        go-c9
      </button>
      <button type="button" onClick={() => navigate(chatOf("c7"))}>
        go-c7
      </button>
    </>
  );
}

/** 任务 1 绑定两个会话 c9、c7；发到 c9 的消息成功返回 */
const twoConversations: Override = (call) => {
  if (call.method === "GET" && call.path === `${o}/conversations`)
    return json(200, {
      conversations: call.query.get("task_id")
        ? [
            summary("c9", { task_id: "task-1" }),
            summary("c7", { task_id: "task-1" }),
          ]
        : [],
      next_cursor: null,
    });
  const one = /\/conversations\/(c9|c7)$/.exec(call.path);
  if (call.method === "GET" && one)
    return json(200, summary(one[1]!, { task_id: "task-1" }));
  if (call.method === "POST" && call.path === `${o}/conversations/c9/messages`)
    return json(
      201,
      message(11, {
        conversation_id: "c9",
        body: (call.body as { body: string }).body,
      }),
    );
  if (call.method === "GET" && call.path.endsWith("/messages"))
    return json(200, { messages: [], next_cursor: 0 });
  return undefined;
};

beforeEach(() => {
  localStorage.setItem(`openfox:onboarding:${ORG}`, JSON.stringify({}));
});
afterEach(() => {
  resetDrafts();
  localStorage.clear();
  sessionStorage.clear();
});

describe("草稿存储", () => {
  it("空草稿删除键；新会话键按 Agent 与小队区分；超过上限丢弃最早的", () => {
    saveDraft("c1", { body: "你好", picked: [], files: [] });
    expect(readDraft("c1")?.body).toBe("你好");
    saveDraft("c1", { body: "", picked: [], files: [] });
    expect(readDraft("c1")).toBeUndefined();
    expect(newDraftKey("a1", false)).not.toBe(newDraftKey("a2", false));
    expect(newDraftKey("a1", false)).not.toBe(newDraftKey("a1", true));
    for (let i = 0; i <= MAX_DRAFTS; i += 1)
      saveDraft(`k${i}`, { body: `草稿 ${i}`, picked: [], files: [] });
    expect(readDraft("k0")).toBeUndefined();
    expect(readDraft(`k${MAX_DRAFTS}`)?.body).toBe(`草稿 ${MAX_DRAFTS}`);
    clearDraft(`k${MAX_DRAFTS}`);
    expect(readDraft(`k${MAX_DRAFTS}`)).toBeUndefined();
  });
});

describe("任务全页并排会话的草稿隔离", () => {
  it("A 写草稿 → 切 B 为空 → B 写草稿 → 切回 A 恢复 → 发送 A 后 A 清空、B 仍在", async () => {
    const user = userEvent.setup();
    const server = backend(twoConversations);
    const queryClient = createQueryClient();
    queryClient.setDefaultOptions({ queries: { retry: false } });
    render(
      <MemoryRouter initialEntries={[chatOf("c9")]}>
        <App api={server.api} queryClient={queryClient} locale="zh-CN" />
        <Switcher />
      </MemoryRouter>,
    );
    const aside = await screen.findByRole("complementary", { name: "AI 面板" });
    const box = () => within(aside).getByRole("textbox", { name: "输入消息" });
    const send = () => within(aside).getByRole("button", { name: "发送" });
    await waitFor(() => expect(box()).toBeEnabled());

    await user.type(box(), "给 A 的草稿");
    expect(send()).toBeEnabled();

    await user.click(screen.getByRole("button", { name: "go-c7" }));
    await waitFor(() => expect(box()).toHaveValue(""));
    expect(send()).toBeDisabled();
    await user.type(box(), "给 B 的草稿");

    await user.click(screen.getByRole("button", { name: "go-c9" }));
    await waitFor(() => expect(box()).toHaveValue("给 A 的草稿"));

    await user.click(send());
    await waitFor(() =>
      expect(server.find("POST", "/c9/messages")).toHaveLength(1),
    );
    expect(server.find("POST", "/c9/messages")[0]!.body).toMatchObject({
      body: "给 A 的草稿",
    });
    expect(server.find("POST", "/c7/messages")).toHaveLength(0);
    await waitFor(() => expect(box()).toHaveValue(""));

    await user.click(screen.getByRole("button", { name: "go-c7" }));
    await waitFor(() => expect(box()).toHaveValue("给 B 的草稿"));
    await user.click(screen.getByRole("button", { name: "go-c9" }));
    await waitFor(() => expect(box()).toHaveValue(""));
  });
});

describe("独立对话页的草稿隔离", () => {
  it("从 /chat/c1 切到 /chat/c2 输入框为空，切回 c1 恢复草稿", async () => {
    const user = userEvent.setup();
    renderSettings(
      "/o/o1/chat/c1",
      chatBase((call) => {
        if (call.path === `${o}/conversations`)
          return settingsJson(200, {
            conversations: [
              summary("c1", { title: "第一个会话" }),
              summary("c2", { title: "第二个会话" }),
            ],
            next_cursor: null,
          });
        const one = /\/conversations\/(c1|c2)$/.exec(call.path);
        if (call.method === "GET" && one)
          return settingsJson(
            200,
            summary(one[1]!, {
              title: one[1] === "c1" ? "第一个会话" : "第二个会话",
            }),
          );
        if (call.method === "GET" && call.path.endsWith("/messages"))
          return settingsJson(200, { messages: [], next_cursor: 0 });
        return undefined;
      }),
    );
    const box = () => screen.getByRole("textbox", { name: "输入消息" });
    await waitFor(() => expect(box()).toBeEnabled());
    await user.type(box(), "c1 的草稿");
    const nav = screen.getByRole("navigation", { name: "对话" });
    await user.click(await within(nav).findByText("第二个会话"));
    await waitFor(() => expect(box()).toHaveValue(""));
    await user.click(within(nav).getByText("第一个会话"));
    await waitFor(() => expect(box()).toHaveValue("c1 的草稿"));
  });
});
