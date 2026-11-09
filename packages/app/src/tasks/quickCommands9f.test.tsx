import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, useLocation } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import type { QuickCommand } from "@openfox/api";

import { App, createQueryClient } from "../App";
import { agent, entry } from "../agents/fixtures";
import { summary } from "../conversations/fixtures";
import { reorderCommands } from "./detail/quickCommands/QuickCommandGroup";
import { backend, json, ORG, SPACE, type Override } from "./test-backend";

// 召唤菜单里的快捷指令：分组与空态、不可用置灰、发送流程、管理对话框（列表 / 新建 / 编辑 / 删除 / 排序）。

function Where() {
  const location = useLocation();
  return <output data-testid="search">{location.search}</output>;
}

function renderAt(override?: Override) {
  const server = backend(override);
  const queryClient = createQueryClient();
  queryClient.setDefaultOptions({ queries: { retry: false } });
  render(
    <MemoryRouter
      initialEntries={[`/o/${ORG}/spaces/${SPACE}/tasks?task=OPEN-1`]}
    >
      <App api={server.api} queryClient={queryClient} locale="zh-CN" />
      <Where />
    </MemoryRouter>,
  );
  return server;
}

const o = `/v1/organizations/${ORG}`;
const qc = `${o}/spaces/${SPACE}/quick-commands`;

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

interface State {
  private: QuickCommand[];
  shared: QuickCommand[];
  openSession?: boolean;
  messageFails?: boolean;
}

/** 有状态的快捷指令后端：列表 / 新建 / 修改 / 删除 / 排序，外加召唤与发送所需的接口 */
function quickBackend(state: State): Override {
  return (call) => {
    if (call.path === `${o}/spaces/${SPACE}/agents`)
      return json(200, {
        agents: [
          agent("a1", { name: "前端 Agent", visibility: "SHARED" }),
          agent("a2", { name: "别人的 Agent", owner_user_id: "u2" }),
          agent("as", { name: "助手", builtin_kind: "ASSISTANT" }),
        ],
      });
    if (call.path === `${o}/agent-directory`)
      return json(200, {
        entries: [entry("c1", { type: "crew", name: "前端小队" })],
        archived_count: 0,
        truncated: false,
      });
    if (call.path === "/v1/me/preferences")
      return json(200, {
        version: 1,
        locale: null,
        outline_color: 0,
        open_session_on_assign: true,
        open_session_on_quick_command: state.openSession ?? true,
        send_shortcut: "enter",
      });
    if (call.path === `${o}/conversations` && call.method === "POST")
      return json(201, summary("c9", { task_id: "task-1" }));
    if (
      call.path === `${o}/conversations/c9/messages` &&
      call.method === "POST"
    )
      return state.messageFails
        ? json(409, {
            error: {
              code: "CONVERSATION_CLOSED",
              message: "x",
              request_id: "r",
            },
          })
        : json(201, { id: "m1" });
    return commandRoutes(state, call);
  };
}

type Call = Parameters<Override>[0];

function commandRoutes(state: State, call: Call) {
  const group = (visibility: string) =>
    visibility === "SHARED" ? state.shared : state.private;
  if (call.path === qc && call.method === "GET")
    return json(200, { private: state.private, shared: state.shared });
  if (call.path === qc && call.method === "POST") {
    const body = call.body as Partial<QuickCommand> & { id: string };
    const item = command(body.id, {
      ...body,
      target: {
        kind: body.crew_id ? "CREW" : "AGENT",
        id: (body.crew_id ?? body.agent_id)!,
        name: "x",
        avatar: null,
        available: true,
      },
    });
    group(item.visibility).push(item);
    return json(201, item);
  }
  if (call.path === `${qc}/order` && call.method === "PUT") {
    const body = call.body as { visibility: string; ids: string[] };
    const list = group(body.visibility);
    const reordered = reorderLike(list, body.ids);
    list.splice(0, list.length, ...reordered);
    return json(200, { quick_commands: list });
  }
  const match = new RegExp(`^${qc}/([^/]+)$`).exec(call.path);
  if (!match) return undefined;
  const id = match[1]!;
  for (const list of [state.private, state.shared]) {
    const index = list.findIndex((item) => item.id === id);
    if (index < 0) continue;
    if (call.method === "DELETE") {
      list.splice(index, 1);
      return new Response(null, { status: 204 });
    }
    const next = {
      ...list[index]!,
      ...(call.body as object),
      version: list[index]!.version + 1,
    };
    list[index] = next;
    return json(200, next);
  }
  return undefined;
}

/** 与服务端一致：只重排 ids 中的条目，其他条目原位不动 */
function reorderLike(list: QuickCommand[], ids: string[]) {
  const byId = new Map(list.map((item) => [item.id, item]));
  let next = 0;
  return list.map((item) =>
    ids.includes(item.id) ? byId.get(ids[next++]!)! : item,
  );
}

/** 头像 / 图标带 aria-label，按可见文字找菜单项 */
const labelOf = (el: Element) =>
  (el.querySelector(".truncate") ?? el).textContent;
const menuItem = (menu: HTMLElement, text: string) =>
  within(menu)
    .getAllByRole("menuitem")
    .find((el) => labelOf(el) === text)!;

async function openSummon(user: ReturnType<typeof userEvent.setup>) {
  await screen.findByRole("textbox", { name: "任务标题" });
  await user.click(screen.getByRole("button", { name: "召唤 Agent" }));
  return screen.findByRole("menu", { name: "召唤 Agent" });
}

beforeEach(() => {
  localStorage.setItem(`openfox:onboarding:${ORG}`, JSON.stringify({}));
});
afterEach(() => localStorage.clear());

describe("召唤菜单 · 快捷指令分组", () => {
  it("两项之后是快捷指令：私有在前、共享在后，末尾是管理入口；目标不可用的置灰并显示原因", async () => {
    const user = userEvent.setup();
    const state: State = {
      private: [command("p1", { name: "需求澄清" })],
      shared: [
        command("s1", { name: "小队评审", visibility: "SHARED" }),
        command("s2", {
          name: "已失效",
          visibility: "SHARED",
          target: {
            kind: "AGENT",
            id: "a9",
            name: "旧 Agent",
            avatar: null,
            available: false,
            unavailable_reason: "ARCHIVED",
          },
        }),
      ],
    };
    const server = renderAt(quickBackend(state));
    const menu = await openSummon(user);
    await within(menu).findByText("需求澄清");
    expect(within(menu).getAllByRole("menuitem").map(labelOf)).toEqual([
      "把任务指派给 Agent",
      "选择 Agent 新开对话",
      "需求澄清",
      "小队评审",
      "已失效",
      "管理快捷指令",
    ]);
    const stale = menuItem(menu, "已失效");
    expect(stale).toHaveAttribute("aria-disabled", "true");
    expect(within(stale).getByText("目标已归档")).toBeInTheDocument();
    await user.click(stale);
    expect(server.find("POST", "/conversations")).toHaveLength(0);
  });

  it("没有指令时显示空态", async () => {
    const user = userEvent.setup();
    renderAt(quickBackend({ private: [], shared: [] }));
    const menu = await openSummon(user);
    expect(
      await within(menu).findByText("没有可用的快捷指令"),
    ).toBeInTheDocument();
    expect(menuItem(menu, "管理快捷指令")).toBeInTheDocument();
  });
});

describe("触发快捷指令", () => {
  async function trigger(state: State, name: string) {
    const user = userEvent.setup();
    const server = renderAt(quickBackend(state));
    const menu = await openSummon(user);
    await within(menu).findByText(name);
    await user.click(menuItem(menu, name));
    return server;
  }

  it("Agent 指令：建挂任务的共享会话，发送提示词并 @ 该 Agent，然后打开会话", async () => {
    const server = await trigger(
      { private: [command("p1", { name: "需求澄清" })], shared: [] },
      "需求澄清",
    );
    await waitFor(() =>
      expect(server.find("POST", "/conversations/c9/messages")).toHaveLength(1),
    );
    expect(server.find("POST", "/conversations")[0]!.body).toEqual({
      space_id: SPACE,
      task_id: "task-1",
      agent_id: "a1",
      visibility: "SHARED",
    });
    const body = server.find("POST", "/conversations/c9/messages")[0]!
      .body as Record<string, unknown>;
    expect(body).toMatchObject({
      body: "请澄清本任务",
      mentions: [{ agent_id: "a1" }],
    });
    expect(typeof body.client_message_id).toBe("string");
    await waitFor(() =>
      expect(screen.getByTestId("search")).toHaveTextContent(
        "?view=chat&conversationId=c9",
      ),
    );
  });

  it("小队指令且提示词为空：用 crew_id 建会话，发送固定文案，不 @（由队长处理）", async () => {
    const crew = command("s1", {
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
    const server = await trigger({ private: [], shared: [crew] }, "小队评审");
    await waitFor(() =>
      expect(server.find("POST", "/conversations/c9/messages")).toHaveLength(1),
    );
    expect(server.find("POST", "/conversations")[0]!.body).toMatchObject({
      crew_id: "c1",
      task_id: "task-1",
    });
    expect(server.find("POST", "/conversations")[0]!.body).not.toHaveProperty(
      "agent_id",
    );
    expect(
      server.find("POST", "/conversations/c9/messages")[0]!.body,
    ).toMatchObject({
      body: "请处理这个任务。",
      mentions: [],
    });
  });

  it("偏好关闭时不打开会话，提示已发送", async () => {
    const server = await trigger(
      {
        private: [command("p1", { name: "需求澄清" })],
        shared: [],
        openSession: false,
      },
      "需求澄清",
    );
    expect(await screen.findByText("已发送给 前端 Agent")).toBeInTheDocument();
    expect(server.find("POST", "/conversations/c9/messages")).toHaveLength(1);
    expect(screen.getByTestId("search")).not.toHaveTextContent(
      "conversationId=c9",
    );
  });

  it("发送失败：会话已建好，打开它并提示可在会话里重发", async () => {
    await trigger(
      {
        private: [command("p1", { name: "需求澄清" })],
        shared: [],
        messageFails: true,
      },
      "需求澄清",
    );
    expect(await screen.findByText(/指令没有发出去/)).toBeInTheDocument();
    await waitFor(() =>
      expect(screen.getByTestId("search")).toHaveTextContent(
        "?view=chat&conversationId=c9",
      ),
    );
  });
});

async function openManage(state: State) {
  const user = userEvent.setup();
  const server = renderAt(quickBackend(state));
  const menu = await openSummon(user);
  await user.click(menuItem(menu, "管理快捷指令"));
  const dialog = await screen.findByRole("dialog", { name: "管理快捷指令" });
  return { user, server, dialog };
}

describe("管理快捷指令", () => {
  it("空态与新建：必填未填时禁用保存，计数 0/100，可见性默认私有；目标只列本空间可用的 Agent 和小队", async () => {
    const { user, server, dialog } = await openManage({
      private: [],
      shared: [],
    });
    expect(
      await within(dialog).findByText(
        "本空间还没有快捷指令，点「新建」创建一个。",
      ),
    ).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "新建" }));
    const form = await screen.findByRole("dialog", { name: "新建快捷指令" });
    const save = within(form).getByRole("button", { name: "保存" });
    expect(save).toBeDisabled();
    expect(within(form).getByText("0/100")).toBeInTheDocument();
    expect(within(form).getByRole("radio", { name: /私有/ })).toBeChecked();
    await user.type(
      within(form).getByRole("textbox", { name: "功能名" }),
      "需求澄清",
    );
    expect(within(form).getByText("4/100")).toBeInTheDocument();
    expect(save).toBeDisabled();
    await user.click(
      within(form).getByRole("button", { name: "Agent / 小队" }),
    );
    const options = screen
      .getAllByRole("option")
      .map((el) => el.querySelector(".truncate")?.textContent);
    // 别人的私有 Agent 与内置助手不列出
    expect(options).toEqual(["前端 Agent", "前端小队"]);
    await user.click(screen.getAllByRole("option")[1]!);
    await user.click(within(form).getByRole("radio", { name: /共享/ }));
    await user.click(save);
    await waitFor(() =>
      expect(server.find("POST", "/quick-commands")).toHaveLength(1),
    );
    expect(server.find("POST", "/quick-commands")[0]!.body).toMatchObject({
      name: "需求澄清",
      visibility: "SHARED",
      crew_id: "c1",
      agent_id: null,
      prompt: "",
      icon_kind: null,
    });
    // 保存后回到列表
    expect(
      await screen.findByRole("dialog", { name: "管理快捷指令" }),
    ).toBeInTheDocument();
  });

  it("编辑：只提交变化的目标；删除需要二次确认；不可编辑的条目没有操作和拖动手柄", async () => {
    const state: State = {
      private: [command("p1", { name: "需求澄清", version: 3 })],
      shared: [
        command("s1", {
          name: "别人的指令",
          visibility: "SHARED",
          owner_user_id: "u2",
          can_edit: false,
        }),
      ],
    };
    const { user, server, dialog } = await openManage(state);
    const privateGroup = await within(dialog).findByRole("region", {
      name: "私有",
    });
    const sharedGroup = within(dialog).getByRole("region", { name: "共享" });
    expect(within(sharedGroup).getByText("别人的指令")).toBeInTheDocument();
    expect(
      within(sharedGroup).queryByRole("button", { name: /编辑|删除|拖动排序/ }),
    ).toBeNull();
    expect(
      within(privateGroup).getByRole("button", { name: "拖动排序 需求澄清" }),
    ).toBeInTheDocument();

    await user.click(
      within(privateGroup).getByRole("button", { name: "编辑 需求澄清" }),
    );
    const form = await screen.findByRole("dialog", { name: "编辑快捷指令" });
    const name = within(form).getByRole("textbox", { name: "功能名" });
    await user.clear(name);
    await user.type(name, "澄清");
    await user.click(within(form).getByRole("button", { name: "保存" }));
    await waitFor(() =>
      expect(server.find("PATCH", "/quick-commands/p1")).toHaveLength(1),
    );
    const patch = server.find("PATCH", "/quick-commands/p1")[0]!.body as Record<
      string,
      unknown
    >;
    expect(patch).toMatchObject({
      version: 3,
      name: "澄清",
      visibility: "PRIVATE",
    });
    expect(patch).not.toHaveProperty("agent_id");

    const list = await screen.findByRole("dialog", { name: "管理快捷指令" });
    await user.click(
      await within(list).findByRole("button", { name: "删除 澄清" }),
    );
    const confirm = await screen.findByRole("dialog", {
      name: "删除快捷指令「澄清」？",
    });
    expect(server.find("DELETE", "/quick-commands/p1")).toHaveLength(0);
    await user.click(within(confirm).getByRole("button", { name: "删除" }));
    await waitFor(() =>
      expect(server.find("DELETE", "/quick-commands/p1")).toHaveLength(1),
    );
    expect(
      await screen.findByRole("dialog", { name: "管理快捷指令" }),
    ).toBeInTheDocument();
  });

  it("排序：只在可编辑条目之间换序，不可编辑条目保持原位", () => {
    const mine1 = command("m1");
    const other = command("o1", { can_edit: false });
    const mine2 = command("m2");
    const ids = (items: QuickCommand[] | null) => items?.map((item) => item.id);
    expect(ids(reorderCommands([mine1, other, mine2], "m2", "m1"))).toEqual([
      "m2",
      "o1",
      "m1",
    ]);
    expect(ids(reorderCommands([mine1, other, mine2], "m1", "o1"))).toEqual([
      "m1",
      "o1",
      "m2",
    ]);
    expect(reorderCommands([mine1, mine2], "m1", "m1")).toBeNull();
    expect(reorderCommands([mine1, mine2], "m1", undefined)).toBeNull();
  });
});
