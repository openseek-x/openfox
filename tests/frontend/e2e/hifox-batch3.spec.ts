import { expect, test, type Page } from "@playwright/test";

// Opt in only against a disposable local database. These tests use the real API.
test.skip(
  process.env.OPENFOX_HIFOX_BATCH3_E2E !== "1",
  "requires the isolated batch 3 fixture database",
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

async function expectAbove(page: Page, first: string, second: string) {
  await expect(page.getByText(first, { exact: true })).toBeVisible();
  await expect(page.getByText(second, { exact: true })).toBeVisible();
  await expect
    .poll(async () => {
      const a = await page.getByText(first, { exact: true }).boundingBox();
      const b = await page.getByText(second, { exact: true }).boundingBox();
      return Boolean(a && b && a.y < b.y);
    })
    .toBe(true);
}

test("T01 sorting direction and group order persist across layouts and shared-view saves", async ({
  page,
}) => {
  await login(page);
  const suffix = Date.now();
  const typesPath = `${base}/spaces/${space}/task-types`;
  const template = (await request(page, typesPath)).task_types.find(
    (type: { id: string }) =>
      type.id === "00000000-0000-0000-0000-000000000111",
  );
  const type = await request(page, typesPath, "POST", {
    name: `T01 browser ${suffix}`,
    status_ids: template.statuses.map(
      (status: { status_id: string }) => status.status_id,
    ),
    default_status_id: template.statuses.find(
      (status: { default: boolean }) => status.default,
    ).status_id,
  });
  const building = type.statuses.find(
    (status: { name: string }) => status.name === "Building",
  );
  const reviewing = type.statuses.find(
    (status: { name: string }) => status.name === "Reviewing",
  );
  const early = `T01 early ${suffix}`;
  const later = `T01 later ${suffix}`;
  for (const [title, status] of [
    [early, building],
    [later, building],
    [`T01 other ${suffix}`, reviewing],
  ] as const) {
    await request(page, `${base}/spaces/${space}/tasks`, "POST", {
      title,
      type_id: type.id,
      status_id: status.status_id,
    });
  }
  const display = page.getByRole("button", { name: "显示设置", exact: true });
  await page.goto(`/o/${org}/spaces/${space}/tasks`);
  await page.getByRole("tab", { name: type.name, exact: true }).click();
  await display.click();
  await page.getByRole("radio", { name: "列表", exact: true }).click();
  await page
    .getByRole("combobox", { name: "排序", exact: true })
    .selectOption("created");
  await page
    .getByRole("combobox", { name: "任务排序方向", exact: true })
    .selectOption("asc");
  await display.click();
  await expectAbove(page, early, later);
  await display.click();
  await page
    .getByRole("combobox", { name: "任务排序方向", exact: true })
    .selectOption("desc");
  await page
    .getByRole("combobox", { name: "状态分组顺序", exact: true })
    .selectOption("reverse");
  await display.click();
  await expectAbove(page, later, early);
  const groups = page.getByRole("button", {
    name: /^展开或折叠「(Building|Reviewing)」$/,
  });
  await expect(groups).toHaveCount(2);
  expect(
    await groups.evaluateAll((nodes) =>
      nodes.map((node) => node.getAttribute("aria-label")),
    ),
  ).toEqual(["展开或折叠「Reviewing」", "展开或折叠「Building」"]);

  await page.reload();
  await page.getByRole("tab", { name: type.name, exact: true }).click();
  await expectAbove(page, later, early);
  await display.click();
  await expect(
    page.getByRole("combobox", { name: "任务排序方向", exact: true }),
  ).toHaveValue("desc");
  await expect(
    page.getByRole("combobox", { name: "状态分组顺序", exact: true }),
  ).toHaveValue("reverse");
  await display.click();
  for (const layout of ["看板", "表格", "甘特图"]) {
    await display.click();
    await page.getByRole("radio", { name: layout, exact: true }).click();
    await display.click();
    await expectAbove(page, later, early);
  }
  await display.click();
  await page
    .getByRole("combobox", { name: "排序", exact: true })
    .selectOption("manual");
  await expect(
    page.getByRole("combobox", { name: "任务排序方向", exact: true }),
  ).toBeDisabled();
  await expect(
    page.getByRole("combobox", { name: "任务排序方向", exact: true }),
  ).toHaveValue("asc");
  await display.click();

  const view = await request(page, `${base}/spaces/${space}/views`, "POST", {
    name: `T01 shared ${suffix}`,
    visibility: "space",
    filters: { type_id: [type.id] },
    display: { layout: "list", sort: "created", sortDirection: "asc" },
  });
  await page.goto(`/o/${org}/spaces/${space}/views/${view.id}/edit`);
  await display.click();
  await page
    .getByRole("combobox", { name: "任务排序方向", exact: true })
    .selectOption("desc");
  await page
    .getByRole("combobox", { name: "状态分组顺序", exact: true })
    .selectOption("reverse");
  await display.click();
  const saved = page.waitForResponse(
    (response) =>
      response.url().endsWith(`/views/${view.id}`) &&
      response.request().method() === "PATCH",
  );
  await page.getByRole("button", { name: "保存", exact: true }).click();
  expect((await saved).status()).toBe(200);
  const stored = await request(page, `${base}/views/${view.id}`);
  expect(stored.display.sortDirection).toBe("desc");
  expect(stored.display.groupOrder.status).toBe("reverse");
  await page.goto(`/o/${org}/spaces/${space}/views/${view.id}`);
  await expectAbove(page, later, early);
  await page.reload();
  await expectAbove(page, later, early);
});

test("C01 quotes user and Agent messages through the real API and preserves bounded snapshots on reload", async ({
  page,
}) => {
  await login(page);
  for (const [conversation, source, author] of [
    [
      "00000000-0000-0000-0000-000000003c01",
      "00000000-0000-0000-0000-000000003c11",
      "USER",
    ],
    [
      "00000000-0000-0000-0000-000000003c02",
      "00000000-0000-0000-0000-000000003c12",
      "AGENT",
    ],
  ] as const) {
    await page.goto(`/o/${org}/chat/${conversation}`);
    const input = page.getByRole("textbox", { name: "输入消息", exact: true });
    await input.fill(`C01 ${author} browser reply`);
    await page
      .getByRole("button", { name: "引用回复", exact: true })
      .first()
      .click();
    const preview = page.getByLabel("引用预览", { exact: true });
    await expect(preview).toContainText("中🦊".repeat(100) + "…");
    await expect(preview).not.toContainText("中🦊".repeat(101));
    await page.getByRole("button", { name: "取消引用", exact: true }).click();
    await expect(preview).toHaveCount(0);
    await expect(input).toHaveValue(`C01 ${author} browser reply`);
    await page
      .getByRole("button", { name: "引用回复", exact: true })
      .first()
      .click();
    const sent = page.waitForResponse(
      (response) =>
        response.url().endsWith(`/conversations/${conversation}/messages`) &&
        response.request().method() === "POST",
    );
    await input.press("Enter");
    const response = await sent;
    expect(response.status()).toBe(201);
    const posted = response.request().postDataJSON();
    expect(posted.reply_to_id).toBe(source);
    expect(posted).not.toHaveProperty("reply_snapshot");
    const message = await response.json();
    expect(message.reply.snapshot).toBe(true);
    expect(message.reply.author_kind).toBe(author);
    expect(message.reply.body).toBe("中🦊".repeat(1000));
    expect(message.reply.truncated).toBe(true);
    await expect(input).toHaveValue("");
    await expect(
      page.getByRole("button", { name: "取消引用", exact: true }),
    ).toHaveCount(0);
    await page.reload();
    await expect(preview).toHaveCount(1);
    await expect(preview).toContainText("中🦊".repeat(100) + "…");
    const persisted = await request(
      page,
      `${base}/conversations/${conversation}/messages`,
    );
    const stored = persisted.messages.find(
      (item: { id: string }) => item.id === message.id,
    );
    expect(stored.reply).toEqual(message.reply);
    expect(stored.body).toBe(`C01 ${author} browser reply`);
  }
});
