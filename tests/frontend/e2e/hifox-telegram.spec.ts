import { test, expect, type Page } from "@playwright/test";
import fs from "node:fs";

// Real API + PostgreSQL + owned TLS Telegram peer. No page.route or API mocks.
const fixtureFile = process.env.OPENFOX_I18_BROWSER_FILE;
test.skip(!fixtureFile, "requires the exclusive I18 HTTP/PostgreSQL harness");
type Fixture = {
  api_url: string;
  web_url: string;
  fixture_url: string;
  organization_id: string;
  space_id: string;
  agent_id: string;
  email: string;
  password: string;
  token: string;
};
function fixture(): Fixture {
  const f = JSON.parse(fs.readFileSync(fixtureFile!, "utf8")) as Fixture;
  for (const raw of [f.api_url, f.web_url, f.fixture_url]) {
    const u = new URL(raw);
    expect(["127.0.0.1", "localhost", "[::1]"]).toContain(u.hostname);
    expect(u.username + u.password).toBe("");
  }
  return f;
}
async function control(
  page: Page,
  f: Fixture,
  path: string,
  data: unknown = {},
) {
  return page.request.post(new URL(path, f.fixture_url).href, { data });
}
async function state(page: Page, f: Fixture) {
  const r = await page.request.get(new URL("/state", f.fixture_url).href);
  expect(r.status()).toBe(200);
  return r.json();
}
async function pumpUntil(page: Page, f: Fixture, key: string, value: number) {
  await expect
    .poll(
      async () => {
        expect((await control(page, f, "/pump")).status()).toBe(200);
        return (await state(page, f))[key];
      },
      { timeout: 20_000, intervals: [250, 500, 1000] },
    )
    .toBe(value);
}

test("I18 real Bot confirmation, personal identity, group consent and original-route reply", async ({
  page,
}) => {
  const f = fixture();
  test.setTimeout(180_000);
  const login = await page.request.post(
    new URL("/api/v1/auth/login", f.web_url).href,
    { data: { email: f.email, password: f.password, client_kind: "web" } },
  );
  expect(login.status()).toBe(201);
  await page.goto(
    new URL(`/o/${f.organization_id}/settings/integrations/telegram`, f.web_url)
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
  await page.getByRole("button", { name: "连接 Bot", exact: true }).click();
  let dialog = page.getByRole("dialog");
  await dialog.getByRole("button", { name: "连接目标", exact: true }).click();
  await page.getByRole("option", { name: "空间任务助手", exact: true }).click();
  await dialog.getByRole("button", { name: "空间", exact: true }).click();
  await page.getByRole("option", { name: "Fox", exact: true }).click();
  await dialog.getByRole("button", { name: "运行环境", exact: true }).click();
  await page
    .getByRole("option", {
      name: "Codex · Telegram controlled computer",
      exact: true,
    })
    .click();
  await dialog.getByLabel("BotFather Token").fill(f.token);
  const prepResponse = page.waitForResponse(
    (r) =>
      r.request().method() === "POST" &&
      new URL(r.url()).pathname.endsWith("/telegram/preparations"),
  );
  await dialog.getByRole("button", { name: "核验 Bot", exact: true }).click();
  const prep = await prepResponse;
  expect(prep.status()).toBe(200);
  await expect(dialog.getByText("900001", { exact: true })).toBeVisible();
  expect((await state(page, f)).set_calls).toBe(0);
  await expect(
    dialog.getByRole("button", { name: "确认并配置 Webhook", exact: true }),
  ).toBeDisabled();
  await expect(dialog.getByLabel("BotFather Token")).toHaveCount(0);
  await dialog.getByLabel("我已核对 Bot 和固定目标").check();
  await dialog
    .getByRole("button", { name: "确认并配置 Webhook", exact: true })
    .click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await pumpUntil(page, f, "set_calls", 1);
  await page.reload();
  await expect(page.getByText("配置有效", { exact: true })).toBeVisible();
  await expect(page.getByText(/尚未收到认证更新/)).toBeVisible();
  await page.getByRole("button", { name: "绑定本人", exact: true }).click();
  dialog = page.getByRole("dialog");
  await dialog
    .getByRole("button", { name: "生成一次性绑定入口", exact: true })
    .click();
  const link = dialog.getByRole("link", {
    name: "打开 Telegram 完成本人候选",
    exact: true,
  });
  await expect(link).toBeVisible();
  const raw = await link.getAttribute("href");
  const deep = new URL(raw!);
  expect(deep.origin).toBe("https://t.me");
  expect(deep.pathname).toBe("/Current_Bot");
  const code = deep.searchParams.get("start")!.slice("bind_".length);
  expect(
    (await control(page, f, "/event", { kind: "start", code })).status(),
  ).toBe(200);
  expect((await state(page, f)).identities).toBe(0);
  await dialog.getByRole("button", { name: "刷新状态", exact: true }).click();
  await expect(dialog.getByText("987654", { exact: true })).toBeVisible();
  await expect(dialog.getByRole("link")).toHaveCount(0);
  await expect(
    dialog.getByRole("button", { name: "确认", exact: true }),
  ).toBeDisabled();
  await dialog.getByLabel("确认这是我本人").check();
  await dialog.getByRole("button", { name: "确认", exact: true }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  expect((await state(page, f)).identities).toBe(1);
  expect((await control(page, f, "/event", { kind: "group" })).status()).toBe(
    200,
  );
  await page.reload();
  await page.getByRole("button", { name: "群聊授权", exact: true }).click();
  dialog = page.getByRole("dialog");
  await dialog.getByRole("radio").check();
  await expect(
    dialog.getByRole("button", { name: "开启群回复", exact: true }),
  ).toBeDisabled();
  await expect(
    dialog.getByLabel("接收此任务助手群的普通文字作为背景"),
  ).not.toBeChecked();
  await dialog.getByLabel("我接受此群的所有成员可见 Bot 的回复").check();
  await dialog.getByLabel("接收此任务助手群的普通文字作为背景").check();
  await dialog.getByRole("button", { name: "开启群回复", exact: true }).click();
  await expect(dialog.getByText("群回复已开启", { exact: true })).toBeVisible();
  await page.keyboard.press("Escape");
  expect((await state(page, f)).groups).toBe(1);
  expect(
    (await control(page, f, "/event", { kind: "background" })).status(),
  ).toBe(200);
  expect((await state(page, f)).background).toBe(1);
  expect((await control(page, f, "/event", { kind: "topic" })).status()).toBe(
    200,
  );
  await page.reload();
  await expect(
    page.getByText("已忽略的话题消息：1", { exact: true }),
  ).toBeVisible();
  expect((await state(page, f)).inputs).toBe(0);
  expect((await control(page, f, "/event", { kind: "mention" })).status()).toBe(
    200,
  );
  expect((await control(page, f, "/complete")).status()).toBe(200);
  await pumpUntil(page, f, "replies_done", 1);
  let proof = await state(page, f);
  expect(proof.inputs).toBe(1);
  expect(proof.send_calls).toBe(1);
  expect(proof.posts[0].chat_id).toBe(-100200300);
  expect(proof.posts[0].has_openfox_link).toBe(true);
  // A real send reaches the TLS peer, then its response is lost. Repeated pumps
  // and UI refresh must never issue another send for that original reply.
  expect(
    (await control(page, f, "/unknown-next", { unknown: true })).status(),
  ).toBe(200);
  expect((await control(page, f, "/event", { kind: "private" })).status()).toBe(
    200,
  );
  expect((await control(page, f, "/complete")).status()).toBe(200);
  await pumpUntil(page, f, "replies_unknown", 1);
  proof = await state(page, f);
  expect(proof.send_calls).toBe(2);
  expect(proof.posts[1].chat_id).toBe(777888);
  await page.reload();
  await page.getByRole("button", { name: "回复诊断", exact: true }).click();
  dialog = page.getByRole("dialog");
  await expect(
    dialog.getByText(/发送结果未知，可能已到达 Telegram/),
  ).toBeVisible();
  await expect(dialog.getByRole("button", { name: /重发|重试/ })).toHaveCount(
    0,
  );
  await dialog.getByRole("button", { name: "刷新状态", exact: true }).click();
  expect((await control(page, f, "/pump")).status()).toBe(200);
  expect((await state(page, f)).send_calls).toBe(2);
  await page.keyboard.press("Escape");
  await page.reload();
  await page.getByRole("button", { name: "断开", exact: true }).click();
  dialog = page.getByRole("dialog");
  await expect(dialog.getByLabel("仅本地断开，保留远端 Webhook")).toBeChecked();
  await dialog.getByRole("button", { name: "断开", exact: true }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(page.getByText("本地已断开", { exact: true })).toBeVisible();
  expect((await control(page, f, "/event", { kind: "private" })).status()).toBe(
    404,
  );
  proof = await state(page, f);
  expect(proof.identities).toBe(0);
  expect(proof.groups).toBe(0);
  expect(proof.delete_calls).toBe(0);
  expect(proof.send_calls).toBe(2);
  // Check for accidental persistence without exposing the private values.
  const persisted = await page.evaluate(() =>
    JSON.stringify({
      local: { ...localStorage },
      session: { ...sessionStorage },
    }),
  );
  expect(persisted.includes(f.token) || persisted.includes(code)).toBe(false);
});
