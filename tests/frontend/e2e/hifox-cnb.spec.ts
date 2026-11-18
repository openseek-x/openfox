import { readFileSync } from "node:fs";
import { expect, test, type Page } from "@playwright/test";
const path = process.env.OPENFOX_CNB_BROWSER_FILE;
test.skip(!path, "requires isolated CNB TLS harness");
const f = path ? JSON.parse(readFileSync(path, "utf8")) : {};
const base = `/api/v1/organizations/${f.organization_id}`;
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
test("I03 CNB opt-in persists, signed events link PR and commit, polling closes PR and member sees no secret", async ({
  page,
  browser,
}) => {
  await req(page, "/api/v1/auth/login", "POST", {
    email: f.email,
    password: f.password,
    client_kind: "web",
  });
  await page.goto(`/o/${f.organization_id}/settings/integrations/cnb`);
  await page.evaluate(
    (id) =>
      localStorage.setItem(
        `openfox:onboarding:${id}`,
        JSON.stringify({ step: "done" }),
      ),
    f.organization_id,
  );
  await expect(
    page.getByText("已选择 0 个仓库", { exact: false }),
  ).toBeVisible();
  await page.getByRole("button", { name: "读取可访问仓库" }).click();
  await page
    .getByRole("checkbox", { name: "team/nested/repo", exact: true })
    .check();
  await page.getByRole("button", { name: "保存仓库选择" }).click();
  await expect
    .poll(
      async () =>
        (
          await req(
            page,
            `${base}/integrations/connections/${f.connection_id}/cnb/repositories`,
          )
        ).repositories.length,
    )
    .toBe(1);
  await page.reload();
  await expect(
    page.getByRole("checkbox", { name: "team/nested/repo", exact: true }),
  ).toBeChecked();
  await page.getByRole("button", { name: "查看事件桥接配置" }).click();
  await page
    .getByRole("button", { name: "生成桥接地址与密钥", exact: true })
    .click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "确认", exact: true })
    .click();
  await expect(
    page.getByRole("textbox", { name: "CNB 事件接收地址" }),
  ).toHaveValue(/\/hooks\/git\/cnb\//);
  await expect(page.getByLabel("CNB 桥接 YAML 示例")).toContainText(
    "pull_request.target",
  );
  await expect(page.getByLabel("CNB 桥接 YAML 示例")).toContainText(
    "signature_secret",
  );
  const event = await page.request.post(`${f.api_url}/fixture/cnb`, {
    data: { build: "browser-pr" },
  });
  expect(event.ok()).toBeTruthy();
  expect((await event.json()).posts).toBe(1);
  await page.getByRole("button", { name: "刷新核对状态" }).click();
  await expect(page.getByText("回链： 已发送")).toBeVisible();
  let links = (
    await req(page, `${base}/spaces/${f.space_id}/tasks/${f.task_id}/git-links`)
  ).links;
  expect(links).toHaveLength(1);
  expect(links[0].state).toBe("OPEN");
  expect(links[0].url).toBe("https://cnb.cool/team/nested/repo/-/pulls/11");
  await page
    .getByRole("switch", { name: "关联 CNB Git Commit", exact: true })
    .click();
  await expect
    .poll(
      async () =>
        (await req(page, `${base}/integrations/cnb/settings`)).settings
          .link_commits,
    )
    .toBe(true);
  expect(
    (
      await page.request.post(`${f.api_url}/fixture/cnb`, {
        data: { build: "browser-commit", push: true },
      })
    ).ok(),
  ).toBeTruthy();
  links = (
    await req(page, `${base}/spaces/${f.space_id}/tasks/${f.task_id}/git-links`)
  ).links;
  expect(links.some((l: { kind: string }) => l.kind === "COMMIT")).toBe(true);
  const close = await page.request.post(`${f.api_url}/fixture/cnb`, {
    data: { state: "closed" },
  });
  expect(close.ok()).toBeTruthy();
  expect((await close.json()).posts).toBe(1);
  links = (
    await req(page, `${base}/spaces/${f.space_id}/tasks/${f.task_id}/git-links`)
  ).links;
  expect(
    links.find((l: { kind: string }) => l.kind === "PULL_REQUEST").state,
  ).toBe("CLOSED");
  await page.getByRole("button", { name: "刷新核对状态" }).click();
  await expect(page.getByText("下次核对： 已结束")).toBeVisible();
  await page
    .getByRole("checkbox", { name: "team/nested/repo", exact: true })
    .click();
  await page.getByRole("button", { name: "保存仓库选择" }).click();
  await expect
    .poll(
      async () =>
        (
          await req(
            page,
            `${base}/integrations/connections/${f.connection_id}/cnb/repositories`,
          )
        ).repositories.length,
    )
    .toBe(0);
  await page.reload();
  await expect(
    page.getByText("已选择 0 个仓库", { exact: false }),
  ).toBeVisible();
  expect(
    (
      await req(
        page,
        `${base}/spaces/${f.space_id}/tasks/${f.task_id}/git-links`,
      )
    ).links,
  ).toHaveLength(2);
  const context = await browser.newContext({ locale: "zh-CN" });
  const member = await context.newPage();
  await req(member, "http://127.0.0.1:4188/api/v1/auth/login", "POST", {
    email: f.member_email,
    password: f.password,
    client_kind: "web",
  });
  await member.goto(
    `http://127.0.0.1:4188/o/${f.organization_id}/settings/integrations/cnb`,
  );
  await expect(
    member.getByRole("switch", { name: "关联 CNB Pull Request", exact: true }),
  ).toBeDisabled();
  await expect(
    member.getByRole("button", { name: "查看事件桥接配置" }),
  ).toHaveCount(0);
  expect(
    (
      await member.request.get(
        `http://127.0.0.1:4188${base}/integrations/connections/${f.connection_id}/webhook`,
      )
    ).status(),
  ).toBe(403);
  await context.close();
});
