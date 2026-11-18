import { readFileSync, writeFileSync } from "node:fs";
import { expect, test, type Page } from "@playwright/test";
const path = process.env.OPENFOX_GITCODE_BROWSER_FILE;
test.skip(!path, "requires isolated GitCode TLS harness");
const f = path ? JSON.parse(readFileSync(path, "utf8")) : {};
async function req(page: Page, url: string, method = "GET", data?: unknown) {
  const csrf = (await page.context().cookies()).find(
    (c) => c.name === "openfox_csrf",
  )?.value;
  const response = await page.request.fetch(url, {
    method,
    data,
    headers: {
      ...(csrf ? { "X-CSRF-Token": csrf } : {}),
      "Idempotency-Key": crypto.randomUUID(),
    },
  });
  expect(
    response.ok(),
    `${method} ${url}: ${response.status()} ${await response.text()}`,
  ).toBeTruthy();
  return response.status() === 204 ? null : response.json();
}
test("I09 organization PAT, stable repository, history, UNKNOWN reconciliation, rotation and cleanup", async ({
  page,
  browser,
}, info) => {
  test.setTimeout(110_000);
  const api = `/api/v1/organizations/${f.organization_id}`,
    failures: string[] = [],
    requests: string[] = [];
  page.on("response", (r) => {
    if (r.url().includes("/api/")) {
      requests.push(
        `${r.request().method()} ${new URL(r.url()).pathname} ${r.status()}`,
      );
      if (r.status() >= 400)
        failures.push(`${r.status()} ${new URL(r.url()).pathname}`);
    }
  });
  await req(page, "/api/v1/auth/login", "POST", {
    email: f.email,
    password: f.password,
    client_kind: "web",
  });
  await page.goto(`/o/${f.organization_id}/settings/integrations/gitcode`);
  await page.evaluate(
    (id) =>
      localStorage.setItem(
        `openfox:onboarding:${id}`,
        JSON.stringify({ step: "done" }),
      ),
    f.organization_id,
  );
  await page.getByRole("button", { name: "连接组织", exact: true }).click();
  let dialog = page.getByRole("dialog");
  await dialog.getByLabel("组织管理员 PAT").fill("fixture-gitcode-token");
  await dialog.getByRole("button", { name: "验证 PAT 并选择组织" }).click();
  await dialog.getByLabel("远端组织").click();
  await page.getByRole("option", { name: "Team (73)" }).click();
  const binding = page.waitForResponse(
    (r) =>
      r.url().endsWith("/gitcode/connections") &&
      r.request().method() === "POST",
  );
  await dialog.getByRole("button", { name: "连接组织", exact: true }).click();
  const bound = await binding;
  expect(bound.ok()).toBeTruthy();
  const connection = (await bound.json()).id;
  const configURL = `${api}/integrations/connections/${connection}/gitcode/configuration`;
  await page.getByRole("button", { name: "添加仓库", exact: true }).click();
  dialog = page.getByRole("dialog");
  await dialog.getByLabel("仓库", { exact: true }).click();
  await page
    .getByRole("option", { name: "team/repo · private", exact: true })
    .click();
  await dialog.getByLabel("目标空间").click();
  await page.getByRole("option", { name: f.space_name, exact: true }).click();
  await expect(
    dialog.getByRole("checkbox", { name: "双向同步", exact: true }),
  ).not.toBeChecked();
  await dialog
    .getByRole("checkbox", { name: "导入已有 Issue", exact: true })
    .check();
  await dialog.getByRole("button", { name: "添加并配置 Webhook" }).click();
  await expect(dialog).toHaveCount(0);
  let upstream = await req(page, `${f.api_url}/fixture/gitcode`, "POST", {
    runs: 10,
  });
  expect(upstream.hook_posts).toBe(1);
  await page.getByRole("button", { name: "刷新", exact: true }).click();
  await expect(
    page.getByText("同步：ACTIVE · Hook：ACTIVE", { exact: false }),
  ).toBeVisible();
  const cfg = await req(page, configURL);
  expect(cfg.repositories[0].issue_write_status).toBe("UNVERIFIED");
  expect(cfg.links[0].backfill.status).toBe("done");
  expect(cfg.links[0].backfill.imported).toBe(1);
  await page.screenshot({
    path: info.outputPath("history-ready.png"),
    fullPage: true,
  });
  await page.getByRole("button", { name: "改变同步方向" }).click();
  dialog = page.getByRole("dialog", { name: "启用双向同步？" });
  await dialog.getByRole("button", { name: "确认", exact: true }).click();
  await expect(dialog).toHaveCount(0);
  await req(page, `${f.api_url}/fixture/gitcode`, "POST", {
    unknown: true,
    runs: 0,
  });
  const task = await req(page, `${api}/spaces/${f.space_id}/tasks`, "POST", {
    title: "GitCode browser outgoing",
    priority: "NONE",
  });
  upstream = await req(page, `${f.api_url}/fixture/gitcode`, "POST", {
    runs: 4,
  });
  expect(upstream.issue_posts).toBe(1);
  await page.getByRole("button", { name: "刷新", exact: true }).click();
  await expect(page.getByText(/结果未知：只核对/)).toBeVisible();
  await page.screenshot({
    path: info.outputPath("unknown-no-resend.png"),
    fullPage: true,
  });
  await req(page, `${f.api_url}/fixture/gitcode`, "POST", {
    login: "renamed",
    unknown: false,
    runs: 0,
  });
  await page.getByRole("button", { name: "更新 PAT", exact: true }).click();
  dialog = page.getByRole("dialog");
  await dialog.getByLabel("组织管理员 PAT").fill("fixture-gitcode-token");
  await dialog.getByRole("button", { name: "验证并更新 PAT" }).click();
  await expect(dialog).toHaveCount(0);
  await page.getByRole("button", { name: "重新核对", exact: true }).click();
  upstream = await req(page, `${f.api_url}/fixture/gitcode`, "POST", {
    runs: 6,
  });
  expect(upstream.issue_posts).toBe(1);
  const linked = await req(
    page,
    `${api}/spaces/${f.space_id}/tasks/${task.id}/git-links`,
  );
  expect(linked.links.some((l: { kind: string }) => l.kind === "ISSUE")).toBe(
    true,
  );
  await page.getByRole("button", { name: "刷新", exact: true }).click();
  await expect(page.getByText(/结果未知：只核对/)).toHaveCount(0);
  await page.getByRole("button", { name: "轮换回调密钥" }).click();
  dialog = page.getByRole("dialog", { name: "轮换此连接全部 Webhook？" });
  await dialog.getByRole("button", { name: "确认", exact: true }).click();
  await expect(dialog).toHaveCount(0);
  upstream = await req(page, `${f.api_url}/fixture/gitcode`, "POST", {
    runs: 4,
  });
  expect(upstream.hook_posts).toBe(1);
  expect(upstream.hook_patches).toBe(1);
  await page.getByRole("button", { name: "刷新", exact: true }).click();
  await expect(page.getByText(/Webhook 曾中断/)).toBeVisible();
  await page.screenshot({
    path: info.outputPath("rotation-ready.png"),
    fullPage: true,
  });
  await page.getByRole("button", { name: "移除同步", exact: true }).click();
  dialog = page.getByRole("dialog", { name: "移除此仓库同步？" });
  await dialog
    .getByRole("checkbox", { name: "同时删除 OpenFox 创建的远端 Hook" })
    .check();
  await dialog.getByRole("button", { name: "确认", exact: true }).click();
  await expect(dialog).toHaveCount(0);
  upstream = await req(page, `${f.api_url}/fixture/gitcode`, "POST", {
    runs: 4,
  });
  expect(upstream.hook_deletes).toBe(1);
  await page.getByRole("button", { name: "刷新", exact: true }).click();
  await expect(page.getByText(/Hook：DELETED/)).toBeVisible();
  await page.getByRole("button", { name: "断开", exact: true }).click();
  dialog = page.getByRole("dialog");
  await dialog.getByRole("textbox").fill("断开 GitCode / AtomGit");
  await dialog.getByRole("button", { name: "断开", exact: true }).click();
  await expect(dialog).toHaveCount(0);
  await expect(page.getByText(/连接已断开，凭据已销毁/)).toBeVisible();
  await page.screenshot({
    path: info.outputPath("disconnected-cleanup.png"),
    fullPage: true,
  });
  const origin = new URL(page.url()).origin,
    context = await browser.newContext({ locale: "zh-CN" }),
    member = await context.newPage();
  await req(member, `${origin}/api/v1/auth/login`, "POST", {
    email: f.member_email,
    password: f.password,
    client_kind: "web",
  });
  await member.goto(
    `${origin}/o/${f.organization_id}/settings/integrations/gitcode`,
  );
  await expect(
    member.getByRole("switch", { name: "关联 Pull Request", exact: true }),
  ).toBeDisabled();
  await expect(member.getByRole("button", { name: "更新 PAT" })).toHaveCount(0);
  await context.close();
  writeFileSync(
    info.outputPath("http-summary.json"),
    JSON.stringify(
      { browser: requests, upstream: upstream.requests, failures },
      null,
      2,
    ),
  );
  expect(failures).toEqual([]);
});
