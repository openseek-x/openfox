import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, useLocation } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import type { ConversationSummary } from "@openfox/api";

import { App, createQueryClient } from "../App";
import { agent, entry } from "../agents/fixtures";
import { summary } from "../conversations/fixtures";
import {
  backend,
  json,
  makeTask,
  ORG,
  SPACE,
  type Override,
} from "./test-backend";

// 召唤菜单「把任务指派给 Agent」：有可复用的指派会话时先选择运行对话；
// 已指派的 Agent 再选一次就再运行一次；成功后在任务全页打开并排会话。

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
const sp = `${o}/spaces/${SPACE}`;
const drawer = `/o/${ORG}/spaces/${SPACE}/tasks?task=OPEN-1`;

interface State {
  assignees?: ReturnType<typeof makeTask>["assignees"];
  /** 任务会话列表（task_id 查询） */
  list: ConversationSummary[];
  /** 指派后轮询（task_id + agent_id 查询）返回的会话 */
  polled?: ConversationSummary[];
  openSessionOnAssign?: boolean;
}

function choiceBackend(state: State): Override {
  return (call) => {
    if (call.path === `${sp}/agents`)
      return json(200, { agents: [agent("a1", { name: "前端 Agent" })] });
    if (call.path === `${o}/agent-directory`)
      return json(200, {
        entries: [entry("c1", { type: "crew", name: "前端小队" })],
        archived_count: 0,
        truncated: false,
      });
    if (/\/tasks\/by-display-id\//.test(call.path))
      return json(200, {
        ...makeTask(1, "st1", { assignees: state.assignees ?? [] }),
        description_reactions: [],
      });
    if (call.path === "/v1/me/preferences")
      return json(200, {
        version: 1,
        locale: null,
        outline_color: 0,
        open_session_on_assign: state.openSessionOnAssign ?? false,
        open_session_on_quick_command: true,
        send_shortcut: "enter",
      });
    if (call.method === "GET" && call.path === `${o}/conversations`)
      return json(200, {
        conversations: call.query.get("agent_id")
          ? (state.polled ?? [])
          : call.query.get("task_id")
            ? state.list
            : [],
        next_cursor: null,
      });
    if (call.method === "POST" && call.path.endsWith("/agent-runs"))
      return json(202, {
        task_id: "task-1",
        agent_id: (call.body as { agent_id: string }).agent_id,
        assignment_conversation: (
          call.body as { assignment_conversation: string }
        ).assignment_conversation,
      });
    return undefined;
  };
}

function renderAt(state: State) {
  const server = backend(choiceBackend(state));
  const queryClient = createQueryClient();
  queryClient.setDefaultOptions({ queries: { retry: false } });
  render(
    <MemoryRouter initialEntries={[drawer]}>
      <App api={server.api} queryClient={queryClient} locale="zh-CN" />
      <Where />
    </MemoryRouter>,
  );
  return server;
}

/** 头像带 aria-label，按可见文字找菜单项 */
const labelOf = (el: Element) =>
  (el.querySelector(".truncate") ?? el).textContent;

async function pickAgent(user: ReturnType<typeof userEvent.setup>) {
  await screen.findByRole("textbox", { name: "任务标题" });
  await user.click(screen.getByRole("button", { name: "召唤 Agent" }));
  const menu = await screen.findByRole("menu", { name: "召唤 Agent" });
  await user.click(
    within(menu).getByRole("menuitem", { name: "把任务指派给 Agent" }),
  );
  const item = within(menu)
    .getAllByRole("menuitemradio")
    .find((el) => labelOf(el) === "前端 Agent")!;
  await user.click(item);
}

const reusable = (extra: Partial<ConversationSummary> = {}) =>
  summary("c5", {
    task_id: "task-1",
    agent_id: "a1",
    origin: "TASK_ASSIGNMENT",
    title: "上次的指派对话",
    visibility: "SHARED",
    ...extra,
  });

const panelOf = (id: string) => `?view=chat&conversationId=${id}`;

beforeEach(() => {
  localStorage.setItem(`openfox:onboarding:${ORG}`, JSON.stringify({}));
});
afterEach(() => localStorage.clear());

describe("未指派的 Agent", () => {
  it("有可复用的指派会话：弹出选择运行对话，默认原有对话；开始运行后 PATCH 不带新字段，直接打开原会话", async () => {
    const user = userEvent.setup();
    const server = renderAt({ list: [reusable()] });
    await pickAgent(user);
    const dialog = await screen.findByRole("dialog", { name: "选择运行对话" });
    const reuse = within(dialog).getByRole("radio", {
      name: "在原有对话中运行（上次的指派对话）",
    });
    expect(reuse).toBeChecked();
    expect(
      within(dialog).getByText(
        "新对话将成为此任务的指派对话，之后的运行都在这里进行",
      ),
    ).toBeInTheDocument();
    // 弹窗打开时还没有写入
    expect(server.find("PATCH", "/tasks/task-1")).toHaveLength(0);
    await user.click(within(dialog).getByRole("button", { name: "开始运行" }));
    await waitFor(() =>
      expect(server.find("PATCH", "/tasks/task-1")).toHaveLength(1),
    );
    const body = server.find("PATCH", "/tasks/task-1")[0]!.body as Record<
      string,
      unknown
    >;
    expect(body).toMatchObject({ assignees: [{ kind: "AGENT", id: "a1" }] });
    expect(body).not.toHaveProperty("assignment_conversation");
    await waitFor(() =>
      expect(screen.getByTestId("where")).toHaveTextContent(
        `/o/${ORG}/tasks/OPEN-1`,
      ),
    );
    expect(screen.getByTestId("search")).toHaveTextContent(panelOf("c5"));
  });

  it("选新开对话：PATCH 带 assignment_conversation = new，打开服务端新建的指派会话", async () => {
    const user = userEvent.setup();
    const server = renderAt({
      list: [reusable()],
      polled: [
        summary("c6", {
          task_id: "task-1",
          agent_id: "a1",
          origin: "TASK_ASSIGNMENT",
        }),
      ],
    });
    await pickAgent(user);
    const dialog = await screen.findByRole("dialog", { name: "选择运行对话" });
    await user.click(
      within(dialog).getByRole("radio", { name: "新开对话运行" }),
    );
    await user.click(within(dialog).getByRole("button", { name: "开始运行" }));
    await waitFor(() =>
      expect(server.find("PATCH", "/tasks/task-1")).toHaveLength(1),
    );
    expect(server.find("PATCH", "/tasks/task-1")[0]!.body).toMatchObject({
      assignees: [{ kind: "AGENT", id: "a1" }],
      assignment_conversation: "new",
    });
    await waitFor(() =>
      expect(screen.getByTestId("search")).toHaveTextContent(panelOf("c6")),
    );
    expect(screen.getByTestId("where")).toHaveTextContent(
      `/o/${ORG}/tasks/OPEN-1`,
    );
  });

  it("取消：不发任何写请求，留在原地", async () => {
    const user = userEvent.setup();
    const server = renderAt({ list: [reusable({ title: null })] });
    await pickAgent(user);
    const dialog = await screen.findByRole("dialog", { name: "选择运行对话" });
    // 空标题显示「无标题」
    expect(
      within(dialog).getByRole("radio", { name: "在原有对话中运行（无标题）" }),
    ).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "取消" }));
    await waitFor(() =>
      expect(screen.queryByRole("dialog", { name: "选择运行对话" })).toBeNull(),
    );
    expect(server.find("PATCH", "/tasks/task-1")).toHaveLength(0);
    expect(server.find("POST", "/agent-runs")).toHaveLength(0);
    expect(screen.getByTestId("where")).toHaveTextContent(
      `/o/${ORG}/spaces/${SPACE}/tasks`,
    );
  });

  it("没有可复用的指派会话（已完成的不算）：不弹窗直接指派，偏好关闭也打开新会话", async () => {
    const user = userEvent.setup();
    const server = renderAt({
      list: [
        reusable({ lifecycle: "COMPLETED" }),
        summary("c4", { task_id: "task-1", agent_id: "a1", origin: "USER" }),
      ],
      polled: [
        summary("c7", {
          task_id: "task-1",
          agent_id: "a1",
          origin: "TASK_ASSIGNMENT",
        }),
      ],
      openSessionOnAssign: false,
    });
    await pickAgent(user);
    await waitFor(() =>
      expect(server.find("PATCH", "/tasks/task-1")).toHaveLength(1),
    );
    expect(screen.queryByRole("dialog", { name: "选择运行对话" })).toBeNull();
    expect(server.find("PATCH", "/tasks/task-1")[0]!.body).not.toHaveProperty(
      "assignment_conversation",
    );
    await waitFor(() =>
      expect(screen.getByTestId("search")).toHaveTextContent(panelOf("c7")),
    );
  });
});

describe("已指派的 Agent 再次选择", () => {
  const assignees = [{ kind: "AGENT" as const, id: "a1", name: "前端 Agent" }];

  it("有可复用的指派会话：弹窗，选原有对话 → 再运行接口 reuse，不改处理人，打开原会话", async () => {
    const user = userEvent.setup();
    const server = renderAt({ assignees, list: [reusable()] });
    await pickAgent(user);
    const dialog = await screen.findByRole("dialog", { name: "选择运行对话" });
    await user.click(within(dialog).getByRole("button", { name: "开始运行" }));
    await waitFor(() =>
      expect(server.find("POST", "/tasks/task-1/agent-runs")).toHaveLength(1),
    );
    const run = server.find("POST", "/tasks/task-1/agent-runs")[0]!;
    expect(run.body).toEqual({
      agent_id: "a1",
      assignment_conversation: "reuse",
    });
    // 每次确认带一个幂等键，防止重放重复入队
    expect(run.headers.get("Idempotency-Key")).toMatch(
      /^[A-Za-z0-9_-]{8,128}$/,
    );
    expect(server.find("PATCH", "/tasks/task-1")).toHaveLength(0);
    await waitFor(() =>
      expect(screen.getByTestId("search")).toHaveTextContent(panelOf("c5")),
    );
  });

  it("选新开对话 → 再运行接口 new，打开新建的指派会话", async () => {
    const user = userEvent.setup();
    const server = renderAt({
      assignees,
      list: [reusable()],
      polled: [
        summary("c6", {
          task_id: "task-1",
          agent_id: "a1",
          origin: "TASK_ASSIGNMENT",
        }),
      ],
    });
    await pickAgent(user);
    const dialog = await screen.findByRole("dialog", { name: "选择运行对话" });
    await user.click(
      within(dialog).getByRole("radio", { name: "新开对话运行" }),
    );
    await user.click(within(dialog).getByRole("button", { name: "开始运行" }));
    await waitFor(() =>
      expect(server.find("POST", "/tasks/task-1/agent-runs")).toHaveLength(1),
    );
    const run = server.find("POST", "/tasks/task-1/agent-runs")[0]!;
    expect(run.body).toMatchObject({
      agent_id: "a1",
      assignment_conversation: "new",
    });
    expect(run.headers.get("Idempotency-Key")).toMatch(
      /^[A-Za-z0-9_-]{8,128}$/,
    );
    await waitFor(() =>
      expect(screen.getByTestId("search")).toHaveTextContent(panelOf("c6")),
    );
  });

  it("再运行被拒绝（任务状态不允许）：提示原因，不跳转", async () => {
    const user = userEvent.setup();
    const server = backend((call) => {
      if (call.method === "POST" && call.path.endsWith("/agent-runs"))
        return json(409, {
          error: {
            code: "TASK_AGENT_RUN_NOT_ALLOWED",
            message: "x",
            request_id: "r",
          },
        });
      return choiceBackend({ assignees, list: [] })(call);
    });
    const queryClient = createQueryClient();
    queryClient.setDefaultOptions({ queries: { retry: false } });
    render(
      <MemoryRouter initialEntries={[drawer]}>
        <App api={server.api} queryClient={queryClient} locale="zh-CN" />
        <Where />
      </MemoryRouter>,
    );
    await pickAgent(user);
    expect(
      await screen.findByText(
        "任务当前的状态不能让 Agent 运行（只有待办和进行中的任务可以）",
      ),
    ).toBeInTheDocument();
    expect(server.find("PATCH", "/tasks/task-1")).toHaveLength(0);
    expect(screen.getByTestId("where")).toHaveTextContent(
      `/o/${ORG}/spaces/${SPACE}/tasks`,
    );
  });

  it("小队维持原有逻辑：已指派时再点不发请求，也不弹窗", async () => {
    const user = userEvent.setup();
    const server = renderAt({
      assignees: [{ kind: "CREW", id: "c1", name: "前端小队" }],
      list: [],
    });
    await screen.findByRole("textbox", { name: "任务标题" });
    await user.click(screen.getByRole("button", { name: "召唤 Agent" }));
    const menu = await screen.findByRole("menu", { name: "召唤 Agent" });
    await user.click(
      within(menu).getByRole("menuitem", { name: "把任务指派给 Agent" }),
    );
    await user.click(
      within(menu)
        .getAllByRole("menuitemradio")
        .find((el) => labelOf(el) === "前端小队")!,
    );
    expect(screen.queryByRole("dialog", { name: "选择运行对话" })).toBeNull();
    expect(server.find("PATCH", "/tasks/task-1")).toHaveLength(0);
    expect(server.find("POST", "/agent-runs")).toHaveLength(0);
  });
});
