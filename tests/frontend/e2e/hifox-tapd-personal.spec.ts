import { readFileSync } from "node:fs";
import { expect, test, type Page } from "@playwright/test";

const path = process.env.OPENFOX_TAPD_BROWSER_FILE;
test.skip(!path, "requires the isolated TAPD TLS/PG harness");
const fixture = path ? JSON.parse(readFileSync(path, "utf8")) : {};
const base = `/api/v1/organizations/${fixture.organization_id}`;

async function request(
  page: Page,
  url: string,
  method = "GET",
  data?: unknown,
) {
  const csrf = (await page.context().cookies()).find(
    (c) => c.name === "openfox_csrf",
  )?.value;
  const response = await page.request.fetch(url, {
    method,
    data,
    headers: {
      ...(csrf ? { "X-CSRF-Token": csrf } : {}),
      "Idempotency-Key": crypto.randomUUID(),
    },
  });
  expect(
    response.ok(),
    `${method} ${url}: ${response.status()} ${await response.text()}`,
  ).toBeTruthy();
  return response.status() === 204 ? null : response.json();
}
async function login(page: Page, email: string) {
  await request(page, "/api/v1/auth/login", "POST", {
    email,
    password: fixture.password,
    client_kind: "web",
  });
}

test("P01 real identity UI, scoped verified mapping, import worker, and unlink", async ({
  page,
  browser,
}) => {
  test.setTimeout(120_000);
  await login(page, fixture.member_email);
  await page.goto(`/o/${fixture.organization_id}/settings/account-binding`);
  await page.evaluate(
    (id) =>
      localStorage.setItem(
        `openfox:onboarding:${id}`,
        JSON.stringify({ step: "done" }),
      ),
    fixture.organization_id,
  );
  const row = page.getByTestId("binding-tapd");
  await row.getByRole("button", { name: "绑定", exact: true }).click();
  let dialog = page.getByRole("dialog", { name: "验证 TAPD 个人身份" });
  await dialog.getByLabel("TAPD 项目 ID", { exact: true }).fill("101");
  await dialog
    .getByLabel("用户态 OAuth Access Token", { exact: true })
    .fill("invalid");
  await dialog.getByRole("button", { name: "验证并添加项目" }).click();
  await expect(dialog.getByRole("alert")).toContainText("Token 无效");
  await expect(dialog.getByLabel("TAPD 项目 ID", { exact: true })).toHaveValue(
    "101",
  );
  await dialog
    .getByLabel("用户态 OAuth Access Token", { exact: true })
    .fill("member");
  await dialog.getByRole("button", { name: "验证并添加项目" }).click();
  await expect(dialog.getByText("已验证昵称：member.nick")).toBeVisible();
  await expect(
    dialog.getByLabel("用户态 OAuth Access Token", { exact: true }),
  ).toHaveValue("");
  await dialog.getByLabel("TAPD 项目 ID", { exact: true }).fill("102");
  await dialog
    .getByLabel("用户态 OAuth Access Token", { exact: true })
    .fill("member");
  await dialog.getByRole("button", { name: "验证并添加项目" }).click();
  await expect(
    dialog.getByRole("button", { name: "移除项目 102" }),
  ).toBeVisible();
  await dialog.getByRole("button", { name: "移除项目 102" }).click();
  await expect(
    dialog.getByRole("button", { name: "移除项目 102" }),
  ).toBeHidden();
  await dialog.getByRole("button", { name: "取消", exact: true }).click();
  await page.reload();
  await expect(row.getByText(/member.nick/)).toBeVisible();

  const ownerContext = await browser.newContext({ locale: "zh-CN" });
  const owner = await ownerContext.newPage();
  await login(owner, fixture.owner_email);
  await owner.goto(
    `/o/${fixture.organization_id}/settings/import-export?source=tapd&method=api&space=${fixture.space_id}`,
  );
  await owner.getByRole("button", { name: "继续", exact: true }).click();
  await owner.getByLabel("实例", { exact: true }).click();
  await owner
    .getByRole("option", { name: "Fixture TAPD Basic", exact: true })
    .click();
  await owner.getByLabel("项目", { exact: true }).click();
  await owner
    .getByRole("option", { name: "Fixture TAPD project", exact: true })
    .click();
  await owner.getByRole("button", { name: "下一步", exact: true }).click();
  for (const heading of ["检查", "选择任务", "映射状态"]) {
    await expect(
      owner.getByRole("heading", { name: heading, exact: true }),
    ).toBeVisible();
    await owner.getByRole("button", { name: "下一步", exact: true }).click();
  }
  await expect(
    owner.getByRole("heading", { name: "映射用户", exact: true }),
  ).toBeVisible();
  await expect(
    owner.getByText("已验证", { exact: false }).first(),
  ).toBeVisible();
  for (let i = 0; i < 2; i++)
    await owner.getByRole("button", { name: "下一步", exact: true }).click();
  const started = owner.waitForResponse(
    (r) => r.request().method() === "POST" && r.url().endsWith("/start"),
  );
  await owner.getByRole("button", { name: "开始导入", exact: true }).click();
  const response = await started;
  expect(response.ok()).toBeTruthy();
  const job = await response.json();
  await expect
    .poll(
      async () => (await request(owner, `${base}/imports/${job.id}`)).status,
    )
    .toBe("SUCCEEDED");
  const imported = await request(owner, `${base}/imports/${job.id}`);
  expect(
    imported.config.users.find(
      (u: { source_id: string }) => u.source_id === "member.nick",
    ).verified_binding_id,
  ).toBeTruthy();
  const tasks = (
    await request(owner, `${base}/spaces/${fixture.space_id}/tasks`)
  ).tasks;
  expect(tasks).toHaveLength(2);
  expect(
    tasks.find((t: { title: string }) => t.title === "Fixture task 8000")
      .assignee_user_id,
  ).toBe(fixture.member_id);
  expect(
    tasks.find((t: { title: string }) => t.title === "Fixture task 8001")
      .external_assignee,
  ).toBeTruthy();
  await row.getByRole("button", { name: "已连接" }).click();
  await page.getByRole("menuitem", { name: "更新", exact: true }).click();
  dialog = page.getByRole("dialog", { name: "验证 TAPD 个人身份" });
  await expect(
    dialog.getByLabel("用户态 OAuth Access Token", { exact: true }),
  ).toHaveValue("");
  await dialog.getByRole("button", { name: "移除项目 101" }).click();
  await expect(dialog.getByText("已验证昵称：member.nick")).toBeHidden();
  await page.reload();
  await expect(
    row.getByRole("button", { name: "绑定", exact: true }),
  ).toBeVisible();
  expect(
    (
      await request(owner, `${base}/spaces/${fixture.space_id}/tasks`)
    ).tasks.find((t: { title: string }) => t.title === "Fixture task 8000")
      .assignee_user_id,
  ).toBe(fixture.member_id);
  await page.screenshot({
    path: "/private/tmp/openfox-p01-tests-egvhsvwk/account-unlinked.png",
    fullPage: true,
  });
  await ownerContext.close();
});
