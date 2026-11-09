import { expect, test, type Locator, type Page } from "@playwright/test";
import { createTranslator } from "../../../packages/i18n/src/translate";
import type { Locale } from "../../../packages/i18n/src/locales";
import {
  agent,
  device,
  entry,
} from "../../../packages/app/src/agents/fixtures";

const ts = "2026-11-07T06:00:00Z";
const org = "/api/v1/organizations/o1";
const model = "model-with-a-long-name-for-narrow-panel-validation";
const conversation = {
  id: "c1",
  organization_id: "o1",
  space_id: "s1",
  agent_id: "a1",
  owner_user_id: "u1",
  visibility: "PRIVATE",
  lifecycle: "OPEN",
  handoff: "NONE",
  origin: "USER",
  version: 3,
  created_at: ts,
  updated_at: ts,
  title: "对话组件验收",
  archived_at: null,
  pinned: false,
  last_read_seq: 2,
  unread: false,
  last_message: null,
  task: null,
  run: null,
  status: "IDLE",
  agent: { id: "a1", name: "研发助手", title: "", avatar: null, online: true },
  overrides: { runtime: null, model: null, approval_mode: null, version: 0 },
  effective: { runtime: "codex", model, approval_mode: "FOLLOW_RUNTIME" },
  agent_defaults: { runtime: "codex", model, approval_mode: "FOLLOW_RUNTIME" },
};
const answer =
  "## 改造完成\n\n消息与输入区共用同一套组件，保留现有行为。\n\n| 区域 | 验证结果 |\n| --- | ---: |\n| 输入框 | 中文与快捷键通过 |\n| 消息 | 长代码横向滚动 |\n\n- [x] 草稿保留\n- [x] 附件与提及\n\n```typescript\nconst message = '" +
  "long_".repeat(55) +
  "';\nconsole.log(message);\n```\n\n[设计说明](https://example.com/docs)[^来源]\n\n[^来源]: 脚注的来源说明。";

for (const locale of ["zh-CN", "en-US"] as const) {
  test(`model reasoning submenu: supported depths, escape and persisted selection ${locale}`, async ({
    page,
  }, testInfo) => {
    await mockChat(page, "light", locale);
    const t = createTranslator(locale);
    const depthLabel = t("chat.overrides.chooseReasoning", { model });
    const high = t("chat.overrides.reasoningLevels.HIGH");
    const ultra = t("chat.overrides.reasoningLevels.ULTRA");
    const defaultDepth = t("chat.overrides.reasoningLevels.DEFAULT");
    const patches: Record<string, unknown>[] = [];
    let effort = "DEFAULT";
    const summary = () => ({
      ...conversation,
      effective: { ...conversation.effective, reasoning_effort: effort },
      overrides: {
        ...conversation.overrides,
        reasoning_effort: effort === "DEFAULT" ? null : effort,
        version: patches.length,
      },
    });
    await page.route(`**${org}/conversations/c1`, (route) =>
      route.fulfill({ json: summary() }),
    );
    await page.route(`**${org}/conversations/c1/override-options`, (route) =>
      route.fulfill({
        json: {
          computer_online: true,
          runtimes: [
            {
              provider: "codex",
              runnable: true,
              approval_modes: ["FOLLOW_RUNTIME"],
              reasoning_efforts: ["DEFAULT", "HIGH", "EXTRA_HIGH", "ULTRA"],
              models: [
                {
                  id: model,
                  display_name: model,
                  is_default: true,
                  reasoning_efforts: ["DEFAULT", "HIGH", "ULTRA"],
                  default_reasoning_effort: "HIGH",
                },
              ],
            },
          ],
        },
      }),
    );
    await page.route(`**${org}/conversations/c1/overrides`, (route) => {
      const patch = route.request().postDataJSON();
      patches.push(patch);
      effort = patch.reasoning_effort ?? "DEFAULT";
      return route.fulfill({ json: summary() });
    });
    await page.reload();
    const trigger = page.getByRole("button", {
      name: t("chat.overrides.button", {
        label: t("chat.overrides.model"),
        value: model,
      }),
      exact: true,
    });
    await trigger.click();
    const depthTrigger = page.getByRole("button", {
      name: depthLabel,
      exact: true,
    });
    await depthTrigger.click();
    const submenu = page.getByRole("menu", {
      name: depthLabel,
      exact: true,
    });
    await expect(submenu.getByRole("menuitemradio")).toHaveText([
      defaultDepth,
      `${high}${t("chat.overrides.modelDefaultReasoning")}`,
      ultra,
    ]);
    const highOption = submenu
      .getByRole("menuitemradio")
      .filter({ hasText: high });
    await expect(highOption).toHaveAttribute("aria-checked", "false");
    await highOption.focus();
    await page.keyboard.press("Escape");
    await expect(submenu).not.toBeVisible();
    await expect(depthTrigger).toBeFocused();
    expect(patches).toHaveLength(0);
    await depthTrigger.click();
    const bounds = (await submenu.boundingBox())!;
    expect(bounds.x).toBeGreaterThanOrEqual(0);
    expect(bounds.x + bounds.width).toBeLessThanOrEqual(
      page.viewportSize()!.width,
    );
    await submenu.screenshot({
      path: testInfo.outputPath("reasoning-menu.png"),
    });
    await highOption.click();
    await expect(page.getByRole("menu")).toHaveCount(0);
    expect(patches).toEqual([
      { model: null, reasoning_effort: "HIGH", expected_version: 0 },
    ]);
    const overriddenTrigger = page.getByRole("button", {
      name: t("chat.overrides.buttonOverridden", {
        label: t("chat.overrides.model"),
        value: model,
      }),
      exact: true,
    });
    await overriddenTrigger.click();
    await depthTrigger.click();
    await expect(highOption).toHaveAttribute("aria-checked", "true");
    await submenu
      .getByRole("menuitemradio", { name: ultra, exact: true })
      .click();
    expect(patches[1]).toEqual({
      model: null,
      reasoning_effort: "ULTRA",
      expected_version: 1,
    });
    await overriddenTrigger.click();
    await depthTrigger.click();
    await expect(
      submenu.getByRole("menuitemradio", { name: ultra, exact: true }),
    ).toHaveAttribute("aria-checked", "true");
    await submenu
      .getByRole("menuitemradio", { name: defaultDepth, exact: true })
      .click();
    expect(patches[2]).toEqual({
      model: null,
      reasoning_effort: null,
      expected_version: 2,
    });
    await expect(trigger).toBeVisible();
  });
}

test("Git defaults: organization save and space inheritance", async ({
  page,
}, testInfo) => {
  await mockChat(page, "light", "zh-CN");
  let organization = {
    author_name_mode: "USER_AND_AGENT",
    author_email_mode: "USER",
    author_name: "",
    author_email: "",
    co_author_enabled: false,
    version: 1,
  };
  let spaceDefaults: {
    author_name_mode: string;
    author_email_mode: string;
    author_name: string;
    author_email: string;
    co_author_enabled: boolean | null;
    version: number;
  } = {
    author_name_mode: "INHERIT",
    author_email_mode: "INHERIT",
    author_name: "",
    author_email: "",
    co_author_enabled: null,
    version: 1,
  };
  const writes: unknown[] = [];
  let spaceReads = 0;
  let repoReads = 0;
  await page.route(`**${org}/git-settings`, (route) => {
    if (route.request().method() === "PUT") {
      const input = route.request().postDataJSON();
      writes.push(input);
      organization = { ...input, version: organization.version + 1 };
    }
    return route.fulfill({
      json: { ...organization, can_manage: true, effective: organization },
    });
  });
  await page.route(`**${org}/spaces/s1/git-settings`, (route) => {
    spaceReads += 1;
    if (route.request().method() === "PUT") {
      const input = route.request().postDataJSON();
      writes.push(input);
      spaceDefaults = { ...input, version: spaceDefaults.version + 1 };
    }
    return route.fulfill({
      json: {
        ...spaceDefaults,
        can_manage: true,
        effective: {
          author_name_mode:
            spaceDefaults.author_name_mode === "INHERIT"
              ? organization.author_name_mode
              : spaceDefaults.author_name_mode,
          author_email_mode:
            spaceDefaults.author_email_mode === "INHERIT"
              ? organization.author_email_mode
              : spaceDefaults.author_email_mode,
          author_name: spaceDefaults.author_name || organization.author_name,
          author_email: spaceDefaults.author_email || organization.author_email,
          co_author_enabled:
            spaceDefaults.co_author_enabled ?? organization.co_author_enabled,
        },
      },
    });
  });
  await page.route(`**${org}/spaces/s1`, (route) =>
    route.fulfill({
      json: {
        id: "s1",
        name: "研发空间",
        prefix: "QA",
        visibility: "shared",
        version: 1,
        created_at: ts,
        updated_at: ts,
        icon: null,
        purpose: "software_development",
        features: {
          views: true,
          projects: true,
          iterations: false,
          triage: false,
        },
        multi_assignee: false,
        estimation: {
          scale: "notUsed",
          extended: false,
          allow_zero: false,
          count_unestimated: false,
        },
        status: "active",
        retired_at: null,
        position: 0,
        my_role: "OWNER",
        can_manage: true,
        member_count: 1,
        task_type_count: 1,
        label_count: 0,
        task_count: 0,
      },
    }),
  );
  await page.route(`**${org}/spaces/s1/repos`, (route) => {
    repoReads += 1;
    return route.fulfill({ json: { repos: [] } });
  });
  await page.goto("/o/o1/settings/git");
  const global = page.getByRole("region", {
    name: "全局 Git 设置",
    exact: true,
  });
  const save = global.getByRole("button", { name: "保存", exact: true });
  await expect(save).toBeDisabled();
  await global.getByRole("button", { name: "提交人名称", exact: true }).click();
  await expect(page.getByRole("option")).toHaveCount(6);
  await page.getByRole("option", { name: "手动填写", exact: true }).click();
  await global
    .getByRole("textbox", { name: "提交人名称", exact: true })
    .fill("Organization QA");
  await global.getByRole("button", { name: "提交人邮箱", exact: true }).click();
  await expect(page.getByRole("option")).toHaveCount(4);
  await page.getByRole("option", { name: "手动填写", exact: true }).click();
  await global
    .getByRole("textbox", { name: "提交人邮箱", exact: true })
    .fill("git@example.test");
  await global
    .getByRole("switch", { name: "Co-authored-by", exact: true })
    .click();
  await save.click();
  await expect(save).toBeDisabled();
  await page.screenshot({ path: testInfo.outputPath("organization-git.png") });
  expect(spaceReads).toBe(0);
  expect(repoReads).toBe(0);
  const expand = page.getByRole("button", { name: "研发空间", exact: true });
  await expand.click();
  await expect(page).toHaveURL(/\/o\/o1\/settings\/git$/);
  const space = page.getByRole("region", { name: "研发空间", exact: true });
  const spaceSave = space.getByRole("button", { name: "保存", exact: true });
  await space.getByRole("button", { name: "提交人名称", exact: true }).click();
  await expect(page.getByRole("option")).toHaveCount(7);
  await expect(
    page.getByRole("option", { name: /继承组织设置/ }),
  ).toContainText("Organization QA");
  await page.screenshot({
    path: testInfo.outputPath("space-git-source-menu.png"),
  });
  await page.getByRole("option", { name: "手动填写", exact: true }).click();
  await space
    .getByRole("textbox", { name: "提交人名称", exact: true })
    .fill("Space QA");
  await expand.click();
  await expand.click();
  await expect(
    space.getByRole("textbox", { name: "提交人名称", exact: true }),
  ).toHaveValue("Space QA");
  await space
    .getByRole("button", { name: "Co-authored-by", exact: true })
    .click();
  await expect(page.getByRole("listbox").getByRole("option")).toHaveText([
    "继承上级设置",
    "开启",
    "关闭",
  ]);
  await page.getByRole("option", { name: "关闭", exact: true }).click();
  await spaceSave.click();
  await expect(spaceSave).toBeDisabled();
  await space.getByRole("button", { name: "提交人名称", exact: true }).click();
  await page.getByRole("option", { name: /继承组织设置/ }).click();
  await space
    .getByRole("button", { name: "Co-authored-by", exact: true })
    .click();
  await page.getByRole("option", { name: "继承上级设置", exact: true }).click();
  await spaceSave.click();
  await expect(spaceSave).toBeDisabled();
  expect(writes).toEqual([
    {
      version: 1,
      author_name: "Organization QA",
      author_name_mode: "MANUAL",
      author_email_mode: "MANUAL",
      author_email: "git@example.test",
      co_author_enabled: true,
    },
    {
      version: 1,
      author_name: "Space QA",
      author_name_mode: "MANUAL",
      author_email_mode: "INHERIT",
      author_email: "",
      co_author_enabled: false,
    },
    {
      version: 2,
      author_name_mode: "INHERIT",
      author_email_mode: "INHERIT",
      author_name: "",
      author_email: "",
      co_author_enabled: null,
    },
  ]);
  expect(repoReads).toBe(1);
});

async function mockChat(page: Page, theme: "light" | "dark", locale: Locale) {
  const sent: string[] = [];
  const messages = [
    {
      id: "m1",
      seq: 1,
      author_kind: "USER",
      body: "优化对话页面，保持原有功能",
    },
    { id: "m2", seq: 2, author_kind: "AGENT", body: answer },
  ].map((message) => ({
    ...message,
    organization_id: "o1",
    conversation_id: "c1",
    kind: "TEXT",
    run_id: null,
    created_at: ts,
  }));
  await page.addInitScript(() =>
    localStorage.setItem("openfox:onboarding:o1", "{}"),
  );
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    let body: unknown;
    let status = 200;
    if (path === "/api/v1/me") body = { id: "u1", email: "qa@example.test" };
    else if (path === "/api/v1/me/organizations")
      body = {
        organizations: [
          {
            id: "o1",
            name: "OpenFox QA",
            slug: "qa",
            role: "OWNER",
            created_at: ts,
          },
        ],
      };
    else if (path === "/api/v1/me/preferences")
      body = {
        theme,
        locale,
        outline_color: 0,
        send_shortcut: "enter",
        command_shortcuts: {},
        version: 1,
      };
    else if (path === `${org}/spaces`)
      body = {
        spaces: [
          {
            id: "s1",
            name: "研发空间",
            prefix: "QA",
            visibility: "shared",
            created_at: ts,
          },
        ],
      };
    else if (path === `${org}/agent-directory`)
      body = {
        entries: [entry("a1", { name: "研发助手", model })],
        archived_count: 0,
        truncated: false,
      };
    else if (path === `${org}/spaces/s1/agents/a1`) {
      const detail = agent("a1", { name: "研发助手" });
      body = {
        ...detail,
        config: {
          ...detail.config,
          directory_mode: "EXPLICIT",
          directory_roots: ["/workspace/project-with-a-long-directory-name"],
        },
      };
    } else if (path === `${org}/devices`)
      body = { devices: [device("d1")], next_cursor: null };
    else if (path.endsWith("/conversations"))
      body = { conversations: [conversation], next_cursor: null };
    else if (path === `${org}/conversations/c1`) body = conversation;
    else if (path === `${org}/conversations/c1/messages`) {
      if (request.method() === "POST") {
        const input = request.postDataJSON() as { body: string };
        sent.push(input.body);
        const message = {
          ...messages[0],
          id: `m${messages.length + 1}`,
          seq: messages.length + 1,
          body: input.body,
        };
        messages.push(message as (typeof messages)[number]);
        body = message;
        status = 201;
      } else body = { messages, next_cursor: 0 };
    } else if (path === `${org}/conversations/c1/artifacts`)
      body = { artifacts: [] };
    else if (path.endsWith("/override-options"))
      body = {
        computer_id: "d1",
        computer_online: true,
        runtimes: device("d1").runtimes,
      };
    else if (path.endsWith("/read")) status = 204;
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
      status = 404;
      body = {
        error: {
          code: "NOT_FOUND",
          message: "fixture endpoint",
          request_id: "qa",
        },
      };
    }
    await route.fulfill({
      status,
      contentType: "application/json",
      body: status === 204 ? undefined : JSON.stringify(body),
    });
  });
  await page.goto("/o/o1/chat/c1");
  await expect(
    page.locator('[data-slot="prompt-input-textarea"]'),
  ).toBeVisible();
  await expect(page.getByRole("table")).toBeVisible();
  return sent;
}

async function mockTaskComments(page: Page) {
  const comments: unknown[] = [];
  const root = {
    id: "root",
    body: "Review the task",
    author_kind: "USER",
    author_user_id: "u1",
    author_name: "QA",
    created_at: ts,
    reactions: [],
    reply_count: 0,
  };
  const task = {
    organization_id: "o1",
    space_id: "s1",
    id: "task-1",
    display_id: "OPEN-1",
    sequence: 1,
    type_id: "t1",
    type_name: "Task",
    status_id: "st1",
    status_name: "Ready",
    status_category: "TODO",
    title: "Comment composer QA",
    description: "",
    acceptance: "",
    priority: "NONE",
    version: 1,
    created_at: ts,
    updated_at: ts,
    labels: [],
    label_items: [],
    assignees: [],
    description_reactions: [],
    start_date: null,
    due_date: null,
    parent_task_id: null,
    parent: null,
    rank: "h1",
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
  };
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const path = url.pathname;
    let body: unknown;
    if (path === `${org}/tasks/by-display-id/OPEN-1`) body = task;
    else if (path === `${org}/conversations/c1`)
      body = { ...conversation, task_id: task.id };
    else if (path === `${org}/quick-replies`)
      body = {
        personal: [
          {
            id: "q1",
            scope: "PERSONAL",
            owner_user_id: "u1",
            title: "Reply title",
            body: "Saved reply body",
            position: 0,
            version: 1,
            can_edit: true,
            created_at: ts,
            updated_at: ts,
          },
        ],
        organization: [],
      };
    else if (path === `${org}/members`)
      body = {
        members: [
          {
            user_id: "u1",
            full_name: "QA",
            email: "qa@example.test",
            role: "OWNER",
            status: "ACTIVE",
          },
          {
            user_id: "u2",
            full_name: "Carol",
            email: "carol@example.test",
            role: "MEMBER",
            status: "ACTIVE",
          },
        ],
        next_cursor: "",
      };
    else if (path.endsWith("/timeline"))
      body = {
        total: 2,
        items:
          url.searchParams.get("thread") !== "roots"
            ? []
            : [
                { type: "comment", comment: root },
                {
                  type: "comment",
                  comment: {
                    ...root,
                    id: "topic",
                    body: "Topic QA",
                    reply_count: 1,
                    recent_replies: [],
                  },
                },
              ],
        next_cursor: null,
      };
    else if (path.endsWith("/comments") && request.method() === "POST") {
      comments.push(request.postDataJSON());
      body = {
        ...root,
        ...request.postDataJSON(),
        id: `comment-${comments.length}`,
      };
    } else if (path.endsWith("/attachments"))
      body = {
        attachments: [
          {
            id: "file1",
            owner_kind: "TASK",
            task_id: task.id,
            name: "notes.txt",
            content_type: "text/plain",
            size_bytes: 32,
            uploaded_by: "u1",
            created_at: ts,
            previewable: false,
            can_delete: false,
          },
        ],
      };
    else if (path.endsWith("/followers"))
      body = { followers: [], following: false };
    else if (path.endsWith("/task-statuses"))
      body = { statuses: [{ id: "st1", name: "Ready", category: "TODO" }] };
    else if (path.endsWith("/task-types")) body = { task_types: [] };
    else if (path.endsWith("/relations")) body = { relations: [] };
    else if (path.endsWith("/links")) body = { links: [] };
    else if (path.endsWith("/fields")) body = { fields: [] };
    else if (path.endsWith("/labels")) body = { labels: [] };
    else if (path.endsWith("/tasks")) body = { tasks: [], next_cursor: "" };
    else {
      await route.fallback();
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(body),
    });
  });
  return comments;
}

for (const { theme, locale } of [
  { theme: "light", locale: "zh-CN" },
  { theme: "dark", locale: "en-US" },
] as const) {
  test(`task comment composer ${theme} ${locale}: anchored menus, mentions and replies`, async ({
    page,
  }, testInfo) => {
    const t = createTranslator(locale);
    const errors: string[] = [];
    page.on("pageerror", (error) => errors.push(error.message));
    await mockChat(page, theme, locale);
    const comments = await mockTaskComments(page);
    await page.goto("/o/o1/tasks/OPEN-1?view=chat&conversationId=c1");
    const composer = page.locator('[data-comment-composer="comment"]');
    const box = composer.getByRole("textbox");
    await expect(box).toBeVisible();
    await box.scrollIntoViewIfNeeded();
    await expect(composer).toHaveCSS("border-radius", "14px");
    await expect(
      composer.locator('[data-slot="prompt-input-actions"] button'),
    ).toHaveCount(3);
    await expect(box).toHaveCSS("min-height", "24px");
    const quick = composer.getByRole("button", {
      name: t("quickReplies.menuLabel"),
    });
    const menu = page.getByRole("dialog", {
      name: t("quickReplies.menuLabel"),
      exact: true,
    });
    const anchored = async (panel: Locator, button: Locator) => {
      await expect
        .poll(async () => {
          const popup = (await panel.boundingBox())!;
          const trigger = (await button.boundingBox())!;
          return Math.abs(popup.y + popup.height + 4 - trigger.y);
        })
        .toBeLessThan(1);
      const popup = (await panel.boundingBox())!;
      const trigger = (await button.boundingBox())!;
      expect(
        Math.abs(popup.x + popup.width - trigger.x - trigger.width),
      ).toBeLessThan(1);
      expect(popup.x).toBeGreaterThanOrEqual(8);
    };
    await quick.click();
    await expect(
      menu.getByRole("button", { name: "Reply title", exact: true }),
    ).toBeVisible();
    await expect(
      menu.getByText("Saved reply body", { exact: true }),
    ).toHaveCount(0);
    await anchored(menu, quick);
    await page.screenshot({
      path: testInfo.outputPath("comment-quick-replies.png"),
    });
    await page.keyboard.press("Escape");
    await expect(quick).toBeFocused();
    const attach = composer.getByRole("button", {
      name: t("attachments.insertIntoComment"),
    });
    await attach.click();
    const attachmentMenu = page.getByRole("dialog", {
      name: t("attachments.insertIntoComment"),
      exact: true,
    });
    await anchored(attachmentMenu, attach);
    await attachmentMenu.getByRole("combobox").fill("notes");
    await anchored(attachmentMenu, attach);
    await attachmentMenu
      .getByRole("option", { name: "notes.txt", exact: true })
      .click();
    await expect(box).toHaveValue(/notes.txt/);
    await expect(box).toBeFocused();
    await box.fill("");
    await box.pressSequentially("Please @car");
    await expect(box).toBeFocused();
    const mention = page.getByRole("dialog", {
      name: t("tasks.detail.mention"),
      exact: true,
    });
    await expect(mention.getByRole("combobox")).toHaveCount(0);
    await expect(mention.getByRole("option")).toHaveCount(1);
    await box.press("Enter");
    await expect(box).toHaveValue("Please @carol@example.test ");
    expect(comments).toHaveLength(0);
    await box.fill("draft");
    await quick.click();
    await menu
      .getByRole("button", {
        name: t("quickReplies.insert", { title: "Reply title" }),
      })
      .click();
    await expect(box).toHaveValue("draftSaved reply body");
    await quick.click();
    await menu
      .getByRole("button", { name: t("quickReplies.add"), exact: true })
      .click();
    const manager = page.getByRole("dialog", {
      name: t("quickReplies.manager.title"),
      exact: true,
    });
    await expect(
      manager.getByRole("textbox", {
        name: t("quickReplies.manager.titleLabel"),
        exact: true,
      }),
    ).toBeVisible();
    await manager
      .getByRole("button", { name: t("common.close"), exact: true })
      .click();
    await box.fill(
      Array.from({ length: 20 }, (_, index) => `Line ${index}`).join("\n"),
    );
    await expect(box).toHaveCSS("height", "180px");
    await box.fill("main draft");
    const inline = page.locator('[data-comment-composer="inline"]').first();
    await inline.getByRole("textbox").fill("reply draft");
    const inlineQuick = inline.getByRole("button", {
      name: t("quickReplies.menuLabel"),
    });
    await inlineQuick.click();
    await anchored(menu, inlineQuick);
    await menu
      .getByRole("button", { name: "Reply title", exact: true })
      .click();
    await expect.poll(() => comments.length).toBe(1);
    expect(comments[0]).toMatchObject({
      body: "Saved reply body",
      reply_to_id: "root",
    });
    await expect(box).toHaveValue("main draft");
    await page
      .getByRole("button", { name: t("tasks.detail.viewTopic"), exact: true })
      .last()
      .click();
    const topic = page.getByRole("dialog", {
      name: t("tasks.detail.topic"),
      exact: true,
    });
    const topicBox = topic.getByRole("textbox", {
      name: t("tasks.detail.replyPlaceholder"),
      exact: true,
    });
    await topicBox.pressSequentially("@car");
    await expect(topicBox).toBeFocused();
    await page.keyboard.press("Escape");
    await expect(topic).toBeVisible();
    await expect(topicBox).toBeFocused();
    await topic
      .getByRole("button", { name: t("common.close"), exact: true })
      .click();
    await page.goto("/o/o1/tasks/OPEN-1");
    await page.setViewportSize({ width: 760, height: 900 });
    await quick.click();
    await expect(
      menu.getByRole("button", { name: "Reply title", exact: true }),
    ).toBeVisible();
    await anchored(menu, quick);
    await page.emulateMedia({ reducedMotion: "reduce" });
    await page.keyboard.press("Escape");
    await expect(quick).toBeFocused();
    expect(errors).toEqual([]);
  });
}

async function dragHandle(page: Page, handle: Locator, delta: number) {
  const expectNoLine = async () => {
    expect(
      await handle.evaluate(
        (element) => getComputedStyle(element, "::before").content,
      ),
    ).toMatch(/^(none|normal)$/);
  };
  const bounds = (await handle.boundingBox())!;
  const startX = bounds.x + bounds.width / 2;
  const startY = bounds.y + bounds.height / 2;
  await page.mouse.move(startX, startY);
  await expectNoLine();
  await page.mouse.down();
  await page.mouse.move(startX + delta, startY, { steps: 10 });
  await expectNoLine();
  await page.mouse.up();
  await handle.focus();
  await expectNoLine();
}

for (const locale of ["zh-CN", "en-US"] as const) {
  test(`task AI panel resizing and composer menus ${locale}`, async ({
    page,
  }, testInfo) => {
    const t = createTranslator(locale);
    const errors: string[] = [];
    page.on("pageerror", (error) => errors.push(error.message));
    const sent = await mockChat(page, "light", locale);
    await mockTaskComments(page);
    await page.goto("/o/o1/tasks/OPEN-1?view=chat&conversationId=c1");
    const panel = page.getByRole("complementary", {
      name: t("tasks.detail.aiPanel"),
      exact: true,
    });
    const handle = page.getByRole("separator", {
      name: t("tasks.detail.aiPanelResize"),
    });
    await expect(panel).toHaveCSS("width", "480px");
    await dragHandle(page, handle, -100);
    await expect(panel).toHaveCSS("width", "580px");
    await page.reload();
    await expect(panel).toHaveCSS("width", "580px");
    await handle.press("Home");
    await expect(panel).toHaveCSS("width", "320px");
    const composer = panel.locator('[data-slot="prompt-input"]');
    expect(
      await composer.evaluate((node) => node.scrollWidth <= node.clientWidth),
    ).toBe(true);
    const quick = panel.getByRole("button", {
      name: t("quickReplies.menuLabel"),
      exact: true,
    });
    await quick.click();
    const replies = page.getByRole("dialog", {
      name: t("quickReplies.menuLabel"),
      exact: true,
    });
    await expect(
      replies.getByRole("button", { name: "Reply title", exact: true }),
    ).toBeVisible();
    await expect(replies.getByRole("textbox")).toHaveCount(0);
    await expect
      .poll(async () => {
        const popup = (await replies.boundingBox())!;
        const button = (await quick.boundingBox())!;
        return Math.abs(popup.y + popup.height + 4 - button.y);
      })
      .toBeLessThan(1);
    await page.screenshot({
      path: testInfo.outputPath("ai-quick-replies.png"),
    });
    await replies
      .getByRole("button", { name: "Reply title", exact: true })
      .click();
    const input = panel.getByRole("textbox", {
      name: t("chat.composer.label"),
    });
    await expect(input).toHaveValue("Saved reply body");
    await expect(input).toBeFocused();
    await input.fill("");
    await input.pressSequentially("@研");
    const mentions = page.getByRole("listbox", {
      name: t("chat.composer.mention"),
    });
    await expect(
      mentions.getByRole("option", { name: "研发助手" }),
    ).toBeVisible();
    await expect(input).toBeFocused();
    await input.press("Enter");
    await expect(input).toHaveValue("@研发助手 ");
    await input.fill("");
    await panel
      .getByRole("button", {
        name: t("chat.overrides.button", {
          label: t("chat.overrides.model"),
          value: model,
        }),
      })
      .click();
    const search = page.getByRole("textbox", {
      name: t("chat.overrides.searchModels"),
    });
    await expect(search).toBeFocused();
    await search.fill("missing-model");
    await expect(page.getByRole("menuitemradio")).toHaveCount(0);
    await search.press("Enter");
    await expect(search).toBeVisible();
    await page
      .getByRole("button", { name: t("chat.overrides.refreshModels") })
      .click();
    await expect(search).toHaveValue("missing-model");
    await search.press("Escape");
    expect(sent).toEqual([]);
    await handle.press("End");
    const parentWidth = await panel.evaluate(
      (node) => node.parentElement!.clientWidth,
    );
    await expect(handle).toHaveAttribute(
      "aria-valuenow",
      String(Math.min(960, parentWidth - 400)),
    );
    await page.setViewportSize({ width: 900, height: 900 });
    await expect(handle).toBeHidden();
    await expect(panel).toHaveCSS("position", "absolute");
    expect(
      await panel.evaluate((node) => node.scrollWidth <= node.clientWidth),
    ).toBe(true);
    await page.setViewportSize({ width: 1440, height: 900 });
    await expect(handle).toBeVisible();
    await page.screenshot({ path: testInfo.outputPath("task-ai-split.png") });
    await handle.press("Enter");
    await expect(panel).toHaveCount(0);
    await expect(
      page.getByRole("textbox", { name: t("tasks.detail.titleLabel") }),
    ).toBeVisible();
    expect(errors).toEqual([]);
  });

  test(`resizable conversation panels ${locale}`, async ({
    page,
  }, testInfo) => {
    const t = createTranslator(locale);
    await mockChat(page, "light", locale);
    const sidebar = page.getByRole("navigation", {
      name: t("chat.title"),
      exact: true,
    });
    const sidebarPanel = page.locator("#org-sidebar-panel");
    const sidebarHandle = page.getByRole("separator", {
      name: t("shell.a11y.resizeSidebar"),
    });
    await expect(sidebarHandle).toHaveAttribute("aria-valuenow", "260");
    await dragHandle(page, sidebarHandle, 80);
    await page.mouse.move(900, 450);
    await expect(sidebarHandle).toHaveAttribute("aria-valuenow", "340");
    await expect(sidebarPanel).toHaveCSS("width", "340px");
    await page.reload();
    await expect(sidebarHandle).toHaveAttribute("aria-valuenow", "340");
    await sidebarHandle.press("Home");
    await expect(sidebarHandle).toHaveAttribute("aria-valuenow", "0");
    await expect(sidebar).toHaveCount(0);
    await sidebarHandle.press("ArrowRight");
    await expect(sidebarPanel).toHaveCSS("width", "340px");
    await sidebarHandle.press("End");
    await expect(sidebarPanel).toHaveCSS("width", "480px");
    await sidebarHandle.press("Enter");
    await expect(sidebarHandle).toHaveAttribute("aria-valuenow", "0");
    await page
      .getByRole("button", { name: t("shell.expand"), exact: true })
      .click();
    await expect(sidebarPanel).toHaveCSS("width", "480px");
    await dragHandle(page, sidebarHandle, -260);
    await expect(sidebarPanel).toHaveCSS("width", "220px");
    await page.goto("/o/o1/chat/c1?artifact=list");
    const artifacts = page.getByRole("complementary", {
      name: t("artifacts.panel.label"),
    });
    const artifactHandle = page.getByRole("separator", {
      name: t("artifacts.panel.resize"),
    });
    await expect(artifactHandle).toBeVisible();
    await expect(artifacts).toHaveCSS("width", "480px");
    await dragHandle(page, artifactHandle, -100);
    await expect(artifacts).toHaveCSS("width", "580px");
    await artifactHandle.press("ArrowRight");
    await expect(artifacts).toHaveCSS("width", "564px");
    await page.reload();
    await expect(artifacts).toHaveCSS("width", "564px");
    await artifactHandle.press("Home");
    await expect(artifacts).toHaveCSS("width", "280px");
    await artifactHandle.press("End");
    const contentWidth = await artifacts.evaluate(
      (element) => element.parentElement!.clientWidth,
    );
    await expect(artifacts).toHaveCSS("width", `${contentWidth - 400}px`);
    await expect(page.locator('[data-slot="prompt-input"]')).toBeVisible();
    expect(
      await page
        .locator('[data-slot="prompt-input"]')
        .evaluate((element) => element.scrollWidth <= element.clientWidth),
    ).toBe(true);
    await page.screenshot({ path: testInfo.outputPath("resized-panels.png") });
    await page.setViewportSize({ width: 760, height: 900 });
    await expect(artifactHandle).toBeHidden();
    expect(
      await artifacts.evaluate(
        (element) =>
          element.clientWidth >= element.parentElement!.clientWidth - 1,
      ),
    ).toBe(true);
    await page
      .getByRole("button", { name: t("artifacts.panel.close"), exact: true })
      .click();
    await expect(artifacts).toHaveCount(0);
    await sidebarHandle.press("End");
    expect(
      (await page.locator("[data-org-main]").boundingBox())!.width,
    ).toBeGreaterThanOrEqual(360);
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto("/o/o1/chat/c1?artifact=list");
    await expect(artifactHandle).toBeVisible();
    await artifactHandle.press("Enter");
    await expect(artifacts).toHaveCount(0);
    await expect(page).not.toHaveURL(/artifact=/);
  });
}

for (const locale of ["zh-CN", "en-US"] as const) {
  test(`global sidebar resize and full collapse ${locale}`, async ({
    page,
  }, testInfo) => {
    const t = createTranslator(locale);
    const errors: string[] = [];
    page.on("pageerror", (error) => errors.push(error.message));
    await mockChat(page, "light", locale);
    const handle = page.getByRole("separator", {
      name: t("shell.a11y.resizeSidebar"),
    });
    const panel = page.locator("#org-sidebar-panel");
    const rail = page.getByRole("navigation", { name: t("shell.a11y.rail") });
    const sidebar = page.getByRole("complementary", {
      name: t("shell.a11y.sidebar"),
    });
    const expand = page.getByRole("button", {
      name: t("shell.expand"),
      exact: true,
    });
    for (const path of [
      "/o/o1/inbox",
      "/o/o1/members",
      "/o/o1/settings/profile",
      "/o/o1/chat/c1",
    ]) {
      await page.goto(path);
      await expect(handle).toBeVisible();
      await expect(panel).toHaveCSS("width", "260px");
      await expect(handle).toHaveAttribute(
        "aria-controls",
        "org-sidebar-panel",
      );
      await dragHandle(page, handle, 80);
      await expect(panel).toHaveCSS("width", "340px");
      const grip = (await handle.boundingBox())!;
      const startX = grip.x + grip.width / 2;
      const startY = grip.y + grip.height / 2;
      await page.mouse.move(startX, startY);
      await page.mouse.down();
      for (const width of [
        240, 220, 200, 160, 110, 100, 40, 0, 40, 160, 220, 340,
      ]) {
        await page.mouse.move(startX + width - 340, startY);
        await expect(panel).toHaveCSS("width", `${width}px`);
        await expect(handle).toHaveAttribute("aria-valuenow", String(width));
        await expect(panel).toHaveCSS("transition-duration", "0s");
      }
      expect(
        await page.evaluate(() =>
          localStorage.getItem("openfox:chat-sidebar-width"),
        ),
      ).toBe("340");
      await page.mouse.up();
      await expect(panel).toHaveCSS("transition-duration", "0.15s");
      const expandedMain = (await page
        .locator("[data-org-main]")
        .boundingBox())!.width;
      await page
        .getByRole("button", { name: t("shell.collapse"), exact: true })
        .click();
      await expect(panel).toHaveCSS("width", "0px");
      await expect(panel).toHaveAttribute("inert", "");
      await expect(sidebar).toHaveCSS("width", "55px");
      await expect(rail).toBeVisible();
      await expect(handle).toHaveAttribute("aria-valuenow", "0");
      expect((await page.locator("[data-org-main]").boundingBox())!.width).toBe(
        expandedMain + 340,
      );
      await page.reload();
      await expect(panel).toHaveCSS("width", "0px");
      await expand.click();
      await expect(panel).toHaveCSS("width", "340px");
      await dragHandle(page, handle, -400);
      await expect(panel).toHaveCSS("width", "0px");
      await expect(sidebar).toHaveCSS("width", "55px");
      await expand.click();
      await expect(panel).toHaveCSS("width", "340px");
      await handle.press("Home");
      await expect(panel).toHaveCSS("width", "0px");
      await dragHandle(page, handle, 260);
      await expect(panel).toHaveCSS("width", "260px");
      await page.setViewportSize({ width: 760, height: 900 });
      await expect(handle).toHaveAttribute("aria-valuemax", "321");
      await handle.press("End");
      await expect(panel).toHaveCSS("width", "321px");
      expect(
        (await page.locator("[data-org-main]").boundingBox())!.width,
      ).toBeGreaterThanOrEqual(360);
      await page.setViewportSize({ width: 1440, height: 900 });
      await expect(handle).toHaveAttribute("aria-valuemax", "480");
      await expect(panel).toHaveCSS("width", "321px");
      await dragHandle(
        page,
        handle,
        260 - Number(await handle.getAttribute("aria-valuenow")),
      );
      await expect(panel).toHaveCSS("width", "260px");
    }
    await dragHandle(page, handle, 80);
    await rail
      .getByRole("link", { name: t("shell.home"), exact: true })
      .click();
    await expect(page).toHaveURL(/\/o\/o1\/inbox$/);
    await expect(panel).toHaveCSS("width", "340px");
    await page
      .getByRole("button", { name: t("shell.collapse"), exact: true })
      .click();
    await rail
      .getByRole("link", { name: t("shell.chat"), exact: true })
      .click();
    await expect(page).toHaveURL(/\/o\/o1\/chat\/c1$/);
    await expect(page.locator('[data-slot="prompt-input"]')).toBeVisible();
    await expect(panel).toHaveCSS("width", "0px");
    await page.screenshot({
      path: testInfo.outputPath("collapsed-sidebar.png"),
    });
    await expand.click();
    await expect(panel).toHaveCSS("width", "340px");
    await page.emulateMedia({ reducedMotion: "reduce" });
    await expect(panel).toHaveCSS("transition-property", "none");
    expect(errors).toEqual([]);
  });
}

for (const { theme, locale } of [
  { theme: "light", locale: "zh-CN" },
  { theme: "dark", locale: "zh-CN" },
  { theme: "light", locale: "en-US" },
  { theme: "dark", locale: "en-US" },
] as const) {
  test(`prompt-kit ${theme} ${locale}: resize, IME, menus and bounded content`, async ({
    page,
  }, testInfo) => {
    const errors: string[] = [];
    page.on("pageerror", (error) => errors.push(error.message));
    const t = createTranslator(locale);
    const sent = await mockChat(page, theme, locale);
    if (theme === "dark") {
      await page.setViewportSize({ width: 760, height: 900 });
    }
    const input = page.locator('[data-slot="prompt-input-textarea"]');
    const composer = page.locator('[data-slot="prompt-input"]');
    for (const label of [
      t("chat.overrides.runtime"),
      t("chat.overrides.model"),
      t("chat.overrides.approval"),
    ]) {
      const trigger = composer.getByRole("button", {
        name: new RegExp(`^${label}[：:]`),
      });
      await trigger.hover();
      await expect(page.getByRole("tooltip")).toHaveText(
        (await trigger.getAttribute("aria-label"))!,
      );
      await trigger.click();
      const menu = page.getByRole("menu");
      await expect(menu).toBeVisible();
      await expect(page.getByRole("tooltip")).toHaveCount(0);
      await expect(menu.getByRole("menuitemradio").first()).toBeVisible();
      await expect(
        menu.getByText(t("chat.overrides.restore"), { exact: true }),
      ).toHaveCount(0);
      await expect(
        menu.getByText(t("chat.overrides.editAgentDefaults"), { exact: true }),
      ).toHaveCount(0);
      await expect(menu.getByRole("note")).toHaveCount(0);
      await expect(menu.getByRole("separator")).toHaveCount(0);
      await page.keyboard.press("Escape");
    }
    const originalUrl = page.url();
    const footnote = page.locator("a[data-footnote-ref]");
    await footnote.click();
    await expect(page.locator("li[id][tabindex='-1']")).toBeFocused();
    await page
      .getByRole("link", { name: t("chat.messages.footnoteBack") })
      .click();
    await expect(footnote).toBeFocused();
    expect(page.url()).toBe(originalUrl);
    await expect(page.getByRole("columnheader").nth(1)).toHaveCSS(
      "text-align",
      "right",
    );
    await input.fill("中文候选");
    await input.dispatchEvent("keydown", {
      key: "Enter",
      code: "Enter",
      isComposing: true,
      bubbles: true,
    });
    expect(sent).toHaveLength(0);
    await expect(input).toHaveValue("中文候选");
    await input.fill(
      Array.from({ length: 18 }, (_, i) => `第 ${i + 1} 行输入内容`).join("\n"),
    );
    await expect
      .poll(async () => (await input.boundingBox())!.height)
      .toBe(240);
    await input.fill("帮我检查输入框");
    await expect
      .poll(async () => (await input.boundingBox())!.height)
      .toBeLessThan(100);
    await expect(
      composer.getByRole("button", {
        name: t("chat.composer.mention"),
        exact: true,
      }),
    ).toHaveCount(0);
    await input.press("End");
    await input.pressSequentially("@");
    await expect(page.getByRole("option", { name: "研发助手" })).toBeVisible();
    await page.keyboard.press("Escape");
    await input.focus();
    await input.press("Shift+Enter");
    expect(sent).toHaveLength(0);
    await input.press("Enter");
    await expect.poll(() => sent.length).toBe(1);
    await expect(input).toHaveValue("");
    await expect
      .poll(async () =>
        composer.evaluate(
          (element) => element.scrollWidth <= element.clientWidth + 1,
        ),
      )
      .toBe(true);
    const code = page.locator('[data-slot="code-block"]');
    await expect(code).toBeVisible();
    expect(
      await code.evaluate(
        (element) => element.scrollWidth <= element.clientWidth + 1,
      ),
    ).toBe(true);
    expect(
      await code
        .locator("pre")
        .evaluate((element) => element.scrollWidth > element.clientWidth),
    ).toBe(true);
    const bounds = (await composer.boundingBox())!;
    expect(bounds.x + bounds.width).toBeLessThanOrEqual(
      page.viewportSize()!.width,
    );
    const send = (await page
      .getByRole("button", { name: t("chat.composer.send"), exact: true })
      .boundingBox())!;
    expect(send.x + send.width).toBeLessThanOrEqual(bounds.x + bounds.width);
    expect(send.y + send.height).toBeLessThanOrEqual(bounds.y + bounds.height);
    const colors = await page
      .getByRole("button", { name: t("chat.composer.send"), exact: true })
      .evaluate((element) => {
        const style = getComputedStyle(element);
        return { color: style.color, background: style.backgroundColor };
      });
    expect(colors.color).not.toBe(colors.background);
    const attach = (await page
      .getByRole("button", { name: t("chat.composer.attach"), exact: true })
      .boundingBox())!;
    expect(
      Math.abs(attach.y + attach.height / 2 - send.y - send.height / 2),
    ).toBeLessThan(3);
    await expect(composer).toHaveCSS("border-radius", "18px");
    await expect(input).toHaveCSS("min-height", "72px");
    expect(send.width).toBe(28);
    expect(send.height).toBe(28);
    const approval = composer.getByRole("button", {
      name: new RegExp(`^${t("chat.overrides.approval")}[：:]`),
    });
    await expect(approval).toHaveText("");
    expect(
      await composer
        .getByRole("button", {
          name: new RegExp(`^${t("chat.overrides.runtime")}[：:]`),
        })
        .locator(".truncate")
        .evaluate((element) => element.scrollWidth <= element.clientWidth),
    ).toBe(true);
    const toolbarButtons = composer.locator(
      '[data-slot="prompt-input-actions"] button',
    );
    const expectedLabels = [
      t("chat.composer.attach"),
      t("chat.overrides.runtime"),
      t("chat.workspace.label"),
      t("chat.overrides.approval"),
      t("chat.overrides.model"),
      t("chat.composer.quickReply"),
      t("chat.composer.send"),
    ];
    await expect(toolbarButtons).toHaveCount(expectedLabels.length);
    let previousRight = bounds.x;
    for (const [index, label] of expectedLabels.entries()) {
      const button = toolbarButtons.nth(index);
      await expect(button).toHaveAccessibleName(new RegExp(`^${label}`));
      const buttonBounds = (await button.boundingBox())!;
      expect(buttonBounds.x).toBeGreaterThanOrEqual(previousRight);
      expect(Math.abs(buttonBounds.y - send.y)).toBeLessThan(1);
      previousRight = buttonBounds.x + buttonBounds.width;
    }
    await page.mouse.move(0, 0);
    await composer.screenshot({ path: testInfo.outputPath("composer.png") });
    expect(errors).toEqual([]);
  });
}
