import { readFileSync } from "node:fs";
import { expect, test, type Page } from "@playwright/test";
const file = process.env.OPENFOX_I19_BROWSER_FILE;
test.skip(!file, "requires isolated WeCom/PG fixture");
const f = file ? JSON.parse(readFileSync(file, "utf8")) : {};
async function login(page: Page, email: string) {
  const response = await page.request.post("/api/v1/auth/login", {
    data: { email, password: f.password, client_kind: "web" },
  });
  expect(response.status()).toBe(201);
  await page.goto(`/o/${f.organization_id}/settings/integrations/wecom`);
  await page.evaluate(
    (id) =>
      localStorage.setItem(
        `openfox:onboarding:${id}`,
        JSON.stringify({ step: "done" }),
      ),
    f.organization_id,
  );
}
test("I19 manager consents, refresh preserves state, reopen creates fresh conversation and member cannot manage", async ({
  page,
  browser,
}) => {
  await login(page, f.email);
  await page.getByRole("button", { name: "群聊授权", exact: true }).click();
  let dialog = page.getByRole("dialog");
  await expect(dialog.getByText(f.group_id, { exact: true })).toBeVisible();
  await dialog.getByRole("button", { name: "开启", exact: true }).click();
  await expect(
    dialog.getByRole("button", { name: "同意并开启" }),
  ).toBeDisabled();
  await dialog.getByRole("checkbox").check();
  await dialog.getByRole("button", { name: "同意并开启" }).click();
  await expect(dialog.getByText("已开启", { exact: true })).toBeVisible();
  const api = `/api/v1/organizations/${f.organization_id}/integrations/wecom/bots/${f.bot_id}/groups`;
  const first = (await (await page.request.get(api)).json()).groups[0];
  expect(first.enabled).toBe(true);
  await page.reload();
  await page.getByRole("button", { name: "群聊授权", exact: true }).click();
  dialog = page.getByRole("dialog");
  await expect(dialog.getByText("已开启", { exact: true })).toBeVisible();
  await dialog
    .getByRole("button", { name: "关闭", exact: true })
    .filter({ hasText: /^关闭$/ })
    .click();
  await expect(dialog.getByText("已关闭", { exact: true })).toBeVisible();
  await dialog.getByRole("button", { name: "开启", exact: true }).click();
  await expect(dialog.getByRole("checkbox")).not.toBeChecked();
  await dialog.getByRole("checkbox").check();
  await dialog.getByRole("button", { name: "同意并开启" }).click();
  await expect(dialog.getByText("已开启", { exact: true })).toBeVisible();
  const latest = (await (await page.request.get(api)).json()).groups[0];
  expect(latest.version).toBe(3);
  expect(latest.conversation_id).not.toBe(first.conversation_id);
  const memberContext = await browser.newContext({ locale: "zh-CN" });
  try {
    const member = await memberContext.newPage();
    await login(member, f.member_email);
    await expect(
      member.getByText("OpenFox 任务助手 - Fox", { exact: true }),
    ).toBeVisible();
    await expect(
      member.getByRole("button", { name: "群聊授权", exact: true }),
    ).toHaveCount(0);
    expect((await member.request.get(api)).status()).toBe(403);
  } finally {
    await memberContext.close();
  }
});
