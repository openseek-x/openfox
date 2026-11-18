import { expect, test, type Page } from "@playwright/test";

test.skip(
  process.env.OPENFOX_HIFOX_V04_E2E !== "1",
  "requires isolated fixtures",
);
const org = "00000000-0000-0000-0000-0000000000a1";
const base = `/api/v1/organizations/${org}`;
const titles = ["V04 zero", "V04 positive", "V04 unestimated"];

async function login(page: Page, email: string) {
  await page.goto("/login");
  await page.getByLabel("邮箱", { exact: true }).fill(email);
  await page.getByRole("button", { name: "继续", exact: true }).click();
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

async function api(page: Page, path: string, method = "GET", data?: unknown) {
  const csrf = (await page.context().cookies()).find(
    (c) => c.name === "openfox_csrf",
  )?.value;
  const response = await page.request.fetch(path, {
    method,
    data,
    headers: csrf ? { "X-CSRF-Token": csrf } : {},
  });
  expect(
    response.ok(),
    `${method} ${path}: ${response.status()} ${await response.text()}`,
  ).toBeTruthy();
  return response.status() === 204 ? null : response.json();
}

async function visibleTasks(page: Page, expected: string[]) {
  for (const title of titles) {
    const text = page.getByText(title, { exact: true });
    if (expected.includes(title)) await expect(text.first()).toBeVisible();
    else await expect(text).toHaveCount(0);
  }
}

async function expectOrder(page: Page, expected: string[]) {
  // Gantt repeats each title inside its visual bar; compare the task rows only.
  await expect
    .poll(() =>
      page
        .getByText(/^V04 (zero|positive|unestimated)$/)
        .evaluateAll((elements) =>
          elements
            .filter(
              (element) => !element.closest('[data-testid^="gantt-bar-"]'),
            )
            .map((element) => element.textContent),
        ),
    )
    .toEqual(expected);
}

async function display(page: Page, layout: string, order: "asc" | "desc") {
  const button = page.getByRole("button", { name: "显示设置", exact: true });
  await button.click();
  await page.getByRole("radio", { name: layout, exact: true }).click();
  await page
    .getByRole("combobox", { name: "排序", exact: true })
    .selectOption("estimate");
  await page
    .getByRole("combobox", { name: "任务排序方向", exact: true })
    .selectOption(order);
  const property = page.getByRole("button", { name: "估时", exact: true });
  if ((await property.getAttribute("aria-pressed")) !== "true")
    await property.click();
  await button.click();
}

async function filter(page: Page, none: boolean) {
  await page.getByRole("button", { name: "筛选", exact: true }).click();
  await page.getByRole("menuitem", { name: "估时", exact: true }).click();
  if (none)
    await page.getByRole("radio", { name: "未估时", exact: true }).click();
  else {
    await page.getByLabel("估时 从", { exact: true }).fill("0");
    await page.getByLabel("估时 至", { exact: true }).fill("0");
  }
  await page.getByRole("button", { name: "应用", exact: true }).click();
  await visibleTasks(page, [none ? titles[2]! : titles[0]!]);
  await expect(page.getByText("1 个匹配任务", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "清除筛选", exact: true }).click();
  await visibleTasks(page, titles);
}

test("V04 zero/null, four layouts, disabled estimates and legacy shared defaults", async ({
  page,
  browser,
}) => {
  await login(page, "alice@example.test");
  const space = await api(page, `${base}/spaces`, "POST", {
    name: "V04 estimate regression",
    visibility: "private",
    copy_from_space_id: "00000000-0000-0000-0000-0000000000aa",
  });
  const spacePath = `${base}/spaces/${space.id}`;
  const taskPage = `/o/${org}/spaces/${space.id}/tasks`;
  const settingsPage = `/o/${org}/settings/spaces/${space.id}/estimates`;
  await api(
    page,
    `${spacePath}/members/00000000-0000-0000-0000-0000000000c3`,
    "PUT",
    { role: "MEMBER", status: "ACTIVE" },
  );
  // Enable through the actual settings UI, then enable the scale's explicit zero option.
  await page.goto(settingsPage);
  await page.getByRole("button", { name: "估时刻度", exact: true }).click();
  await page.getByRole("menuitemradio", { name: /^线性/ }).click();
  const zeroSwitch = page.getByRole("switch", {
    name: "允许零估时",
    exact: true,
  });
  await expect(zeroSwitch).toBeEnabled();
  await zeroSwitch.click();
  await expect(zeroSwitch).toBeChecked();
  await expect
    .poll(async () => (await api(page, spacePath)).estimation.allow_zero)
    .toBe(true);
  const types = await api(page, `${spacePath}/task-types`);
  const created = [];
  for (const [index, estimate] of [0, 3, null].entries()) {
    created.push(
      await api(page, `${spacePath}/tasks`, "POST", {
        title: titles[index],
        type_id: types.task_types[0].id,
        estimate,
        start_date: "2026-11-13",
        due_date: "2026-11-14",
      }),
    );
  }
  await page.goto(taskPage);
  for (const layout of ["列表", "看板", "表格", "甘特图"]) {
    await display(page, layout, "asc");
    await visibleTasks(page, titles);
    await expectOrder(page, titles);
    if (layout === "列表" || layout === "看板") {
      const container = layout === "列表" ? "li" : "article";
      const row = (title: string) =>
        page
          .locator(container)
          .filter({ has: page.getByText(title, { exact: true }) });
      await expect(
        row(titles[0]!).getByTitle("估时", { exact: true }),
      ).toHaveText("0");
      await expect(
        row(titles[2]!).getByTitle("估时", { exact: true }),
      ).toHaveCount(0);
    }
    if (layout === "表格") {
      await expect(
        page.getByRole("columnheader", { name: "估时", exact: true }),
      ).toBeVisible();
      await expect(
        page
          .getByRole("row")
          .filter({ hasText: titles[0] })
          .getByRole("cell", { name: "0", exact: true }),
      ).toBeVisible();
      await expect(
        page
          .getByRole("row")
          .filter({ hasText: titles[2] })
          .getByRole("cell", { name: "—", exact: true })
          .last(),
      ).toBeVisible();
    }
    await display(page, layout, "desc");
    await expectOrder(page, [titles[1]!, titles[0]!, titles[2]!]);
    await page.reload();
    await expectOrder(page, [titles[1]!, titles[0]!, titles[2]!]);
    await filter(page, false);
    await filter(page, true);
  }
  const view = await api(page, `${spacePath}/views`, "POST", {
    name: "V04 legacy zero view",
    visibility: "space",
    filters: { estimate_min: ["0"], estimate_max: ["0"] },
    display: { layout: "list", sort: "estimate", properties: ["estimate"] },
  });
  let current = await api(page, spacePath);
  await api(page, spacePath, "PATCH", {
    version: current.version,
    default_view_id: view.id,
  });
  const context = await browser.newContext({
    baseURL: page.url().split("/o/")[0],
    locale: "zh-CN",
  });
  try {
    const member = await context.newPage();
    await login(member, "carol@example.test");
    await member.goto(taskPage);
    await expect(
      member.getByText("空间默认：V04 legacy zero view", { exact: true }),
    ).toBeVisible();
    await visibleTasks(member, [titles[0]!]);
    await expect(member.getByTitle("估时", { exact: true })).toHaveText("0");
    await member.goto(settingsPage);
    await expect(
      member.getByRole("button", { name: "估时刻度", exact: true }),
    ).toBeDisabled();
    await page.goto(settingsPage);
    await page.getByRole("button", { name: "估时刻度", exact: true }).click();
    await page
      .getByRole("menuitemradio", { name: "未启用", exact: true })
      .click();
    await expect
      .poll(async () => (await api(page, spacePath)).estimation.scale)
      .toBe("notUsed");
    for (const route of [
      taskPage,
      `/o/${org}/spaces/${space.id}/views/${view.id}`,
    ]) {
      await member.goto(route);
      await visibleTasks(member, [titles[0]!]);
      await expect(member.getByTitle("估时", { exact: true })).toHaveCount(0);
      await member
        .getByRole("button", { name: "显示设置", exact: true })
        .click();
      await expect(
        member
          .getByRole("combobox", { name: "排序", exact: true })
          .locator('option[value="estimate"]'),
      ).toHaveCount(0);
      await expect(
        member.getByRole("button", { name: "估时", exact: true }),
      ).toHaveCount(0);
      await member
        .getByRole("button", { name: "显示设置", exact: true })
        .click();
      await member.getByRole("button", { name: "筛选", exact: true }).click();
      await expect(
        member.getByRole("menuitem", { name: "估时", exact: true }),
      ).toHaveCount(0);
      await member.keyboard.press("Escape");
      await member.reload();
      await visibleTasks(member, [titles[0]!]);
    }
    expect((await api(page, `${base}/views/${view.id}`)).display).toEqual(
      view.display,
    );
    for (const [i, task] of created.entries())
      expect((await api(page, `${spacePath}/tasks/${task.id}`)).estimate).toBe(
        [0, 3, null][i],
      );
    // Restore the same explicit scale options through the API; no conversion is requested.
    current = await api(page, spacePath);
    await api(page, `${spacePath}/estimation`, "PATCH", {
      version: current.version,
      scale: "linear",
      allow_zero: true,
    });
    await member.goto(taskPage);
    await visibleTasks(member, [titles[0]!]);
    await expect(member.getByTitle("估时", { exact: true })).toHaveText("0");
  } finally {
    await context.close();
  }
});
