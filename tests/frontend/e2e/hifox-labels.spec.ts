import { expect, test, type Page } from "@playwright/test";

test.skip(
  process.env.OPENFOX_HIFOX_BATCH6_E2E !== "1",
  "requires isolated fixtures",
);
const org = "00000000-0000-0000-0000-0000000000a1";
const space = "00000000-0000-0000-0000-0000000000aa";
const base = `/api/v1/organizations/${org}`;
const path = `/o/${org}/settings/labels`;

async function login(page: Page, email: string) {
  await page.goto("/login");
  await page.getByLabel("邮箱", { exact: true }).fill(email);
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
  endpoint: string,
  method = "GET",
  data?: unknown,
) {
  const csrf = (await page.context().cookies()).find(
    (cookie) => cookie.name === "openfox_csrf",
  )?.value;
  return page.request.fetch(`${base}/${endpoint}`, {
    method,
    data,
    headers: csrf ? { "X-CSRF-Token": csrf } : {},
  });
}

async function body(
  page: Page,
  endpoint: string,
  method = "GET",
  data?: unknown,
) {
  const response = await request(page, endpoint, method, data);
  expect(response.ok(), `${method} ${endpoint}: ${response.status()}`).toBe(
    true,
  );
  return response.status() === 204 ? null : response.json();
}

test("V01 empty/nonempty groups persist and task counts follow real writes without granting member bulk access", async ({
  page,
  browser,
}) => {
  await login(page, "alice@example.test");
  expect((await body(page, "label-groups")).label_groups).toEqual([]);
  await page.goto(path);
  await page.getByRole("button", { name: "新建标签", exact: true }).click();
  let dialog = page.getByRole("dialog", { name: "创建标签", exact: true });
  await expect(dialog.getByLabel("分组", { exact: true })).toHaveCount(0);
  await dialog.getByRole("button", { name: "取消", exact: true }).click();

  await page.getByRole("button", { name: "新建分组", exact: true }).click();
  dialog = page.getByRole("dialog", { name: "创建分组", exact: true });
  await dialog.getByLabel("名称", { exact: true }).fill("V01 Area");
  await dialog.getByRole("button", { name: "创建", exact: true }).click();
  await expect(dialog).toBeHidden();
  const groups = (await body(page, "label-groups")).label_groups;
  expect(groups).toHaveLength(1);
  const group = groups[0];

  for (const name of ["V01 used", "V01 unused"]) {
    await page.getByRole("button", { name: "新建标签", exact: true }).click();
    dialog = page.getByRole("dialog", { name: "创建标签", exact: true });
    await dialog.getByLabel("名称", { exact: true }).fill(name);
    await dialog.getByLabel("分组", { exact: true }).click();
    await page.getByRole("option", { name: "V01 Area", exact: true }).click();
    await dialog.getByRole("button", { name: "创建", exact: true }).click();
    await expect(dialog).toBeHidden();
  }
  const labels = (await body(page, "labels")).labels;
  expect(labels).toHaveLength(2);
  for (const label of labels) {
    expect(label.group_id).toBe(group.id);
    expect(label.task_count).toBe(0);
  }
  expect((await body(page, "label-groups")).label_groups[0].label_count).toBe(
    2,
  );
  const used = labels.find(
    (label: { name: string }) => label.name === "V01 used",
  );
  const types = (await body(page, `spaces/${space}/task-types`)).task_types;
  let task = await body(page, `spaces/${space}/tasks`, "POST", {
    title: "V01 controlled label usage",
    type_id: types[0].id,
    label_ids: [used.id],
  });
  const row = (name: string) => page.getByRole("row").filter({ hasText: name });
  const checkCounts = async (count: number) => {
    const current = (await body(page, "labels")).labels;
    expect(
      current.find((label: { id: string }) => label.id === used.id).task_count,
    ).toBe(count);
    // The settings page refresh is explicit; this does not claim live count updates.
    await page.reload();
    await expect(row("V01 used").getByRole("cell").nth(3)).toHaveText(
      count ? String(count) : "",
    );
    await expect(row("V01 unused").getByRole("cell").nth(3)).toHaveText("");
    await expect(
      page.getByRole("button", { name: "折叠分组 V01 Area" }),
    ).toBeVisible();
  };
  await checkCounts(1);
  await body(
    page,
    `spaces/${space}/tasks/${task.id}?version=${task.version}`,
    "DELETE",
  );
  await checkCounts(0);
  task = await body(page, `spaces/${space}/tasks/${task.id}/restore`, "POST", {
    version: task.version + 1,
  });
  await checkCounts(1);
  await body(page, `spaces/${space}/tasks/${task.id}`, "PATCH", {
    version: task.version,
    label_ids: [],
  });
  await checkCounts(0);

  const context = await browser.newContext({
    baseURL: page.url().split("/o/")[0],
    locale: "zh-CN",
  });
  try {
    const member = await context.newPage();
    await login(member, "carol@example.test");
    await member.goto(path);
    await expect(member.getByText("V01 used", { exact: true })).toBeVisible();
    await expect(member.getByRole("checkbox")).toHaveCount(0);
    await expect(
      member.getByRole("button", { name: "新建标签", exact: true }),
    ).toHaveCount(0);
    const rejected = await request(member, "labels/bulk", "POST", {
      action: "ARCHIVE",
      label_ids: labels.map((label: { id: string }) => label.id),
    });
    expect(rejected.status()).toBe(403);
    const persisted = (await body(page, "labels")).labels;
    expect(persisted).toHaveLength(2);
    expect(
      persisted.every(
        (label: { archived_at: string | null }) => label.archived_at === null,
      ),
    ).toBe(true);
    expect((await body(page, "label-groups")).label_groups[0].label_count).toBe(
      2,
    );
  } finally {
    await context.close();
  }
});
