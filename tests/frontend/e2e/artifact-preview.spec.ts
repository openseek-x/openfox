import { expect, test, type Page } from "@playwright/test";

const ts = "2026-11-16T08:00:00Z";
const org = "/api/v1/organizations/o1";
const html = `<!doctype html><html lang="zh-CN"><head><style>
  body{margin:0;padding:32px;background:#f8f7f4;color:#243c3c;font:15px/1.7 system-ui}
  h1{font-size:30px;margin:0}h2{font-size:16px;margin-top:28px;border-bottom:1px solid #ccd4cf}
  .meta{color:#596e68}strong{color:#28756c}li{margin:8px 0}
  </style></head><body><h1>林晓 · 产品设计师</h1><p class="meta">上海 / 5 年工作经验</p>
  <h2>个人简介</h2><p>专注协作工具与复杂工作流，让产品清晰、易用。</p>
  <h2>项目经历</h2><strong>OpenFox 工作台</strong><ul><li>设计从对话到交付的完整体验</li><li>统一文件阅读、源码检查与下载流程</li></ul>
  <script>window.parent.artifactExecuted=true</script></body></html>`;
const markdown = `---\ntitle: 项目交付说明\n---\n# 产物预览\n\n支持 **Markdown**、表格和代码。\n\n| 文件 | 结果 |\n|---|---|\n| 个人简历.html | 静态页面 |\n| 说明.md | 阅读模式 |\n\n- [x] 预览与源码切换\n- [x] 复制和下载保留原文\n\n> 外部资源与脚本不在静态预览中运行。\n\n\`\`\`typescript\nconst preview = 'ready';\n\`\`\``;
const artifacts = [
  { id: "html", name: "个人简历.html", mime_type: "text/plain", content: html },
  { id: "md", name: "说明.md", mime_type: "text/markdown", content: markdown },
].map((file) => ({
  ...file,
  content_id: `content-${file.id}`,
  content_revision: 1,
  head_artifact_id: file.id,
  organization_id: "o1",
  run_id: "r1",
  size_bytes: Buffer.byteLength(file.content),
  sha256: "0".repeat(64),
  state: "AVAILABLE",
  retention_until: "2099-01-01T00:00:00Z",
  pinned: false,
  version: 1,
  created_at: ts,
}));

// Frontend/browser fixtures only; no real API, credentials or external model calls.
async function mockArtifacts(
  page: Page,
  theme: "dark" | "light",
  htmlContent = html,
  messageBody = "简历与说明已整理好，可以在右侧预览。",
) {
  await page.addInitScript(() =>
    localStorage.setItem("openfox:onboarding:o1", "{}"),
  );
  const conversation = {
    organization_id: "o1",
    id: "c1",
    space_id: "s1",
    agent_id: "a1",
    owner_user_id: "u1",
    visibility: "PRIVATE",
    lifecycle: "OPEN",
    handoff: "NONE",
    origin: "USER",
    version: 1,
    created_at: ts,
    updated_at: ts,
    title: "个人简历",
    archived_at: null,
    pinned: false,
    last_read_seq: 2,
    unread: false,
    last_message: null,
    task: null,
    run: { id: "r1", state: "SUCCEEDED", started_at: ts, finished_at: ts },
    status: "IDLE",
    agent: {
      id: "a1",
      name: "产品助手",
      title: "",
      avatar: null,
      online: true,
    },
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
  await page.route("**/api/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    const file = artifacts.find(
      (item) => path === `${org}/runs/r1/artifacts/${item.id}`,
    );
    if (file)
      return route.fulfill({
        body: file.id === "html" ? htmlContent : file.content,
        contentType: "text/plain",
      });
    let body: unknown;
    let status = 200;
    if (path === "/api/v1/me") body = { id: "u1", email: "qa@example.test" };
    else if (path === "/api/v1/me/organizations")
      body = {
        organizations: [
          {
            id: "o1",
            name: "测试组织",
            slug: "qa",
            role: "OWNER",
            created_at: ts,
          },
        ],
      };
    else if (path === "/api/v1/me/preferences")
      body = {
        theme,
        locale: "zh-CN",
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
            name: "默认空间",
            prefix: "QA",
            visibility: "shared",
            created_at: ts,
          },
        ],
      };
    else if (path.endsWith("/conversations"))
      body = { conversations: [conversation], next_cursor: null };
    else if (path === `${org}/conversations/c1`) body = conversation;
    else if (path.endsWith("/messages"))
      body = {
        messages: [
          {
            id: "m1",
            seq: 1,
            author_kind: "AGENT",
            body: messageBody,
            organization_id: "o1",
            conversation_id: "c1",
            kind: "TEXT",
            run_id: null,
            created_at: ts,
          },
        ],
        next_cursor: 0,
      };
    else if (path.endsWith("/artifacts")) body = { artifacts };
    else if (path.endsWith("/comments"))
      body = { threads: [], next_cursor: "" };
    else if (path.endsWith("/proposals"))
      body = { proposals: [], next_cursor: "" };
    else if (path === `${org}/runs/r1`)
      body = {
        id: "r1",
        organization_id: "o1",
        conversation_id: "c1",
        generation: 1,
        state: "SUCCEEDED",
        created_at: ts,
        started_at: ts,
        finished_at: ts,
        usage: {
          input: 900000,
          cached_input: 819000,
          cache_write_input: 0,
          output: 36000,
          reasoning_output: 0,
        },
        duration_ms: 120000,
      };
    else if (path === `${org}/runs/r1/events`)
      body = {
        events: [
          {
            run_id: "r1",
            generation: 1,
            sequence: 1,
            type: "context_usage",
            occurred_at: ts,
            safe_payload: {
              data: {
                schema: 1,
                runtime: "codex",
                requested_model: "",
                model: "fixture-model",
                current_tokens: 10000,
                window_tokens: 200000,
              },
            },
          },
        ],
        next_cursor: 0,
      };
    else if (path.endsWith("/read")) status = 204;
    else if (path.endsWith("/direct-chats"))
      body = { chats: [], next_before: "" };
    else if (path.endsWith("/agent-directory"))
      body = { entries: [], archived_count: 0, truncated: false };
    else if (path.endsWith("/devices"))
      body = { devices: [], next_cursor: null };
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
}

test("HTML static sandbox blocks script, navigation and all network resources under the deployed CSP", async ({
  page,
}) => {
  const attempted: string[] = [];
  await page.route("https://artifact-invalid.test/**", (route) => {
    attempted.push(route.request().url());
    return route.abort();
  });
  await page.route("**/o/o1/chat/c1?artifact=html", async (route) => {
    const response = await route.fetch();
    await route.fulfill({
      response,
      headers: {
        ...response.headers(),
        "Content-Security-Policy":
          "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob: https:; font-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'",
      },
    });
  });
  const hostile = `<style>@import url(https://artifact-invalid.test/import);body{background-image:url(https://artifact-invalid.test/css)}</style>
    <script>window.parent.artifactExecuted=true;fetch('https://artifact-invalid.test/script')</script>
    <meta http-equiv="refresh" content="0;url=https://artifact-invalid.test/refresh">
    <img src="https://artifact-invalid.test/image" onerror="parent.artifactExecuted=true">
    <iframe src="https://artifact-invalid.test/frame"></iframe>
    <a href="https://artifact-invalid.test/nav" target="_top">External</a>
    <form action="https://artifact-invalid.test/form"><button>Send</button></form><h1>Static content</h1>`;
  await mockArtifacts(page, "dark", hostile);
  await page.goto("/o/o1/chat/c1?artifact=html");
  const frame = page.frameLocator("iframe");
  await expect(
    frame.getByRole("heading", { name: "Static content" }),
  ).toBeVisible();
  await frame.getByText("External").click();
  await expect(frame.getByRole("button", { name: "Send" })).toBeDisabled();
  await expect(frame.locator("iframe,meta[http-equiv=refresh]")).toHaveCount(0);
  expect(await page.evaluate(() => "artifactExecuted" in window)).toBe(false);
  expect(
    await page.evaluate(
      () => document.querySelector("iframe")!.contentDocument,
    ),
  ).toBeNull();
  await page.waitForTimeout(300);
  expect(attempted).toEqual([]);
  expect(page.frames()[1]!.url()).toBe("about:srcdoc");
});

test("HTML preview uses thin scrollbars for the page and nested content", async ({
  page,
}) => {
  await mockArtifacts(
    page,
    "light",
    '<div id="table" style="width:120px;overflow:auto"><table style="width:600px"><tr><td>内容</td></tr></table></div><p style="height:200vh">正文</p>',
  );
  await page.goto("/o/o1/chat/c1?artifact=html");
  const frame = page.frameLocator("iframe");
  const root = frame.locator("html");
  const table = frame.locator("#table");
  await expect(root).toHaveCSS("scrollbar-width", "thin");
  await expect(table).toHaveCSS("scrollbar-width", "thin");
  await table.hover();
  await page.mouse.wheel(180, 0);
  await expect
    .poll(() => table.evaluate((el) => el.scrollLeft))
    .toBeGreaterThan(0);
  await frame.getByText("正文").hover({ position: { x: 5, y: 5 } });
  await page.mouse.wheel(0, 180);
  await expect
    .poll(() => root.evaluate((el) => el.scrollTop))
    .toBeGreaterThan(0);
});

test("artifact reader: dark HTML, light Markdown, narrow panel and source switching", async ({
  page,
}, testInfo) => {
  await mockArtifacts(page, "dark");
  await page.goto("/o/o1/chat/c1?artifact=html");
  const panel = page.getByRole("complementary", { name: "会话产物" });
  await expect(
    page
      .frameLocator('iframe[title="HTML 预览：个人简历.html"]')
      .getByRole("heading", { name: "林晓 · 产品设计师" }),
  ).toBeVisible();
  await expect(
    panel.getByRole("button", { name: "预览", exact: true }),
  ).toHaveAttribute("aria-pressed", "true");
  const settings = page.getByRole("button", { name: "设置", exact: true });
  const header = settings.locator("xpath=ancestor::header");
  const initialPanel = (await panel.boundingBox())!;
  const initialHeader = (await header.boundingBox())!;
  expect(initialPanel.y).toBe(initialHeader.y);
  expect(initialHeader.x + initialHeader.width).toBe(initialPanel.x);
  // 宽工作区采用单行 44px 统一顶栏，消除双层工具栏并严格与会话顶栏齐平
  expect(
    (await panel.locator("iframe").boundingBox())!.y - initialPanel.y,
  ).toBe(44);
  const handle = page.getByRole("separator", { name: "调整产物面板宽度" });
  const handleBox = (await handle.boundingBox())!;
  await page.mouse.move(handleBox.x + handleBox.width / 2, handleBox.y + 20);
  await page.mouse.down();
  await page.mouse.move(
    handleBox.x + handleBox.width / 2 + 80,
    handleBox.y + 20,
    { steps: 5 },
  );
  await page.mouse.up();
  await expect
    .poll(async () => (await panel.boundingBox())!.width)
    .toBe(initialPanel.width - 80);
  const resizedPanel = (await panel.boundingBox())!;
  const settingsBox = (await settings.boundingBox())!;
  expect(settingsBox.x + settingsBox.width).toBeLessThanOrEqual(resizedPanel.x);
  expect((await header.boundingBox())!.width).toBe(initialHeader.width + 80);
  await panel.getByRole("button", { name: "更多预览操作" }).click();
  const info = page.getByRole("dialog", { name: "文件信息" });
  await expect(info).toContainText("脚本、外部资源和跳转已禁用");
  await expect(info.locator("time")).toHaveAttribute("datetime", ts);
  await page.keyboard.press("Escape");
  await expect(info).toHaveCount(0);
  await expect(page.getByText("936K tok", { exact: true })).toBeVisible();
  await expect(page.getByText("缓存命中 91%", { exact: true })).toBeVisible();
  await expect(
    page.getByRole("progressbar", { name: "上下文" }),
  ).toHaveAttribute("aria-valuenow", "5");
  await expect(
    page.getByText("有效模型与变量来源", { exact: true }),
  ).toHaveCount(0);
  await page.screenshot({
    path: testInfo.outputPath("artifacts-dark-html.png"),
  });
  await panel.getByRole("button", { name: "源码", exact: true }).click();
  await expect(panel.locator("iframe")).toHaveCount(0);
  await expect(
    panel.getByText("window.parent.artifactExecuted=true", { exact: false }),
  ).toBeVisible();
  await expect(
    panel.getByRole("link", { name: "下载", exact: true }),
  ).toHaveAttribute("href", `${org}/runs/r1/artifacts/html`);

  await mockArtifacts(page, "light");
  await page.goto("/o/o1/chat/c1?artifact=md");
  await expect(panel.getByRole("table")).toBeVisible();
  await expect(panel.getByRole("heading", { name: "产物预览" })).toBeVisible();
  await page.screenshot({
    path: testInfo.outputPath("artifacts-light-markdown.png"),
  });
  await panel.getByRole("button", { name: "源码", exact: true }).click();
  await expect(panel.getByRole("table")).toHaveCount(0);
  await panel.getByRole("button", { name: "关闭产物面板" }).click();
  await expect(panel).toHaveCount(0);
  expect((await header.boundingBox())!.width).toBe(
    initialHeader.width + initialPanel.width,
  );

  await page.setViewportSize({ width: 950, height: 820 });
  await page.goto("/o/o1/chat/c1?artifact=html");
  await panel.getByRole("button", { name: "预览", exact: true }).click();
  await expect(panel.locator("iframe")).toBeVisible();
  await expect(
    panel.getByRole("button", { name: "关闭产物面板" }),
  ).toBeVisible();
  const box = await panel.boundingBox();
  expect(box!.width).toBeGreaterThan(280);
  expect(box!.x + box!.width).toBeLessThanOrEqual(950);
  expect((await panel.locator("iframe").boundingBox())!.y - box!.y).toBe(44);
  await page.screenshot({ path: testInfo.outputPath("artifacts-narrow.png") });
});

test("long conversation scrolls independently while split headers and composer stay in view", async ({
  page,
}) => {
  await mockArtifacts(
    page,
    "dark",
    html,
    Array.from(
      { length: 60 },
      (_, i) => `第 ${i + 1} 段：核对交付结果与产物预览。`,
    ).join("\n\n"),
  );
  await page.goto("/o/o1/chat/c1?artifact=html");
  const panel = page.getByRole("complementary", { name: "会话产物" });
  const header = page
    .getByRole("button", { name: "设置", exact: true })
    .locator("xpath=ancestor::header");
  const composer = page.getByRole("textbox", { name: "输入消息" });
  const message = page.getByText("第 60 段：核对交付结果与产物预览。", {
    exact: true,
  });
  await expect(message).toBeVisible();
  await expect(panel.locator("iframe")).toBeVisible();
  const main = page.locator("[data-org-main]");
  const mainBox = (await main.boundingBox())!;
  const assertContained = async () => {
    expect((await header.boundingBox())!.y).toBe(mainBox.y);
    const composerBox = (await composer.boundingBox())!;
    expect(composerBox.y + composerBox.height).toBeLessThanOrEqual(
      mainBox.y + mainBox.height,
    );
    expect(
      await main.evaluate((el) => el.scrollHeight - el.clientHeight),
    ).toBeLessThanOrEqual(1);
  };
  await assertContained();
  const panelBox = (await panel.boundingBox())!;
  expect(panelBox.y).toBe(mainBox.y);
  expect(panelBox.height).toBe(mainBox.height);
  // Scroll the actual message viewport; neither header nor the preview should move.
  const scroller = message.locator(
    'xpath=ancestor::div[contains(@class,"overflow-y-auto")][1]',
  );
  expect(await scroller.evaluate((el) => el.scrollHeight)).toBeGreaterThan(
    (await scroller.boundingBox())!.height,
  );
  await scroller.evaluate((el) => {
    el.scrollTop = 0;
  });
  await expect(
    page.getByText("第 1 段：核对交付结果与产物预览。", { exact: true }),
  ).toBeInViewport();
  await assertContained();
  expect(await panel.boundingBox()).toEqual(panelBox);
  await panel.getByRole("button", { name: "关闭产物面板" }).click();
  await expect(panel).toHaveCount(0);
  await assertContained();
  expect((await header.boundingBox())!.width).toBe(mainBox.width);
});

test("dual sidebars: top-right workspace toggle, inline comments/proposals tabs and keyboard shortcuts", async ({
  page,
}) => {
  await mockArtifacts(page, "dark");
  await page.goto("/o/o1/chat/c1");
  const panel = page.getByRole("complementary", { name: "会话产物" });
  await expect(panel).toHaveCount(0);

  // 点击顶栏工作区按钮打开启动页，展示最近产物与内联评论/提案入口
  await page.getByRole("button", { name: "打开工作区", exact: true }).click();
  await expect(panel).toBeVisible();
  await expect(panel.getByText("最近产物")).toBeVisible();
  await panel.getByRole("button", { name: "已保存评论", exact: true }).click();
  await expect(
    panel.getByRole("tab", { name: "已保存评论", exact: true }),
  ).toHaveAttribute("aria-selected", "true");
  await expect(panel.getByText("还没有保存的评论")).toBeVisible();

  // 从「更多」菜单切到修改提案内联标签（不弹模态框）
  await page.getByRole("button", { name: "更多操作", exact: true }).click();
  await page.getByRole("menuitem", { name: "修改提案", exact: true }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(
    panel.getByRole("tab", { name: "修改提案", exact: true }),
  ).toHaveAttribute("aria-selected", "true");
  await expect(panel.getByText("暂无修改提案")).toBeVisible();

  // ⌘⇧\ / Ctrl+Shift+\ 收起并恢复工作区
  const mod = process.platform === "darwin" ? "Meta" : "Control";
  await page.keyboard.press(`${mod}+Shift+Backslash`);
  await expect(panel).toHaveCount(0);
  await page.keyboard.press(`${mod}+Shift+Backslash`);
  await expect(panel).toBeVisible();
  await expect(
    panel.getByRole("tab", { name: "修改提案", exact: true }),
  ).toHaveAttribute("aria-selected", "true");

  // ⌘\ / Ctrl+\ 切换左侧导航栏收起并恢复
  await page.keyboard.press(`${mod}+Backslash`);
  await expect(
    page.getByRole("button", { name: "展开侧边栏", exact: true }),
  ).toBeVisible();
  await page.keyboard.press(`${mod}+Backslash`);
  await expect(
    page.getByRole("button", { name: "展开侧边栏", exact: true }),
  ).toHaveCount(0);
});

test("W01: right workspace switches cleanly between 2-row (459px) and single 44px row (460px) from both sides", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  for (const theme of ["dark", "light"] as const) {
    await mockArtifacts(page, theme);
    await page.goto("/o/o1/chat/c1?artifact=md");
    const panel = page.getByRole("complementary", { name: "会话产物" });
    await expect(
      panel.getByRole("heading", { name: "产物预览" }),
    ).toBeVisible();
    const handle = page.getByRole("separator", { name: "调整产物面板宽度" });
    const dragToWidth = async (target: number) => {
      const box = (await panel.boundingBox())!;
      const handleBox = (await handle.boundingBox())!;
      const delta = box.width - target;
      const cx = handleBox.x + handleBox.width / 2;
      const cy = handleBox.y + 40;
      await page.mouse.move(cx, cy);
      await page.mouse.down();
      await page.mouse.move(cx + delta, cy, { steps: 6 });
      await page.mouse.up();
      await expect
        .poll(async () => Math.round((await panel.boundingBox())!.width))
        .toBe(target);
      await expect(handle).toHaveAttribute("aria-valuenow", String(target));
    };
    const isSingleRow = async () => {
      const panelBox = (await panel.boundingBox())!;
      const previewBtn = panel.getByRole("button", {
        name: "预览",
        exact: true,
      });
      const btnBox = (await previewBtn.boundingBox())!;
      return btnBox.y - panelBox.y < 44;
    };

    for (const [width, expectedSingle] of [
      [461, true],
      [460, true],
      [459, false],
      [280, false],
      [460, true],
    ] as const) {
      await dragToWidth(width);
      expect(await isSingleRow()).toBe(expectedSingle);
      const overflowX = await page.evaluate(
        () => document.documentElement.scrollWidth > window.innerWidth,
      );
      expect(overflowX).toBe(false);
    }
  }
});

test("W02 & W05: locating a long-document annotation from draft tray and saved comments scrolls into view and retains selection", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  const longMarkdown = [
    "# 季度验收报告",
    ...Array.from(
      { length: 80 },
      (_, i) => `第 ${i + 1} 段：核对长文审阅、定位滚动与选区保持能力。`,
    ),
    "请修改**原文**位置。",
  ].join("\n\n");
  const start = Buffer.byteLength(
    longMarkdown.slice(0, longMarkdown.indexOf("原文")),
  );
  const end = Buffer.byteLength(
    longMarkdown.slice(0, longMarkdown.indexOf("原文") + 2),
  );
  const longArtifact = {
    ...artifacts[1]!,
    id: "md-long",
    name: "报告.md",
    content_id: "content-md-long",
    head_artifact_id: "md-long",
    size_bytes: Buffer.byteLength(longMarkdown),
  };
  await mockArtifacts(page, "dark");
  await page.route("**/api/**", async (route) => {
    const pathname = new URL(route.request().url()).pathname;
    if (pathname === `${org}/conversations/c1/artifacts`)
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ artifacts: [longArtifact] }),
      });
    if (pathname === `${org}/runs/r1/artifacts/md-long`)
      return route.fulfill({
        status: 200,
        contentType: "text/markdown",
        body: longMarkdown,
      });
    if (pathname === `${org}/conversations/c1/comments`)
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          threads: [
            {
              id: "thread-1",
              version: 1,
              status: "OPEN",
              source: {
                name: "报告.md",
                file: {
                  id: "fc-1",
                  artifact_id: "md-long",
                  sha256: longArtifact.sha256,
                  comments: [
                    {
                      thread_id: "thread-1",
                      body: "请核对末段",
                      anchor: { kind: "text", start, end, quote: "原文" },
                    },
                  ],
                },
              },
              comments: [
                {
                  id: "post-1",
                  author_id: "u1",
                  body: "请核对末段",
                  created_at: ts,
                },
              ],
            },
          ],
          next_cursor: "",
        }),
      });
    return route.fallback();
  });

  await page.goto("/o/o1/chat/c1?artifact=md-long");
  const panel = page.getByRole("complementary", { name: "会话产物" });
  await expect(
    panel.getByRole("heading", { name: "季度验收报告" }),
  ).toBeVisible();

  // 开启批注，滚至末段并选「原文」创建待发送草稿，再就地修改意见
  await panel.getByRole("button", { name: "批注", exact: true }).click();
  const target = panel.getByText("原文", { exact: true }).last();
  await target.scrollIntoViewIfNeeded();
  await target.evaluate((el) => {
    const range = document.createRange();
    range.selectNodeContents(el);
    const sel = window.getSelection()!;
    sel.removeAllRanges();
    sel.addRange(range);
  });
  await target.dispatchEvent("mouseup");
  await page
    .getByRole("textbox", { name: "针对选区的修改意见" })
    .fill("第一版");
  await page.getByRole("button", { name: "添加到聊天", exact: true }).click();
  await page.getByRole("button", { name: "编辑批注", exact: true }).click();
  await page.getByRole("textbox", { name: "编辑批注" }).fill("修改后的意见");
  await page.getByRole("button", { name: "保存", exact: true }).click();

  const assertLocatedAndRetained = async () => {
    await expect
      .poll(async () =>
        page.evaluate(() => {
          const code = document.querySelector("pre > code");
          const state = code?.getAttribute("data-highlight-state");
          const sel = window.getSelection();
          if (!sel || sel.rangeCount === 0)
            return { state, text: "", visible: false };
          const range = sel.getRangeAt(0);
          const rect = range.getBoundingClientRect();
          const scroller = code?.closest("div.overflow-auto");
          const sRect = scroller?.getBoundingClientRect();
          const visible = Boolean(
            sRect &&
            rect.height > 0 &&
            rect.top >= sRect.top - 1 &&
            rect.bottom <= sRect.bottom + 1,
          );
          return { state, text: sel.toString(), visible };
        }),
      )
      .toEqual({ state: "ready", text: "原文", visible: true });
    // 停留 500ms 后复检，确认不会被异步高亮或父级滚动恢复清空
    await page.waitForTimeout(500);
    const stable = await page.evaluate(() => {
      const code = document.querySelector("pre > code");
      const sel = window.getSelection();
      const rect = sel?.rangeCount
        ? sel.getRangeAt(0).getBoundingClientRect()
        : null;
      const sRect = code?.closest("div.overflow-auto")?.getBoundingClientRect();
      return {
        text: sel?.toString() ?? "",
        visible: Boolean(
          rect &&
          sRect &&
          rect.top >= sRect.top - 1 &&
          rect.bottom <= sRect.bottom + 1,
        ),
      };
    });
    expect(stable).toEqual({ text: "原文", visible: true });
  };

  // 路径 A（W02 已关）：滚回文首并关闭面板后，从草稿托盘定位原文
  await panel
    .locator("div.overflow-auto")
    .first()
    .evaluate((el) => {
      el.scrollTop = 0;
    });
  await page.evaluate(() => window.getSelection()?.removeAllRanges());
  await panel
    .getByRole("button", { name: "关闭产物面板", exact: true })
    .click();
  await expect(panel).toHaveCount(0);
  await page.getByText("修改后的意见").click();
  await page.getByRole("button", { name: "打开 报告.md", exact: true }).click();
  await assertLocatedAndRetained();

  // 路径 B（W02 已开）：面板保持打开、切回预览并滚回文首，再次从草稿托盘定位原文
  await panel.getByRole("button", { name: "预览", exact: true }).click();
  await panel
    .locator("div.overflow-auto")
    .first()
    .evaluate((el) => {
      el.scrollTop = 0;
    });
  await page.evaluate(() => window.getSelection()?.removeAllRanges());
  await page.getByRole("button", { name: "打开 报告.md", exact: true }).click();
  await assertLocatedAndRetained();

  // W05：从已保存评论标签点击「查看来源」，打开原文并滚入视口保留选区
  await page.getByRole("button", { name: "更多操作", exact: true }).click();
  await page
    .getByRole("menuitem", { name: "已保存的评论", exact: true })
    .click();
  await panel.locator("summary").filter({ hasText: "请核对末段" }).click();
  await panel.getByRole("button", { name: "查看来源", exact: true }).click();
  await assertLocatedAndRetained();
});
