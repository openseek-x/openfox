import { expect, test, type BrowserContext } from "@playwright/test";

const orgId = "density-org";
const base = `/api/v1/organizations/${orgId}`;
const settingsPath = `/o/${orgId}/settings/preferences`;
const tasksPath = `/o/${orgId}/spaces/s1/tasks`;
const timestamp = "2026-11-08T00:00:00Z";

function account(locale: "zh-CN" | "en-US", theme: "light" | "dark") {
  return {
    prefs: {
      density: "comfortable",
      theme,
      locale,
      outline_color: 0,
      open_session_on_assign: true,
      open_session_on_quick_command: true,
      send_shortcut: "enter",
      command_shortcuts: {},
      version: 1,
    },
    writes: [] as Record<string, unknown>[],
    failed: false,
  };
}

async function fixture(
  context: BrowserContext,
  state: ReturnType<typeof account>,
) {
  const status = {
    id: "st1",
    name: "待处理",
    category: "TODO",
    position: 0,
    version: 1,
    created_at: timestamp,
    updated_at: timestamp,
    is_default: true,
    collapsed_by_default: false,
    task_count: 80,
    type_usage: [],
  };
  const space = {
    id: "s1",
    name: "密度验收空间",
    prefix: "QA",
    visibility: "shared",
    version: 1,
    created_at: timestamp,
    updated_at: timestamp,
    features: {
      views: false,
      projects: false,
      iterations: false,
      triage: false,
    },
    status: "active",
    retired_at: null,
    my_role: "OWNER",
    can_manage: true,
    estimation: {
      scale: "notUsed",
      extended: false,
      allow_zero: false,
      count_unestimated: false,
    },
  };
  const tasks = Array.from({ length: 80 }, (_, index) => ({
    organization_id: orgId,
    space_id: "s1",
    id: `task-${index + 1}`,
    display_id: `QA-${index + 1}`,
    sequence: index + 1,
    title: `Density task ${index + 1}`,
    type_id: "t1",
    type_name: "任务",
    type_icon: null,
    status_id: "st1",
    status_name: "待处理",
    status_category: "TODO",
    status_color: null,
    description: "",
    acceptance: "",
    priority: "NONE",
    version: 1,
    created_at: timestamp,
    updated_at: timestamp,
    created_by: "u1",
    labels: [],
    label_items: [],
    assignees: [],
    rank: `r${index}`,
    parent_task_id: null,
    parent: null,
    subtask_count: 0,
    subtask_done_count: 0,
    start_date: "2026-11-08",
    due_date: "2026-11-10",
    deleted: false,
    project_id: null,
    iteration_id: null,
    triage: null,
    duplicate_of: null,
  }));
  await context.addInitScript((organization) => {
    localStorage.setItem(`openfox:onboarding:${organization}`, "{}");
  }, orgId);
  await context.route("**/api/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    let body: unknown;
    let statusCode = 200;
    if (path === "/api/v1/me")
      body = { id: "u1", email: "density@example.test" };
    else if (path === "/api/v1/me/organizations")
      body = {
        organizations: [
          { id: orgId, slug: "qa", name: "Density QA", role: "OWNER" },
        ],
      };
    else if (path === "/api/v1/me/preferences") {
      if (request.method() === "PATCH") {
        const patch = request.postDataJSON();
        state.writes.push(patch);
        if (state.failed || patch.version !== state.prefs.version) {
          await route.fulfill({
            status: state.failed ? 500 : 409,
            json: {
              error: {
                code: state.failed ? "INTERNAL_ERROR" : "VERSION_CONFLICT",
                message: "fixture",
                request_id: "density",
              },
            },
          });
          return;
        }
        state.prefs = {
          ...state.prefs,
          ...patch,
          version: state.prefs.version + 1,
        };
      }
      body = state.prefs;
    } else if (path === `${base}/spaces`) body = { spaces: [space] };
    else if (path === `${base}/spaces/s1`) body = space;
    else if (path === `${base}/task-statuses`) body = { statuses: [status] };
    else if (path === `${base}/spaces/s1/task-types`)
      body = {
        task_types: [
          {
            id: "t1",
            name: "任务",
            enabled: true,
            is_default: true,
            statuses: [
              {
                status_id: "st1",
                name: "待处理",
                category: "TODO",
                position: 0,
                default: true,
              },
            ],
          },
        ],
      };
    else if (path.endsWith("/tasks/counts"))
      body = {
        total: tasks.length,
        by_status: [{ status_id: "st1", count: tasks.length }],
      };
    else if (path.endsWith("/tasks")) body = { tasks, next_cursor: "" };
    else if (path.endsWith("/members")) body = { members: [], next_cursor: "" };
    else if (path.endsWith("/agents")) body = { agents: [] };
    else if (path.endsWith("/agent-directory"))
      body = { entries: [], archived_count: 0, truncated: false };
    else if (path.endsWith("/labels")) body = { labels: [] };
    else if (path.endsWith("/conversations"))
      body = { conversations: [], next_cursor: null };
    else if (path.endsWith("/direct-chats"))
      body = { chats: [], next_before: "" };
    else if (path.endsWith("/inbox/stats"))
      body = {
        counts: {
          inbox: 0,
          inbox_unread: 0,
          inbox_important_unread: 0,
          inbox_other_unread: 0,
          later: 0,
          cleared: 0,
        },
      };
    else {
      statusCode = 404;
      body = {
        error: { code: "NOT_FOUND", message: "fixture", request_id: "density" },
      };
    }
    await route.fulfill({ status: statusCode, json: body });
  });
}

for (const [locale, theme] of [
  ["zh-CN", "light"],
  ["en-US", "dark"],
] as const) {
  test(`account density sync, rollback and layouts ${locale} ${theme}`, async ({
    browser,
    page,
  }, testInfo) => {
    const state = account(locale, theme);
    await fixture(page.context(), state);
    const secondContext = await browser.newContext({
      baseURL: testInfo.project.use.baseURL,
      viewport: { width: 1280, height: 800 },
      locale,
    });
    try {
      await fixture(secondContext, state);
      const second = await secondContext.newPage();
      await second.goto(`${tasksPath}?view=list`);
      const listRows = second.locator("li[aria-selected]");
      await expect(listRows).toHaveCount(80);
      await expect(listRows.first()).toHaveCSS("height", "44px");
      await page.goto(settingsPath);
      const label = locale === "zh-CN" ? "密度" : "Density";
      const compact = locale === "zh-CN" ? "紧凑" : "Compact";
      const comfortable = locale === "zh-CN" ? "标准" : "Comfortable";
      await page.getByRole("button", { name: label, exact: true }).click();
      await page.getByRole("option", { name: compact, exact: true }).click();
      await expect.poll(() => state.prefs.density).toBe("compact");
      expect(state.writes[0]).toEqual({ density: "compact", version: 1 });
      await page.reload();
      await expect(page.locator("html")).toHaveAttribute(
        "data-density",
        "compact",
      );
      await page.screenshot({
        path: testInfo.outputPath("density-settings.png"),
      });
      await second.bringToFront();
      await second.evaluate(() => window.dispatchEvent(new Event("focus")));
      await expect(listRows.first()).toHaveCSS("height", "36px");
      await listRows.last().scrollIntoViewIfNeeded();
      await expect(listRows.last()).toBeInViewport();
      await expect(
        listRows
          .last()
          .getByRole("button", { name: "QA-80 Density task 80", exact: true }),
      ).toBeVisible();
      await second.screenshot({
        path: testInfo.outputPath("density-list.png"),
      });
      await second.goto(`${tasksPath}?view=table`);
      await expect(second.locator("tbody tr")).toHaveCount(80);
      await expect(second.locator("tbody td").first()).toHaveCSS(
        "height",
        "32px",
      );
      await second.goto(`${tasksPath}?view=board`);
      await expect(second.locator("article").first()).toHaveCSS(
        "padding-top",
        "8px",
      );
      await second.goto(`${tasksPath}?view=gantt`);
      await expect(second.getByTestId("gantt-row-task-1")).toHaveCSS(
        "height",
        "32px",
      );
      await expect(second.getByTestId("gantt-bar-task-1")).toHaveCSS(
        "height",
        "20px",
      );
      await second.getByTestId("gantt-row-task-80").scrollIntoViewIfNeeded();
      await expect(second.getByTestId("gantt-bar-task-80")).toBeInViewport();
      const expectedBackground =
        theme === "dark" ? "rgb(39, 39, 42)" : "rgb(255, 255, 255)";
      await expect(
        second.getByTestId("gantt-scroller").locator(".sticky.top-0"),
      ).toHaveCSS("background-color", expectedBackground);
      await expect(
        second.getByTestId("gantt-row-task-80").getByRole("button").first(),
      ).toHaveCSS("background-color", expectedBackground);
      await second.screenshot({
        path: testInfo.outputPath("density-gantt.png"),
      });
      state.failed = true;
      await page.bringToFront();
      await page.getByRole("button", { name: label, exact: true }).click();
      await page
        .getByRole("option", { name: comfortable, exact: true })
        .click();
      await expect.poll(() => state.writes.length).toBe(2);
      await expect(
        page.getByRole("button", { name: label, exact: true }),
      ).toHaveText(compact);
      await expect(page.locator("html")).toHaveAttribute(
        "data-density",
        "compact",
      );
      state.failed = false;
      await page.getByRole("button", { name: label, exact: true }).click();
      await page
        .getByRole("option", { name: comfortable, exact: true })
        .click();
      await expect.poll(() => state.prefs.density).toBe("comfortable");
      await second.bringToFront();
      await second.evaluate(() => window.dispatchEvent(new Event("focus")));
      await expect(second.getByTestId("gantt-row-task-80")).toHaveCSS(
        "height",
        "36px",
      );
      await expect(second.getByTestId("gantt-bar-task-80")).toHaveCSS(
        "height",
        "24px",
      );
      await second.screenshot({
        path: testInfo.outputPath("density-gantt-standard.png"),
      });
    } finally {
      await secondContext.close();
    }
  });
}
