import { test, expect, type Page } from "@playwright/test";
import fs from "node:fs";

// Real API/PG harness only. The local fixture performs actual signed callbacks;
// no Playwright route interception or synthetic application state is used.
const fixtureFile = process.env.OPENFOX_I15_BROWSER_FILE;
test.skip(
  !fixtureFile,
  "requires the exclusive I15 HTTP/PostgreSQL/worker harness",
);
type Fixture = {
  api_url: string;
  web_url: string;
  fixture_url: string;
  organization_id: string;
  space_id: string;
  agent_id: string;
  email: string;
  password: string;
};
function fixture(): Fixture {
  const value = JSON.parse(fs.readFileSync(fixtureFile!, "utf8")) as Fixture;
  for (const raw of [value.api_url, value.web_url, value.fixture_url]) {
    const url = new URL(raw);
    expect(["127.0.0.1", "localhost", "[::1]"]).toContain(url.hostname);
    expect(["http:", "https:"]).toContain(url.protocol);
    expect(url.username + url.password).toBe("");
  }
  return value;
}
async function completeOAuth(
  page: Page,
  f: Fixture,
  button: string,
  purpose: "organization" | "personal" | "app",
) {
  const pending = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      new URL(response.url()).pathname.endsWith(
        "/integrations/slack/oauth/start",
      ),
  );
  await page.getByRole("button", { name: button, exact: true }).click();
  const response = await pending;
  expect(response.status()).toBe(201);
  const session = await response.json();
  const authorize = new URL(session.authorize_url);
  expect(authorize.origin).toBe("https://slack.com");
  expect(authorize.pathname).toBe(
    purpose === "personal"
      ? "/openid/connect/authorize"
      : "/oauth/v2/authorize",
  );
  expect(authorize.searchParams.get("state")).toBeTruthy();
  // Do not open Slack or log state. The private harness has the same persisted
  // session and sends a real request to the public callback with local TLS peers.
  const callback = await page.request.post(
    new URL("/complete-oauth", f.fixture_url).href,
    { data: { session_id: session.id, purpose } },
  );
  expect(callback.ok()).toBeTruthy();
  await expect
    .poll(async () => {
      const status = await page.request.get(
        new URL(
          `/api/v1/organizations/${f.organization_id}/integrations/slack/oauth/sessions/${session.id}`,
          f.web_url,
        ).href,
      );
      expect(status.ok()).toBeTruthy();
      return (await status.json()).status;
    })
    .toBe("DONE");
  // A successful personal binding replaces its launch button and closes that
  // dialog. Close any remaining dialog, then refresh through the actual UI.
  await page.keyboard.press("Escape");
  await page.getByRole("button", { name: "刷新状态", exact: true }).click();
  return session;
}

test("I15 real workspace installation, separate OIDC and notification bootstrap", async ({
  page,
}) => {
  const f = fixture();
  test.setTimeout(180_000);
  const login = await page.request.post(
    new URL("/api/v1/auth/login", f.web_url).href,
    { data: { email: f.email, password: f.password, client_kind: "web" } },
  );
  expect(login.status()).toBe(201);
  const settings = `/o/${f.organization_id}/settings/integrations/slack`;
  await page.goto(new URL(settings, f.web_url).href);
  await page.evaluate(
    (org) =>
      localStorage.setItem(
        `openfox:onboarding:${org}`,
        JSON.stringify({ step: "done" }),
      ),
    f.organization_id,
  );
  await page.reload();
  await completeOAuth(page, f, "安装工作区应用", "organization");
  await expect(
    page.getByRole("button", { name: "绑定本人身份", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "创建 Agent App", exact: true }),
  ).toBeDisabled();
  await completeOAuth(page, f, "绑定本人身份", "personal");
  await expect(
    page.getByRole("button", { name: "创建 Agent App", exact: true }),
  ).toBeEnabled();
  await page.reload();
  await expect(
    page.getByRole("button", { name: "解除本人绑定", exact: true }),
  ).toBeVisible();
  await page.goto(
    new URL(`/o/${f.organization_id}/settings/notifications`, f.web_url).href,
  );
  await page.getByText("Slack", { exact: true }).click();
  await expect(
    page.getByRole("switch", { name: "开启 Slack 通知", exact: true }),
  ).not.toBeChecked();
  await expect(
    page.getByRole("switch", { name: "允许真人消息提醒", exact: true }),
  ).not.toBeChecked();
  // The following cases use the same exclusive organization to cover dynamic
  // installation, channel authorization and durable notification reconciliation.
});

async function login(page: Page, f: Fixture) {
  const r = await page.request.post(
    new URL("/api/v1/auth/login", f.web_url).href,
    { data: { email: f.email, password: f.password, client_kind: "web" } },
  );
  expect(r.status()).toBe(201);
  await page.goto(
    new URL(`/o/${f.organization_id}/settings/integrations/slack`, f.web_url)
      .href,
  );
  await page.evaluate(
    (org) =>
      localStorage.setItem(
        `openfox:onboarding:${org}`,
        JSON.stringify({ step: "done" }),
      ),
    f.organization_id,
  );
  await page.reload();
}
async function control(
  page: Page,
  f: Fixture,
  path: string,
  data: object = {},
) {
  const r = await page.request.post(new URL(path, f.fixture_url).href, {
    data,
  });
  expect(r.ok(), `controlled ${path} status ${r.status()}`).toBeTruthy();
  return r.json();
}
async function configuration(page: Page, f: Fixture) {
  const r = await page.request.get(
    new URL(
      `/api/v1/organizations/${f.organization_id}/integrations/slack/configuration`,
      f.web_url,
    ).href,
  );
  expect(r.ok()).toBeTruthy();
  return r.json();
}
async function state(page: Page, f: Fixture) {
  const r = await page.request.get(new URL("/state", f.fixture_url).href);
  expect(r.ok()).toBeTruthy();
  return r.json();
}

test("I15 real dynamic App, URL/OAuth gates, channel consent and inbound route", async ({
  page,
}) => {
  const f = fixture();
  test.setTimeout(180_000);
  await login(page, f);
  await page
    .getByRole("button", { name: "创建 Agent App", exact: true })
    .click();
  const dialog = page.getByRole("dialog", {
    name: "创建 Agent App",
    exact: true,
  });
  await dialog.getByLabel("Agent", { exact: true }).click();
  await page
    .getByRole("option", { name: "Slack browser Agent", exact: true })
    .click();
  await dialog
    .getByLabel("Slack App 名称", { exact: true })
    .fill("Controlled browser Agent");
  const synthetic = "synthetic-browser-config-refresh";
  await dialog
    .getByLabel("Configuration refresh token", { exact: true })
    .fill(synthetic);
  await dialog
    .getByRole("checkbox", {
      name: "我了解该凭据的应用管理权限和轮换影响",
      exact: true,
    })
    .check();
  const pending = page.waitForResponse(
    (r) =>
      r.request().method() === "POST" &&
      new URL(r.url()).pathname.endsWith("/integrations/slack/apps/operations"),
  );
  await dialog
    .getByRole("button", { name: "创建独立应用", exact: true })
    .click();
  const response = await pending;
  expect(response.status()).toBe(202);
  const app = (await configuration(page, f)).apps.find(
    (item: { agent_id: string; name: string }) =>
      item.agent_id === f.agent_id && item.name === "Controlled browser Agent",
  );
  expect(app).toBeDefined();
  expect(app.id).toBeTruthy();
  await expect(dialog).not.toBeVisible();
  expect(
    await page.evaluate(() =>
      JSON.stringify({ local: localStorage, session: sessionStorage }),
    ),
  ).not.toContain(synthetic);
  await expect
    .poll(async () => {
      await control(page, f, "/pump");
      return (await configuration(page, f)).apps.find(
        (a: { id: string }) => a.id === app.id,
      )?.state;
    })
    .toBe("WAITING_URL_VERIFICATION");
  await page.getByRole("button", { name: "刷新状态", exact: true }).click();
  const card = page.getByRole("article", {
    name: "Controlled browser Agent",
    exact: true,
  });
  await expect(
    card.getByText("等待手动 URL 核验", { exact: true }),
  ).toBeVisible();
  await control(page, f, "/challenge", { app_id: app.id });
  await control(page, f, "/pump");
  await page.getByRole("button", { name: "刷新状态", exact: true }).click();
  await completeOAuth(page, f, "安装此 App", "app");
  await expect
    .poll(
      async () =>
        (await configuration(page, f)).apps.find(
          (a: { id: string }) => a.id === app.id,
        )?.state,
    )
    .toBe("ACTIVE");
  await page.reload();
  await card
    .getByRole("button", { name: "频道公开回复授权", exact: true })
    .click();
  const channels = page.getByRole("dialog", {
    name: "频道公开回复授权",
    exact: true,
  });
  await channels.getByLabel("Slack 频道 ID", { exact: true }).fill("C01");
  await expect(
    channels.getByRole("button", { name: "启用频道回复", exact: true }),
  ).toBeDisabled();
  await channels
    .getByRole("checkbox", {
      name: "我确认频道内所有成员可以看到回复和共享的任务预览",
      exact: true,
    })
    .check();
  await channels
    .getByRole("button", { name: "启用频道回复", exact: true })
    .click();
  await expect(channels.getByText("已启用", { exact: true })).toBeVisible();
  await page.keyboard.press("Escape");
  await control(page, f, "/event", {
    app_id: app.id,
    channel: "C01",
    text: `<@${(await configuration(page, f)).apps.find((a: { id: string }) => a.id === app.id).bot_user_id}> controlled inbound`,
  });
  await expect
    .poll(async () => (await state(page, f)).inputs)
    .toBeGreaterThan(0);
  await page.reload();
  await card
    .getByRole("button", { name: "频道公开回复授权", exact: true })
    .click();
  const row = page.getByRole("dialog", {
    name: "频道公开回复授权",
    exact: true,
  });
  await row.getByRole("button", { name: "停用频道回复", exact: true }).click();
  await row
    .getByRole("button", { name: "停用频道回复", exact: true })
    .last()
    .click();
  await expect(row.getByText("已停用", { exact: true })).toBeVisible();
  await page.keyboard.press("Escape");
  await card.getByRole("button", { name: "本地断开", exact: true }).click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "本地断开", exact: true })
    .click();
  await expect(
    card.getByRole("button", { name: "重新安装", exact: true }),
  ).toBeVisible();
  await expect(
    card.getByRole("button", { name: "取消并释放绑定", exact: true }),
  ).toBeVisible();
  const proof = await state(page, f);
  expect(proof.creates).toBe(1);
  expect(proof.updates).toBeGreaterThanOrEqual(1);
  fs.writeFileSync(
    `${fixtureFile}.dynamic-proof.json`,
    JSON.stringify({
      creates: proof.creates,
      updates: proof.updates,
      routes: proof.routes,
      inputs: proof.inputs,
      events_done: proof.events_done,
    }),
    { mode: 0o600 },
  );
});

test("I15 real task notification UNKNOWN verifies existing receipt without a second send", async ({
  page,
  playwright,
}) => {
  const f = fixture();
  test.setTimeout(180_000);
  await login(page, f);
  const config = await configuration(page, f);
  const central = config.apps.find(
    (a: { kind: string }) => a.kind === "CENTRAL",
  );
  expect(central?.state).toBe("ACTIVE");
  await page.goto(
    new URL(`/o/${f.organization_id}/settings/notifications`, f.web_url).href,
  );
  await page.getByText("Slack", { exact: true }).click();
  const enabled = page.getByRole("switch", {
    name: "开启 Slack 通知",
    exact: true,
  });
  if (!(await enabled.isChecked())) await enabled.click();
  await expect(
    page.getByRole("switch", { name: "开启 Slack 通知", exact: true }),
  ).toBeChecked();
  await expect(
    page.getByRole("switch", { name: "允许真人消息提醒", exact: true }),
  ).not.toBeChecked();
  const me = await page.request.get(new URL("/api/v1/me", f.web_url).href);
  expect(me.ok()).toBeTruthy();
  const owner = (await me.json()).id;
  expect(owner).toBeTruthy();
  const admin = await playwright.request.newContext();
  try {
    const auth = await admin.post(
      new URL("/api/v1/auth/login", f.web_url).href,
      {
        data: {
          email: `fs-admin-${f.organization_id.slice(0, 8)}@example.test`,
          password: f.password,
          client_kind: "web",
        },
      },
    );
    expect(auth.status()).toBe(201);
    const csrf = (await admin.storageState()).cookies.find(
      (c) => c.name === "openfox_csrf",
    )?.value;
    expect(csrf).toBeTruthy();
    await control(page, f, "/unknown-next", { unknown: true });
    const created = await admin.post(
      new URL(
        `/api/v1/organizations/${f.organization_id}/spaces/${f.space_id}/tasks`,
        f.web_url,
      ).href,
      {
        headers: { "X-CSRF-Token": csrf! },
        data: {
          title: "Controlled Slack browser notification",
          assignees: [{ kind: "HUMAN", id: owner }],
        },
      },
    );
    expect(created.status()).toBe(201);
  } finally {
    await admin.dispose();
  }
  await expect
    .poll(async () => {
      await control(page, f, "/pump");
      const r = await page.request.get(
        new URL(
          `/api/v1/organizations/${f.organization_id}/integrations/slack/apps/${central.id}/deliveries`,
          f.web_url,
        ).href,
      );
      expect(r.ok()).toBeTruthy();
      return (await r.json()).deliveries.some(
        (d: { source_kind: string; state: string }) =>
          d.source_kind === "NOTIFICATION" && d.state === "UNKNOWN",
      );
    })
    .toBe(true);
  const before = await state(page, f),
    sent = before.posts.at(-1);
  expect(sent?.ts).toBeTruthy();
  await page.goto(
    new URL(`/o/${f.organization_id}/settings/integrations/slack`, f.web_url)
      .href,
  );
  const card = page.getByRole("article", {
    name: central.name || central.team_id || central.id,
    exact: true,
  });
  await card.getByRole("button", { name: "发送记录", exact: true }).click();
  await page.getByRole("button", { name: "核对已有消息", exact: true }).click();
  const verify = page.getByRole("dialog", {
    name: "核对已有消息",
    exact: true,
  });
  await verify.getByLabel("原消息 Slack 时间戳", { exact: true }).fill(sent.ts);
  const pending = page.waitForResponse(
    (r) =>
      r.request().method() === "POST" &&
      new URL(r.url()).pathname.endsWith("/resolve"),
  );
  await verify
    .getByRole("button", { name: "核对已有消息", exact: true })
    .click();
  expect((await pending).status()).toBe(200);
  await expect(
    page.getByText("已核对到匹配的已发送消息，没有再次发送。", { exact: true }),
  ).toBeVisible();
  await control(page, f, "/pump");
  const after = await state(page, f);
  expect(after.posts.length).toBe(before.posts.length);
  fs.writeFileSync(
    `${fixtureFile}.notification-proof.json`,
    JSON.stringify({
      source: "actual_task_assignment",
      unknown_to_done: true,
      posts_before: before.posts.length,
      posts_after: after.posts.length,
      verified_no_second_send: true,
    }),
    { mode: 0o600 },
  );
});
