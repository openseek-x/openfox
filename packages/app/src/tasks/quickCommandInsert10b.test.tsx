import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, useLocation } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import type { ConversationSummary, QuickCommand } from "@openfox/api";

import { App, createQueryClient } from "../App";
import { agent } from "../agents/fixtures";
import { resetDrafts } from "../conversations/drafts";
import { summary } from "../conversations/fixtures";
import { backend, json, ORG, SPACE, type Override } from "./test-backend";

// 召唤菜单快捷指令右侧的「插入到输入框」：只填进输入框，不发送。
// 已打开并排会话时追加到它的草稿；没有时新建挂任务的会话（不发消息）并打开，草稿里带 @Agent（小队不带）。

function Where() {
  const location = useLocation();
  return (
    <>
      <output data-testid="where">{location.pathname}</output>
      <output data-testid="search">{location.search}</output>
    </>
  );
}

const o = `/v1/organizations/${ORG}`;
const drawer = `/o/${ORG}/spaces/${SPACE}/tasks?task=OPEN-1`;
const page = `/o/${ORG}/tasks/OPEN-1`;
const panelOf = (id: string) => `${page}?view=chat&conversationId=${id}`;

function command(id: string, extra: Partial<QuickCommand> = {}): QuickCommand {
  return {
    id,
    space_id: SPACE,
    owner_user_id: "u1",
    visibility: "PRIVATE",
    name: `指令 ${id}`,
    icon_kind: null,
    icon: null,
    icon_url: null,
    agent_id: "a1",
    crew_id: null,
    target: {
      kind: "AGENT",
      id: "a1",
      name: "前端 Agent",
      avatar: null,
      available: true,
    },
    prompt: "请澄清本任务",
    position: 0,
    version: 1,
    can_edit: true,
    created_at: "2026-11-04T00:00:00Z",
    updated_at: "2026-11-04T00:00:00Z",
    ...extra,
  };
}

const crewCommand = command("s1", {
  name: "小队评审",
  visibility: "SHARED",
  prompt: "",
  agent_id: null,
  crew_id: "c1",
  target: {
    kind: "CREW",
    id: "c1",
    name: "前端小队",
    avatar: null,
    available: true,
  },
});

/** 任务 1 已绑定会话 c9（Agent a1 的单聊）；新建会话返回 c3 */
function insertBackend(commands: QuickCommand[]): Override {
  const conversation = (id: string): ConversationSummary =>
    summary(id, { task_id: "task-1", visibility: "SHARED" });
  return (call) => {
    if (call.path === `${o}/spaces/${SPACE}/agents`)
      return json(200, { agents: [agent("a1", { name: "前端 Agent" })] });
    if (call.path === `${o}/spaces/${SPACE}/quick-commands`)
      return json(200, { private: commands, shared: [] });
    if (call.method === "GET" && call.path === `${o}/conversations`)
      return json(200, {
        conversations: call.query.get("task_id") ? [conversation("c9")] : [],
        next_cursor: null,
      });
    if (call.method === "POST" && call.path === `${o}/conversations`)
      return json(201, conversation("c3"));
    const one = /\/conversations\/(c9|c3)$/.exec(call.path);
    if (call.method === "GET" && one) return json(200, conversation(one[1]!));
    if (call.method === "GET" && call.path.endsWith("/messages"))
      return json(200, { messages: [], next_cursor: 0 });
    return undefined;
  };
}

function renderAt(path: string, commands: QuickCommand[]) {
  const server = backend(insertBackend(commands));
  const queryClient = createQueryClient();
  queryClient.setDefaultOptions({ queries: { retry: false } });
  render(
    <MemoryRouter initialEntries={[path]}>
      <App api={server.api} queryClient={queryClient} locale="zh-CN" />
      <Where />
    </MemoryRouter>,
  );
  return server;
}

async function insert(user: ReturnType<typeof userEvent.setup>, name: string) {
  await screen.findByRole("textbox", { name: "任务标题" });
  await user.click(screen.getAllByRole("button", { name: "召唤 Agent" })[0]!);
  const menu = await screen.findByRole("menu", { name: "召唤 Agent" });
  await user.click(
    await within(menu).findByRole("button", {
      name: `把「${name}」插入到输入框`,
    }),
  );
}

const panelBox = async () => {
  const aside = await screen.findByRole("complementary", { name: "AI 面板" });
  return within(aside).findByRole("textbox", { name: "输入消息" });
};

beforeEach(() => {
  localStorage.setItem(`openfox:onboarding:${ORG}`, JSON.stringify({}));
});
afterEach(() => {
  resetDrafts();
  localStorage.clear();
});

describe("没有打开的会话", () => {
  it("新建挂任务的共享会话但不发消息，打开它，草稿是 @Agent + 提示词并聚焦输入框", async () => {
    const user = userEvent.setup();
    const server = renderAt(drawer, [command("p1", { name: "需求澄清" })]);
    await insert(user, "需求澄清");
    await waitFor(() =>
      expect(server.find("POST", "/conversations")).toHaveLength(1),
    );
    expect(server.find("POST", "/conversations")[0]!.body).toEqual({
      space_id: SPACE,
      task_id: "task-1",
      agent_id: "a1",
      visibility: "SHARED",
    });
    await waitFor(() =>
      expect(
        screen.getByTestId("where").textContent! +
          screen.getByTestId("search").textContent!,
      ).toBe(panelOf("c3")),
    );
    const box = await panelBox();
    await waitFor(() => expect(box).toHaveValue("@前端 Agent 请澄清本任务"));
    await waitFor(() => expect(box).toHaveFocus());
    // 只填进输入框，不发送
    expect(server.find("POST", "/messages")).toHaveLength(0);
  });

  it("小队指令：用 crew_id 建会话，空提示词用固定文案，不带提及", async () => {
    const user = userEvent.setup();
    const server = renderAt(drawer, [crewCommand]);
    await insert(user, "小队评审");
    await waitFor(() =>
      expect(server.find("POST", "/conversations")).toHaveLength(1),
    );
    expect(server.find("POST", "/conversations")[0]!.body).toMatchObject({
      crew_id: "c1",
      task_id: "task-1",
      visibility: "SHARED",
    });
    const box = await panelBox();
    await waitFor(() => expect(box).toHaveValue("请处理这个任务。"));
    expect(server.find("POST", "/messages")).toHaveLength(0);
  });
});

describe("任务全页已打开并排会话", () => {
  it("追加到该会话的草稿（空一行，不覆盖），聚焦输入框，不新建会话也不发送", async () => {
    const user = userEvent.setup();
    const server = renderAt(panelOf("c9"), [
      command("p1", { name: "需求澄清" }),
    ]);
    const box = await panelBox();
    await waitFor(() => expect(box).toBeEnabled());
    await user.type(box, "已有内容");
    await insert(user, "需求澄清");
    await waitFor(() =>
      expect(box).toHaveValue("已有内容\n\n@前端 Agent 请澄清本任务"),
    );
    await waitFor(() => expect(box).toHaveFocus());
    // 再插入一次：继续追加，已带过提及的不重复
    await insert(user, "需求澄清");
    await waitFor(() =>
      expect(box).toHaveValue(
        "已有内容\n\n@前端 Agent 请澄清本任务\n\n请澄清本任务",
      ),
    );
    expect(server.find("POST", "/conversations")).toHaveLength(0);
    expect(server.find("POST", "/messages")).toHaveLength(0);
    expect(screen.getByTestId("search")).toHaveTextContent(
      "?view=chat&conversationId=c9",
    );
  });

  it("插入后按发送：提及随消息发出", async () => {
    const user = userEvent.setup();
    const server = backend((call) => {
      if (
        call.method === "POST" &&
        call.path === `${o}/conversations/c9/messages`
      )
        return json(201, { id: "m1" });
      return insertBackend([command("p1", { name: "需求澄清" })])(call);
    });
    const queryClient = createQueryClient();
    queryClient.setDefaultOptions({ queries: { retry: false } });
    render(
      <MemoryRouter initialEntries={[panelOf("c9")]}>
        <App api={server.api} queryClient={queryClient} locale="zh-CN" />
      </MemoryRouter>,
    );
    const box = await panelBox();
    await waitFor(() => expect(box).toBeEnabled());
    await insert(user, "需求澄清");
    await waitFor(() => expect(box).toHaveValue("@前端 Agent 请澄清本任务"));
    const aside = screen.getByRole("complementary", { name: "AI 面板" });
    await user.click(within(aside).getByRole("button", { name: "发送" }));
    await waitFor(() =>
      expect(server.find("POST", "/conversations/c9/messages")).toHaveLength(1),
    );
    expect(
      server.find("POST", "/conversations/c9/messages")[0]!.body,
    ).toMatchObject({
      body: "@前端 Agent 请澄清本任务",
      mentions: [{ agent_id: "a1" }],
    });
  });

  it("按钮可用键盘操作：Tab 到按钮按回车只插入，不触发主项的发送", async () => {
    const user = userEvent.setup();
    const server = renderAt(panelOf("c9"), [
      command("p1", { name: "需求澄清" }),
    ]);
    const box = await panelBox();
    await waitFor(() => expect(box).toBeEnabled());
    await screen.findByRole("textbox", { name: "任务标题" });
    await user.click(screen.getAllByRole("button", { name: "召唤 Agent" })[0]!);
    const menu = await screen.findByRole("menu", { name: "召唤 Agent" });
    const button = await within(menu).findByRole("button", {
      name: "把「需求澄清」插入到输入框",
    });
    button.focus();
    await user.keyboard("{Enter}");
    await waitFor(() => expect(box).toHaveValue("@前端 Agent 请澄清本任务"));
    expect(server.find("POST", "/conversations")).toHaveLength(0);
    expect(server.find("POST", "/messages")).toHaveLength(0);
  });
});
