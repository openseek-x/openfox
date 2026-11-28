import fs from "node:fs";
import path from "node:path";

import { expect, test, type Page } from "@playwright/test";

import { entry } from "../../../packages/app/src/agents/fixtures";

const org = "/api/v1/organizations/o1";
const captureDir = process.env.OPE24_CAPTURE_DIR;

const space = {
  id: "s1",
  name: "OpenFox 研发",
  prefix: "OPENFOX",
  visibility: "shared",
  my_role: "OWNER",
  can_manage: true,
  status: "active",
  version: 1,
  default_view_id: null,
  created_at: "2026-10-01T00:00:00Z",
  features: {
    views: true,
    projects: true,
    iterations: true,
    triage: true,
  },
};

const statuses = [
  {
    id: "st1",
    name: "待处理",
    description: "",
    category: "TODO",
    position: 0,
    version: 1,
    created_at: "2026-10-01T00:00:00Z",
    updated_at: "2026-10-01T00:00:00Z",
    is_default: true,
    collapsed_by_default: false,
    task_count: 2,
    type_usage: [],
  },
  {
    id: "st2",
    name: "进行中",
    description: "",
    category: "IN_PROGRESS",
    position: 1,
    version: 1,
    created_at: "2026-10-01T00:00:00Z",
    updated_at: "2026-10-01T00:00:00Z",
    is_default: false,
    collapsed_by_default: false,
    task_count: 1,
    type_usage: [],
  },
];

const tasks = [
  {
    organization_id: "o1",
    space_id: "s1",
    id: "task-1",
    display_id: "OPENFOX-1234",
    sequence: 1234,
    type_id: "t1",
    type_name: "任务",
    status_id: "st1",
    status_name: "待处理",
    status_category: "TODO",
    title: "无优先级任务行与日期列对齐验证",
    description: "",
    acceptance: "",
    priority: "NONE",
    version: 1,
    created_at: "2026-11-24T00:00:00Z",
    updated_at: "2026-11-27T08:00:00Z",
    labels: [],
    start_date: null,
    due_date: "2026-12-03",
    parent_task_id: null,
    parent: null,
    rank: "h1",
    assignees: [],
    label_items: [],
    subtask_count: 0,
    subtask_done_count: 0,
    created_by: "u1",
    type_icon: null,
    status_color: null,
    deleted: false,
    iteration_id: null,
    project_id: null,
    triage: null,
    duplicate_of: null,
  },
  {
    organization_id: "o1",
    space_id: "s1",
    id: "task-2",
    display_id: "OPENFOX-1235",
    sequence: 1235,
    type_id: "t1",
    type_name: "任务",
    status_id: "st1",
    status_name: "待处理",
    status_category: "TODO",
    title: "高优先级任务与长前缀 ID 完整展示",
    description: "",
    acceptance: "",
    priority: "HIGH",
    version: 1,
    created_at: "2026-11-25T00:00:00Z",
    updated_at: "2026-11-28T08:00:00Z",
    labels: [],
    start_date: null,
    due_date: null,
    parent_task_id: null,
    parent: null,
    rank: "h2",
    assignees: [],
    label_items: [],
    subtask_count: 0,
    subtask_done_count: 0,
    created_by: "u1",
    type_icon: null,
    status_color: null,
    deleted: false,
    iteration_id: null,
    project_id: null,
    triage: null,
    duplicate_of: null,
  },
];

const directoryEntries = [
  entry("a-online", {
    name: "前小端",
    title: "前端开发",
    space_id: "s1",
    space_name: "OpenFox 研发",
    online: true,
    computer_name: "MacBook-Pro",
    runtime: "codex",
    model: "default",
    pinned: true,
  }),
  entry("a-offline", {
    name: "后小端",
    title: "后端研发",
    space_id: "s1",
    space_name: "OpenFox 研发",
    online: false,
    computer_name: "Build-Mac",
    runtime: "claude",
    model: "claude-sonnet-4",
  }),
  entry("a-unbound", {
    name: "测试小助手",
    title: "QA",
    space_id: "s1",
    space_name: "OpenFox 研发",
    online: false,
    computer_name: "",
    runtime: "",
    model: "default",
  }),
  entry("c1", {
    type: "crew",
    name: "OpenFox 核心小队",
    space_id: "s1",
    space_name: "OpenFox 研发",
    member_count: 3,
    members: [{ id: "m1", name: "前小端", kind: "CODE", title: null }],
  }),
];

const unboundAgentDetail = {
  organization_id: "o1",
  space_id: "s1",
  id: "a-unbound",
  owner_user_id: "u1",
  name: "测试小助手",
  title: "QA",
  description: "",
  avatar: null,
  kind: "CODE",
  visibility: "SHARED",
  allow_assign: true,
  allow_direct_chat: true,
  trigger_crew: true,
  trigger_mention: true,
  trigger_workflow: false,
  profile_version: 1,
  online: false,
  computer_name: "",
  favorite: false,
  current_config_version: 0,
  version: 1,
  created_at: "2026-10-01T00:00:00Z",
  updated_at: "2026-10-01T00:00:00Z",
  config: null,
};

const conversation = {
  organization_id: "o1",
  id: "c1",
  space_id: "s1",
  agent_id: "a-unbound",
  owner_user_id: "u1",
  visibility: "PRIVATE",
  lifecycle: "OPEN",
  handoff: "NONE",
  origin: "USER",
  version: 1,
  created_at: "2026-11-28T00:00:00Z",
  updated_at: "2026-11-28T00:00:00Z",
  title: "窄屏顶栏折行与本地保存提示回归会话",
  archived_at: null,
  pinned: false,
  last_read_seq: 0,
  unread: false,
  last_message: null,
  agent: {
    id: "a-unbound",
    name: "测试小助手超长名称验证截断与悬停",
    title: "QA",
    avatar: null,
    online: false,
  },
  task: null,
  run: {
    id: "r1",
    state: "RUNNING",
    wait_reason: "",
    started_at: new Date().toISOString(),
    finished_at: null,
  },
  status: "WORKING",
  overrides: { runtime: null, model: null, approval_mode: null, version: 0 },
  effective: {
    runtime: "codex",
    model: "default",
    approval_mode: "FOLLOW_RUNTIME",
  },
  agent_defaults: {
    runtime: "codex",
    model: "default",
    approval_mode: "FOLLOW_RUNTIME",
  },
};

// UI-02：关联任务 task-1 的会话，Agent 名称与会话标题都很长，用于验证 AI 面板页头截断后的悬停全称
const linkedConversation = {
  ...conversation,
  id: "c2",
  title: "关联任务的超长会话标题用于验证 AI 面板页头截断后仍可悬停读全",
  task_id: "task-1",
  task: {
    id: "task-1",
    display_id: "OPENFOX-1234",
    title: "无优先级任务行与日期列对齐验证",
  },
};

async function snap(page: Page, name: string) {
  if (!captureDir) return;
  fs.mkdirSync(captureDir, { recursive: true });
  await page.screenshot({ path: path.join(captureDir, `${name}.png`) });
}

async function expectWindowFits(page: Page) {
  const overflow = await page.locator("[data-org-main]").evaluate((main) => {
    const root = document.documentElement;
    return [
      root.scrollWidth - root.clientWidth,
      root.scrollHeight - root.clientHeight,
      main.scrollWidth - main.clientWidth,
    ];
  });
  expect(overflow).toEqual([0, 0, 0]);
}

async function mockApp(
  page: Page,
  options: { locale?: "zh-CN" | "en-US"; theme?: "light" | "dark" } = {},
) {
  const locale = options.locale ?? "zh-CN";
  const theme = options.theme ?? "light";
  await page.addInitScript(() => {
    localStorage.setItem("openfox:onboarding:o1", JSON.stringify({}));
  });
  const data: Record<string, unknown> = {
    "/api/v1/me": { id: "u1", email: "alice@openfox.dev", full_name: "Alice" },
    "/api/v1/me/organizations": {
      organizations: [
        { id: "o1", name: "OpenFox", slug: "openfox", role: "OWNER" },
      ],
    },
    "/api/v1/me/preferences": { locale, theme },
    [org + "/spaces"]: { spaces: [space] },
    [org + "/spaces/s1"]: space,
    [org + "/spaces/s1/statuses"]: { statuses },
    [org + "/task-statuses"]: { statuses },
    [org + "/statuses"]: { statuses },
    [org + "/spaces/s1/task-types"]: {
      task_types: [
        {
          id: "t1",
          name: "任务",
          enabled: true,
          is_default: true,
          statuses: statuses.map((s, i) => ({
            status_id: s.id,
            name: s.name,
            category: s.category,
            position: i,
            default: i === 0,
          })),
        },
      ],
    },
    [org + "/task-types"]: {
      spaces: [
        {
          space_id: "s1",
          space_name: space.name,
          space_prefix: space.prefix,
          task_types: [{ id: "t1", name: "任务", field_ids: [] }],
        },
      ],
    },
    [org + "/spaces/s1/labels"]: { labels: [] },
    [org + "/labels"]: { labels: [] },
    [org + "/spaces/s1/views"]: { views: [] },
    [org + "/spaces/s1/tasks/counts"]: {
      total: tasks.length,
      by_status: [{ status_id: "st1", count: tasks.length }],
    },
    [org + "/spaces/s1/tasks"]: { tasks, next_cursor: null },
    [org + "/my-tasks/counts"]: { total: 0, by_status: [] },
    [org + "/my-tasks"]: { tasks: [], next_cursor: null },
    [org + "/members"]: {
      members: [
        {
          user_id: "u1",
          email: "alice@openfox.dev",
          role: "OWNER",
          status: "ACTIVE",
        },
      ],
      next_cursor: "",
    },
    [org + "/devices"]: { devices: [], next_cursor: null },
    [org + "/agent-directory"]: {
      entries: directoryEntries,
      archived_count: 0,
      truncated: false,
    },
    [org + "/spaces/s1/agents/a-unbound"]: unboundAgentDetail,
    [org + "/spaces/s1/agents/a-unbound/stats"]: {
      task_count: 0,
      run_count: 0,
      input_tokens: 0,
      output_tokens: 0,
      last_run_at: null,
    },
    [org + "/spaces/s1/agents/a-unbound/repos"]: { repos: [] },
    [org + "/spaces/s1/agents/a-unbound/env-vars"]: {
      env_vars: [],
      can_view_values: true,
      secrets_available: true,
    },
    [org + "/conversations"]: { conversations: [], next_cursor: null },
    [org + "/conversations/c1"]: conversation,
    [org + "/conversations/c1/messages"]: { messages: [], next_cursor: 0 },
    [org + "/conversations/c1/artifacts"]: { artifacts: [] },
    [org + "/conversations/c2"]: linkedConversation,
    [org + "/conversations/c2/messages"]: { messages: [], next_cursor: 0 },
    [org + "/conversations/c2/artifacts"]: { artifacts: [] },
    [org + "/tasks/by-display-id/OPENFOX-1234"]: {
      ...tasks[0],
      description_reactions: [],
    },
    [org + "/spaces/s1/tasks/task-1/timeline"]: {
      items: [],
      next_cursor: null,
    },
    [org + "/spaces/s1/tasks/task-1/followers"]: {
      followers: [],
      following: false,
    },
    [org + "/spaces/s1/tasks/task-1/relations"]: { relations: [] },
    [org + "/spaces/s1/tasks/task-1/links"]: { links: [] },
    [org + "/spaces/s1/tasks/task-1/fields"]: { fields: [] },
    [org + "/task-relation-types"]: { relation_types: [] },
    [org + "/direct-chats"]: { chats: [], next_before: "" },
    [org + "/inbox/stats"]: {
      counts: {
        inbox: 0,
        inbox_unread: 0,
        inbox_important_unread: 0,
        inbox_other_unread: 0,
        later: 0,
        cleared: 0,
      },
    },
    [org + "/inbox"]: {
      groups: [],
      counts: {
        inbox: 0,
        inbox_unread: 0,
        inbox_important_unread: 0,
        inbox_other_unread: 0,
        later: 0,
        cleared: 0,
      },
    },
    [org + "/market/catalog"]: {
      source: "hifox",
      catalog_version: "1",
      imported_at: "2026-10-01T00:00:00Z",
      categories: [],
      packs: [],
      counts: { skills: 0, templates: 0 },
    },
    [org + "/market/skills"]: { items: [], total: 0, next_cursor: null },
    [org + "/get-started"]: { score: 0, total: 60, categories: [] },
  };
  await page.route("**/api/**", async (route) => {
    const pathname = new URL(route.request().url()).pathname;
    await route.fulfill(
      pathname in data
        ? { json: data[pathname] }
        : {
            status: 404,
            json: { error: { code: "NOT_FOUND", message: "fixture" } },
          },
    );
  });
}

test("OPE-24 light desktop: inbox, tasks, agents, market, and unbound chat", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await mockApp(page, { locale: "zh-CN", theme: "light" });

  // 1. 收件箱 + 首页侧栏整理
  await page.goto("/o/o1/inbox");
  await expect(
    page.getByRole("heading", { level: 1, name: "收件箱" }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "前往我的任务" }),
  ).toBeVisible();
  const nav = page.getByRole("navigation", { name: "页面导航" });
  await expect(nav.getByRole("link", { name: "组织 Agent" })).toBeVisible();
  await expect(nav.getByRole("link", { name: "更多设置" })).toHaveCount(0);
  await expect(nav.getByText("AI 队友")).toHaveCount(0);
  await expect(nav.getByRole("link", { name: "查看全部对话" })).toHaveCount(0);
  const newChatBtn = nav.getByRole("button", { name: "新建对话" });
  await expect(newChatBtn).toHaveCount(1);
  await expect(newChatBtn).toBeVisible();
  // 「+」按钮位于「对话」分组标题同一行，而不是空态文案下方
  const chatLabelBox = await nav
    .getByText("对话", { exact: true })
    .boundingBox();
  const newChatBox = await newChatBtn.boundingBox();
  expect(chatLabelBox).not.toBeNull();
  expect(newChatBox).not.toBeNull();
  expect(Math.abs(newChatBox!.y - chatLabelBox!.y)).toBeLessThanOrEqual(8);
  await expect(nav.getByText("暂无 Agent 会话")).toBeVisible();
  await expect(nav.getByRole("button", { name: "更多任务项…" })).toBeVisible();
  await expect(nav.getByRole("button", { name: "空间设置" })).toBeVisible();
  await expectWindowFits(page);
  await snap(page, "light-inbox-sidebar");

  // 2. 空间任务页：视图来源稳定 + 外露布局分段切换 + 工具栏 Tooltip + 列表日期列头/对齐
  await page.goto("/o/o1/spaces/s1/tasks");
  await expect(
    page.getByRole("button", { name: "空间默认", exact: true }),
  ).toHaveAttribute("aria-pressed", "true");
  await expect(
    page.getByRole("button", { name: "新建任务" }).first(),
  ).toBeVisible();
  await expect(page.getByText("OPENFOX-1234")).toBeVisible();
  await snap(page, "light-space-tasks-board");

  const layoutGroup = page.getByRole("group", { name: "布局" });
  await layoutGroup.getByRole("button", { name: "列表" }).click();
  await expect(
    page.getByRole("button", { name: "空间默认", exact: true }),
  ).toHaveAttribute("aria-pressed", "true");
  const listRegion = page.getByRole("region", { name: "任务列表" });
  await expect(listRegion.getByText("截止日期")).toBeVisible();
  await expect(listRegion.getByText("创建日期")).toBeVisible();
  await expect(listRegion.getByText("OPENFOX-1234")).toBeVisible();
  await expect(listRegion.getByRole("img", { name: "无优先级" })).toBeVisible();

  // 工具栏筛选图标悬停显示 Tooltip
  await page.getByRole("button", { name: "筛选" }).hover();
  await expect(page.getByRole("tooltip")).toHaveText("筛选");
  await expectWindowFits(page);
  await snap(page, "light-space-tasks-list");

  // 3. 我的任务页统一顶栏
  await page.goto("/o/o1/my-tasks");
  await expect(
    page.getByRole("heading", { level: 1, name: "我的任务" }),
  ).toBeVisible();
  await expectWindowFits(page);

  // 4. 组织 Agent 页：统一页头 + 在线/离线/未绑定电脑状态 + 绑定电脑按钮
  await page.goto("/o/o1/agents");
  await expect(
    page.getByRole("heading", { level: 1, name: "组织 Agent 和小队" }),
  ).toBeVisible();
  await expect(page.getByText("在线 · MacBook-Pro")).toBeVisible();
  await expect(page.getByText("离线 · Build-Mac")).toBeVisible();
  await expect(page.getByText("未绑定电脑")).toBeVisible();
  await expect(page.getByText("默认 · 你")).toHaveCount(0);
  await expectWindowFits(page);
  await snap(page, "light-agents");

  await page.getByRole("button", { name: "绑定电脑" }).click();
  await expect(
    page.getByRole("complementary", { name: "测试小助手 的设置" }),
  ).toBeVisible();

  // 5. 市场空目录：统一页头 + 下一步指引
  await page.goto("/o/o1/market");
  await expect(
    page.getByRole("heading", { level: 1, name: "市场" }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "前往组织 Agent" }),
  ).toBeVisible();
  await expectWindowFits(page);
  await snap(page, "light-market-empty");

  // 6. 未绑定 Agent 对话：P0-1 复验「绑定电脑」，P0-4 无空挂载保存提示
  await page.goto("/o/o1/chat/c1");
  await expect(page.getByRole("button", { name: "绑定电脑" })).toBeVisible();
  await expect(page.getByText("正在保存到本机…")).toHaveCount(0);
  await expectWindowFits(page);
  await snap(page, "light-chat-unbound");
});

test("OPE-24 dark desktop: tasks list, agents, and inbox", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await mockApp(page, { locale: "zh-CN", theme: "dark" });

  await page.goto("/o/o1/spaces/s1/tasks");
  await page
    .getByRole("group", { name: "布局" })
    .getByRole("button", { name: "列表" })
    .click();
  await expect(page.getByText("OPENFOX-1234")).toBeVisible();
  await expectWindowFits(page);
  await snap(page, "dark-space-tasks-list");

  await page.goto("/o/o1/agents");
  await expect(page.getByText("未绑定电脑")).toBeVisible();
  await expectWindowFits(page);
  await snap(page, "dark-agents");

  await page.goto("/o/o1/inbox");
  await expect(
    page.getByRole("heading", { level: 1, name: "收件箱" }),
  ).toBeVisible();
  await expectWindowFits(page);
  await snap(page, "dark-inbox");
});

test("OPE-24 narrow viewports (<700px & 960x640 wide sidebar): zero vertical wrap & zero overflow", async ({
  page,
}) => {
  await mockApp(page, { locale: "zh-CN", theme: "light" });

  // <700px 窄屏检查对话顶栏与任务工作区
  await page.setViewportSize({ width: 680, height: 800 });
  await page.goto("/o/o1/chat/c1");
  const settingsBtn = page.getByRole("button", { name: "设置" });
  await expect(settingsBtn).toBeVisible();
  const btnBox = await settingsBtn.boundingBox();
  expect(btnBox).not.toBeNull();
  // 单行按钮或图标按钮高度不超过 34px，绝不出现双行竖排
  expect(btnBox!.height).toBeLessThanOrEqual(34);
  const statusBadge = page.getByText(/工作中/);
  const statusBox = await statusBadge.boundingBox();
  expect(statusBox!.height).toBeLessThanOrEqual(32);
  await expectWindowFits(page);
  await snap(page, "narrow-680-chat-header");

  await page.goto("/o/o1/spaces/s1/tasks");
  await expect(
    page.getByRole("button", { name: "空间默认", exact: true }),
  ).toBeVisible();
  await expectWindowFits(page);
  await snap(page, "narrow-680-space-tasks");

  // 960x640 + 480px 最宽侧栏
  await page.setViewportSize({ width: 960, height: 640 });
  await page.goto("/o/o1/chat/c1");
  const handle = page.locator(
    '[role="separator"][aria-controls="org-sidebar-panel"]',
  );
  await handle.press("End");
  await expect(page.locator("#org-sidebar-panel")).toHaveCSS("width", "480px");
  const wideSidebarBtnBox = await page
    .getByRole("button", { name: "设置" })
    .boundingBox();
  expect(wideSidebarBtnBox!.height).toBeLessThanOrEqual(34);
  await expectWindowFits(page);
  await snap(page, "narrow-960-wide-sidebar-chat");
});

test("OPE-24 en-US narrow desktop (960x640): i18n parity and zero overflow", async ({
  page,
}) => {
  await page.setViewportSize({ width: 960, height: 640 });
  await mockApp(page, { locale: "en-US", theme: "light" });

  await page.goto("/o/o1/agents");
  await expect(
    page.getByRole("heading", {
      level: 1,
      name: "Organization agents and crews",
    }),
  ).toBeVisible();
  await expect(page.getByText("No computer")).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Bind computer" }),
  ).toBeVisible();
  await expectWindowFits(page);
  await snap(page, "en-agents");

  await page.goto("/o/o1/spaces/s1/tasks");
  await expect(
    page.getByRole("button", { name: "Space default", exact: true }),
  ).toHaveAttribute("aria-pressed", "true");
  await expect(
    page.getByRole("button", { name: "Personal view", exact: true }),
  ).toBeVisible();
  await expectWindowFits(page);
  await snap(page, "en-space-tasks");
});

test("OPE-24 UI-02: linked-task AI panel header exposes the full title at 700px and 900px", async ({
  page,
}) => {
  await mockApp(page, { locale: "zh-CN", theme: "light" });
  const fullTitle = `${linkedConversation.agent.name} · ${linkedConversation.title}`;

  for (const width of [700, 900]) {
    await page.setViewportSize({ width, height: 800 });
    await page.goto("/o/o1/tasks/OPENFOX-1234?view=chat&conversationId=c2");
    const panel = page.getByRole("complementary", { name: "AI 面板" });
    await expect(panel).toBeVisible();
    // 标题被 truncate 截断后，悬停提示必须带「名称 · 会话标题」全文
    await expect(
      panel.locator("header").first().getByTitle(fullTitle, { exact: true }),
    ).toBeVisible();
    await expectWindowFits(page);
    await snap(page, `ui02-linked-panel-${width}`);
  }
});
