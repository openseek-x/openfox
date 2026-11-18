import { expect, test, type Page } from "@playwright/test";

test.skip(
  process.env.OPENFOX_HIFOX_BATCH6_E2E !== "1",
  "requires isolated fixtures",
);
const org = "00000000-0000-0000-0000-0000000000a1";
const template = "00000000-0000-0000-0000-0000000000aa";
const recipient = "00000000-0000-0000-0000-0000000000c3";
const base = `/api/v1/organizations/${org}`;

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
    headers: csrf ? { "X-CSRF-Token": csrf } : {},
  });
  expect(response.ok(), `${method} ${path}: ${response.status()}`).toBeTruthy();
  return response.status() === 204 ? null : response.json();
}

async function layout(page: Page, name: string, change = false) {
  const display = page.getByRole("button", { name: "显示设置", exact: true });
  await display.click();
  const radio = page.getByRole("radio", { name, exact: true });
  if (change) await radio.click();
  await expect(radio).toBeChecked();
  await display.click();
}

async function tasks(page: Page, high: boolean, low: boolean) {
  for (const [title, visible] of [
    ["T03 high task", high],
    ["T03 low task", low],
  ] as const) {
    const task = page.getByText(title, { exact: true });
    if (visible) await expect(task.first()).toBeVisible();
    else await expect(task).toHaveCount(0);
  }
}

test("T03 dynamic shared defaults preserve old overrides and follow current filters and display", async ({
  page,
  browser,
}) => {
  await login(page, "alice@example.test");
  let space = await request(page, `${base}/spaces`, "POST", {
    name: "T03 default view space",
    visibility: "private",
    copy_from_space_id: template,
  });
  const spacePath = `${base}/spaces/${space.id}`;
  await request(page, `${spacePath}/members/${recipient}`, "PUT", {
    role: "MEMBER",
    status: "ACTIVE",
  });
  const types = await request(page, `${spacePath}/task-types`);
  for (const [priority, title] of [
    ["HIGH", "T03 high task"],
    ["LOW", "T03 low task"],
  ])
    await request(page, `${spacePath}/tasks`, "POST", {
      title,
      priority,
      type_id: types.task_types[0].id,
    });
  let view = await request(page, `${spacePath}/views`, "POST", {
    name: "T03 shared source",
    visibility: "space",
    filters: { priority: ["HIGH"] },
    display: { layout: "list" },
  });
  const viewPath = `${base}/views/${view.id}`;
  const pagePath = `/o/${org}/spaces/${space.id}/tasks`;
  const storageKey = `openfox:tasks-display:${org}:space:${space.id}`;
  // A pre-feature local choice must keep its original unfiltered task set.
  await page.evaluate(
    (key) => localStorage.setItem(key, JSON.stringify({ layout: "table" })),
    storageKey,
  );
  await page.goto(pagePath);
  await page.getByRole("button", { name: "空间默认视图", exact: true }).click();
  const dialog = page.getByRole("dialog", {
    name: "空间默认视图",
    exact: true,
  });
  await dialog
    .getByRole("combobox", { name: "默认共享视图", exact: true })
    .selectOption(view.id);
  // A real concurrent API update exercises the UI's CAS conflict recovery.
  space = await request(page, spacePath);
  await request(page, spacePath, "PATCH", {
    version: space.version,
    name: "T03 concurrent rename",
  });
  await dialog
    .getByRole("button", { name: "保存默认视图", exact: true })
    .click();
  await expect(dialog.getByRole("alert")).toBeVisible();
  await dialog
    .getByRole("button", { name: "刷新版本后重试", exact: true })
    .click();
  await expect(dialog.getByRole("alert")).toHaveCount(0);
  await dialog
    .getByRole("button", { name: "保存默认视图", exact: true })
    .click();
  await expect(dialog).toBeHidden();
  expect((await request(page, spacePath)).default_view_id).toBe(view.id);
  await expect(page.getByText("个人视图", { exact: true })).toBeVisible();
  await tasks(page, true, true);
  await layout(page, "表格");

  const context = await browser.newContext({
    baseURL: page.url().split("/o/")[0],
    locale: "zh-CN",
  });
  try {
    const reader = await context.newPage();
    await login(reader, "carol@example.test");
    await reader.goto(pagePath);
    await expect(
      reader.getByText("空间默认：T03 shared source", { exact: true }),
    ).toBeVisible();
    await expect(
      reader.getByRole("button", { name: "空间默认视图", exact: true }),
    ).toHaveCount(0);
    await tasks(reader, true, false);
    await layout(reader, "列表");
    view = await request(page, viewPath, "PATCH", {
      version: view.version,
      filters: { priority: ["LOW"] },
      display: { layout: "board" },
    });
    await tasks(reader, false, true);
    await layout(reader, "看板");
    await layout(reader, "表格", true);
    expect(
      await reader.evaluate(
        (key) => localStorage.getItem(`${key}:inherit-default`),
        storageKey,
      ),
    ).toBe("true");
    await reader.reload();
    await tasks(reader, false, true);
    await layout(reader, "表格");
    view = await request(page, viewPath, "PATCH", {
      version: view.version,
      filters: { priority: ["HIGH"] },
      display: { layout: "list" },
    });
    await tasks(reader, true, false);
    await layout(reader, "表格");
    await reader
      .getByRole("button", { name: "改用个人视图", exact: true })
      .click();
    await tasks(reader, true, true);
    await reader
      .getByRole("button", { name: "使用空间默认", exact: true })
      .click();
    await tasks(reader, true, false);
    await layout(reader, "列表");

    // Source privacy removes the default immediately; no copied filter survives.
    view = await request(page, viewPath, "PATCH", {
      version: view.version,
      visibility: "personal",
    });
    await tasks(reader, true, true);
    await expect(
      reader.getByText("空间默认：T03 shared source", { exact: true }),
    ).toHaveCount(0);
    expect((await request(page, spacePath)).default_view_id).toBeNull();
    view = await request(page, viewPath, "PATCH", {
      version: view.version,
      visibility: "space",
    });
    space = await request(page, spacePath);
    space = await request(page, spacePath, "PATCH", {
      version: space.version,
      default_view_id: view.id,
    });
    await tasks(reader, true, false);
    space = await request(page, spacePath, "PATCH", {
      version: space.version,
      features: { views: false },
    });
    await tasks(reader, true, true);
    // Old clients omit the new field while views are disabled; stored default survives.
    space = await request(page, spacePath, "PATCH", {
      version: space.version,
      name: "T03 disabled rename",
    });
    space = await request(page, spacePath, "PATCH", {
      version: space.version,
      features: { views: true },
    });
    expect(space.default_view_id).toBe(view.id);
    await tasks(reader, true, false);
    await request(page, `${viewPath}?version=${view.version}`, "DELETE");
    await tasks(reader, true, true);
    await reader.reload();
    await tasks(reader, true, true);
    expect((await request(page, spacePath)).default_view_id).toBeNull();
  } finally {
    await context.close();
  }
});
