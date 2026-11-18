import { test, expect } from "@playwright/test";
import fs from "node:fs";
const path = process.env.OPENFOX_YUNXIAO_BROWSER_FILE;
test.skip(!path, "requires isolated Yunxiao/PG fixture");
test("I04 organization PAT, identity-only binding and inbound import persist through real HTTP", async ({
  page,
}) => {
  const f = JSON.parse(fs.readFileSync(path!, "utf8")) as {
    organization_id: string;
    space_id: string;
    email: string;
    password: string;
  };
  const login = await page.request.post("/api/v1/auth/login", {
    data: { email: f.email, password: f.password, client_kind: "web" },
  });
  expect(login.status()).toBe(201);
  await page.goto(`/o/${f.organization_id}/settings/integrations/yunxiao`);
  await page.evaluate(
    (org) =>
      localStorage.setItem(
        `openfox:onboarding:${org}`,
        JSON.stringify({ step: "done" }),
      ),
    f.organization_id,
  );
  await page.getByRole("button", { name: "绑定 云效", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("访问令牌").fill("synthetic-owner");
  const inputs = dialog.getByRole("textbox");
  await inputs.last().fill("cloud-org");
  const binding = page.waitForResponse(
    (r) =>
      r.url().endsWith("/yunxiao/instance-token") &&
      r.request().method() === "POST",
  );
  await dialog.getByRole("button", { name: "绑定 云效", exact: true }).click();
  const bound = await binding;
  expect(bound.ok(), await bound.text()).toBeTruthy();
  await expect(dialog).toHaveCount(0);
  await expect(
    page.getByText("cloud-org", { exact: true }).first(),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "验证我的云效身份", exact: true })
    .click();
  await dialog.getByLabel("实例", { exact: true }).click();
  await page.getByRole("option", { name: "cloud-org", exact: true }).click();
  await dialog.getByLabel("访问令牌").fill("synthetic-owner");
  await dialog.getByRole("button", { name: "保存", exact: true }).click();
  await expect(dialog).toHaveCount(0);
  await expect(
    page.getByText("Cloud identity", { exact: false }).first(),
  ).toBeVisible();
  await page.goto(
    `/o/${f.organization_id}/settings/import-export?source=yunxiao&space=${f.space_id}&sync=1`,
  );
  await page.getByRole("button", { name: "继续", exact: true }).click();
  await page.getByLabel("实例", { exact: true }).click();
  await page.getByRole("option", { name: "cloud-org", exact: true }).click();
  await page.getByLabel("项目", { exact: true }).click();
  await page
    .getByRole("option", { name: "Cloud project", exact: true })
    .click();
  await page.getByRole("button", { name: /下一步/ }).click();
  await expect(
    page.getByRole("heading", { name: "检查", exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "下一步", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "选择任务", exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "下一步", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "映射状态", exact: true }),
  ).toBeVisible();
  await page.getByLabel("类型「Custom type」映射", { exact: true }).click();
  await page.getByRole("option", { name: "空间默认类型", exact: true }).click();
  await page
    .getByLabel("「Custom type」的来源优先级字段", { exact: true })
    .click();
  await page.getByRole("option", { name: "Importance", exact: true }).click();
  await page.getByLabel("优先级「Must」映射", { exact: true }).click();
  await page.getByRole("option", { name: "高", exact: true }).click();
  await page.getByRole("button", { name: /下一步/ }).click();
  await expect(
    page.getByRole("heading", { name: "映射用户", exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: /下一步/ }).click();
  await expect(
    page.getByRole("heading", { name: "导入选项", exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: /下一步/ }).click();
  const checks = page.getByRole("checkbox");
  for (const box of await checks.all()) {
    if ((await box.isVisible()) && !(await box.isChecked())) await box.check();
  }
  await page.getByRole("button", { name: /开始导入/, exact: true }).click();
  await expect(page.getByText(/导入完成/).first()).toBeVisible({
    timeout: 20000,
  });
  await page.reload();
  const csrf = (await page.context().cookies()).find(
    (c) => c.name === "openfox_csrf",
  )?.value;
  const response = await page.request.get(
    `/api/v1/organizations/${f.organization_id}/spaces/${f.space_id}/tasks`,
    { headers: { "X-CSRF-Token": csrf ?? "" } },
  );
  expect(response.ok()).toBeTruthy();
  expect(JSON.stringify(await response.json())).toContain("Remote 0");
});
