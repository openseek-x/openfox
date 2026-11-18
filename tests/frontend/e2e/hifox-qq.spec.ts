import { test, expect } from "@playwright/test";
import fs from "node:fs";
const file = process.env.OPENFOX_I14_BROWSER_FILE;
test.skip(!file, "requires isolated QQ HTTP/PG/native SDK harness");
test("I14 actual SDK QR, signed identity, original Run reply and disconnect", async ({
  page,
}) => {
  test.setTimeout(180_000);
  const f = JSON.parse(fs.readFileSync(file!, "utf8"));
  for (const key of ["web_url", "fixture_url", "api_url"]) {
    const u = new URL(f[key]);
    expect(u.protocol).toBe("http:");
    expect(u.hostname).toBe("127.0.0.1");
  }
  const control = async (path: string, data: object = {}) => {
    const r = await page.request.post(new URL(path, f.fixture_url).href, {
      data,
    });
    expect(r.status(), await r.text()).toBe(200);
    return r.json();
  };
  const login = await page.request.post(
    new URL("/api/v1/auth/login", f.web_url).href,
    { data: { email: f.email, password: f.password, client_kind: "web" } },
  );
  expect(login.status()).toBe(201);
  await page.goto(
    new URL(`/o/${f.organization_id}/settings/integrations/qq`, f.web_url).href,
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
  await page
    .getByRole("button", { name: "连接 Agent 到 QQ", exact: true })
    .click();
  const create = page.getByRole("dialog", {
    name: "连接 Agent 到 QQ",
    exact: true,
  });
  await create.getByLabel("选择 Agent", { exact: true }).click();
  await page
    .getByRole("option", { name: "QQ browser Agent", exact: true })
    .click();
  await create
    .getByLabel("QQ 连接名称", { exact: true })
    .fill("QQ browser connection");
  const created = page.waitForResponse(
    (r) =>
      r.request().method() === "POST" &&
      new URL(r.url()).pathname.endsWith("/integrations/qq/bots"),
  );
  await create.getByRole("button", { name: "创建", exact: true }).click();
  const createdResponse = await created;
  expect(createdResponse.status()).toBe(201);
  const config = await page.request.get(
    new URL(
      `/api/v1/organizations/${f.organization_id}/integrations/qq/configuration`,
      f.web_url,
    ).href,
  );
  expect(config.ok()).toBeTruthy();
  const bot = (await config.json()).bots.find(
    (item: { name: string }) => item.name === "QQ browser connection",
  );
  expect(bot).toBeDefined();
  const management = page.getByRole("dialog", {
    name: "管理 QQ 连接",
    exact: true,
  });
  await management
    .getByRole("button", { name: "显示官方连接二维码", exact: true })
    .click();
  const qr = page.getByRole("dialog", { name: "QQ 官方扫码连接", exact: true });
  await control("/pump");
  await expect(
    qr.getByRole("link", { name: "打开腾讯官方连接页面" }),
  ).toBeVisible({ timeout: 20_000 });
  const official = new URL((await qr.getByRole("link").getAttribute("href"))!);
  expect(official.origin).toBe("https://q.qq.com");
  expect(official.pathname).toBe("/qqbot/openclaw/connect.html");
  // The actual official SDK runs in a sandbox; only this owned peer answers.
  // Never open the official URL or scan with a real QQ account.
  await control("/scan");
  await expect(qr.getByText("已完成", { exact: true })).toBeVisible({
    timeout: 20_000,
  });
  await qr.getByRole("button", { name: "关闭", exact: true }).last().click();
  await expect(
    management.getByText("待核验回调与签名事件", { exact: true }),
  ).toBeVisible();
  await management
    .getByRole("button", { name: "显示回调地址", exact: true })
    .click();
  const hook = page.getByRole("dialog", { name: "配置 QQ 回调", exact: true });
  await hook.getByRole("button", { name: "显示回调地址", exact: true }).click();
  await expect(
    hook.getByText(/https:\/\/openfox\.example\.test\/api\/v1\/hooks\/qq\//),
  ).toBeVisible();
  await hook.getByRole("button", { name: "关闭", exact: true }).last().click();
  await control("/challenge", { BotID: bot.id });
  await control("/event", {
    BotID: bot.id,
    ID: "activation",
    Text: "controlled activation",
  });
  await expect(management.getByText("已激活", { exact: true })).toBeVisible();
  await management
    .getByRole("button", { name: "关闭", exact: true })
    .last()
    .click();
  await page
    .getByRole("button", { name: "验证我的 QQ 单聊身份", exact: true })
    .click();
  const proof = page.getByRole("dialog", {
    name: "确认 QQ 身份或群授权",
    exact: true,
  });
  const command = await proof.locator("pre").innerText();
  expect(command).toMatch(/^\/openfox-bind /);
  await control("/event", {
    BotID: bot.id,
    ID: "binding-proof",
    Text: command,
  });
  await expect(
    proof.getByRole("button", { name: "确认绑定本人身份", exact: true }),
  ).toBeEnabled();
  await proof
    .getByRole("button", { name: "确认绑定本人身份", exact: true })
    .click();
  await expect(
    page.getByRole("button", { name: "解除身份绑定", exact: true }),
  ).toBeVisible();
  await control("/event", {
    BotID: bot.id,
    ID: "original-request",
    Text: "controlled ordinary Agent request",
  });
  await control("/finish-run");
  const stateResponse = await page.request.get(
    new URL("/proof", f.fixture_url).href,
  );
  expect(stateResponse.ok()).toBeTruthy();
  const state = await stateResponse.json();
  expect(state.creates).toBe(1);
  expect(state.routes).toBe(1);
  expect(state.inputs).toBe(1);
  expect(state.replies_done).toBe(1);
  expect(state.posts).toHaveLength(1);
  expect(state.posts[0]).toMatchObject({
    msg_id: "original-request",
    msg_seq: 1,
    content: "Controlled QQ browser terminal reply",
  });
  await page.getByRole("button", { name: "管理 QQ 连接", exact: true }).click();
  await expect(
    management.getByText("近期回复状态", { exact: true }),
  ).toBeVisible();
  await expect(
    management.getByText("已完成", { exact: true }).first(),
  ).toBeVisible();
  await management
    .getByRole("button", { name: "本地断开 QQ", exact: true })
    .click();
  const disconnect = page.getByRole("dialog", {
    name: "本地断开 QQ",
    exact: true,
  });
  await disconnect.getByRole("button", { name: "确认", exact: true }).click();
  await expect(
    management.getByText("已本地断开", { exact: true }),
  ).toBeVisible();
  await expect(
    management.getByRole("button", { name: "释放 Agent 绑定", exact: true }),
  ).toBeVisible();
  await control("/pump");
  const after = await (
    await page.request.get(new URL("/proof", f.fixture_url).href)
  ).json();
  expect(after.posts).toHaveLength(1);
  const output = process.env.OPENFOX_I14_BROWSER_PROOF;
  if (output)
    fs.writeFileSync(
      output,
      JSON.stringify(
        {
          native_sdk_creates: state.creates,
          routes: state.routes,
          frozen_inputs: state.inputs,
          replies_done: state.replies_done,
          post_count_before_disconnect: state.posts.length,
          post_count_after_disconnect: after.posts.length,
          actual_original_message: state.posts[0].msg_id,
          actual_sequence: state.posts[0].msg_seq,
        },
        null,
        2,
      ),
      { mode: 0o600 },
    );
});
