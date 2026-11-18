import { readFileSync } from "node:fs";
import { expect, test, type Page } from "@playwright/test";

const fixturePath = process.env.OPENFOX_SITE_BROWSER_FILE;
test.skip(!fixturePath, "requires the isolated TLS site browser harness");
// The harness writes only disposable fixture accounts, never user credentials.
type SiteFixture = {
  organization_id: string;
  email: string;
  password: string;
  jira_url: string;
  jira_email: string;
  jira_token: string;
  zentao_url: string;
  zentao_account: string;
  zentao_password: string;
};
const fixture: SiteFixture = fixturePath
  ? JSON.parse(readFileSync(fixturePath, "utf8"))
  : ({} as SiteFixture);
const base = `/api/v1/organizations/${fixture.organization_id}`;

async function request(
  page: Page,
  path: string,
  method = "GET",
  data?: unknown,
) {
  const csrf = (await page.context().cookies()).find(
    (c) => c.name === "openfox_csrf",
  )?.value;
  const response = await page.request.fetch(path, {
    method,
    data,
    headers: {
      ...(csrf ? { "X-CSRF-Token": csrf } : {}),
      "Idempotency-Key": crypto.randomUUID(),
    },
  });
  expect(response.ok(), `${method} ${path}: ${response.status()}`).toBeTruthy();
  return response.status() === 204 ? null : response.json();
}

test("I01 I02 standalone sites persist and both import wizards reuse the selected connection", async ({
  page,
}) => {
  test.setTimeout(120_000);
  await request(page, "/api/v1/auth/login", "POST", {
    email: fixture.email,
    password: fixture.password,
    client_kind: "web",
  });
  await page.goto(`/o/${fixture.organization_id}/settings/integrations`);
  await page.evaluate(
    (id) =>
      localStorage.setItem(
        `openfox:onboarding:${id}`,
        JSON.stringify({ step: "done" }),
      ),
    fixture.organization_id,
  );
  for (const provider of ["jira", "zentao"] as const) {
    const name = provider === "jira" ? "Jira" : "ZenTao";
    const site = fixture[`${provider}_url`];
    const account =
      provider === "jira" ? fixture.jira_email : fixture.zentao_account;
    const secret =
      provider === "jira" ? fixture.jira_token : fixture.zentao_password;
    const secretLabel = provider === "jira" ? "令牌" : "密码";
    await page.goto(
      `/o/${fixture.organization_id}/settings/integrations/${provider}`,
    );
    await page.getByRole("button", { name: "绑定", exact: true }).click();
    let dialog = page.getByRole("dialog", {
      name: `连接 ${name} 组织站点`,
      exact: true,
    });
    await dialog.getByLabel("站点地址", { exact: true }).fill(site);
    await dialog.getByLabel("账号或邮箱", { exact: true }).fill(account);
    await dialog.getByLabel(secretLabel, { exact: true }).fill(secret);
    const saved = page.waitForResponse(
      (r) =>
        r.request().method() === "POST" &&
        r.url().endsWith(`/integrations/${provider}/site`),
    );
    await dialog.getByRole("button", { name: "保存", exact: true }).click();
    const bound = await saved;
    expect(bound.ok()).toBeTruthy();
    let connection = await bound.json();
    expect(JSON.stringify(connection)).not.toContain(secret);
    await expect(dialog).toBeHidden();
    await page.reload();
    await expect(
      page.getByText(connection.display_name, { exact: true }).first(),
    ).toBeVisible();
    const detail = await request(page, `${base}/integrations/${provider}`);
    expect(detail.connections).toHaveLength(1);

    // Updating is explicit, uses CAS, and does not create a second connection.
    await page
      .getByRole("button", {
        name: `${connection.display_name} 的更多操作`,
        exact: true,
      })
      .click();
    await page
      .getByRole("menuitem", { name: "更新连接凭据", exact: true })
      .click();
    dialog = page.getByRole("dialog", { name: "更新连接凭据", exact: true });
    await expect(dialog.getByLabel(secretLabel, { exact: true })).toHaveValue(
      "",
    );
    await dialog.getByLabel("账号或邮箱", { exact: true }).fill(account);
    await dialog.getByLabel(secretLabel, { exact: true }).fill(secret);
    const refreshed = page.waitForResponse(
      (r) =>
        r.request().method() === "POST" &&
        r.url().endsWith(`/integrations/${provider}/site`),
    );
    await dialog.getByRole("button", { name: "保存", exact: true }).click();
    const refreshedResponse = await refreshed;
    expect(refreshedResponse.ok()).toBeTruthy();
    const updatedDetail = await request(
      page,
      `${base}/integrations/${provider}`,
    );
    expect(updatedDetail.connections).toHaveLength(1);
    const updated = updatedDetail.connections[0];
    expect(updated.id).toBe(connection.id);
    expect(updated.version).toBe(connection.version + 1);
    connection = updated;

    const target = await request(page, `${base}/spaces`, "POST", {
      name: `${name} imported browser`,
      prefix: provider === "jira" ? "JBR" : "ZBR",
      visibility: "SHARED",
    });
    await page.goto(
      `/o/${fixture.organization_id}/settings/import-export?source=${provider}&method=api&space=${target.id}`,
    );
    await page
      .getByRole("button", {
        name: provider === "jira" ? "下一步" : "继续",
        exact: true,
      })
      .click();
    await page.getByLabel("选择组织站点连接", { exact: true }).click();
    await page
      .getByRole("option", {
        name: `${connection.display_name} · ${connection.id.slice(0, 8)}`,
        exact: true,
      })
      .click();
    await expect(page.getByLabel("站点地址", { exact: true })).toHaveCount(0);
    if (provider === "jira") {
      await expect(page.getByLabel("API 令牌", { exact: true })).toHaveCount(0);
      await page.getByLabel("项目 Key", { exact: true }).fill("PRJ");
      await page
        .getByRole("button", { name: "验证并获取", exact: true })
        .click();
    } else {
      await expect(page.getByLabel("禅道密码", { exact: true })).toHaveCount(0);
      await page
        .getByRole("button", { name: "获取产品与项目", exact: true })
        .click();
      await page.getByLabel("项目", { exact: true }).click();
      await page.getByRole("option", { name: /Product/ }).click();
      await page.getByRole("button", { name: "下一步", exact: true }).click();
    }
    const headings = ["检查", "选择任务", "映射状态", "映射用户", "导入选项"];
    for (const heading of headings) {
      await expect(
        page.getByRole("heading", { name: heading, exact: true }),
      ).toBeVisible();
      if (provider === "zentao" && heading === "映射状态") {
        await page.getByLabel("active 导入为", { exact: true }).click();
        await page.getByRole("option", { name: /^To Do/ }).click();
      }
      await page.getByRole("button", { name: "下一步", exact: true }).click();
    }
    await expect(
      page.getByRole("heading", { name: "确认导入", exact: true }),
    ).toBeVisible();
    const started = page.waitForResponse(
      (r) => r.request().method() === "POST" && r.url().endsWith("/start"),
    );
    await page.getByRole("button", { name: "开始导入", exact: true }).click();
    const startedResponse = await started;
    expect(startedResponse.ok()).toBeTruthy();
    const job = await startedResponse.json();
    await expect
      .poll(
        async () => (await request(page, `${base}/imports/${job.id}`)).status,
        { timeout: 30_000 },
      )
      .toBe("SUCCEEDED");
    const tasks = (await request(page, `${base}/spaces/${target.id}/tasks`))
      .tasks;
    expect(tasks).toHaveLength(1);
    const final = await request(page, `${base}/integrations/${provider}`);
    expect(final.connections.map((c: { id: string }) => c.id)).toEqual([
      connection.id,
    ]);
    await page.reload();
    expect(
      (await request(page, `${base}/spaces/${target.id}/tasks`)).tasks[0].id,
    ).toBe(tasks[0].id);
  }
});
