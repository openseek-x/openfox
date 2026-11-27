import { readFileSync } from "node:fs";
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
          "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob: https:; media-src 'self' blob:; font-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'",
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
  expect(
    (await panel.locator("iframe").boundingBox())!.y - initialPanel.y,
  ).toBe(96);
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
  expect((await panel.locator("iframe").boundingBox())!.y - box!.y).toBe(96);
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

// 测试证据交付（OPE-19）：单文件 HTML 报告（> 7,447,186 字节，步骤截图内嵌）与真实 WebM 录像，
// 在部署 CSP（deploy/nginx-web.example.conf）下渲染与播放。
test("test evidence: large HTML report renders and a recorded WebM plays under the deployed CSP", async ({
  page,
  browser,
}) => {
  // 用 Playwright 录制一段真实 WebM（与测试智能体的用例录像同源）。
  const recordDir = test.info().outputPath("recording");
  const recorder = await browser.newContext({
    recordVideo: { dir: recordDir, size: { width: 320, height: 240 } },
    viewport: { width: 320, height: 240 },
  });
  const recorded = await recorder.newPage();
  await recorded.setContent(
    `<body style="margin:0"><div id="b" style="width:320px;height:240px;background:#28756c"></div></body>`,
  );
  for (const color of ["#d93025", "#2b579a", "#f0bf00", "#7c3aed"]) {
    await recorded.evaluate(
      (c) => (document.getElementById("b")!.style.background = c),
      color,
    );
    await recorded.waitForTimeout(400);
  }
  // 步骤截图同样取自录制页面（真实 PNG），重复内嵌以达到报告体积。
  await recorded.evaluate(() => {
    const canvas = document.createElement("canvas");
    canvas.width = 256;
    canvas.height = 180;
    const ctx = canvas.getContext("2d")!;
    const image = ctx.createImageData(256, 180);
    for (let i = 0; i < image.data.length; i++)
      image.data[i] = i % 4 === 3 ? 255 : Math.floor(Math.random() * 256);
    ctx.putImageData(image, 0, 0);
    document.body.replaceChildren(canvas);
  });
  const step = (await recorded.locator("canvas").screenshot()).toString(
    "base64",
  );
  await recorded.close();
  const webm = readFileSync((await recorded.video()!.path())!);
  await recorder.close();
  expect(webm.subarray(0, 4)).toEqual(Buffer.from([0x1a, 0x45, 0xdf, 0xa3]));

  const cases = Array.from(
    { length: 14 },
    (_, c) =>
      `<section><h2>TC-${String(c + 1).padStart(2, "0")} ${c === 2 ? "失败" : "通过"}</h2><table><tr><th>步骤</th><th>预期</th><th>实际</th></tr>` +
      Array.from(
        { length: 3 },
        (_, s) =>
          `<tr><td>${s + 1}</td><td>出现弹窗</td><td>${c === 2 && s === 2 ? "未出现" : "符合"}</td></tr><tr><td colspan="3"><figure><img alt="TC-${c + 1} 步骤 ${s + 1}" src="data:image/png;base64,${step}"><figcaption>步骤 ${s + 1}</figcaption></figure></td></tr>`,
      ).join("") +
      "</table></section>",
  ).join("");
  const report = `<!doctype html><html lang="zh-CN"><head><title>测试报告</title><style>img{width:96px}</style></head><body><h1>测试报告</h1><p>13 通过 · 1 失败</p>${cases}</body></html>`;
  expect(Buffer.byteLength(report)).toBeGreaterThan(7_447_186);

  const extra = [
    {
      id: "report",
      name: "测试报告.html",
      mime_type: "text/plain",
      body: Buffer.from(report),
    },
    {
      id: "video",
      name: "TC-03-弹窗.webm",
      mime_type: "video/webm",
      body: webm,
    },
  ].map(({ body, ...file }) => ({
    ...artifacts[0]!,
    ...file,
    content: "",
    content_id: `content-${file.id}`,
    head_artifact_id: file.id,
    size_bytes: body.length,
    body,
  }));
  await mockArtifacts(page, "light");
  // 后注册的路由优先：扩展产物列表与内容。
  await page.route(
    (url) =>
      url.pathname.startsWith("/api/") && url.pathname.endsWith("/artifacts"),
    (route) =>
      route.fulfill({
        json: {
          artifacts: [
            ...artifacts,
            ...extra.map(({ body: _, ...file }) => file),
          ],
        },
      }),
  );
  // 与服务端 http.ServeContent 一致：声明 Accept-Ranges，并按 Range 头返回 206 分段；视频直连下载地址，拖动依赖它。
  const rangeRequests: string[] = [];
  for (const file of extra)
    await page.route(`**${org}/runs/r1/artifacts/${file.id}`, (route) => {
      const size = file.body.length;
      const header = route.request().headers()["range"];
      const range = header ? /^bytes=(\d*)-(\d*)$/.exec(header) : null;
      const headers = { "Accept-Ranges": "bytes" };
      if (!range || (!range[1] && !range[2]))
        return route.fulfill({
          body: file.body,
          contentType: file.mime_type,
          headers,
        });
      rangeRequests.push(header!);
      const start = range[1]
        ? Number(range[1])
        : Math.max(size - Number(range[2]), 0);
      const end =
        range[1] && range[2] ? Math.min(Number(range[2]), size - 1) : size - 1;
      if (start >= size || start > end)
        return route.fulfill({
          status: 416,
          headers: { ...headers, "Content-Range": `bytes */${size}` },
        });
      return route.fulfill({
        status: 206,
        body: file.body.subarray(start, end + 1),
        contentType: file.mime_type,
        headers: {
          ...headers,
          "Content-Range": `bytes ${start}-${end}/${size}`,
        },
      });
    });
  const csp = readFileSync("deploy/nginx-web.example.conf", "utf8").match(
    /Content-Security-Policy "([^"]+)"/,
  )![1]!;
  expect(csp).toContain("media-src 'self' blob:");
  await page.route(/\/o\/o1\/chat\/c1\?artifact=/, async (route) => {
    const response = await route.fetch();
    await route.fulfill({
      response,
      headers: {
        ...response.headers(),
        "Content-Security-Policy": csp,
      },
    });
  });
  const violations: string[] = [];
  page.on("console", (message) => {
    if (/Content Security Policy/i.test(message.text()))
      violations.push(message.text());
  });

  await page.goto("/o/o1/chat/c1?artifact=report");
  const frame = page.frameLocator('iframe[title="HTML 预览：测试报告.html"]');
  await expect(frame.getByRole("heading", { name: "测试报告" })).toBeVisible({
    timeout: 20000,
  });
  await expect(frame.locator("h2")).toHaveCount(14);
  await expect(frame.locator("img[src^='data:image/png']")).toHaveCount(42);
  await expect(page.getByRole("button", { name: "批注" })).toHaveCount(0);
  await page.screenshot({ path: test.info().outputPath("report.png") });

  await page.goto("/o/o1/chat/c1?artifact=video");
  const video = page.getByLabel("视频：TC-03-弹窗.webm");
  await expect(video).toBeVisible();
  await expect
    .poll(() => video.evaluate((v: HTMLVideoElement) => v.readyState), {
      timeout: 15000,
    })
    .toBeGreaterThanOrEqual(1);
  expect(await video.evaluate((v: HTMLVideoElement) => v.videoWidth)).toBe(320);
  // 拖动：跳到 1 秒处并完成 seeked。
  const seeked = await video.evaluate(
    (v: HTMLVideoElement) =>
      new Promise<number>((resolve) => {
        v.addEventListener("seeked", () => resolve(v.currentTime), {
          once: true,
        });
        v.currentTime = 1;
      }),
  );
  expect(seeked).toBeGreaterThan(0.5);
  // 视频确实走了 Range 分段，而不是整段下载后再拖动。
  expect(rangeRequests.length).toBeGreaterThan(0);
  await expect(page.getByRole("link", { name: "下载" })).toHaveAttribute(
    "download",
    "TC-03-弹窗.webm",
  );
  await page.screenshot({ path: test.info().outputPath("video.png") });
  expect(violations).toEqual([]);
});
