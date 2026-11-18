import { test, expect } from "@playwright/test";
import fs from "node:fs";
const fixture = process.env.OPENFOX_GONGFENG_BROWSER_FILE;
test.skip(!fixture, "requires isolated Gongfeng/PG/TLS fixture");
test("I10 bind, choose repository, activate Hook and import through real HTTP", async ({
  page,
}) => {
  const f = JSON.parse(fs.readFileSync(fixture!, "utf8"));
  const login = await page.request.post("/api/v1/auth/login", {
    data: { email: f.email, password: f.password, client_kind: "web" },
  });
  expect(login.status()).toBe(201);
  await page.goto(`/o/${f.organization_id}/settings/integrations/gongfeng`);
  await page.evaluate(
    (org) =>
      localStorage.setItem(
        `openfox:onboarding:${org}`,
        JSON.stringify({ step: "done" }),
      ),
    f.organization_id,
  );
  await page.getByRole("button", { name: "绑定腾讯工蜂", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("个人访问令牌（PAT）").fill("synthetic-owner");
  await dialog.getByRole("button", { name: "保存", exact: true }).click();
  await expect(dialog).toHaveCount(0);
  await page.getByRole("button", { name: "添加仓库同步", exact: true }).click();
  await dialog
    .getByRole("button", { name: "读取可访问仓库", exact: true })
    .click();
  await dialog.getByLabel("选择仓库", { exact: true }).click();
  await page
    .getByRole("option", { name: "fixture/project", exact: true })
    .click();
  await dialog.getByLabel("目标空间", { exact: true }).click();
  await page.getByRole("option").first().click();
  await dialog.getByLabel("导入现有 Issue", { exact: true }).check();
  await dialog
    .getByLabel("我确认创建并激活远端 Webhook", { exact: true })
    .check();
  await dialog.getByRole("button", { name: "创建同步", exact: true }).click();
  await expect(dialog).toHaveCount(0);
  await expect
    .poll(
      async () => {
        await page
          .getByRole("button", { name: "刷新状态", exact: true })
          .click();
        return page.getByText("扫描完成", { exact: true }).count();
      },
      { timeout: 30000 },
    )
    .toBeGreaterThan(0);
  await page.reload();
  await expect(
    page.getByText("fixture/project", { exact: true }).first(),
  ).toBeVisible();
  const response = await page.request.get(
    `/api/v1/organizations/${f.organization_id}/spaces/${f.space_id}/tasks`,
  );
  expect(response.ok(), await response.text()).toBeTruthy();
  expect(JSON.stringify(await response.json())).toContain(
    "Controlled Gongfeng issue",
  );
  await page.screenshot({
    path: process.env.OPENFOX_GONGFENG_SCREENSHOT,
    fullPage: true,
  });
});
