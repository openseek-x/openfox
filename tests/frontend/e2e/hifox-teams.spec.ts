import { test, expect, type Page } from "@playwright/test";
import fs from "node:fs";
// Actual sessions, CSRF, PostgreSQL, worker, RS256 and owned TLS. No route mocks.
const file = process.env.OPENFOX_I16_BROWSER_FILE;
test.skip(!file, "requires exclusive Teams HTTP/PG harness");
type Fixture = {
  web_url: string;
  api_url: string;
  fixture_url: string;
  organization_id: string;
  tenant_id: string;
  app_id: string;
  email: string;
  password: string;
  secret: string;
};
function fixture(): Fixture {
  const f = JSON.parse(fs.readFileSync(file!, "utf8")) as Fixture;
  for (const raw of [f.web_url, f.api_url, f.fixture_url]) {
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
test("I16 real identity, explicit group epoch and original authenticated reply", async ({
  page,
}) => {
  test.setTimeout(180000);
  const f = fixture();
  expect(
    (
      await page.request.post(new URL("/api/v1/auth/login", f.web_url).href, {
        data: { email: f.email, password: f.password, client_kind: "web" },
      })
    ).status(),
  ).toBe(201);
  await page.goto(
    new URL(`/o/${f.organization_id}/settings/integrations/teams`, f.web_url)
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
  await page
    .getByRole("button", { name: "连接 Teams Bot", exact: true })
    .click();
  let dialog = page.getByRole("dialog");
  await dialog.getByRole("button", { name: "Agent", exact: true }).click();
  await page
    .getByRole("option", { name: "Teams browser Agent", exact: true })
    .click();
  for (const [label, value] of [
    ["应用名称", "Controlled Teams"],
    ["Tenant ID", f.tenant_id],
    ["Microsoft App ID", f.app_id],
    ["Client Secret", f.secret],
    ["开发者名称", "Controlled developer"],
    ["开发者网站 HTTPS URL", "https://example.test"],
    ["隐私政策 HTTPS URL", "https://example.test/privacy"],
    ["使用条款 HTTPS URL", "https://example.test/terms"],
  ] as const)
    await dialog.getByLabel(label, { exact: true }).fill(value);
  const saved = page.waitForResponse(
    (r) =>
      r.request().method() === "POST" &&
      new URL(r.url()).pathname.endsWith("/integrations/teams/bots"),
  );
  await dialog.getByRole("button", { name: "核验并保存", exact: true }).click();
  const response = await saved;
  expect(response.status()).toBe(200);
  const bot = await response.json();
  expect(JSON.stringify(bot)).not.toContain(f.secret);
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(page.getByText(/凭据校验成功不代表应用已安装/)).toBeVisible();
  expect((await state(page, f)).send_calls).toBe(0);
  const zip = page.waitForEvent("download");
  await page
    .getByRole("button", { name: "下载 Teams 应用包", exact: true })
    .click();
  expect((await zip).suggestedFilename()).toBe("openfox-teams.zip");
  expect(
    (
      await control(page, f, "/event", {
        bot_id: bot.id,
        kind: "private",
        invalid_auth: true,
      })
    ).status(),
  ).toBe(401);
  expect((await state(page, f)).inputs).toBe(0);
  await page.getByRole("button", { name: "绑定本人", exact: true }).click();
  dialog = page.getByRole("dialog");
  await dialog
    .getByRole("button", { name: "生成一次性绑定指令", exact: true })
    .click();
  const command = await dialog.locator("code").textContent();
  expect(command).toMatch(/^\/openfox bind [A-Za-z0-9_-]{32}$/);
  const binding = await control(page, f, "/event", {
    bot_id: bot.id,
    kind: "bind",
    code: command,
  });
  expect(binding.status()).toBe(200);
  expect((await binding.json()).result).toBe("identity_bound");
  expect((await state(page, f)).identities).toBe(1);
  await dialog.getByRole("button", { name: "刷新状态", exact: true }).click();
  await expect(dialog.getByText("身份有效", { exact: true })).toBeVisible();
  await expect(dialog.locator("code")).toHaveCount(0);
  await dialog
    .getByRole("button", { name: "关闭", exact: true })
    .last()
    .click();
  expect((await control(page, f, "/pump")).status()).toBe(200);
  expect((await state(page, f)).send_calls).toBe(1);
  expect(
    (
      await control(page, f, "/event", { bot_id: bot.id, kind: "private" })
    ).status(),
  ).toBe(200);
  expect((await control(page, f, "/complete")).status()).toBe(200);
  let proof = await state(page, f);
  expect(proof.inputs).toBe(1);
  expect(proof.send_calls).toBe(2);
  expect(proof.posts[1].path).toContain(
    "/conversations/19:owned-personal;different-from-sender/activities/opaque/private;",
  );
  expect(proof.posts[1].authorization_ok).toBe(true);
  const discover = await control(page, f, "/event", {
    bot_id: bot.id,
    kind: "group",
  });
  expect(discover.status()).toBe(200);
  expect((await discover.json()).result).toBe("scope_not_enabled");
  expect((await state(page, f)).inputs).toBe(1);
  await page
    .getByRole("button", { name: "群授权与送达记录", exact: true })
    .click();
  dialog = page.getByRole("dialog");
  await expect(
    dialog.getByText("19:owned-group;opaque", { exact: true }),
  ).toBeVisible();
  await expect(
    dialog.getByRole("button", { name: "开启回复", exact: true }),
  ).toBeDisabled();
  await dialog
    .getByRole("checkbox", { name: "我同意在此群或频道公开回复" })
    .check();
  await dialog.getByRole("button", { name: "开启回复", exact: true }).click();
  await expect(dialog.getByText("已开启", { exact: true })).toBeVisible();
  await dialog
    .getByRole("button", { name: "关闭", exact: true })
    .last()
    .click();
  expect(
    (
      await control(page, f, "/event", { bot_id: bot.id, kind: "group" })
    ).status(),
  ).toBe(200);
  expect((await control(page, f, "/complete")).status()).toBe(200);
  proof = await state(page, f);
  expect(proof.inputs).toBe(2);
  expect(proof.send_calls).toBe(3);
  expect(proof.posts[2].path).toContain(
    "/conversations/19:owned-group;opaque/activities/opaque/group;",
  );
  expect(
    (await control(page, f, "/unknown-next", { unknown: true })).status(),
  ).toBe(200);
  expect(
    (
      await control(page, f, "/event", { bot_id: bot.id, kind: "private" })
    ).status(),
  ).toBe(200);
  expect((await control(page, f, "/complete")).status()).toBe(200);
  proof = await state(page, f);
  expect(proof.send_calls).toBe(4);
  expect(proof.replies_unknown).toBe(1);
  expect((await control(page, f, "/pump")).status()).toBe(200);
  expect((await state(page, f)).send_calls).toBe(4);
  await page.reload();
  await page
    .getByRole("button", { name: "群授权与送达记录", exact: true })
    .click();
  dialog = page.getByRole("dialog");
  await expect(dialog.getByText(/发送结果未知，已停止自动重试/)).toBeVisible();
  await expect(dialog.getByRole("button", { name: /重试|重发/ })).toHaveCount(
    0,
  );
  await dialog.getByRole("button", { name: "关闭回复", exact: true }).click();
  await expect(dialog.getByText("已关闭", { exact: true })).toBeVisible();
  await dialog
    .getByRole("button", { name: "关闭", exact: true })
    .last()
    .click();
  expect(
    (
      await control(page, f, "/event", { bot_id: bot.id, kind: "group" })
    ).status(),
  ).toBe(200);
  expect((await control(page, f, "/complete")).status()).toBe(200);
  expect((await state(page, f)).send_calls).toBe(4);
  await page.getByRole("button", { name: "移除本地连接", exact: true }).click();
  dialog = page.getByRole("dialog");
  await dialog.getByRole("checkbox", { name: "我确认移除本地连接" }).check();
  await dialog
    .getByRole("button", { name: "移除本地连接", exact: true })
    .click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  expect(
    (
      await control(page, f, "/event", { bot_id: bot.id, kind: "private" })
    ).status(),
  ).toBe(404);
  proof = await state(page, f);
  expect(proof.identities).toBe(0);
  expect(proof.replies_unknown).toBe(1);
  expect(proof.send_calls).toBe(4);
});
