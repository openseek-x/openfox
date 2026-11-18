import { readFileSync, writeFileSync } from "node:fs";
import { expect, test, type Page } from "@playwright/test";
const fixturePath = process.env.OPENFOX_ASANA_BROWSER_FILE;
test.skip(
  !fixturePath,
  "requires the isolated Asana TLS/PG harness and CONNECT proxy",
);
const fixture = fixturePath
  ? JSON.parse(readFileSync(fixturePath, "utf8"))
  : {};
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
  const r = await page.request.fetch(url, {
    method,
    data,
    headers: {
      ...(csrf ? { "X-CSRF-Token": csrf } : {}),
      "Idempotency-Key": crypto.randomUUID(),
    },
  });
  expect(
    r.ok(),
    `${method} ${url}: ${r.status()} ${await r.text()}`,
  ).toBeTruthy();
  return r.status() === 204 ? null : r.json();
}
async function login(page: Page, email: string) {
  await request(page, "/api/v1/auth/login", "POST", {
    email,
    password: fixture.password,
    client_kind: "web",
  });
}
test("Asana real OAuth, personal proof, project crawl and import", async ({
  page,
  browser,
}, testInfo) => {
  test.setTimeout(120000);
  await login(page, fixture.owner_email);
  await page.goto(`/o/${fixture.organization_id}/settings/integrations/asana`);
  const popupEvent = page.waitForEvent("popup");
  await page
    .getByRole("button", { name: "连接 Asana workspace", exact: true })
    .click();
  const popup = await popupEvent;
  await expect(
    popup.getByRole("heading", { name: "确认 Asana 组织连接" }),
  ).toBeVisible();
  await popup.getByLabel("Asana workspace", { exact: true }).click();
  await popup.getByRole("option", { name: "Workspace A", exact: true }).click();
  await popup.getByRole("checkbox").check();
  await popup.getByRole("button", { name: "确认共享此 workspace" }).click();
  await expect(
    page.getByText("委托授权者：Fixture Person（100）"),
  ).toBeVisible();
  const memberContext = await browser.newContext({
    baseURL: "http://127.0.0.1:4197",
    locale: "zh-CN",
    ignoreHTTPSErrors: true,
  });
  const member = await memberContext.newPage();
  await login(member, fixture.member_email);
  await member.goto(`/o/${fixture.organization_id}/settings/account-binding`);
  const row = member.getByTestId("binding-asana");
  const identityEvent = member.waitForEvent("popup");
  await row.getByRole("button", { name: "绑定", exact: true }).click();
  const identity = await identityEvent;
  await expect(
    identity.getByRole("heading", { name: "Asana 个人身份" }),
  ).toBeVisible();
  await identity.getByRole("button", { name: "确认绑定此身份" }).click();
  await expect(row.getByRole("button", { name: "已连接" })).toBeVisible();
  await page.goto(
    `/o/${fixture.organization_id}/settings/import-export?source=asana&method=api&space=${fixture.space_id}`,
  );
  await page.getByRole("button", { name: "继续", exact: true }).click();
  await page.getByLabel("Asana workspace", { exact: true }).click();
  await page.getByRole("option", { name: "Workspace A", exact: true }).click();
  await page.getByRole("button", { name: "加载更多项目", exact: true }).click();
  await expect(
    page.getByText("已加载 2 个可访问项目", { exact: true }),
  ).toBeVisible();
  await page.getByLabel("项目", { exact: true }).click();
  await page
    .getByRole("option", { name: "Fixture Project", exact: true })
    .click();
  await page.getByRole("button", { name: "下一步", exact: true }).click();
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
  await page.getByLabel("类型「default_task」映射", { exact: true }).click();
  await page.getByRole("option", { name: "空间默认类型", exact: true }).click();
  await page.getByRole("button", { name: "下一步", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "映射用户", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText("已验证", { exact: false }).first(),
  ).toBeVisible();
  await page.getByRole("button", { name: "下一步", exact: true }).click();
  await page
    .getByRole("switch", { name: "导入后从 Asana 持续同步", exact: true })
    .check();
  await page.getByRole("button", { name: "下一步", exact: true }).click();
  const started = page.waitForResponse(
    (r) => r.request().method() === "POST" && r.url().endsWith("/start"),
  );
  await page.getByRole("button", { name: "开始导入", exact: true }).click();
  const response = await started;
  expect(response.ok()).toBeTruthy();
  const job = await response.json();
  await expect
    .poll(async () => (await request(page, `${base}/imports/${job.id}`)).status)
    .toBe("SUCCEEDED");
  const tasks = (
    await request(page, `${base}/spaces/${fixture.space_id}/tasks`)
  ).tasks;
  expect(tasks).toHaveLength(2);
  expect(
    tasks.every((t: any) => t.assignee_user_id === fixture.member_id),
  ).toBeTruthy();
  expect(tasks.find((t: any) => t.title === "Task 402").parent_task_id).toBe(
    tasks.find((t: any) => t.title === "Task 401").id,
  );
  await expect(
    page.getByRole("heading", { name: "导入完成", exact: true }),
  ).toBeVisible({ timeout: 15000 });
  await expect(page.getByText(/无法识别，已设为无优先级/)).toHaveCount(0);
  await page.screenshot({
    path: testInfo.outputPath("import-success.png"),
    fullPage: true,
  });
  await row.getByRole("button", { name: "已连接" }).click();
  await member.getByRole("menuitem", { name: /断开/ }).click();
  await member
    .getByRole("dialog")
    .getByRole("button", { name: /断开/ })
    .click();
  await expect(
    row.getByRole("button", { name: "绑定", exact: true }),
  ).toBeVisible();
  writeFileSync(fixturePath + ".source-change", "controlled source change");
  await page.goto(`/o/${fixture.organization_id}/settings/integrations/asana`);
  await expect(page.getByLabel("同步方向", { exact: true })).toBeDisabled();
  await expect(page.getByLabel("同步方向", { exact: true })).toContainText(
    "Asana → OpenFox",
  );
  await page.getByRole("button", { name: "立即检测", exact: true }).click();
  await expect
    .poll(
      async () =>
        (await request(page, `${base}/imports/${job.id}`)).sync?.error_code,
      { timeout: 15000 },
    )
    .toBe("TYPE_MAPPING");
  await page.reload();
  await page.getByRole("button", { name: "类型映射", exact: true }).click();
  const types = page.getByRole("dialog", { name: "类型映射", exact: true });
  await types.getByLabel("类型「approval」映射", { exact: true }).click();
  await page.getByRole("option", { name: "空间默认类型", exact: true }).click();
  await types.getByRole("button", { name: "保存", exact: true }).click();
  await expect
    .poll(
      async () =>
        (await request(page, `${base}/spaces/${fixture.space_id}/tasks`)).tasks
          .length,
      { timeout: 15000 },
    )
    .toBe(3);
  const updated = (
    await request(page, `${base}/spaces/${fixture.space_id}/tasks`)
  ).tasks;
  expect(
    updated.find((t: any) => t.title === "Task 403").external_assignee,
  ).toBeTruthy();
  expect(
    updated
      .filter((t: any) => t.title !== "Task 403")
      .every((t: any) => t.assignee_user_id === fixture.member_id),
  ).toBeTruthy();
  await expect(
    page.getByRole("button", { name: "立即检测", exact: true }),
  ).toBeEnabled({ timeout: 15000 });
  await page.screenshot({
    path: testInfo.outputPath("sync-recovered.png"),
    fullPage: true,
  });
  await member.screenshot({
    path: testInfo.outputPath("identity-success.png"),
    fullPage: true,
  });
  await memberContext.close();
});
