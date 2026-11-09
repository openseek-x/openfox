import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {
  MemoryRouter,
  Route,
  Routes,
  useLocation,
  useNavigationType,
} from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { App, createQueryClient } from "../App";
import { displayStorageKey } from "../tasks/model";
import {
  backend,
  json,
  makeTask,
  ORG,
  SPACE,
  type Call,
} from "../tasks/test-backend";
import { agent, entry } from "./fixtures";
import { readAgentTaskTab, useAgentTaskTab, writeAgentPanel } from "./params";

// Agent 抽屉「任务」页签的任务工作台（D23）：
// 已指派 / 已创建两个页签与数量、查询参数（assignee / created_via_agent）、查找与筛选、
// 显示设置按 agent:<id> 单独存本机、页签写进地址（replace，刷新保留）、点任务打开抽屉、默认列表、新建预填处理人。

const o = `/v1/organizations/${ORG}`;
const sp = `${o}/spaces/${SPACE}`;
const TASKS = `/o/${ORG}/inbox?agentSettings=a1&agentPanel=full&agentTab=tasks`;

// 已指派：任务 1、2；已创建：任务 3
const assignedTasks = [makeTask(1, "st1"), makeTask(2, "st2")];
const createdTasks = [makeTask(3, "st1", { title: "Agent 建的任务" })];

function scoped(call: Call) {
  if (call.query.getAll("created_via_agent").includes("a1"))
    return createdTasks;
  if (call.query.getAll("assignee").includes("agent:a1")) return assignedTasks;
  return null;
}

function renderAt(path: string) {
  const newTasks: ReturnType<typeof makeTask>[] = [];
  const server = backend((call) => {
    if (call.path === `${sp}/tasks` && call.method === "POST") {
      const body = call.body as { title: string; status_id?: string };
      const task = makeTask(50 + newTasks.length, body.status_id ?? "st1", {
        title: body.title,
        // 当前后端把执行 Agent 写入 executor_agent_id。
        executor_agent_id: "a1",
      });
      newTasks.push(task);
      return json(201, task);
    }
    const newTask = newTasks.find(
      (task) =>
        call.path ===
        `${o}/tasks/by-display-id/${task.display_id.toUpperCase()}`,
    );
    if (newTask) return json(200, { ...newTask, description_reactions: [] });
    if (call.path === `${sp}/agents/a1/stats`)
      return json(200, {
        task_count: assignedTasks.length + newTasks.length,
        run_count: 0,
        input_tokens: 0,
        output_tokens: 0,
        last_run_at: null,
      });
    if (call.path === `${o}/agent-directory`)
      return json(200, {
        entries: [entry("a1", { name: "熊孩子" })],
        archived_count: 0,
        truncated: false,
      });
    if (call.path === `${sp}/agents/a1`)
      return json(200, agent("a1", { name: "熊孩子" }));
    if (call.path === `${sp}/agents`)
      return json(200, { agents: [agent("a1", { name: "熊孩子" })] });
    if (call.path === sp)
      return json(200, {
        id: SPACE,
        prefix: "open",
        name: "默认空间",
        features: { views: false, projects: false, iterations: false },
        estimation: { scale: "notUsed" },
      });
    const list = scoped(call);
    if (!list) return undefined;
    // 查找：q 只留标题包含的
    const q = call.query.get("q");
    const ids = call.query.getAll("status_id");
    const matched = list.filter(
      (t) =>
        (!q || t.title.includes(q)) &&
        (ids.length === 0 || ids.includes(t.status_id)),
    );
    if (call.path === `${sp}/tasks/counts`) {
      const by = new Map<string, number>();
      for (const t of matched)
        by.set(t.status_id, (by.get(t.status_id) ?? 0) + 1);
      return json(200, {
        total: matched.length,
        by_status: [...by].map(([status_id, count]) => ({ status_id, count })),
      });
    }
    if (call.path === `${sp}/tasks` && call.method === "GET")
      return json(200, { tasks: matched, next_cursor: "" });
    return undefined;
  });
  const queryClient = createQueryClient();
  queryClient.setDefaultOptions({ queries: { retry: false } });
  render(
    <MemoryRouter initialEntries={[path]}>
      <App api={server.api} queryClient={queryClient} locale="zh-CN" />
    </MemoryRouter>,
  );
  return server;
}

const drawer = () =>
  screen.findByRole("complementary", { name: "熊孩子 的设置" });

beforeEach(() => {
  localStorage.setItem(`openfox:onboarding:${ORG}`, JSON.stringify({}));
});
afterEach(() => localStorage.clear());

describe("Agent 任务工作台", () => {
  it("两个页签带数量；默认在已指派，按 assignee=agent:<id> 查，默认列表布局", async () => {
    const server = renderAt(TASKS);
    const panel = await drawer();
    const tabs = within(panel).getByRole("tablist", { name: "Agent 的任务" });
    expect(
      await within(tabs).findByRole("tab", { name: /^已指派\s*2$/ }),
    ).toHaveAttribute("aria-selected", "true");
    expect(
      await within(tabs).findByRole("tab", { name: /^已创建\s*1$/ }),
    ).toHaveAttribute("aria-selected", "false");
    // 默认列表（不是看板）
    expect(
      await within(panel).findByRole("region", { name: "任务列表" }),
    ).toBeInTheDocument();
    expect(
      within(panel).queryByRole("region", { name: "任务看板" }),
    ).toBeNull();
    expect(await within(panel).findByText("任务标题 1")).toBeInTheDocument();
    expect(within(panel).queryByText("Agent 建的任务")).toBeNull();
    // 列表请求固定在 Agent 所在空间，带 assignee
    const lists = server.calls.filter(
      (c) => c.path === `${sp}/tasks` && c.method === "GET",
    );
    expect(lists.length).toBeGreaterThan(0);
    expect(
      lists.every((c) => c.query.getAll("assignee").includes("agent:a1")),
    ).toBe(true);
  });

  it("切到已创建：按 created_via_agent=<id> 查；刷新后页签保留", async () => {
    const user = userEvent.setup();
    const server = renderAt(TASKS);
    const panel = await drawer();
    await user.click(
      await within(panel).findByRole("tab", { name: /^已创建\s*1$/ }),
    );
    expect(
      await within(panel).findByText("Agent 建的任务"),
    ).toBeInTheDocument();
    expect(within(panel).queryByText("任务标题 1")).toBeNull();
    const created = server.calls.filter(
      (c) =>
        c.path === `${sp}/tasks` &&
        c.query.getAll("created_via_agent").includes("a1"),
    );
    expect(created.length).toBeGreaterThan(0);
    // 已创建不带 assignee（不是 creator_id）
    expect(created.every((c) => !c.query.has("assignee"))).toBe(true);
    expect(created.every((c) => !c.query.has("creator_id"))).toBe(true);
  });

  it("直接用带 agentTaskTab=created 的地址打开（刷新）：停在已创建", async () => {
    renderAt(`${TASKS}&agentTaskTab=created`);
    const panel = await drawer();
    expect(
      await within(panel).findByRole("tab", { name: /^已创建\s*1$/ }),
    ).toHaveAttribute("aria-selected", "true");
    expect(
      await within(panel).findByText("Agent 建的任务"),
    ).toBeInTheDocument();
  });

  it("查找与筛选：与范围参数一起发出，结果随之变化", async () => {
    const user = userEvent.setup();
    const server = renderAt(TASKS);
    const panel = await drawer();
    expect(await within(panel).findByText("任务标题 2")).toBeInTheDocument();
    await user.click(within(panel).getByRole("button", { name: "查找" }));
    await user.type(
      within(panel).getByRole("textbox", { name: "查找" }),
      "标题 1",
    );
    await waitFor(() =>
      expect(within(panel).queryByText("任务标题 2")).toBeNull(),
    );
    expect(within(panel).getByText("任务标题 1")).toBeInTheDocument();
    expect(
      server.calls.some(
        (c) =>
          c.path === `${sp}/tasks/counts` &&
          c.query.get("q") === "标题 1" &&
          c.query.getAll("assignee").includes("agent:a1"),
      ),
    ).toBe(true);
    // 筛选：优先级（与空间任务页相同的字段）
    await user.click(within(panel).getByRole("button", { name: "筛选" }));
    await user.click(await screen.findByRole("menuitem", { name: "优先级" }));
    await user.click(await screen.findByRole("option", { name: "高" }));
    await user.keyboard("{Escape}");
    await waitFor(() =>
      expect(
        server.calls.some(
          (c) =>
            c.path === `${sp}/tasks/counts` &&
            c.query.getAll("priority").includes("HIGH") &&
            c.query.getAll("assignee").includes("agent:a1"),
        ),
      ).toBe(true),
    );
  });

  it("显示设置按 agent:<id> 单独存本机，不影响空间任务页；看板列上新建预填这个 Agent", async () => {
    const user = userEvent.setup();
    localStorage.setItem(
      displayStorageKey(ORG, `space:${SPACE}`),
      JSON.stringify({ layout: "table" }),
    );
    const server = renderAt(TASKS);
    const panel = await drawer();
    // 空间任务页存的是表格，这里仍是默认列表
    expect(
      await within(panel).findByRole("region", { name: "任务列表" }),
    ).toBeInTheDocument();
    await user.click(within(panel).getByRole("button", { name: "显示设置" }));
    await user.click(screen.getByRole("radio", { name: "看板" }));
    expect(
      await within(panel).findByRole("region", { name: "任务看板" }),
    ).toBeInTheDocument();
    expect(
      JSON.parse(localStorage.getItem(displayStorageKey(ORG, "agent:a1"))!),
    ).toMatchObject({ layout: "board" });
    expect(
      JSON.parse(
        localStorage.getItem(displayStorageKey(ORG, `space:${SPACE}`))!,
      ),
    ).toEqual({ layout: "table" });
    await user.keyboard("{Escape}");
    await user.click(
      await within(panel).findByRole("button", {
        name: "在「待处理」中新建任务",
      }),
    );
    const dialog = await screen.findByRole("dialog", { name: /新建任务/ });
    expect(within(dialog).getByText("熊孩子")).toBeInTheDocument();
    await user.type(
      within(dialog).getByRole("textbox", { name: "标题" }),
      "工作台列上创建",
    );
    await user.click(within(dialog).getByRole("button", { name: "创 建" }));
    await waitFor(() =>
      expect(screen.getByRole("textbox", { name: "任务标题" })).toHaveValue(
        "工作台列上创建",
      ),
    );
    expect(
      server.find("POST", `/spaces/${SPACE}/tasks`)[0]!.body,
    ).toMatchObject({
      status_id: "st1",
      assignees: [{ kind: "AGENT", id: "a1" }],
    });
  });

  it.each([
    `/o/${ORG}/inbox?agentSettings=a1&agentPanel=full`,
    `/o/${ORG}/agents?agentSettings=a1`,
  ])(
    "从 %s 新建或指派任务：新建后打开详情，关闭后保留 Agent 设置",
    async (path) => {
      const user = userEvent.setup();
      const server = renderAt(path);
      const panel = await drawer();
      const taskCount = () =>
        within(panel).getByText("任务", { selector: "dt" })
          .previousElementSibling;
      await waitFor(() => expect(taskCount()).toHaveTextContent("2"));
      await user.click(
        within(panel).getByRole("button", { name: "新建或指派任务" }),
      );
      const palette = await screen.findByRole("dialog", {
        name: "指派任务给 熊孩子",
      });
      await user.click(
        within(palette).getByRole("button", { name: "新建任务" }),
      );
      const dialog = await screen.findByRole("dialog", { name: /新建任务/ });
      await user.type(
        within(dialog).getByRole("textbox", { name: "标题" }),
        "Agent 设置里创建",
      );
      await user.click(within(dialog).getByRole("button", { name: "创 建" }));
      await waitFor(() =>
        expect(screen.getByRole("textbox", { name: "任务标题" })).toHaveValue(
          "Agent 设置里创建",
        ),
      );
      expect(
        server.find("POST", `/spaces/${SPACE}/tasks`)[0]!.body,
      ).toMatchObject({
        assignees: [{ kind: "AGENT", id: "a1" }],
      });
      expect(
        screen.queryByRole("dialog", { name: /新建任务/ }),
      ).not.toBeInTheDocument();
      const detail = screen.getByRole("dialog", { name: /Agent 设置里创建/ });
      await user.click(within(detail).getByRole("button", { name: "关闭" }));
      expect(await drawer()).toBeInTheDocument();
      expect(
        screen.queryByRole("textbox", { name: "任务标题" }),
      ).not.toBeInTheDocument();
      await waitFor(() => expect(taskCount()).toHaveTextContent("3"));
    },
  );

  it("点任务打开任务抽屉", async () => {
    const user = userEvent.setup();
    const server = renderAt(TASKS);
    const panel = await drawer();
    await user.click(
      await within(panel).findByRole("button", { name: "OPEN-1 任务标题 1" }),
    );
    await waitFor(() =>
      expect(
        server.calls.some((c) => /by-display-id\/OPEN-1$/i.test(c.path)),
      ).toBe(true),
    );
  });
});

function TabProbe() {
  const [tab, setTab] = useAgentTaskTab();
  const location = useLocation();
  return (
    <>
      <p data-testid="where">{`${location.pathname}${location.search}`}</p>
      <p data-testid="type">{useNavigationType()}</p>
      <p data-testid="tab">{tab}</p>
      <button type="button" onClick={() => setTab("created")}>
        created
      </button>
      <button type="button" onClick={() => setTab("assigned")}>
        assigned
      </button>
    </>
  );
}

describe("agentTaskTab 地址参数", () => {
  it("已创建写 agentTaskTab=created（replace），已指派是默认值不写；保留其他参数", async () => {
    const user = userEvent.setup();
    render(
      <MemoryRouter
        initialEntries={["/x?agentSettings=a1&agentPanel=full&agentTab=tasks"]}
      >
        <Routes>
          <Route path="/x" element={<TabProbe />} />
        </Routes>
      </MemoryRouter>,
    );
    expect(screen.getByTestId("tab")).toHaveTextContent("assigned");
    await user.click(screen.getByRole("button", { name: "created" }));
    expect(screen.getByTestId("where")).toHaveTextContent(
      "/x?agentSettings=a1&agentPanel=full&agentTab=tasks&agentTaskTab=created",
    );
    expect(screen.getByTestId("type")).toHaveTextContent("REPLACE");
    expect(screen.getByTestId("tab")).toHaveTextContent("created");
    await user.click(screen.getByRole("button", { name: "assigned" }));
    expect(screen.getByTestId("where")).toHaveTextContent(
      "/x?agentSettings=a1&agentPanel=full&agentTab=tasks",
    );
  });

  it("读：未知值按已指派；切面板页签或收起时去掉 agentTaskTab", () => {
    expect(readAgentTaskTab(new URLSearchParams("agentTaskTab=bogus"))).toBe(
      "assigned",
    );
    const base =
      "agentSettings=a1&agentPanel=full&agentTab=tasks&agentTaskTab=created";
    expect(
      writeAgentPanel(new URLSearchParams(base), {
        expanded: true,
        tab: "runs",
      }).toString(),
    ).toBe("agentSettings=a1&agentPanel=full&agentTab=runs");
    expect(
      writeAgentPanel(new URLSearchParams(base), {
        expanded: false,
      }).toString(),
    ).toBe("agentSettings=a1");
  });
});
