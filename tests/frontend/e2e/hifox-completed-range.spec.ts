import { expect, test, type Page } from "@playwright/test";

test.skip(
  process.env.OPENFOX_HIFOX_BATCH6_E2E !== "1",
  "requires isolated completed-time fixtures",
);
const org = "00000000-0000-0000-0000-0000000000a1";
const space = "00000000-0000-0000-0000-0000000000aa";
const typeId = "00000000-0000-0000-0000-000000006021";
const base = `/api/v1/organizations/${org}`;
const tasksPath = `${base}/spaces/${space}/tasks`;

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
    headers: csrf ? { "X-CSRF-Token": csrf } : {},
  });
  expect(response.ok(), `${method} ${path}: ${response.status()}`).toBeTruthy();
  return response.json();
}

test("T02 rolling range filters real persisted tasks in four layouts and shared views", async ({
  page,
}) => {
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
  const type = (
    await request(page, `${base}/spaces/${space}/task-types`)
  ).task_types.find((item: { id: string }) => item.id === typeId);
  const done = type.statuses.find(
    (item: { category: string }) => item.category === "DONE",
  );
  const todo = type.statuses.find(
    (item: { category: string }) => item.category === "TODO",
  );
  await request(page, tasksPath, "POST", {
    title: "T02 completed now",
    type_id: typeId,
    status_id: done.status_id,
  });
  await page.goto(`/o/${org}/spaces/${space}/tasks`);
  await page
    .getByRole("tab", { name: "T02 browser type", exact: true })
    .click();
  const display = page.getByRole("button", { name: "显示设置", exact: true });
  const range = page.getByRole("combobox", { name: "已完成任务", exact: true });
  const visible = async (name: string, shown: boolean) => {
    const locator = page.getByText(name, { exact: true });
    if (shown) await expect(locator.first()).toBeVisible();
    else await expect(locator).toHaveCount(0);
  };
  await display.click();
  await page.getByRole("radio", { name: "列表", exact: true }).click();
  await expect(range).toHaveValue("all");
  const counted = page.waitForResponse(
    (r) =>
      r.url().includes("/tasks/counts?") &&
      new URL(r.url()).searchParams.get("completed") === "last_7_days",
  );
  const listed = page.waitForResponse(
    (r) =>
      new URL(r.url()).pathname.endsWith(`/spaces/${space}/tasks`) &&
      new URL(r.url()).searchParams.get("completed") === "last_7_days",
  );
  await range.selectOption("last_7_days");
  const counts = await (await counted).json();
  const listResponse = await listed;
  expect(typeof counts.completed_as_of).toBe("string");
  expect(new URL(listResponse.url()).searchParams.get("completed_as_of")).toBe(
    counts.completed_as_of,
  );
  await display.click();
  await visible("T02 completed now", true);
  await visible("T02 active task", true);
  for (const name of [
    "T02 eight days ago",
    "T02 thirty-one days ago",
    "T02 unknown historical completion",
    "T02 cancelled task",
    "T02 duplicate task",
  ])
    await visible(name, false);
  for (const layout of ["看板", "表格", "甘特图"]) {
    await display.click();
    await page.getByRole("radio", { name: layout, exact: true }).click();
    await display.click();
    await visible("T02 completed now", true);
    await visible("T02 unknown historical completion", false);
    await visible("T02 eight days ago", false);
  }
  await page.reload();
  await page
    .getByRole("tab", { name: "T02 browser type", exact: true })
    .click();
  await display.click();
  await expect(range).toHaveValue("last_7_days");
  await range.selectOption("last_30_days");
  await display.click();
  await visible("T02 eight days ago", true);
  await visible("T02 thirty-one days ago", false);
  await visible("T02 unknown historical completion", false);
  await display.click();
  await range.selectOption("all");
  await display.click();
  for (const name of [
    "T02 unknown historical completion",
    "T02 cancelled task",
    "T02 duplicate task",
    "T02 thirty-one days ago",
  ])
    await visible(name, true);

  // Reopening and completing old unknown history creates a real new completion.
  const historicalPath = `${tasksPath}/00000000-0000-0000-0000-000000006031`;
  const historical = await request(page, historicalPath);
  const reopened = await request(page, historicalPath, "PATCH", {
    version: historical.version,
    status_id: todo.status_id,
  });
  await request(page, historicalPath, "PATCH", {
    version: reopened.version,
    status_id: done.status_id,
  });
  const view = await request(page, `${base}/spaces/${space}/views`, "POST", {
    name: "T02 shared range",
    visibility: "space",
    filters: { type_id: [typeId] },
    display: { layout: "list", completed: "last_30_days" },
  });
  await page.goto(`/o/${org}/spaces/${space}/views/${view.id}/edit`);
  await display.click();
  await expect(range).toHaveValue("last_30_days");
  await range.selectOption("last_7_days");
  await range.press("Escape");
  await expect(
    page.getByRole("dialog", { name: "显示设置", exact: true }),
  ).toBeHidden();
  const saved = page.waitForResponse(
    (r) =>
      r.request().method() === "PATCH" && r.url().endsWith(`/views/${view.id}`),
  );
  await page.getByRole("button", { name: "保存", exact: true }).click();
  expect((await saved).ok()).toBeTruthy();
  const stored = await request(page, `${base}/views/${view.id}`);
  expect(stored.display.completed).toBe("last_7_days");
  expect(stored.display).not.toHaveProperty("completed_as_of");
  await page.goto(`/o/${org}/spaces/${space}/views/${view.id}`);
  await page.reload();
  await visible("T02 unknown historical completion", true);
  await visible("T02 eight days ago", false);
});
