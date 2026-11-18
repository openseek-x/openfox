import { expect, test, type Page } from "@playwright/test";

// Real HTTP and PostgreSQL; opt in only with the disposable parity fixture.
test.skip(
  process.env.OPENFOX_HIFOX_BATCH4_E2E !== "1",
  "requires the isolated batch 4 fixture database",
);
const org = "00000000-0000-0000-0000-0000000000a1";
const space = "00000000-0000-0000-0000-0000000000aa";
const base = `/api/v1/organizations/${org}`;
const channelsPath = "/api/v1/me/notification-channels";

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

async function toggle(page: Page, name: string) {
  const saved = page.waitForResponse(
    (response) =>
      response.request().method() === "PATCH" &&
      response.url().endsWith(`${channelsPath}/browser`),
  );
  await page.getByRole("switch", { name, exact: true }).click();
  expect((await saved).status()).toBe(200);
  await expect(page.getByRole("switch", { name, exact: true })).toBeEnabled();
}

test("N01 channel leaf and group choices persist and filter real task events before grouping", async ({
  page,
  browser,
}) => {
  await login(page, "carol@example.test");
  // Exercise the old coarse API as well as the new leaf UI.
  await request(page, `${channelsPath}/browser`, "PUT", {
    enabled: true,
    disabled_events: ["status"],
  });
  await page.goto(`/o/${org}/settings/notifications`);
  await page.getByRole("button", { name: /^浏览器 / }).click();
  const priority = "你负责的任务优先级调整时";
  const due = "你负责的任务截止日期修改时";
  const group = "任务状态变更";
  await expect(page.getByRole("switch")).toHaveCount(51);
  await expect(
    page.getByRole("switch", { name: priority, exact: true }),
  ).not.toBeChecked();
  await toggle(page, priority);
  await toggle(page, due);
  await toggle(page, due);
  await toggle(page, group);
  await expect(
    page.getByRole("switch", { name: priority, exact: true }),
  ).toBeChecked();
  await expect(
    page.getByRole("switch", { name: priority, exact: true }),
  ).toBeDisabled();
  await toggle(page, group);
  await expect(
    page.getByRole("switch", { name: due, exact: true }),
  ).not.toBeChecked();
  await page.reload();
  await page.getByRole("button", { name: /^浏览器 / }).click();
  await expect(
    page.getByRole("switch", { name: priority, exact: true }),
  ).toBeChecked();
  await expect(
    page.getByRole("switch", { name: due, exact: true }),
  ).not.toBeChecked();
  const policy = (await request(page, channelsPath)).channels.find(
    (value: { channel: string }) => value.channel === "browser",
  );
  expect(policy.disabled_events).toEqual(["status"]);
  expect(policy.event_overrides).toMatchObject({
    task_priority_updated_assignee: true,
    task_due_date_updated_assignee: false,
  });
  expect(policy.muted_groups).not.toContain("status_changes");

  const actorContext = await browser.newContext({
    baseURL: page.url().split("/o/")[0],
    locale: "zh-CN",
  });
  try {
    const actor = await actorContext.newPage();
    await login(actor, "alice@example.test");
    let task = await request(actor, `${base}/spaces/${space}/tasks`, "POST", {
      title: `N01 real events ${Date.now()}`,
      type_id: "00000000-0000-0000-0000-000000000111",
      assignee_user_id: "00000000-0000-0000-0000-0000000000c3",
    });
    const taskPath = `${base}/spaces/${space}/tasks/${task.id}`;
    task = await request(actor, taskPath, "PATCH", {
      version: task.version,
      priority: "HIGH",
    });
    await request(actor, taskPath, "PATCH", {
      version: task.version,
      due_date: "2030-02-01",
    });
    const latest = async (query: string) =>
      (await request(page, `${base}/inbox?${query}`)).groups.find(
        (value: { task_id?: string }) => value.task_id === task.id,
      )?.inbox_event;
    expect(await latest("delivery=banner&channel=browser")).toBe(
      "task_priority_updated_assignee",
    );
    expect(await latest("delivery=banner&channel=desktop")).toBe(
      "task_due_date_updated_assignee",
    );
    expect(await latest("delivery=inbox")).toBe(
      "task_due_date_updated_assignee",
    );
    expect(await latest("delivery=banner")).not.toBe(
      "task_due_date_updated_assignee",
    );
    await toggle(page, group);
    expect(await latest("delivery=banner&channel=browser")).not.toBe(
      "task_priority_updated_assignee",
    );
    await expect(
      page.getByRole("switch", { name: priority, exact: true }),
    ).toBeChecked();
  } finally {
    await actorContext.close();
  }
});

test("N02 independent preferences do not present unbound IM accounts as enabled", async ({
  page,
}) => {
  await login(page, "alice@example.test");
  for (const [channel, enabled] of [
    ["feishu", false],
    ["lark", true],
    ["dingtalk", false],
  ] as const) {
    await request(page, `${channelsPath}/${channel}`, "PUT", {
      enabled,
      disabled_events: channel === "lark" ? ["comment"] : [],
    });
  }
  let saved = (await request(page, channelsPath)).channels;
  for (const [channel, enabled] of [
    ["feishu", false],
    ["lark", true],
    ["dingtalk", false],
  ] as const) {
    const policy = saved.find(
      (item: { channel: string }) => item.channel === channel,
    );
    expect(policy.enabled).toBe(enabled);
    expect(policy.direct_messages).toBe(false);
  }
  await request(page, `${channelsPath}/feishu`, "PUT", {
    enabled: true,
    disabled_events: ["status"],
  });
  saved = (await request(page, channelsPath)).channels;
  expect(
    saved.find((item: { channel: string }) => item.channel === "lark")
      .disabled_events,
  ).toEqual(["comment"]);
  expect(
    saved.find((item: { channel: string }) => item.channel === "dingtalk")
      .enabled,
  ).toBe(false);
  for (const name of ["飞书", "Lark", "钉钉"]) {
    await page.goto(`/o/${org}/settings/notifications`);
    const row = page.getByRole("button", {
      name: `${name} 未绑定`,
      exact: true,
    });
    await expect(row).toBeVisible();
    await row.click();
    const toggle = page.getByRole("switch", {
      name: `显示 ${name} 通知`,
      exact: true,
    });
    await expect(toggle).toBeDisabled();
    await expect(toggle).not.toBeChecked();
  }
});
