import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, useLocation, useNavigate } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { App, createQueryClient } from "../App";
import { readNavMemory, recordNav } from "../shell/navMemory";
import { backend, json, type Override } from "../tasks/test-backend";
import { chatBase, message, summary, ORG } from "./fixtures";

const ordinary = summary("c1", { title: "普通会话" });
const taskConversation = summary("c9", {
  title: null,
  task_id: "task-1",
  task: { id: "task-1", display_id: "OPEN-1", title: "任务标题 1" },
});
const taskPath = "/o/o1/tasks/OPEN-1";
const panelPath = `${taskPath}?view=chat&conversationId=c9`;

function Probe() {
  const location = useLocation();
  const navigate = useNavigate();
  return (
    <>
      <output data-testid="location">
        {location.pathname + location.search}
      </output>
      <button onClick={() => navigate(-1)}>history-back</button>
    </>
  );
}

function renderAt(path: string, extra?: Override) {
  const server = backend(
    chatBase((call) => {
      const custom = extra?.(call);
      if (custom) return custom;
      if (call.path === `${ORG}/conversations` && call.method === "GET")
        return json(200, {
          conversations: call.query.has("task_id")
            ? [taskConversation]
            : [ordinary, taskConversation],
          next_cursor: null,
        });
      if (call.path === `${ORG}/conversations/c1`) return json(200, ordinary);
      if (call.path === `${ORG}/conversations/c9`)
        return json(200, taskConversation);
      if (call.path.endsWith("/messages") && call.method === "GET")
        return json(200, { messages: [], next_cursor: 0 });
      if (call.path.endsWith("/artifacts")) return json(200, { artifacts: [] });
      return undefined;
    }),
  );
  const client = createQueryClient();
  client.setDefaultOptions({ queries: { retry: false } });
  render(
    <MemoryRouter initialEntries={[path]}>
      <App api={server.api} queryClient={client} locale="zh-CN" />
      <Probe />
    </MemoryRouter>,
  );
  return server;
}

const where = () => screen.getByTestId("location").textContent;
const panel = () => screen.findByRole("complementary", { name: "AI 面板" });
const rail = () => screen.getByRole("navigation", { name: "主导航" });

beforeEach(() => localStorage.setItem("openfox:onboarding:o1", "{}"));
afterEach(() => {
  localStorage.clear();
  sessionStorage.clear();
});

describe("普通 / 任务对话导航", () => {
  it.each([ordinary, taskConversation])(
    "从只有会话 ID 的收件箱通知打开 $id 时保留首页和返回路径",
    async ({ id, task }) => {
      const user = userEvent.setup();
      const inbox = "/o/o1/inbox";
      const previousChat = "/o/o1/chat?agent=a1";
      recordNav("o1", previousChat);
      renderAt(inbox, (call) => {
        if (call.path === `${ORG}/inbox`)
          return json(200, {
            groups: [
              {
                id,
                notification_id: "n1",
                kind: "REPLY",
                conversation_id: id,
                actor_name: "通知来源",
                task_type_icon: null,
                safe_summary: "",
                count: 1,
                unread: false,
                latest_at: ordinary.created_at,
              },
            ],
            counts: {
              inbox: 1,
              inbox_unread: 0,
              inbox_important_unread: 0,
              inbox_other_unread: 0,
              later: 0,
              cleared: 0,
            },
          });
        return undefined;
      });
      await user.click(await screen.findByRole("button", { name: "通知来源" }));
      if (task) await panel();
      else await screen.findByRole("textbox", { name: "输入消息" });
      expect(
        within(rail()).getByRole("link", { name: "首页" }),
      ).toHaveAttribute("aria-current", "page");
      expect(readNavMemory("o1", "chat")).toBe(previousChat);
      const target = new URL(where()!, "http://localhost");
      expect(target.pathname).toBe(task ? taskPath : "/o/o1/chat/c1");
      if (task) expect(target.searchParams.get("conversationId")).toBe(id);
      await user.click(screen.getByRole("button", { name: "history-back" }));
      await screen.findByRole("list", { name: "通知列表" });
      expect(where()).toBe(inbox);
    },
  );

  it.each(["home", "chat"] as const)(
    "%s 入口：普通聊天与任务分栏切换，保留选中项和后退历史",
    async (entry) => {
      const user = userEvent.setup();
      renderAt(entry === "home" ? "/o/o1/inbox" : "/o/o1/chat");
      const normal = await screen.findByRole("link", { name: /普通会话/ });
      await user.click(normal);
      await screen.findByRole("textbox", { name: "输入消息" });
      const ordinaryPath = `/o/o1/chat/c1${entry === "home" ? "?rail=home" : ""}`;
      expect(where()).toBe(ordinaryPath);
      expect(
        screen.queryByRole("complementary", { name: "AI 面板" }),
      ).not.toBeInTheDocument();
      await user.click(screen.getByRole("link", { name: /任务标题 1/ }));
      await panel();
      expect(where()).toBe(`${panelPath}&rail=${entry}`);
      expect(
        await screen.findByRole("textbox", { name: "任务标题" }),
      ).toHaveValue("任务标题 1");
      expect(screen.getByRole("link", { name: /任务标题 1/ })).toHaveAttribute(
        "aria-current",
        "page",
      );
      expect(
        within(rail()).getByRole("link", {
          name: entry === "home" ? "首页" : "对话",
        }),
      ).toHaveAttribute("aria-current", "page");
      expect(readNavMemory("o1", entry)).toBe(`${panelPath}&rail=${entry}`);
      await user.click(screen.getByRole("button", { name: "history-back" }));
      await waitFor(() => expect(where()).toBe(ordinaryPath));
      await screen.findByRole("textbox", { name: "输入消息" });
      expect(
        screen.queryByRole("complementary", { name: "AI 面板" }),
      ).not.toBeInTheDocument();
    },
  );

  it("多会话模式也打开任务面板；关闭后重开不切换页签、不丢草稿", async () => {
    localStorage.setItem("openfox:chat-mode", "sessions");
    const user = userEvent.setup();
    renderAt("/o/o1/chat");
    await user.click(await screen.findByRole("link", { name: /任务标题 1/ }));
    const aside = await panel();
    await user.type(
      await within(aside).findByRole("textbox", { name: "输入消息" }),
      "保留任务草稿",
    );
    await user.click(
      within(aside).getByRole("button", { name: "关闭 AI 面板" }),
    );
    expect(where()).toBe(`${taskPath}?rail=chat`);
    expect(within(rail()).getByRole("link", { name: "对话" })).toHaveAttribute(
      "aria-current",
      "page",
    );
    await user.click(screen.getByRole("tab", { name: /对话记录/ }));
    await user.click(
      within(await screen.findByRole("list", { name: "对话记录" })).getByRole(
        "button",
        { name: "打开会话 熊猫" },
      ),
    );
    const reopened = await panel();
    expect(new URLSearchParams(where()!.split("?")[1]).get("rail")).toBe(
      "chat",
    );
    expect(
      await within(reopened).findByRole("textbox", { name: "输入消息" }),
    ).toHaveValue("保留任务草稿");
  });

  it("标题切换到任务会话，仍保留首页入口", async () => {
    const user = userEvent.setup();
    renderAt("/o/o1/chat/c1?rail=home");
    await user.click(await screen.findByRole("button", { name: "切换会话" }));
    await user.click(
      await within(screen.getByRole("menu", { name: "切换会话" })).findByRole(
        "menuitemradio",
        { name: /任务标题 1/ },
      ),
    );
    await panel();
    expect(where()).toBe(`${panelPath}&rail=home`);
  });

  it("Slack 模式的 Agent 最近任务会话打开分栏并选中该 Agent", async () => {
    localStorage.setItem("openfox:chat-mode", "organized");
    const user = userEvent.setup();
    renderAt("/o/o1/chat");
    const nav = await screen.findByRole("navigation", { name: "对话" });
    const agent = await within(nav).findByRole("button", {
      name: /熊猫/,
    });
    await user.click(agent);
    await panel();
    expect(where()).toBe(`${panelPath}&rail=chat`);
    expect(agent).toHaveAttribute("aria-current", "page");
  });

  it("普通对话转为任务后继续打开同一会话，首页上下文不变", async () => {
    const user = userEvent.setup();
    let converted = false;
    const convertedSummary = {
      ...ordinary,
      task_id: taskConversation.task_id,
      task: taskConversation.task,
    };
    const server = renderAt("/o/o1/chat/c1?rail=home", (call) => {
      if (
        call.method === "POST" &&
        call.path === `${ORG}/conversations/c1/convert-to-task`
      ) {
        converted = true;
        return json(200, convertedSummary);
      }
      if (converted && call.path === `${ORG}/conversations/c1`)
        return json(200, convertedSummary);
      return undefined;
    });
    await user.click(await screen.findByRole("button", { name: "更多操作" }));
    await user.click(screen.getByRole("menuitem", { name: "转为任务" }));
    await panel();
    expect(where()).toBe(`${taskPath}?rail=home&view=chat&conversationId=c1`);
    expect(
      server.calls.filter(
        (call) =>
          call.method === "POST" && call.path.endsWith("/convert-to-task"),
      ),
    ).toHaveLength(1);
    expect(
      server.calls.some(
        (call) =>
          call.method === "POST" && call.path === `${ORG}/conversations`,
      ),
    ).toBe(false);
  });

  it("历史任务会话链接按 replace 规范化，保留产物参数并向原会话发送", async () => {
    const user = userEvent.setup();
    const server = renderAt("/o/o1/chat/c9?rail=home&artifact=list", (call) => {
      if (
        call.method === "POST" &&
        call.path === `${ORG}/conversations/c9/messages`
      ) {
        const body = call.body as { body: string; client_message_id: string };
        return json(201, message(1, { ...body, conversation_id: "c9" }));
      }
      return undefined;
    });
    const aside = await panel();
    const search = new URLSearchParams(where()!.split("?")[1]);
    expect(search.get("artifact")).toBe("list");
    expect(search.get("conversationId")).toBe("c9");
    expect(search.get("rail")).toBe("home");
    const box = await within(aside).findByRole("textbox", { name: "输入消息" });
    await user.type(box, "继续任务{Enter}");
    await waitFor(() => expect(box).toHaveValue(""));
    const posts = server.calls.filter(
      (call) => call.method === "POST" && call.path.endsWith("/messages"),
    );
    expect(posts).toHaveLength(1);
    expect(posts[0]!.path).toBe(`${ORG}/conversations/c9/messages`);
    expect(posts[0]!.body).toMatchObject({
      body: "继续任务",
      mentions: [{ agent_id: "a1" }],
    });
    expect(
      server.calls.some(
        (call) =>
          call.method === "POST" && call.path === `${ORG}/conversations`,
      ),
    ).toBe(false);
  });

  it("已删除任务的会话仍能查看聊天，避免无效跳转", async () => {
    renderAt("/o/o1/chat/c9", (call) =>
      call.path === `${ORG}/conversations/c9`
        ? json(200, summary("c9", { task_id: "deleted-task", task: null }))
        : undefined,
    );
    await screen.findByRole("textbox", { name: "输入消息" });
    expect(where()).toBe("/o/o1/chat/c9");
    expect(
      screen.queryByRole("complementary", { name: "AI 面板" }),
    ).not.toBeInTheDocument();
  });
});
