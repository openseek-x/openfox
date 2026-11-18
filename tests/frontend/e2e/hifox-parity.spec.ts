import { expect, test, type Page } from "@playwright/test";

// Real browser + HTTP + disposable PostgreSQL; no route mocks or external messages.
const org = "00000000-0000-0000-0000-0000000000a1";
const space = "00000000-0000-0000-0000-0000000000aa";
const taskType = "00000000-0000-0000-0000-000000000111";

async function login(page: Page) {
  await page.goto("/login");
  await page.getByLabel("邮箱", { exact: true }).fill("alice@example.test");
  await page.getByRole("button", { name: "继续" }).click();
  await page
    .getByLabel("第 1 位")
    .pressSequentially(process.env.OPENFOX_LIVE_OTP_CODE!);
  await page.waitForURL(/\/o\//);
  await page.evaluate(
    (key) => localStorage.setItem(key, JSON.stringify({ step: "done" })),
    `openfox:onboarding:${org}`,
  );
}

test("S01/S05 theme reset and task type icon persist through real API", async ({
  page,
  browser,
}) => {
  test.skip(
    !process.env.OPENFOX_LIVE_OTP_CODE,
    "Requires an isolated live-test server and disposable database",
  );
  await login(page);
  const preferences = `/o/${org}/settings/preferences`;
  await page.goto(preferences);
  await page.getByRole("radio", { name: "深色", exact: true }).click();
  await expect(
    page.getByRole("radio", { name: "深色", exact: true }),
  ).toHaveAttribute("aria-checked", "true");
  await expect(
    page.getByRole("radio", { name: "内置外边框颜色 3" }),
  ).toBeEnabled();
  await page.getByRole("radio", { name: "内置外边框颜色 3" }).click();
  await expect(page.getByRole("button", { name: "密度" })).toBeEnabled();
  await page.getByRole("button", { name: "密度" }).click();
  await page.getByRole("option", { name: "紧凑" }).click();
  await expect(
    page.getByRole("button", { name: "恢复主题默认" }),
  ).toBeEnabled();
  const saved = page.waitForResponse(
    (r) =>
      r.url().endsWith("/me/preferences") && r.request().method() === "PATCH",
  );
  await page.getByRole("button", { name: "恢复主题默认" }).click();
  expect((await saved).status()).toBe(200);
  await page.reload();
  await expect(page.getByRole("radio", { name: "跟随系统" })).toHaveAttribute(
    "aria-checked",
    "true",
  );
  await expect(page.getByRole("radio", { name: "不设置" })).toHaveAttribute(
    "aria-checked",
    "true",
  );
  await expect(page.getByRole("button", { name: "密度" })).toHaveText("紧凑");
  const second = await browser.newContext({ locale: "zh-CN" });
  try {
    const other = await second.newPage();
    await login(other);
    await other.goto(preferences);
    await expect(
      other.getByRole("radio", { name: "跟随系统" }),
    ).toHaveAttribute("aria-checked", "true");
    await expect(other.getByRole("button", { name: "密度" })).toHaveText(
      "紧凑",
    );
  } finally {
    await second.close();
  }

  await page.goto(`/o/${org}/settings/spaces/${space}/task-types/${taskType}`);
  await page.getByRole("button", { name: "选择任务类型图标" }).click();
  let dialog = page.getByRole("dialog", { name: "选择任务类型图标" });
  await dialog.getByRole("button", { name: "bug", exact: true }).click();
  await dialog.getByRole("button", { name: "取消" }).click();
  await page.getByRole("button", { name: "选择任务类型图标" }).click();
  dialog = page.getByRole("dialog", { name: "选择任务类型图标" });
  await expect(
    dialog.getByRole("button", { name: "bug", exact: true }),
  ).toHaveAttribute("aria-pressed", "false");
  await dialog.getByRole("button", { name: "bug", exact: true }).click();
  const iconSaved = page.waitForResponse(
    (r) =>
      r.url().endsWith(`/task-types/${taskType}`) &&
      r.request().method() === "PATCH",
  );
  await dialog.getByRole("button", { name: "保存" }).click();
  expect((await iconSaved).status()).toBe(200);
  await expect(dialog).not.toBeVisible();
  await page.reload();
  await page.getByRole("button", { name: "选择任务类型图标" }).click();
  await expect(
    page.getByRole("dialog").getByRole("button", { name: "bug", exact: true }),
  ).toHaveAttribute("aria-pressed", "true");
});
