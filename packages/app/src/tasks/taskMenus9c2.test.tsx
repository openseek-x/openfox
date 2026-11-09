import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, useLocation } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

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

// 任务详情菜单：复制标题 & 链接 / commit message、
// 完整子任务表单（D04）、召唤菜单两级结构（D06）、表情选择器（D10）。

function CurrentLocation() {
  const location = useLocation();
  return (
    <output data-testid="location">
      {location.pathname + location.search}
    </output>
  );
}

function renderAt(path: string, override?: Override) {
  const server = backend(override);
  const queryClient = createQueryClient();
  queryClient.setDefaultOptions({ queries: { retry: false } });
  render(
    <MemoryRouter initialEntries={[path]}>
      <CurrentLocation />
      <App api={server.api} queryClient={queryClient} locale="zh-CN" />
    </MemoryRouter>,
  );
  return server;
}

const detailPath = `/o/${ORG}/spaces/${SPACE}/tasks?task=OPEN-1`;
const o = `/v1/organizations/${ORG}`;
const sp = `${o}/spaces/${SPACE}`;

function mockClipboard() {
  const writeText = vi.fn().mockResolvedValue(undefined);
  Object.defineProperty(navigator, "clipboard", {
    value: { writeText },
    configurable: true,
  });
  return writeText;
}

async function openMore(user: ReturnType<typeof userEvent.setup>) {
  await screen.findByRole("textbox", { name: "任务标题" });
  await user.click(screen.getByRole("button", { name: "更多操作" }));
  return screen.findByRole("menu", { name: "更多操作" });
}

beforeEach(() => {
  localStorage.setItem(`openfox:onboarding:${ORG}`, JSON.stringify({}));
});
afterEach(() => {
  localStorage.clear();
  vi.restoreAllMocks();
});

describe("D09 复制类菜单", () => {
  it("菜单顺序：复制链接、复制标题 & 链接 … 复制 git 分支名、复制 git commit message", async () => {
    const user = userEvent.setup();
    renderAt(detailPath);
    const menu = await openMore(user);
    const names = within(menu)
      .getAllByRole("menuitem")
      .map((item) => item.textContent);
    const at = (name: string) => names.indexOf(name);
    expect(at("复制标题 & 链接")).toBe(at("复制链接") + 1);
    expect(at("复制 git commit message")).toBe(at("复制 git 分支名") + 1);
  });

  it("复制标题 & 链接：编号 标题，换行，链接", async () => {
    const user = userEvent.setup();
    const writeText = mockClipboard();
    renderAt(detailPath);
    const menu = await openMore(user);
    await user.click(
      within(menu).getByRole("menuitem", { name: "复制标题 & 链接" }),
    );
    await waitFor(() =>
      expect(writeText).toHaveBeenCalledWith(
        `OPEN-1 任务标题 1\n${window.location.origin}/o/${ORG}/spaces/${SPACE}/tasks?task=OPEN-1`,
      ),
    );
    expect(await screen.findByText("已复制标题和链接")).toBeInTheDocument();
  });

  it("复制 git commit message：标题，空一行，Refs: 编号", async () => {
    const user = userEvent.setup();
    const writeText = mockClipboard();
    renderAt(detailPath);
    const menu = await openMore(user);
    await user.click(
      within(menu).getByRole("menuitem", { name: "复制 git commit message" }),
    );
    await waitFor(() =>
      expect(writeText).toHaveBeenCalledWith("任务标题 1\n\nRefs: OPEN-1"),
    );
    expect(
      await screen.findByText("已复制 commit message"),
    ).toBeInTheDocument();
  });
});

describe("D04 完整子任务表单", () => {
  /** 新建时保留 parent_task_id，列表按 parent_task_id 过滤，模拟真实后端 */
  function withSubtasks(): Override {
    const created: ReturnType<typeof makeTask>[] = [];
    return (call) => {
      if (call.path === `${sp}/tasks` && call.method === "POST") {
        const body = call.body as { title: string; parent_task_id?: string };
        const task = makeTask(50 + created.length, "st1", {
          title: body.title,
          parent_task_id: body.parent_task_id ?? null,
          parent: body.parent_task_id
            ? {
                id: body.parent_task_id,
                display_id: "open-1",
                title: "任务标题 1",
              }
            : null,
        });
        created.push(task);
        return json(201, task);
      }
      const parent = call.query.get("parent_task_id");
      if (call.path === `${sp}/tasks` && call.method === "GET" && parent)
        return json(200, {
          tasks: created.filter((t) => t.parent_task_id === parent),
          next_cursor: "",
        });
      const child = created.find(
        (t) =>
          call.path ===
          `${o}/tasks/by-display-id/${t.display_id.toUpperCase()}`,
      );
      if (call.method === "GET" && child)
        return json(200, { ...child, description_reactions: [] });
      return undefined;
    };
  }

  it.each([
    [detailPath, `/o/${ORG}/spaces/${SPACE}/tasks?task=OPEN-50`],
    [`/o/${ORG}/tasks/OPEN-1`, `/o/${ORG}/tasks/OPEN-50`],
  ])("从 %s 添加子任务后直接打开新子任务", async (path, expectedPath) => {
    const user = userEvent.setup();
    const server = renderAt(path, withSubtasks());
    await user.click(await screen.findByRole("button", { name: "添加子任务" }));
    const dialog = await screen.findByRole("dialog", { name: "新建任务" });
    expect(within(dialog).getByTestId("create-parent")).toHaveTextContent(
      "父任务：OPEN-1",
    );
    expect(
      within(dialog).getByRole("button", { name: /^切换空间/ }),
    ).toBeDisabled();
    await user.type(
      within(dialog).getByRole("textbox", { name: "标题" }),
      "拆出来的子任务",
    );
    await user.click(within(dialog).getByRole("button", { name: "创 建" }));
    await waitFor(() =>
      expect(server.find("POST", `/spaces/${SPACE}/tasks`)).toHaveLength(1),
    );
    expect(
      server.find("POST", `/spaces/${SPACE}/tasks`)[0]!.body,
    ).toMatchObject({ title: "拆出来的子任务", parent_task_id: "task-1" });
    await waitFor(() =>
      expect(screen.getByRole("textbox", { name: "任务标题" })).toHaveValue(
        "拆出来的子任务",
      ),
    );
    expect(screen.getByTestId("location")).toHaveTextContent(expectedPath);
    expect(
      screen.queryByRole("dialog", { name: "新建任务" }),
    ).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "更多属性" }));
    const parent = await screen.findByRole("group", { name: "父任务" });
    // 编辑父关系仍有独立入口；点击名称只导航，不更新关系。
    await user.click(
      within(parent).getByRole("button", { name: "设置父任务" }),
    );
    const picker = await screen.findByRole("dialog", { name: "设置父任务" });
    await user.click(within(picker).getByRole("button", { name: "关闭" }));
    await user.click(
      within(parent).getByRole("button", { name: "OPEN-1 任务标题 1" }),
    );
    await waitFor(() =>
      expect(screen.getByRole("textbox", { name: "任务标题" })).toHaveValue(
        "任务标题 1",
      ),
    );
    expect(server.find("PATCH", `/spaces/${SPACE}/tasks/task-50`)).toHaveLength(
      0,
    );
    expect(
      await within(screen.getByRole("region", { name: "子任务" })).findByRole(
        "button",
        { name: /OPEN-50\s*拆出来的子任务/ },
      ),
    ).toBeInTheDocument();
  });

  it("继续创建保留父任务和表单，关闭继续创建后打开最后创建的子任务", async () => {
    const user = userEvent.setup();
    const server = renderAt(detailPath, withSubtasks());
    await user.click(await screen.findByRole("button", { name: "添加子任务" }));
    const dialog = await screen.findByRole("dialog", { name: "新建任务" });
    const title = within(dialog).getByRole("textbox", { name: "标题" });
    const keepCreating = within(dialog).getByRole("switch", {
      name: "继续创建",
    });
    await user.click(keepCreating);
    await user.type(title, "第一个子任务");
    await user.click(within(dialog).getByRole("button", { name: "创 建" }));
    await waitFor(() => expect(title).toHaveValue(""));
    expect(screen.getByTestId("location")).toHaveTextContent(detailPath);
    expect(within(dialog).getByTestId("create-parent")).toHaveTextContent(
      "父任务：OPEN-1",
    );
    await user.click(keepCreating);
    await user.type(title, "第二个子任务");
    await user.click(within(dialog).getByRole("button", { name: "创 建" }));
    await waitFor(() =>
      expect(screen.getByRole("textbox", { name: "任务标题" })).toHaveValue(
        "第二个子任务",
      ),
    );
    expect(screen.getByTestId("location")).toHaveTextContent("?task=OPEN-51");
    const calls = server.find("POST", `/spaces/${SPACE}/tasks`);
    expect(calls).toHaveLength(2);
    expect(calls.map((call) => call.body)).toEqual([
      expect.objectContaining({ parent_task_id: "task-1" }),
      expect.objectContaining({ parent_task_id: "task-1" }),
    ]);
    expect(calls[0]!.headers.get("Idempotency-Key")).not.toBe(
      calls[1]!.headers.get("Idempotency-Key"),
    );
  });

  it("创建失败时保留标题、父任务和表单，不跳转", async () => {
    const user = userEvent.setup();
    renderAt(detailPath, (call) =>
      call.path === `${sp}/tasks` && call.method === "POST"
        ? json(500, {
            error: {
              code: "INTERNAL_ERROR",
              message: "failed",
              request_id: "r",
            },
          })
        : undefined,
    );
    await user.click(await screen.findByRole("button", { name: "添加子任务" }));
    const dialog = await screen.findByRole("dialog", { name: "新建任务" });
    await user.type(
      within(dialog).getByRole("textbox", { name: "标题" }),
      "失败保留草稿",
    );
    await user.click(within(dialog).getByRole("button", { name: "创 建" }));
    await waitFor(() =>
      expect(
        within(dialog).getByRole("button", { name: "创 建" }),
      ).toBeEnabled(),
    );
    expect(within(dialog).getByRole("textbox", { name: "标题" })).toHaveValue(
      "失败保留草稿",
    );
    expect(screen.getByTestId("location")).toHaveTextContent(detailPath);
  });
});

describe("D06 召唤菜单", () => {
  /** a1 可指派；a2 不允许单独指派；as 是内置助手；小队 c1 在本空间 */
  const summonBackend =
    (assignees: ReturnType<typeof makeTask>["assignees"] = []): Override =>
    (call) => {
      if (call.path === `${sp}/agents`)
        return json(200, {
          agents: [
            agent("a1", { name: "前端 Agent" }),
            agent("a2", { name: "专属 Agent", allow_assign: false }),
            agent("as", { name: "助手", builtin_kind: "ASSISTANT" }),
          ],
        });
      if (call.path === `${o}/agent-directory`)
        return json(200, {
          entries: [entry("c1", { type: "crew", name: "前端小队" })],
          archived_count: 0,
          truncated: false,
        });
      if (/\/tasks\/by-display-id\//.test(call.path))
        return json(200, {
          ...makeTask(1, "st1", { assignees }),
          description_reactions: [],
        });
      if (call.path === `${o}/conversations` && call.method === "POST")
        return json(201, summary("c9", { task_id: "task-1" }));
      return undefined;
    };

  /** 头像带 aria-label，可访问名会重复一次，按可见文字找菜单项 */
  const labelOf = (el: Element) =>
    (el.querySelector(".truncate") ?? el).textContent;
  const labels = (menu: HTMLElement) =>
    Array.from(menu.querySelectorAll('[role^="menuitem"]')).map(labelOf);
  const item = (menu: HTMLElement, role: string, text: string) =>
    within(menu)
      .getAllByRole(role)
      .find((el) => labelOf(el) === text)!;

  async function openSummon(user: ReturnType<typeof userEvent.setup>) {
    await screen.findByRole("textbox", { name: "任务标题" });
    await user.click(screen.getByRole("button", { name: "召唤 Agent" }));
    return screen.findByRole("menu", { name: "召唤 Agent" });
  }

  // 第一级在两个子菜单之后显示「快捷指令」分组，详细用例见 quickCommands9f.test.tsx。
  it("第一级是两个子菜单，之后是快捷指令分组", async () => {
    const user = userEvent.setup();
    renderAt(detailPath, summonBackend());
    const menu = await openSummon(user);
    expect(
      within(menu)
        .getAllByRole("menuitem")
        .map((item) => item.textContent),
    ).toEqual([
      "把任务指派给 Agent",
      "选择 Agent 新开对话",
      "没有可用的快捷指令",
      "管理快捷指令",
    ]);
    expect(within(menu).getByText("快捷指令")).toBeInTheDocument();
  });

  it("把任务指派给 Agent：列出可指派的 Agent 和小队，选中后走处理人 PATCH", async () => {
    const user = userEvent.setup();
    const server = renderAt(detailPath, summonBackend());
    const menu = await openSummon(user);
    await user.click(
      within(menu).getByRole("menuitem", { name: "把任务指派给 Agent" }),
    );
    expect(labels(menu)).toEqual(["返回", "前端 Agent", "前端小队"]);
    await user.click(item(menu, "menuitemradio", "前端 Agent"));
    await waitFor(() =>
      expect(server.find("PATCH", "/tasks/task-1")).toHaveLength(1),
    );
    expect(server.find("PATCH", "/tasks/task-1")[0]!.body).toMatchObject({
      assignees: [{ kind: "AGENT", id: "a1" }],
    });
    // 只是指派，不在这里新开会话（自动执行由「指派即执行」负责）
    expect(server.find("POST", "/conversations")).toHaveLength(0);
  });

  // 再点已指派的 Agent 会让它再运行一次（不改处理人），详细用例见 runChoice10b.test.tsx。
  it("已在处理人里的项打勾，再点关闭菜单、不改处理人，改为再运行一次", async () => {
    const user = userEvent.setup();
    const server = renderAt(
      detailPath,
      summonBackend([{ kind: "AGENT", id: "a1", name: "前端 Agent" }]),
    );
    const menu = await openSummon(user);
    await user.click(
      within(menu).getByRole("menuitem", { name: "把任务指派给 Agent" }),
    );
    const assigned = item(menu, "menuitemradio", "前端 Agent");
    expect(assigned).toHaveAttribute("aria-checked", "true");
    expect(item(menu, "menuitemradio", "前端小队")).toHaveAttribute(
      "aria-checked",
      "false",
    );
    await user.click(assigned);
    await waitFor(() =>
      expect(screen.queryByRole("menu", { name: "召唤 Agent" })).toBeNull(),
    );
    expect(server.find("PATCH", "/tasks/task-1")).toHaveLength(0);
    await waitFor(() =>
      expect(server.find("POST", "/tasks/task-1/agent-runs")).toHaveLength(1),
    );
  });

  it("指派小队用 CREW，保留已有的 Agent 处理人", async () => {
    const user = userEvent.setup();
    const server = renderAt(
      detailPath,
      summonBackend([{ kind: "AGENT", id: "a1", name: "前端 Agent" }]),
    );
    const menu = await openSummon(user);
    await user.click(
      within(menu).getByRole("menuitem", { name: "把任务指派给 Agent" }),
    );
    await user.click(item(menu, "menuitemradio", "前端小队"));
    await waitFor(() =>
      expect(server.find("PATCH", "/tasks/task-1")).toHaveLength(1),
    );
    expect(server.find("PATCH", "/tasks/task-1")[0]!.body).toMatchObject({
      assignees: [
        { kind: "AGENT", id: "a1" },
        { kind: "CREW", id: "c1" },
      ],
    });
  });

  it("选择 Agent 新开对话：原来的 Agent 列表，选中后新建任务会话", async () => {
    const user = userEvent.setup();
    const server = renderAt(detailPath, summonBackend());
    const menu = await openSummon(user);
    await user.click(
      within(menu).getByRole("menuitem", { name: "选择 Agent 新开对话" }),
    );
    // 不允许单独指派的 Agent 仍可对话，内置助手不列出（与原来一致）
    expect(
      within(menu)
        .getAllByRole("menuitem")
        .map((item) => item.textContent),
    ).toEqual(["返回", "前端 Agent", "专属 Agent"]);
    await user.click(item(menu, "menuitem", "专属 Agent"));
    await waitFor(() =>
      expect(server.find("POST", "/conversations")).toHaveLength(1),
    );
    expect(server.find("POST", "/conversations")[0]!.body).toMatchObject({
      agent_id: "a2",
      task_id: "task-1",
      visibility: "SHARED",
    });
    expect(server.find("PATCH", "/tasks/task-1")).toHaveLength(0);
  });

  it("返回回到第一级", async () => {
    const user = userEvent.setup();
    renderAt(detailPath, summonBackend());
    const menu = await openSummon(user);
    await user.click(
      within(menu).getByRole("menuitem", { name: "选择 Agent 新开对话" }),
    );
    await user.click(within(menu).getByRole("menuitem", { name: "返回" }));
    expect(
      within(menu).getByRole("menuitem", { name: "把任务指派给 Agent" }),
    ).toBeInTheDocument();
  });
});

describe("D10 表情反应", () => {
  const withReactions: Override = (call) => {
    if (/\/tasks\/by-display-id\//.test(call.path))
      return json(200, {
        ...makeTask(1, "st1"),
        description_reactions: [{ emoji: "❤️", count: 2, reacted: false }],
      });
    if (call.path.endsWith("/reactions"))
      return new Response(null, { status: 204 });
    return undefined;
  };

  it("旧表情照常显示；打开选择器选中后发出添加反应请求", async () => {
    const user = userEvent.setup();
    const server = renderAt(detailPath, withReactions);
    const bar = await screen.findByRole("group", { name: "表情回应" });
    expect(
      within(bar).getByRole("button", { name: "回应 ❤️ 2" }),
    ).toBeInTheDocument();
    await user.click(within(bar).getByRole("button", { name: "添加表情" }));
    const popover = await screen.findByRole("dialog", { name: "添加表情" });
    const recent = await within(popover).findByRole("rowgroup", {
      name: "最近使用",
    });
    await user.click(
      within(recent).getByRole("gridcell", { name: "拉炮彩带" }),
    );
    await waitFor(() =>
      expect(server.find("PUT", "/reactions")).toHaveLength(1),
    );
    expect(server.find("PUT", "/reactions")[0]!.body).toMatchObject({
      emoji: "🎉",
    });
    expect(screen.queryByRole("dialog", { name: "添加表情" })).toBeNull();
  });
});
