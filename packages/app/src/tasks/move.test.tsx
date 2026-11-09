import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, useLocation } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import type { TaskMovePreview } from "@openfox/api";

import { App, createQueryClient } from "../App";
import { moveTargetOptions } from "./detail/moveToSpace";
import {
  backend,
  json,
  makeTask,
  ORG,
  SPACE,
  type Override,
} from "./test-backend";

// 任务「移动到空间」：菜单入口与显示条件、目标过滤、预览、确认、幂等键、错误、成功跳转、快捷键。

const o = `/v1/organizations/${ORG}`;
const spaceTasks = `/o/${ORG}/spaces/${SPACE}/tasks`;
const movePath = `${o}/spaces/${SPACE}/tasks/task-1/move`;

type SpaceFixture = Record<string, unknown>;
function space(
  id: string,
  prefix: string,
  name: string,
  extra: SpaceFixture = {},
) {
  return {
    id,
    prefix,
    name,
    visibility: "shared",
    version: 1,
    created_at: "2026-10-01T00:00:00Z",
    status: "active",
    my_role: "OWNER",
    can_manage: true,
    features: {
      views: false,
      projects: false,
      iterations: false,
      triage: false,
    },
    ...extra,
  };
}

/** s1 当前空间；s2 可写；s3 已停用；s4 私有且不是成员；s5 只读（VIEWER）；s6 公开未加入。 */
const SPACES = [
  space(SPACE, "open", "默认空间"),
  space("s2", "pro", "ProaWork"),
  space("s3", "old", "旧空间", { status: "retired" }),
  space("s4", "sec", "私密空间", { visibility: "private", my_role: null }),
  space("s5", "see", "只读空间", { my_role: "VIEWER" }),
  space("s6", "pub", "公开空间", { my_role: null }),
];

function preview(extra: Partial<TaskMovePreview> = {}): TaskMovePreview {
  return {
    target_space_id: "s2",
    tasks_count: 2,
    tasks: [
      {
        task_id: "task-1",
        title: "任务标题 1",
        display_id: "OPEN-1",
        display_id_hint: "PRO-7",
        type_from: { id: "t1", name: "任务" },
        type_to: { id: "t9", name: "需求" },
        status_from: { id: "st1", name: "待处理" },
        status_to: { id: "st8", name: "待办" },
        estimate_from: 5,
        estimate_to: 3,
        cleared: ["iteration", "project", "parent"],
        dropped_labels: [{ id: "l1", name: "前端" }],
      },
      {
        task_id: "task-9",
        title: "子任务甲",
        display_id: "OPEN-9",
        display_id_hint: "PRO-8",
        type_from: { id: "t1", name: "任务" },
        type_to: { id: "t1", name: "任务" },
        status_from: { id: "st1", name: "待处理" },
        status_to: { id: "st1", name: "待处理" },
        estimate_from: null,
        estimate_to: null,
        cleared: [],
        dropped_labels: [],
      },
    ],
    detached_parent: null,
    removed_assignees: [],
    active_runs: 0,
    unlinked_issues: [],
    requires_confirmation: false,
    ...extra,
  };
}

const apiError = (status: number, code: string, details?: object) =>
  json(status, { error: { code, message: "x", request_id: "r", details } });

/** 移动成功后的任务：到了 s2，编号 PRO-7。 */
const movedTask = makeTask(1, "st1", { space_id: "s2", display_id: "pro-7" });

function moveResult() {
  return {
    task: movedTask,
    display_id: "PRO-7",
    previous_display_id: "OPEN-1",
    moved_count: 2,
    canceled_runs: 0,
  };
}

interface Routes {
  spaces?: unknown[];
  preview?: () => Response;
  /** 第 n 次（从 0 开始）调用移动接口的响应；默认成功 */
  move?: (n: number) => Response;
  prefs?: Record<string, string>;
  /** 先于以上路由匹配 */
  extra?: Override;
}

/** 空间列表、预览、移动、新编号详情与偏好的假接口；其余走 test-backend。 */
function routes(options: Routes = {}): Override {
  let moves = 0;
  return (call) => {
    const custom = options.extra?.(call);
    if (custom) return custom;
    if (call.path === `${o}/spaces` && call.method === "GET")
      return json(200, { spaces: options.spaces ?? SPACES });
    if (call.path.endsWith("/tasks/task-1/move-preview"))
      return options.preview?.() ?? json(200, preview());
    if (call.path === movePath && call.method === "POST")
      return options.move?.(moves++) ?? json(200, moveResult());
    if (call.path.endsWith("/tasks/by-display-id/PRO-7"))
      return json(200, { ...movedTask, description_reactions: [] });
    if (call.path === "/v1/me/preferences" && call.method === "GET")
      return json(200, {
        theme: "system",
        locale: null,
        outline_color: 0,
        open_session_on_assign: true,
        open_session_on_quick_command: true,
        send_shortcut: "enter",
        command_shortcuts: options.prefs ?? {},
        version: 1,
      });
    return undefined;
  };
}

function Probe() {
  const location = useLocation();
  return (
    <output data-testid="where">{location.pathname + location.search}</output>
  );
}

function renderAt(path: string, options?: Routes) {
  const server = backend(routes(options));
  const queryClient = createQueryClient();
  queryClient.setDefaultOptions({ queries: { retry: false } });
  render(
    <MemoryRouter initialEntries={[path]}>
      <App api={server.api} queryClient={queryClient} locale="zh-CN" />
      <Probe />
    </MemoryRouter>,
  );
  return server;
}

const where = () => screen.getByTestId("where").textContent;

beforeEach(() => {
  localStorage.setItem(`openfox:onboarding:${ORG}`, JSON.stringify({}));
});
afterEach(() => localStorage.clear());

/** 打开任务详情更多菜单里的「移动到空间」子菜单，返回菜单元素。 */
async function openMoveMenu(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await moreButton());
  await user.click(await screen.findByRole("menuitem", { name: "移动到空间" }));
  return screen.getByRole("menu");
}

/** 从子菜单选 ProaWork，返回打开的确认对话框。 */
async function pickProaWork(user: ReturnType<typeof userEvent.setup>) {
  const menu = await openMoveMenu(user);
  await user.click(within(menu).getByRole("menuitem", { name: /ProaWork/ }));
  return screen.findByRole("dialog", { name: "移动到 ProaWork" });
}

/** 详情顶栏的「更多操作」按钮（等详情加载完）。 */
async function moreButton() {
  return screen.findByRole("button", { name: "更多操作" });
}

describe("移动到空间 · 目标过滤", () => {
  it("不含自身（当前空间放最前、不可选）、已停用、无权限的私有空间与只读空间", () => {
    const items = moveTargetOptions(SPACES as never, SPACE, "MEMBER");
    expect(items.map((i) => [i.space.id, i.current])).toEqual([
      [SPACE, true],
      ["s2", false],
      ["s6", false],
    ]);
    // 组织访客没有显式角色时不能写公开空间
    expect(
      moveTargetOptions(SPACES as never, SPACE, "GUEST").map((i) => i.space.id),
    ).toEqual([SPACE, "s2"]);
  });
});

describe("移动到空间 · 菜单入口", () => {
  it("详情更多菜单：在「状态」之前；子菜单列出可写空间，当前空间灰显", async () => {
    const user = userEvent.setup();
    renderAt(`${spaceTasks}?task=OPEN-1`);
    const menu = await openMoveMenu(user);
    const names = within(menu)
      .getAllByRole("menuitem")
      .map((item) => item.textContent);
    expect(names).toEqual(["默认空间open", "ProaWorkpro", "公开空间pub"]);
    const current = within(menu).getByRole("menuitem", { name: /默认空间/ });
    expect(current).toHaveAttribute("aria-disabled", "true");
    expect(current).toHaveAttribute("title", "当前空间");
    // 8 个及以下没有搜索框
    expect(within(menu).queryByRole("textbox")).toBeNull();
  });

  it("入口在复制段之后、「状态」之前", async () => {
    const user = userEvent.setup();
    renderAt(`${spaceTasks}?task=OPEN-1`);
    await user.click(await moreButton());
    const items = (await screen.findAllByRole("menuitem")).map(
      (item) => item.textContent ?? "",
    );
    const move = items.findIndex((text) => text.includes("移动到空间"));
    expect(items[move + 1]).toContain("状态");
    expect(items[move - 1]).toContain("含评论");
  });

  it("只读（源空间 VIEWER）：入口禁用并给原因，点了不进入空间列表", async () => {
    const user = userEvent.setup();
    renderAt(`${spaceTasks}?task=OPEN-1`, {
      spaces: [
        space(SPACE, "open", "默认空间", { my_role: "VIEWER" }),
        SPACES[1],
      ],
    });
    await user.click(await moreButton());
    const entry = await screen.findByRole("menuitem", { name: "移动到空间" });
    await waitFor(() => expect(entry).toHaveAttribute("aria-disabled", "true"));
    expect(entry).toHaveAttribute(
      "title",
      "你在此空间只有查看权限，不能移动任务",
    );
    await user.click(entry);
    expect(screen.queryByRole("menuitem", { name: /ProaWork/ })).toBeNull();
  });

  it("源空间已停用：入口禁用，原因与删除任务一致", async () => {
    const user = userEvent.setup();
    const server = backend((call) => {
      if (call.path === `${o}/spaces`) return json(200, { spaces: [] });
      if (call.path === `${o}/spaces/${SPACE}`)
        return json(
          200,
          space(SPACE, "open", "默认空间", { status: "retired" }),
        );
      return undefined;
    });
    const queryClient = createQueryClient();
    queryClient.setDefaultOptions({ queries: { retry: false } });
    render(
      <MemoryRouter initialEntries={[`${spaceTasks}?task=OPEN-1`]}>
        <App api={server.api} queryClient={queryClient} locale="zh-CN" />
      </MemoryRouter>,
    );
    await user.click(await moreButton());
    const entry = await screen.findByRole("menuitem", { name: "移动到空间" });
    await waitFor(() => expect(entry).toHaveAttribute("aria-disabled", "true"));
    expect(entry).toHaveAttribute("title", "此空间已停用，只能查看。");
  });

  it("超过 8 个空间时子菜单顶部有搜索框，按名称或前缀过滤", async () => {
    const user = userEvent.setup();
    const many = Array.from({ length: 9 }, (_, i) =>
      space(`m${i}`, `m${i}x`, `空间${i}`),
    );
    renderAt(`${spaceTasks}?task=OPEN-1`, { spaces: [SPACES[0], ...many] });
    const menu = await openMoveMenu(user);
    const search = within(menu).getByRole("textbox", {
      name: "按名称或前缀搜索空间",
    });
    await user.type(search, "M3X");
    expect(
      within(menu)
        .getAllByRole("menuitem")
        .map((item) => item.textContent),
    ).toEqual(["空间3m3x"]);
    await user.clear(search);
    await user.type(search, "不存在");
    expect(within(menu).getByText("没有匹配的空间")).toBeInTheDocument();
  });

  it("看板卡片菜单：有「移动到空间」，选中后打开对话框且不打开任务详情", async () => {
    const user = userEvent.setup();
    renderAt(spaceTasks);
    const todo = await screen.findByRole("region", { name: "待处理 2" });
    const card = await within(todo).findByRole("button", {
      name: /OPEN-1 任务标题 1/,
    });
    await user.click(within(card).getByRole("button", { name: "任务操作" }));
    await user.click(
      await screen.findByRole("menuitem", { name: "移动到空间" }),
    );
    await user.click(await screen.findByRole("menuitem", { name: /ProaWork/ }));
    expect(
      await screen.findByRole("dialog", { name: "移动到 ProaWork" }),
    ).toBeInTheDocument();
    expect(where()).toBe(spaceTasks);
  });
});

describe("移动到空间 · 预览", () => {
  it("逐行显示新编号、类型 / 状态 / 估时变化、清空字段、丢弃标签，以及脱离父任务、移除 Agent、取消运行、解除 Issue", async () => {
    const user = userEvent.setup();
    const parented = makeTask(1, "st1", {
      parent_task_id: "task-5",
      parent: { id: "task-5", display_id: "open-5", title: "父任务" },
    });
    const server = renderAt(`${spaceTasks}?task=OPEN-1`, {
      extra: (call) =>
        call.path.endsWith("/tasks/by-display-id/OPEN-1")
          ? json(200, { ...parented, description_reactions: [] })
          : undefined,
      preview: () =>
        json(
          200,
          preview({
            detached_parent: { id: "task-5", name: "父任务" },
            removed_assignees: [
              { task_id: "task-1", kind: "AGENT", id: "a1", name: "熊猫" },
              { task_id: "task-9", kind: "AGENT", id: "a1", name: "熊猫" },
              { task_id: "task-9", kind: "CREW", id: "c1", name: "前端小队" },
            ],
            active_runs: 2,
            unlinked_issues: [
              {
                task_id: "task-1",
                repository: "acme/web",
                number: 12,
                url: "https://x",
              },
            ],
            requires_confirmation: true,
          }),
        ),
    });
    const dialog = await pickProaWork(user);
    expect(
      await within(dialog).findByText(
        "共 2 个任务（含子任务），移动后取新编号，旧编号继续可用。",
      ),
    ).toBeInTheDocument();
    const rows = within(dialog).getAllByRole("listitem");
    expect(rows[0]).toHaveTextContent("OPEN-1→PRO-7预计任务标题 1");
    expect(rows[0]).toHaveTextContent(
      "类型：任务 → 需求 · 状态：待处理 → 待办 · 估时：5 → 3",
    );
    // 「父任务」不在行内重复，由下方统一说明
    expect(rows[0]).toHaveTextContent("将清空：迭代、专项");
    expect(rows[0]).not.toHaveTextContent("父任务");
    expect(rows[0]).toHaveTextContent("将丢弃标签：前端");
    expect(rows[1]).toHaveTextContent("OPEN-9→PRO-8预计子任务甲");
    expect(rows[1]).not.toHaveTextContent("→ ");
    expect(
      within(dialog).getByText("将脱离父任务 OPEN-5 父任务"),
    ).toBeInTheDocument();
    // 需要确认：按钮改为危险样式的「取消运行并移动」
    expect(
      within(dialog).getByRole("button", { name: "取消运行并移动" }),
    ).toBeInTheDocument();
    expect(
      within(dialog).getByText("将移除 Agent / 小队处理人：熊猫、前端小队"),
    ).toBeInTheDocument();
    expect(
      within(dialog).getByText("将取消 2 个正在进行的运行"),
    ).toBeInTheDocument();
    expect(
      within(dialog).getByText(
        "将解除 Issue 同步：acme/web#12（远端 Issue 不受影响）",
      ),
    ).toBeInTheDocument();
    expect(
      within(dialog).getByText(/评论、动态、附件、Git 关联随任务移动/),
    ).toBeInTheDocument();
    expect(server.find("POST", "/move-preview")[0]!.body).toEqual({
      target_space_id: "s2",
    });
  });
});

describe("移动到空间 · 提交", () => {
  it("不需要确认：按钮「移动」，请求不带 cancel_runs，带 version 与 Idempotency-Key", async () => {
    const user = userEvent.setup();
    const server = renderAt(`${spaceTasks}?task=OPEN-1`);
    const dialog = await pickProaWork(user);
    await user.click(
      await within(dialog).findByRole("button", { name: "移动" }),
    );
    await waitFor(() =>
      expect(server.find("POST", "/task-1/move")).toHaveLength(1),
    );
    const call = server.find("POST", "/task-1/move")[0]!;
    expect(call.body).toEqual({ target_space_id: "s2", version: 1 });
    expect(call.headers.get("Idempotency-Key")).toMatch(/^[a-f0-9]{32}$/);
  });

  it("需要确认：请求带 cancel_runs: true", async () => {
    const user = userEvent.setup();
    const server = renderAt(`${spaceTasks}?task=OPEN-1`, {
      preview: () => json(200, preview({ requires_confirmation: true })),
    });
    const dialog = await pickProaWork(user);
    await user.click(
      await within(dialog).findByRole("button", { name: "取消运行并移动" }),
    );
    await waitFor(() =>
      expect(server.find("POST", "/task-1/move")).toHaveLength(1),
    );
    expect(server.find("POST", "/task-1/move")[0]!.body).toEqual({
      target_space_id: "s2",
      version: 1,
      cancel_runs: true,
    });
  });

  it("同一个对话框里重复提交复用同一个 Idempotency-Key；重新打开对话框换新键", async () => {
    const user = userEvent.setup();
    const server = renderAt(`${spaceTasks}?task=OPEN-1`, {
      move: () => apiError(500, "INTERNAL"),
    });
    let dialog = await pickProaWork(user);
    const submit = await within(dialog).findByRole("button", { name: "移动" });
    await user.click(submit);
    expect(await within(dialog).findByRole("alert")).toHaveTextContent(
      "移动失败，请稍后重试。",
    );
    await user.click(submit);
    await waitFor(() =>
      expect(server.find("POST", "/task-1/move")).toHaveLength(2),
    );
    const [first, second] = server.find("POST", "/task-1/move");
    const key = first!.headers.get("Idempotency-Key");
    expect(second!.headers.get("Idempotency-Key")).toBe(key);
    // 对话框保持打开
    expect(dialog).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "取消" }));
    dialog = await pickProaWork(user);
    await user.click(
      await within(dialog).findByRole("button", { name: "移动" }),
    );
    await waitFor(() =>
      expect(server.find("POST", "/task-1/move")).toHaveLength(3),
    );
    expect(
      server.find("POST", "/task-1/move")[2]!.headers.get("Idempotency-Key"),
    ).not.toBe(key);
  });

  it("VERSION_CONFLICT：提示刷新，重新拉预览与任务详情，对话框保持打开", async () => {
    const user = userEvent.setup();
    const server = renderAt(`${spaceTasks}?task=OPEN-1`, {
      move: () => apiError(409, "VERSION_CONFLICT"),
    });
    const dialog = await pickProaWork(user);
    await user.click(
      await within(dialog).findByRole("button", { name: "移动" }),
    );
    expect(await within(dialog).findByRole("alert")).toHaveTextContent(
      "任务已被修改，请刷新后重试。",
    );
    await waitFor(() => {
      expect(server.find("POST", "/move-preview")).toHaveLength(2);
      expect(
        server.find("GET", "/by-display-id/OPEN-1").length,
      ).toBeGreaterThan(1);
    });
    expect(dialog).toBeInTheDocument();
  });

  it("TASK_HAS_ACTIVE_RUNS：用 details 刷新确认信息、按钮切到「取消运行并移动」，不自动重发", async () => {
    const user = userEvent.setup();
    const server = renderAt(`${spaceTasks}?task=OPEN-1`, {
      move: (n) =>
        n === 0
          ? apiError(409, "TASK_HAS_ACTIVE_RUNS", {
              active_runs: 1,
              agent_assignees: 2,
              tasks_count: 2,
            })
          : json(200, moveResult()),
    });
    const dialog = await pickProaWork(user);
    await user.click(
      await within(dialog).findByRole("button", { name: "移动" }),
    );
    expect(
      await within(dialog).findByText(
        "预览之后有人指派了 Agent：现有 2 个 Agent / 小队处理人、1 个正在进行的运行。确认后会一并取消。",
      ),
    ).toBeInTheDocument();
    const confirm = within(dialog).getByRole("button", {
      name: "取消运行并移动",
    });
    await waitFor(() =>
      expect(server.find("POST", "/move-preview")).toHaveLength(2),
    );
    expect(server.find("POST", "/task-1/move")).toHaveLength(1);
    await user.click(confirm);
    await waitFor(() =>
      expect(server.find("POST", "/task-1/move")).toHaveLength(2),
    );
    const [first, second] = server.find("POST", "/task-1/move");
    expect(second!.body).toMatchObject({ cancel_runs: true });
    expect(second!.headers.get("Idempotency-Key")).toBe(
      first!.headers.get("Idempotency-Key"),
    );
  });

  it.each([
    ["INVALID_TARGET_SPACE", 400, "目标空间不可用，请选择其他空间。"],
    ["SPACE_RETIRED", 409, "此空间已停用，任务不能移动。"],
    ["TOO_MANY_TASKS", 409, "子任务太多，一次最多移动 5000 个任务。"],
    ["FORBIDDEN", 403, "你没有在此空间移动任务的权限。"],
    ["TASK_NOT_FOUND", 404, "任务不存在或已被删除。"],
  ])("预览出错 %s：显示对应文案和重试", async (code, status, text) => {
    const user = userEvent.setup();
    let calls = 0;
    renderAt(`${spaceTasks}?task=OPEN-1`, {
      preview: () =>
        calls++ === 0 ? apiError(status, code) : json(200, preview()),
    });
    const dialog = await pickProaWork(user);
    expect(await within(dialog).findByRole("alert")).toHaveTextContent(text);
    expect(within(dialog).getByRole("button", { name: "移动" })).toBeDisabled();
    await user.click(within(dialog).getByRole("button", { name: "重试" }));
    expect(
      await within(dialog).findByText(/共 2 个任务（含子任务）/),
    ).toBeInTheDocument();
  });
});

describe("移动到空间 · 成功之后", () => {
  // 用户 2026-11-04 确认：在源空间页面的抽屉里移动后跳到目标空间任务页（原为停在源空间、只换编号）
  it("抽屉（源空间页面）：toast，跳到目标空间任务页并打开新编号", async () => {
    const user = userEvent.setup();
    renderAt(`${spaceTasks}?task=OPEN-1`);
    const dialog = await pickProaWork(user);
    await user.click(
      await within(dialog).findByRole("button", { name: "移动" }),
    );
    expect(
      await screen.findByText("已移动到 ProaWork，新编号 PRO-7"),
    ).toBeInTheDocument();
    await waitFor(() =>
      expect(where()).toBe(`/o/${ORG}/spaces/s2/tasks?task=PRO-7`),
    );
    expect(
      screen.queryByRole("dialog", { name: "移动到 ProaWork" }),
    ).toBeNull();
  });

  it("抽屉（跨空间页面，如收件箱）：不换页面，只把 task= 换成新编号", async () => {
    const user = userEvent.setup();
    renderAt(`/o/${ORG}/inbox?task=OPEN-1`);
    const dialog = await pickProaWork(user);
    await user.click(
      await within(dialog).findByRole("button", { name: "移动" }),
    );
    await waitFor(() => expect(where()).toBe(`/o/${ORG}/inbox?task=PRO-7`));
  });

  it("全页：跳到新编号的全页地址", async () => {
    const user = userEvent.setup();
    renderAt(`/o/${ORG}/tasks/OPEN-1`);
    const dialog = await pickProaWork(user);
    await user.click(
      await within(dialog).findByRole("button", { name: "移动" }),
    );
    await waitFor(() => expect(where()).toBe(`/o/${ORG}/tasks/PRO-7`));
    expect(
      await screen.findByText("已移动到 ProaWork，新编号 PRO-7"),
    ).toBeInTheDocument();
  });

  it("看板卡片：不跳转，移走的卡片从源空间看板消失", async () => {
    const user = userEvent.setup();
    let moved = false;
    renderAt(spaceTasks, {
      move: () => {
        moved = true;
        return json(200, moveResult());
      },
      extra: (call) =>
        moved &&
        call.method === "GET" &&
        call.path === `${o}/spaces/${SPACE}/tasks`
          ? json(200, {
              tasks: [makeTask(2, "st1"), makeTask(3, "st2")].filter(
                (t) =>
                  call.query.getAll("status_id").length === 0 ||
                  call.query.getAll("status_id").includes(t.status_id),
              ),
              next_cursor: "",
            })
          : undefined,
    });
    const todo = await screen.findByRole("region", { name: "待处理 2" });
    const card = await within(todo).findByRole("button", {
      name: /OPEN-1 任务标题 1/,
    });
    await user.click(within(card).getByRole("button", { name: "任务操作" }));
    await user.click(
      await screen.findByRole("menuitem", { name: "移动到空间" }),
    );
    await user.click(await screen.findByRole("menuitem", { name: /ProaWork/ }));
    const dialog = await screen.findByRole("dialog", {
      name: "移动到 ProaWork",
    });
    await user.click(
      await within(dialog).findByRole("button", { name: "移动" }),
    );
    await waitFor(() =>
      expect(
        screen.queryByRole("button", { name: /OPEN-1 任务标题 1/ }),
      ).toBeNull(),
    );
    expect(where()).toBe(spaceTasks);
    expect(
      screen.getByText("已移动到 ProaWork，新编号 PRO-7"),
    ).toBeInTheDocument();
  });
});

describe("移动到空间 · 快捷键", () => {
  it("任务详情打开时按 move_to_space（默认 mod+shift+m）打开「选择目标」一步", async () => {
    const user = userEvent.setup();
    renderAt(`${spaceTasks}?task=OPEN-1`);
    await moreButton();
    await user.keyboard("{Control>}{Shift>}m{/Shift}{/Control}");
    const dialog = await screen.findByRole("dialog", { name: "移动到空间" });
    await user.click(within(dialog).getByRole("button", { name: /ProaWork/ }));
    expect(
      await screen.findByRole("dialog", { name: "移动到 ProaWork" }),
    ).toBeInTheDocument();
  });

  it("按设置里改过的按键响应，旧的默认键不再响应", async () => {
    const user = userEvent.setup();
    renderAt(`${spaceTasks}?task=OPEN-1`, {
      prefs: { move_to_space: "mod+j" },
    });
    await moreButton();
    await waitFor(async () => {
      await user.keyboard("{Control>}j{/Control}");
      expect(
        screen.getByRole("dialog", { name: "移动到空间" }),
      ).toBeInTheDocument();
    });
    await user.click(screen.getByRole("button", { name: "取消" }));
    await user.keyboard("{Control>}{Shift>}m{/Shift}{/Control}");
    expect(screen.queryByRole("dialog", { name: "移动到空间" })).toBeNull();
  });

  it("没有打开任务详情时不响应", async () => {
    const user = userEvent.setup();
    renderAt(spaceTasks);
    await screen.findByRole("region", { name: "待处理 2" });
    await user.keyboard("{Control>}{Shift>}m{/Shift}{/Control}");
    expect(screen.queryByRole("dialog", { name: "移动到空间" })).toBeNull();
  });
});
