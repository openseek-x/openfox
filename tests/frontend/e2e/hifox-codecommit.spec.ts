import { test, expect, type Page } from "@playwright/test";
import fs from "node:fs";

const fixtureFile = process.env.OPENFOX_I13_BROWSER_FILE;
test.skip(
  !fixtureFile,
  "requires the exclusive I13 PostgreSQL, real API and local AWS TLS fixture",
);

async function json(page: Page, path: string) {
  const response = await page.request.get(path);
  expect(response.status()).toBe(200);
  return response.json();
}

test("I13 IAM, paginated repository selection, durable webhook, PR backlink and rotation", async ({
  page,
}) => {
  test.setTimeout(120_000);
  const fixture = JSON.parse(fs.readFileSync(fixtureFile!, "utf8"));
  const base = `/api/v1/organizations/${fixture.organization_id}`;
  const settings = `/o/${fixture.organization_id}/settings/integrations/codecommit`;
  const secrets: string[] = [];
  const proof: Record<string, unknown> = { mocked_browser_requests: false };
  try {
    const login = await page.request.post("/api/v1/auth/login", {
      data: {
        email: fixture.email,
        password: fixture.password,
        client_kind: "web",
      },
    });
    expect(login.status()).toBe(201);
    await page.goto(settings);
    await page.evaluate(
      (org) =>
        localStorage.setItem(
          `openfox:onboarding:${org}`,
          JSON.stringify({ step: "done" }),
        ),
      fixture.organization_id,
    );
    await page.reload();
    await page
      .getByRole("button", { name: "绑定 AWS 账号与区域", exact: true })
      .click();
    let dialog = page.getByRole("dialog");
    await dialog.getByRole("button", { name: "AWS 区域", exact: true }).click();
    await page.getByRole("option", { name: "us-east-1", exact: true }).click();
    await dialog
      .getByLabel("Access key ID", { exact: true })
      .fill("AKIA1234567890123456");
    await dialog
      .getByLabel("Secret access key", { exact: true })
      .fill("synthetic-secret-value");
    await dialog
      .getByLabel("Session token（可选）", { exact: true })
      .fill("synthetic-session");
    const created = page.waitForResponse(
      (response) =>
        response.url().endsWith("/integrations/codecommit/connections") &&
        response.request().method() === "POST",
    );
    await dialog
      .getByRole("button", { name: "验证并保存", exact: true })
      .click();
    const createdResponse = await created;
    expect(createdResponse.status()).toBe(201);
    expect(createdResponse.headers()["cache-control"]).toBe("no-store");
    const connection = await createdResponse.json();
    const connectionPath = `${base}/integrations/connections/${connection.id}/codecommit`;
    await expect(dialog).toHaveCount(0);
    await expect(page.getByText("凭据已验证", { exact: true })).toBeVisible();
    await page
      .getByRole("button", { name: "选择并启用仓库", exact: true })
      .click();
    dialog = page.getByRole("dialog");
    await dialog.getByRole("button", { name: "下一页", exact: true }).click();
    await dialog.getByRole("button", { name: "仓库", exact: true }).click();
    await expect(
      page.getByRole("option", { name: "Other", exact: true }),
    ).toBeVisible();
    await page.getByRole("option", { name: "Other", exact: true }).click();
    await dialog
      .getByRole("button", { name: "回到第一页", exact: true })
      .click();
    await dialog.getByRole("button", { name: "仓库", exact: true }).click();
    await page.getByRole("option", { name: "Repo", exact: true }).click();
    await expect(
      dialog.getByRole("button", { name: "启用并建立基线", exact: true }),
    ).toBeDisabled();
    await dialog
      .getByRole("checkbox", {
        name: "我确认仅建立初始基线，并按当前 Git 设置处理后续 PR 回链。",
        exact: true,
      })
      .check();
    await dialog
      .getByRole("button", { name: "启用并建立基线", exact: true })
      .click();
    const tokenInput = page.getByLabel("Bearer 令牌", { exact: true });
    await expect(tokenInput).toBeVisible();
    await expect(tokenInput).toHaveAttribute("type", "password");
    const token = await tokenInput.inputValue();
    const hookURL = await page
      .getByLabel("OpenFox 接收地址", { exact: true })
      .inputValue();
    secrets.push(token);
    expect(token.length === 43).toBe(true);
    expect(new URL(hookURL).origin === "http://127.0.0.1:4213").toBe(true);
    const hookPath = new URL(hookURL).pathname;
    await dialog
      .getByRole("button", { name: "关闭", exact: true })
      .last()
      .click();
    await expect(tokenInput).toHaveCount(0);
    await page.reload();
    await expect(
      page.getByRole("heading", { name: "Repo", exact: true }),
    ).toBeVisible();
    await expect(tokenInput).toHaveCount(0);
    const leaked = await page.evaluate((values) => {
      const storage = JSON.stringify({
        local: { ...localStorage },
        session: { ...sessionStorage },
      });
      return values.some((value) => storage.includes(value));
    }, secrets);
    expect(leaked).toBe(false);
    await expect
      .poll(
        async () =>
          (await json(page, `${connectionPath}/configuration`)).repositories[0]
            ?.initialized_at,
        { timeout: 30_000 },
      )
      .toBeTruthy();
    await page.getByRole("button", { name: "刷新", exact: true }).click();
    await expect(page.getByText("基线已建立", { exact: true })).toBeVisible();
    proof.iam_and_pagination = true;
    proof.one_time_token_hidden_after_close_and_reload = true;

    const payload = {
      version: "0",
      id: "i13-browser-pr-7",
      source: "aws.codecommit",
      account: "123456789012",
      region: "us-east-1",
      time: new Date().toISOString(),
      "detail-type": "CodeCommit Pull Request State Change",
      resources: ["arn:aws:codecommit:us-east-1:123456789012:Repo"],
      detail: {
        event: "pullRequestCreated",
        pullRequestId: "7",
        repositoryNames: ["Repo"],
        sourceCommit: "b".repeat(40),
        destinationCommit: "a".repeat(40),
        sourceReference: "refs/heads/feature",
        destinationReference: "refs/heads/main",
      },
    };
    const body = JSON.stringify(payload);
    const received = await page.request.post(hookPath, {
      data: body,
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${token}`,
      },
    });
    expect(received.status()).toBe(202);
    expect(await received.json()).toEqual({ accepted: true, duplicate: false });
    const duplicate = await page.request.post(hookPath, {
      data: body,
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${token}`,
      },
    });
    expect(duplicate.status()).toBe(202);
    expect(await duplicate.json()).toEqual({ accepted: true, duplicate: true });
    await expect
      .poll(
        async () =>
          (await json(page, `${connectionPath}/work`)).jobs.some(
            (job: { state: string; backlink_state: string }) =>
              job.state === "DONE" && job.backlink_state === "SENT",
          ),
        { timeout: 40_000 },
      )
      .toBe(true);
    const links = await json(
      page,
      `${base}/spaces/${fixture.space_id}/tasks/${fixture.task_id}/git-links`,
    );
    expect(JSON.stringify(links)).toContain("codecommit");
    await page.getByRole("button", { name: "刷新", exact: true }).click();
    await expect(
      page.getByText("已处理 · 回链已发送", { exact: true }),
    ).toBeVisible();
    proof.webhook_status = 202;
    proof.duplicate_confirmed = true;
    proof.worker_backlink_sent = true;

    await page.goto(`/o/${fixture.organization_id}/tasks/${fixture.task_key}`);
    await expect(
      page.getByText("CodeCommit 关联", { exact: true }),
    ).toBeVisible();
    const prLink = page.locator(
      'a[href*="console.aws.amazon.com/codesuite/codecommit/repositories/Repo/pull-requests/7"]',
    );
    await expect(prLink.first()).toBeVisible();
    expect(
      (await prLink.first().getAttribute("href"))?.includes("region=us-east-1"),
    ).toBe(true);
    proof.task_git_link_visible = true;
    await page.goto(settings);
    await page
      .getByRole("button", { name: "轮换桥接令牌", exact: true })
      .click();
    dialog = page.getByRole("dialog");
    await dialog
      .getByRole("button", { name: "轮换桥接令牌", exact: true })
      .click();
    await expect(tokenInput).toBeVisible();
    const replacement = await tokenInput.inputValue();
    secrets.push(replacement);
    expect(replacement.length === 43 && replacement !== token).toBe(true);
    await dialog
      .getByRole("button", { name: "关闭", exact: true })
      .last()
      .click();
    const old = await page.request.post(hookPath, {
      data: body,
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${token}`,
      },
    });
    expect(old.status()).toBe(401);
    await expect(tokenInput).toHaveCount(0);
    proof.old_token_status_after_rotation = 401;
    proof.completed = true;
  } finally {
    // No screenshots, traces, request headers, credentials, or one-time tokens.
    fs.writeFileSync(
      `${fixtureFile}.browser-proof.json`,
      JSON.stringify(proof, null, 2),
      { mode: 0o600 },
    );
    await page.close();
    fs.writeFileSync(`${fixtureFile}.stop`, "stop", { mode: 0o600 });
  }
});
