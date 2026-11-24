import { expect, test, type Page } from "@playwright/test";

import { entry } from "../../../packages/app/src/agents/fixtures";

test.use({ viewport: { width: 960, height: 640 } });

const org = "/api/v1/organizations/o1";
const space = {
  id: "s1",
  name: "研发",
  prefix: "DEV",
  visibility: "shared",
  my_role: "OWNER",
  can_manage: true,
  status: "active",
  created_at: "2026-10-01T00:00:00Z",
  features: { iterations: true },
};

async function mockPage(page: Page, responses: Record<string, unknown>) {
  const data: Record<string, unknown> = {
    "/api/v1/me": { id: "u1", email: "test@example.test" },
    "/api/v1/me/organizations": {
      organizations: [{ id: "o1", name: "研发", slug: "dev", role: "OWNER" }],
    },
    "/api/v1/me/preferences": { locale: "zh-CN", theme: "light" },
    [org + "/spaces"]: { spaces: [space] },
    [org + "/conversations"]: { conversations: [], next_cursor: null },
    [org + "/direct-chats"]: { chats: [], next_before: "" },
    [org + "/inbox/stats"]: { counts: {} },
    ...responses,
  };
  await page.route("**/api/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    await route.fulfill(
      path in data
        ? { json: data[path] }
        : {
            status: 404,
            json: { error: { code: "NOT_FOUND", message: "fixture" } },
          },
    );
  });
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

async function widenSidebar(page: Page) {
  const handle = page.locator(
    '[role="separator"][aria-controls="org-sidebar-panel"]',
  );
  await handle.press("End");
  await expect(page.locator("#org-sidebar-panel")).toHaveCSS("width", "480px");
}

for (const type of ["agent", "crew"] as const) {
  test(`private ${type} table stays inside the settings scroller`, async ({
    page,
  }) => {
    const entries = Array.from({ length: 20 }, (_, i) =>
      entry(type + i, { type, visibility: i === 19 ? "PRIVATE" : "SHARED" }),
    );
    await mockPage(page, {
      [org + "/agent-directory"]: {
        entries,
        archived_count: 0,
        truncated: false,
      },
    });
    await page.goto("/o/o1/settings/" + type + "s");
    const last = page.getByText(type + "19", { exact: true });
    await widenSidebar(page);
    await last.scrollIntoViewIfNeeded();
    await expect(last).toBeInViewport();
    await expectWindowFits(page);
  });
}

test("last iteration menu is reachable without scrolling the window", async ({
  page,
}) => {
  const iterations = Array.from({ length: 25 }, (_, i) => ({
    id: "i" + i,
    display_name: "周期" + i,
    number: i + 1,
    start_date: "2026-10-01",
    end_date: "2026-10-08",
    status: "planned",
    live: { scope_count: 0, completed_count: 0 },
    capacity_percent: null,
    version: 1,
  }));
  const base = org + "/spaces/s1";
  await mockPage(page, {
    [base]: space,
    [base + "/iteration-settings"]: {
      enabled: true,
      mode: "manual",
      timezone: "UTC",
    },
    [base + "/iterations"]: { iterations, next_cursor: "" },
  });
  await page.goto("/o/o1/spaces/s1/iterations");
  const last = page.getByRole("listitem").filter({ hasText: "周期24" });
  await last.getByRole("button", { name: "迭代", exact: true }).click();
  await expect(page.getByRole("menu")).toBeVisible();
  await expectWindowFits(page);
});

test("the labels table owns horizontal scrolling at the minimum desktop width", async ({
  page,
}) => {
  await mockPage(page, {
    [org + "/label-groups"]: { label_groups: [] },
    [org + "/labels"]: {
      labels: [
        {
          id: "l1",
          name: "Bug",
          color: "#16a34a",
          task_count: 0,
          created_at: "2026-10-01T00:00:00Z",
        },
      ],
    },
  });
  await page.goto("/o/o1/settings/labels");
  const menu = page.getByRole("button", { name: "Bug 的操作" });
  await menu.scrollIntoViewIfNeeded();
  await expect(menu).toBeInViewport();
  await expectWindowFits(page);
});

test("the last task type can be selected without extending the modal backdrop", async ({
  page,
}) => {
  await mockPage(page, {
    [org + "/custom-fields"]: { custom_fields: [] },
    [org + "/task-types"]: {
      spaces: Array.from({ length: 24 }, (_, i) => ({
        space_id: "s" + i,
        space_name: "空间" + i,
        space_prefix: "QA",
        task_types: [{ id: "t1", name: "任务", field_ids: [] }],
      })),
    },
  });
  await page.route("**/custom-fields", async (route) => {
    if (route.request().method() !== "POST") return route.fallback();
    await route.fulfill({
      status: 201,
      json: { id: "f1", name: "测试", kind: "TEXT", options: [], version: 1 },
    });
  });
  await page.goto("/o/o1/settings/custom-fields");
  await page.getByRole("button", { name: "添加自定义字段" }).first().click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("名称", { exact: true }).fill("测试");
  await dialog.getByRole("button", { name: "保存" }).click();
  const last = dialog
    .getByRole("group", { name: "空间23", exact: true })
    .getByRole("checkbox", { name: "任务", exact: true });
  await last.check();
  await expect(last).toBeChecked();
  const backdropOverflow = await dialog.evaluate((el) => {
    const backdrop = el.parentElement!;
    return backdrop.scrollHeight - backdrop.clientHeight;
  });
  expect(backdropOverflow).toBe(0);
});

test("completed sidebar guide steps stay inside the navigation scroller", async ({
  page,
}) => {
  await mockPage(page, {
    [org + "/get-started"]: {
      categories: [{ steps: [{ key: "create_automation", completed: true }] }],
    },
    [org + "/market/catalog"]: {
      categories: [],
      packs: [],
      counts: { skills: 0, templates: 0 },
    },
    [org + "/market/skills"]: { items: [], total: 0, next_cursor: null },
  });
  await page.goto("/o/o1/market");
  await page.getByRole("button", { name: "展开进阶指引" }).click();
  await expect(
    page.getByRole("link", { name: /创建一个 Agent 自动化.*已完成/ }),
  ).toBeInViewport();
  await expectWindowFits(page);
});

test("the organization menu scrolls to both ends by keyboard", async ({
  page,
}) => {
  const organizations = Array.from({ length: 31 }, (_, i) => ({
    id: "o" + (i + 1),
    name: "研发组织" + i,
    role: "OWNER",
  }));
  await mockPage(page, { "/api/v1/me/organizations": { organizations } });
  await page.goto("/o/o1/inbox");
  await page.getByRole("button", { name: /研发组织0/ }).click();
  const menu = page.getByRole("menu", { name: "切换组织" });
  const first = menu.getByRole("menuitemradio").first();
  const last = menu.getByRole("menuitem").last();
  await expect(menu).toBeInViewport({ ratio: 1 });
  await first.focus();
  await first.press("End");
  await expect(last).toBeFocused();
  await expect(last).toBeInViewport({ ratio: 1 });
  await last.press("Home");
  await expect(first).toBeFocused();
  await expect(first).toBeInViewport({ ratio: 1 });
  await expectWindowFits(page);
});

test("all outline colors remain reachable with the widest sidebar", async ({
  page,
}) => {
  await mockPage(page, {});
  await page.goto("/o/o1/settings/preferences");
  await widenSidebar(page);
  const colors = page.getByRole("radiogroup", { name: "外边框颜色" });
  const last = colors.getByRole("radio").last();
  await last.focus();
  await expect(last).toBeInViewport({ ratio: 1 });
  await expectWindowFits(page);
});

test("space settings keep all columns inside the table scroller", async ({
  page,
}) => {
  await mockPage(page, {});
  await page.goto("/o/o1/settings/spaces");
  await widenSidebar(page);
  const table = page.getByRole("table");
  const action = table.getByRole("row").last().getByRole("button").last();
  await action.scrollIntoViewIfNeeded();
  await expect(action).toBeInViewport({ ratio: 1 });
  expect(await table.evaluate((el) => el.scrollLeft)).toBeGreaterThan(0);
  await expectWindowFits(page);
});

test("market pack titles and two-line descriptions fit the narrow content panel", async ({
  page,
}) => {
  const description = "可折行的市场卡片描述".repeat(40);
  const packs = Array.from({ length: 4 }, (_, n) => ({
    id: "p" + n,
    name: { zh: "精选包" + n },
    tint: "#33bbff",
    members: Array.from({ length: 4 }, (_, i) => ({
      market_skill_id: "s" + i,
      name: { zh: "技能" + i },
      available: true,
    })),
  }));
  await mockPage(page, {
    [org + "/market/catalog"]: {
      categories: [],
      packs,
      counts: { skills: 1, templates: 1 },
    },
    [org + "/market/skills"]: {
      items: [
        {
          market_skill_id: "s0",
          name: { zh: "技能" },
          description: { zh: description },
          tags: [],
          publisher: { name: "发布者" },
          downloads: 1,
          stars: 1,
        },
      ],
      total: 1,
      next_cursor: null,
    },
    [org + "/market/templates"]: {
      items: [
        {
          market_template_id: "t0",
          name: { zh: "模板" },
          tagline: { zh: description },
          keywords: [],
          skill_count: 1,
        },
      ],
      total: 1,
      next_cursor: null,
    },
  });
  await page.goto("/o/o1/market");
  await widenSidebar(page);
  const titles = page.getByRole("article").getByRole("heading");
  await expect(titles).toHaveCount(3);
  for (const title of await titles.all()) await expect(title).toBeVisible();
  for (const [tab, card] of [
    ["技能", "查看技能 技能"],
    ["Agent 模板", "查看 Agent 模板 模板"],
  ]) {
    await page.getByRole("radio", { name: tab, exact: true }).click();
    const text = page
      .getByRole("button", { name: card, exact: true })
      .getByText(description, { exact: true });
    await expect(text).toBeVisible();
    const metrics = await text.evaluate((el) => ({
      height: el.clientHeight,
      full: el.scrollHeight,
      line: parseFloat(getComputedStyle(el).lineHeight),
    }));
    expect(metrics.height).toBe(metrics.line * 2);
    expect(metrics.full).toBeGreaterThan(metrics.height);
    await expectWindowFits(page);
  }
});

test("space navigation and Gantt controls fit as the content panel changes width", async ({
  page,
}) => {
  const namedSpace = { ...space, name: "W".repeat(32) };
  await mockPage(page, {
    [org + "/spaces"]: { spaces: [namedSpace] },
    [org + "/spaces/s1"]: namedSpace,
    [org + "/agent-directory"]: {
      entries: [entry("ace")],
      archived_count: 0,
      truncated: false,
    },
    [org + "/spaces/s1/tasks/counts"]: {
      total: 1,
      by_status: [{ status_id: "st1", count: 1 }],
    },
    [org + "/spaces/s1/tasks"]: {
      tasks: [
        {
          id: "task1",
          display_id: "DEV-1",
          title: "测试任务",
          type_id: "t1",
          status_id: "st1",
          status_category: "TODO",
          start_date: null,
          due_date: null,
          assignees: [],
          label_items: [],
        },
      ],
      next_cursor: null,
    },
  });
  await page.goto("/o/o1/spaces/s1/agents");
  await widenSidebar(page);
  const card = page.getByTestId("directory-card-ace");
  await expect(card).toBeVisible();
  await expect(card.getByText("ace", { exact: true })).toBeVisible();
  await expectWindowFits(page);
  await page.goto("/o/o1/spaces/s1/docs");
  const nav = page.locator("main aside");
  await expect(nav).toHaveCSS("width", "240px");
  await expectWindowFits(page);
  await page.setViewportSize({ width: 1440, height: 900 });
  await expect(nav).toHaveCSS("width", "300px");
  await page.setViewportSize({ width: 960, height: 640 });
  await page.goto("/o/o1/spaces/s1/tasks?view=gantt");
  const toggle = page.getByRole("switch", { name: "显示已完成" });
  await expect(toggle).toBeInViewport({ ratio: 1 });
  await toggle.click();
  await expectWindowFits(page);
});

test("English iteration actions and profile controls fit beside the widest sidebar", async ({
  page,
}) => {
  const namedSpace = { ...space, name: "W".repeat(32) };
  await mockPage(page, {
    "/api/v1/me/preferences": { locale: "en-US", theme: "light" },
    [org + "/spaces"]: { spaces: [namedSpace] },
    [org + "/spaces/s1"]: namedSpace,
    [org + "/spaces/s1/iteration-settings"]: {
      enabled: true,
      mode: "manual",
      timezone: "UTC",
    },
    [org + "/spaces/s1/iterations"]: { iterations: [], next_cursor: "" },
  });
  await page.goto("/o/o1/spaces/s1/iterations");
  await widenSidebar(page);
  const create = page.getByRole("button", { name: "New iteration" }).first();
  await expect(create).toBeInViewport({ ratio: 1 });
  await expectWindowFits(page);
  await page.goto("/o/o1/settings/profile");
  await expect(
    page.getByRole("heading", { name: "Profile", exact: true }),
  ).toBeVisible();
  await expectWindowFits(page);
});

test("settings pages fit a resizable panel with live fixture data", async ({
  page,
}) => {
  test.skip(
    !process.env.OPENFOX_LIVE_OTP_CODE,
    "Requires the disposable frontend live fixture",
  );
  const orgId = "00000000-0000-0000-0000-0000000000a1";
  const spaceId = "00000000-0000-0000-0000-0000000000aa";
  await page.goto("/login");
  await page.getByLabel("邮箱", { exact: true }).fill("alice@example.test");
  await page.getByRole("button", { name: "继续" }).click();
  await page
    .getByLabel("第 1 位")
    .pressSequentially(process.env.OPENFOX_LIVE_OTP_CODE!);
  await page.waitForURL("**/o/**");
  await widenSidebar(page);
  const pages = [
    ["get-started", "aside [role=img]"],
    ["account-binding", "[data-testid=binding-bitbucket]"],
    ["computers", "section h2"],
    ["agents", "section h2"],
    ["integrations/yunxiao", "section button"],
    ["spaces/" + spaceId + "/labels", "input[type=search]"],
  ] as const;
  for (const [path, content] of pages) {
    await test.step(path, async () => {
      await page.goto("/o/" + orgId + "/settings/" + path);
      await expect(page.locator("main").locator(content).first()).toBeVisible();
      for (const width of [1440, 960]) {
        await page.setViewportSize({
          width,
          height: width === 960 ? 640 : 900,
        });
        await expectWindowFits(page);
      }
    });
  }
});
