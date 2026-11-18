import { expect, test, type Page } from "@playwright/test";

test.skip(
  process.env.OPENFOX_HIFOX_BATCH5_E2E !== "1",
  "requires isolated batch 5 services",
);
const org = "00000000-0000-0000-0000-0000000000a1";
const base = `/api/v1/organizations/${org}`;
const acknowledgement = "我同意将目标组织升级为五级优先级";

async function request(
  page: Page,
  path: string,
  method = "GET",
  body?: unknown,
) {
  const csrf = (await page.context().cookies()).find(
    (c) => c.name === "openfox_csrf",
  )?.value;
  const response = await page.request.fetch(path, {
    method,
    data: body,
    headers: {
      ...(csrf ? { "X-CSRF-Token": csrf } : {}),
      "Idempotency-Key": crypto.randomUUID(),
    },
  });
  expect(response.ok(), `${method} ${path}: ${response.status()}`).toBeTruthy();
  return response.json();
}

test("O01 CSV and native clone require confirmation and persist the lowest task and project priority", async ({
  page,
}) => {
  test.setTimeout(120_000);
  await page.goto("/login");
  await page.getByLabel("邮箱", { exact: true }).fill("alice@example.test");
  await page.getByRole("button", { name: "继续" }).click();
  await page
    .getByLabel("第 1 位")
    .pressSequentially(process.env.OPENFOX_LIVE_OTP_CODE!);
  await page.waitForURL(/\/o\//);
  await page.evaluate(
    (id) =>
      localStorage.setItem(
        `openfox:onboarding:${id}`,
        JSON.stringify({ step: "done" }),
      ),
    org,
  );
  expect((await request(page, `${base}/settings`)).priority_levels).toBe(4);
  const source = await request(page, `${base}/spaces`, "POST", {
    name: "O01 browser source",
    prefix: "OPR",
    visibility: "SHARED",
  });
  await page.goto(
    `/o/${org}/settings/import-export?source=jira&space=${source.id}`,
  );
  await page.getByRole("button", { name: "下一步", exact: true }).click();
  await page.getByLabel("选择 CSV 文件", { exact: true }).setInputFiles({
    name: "o01-priority.csv",
    mimeType: "text/csv",
    buffer: Buffer.from(
      "Summary,Issue key,Issue id,Status,Priority\nO01 lowest task,OP-1,1,Todo,Lowest\n",
    ),
  });
  await page.getByRole("button", { name: "上传并预览", exact: true }).click();
  for (const heading of [
    "预览",
    "映射列/字段",
    "映射状态",
    "映射用户",
    "导入选项",
  ]) {
    await expect(
      page.getByRole("heading", { name: heading, exact: true }),
    ).toBeVisible();
    await page.getByRole("button", { name: "下一步", exact: true }).click();
  }
  await expect(
    page.getByRole("heading", { name: "确认导入", exact: true }),
  ).toBeVisible();
  const ack = page.getByRole("checkbox", {
    name: acknowledgement,
    exact: true,
  });
  const start = page.getByRole("button", { name: "开始导入", exact: true });
  await expect(ack).not.toBeChecked();
  await expect(start).toBeDisabled();
  await expect(
    page.getByText("此设置影响整个目标组织。取消或回滚导入不会自动改回四级。", {
      exact: true,
    }),
  ).toBeVisible();
  await ack.check();
  await expect(start).toBeEnabled();
  await page.getByRole("button", { name: "重新预检", exact: true }).click();
  await expect(ack).not.toBeChecked();
  await expect(start).toBeDisabled();
  await ack.check();
  const started = page.waitForResponse(
    (r) => r.request().method() === "POST" && r.url().endsWith("/start"),
  );
  await start.click();
  const response = await started;
  expect(response.ok()).toBeTruthy();
  const job = await response.json();
  await expect
    .poll(
      async () => (await request(page, `${base}/imports/${job.id}`)).status,
      { timeout: 30_000 },
    )
    .toBe("SUCCEEDED");
  expect((await request(page, `${base}/settings`)).priority_levels).toBe(5);
  const imported = (await request(page, `${base}/spaces/${source.id}/tasks`))
    .tasks;
  expect(imported).toHaveLength(1);
  expect(imported[0]).toMatchObject({
    title: "O01 lowest task",
    priority: "LOWEST",
  });

  // A native clone must also account for the referenced LOWEST project.
  const project = await request(page, `${base}/projects`, "POST", {
    space_id: source.id,
    name: "O01 lowest project",
    priority: "LOWEST",
  });
  await request(
    page,
    `${base}/spaces/${source.id}/tasks/${imported[0].id}`,
    "PATCH",
    {
      version: imported[0].version,
      project_id: project.id,
    },
  );
  const target = await request(page, "/api/v1/organizations", "POST", {
    name: "O01 clone target",
    slug: `o01-${crypto.randomUUID().slice(0, 8)}`,
  });
  const targetBase = `/api/v1/organizations/${target.id}`;
  expect((await request(page, `${targetBase}/settings`)).priority_levels).toBe(
    4,
  );
  await page.goto(`/o/${org}/settings/spaces/${source.id}/general`);
  await page.getByRole("button", { name: "克隆空间", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "克隆空间", exact: true });
  await dialog.getByRole("button", { name: "预览", exact: true }).click();
  const cloneAck = dialog.getByRole("checkbox", {
    name: acknowledgement,
    exact: true,
  });
  const cloneStart = dialog.getByRole("button", { name: "开始", exact: true });
  await expect(cloneAck).not.toBeChecked();
  await expect(cloneStart).toBeDisabled();
  await cloneAck.check();
  await expect(cloneStart).toBeEnabled();
  await dialog.getByRole("button", { name: "预览", exact: true }).click();
  await expect(cloneAck).not.toBeChecked();
  await expect(cloneStart).toBeDisabled();
  await cloneAck.check();
  const cloning = page.waitForResponse(
    (r) =>
      r.request().method() === "POST" &&
      r.url().endsWith(`/spaces/${source.id}/clone`),
  );
  await cloneStart.click();
  const clonedResponse = await cloning;
  expect(clonedResponse.ok()).toBeTruthy();
  const clone = await clonedResponse.json();
  await expect
    .poll(
      async () =>
        (await request(page, `${targetBase}/clone-jobs/${clone.id}`)).status,
      { timeout: 30_000 },
    )
    .toBe("SUCCEEDED");
  const finished = await request(page, `${targetBase}/clone-jobs/${clone.id}`);
  expect((await request(page, `${targetBase}/settings`)).priority_levels).toBe(
    5,
  );
  const targetSpace = finished.target_space_id;
  expect(typeof targetSpace).toBe("string");
  expect(
    (await request(page, `${targetBase}/spaces/${targetSpace}/tasks`)).tasks,
  ).toEqual(
    expect.arrayContaining([
      expect.objectContaining({ title: "O01 lowest task", priority: "LOWEST" }),
    ]),
  );
  expect(
    (await request(page, `${targetBase}/projects?space_id=${targetSpace}`))
      .projects,
  ).toEqual(
    expect.arrayContaining([
      expect.objectContaining({
        name: "O01 lowest project",
        priority: "LOWEST",
      }),
    ]),
  );
  await page.reload();
  expect((await request(page, `${targetBase}/settings`)).priority_levels).toBe(
    5,
  );
});
