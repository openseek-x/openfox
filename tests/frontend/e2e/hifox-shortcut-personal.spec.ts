import { readFileSync } from "node:fs";
import { expect, test, type Page } from "@playwright/test";

const path = process.env.OPENFOX_SHORTCUT_BROWSER_FILE;
test.skip(!path, "requires the isolated Shortcut TLS/PG harness");
const fixture = path ? JSON.parse(readFileSync(path, "utf8")) : {};
const base = `/api/v1/organizations/${fixture.organization_id}`;
const sourceMember = "22222222-2222-4222-8222-222222222222";
const summaryURL = path
  ? `${fixture.api_url}/__shortcut_test/status`
  : "http://127.0.0.1:18149/__shortcut_test/status";
if (path) {
  const target = new URL(fixture.api_url);
  if (target.origin !== "http://127.0.0.1:18149")
    throw new Error(
      "Shortcut browser harness must use its dedicated localhost API",
    );
}

type HTTPRecord = { method: string; path: string; status: number };
const explicitRequests: HTTPRecord[] = [];
type Summary = { posts: number; text: string[] };
type Task = {
  id: string;
  title: string;
  assignee_user_id?: string;
  external_assignee?: unknown;
};

async function request(
  page: Page,
  url: string,
  method = "GET",
  data?: unknown,
) {
  const csrf = (await page.context().cookies()).find(
    (cookie) => cookie.name === "openfox_csrf",
  )?.value;
  const response = await page.request.fetch(url, {
    method,
    data,
    headers: {
      ...(csrf ? { "X-CSRF-Token": csrf } : {}),
      "Idempotency-Key": crypto.randomUUID(),
    },
  });
  explicitRequests.push({
    method,
    path: new URL(url, fixture.api_url).pathname,
    status: response.status(),
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
async function currentBinding(page: Page) {
  const view = await request(page, `${base}/me/account-bindings`);
  const provider = view.providers.find(
    (item: { id: string }) => item.id === "shortcut",
  );
  return provider.instances.find(
    (item: { connection_id: string }) =>
      item.connection_id === fixture.parent_id,
  ).binding;
}
async function summary(page: Page): Promise<Summary> {
  const response = await page.request.get(summaryURL);
  explicitRequests.push({
    method: "GET",
    path: "/__shortcut_test/status",
    status: response.status(),
  });
  expect(response.ok()).toBeTruthy();
  return response.json();
}
async function manage(page: Page) {
  await page
    .getByTestId("binding-shortcut")
    .getByRole("button", { name: "已连接" })
    .click();
  await page
    .getByRole("menuitem", {
      name: "管理 Fixture Shortcut workspace 身份与评论",
      exact: true,
    })
    .click();
  return page.getByRole("dialog", { name: "Shortcut 个人身份与评论授权" });
}
async function checkNow(page: Page, id: string) {
  await expect
    .poll(
      async () => (await request(page, `${base}/imports/${id}`)).sync.checking,
    )
    .toBe(false);
  const before = await request(page, `${base}/imports/${id}`);
  await request(page, `${base}/imports/${id}/sync`, "PUT", {
    version: before.sync.version,
    check_now: true,
  });
  await expect
    .poll(async () => {
      const current = await request(page, `${base}/imports/${id}`);
      return (
        !current.sync.checking &&
        current.sync.last_checked !== before.sync.last_checked
      );
    })
    .toBe(true);
}

// Real application UI/API, a private PostgreSQL database and worker, and the
// harness's local TLS Shortcut server. There are no page.route/request mocks.
test("P03 identity modes, scoped import, personal comment delivery and unlink", async ({
  page,
  browser,
}) => {
  test.setTimeout(120_000);
  const http: HTTPRecord[] = [];
  explicitRequests.length = 0;
  const record = (target: Page) =>
    target.on("response", (response) => {
      const url = new URL(response.url());
      if (url.pathname.startsWith("/api/v1/") && http.length < 1000)
        http.push({
          method: response.request().method(),
          path: url.pathname,
          status: response.status(),
        });
    });
  record(page);
  const ownerContext = await browser.newContext({ locale: "zh-CN" });
  const owner = await ownerContext.newPage();
  record(owner);
  try {
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
    const row = page.getByTestId("binding-shortcut");
    await row.getByRole("button", { name: "绑定", exact: true }).click();
    let dialog = page.getByRole("dialog", {
      name: "Shortcut 个人身份与评论授权",
    });
    await expect(
      dialog.getByRole("checkbox", { name: "允许以本人身份同步新评论" }),
    ).not.toBeChecked();
    await expect(
      dialog.getByRole("button", { name: "验证并保存" }),
    ).toBeDisabled();
    expect((await summary(page)).posts).toBe(0);
    await dialog
      .getByLabel("Shortcut V3 个人 Token", { exact: true })
      .fill("invalid");
    await dialog.getByRole("button", { name: "验证并保存" }).click();
    await expect(dialog.getByRole("alert")).toContainText("Token 无效");
    await expect(
      dialog.getByLabel("Shortcut V3 个人 Token", { exact: true }),
    ).toHaveValue("invalid");
    await dialog
      .getByLabel("Shortcut V3 个人 Token", { exact: true })
      .fill("member");
    await dialog.getByRole("button", { name: "验证并保存" }).click();
    await expect(dialog).toBeHidden();
    await expect
      .poll(
        async () =>
          (await currentBinding(page))?.shortcut_identity.comments_enabled,
      )
      .toBe(false);
    await page.screenshot({
      path: test.info().outputPath("01-identity-only.png"),
      fullPage: true,
    });

    dialog = await manage(page);
    await expect(
      dialog.getByLabel("Shortcut V3 个人 Token", { exact: true }),
    ).toHaveValue("");
    await dialog
      .getByRole("checkbox", { name: "允许以本人身份同步新评论" })
      .check();
    await expect(
      dialog.getByRole("button", { name: "验证并保存" }),
    ).toBeDisabled();
    await dialog
      .getByLabel("Shortcut V3 个人 Token", { exact: true })
      .fill("member");
    await dialog.getByRole("button", { name: "验证并保存" }).click();
    await expect(dialog).toBeHidden();
    await expect
      .poll(
        async () =>
          (await currentBinding(page))?.shortcut_identity.comments_enabled,
      )
      .toBe(true);

    dialog = await manage(page);
    await dialog
      .getByRole("checkbox", { name: "允许以本人身份同步新评论" })
      .uncheck();
    await expect(
      dialog.getByLabel("Shortcut V3 个人 Token", { exact: true }),
    ).toHaveValue("");
    await dialog.getByRole("button", { name: "关闭评论授权" }).click();
    await expect(dialog).toBeHidden();
    await expect
      .poll(
        async () =>
          (await currentBinding(page))?.shortcut_identity.comments_enabled,
      )
      .toBe(false);

    dialog = await manage(page);
    await expect(dialog.getByRole("checkbox")).not.toBeChecked();
    await dialog
      .getByRole("checkbox", { name: "允许以本人身份同步新评论" })
      .check();
    await dialog
      .getByLabel("Shortcut V3 个人 Token", { exact: true })
      .fill("member");
    await dialog.getByRole("button", { name: "验证并保存" }).click();
    await expect(dialog).toBeHidden();
    await expect
      .poll(
        async () =>
          (await currentBinding(page))?.shortcut_identity.comments_enabled,
      )
      .toBe(true);
    expect((await summary(page)).posts).toBe(0);
    await page.reload();
    dialog = await manage(page);
    await expect(dialog.getByRole("checkbox")).toBeChecked();
    await expect(
      dialog.getByLabel("Shortcut V3 个人 Token", { exact: true }),
    ).toHaveValue("");
    await page.screenshot({
      path: test.info().outputPath("02-comments-authorized.png"),
      fullPage: true,
    });
    await dialog.getByRole("button", { name: "取消", exact: true }).click();

    await login(owner, fixture.owner_email);
    await owner.goto(
      `/o/${fixture.organization_id}/settings/import-export?source=shortcut&method=api&space=${fixture.space_id}&sync=1`,
    );
    await owner.getByRole("button", { name: "继续", exact: true }).click();
    await owner.getByLabel("实例", { exact: true }).click();
    await owner
      .getByRole("option", { name: "Fixture Shortcut workspace", exact: true })
      .click();
    await owner.getByLabel("项目", { exact: true }).click();
    await owner
      .getByRole("option", { name: "Fixture Shortcut project", exact: true })
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
    await owner.screenshot({
      path: test.info().outputPath("03-verified-mapping.png"),
      fullPage: true,
    });
    await owner.getByRole("button", { name: "下一步", exact: true }).click();
    await expect(
      owner.getByRole("heading", { name: "导入选项", exact: true }),
    ).toBeVisible();
    const continuous = owner.getByRole("switch", {
      name: "导入后与 Shortcut 双向同步",
      exact: true,
    });
    await expect(continuous).toBeVisible();
    if ((await continuous.getAttribute("aria-checked")) !== "true")
      await continuous.click();
    await expect(continuous).toBeChecked();
    await owner.getByRole("button", { name: "下一步", exact: true }).click();
    const started = owner.waitForResponse(
      (response) =>
        response.request().method() === "POST" &&
        response.url().endsWith("/start"),
    );
    await owner.getByRole("button", { name: "开始导入", exact: true }).click();
    const startResponse = await started;
    expect(startResponse.ok()).toBeTruthy();
    const job = await startResponse.json();
    await expect
      .poll(
        async () => (await request(owner, `${base}/imports/${job.id}`)).status,
      )
      .toBe("SUCCEEDED");
    await expect(
      owner.getByRole("heading", { name: "导入完成", exact: true }),
    ).toBeVisible({ timeout: 15_000 });
    await owner.screenshot({
      path: test.info().outputPath("04-import-complete.png"),
      fullPage: true,
    });
    const imported = await request(owner, `${base}/imports/${job.id}`);
    expect(imported.config.options.continuous_sync).toBe(true);
    expect(
      imported.config.users.find(
        (item: { source_id: string }) => item.source_id === sourceMember,
      ).verified_binding_id,
    ).toBeTruthy();
    expect(imported.sync.state).toBe("ACTIVE");
    // The normal sync management API changes direction only if needed.
    if (imported.sync.direction !== "BIDIRECTIONAL")
      await request(owner, `${base}/imports/${job.id}/sync`, "PUT", {
        version: imported.sync.version,
        direction: "BIDIRECTIONAL",
      });
    const tasks: Task[] = (
      await request(owner, `${base}/spaces/${fixture.space_id}/tasks`)
    ).tasks;
    expect(tasks).toHaveLength(2);
    const task = tasks.find((item) => item.title === "Shortcut task 8000")!;
    expect(task.assignee_user_id).toBe(fixture.member_id);
    expect(
      tasks.find((item) => item.title === "Shortcut task 8001")
        ?.external_assignee,
    ).toBeTruthy();

    // A real local comment API writes the original member as author. The real
    // worker must send it once to the controlled TLS Shortcut endpoint.
    const text = "P03 browser authorized member comment";
    await request(
      page,
      `${base}/spaces/${fixture.space_id}/tasks/${task.id}/comments`,
      "POST",
      { body: text, client_comment_id: crypto.randomUUID() },
    );
    await checkNow(owner, job.id);
    await expect.poll(async () => (await summary(page)).posts).toBe(1);
    expect((await summary(page)).text).toEqual([text]);
    await checkNow(owner, job.id);
    expect((await summary(page)).posts).toBe(1);
    await test.info().attach("personal-comment-delivery.json", {
      body: JSON.stringify(await summary(page), null, 2),
      contentType: "application/json",
    });

    await row.getByRole("button", { name: "已连接" }).click();
    await page.getByRole("menuitem", { name: /断开 / }).click();
    const confirm = page.getByRole("dialog", { name: /断开 Shortcut/ });
    await expect(
      confirm.getByText(/尚未开始发送的本人评论停止发送/),
    ).toBeVisible();
    await confirm
      .getByRole("button", { name: "断开连接", exact: true })
      .click();
    await expect(
      row.getByRole("button", { name: "绑定", exact: true }),
    ).toBeVisible();
    expect(await currentBinding(page)).toBeNull();
    await request(
      page,
      `${base}/spaces/${fixture.space_id}/tasks/${task.id}/comments`,
      "POST",
      {
        body: "P03 browser comment after unlink",
        client_comment_id: crypto.randomUUID(),
      },
    );
    await checkNow(owner, job.id);
    expect((await summary(page)).posts).toBe(1);
    const finalTasks: Task[] = (
      await request(owner, `${base}/spaces/${fixture.space_id}/tasks`)
    ).tasks;
    expect(
      finalTasks.find((item) => item.id === task.id)?.assignee_user_id,
    ).toBe(fixture.member_id);
    await page.screenshot({
      path: test.info().outputPath("05-unlinked-history-retained.png"),
      fullPage: true,
    });
    await test.info().attach("post-unlink-delivery.json", {
      body: JSON.stringify(await summary(page), null, 2),
      contentType: "application/json",
    });
  } finally {
    await test.info().attach("browser-http-summary.json", {
      body: JSON.stringify(
        { ui: http, explicit_api: explicitRequests },
        null,
        2,
      ),
      contentType: "application/json",
    });
    await ownerContext.close();
  }
});
