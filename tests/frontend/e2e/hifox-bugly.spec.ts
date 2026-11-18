import { test, expect, type Page } from "@playwright/test";
import fs from "node:fs";

const fixture = process.env.OPENFOX_BUGLY_BROWSER_FILE;
test.skip(
  !fixture,
  "requires the exclusive I07 PostgreSQL and actual local API fixture",
);

async function confirm(page: Page, label: string) {
  await page.getByRole("button", { name: label, exact: true }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("button", { name: label, exact: true }).click();
  await expect(dialog).toHaveCount(0);
}

test("I07 explicit inbound configuration, real creation, duplicate and rotation", async ({
  page,
}) => {
  const f = JSON.parse(fs.readFileSync(fixture!, "utf8"));
  const login = await page.request.post("/api/v1/auth/login", {
    data: { email: f.email, password: f.password, client_kind: "web" },
  });
  expect(login.status()).toBe(201);
  const settings = `/o/${f.organization_id}/settings/integrations/bugly`;
  await page.goto(settings);
  await page.evaluate(
    (org) =>
      localStorage.setItem(
        `openfox:onboarding:${org}`,
        JSON.stringify({ step: "done" }),
      ),
    f.organization_id,
  );
  await page.reload();
  await page.getByRole("button", { name: "创建入站连接", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "启用接收", exact: true }),
  ).toBeVisible();
  await expect(page.getByLabel("秘密 URL", { exact: true })).toHaveCount(0);
  await page.getByRole("button", { name: "绑定产品", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await dialog
    .getByRole("textbox", { name: "ProductID", exact: true })
    .fill("BrowserProduct-01");
  await dialog
    .getByLabel("项目显示名", { exact: true })
    .fill("受控 Bugly 浏览器产品");
  await dialog.getByRole("button", { name: "目标空间", exact: true }).click();
  await page.getByRole("option").first().click();
  await dialog
    .getByRole("button", { name: "默认任务类型", exact: true })
    .click();
  await page.getByRole("option").first().click();
  await dialog
    .getByRole("button", { name: "添加优先级映射", exact: true })
    .click();
  await dialog.getByLabel("外部优先级 1", { exact: true }).fill("urgent");
  await dialog.getByLabel("OpenFox 优先级 1", { exact: true }).click();
  await page.getByRole("option", { name: "高", exact: true }).click();
  await dialog.getByRole("button", { name: "保存", exact: true }).click();
  await expect(dialog).toHaveCount(0);
  await confirm(page, "启用接收");
  await page.getByRole("button", { name: "显示秘密 URL", exact: true }).click();
  const input = page.getByLabel("秘密 URL", { exact: true });
  await expect(input).toBeVisible();
  const secretURL = await input.inputValue();
  await page.getByRole("button", { name: "隐藏秘密 URL", exact: true }).click();
  const secretPath = new URL(secretURL).pathname;
  const payload = {
    ProductID: "BrowserProduct-01",
    IssueKey: "BrowserIssue-001",
    IssueType: "crash",
    FieldValues: {
      title: "Bugly 浏览器实际提单",
      description:
        '<script>window.__BUGLY_EXECUTED = true</script> BROWSER_LITERAL <img src="https://external.example/image?token=CANARY_SECRET">',
      priority: "urgent",
      workspace_id: "untrusted",
      executor_agent_id: "untrusted",
    },
  };
  const received = await page.request.post(secretPath, { data: payload });
  expect(received.status()).toBe(200);
  const receipt = await received.json();
  expect(receipt.code).toBe(0);
  expect(new URL(receipt.url).pathname).toContain(f.space_id);
  const duplicate = await page.request.post(secretPath, {
    data: {
      ...payload,
      FieldValues: { ...payload.FieldValues, title: "Must not overwrite" },
    },
  });
  expect(duplicate.status()).toBe(200);
  expect((await duplicate.json()).url).toBe(receipt.url);
  await page.getByRole("button", { name: "刷新", exact: true }).click();
  await expect(page.getByText("已创建任务", { exact: true })).toBeVisible();
  await expect(
    page.getByText("重复来源，返回原任务", { exact: true }),
  ).toBeVisible();
  await page.reload();
  await expect(
    page.getByText("受控 Bugly 浏览器产品", { exact: true }),
  ).toBeVisible();
  await expect(page.getByLabel("秘密 URL", { exact: true })).toHaveCount(0);
  if (process.env.OPENFOX_BUGLY_SCREENSHOT)
    await page.screenshot({
      path: process.env.OPENFOX_BUGLY_SCREENSHOT,
      fullPage: true,
    });

  const remoteLoads: string[] = [];
  page.on("request", (request) => {
    if (new URL(request.url()).hostname === "external.example")
      remoteLoads.push("external.example");
  });
  await page.goto(receipt.url);
  await expect(
    page.getByText("Bugly 浏览器实际提单", { exact: true }).first(),
  ).toBeVisible();
  const description = page.getByRole("textbox", {
    name: "任务描述",
    exact: true,
  });
  await expect(description).toHaveValue(
    /<script>window\.__BUGLY_EXECUTED = true<\/script> BROWSER_LITERAL/,
  );
  await expect(description).not.toHaveValue(/CANARY_SECRET/);
  await expect(description.locator("script, img")).toHaveCount(0);
  expect(await page.evaluate(() => "__BUGLY_EXECUTED" in window)).toBe(false);
  expect(remoteLoads).toEqual([]);
  await expect(page.getByText("CANARY_SECRET", { exact: false })).toHaveCount(
    0,
  );

  await page.goto(settings);
  await confirm(page, "暂停");
  const paused = await page.request.post(secretPath, {
    data: { ...payload, IssueKey: "PausedIssue" },
  });
  expect(paused.status()).toBe(409);
  await confirm(page, "恢复");
  await confirm(page, "轮换秘密 URL");
  await expect(page.getByLabel("秘密 URL", { exact: true })).toHaveCount(0);
  const old = await page.request.post(secretPath, { data: payload });
  expect(old.status()).toBe(404);
  const tasks = await page.request.get(
    `/api/v1/organizations/${f.organization_id}/spaces/${f.space_id}/tasks`,
  );
  expect(tasks.ok()).toBeTruthy();
  const text = JSON.stringify(await tasks.json());
  expect(text).toContain("Bugly 浏览器实际提单");
  expect(text).not.toContain("Must not overwrite");
});
