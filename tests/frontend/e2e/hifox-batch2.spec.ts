import { expect, test, type Page } from "@playwright/test";

// Run only with the disposable DB plus hifox-batch2-fixture.sql. No route mocks.
test.skip(
  process.env.OPENFOX_HIFOX_BATCH2_E2E !== "1",
  "requires the isolated batch 2 fixture database",
);
const org = "00000000-0000-0000-0000-0000000000a1";
const space = "00000000-0000-0000-0000-0000000000aa";
const base = `/api/v1/organizations/${org}`;

async function login(page: Page) {
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
}

async function request(
  page: Page,
  path: string,
  method = "GET",
  body?: unknown,
) {
  const csrf = (await page.context().cookies()).find(
    (cookie) => cookie.name === "openfox_csrf",
  )?.value;
  const response = await page.request.fetch(path, {
    method,
    data: body,
    headers: csrf ? { "X-CSRF-Token": csrf } : {},
  });
  expect(
    response.ok(),
    `${method} ${path}: ${response.status()} ${await response.text()}`,
  ).toBeTruthy();
  return response.json();
}

type WorkflowType = {
  id: string;
  version: number;
  statuses: {
    status_id: string;
    name: string;
    category: string;
    position: number;
    default: boolean;
    task_count: number;
  }[];
};

test("S07 type-local workflow order and counts persist without changing another type or default", async ({
  page,
}) => {
  await login(page);
  const typesPath = `${base}/spaces/${space}/task-types`;
  const listing = await request(page, typesPath);
  const template: WorkflowType = listing.task_types.find(
    (item: WorkflowType) => item.id === "00000000-0000-0000-0000-000000000111",
  );
  const common = {
    status_ids: template.statuses.map((status) => status.status_id),
    default_status_id: template.statuses.find((status) => status.default)!
      .status_id,
  };
  const first: WorkflowType = await request(page, typesPath, "POST", {
    name: "Browser workflow A",
    ...common,
  });
  const second: WorkflowType = await request(page, typesPath, "POST", {
    name: "Browser workflow B",
    ...common,
  });
  const building = first.statuses.find((status) => status.name === "Building")!;
  const reviewing = first.statuses.find(
    (status) => status.name === "Reviewing",
  )!;
  await request(page, `${base}/spaces/${space}/tasks`, "POST", {
    title: "Counted workflow browser task",
    type_id: first.id,
    status_id: building.status_id,
  });
  await page.goto(
    `/o/${org}/settings/spaces/${space}/task-types/${first.id}?tab=workflow`,
  );
  await expect(
    page.getByRole("listitem").filter({ hasText: "Building" }),
  ).toContainText("1 个任务");
  const saved = page.waitForResponse(
    (response) =>
      response.url().endsWith(`/task-types/${first.id}`) &&
      response.request().method() === "PATCH",
  );
  await page
    .getByRole("button", { name: "上移 Reviewing", exact: true })
    .click();
  expect((await saved).status()).toBe(200);
  await page.reload();
  await expect(
    page.getByRole("listitem").filter({ hasText: "Reviewing" }),
  ).toBeVisible();
  const visibleOrder = await page.getByRole("listitem").allTextContents();
  expect(
    visibleOrder.findIndex((text) => text.includes("Reviewing")),
  ).toBeLessThan(visibleOrder.findIndex((text) => text.includes("Building")));
  const after = (await request(page, typesPath)).task_types as WorkflowType[];
  const updated = after.find((item) => item.id === first.id)!;
  expect(updated.statuses.find((status) => status.default)!.status_id).toBe(
    common.default_status_id,
  );
  expect(
    updated.statuses.find((status) => status.status_id === reviewing.status_id)!
      .position,
  ).toBeLessThan(
    updated.statuses.find((status) => status.status_id === building.status_id)!
      .position,
  );
  expect(after.find((item) => item.id === second.id)!.statuses).toEqual(
    second.statuses,
  );
});

test("R07 real persisted capabilities show bounded tool results and collection time", async ({
  page,
}) => {
  await login(page);
  const device = "00000000-0000-0000-0000-000000000011";
  await page.goto(`/o/${org}/settings/computers?computer=${device}`);
  const section = page.getByRole("region", { name: "开发环境" });
  await expect(section).toContainText("Node.js");
  await expect(section).toContainText("v22.23.1");
  await expect(section).toContainText("/工具 bin/node");
  for (const status of ["已安装", "未检测到", "检测超时", "检测失败"])
    await expect(section).toContainText(status);
  await expect(section.locator("time")).toHaveCount(4);
  await expect(section.locator("time").first()).toHaveAttribute(
    "datetime",
    "2026-11-13T01:02:03Z",
  );
  await page.reload();
  await expect(section).toContainText("v22.23.1");
});

test("O02 space list shows the authenticated creator and leaves legacy attribution unknown", async ({
  page,
}) => {
  await login(page);
  const me = await request(page, "/api/v1/me");
  const created = await request(page, `${base}/spaces`, "POST", {
    name: "Creator browser space",
    prefix: "O02B",
    visibility: "shared",
  });
  expect(created.creator.id).toBe(me.id);
  expect(created.creator.status).toBe("active");
  const legacy = await request(page, `${base}/spaces/${space}`);
  expect(legacy.creator).toBeNull();
  await page.goto(`/o/${org}/settings/spaces`);
  await expect(
    page.getByRole("columnheader", { name: "创建人", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("row").filter({ hasText: created.name }),
  ).toContainText(created.creator.name);
  await expect(
    page.getByRole("row").filter({ hasText: legacy.name }),
  ).toContainText("未知");
  await page.reload();
  await expect(
    page.getByRole("row").filter({ hasText: created.name }),
  ).toContainText(created.creator.name);
});
