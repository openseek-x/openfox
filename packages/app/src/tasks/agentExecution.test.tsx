import { QueryClient } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, useLocation } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { App, createQueryClient } from "../App";
import { agent } from "../agents/fixtures";
import { keys } from "../context";
import { summary } from "../conversations/fixtures";
import {
  EVENT_COALESCE_MS,
  createEventBatcher,
} from "../realtime/useOrgEvents";
import { backend, json, ORG, SPACE, type Override } from "./test-backend";

const TS = "2026-10-29T08:00:00Z";

function Where() {
  const location = useLocation();
  return (
    <>
      <output data-testid="where">{location.pathname}</output>
      <output data-testid="search">{location.search}</output>
    </>
  );
}

function renderAt(path: string, override?: Override) {
  const server = backend(override);
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

const drawer = `/o/${ORG}/spaces/${SPACE}/tasks?task=OPEN-1`;

beforeEach(() => {
  localStorage.setItem(`openfox:onboarding:${ORG}`, JSON.stringify({}));
});
afterEach(() => localStorage.clear());

describe("Agent 作者评论", () => {
  it("Agent 评论显示 Agent 名称与 Agent 标识，用户评论没有标识", async () => {
    renderAt(drawer, (call) =>
      call.path.endsWith("/tasks/task-1/timeline")
        ? json(200, {
            items: [
              {
                type: "comment",
                comment: {
                  organization_id: ORG,
                  space_id: SPACE,
                  task_id: "task-1",
                  id: "c1",
                  author_kind: "AGENT",
                  author_agent_id: "a1",
                  author_name: "熊猫",
                  body: "已完成初步分析",
                  created_at: TS,
                  reactions: [],
                },
              },
              {
                type: "comment",
                comment: {
                  organization_id: ORG,
                  space_id: SPACE,
                  task_id: "task-1",
                  id: "c2",
                  author_kind: "USER",
                  author_user_id: "u1",
                  author_name: "林",
                  body: "收到",
                  created_at: TS,
                  reactions: [],
                },
              },
            ],
            next_cursor: null,
          })
        : undefined,
    );
    const agentRow = (await screen.findByText("已完成初步分析")).closest("li")!;
    expect(within(agentRow).getByText("熊猫")).toBeInTheDocument();
    expect(within(agentRow).getByText("Agent")).toBeInTheDocument();
    const userRow = screen.getByText("收到").closest("li")!;
    expect(within(userRow).queryByText("Agent")).toBeNull();
  });
});

describe("执行区块", () => {
  it("列出该任务的会话（按 task_id 查询），点击打开任务全页的 AI 面板", async () => {
    const user = userEvent.setup();
    const server = renderAt(drawer, (call) =>
      call.path === `/v1/organizations/${ORG}/conversations`
        ? json(200, {
            conversations: [
              summary("c9", {
                task_id: "task-1",
                origin: "TASK_ASSIGNMENT",
                status: "REVIEW",
              }),
            ],
            next_cursor: null,
          })
        : undefined,
    );
    const section = await screen.findByRole("region", { name: "执行" });
    expect(within(section).getByText("熊猫")).toBeInTheDocument();
    expect(
      server.calls.some(
        (c) =>
          c.path.endsWith("/conversations") &&
          c.query.get("task_id") === "task-1",
      ),
    ).toBe(true);
    await user.click(
      within(section).getByRole("button", { name: "打开会话 熊猫" }),
    );
    // 指派 Agent 后进入任务全页并打开 AI 面板。
    expect(screen.getByTestId("where")).toHaveTextContent(
      `/o/${ORG}/tasks/OPEN-1`,
    );
    expect(screen.getByTestId("search")).toHaveTextContent(
      "?view=chat&conversationId=c9",
    );
  });

  it("没有会话时不显示执行区块", async () => {
    renderAt(drawer);
    await screen.findByRole("textbox", { name: "任务标题" });
    await waitFor(() =>
      expect(screen.queryByRole("region", { name: "执行" })).toBeNull(),
    );
  });
});

describe("指派 Agent 后自动打开会话", () => {
  function setup(enabled: boolean) {
    let polls = 0;
    const server = renderAt(drawer, (call) => {
      const base = `/v1/organizations/${ORG}`;
      if (call.path === `${base}/spaces/${SPACE}/agents`)
        return json(200, { agents: [agent("a1", { name: "熊猫" })] });
      if (call.path === "/v1/me/preferences")
        return json(200, {
          version: 1,
          locale: null,
          outline_color: 0,
          open_session_on_assign: enabled,
          open_session_on_quick_command: false,
          send_shortcut: "enter",
        });
      if (
        call.path === `${base}/conversations` &&
        call.query.get("agent_id") === "a1"
      ) {
        polls += 1;
        // 第一次还没创建，第二次出现
        return json(200, {
          conversations:
            polls < 2
              ? []
              : [
                  summary("c7", {
                    task_id: "task-1",
                    origin: "TASK_ASSIGNMENT",
                  }),
                ],
          next_cursor: null,
        });
      }
      return undefined;
    });
    return { server, polls: () => polls };
  }

  async function assign() {
    const user = userEvent.setup();
    const trigger = await screen.findByRole("button", { name: "处理人" });
    await user.click(trigger);
    await user.click(await screen.findByRole("option", { name: /熊猫/ }));
  }

  // 指派后的落点是任务全页的并排会话，不跳到独立的 /chat/<id>。
  it("偏好开启：保存成功后轮询到会话并在任务全页打开", async () => {
    const { server } = setup(true);
    await assign();
    await waitFor(() =>
      expect(server.find("PATCH", "/tasks/task-1")).toHaveLength(1),
    );
    await waitFor(
      () =>
        expect(screen.getByTestId("where")).toHaveTextContent(
          `/o/${ORG}/tasks/OPEN-1`,
        ),
      { timeout: 4000 },
    );
    expect(screen.getByTestId("search")).toHaveTextContent(
      "?view=chat&conversationId=c7",
    );
  });

  it("偏好关闭：不查找也不打开会话", async () => {
    const { server, polls } = setup(false);
    await assign();
    await waitFor(() =>
      expect(server.find("PATCH", "/tasks/task-1")).toHaveLength(1),
    );
    await waitFor(() =>
      expect(server.find("GET", "/v1/me/preferences")).not.toHaveLength(0),
    );
    expect(polls()).toBe(0);
    expect(screen.getByTestId("where")).not.toHaveTextContent("/chat/");
  });
});

describe("实时事件失效任务时间线与执行区块", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  function seeded() {
    const client = new QueryClient();
    client.setQueryData(keys.taskSub(ORG, "t1", "timeline"), {});
    client.setQueryData(keys.taskSub(ORG, "t2", "timeline"), {});
    client.setQueryData(keys.taskSub(ORG, "t1", "followers"), {});
    client.setQueryData(keys.conversationList(ORG, { task_id: "t1" }), {});
    const stale = () =>
      client
        .getQueryCache()
        .getAll()
        .filter((q) => q.state.isInvalidated)
        .map((q) => JSON.stringify(q.queryKey));
    return { client, stale };
  }

  it("run.state 带 task_id：只失效该任务时间线与会话列表", () => {
    const { client, stale } = seeded();
    const batcher = createEventBatcher(client, ORG);
    batcher.push("run.state", {
      type: "run.state",
      conversation_id: "c1",
      payload: { state: "RUNNING", task_id: "t1" },
    });
    vi.advanceTimersByTime(EVENT_COALESCE_MS + 10);
    const hit = stale();
    expect(hit).toContain(JSON.stringify(keys.taskSub(ORG, "t1", "timeline")));
    expect(hit).not.toContain(
      JSON.stringify(keys.taskSub(ORG, "t2", "timeline")),
    );
    expect(hit).not.toContain(
      JSON.stringify(keys.taskSub(ORG, "t1", "followers")),
    );
    expect(hit).toContain(
      JSON.stringify(keys.conversationList(ORG, { task_id: "t1" })),
    );
    batcher.dispose();
  });

  it("conversation.updated：失效全部任务时间线与会话列表", () => {
    const { client, stale } = seeded();
    const batcher = createEventBatcher(client, ORG);
    batcher.push("conversation.updated", {
      type: "conversation.updated",
      conversation_id: "c1",
      payload: { lifecycle: "OPEN", handoff: "WAITING_REVIEW" },
    });
    vi.advanceTimersByTime(EVENT_COALESCE_MS + 10);
    const hit = stale();
    expect(hit).toContain(JSON.stringify(keys.taskSub(ORG, "t1", "timeline")));
    expect(hit).toContain(JSON.stringify(keys.taskSub(ORG, "t2", "timeline")));
    expect(hit).toContain(
      JSON.stringify(keys.conversationList(ORG, { task_id: "t1" })),
    );
    batcher.dispose();
  });
});
