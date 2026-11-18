import { readFileSync, writeFileSync } from "node:fs";
import { createHmac, randomUUID } from "node:crypto";
import { expect, test, type Page } from "@playwright/test";

const fixturePath = process.env.OPENFOX_BITBUCKET_BROWSER_FILE;
test.skip(
  !fixturePath,
  "requires isolated Bitbucket TLS, PostgreSQL and CONNECT fixture",
);
const fixture = fixturePath
  ? JSON.parse(readFileSync(fixturePath, "utf8"))
  : {};
const base = `/api/v1/organizations/${fixture.organization_id}`;
const diary: { method: string; path: string; status: number }[] = [];
async function request(
  page: Page,
  path: string,
  method = "GET",
  data?: unknown,
  expected = 200,
) {
  const csrf = (await page.context().cookies()).find(
    (c) => c.name === "openfox_csrf",
  )?.value;
  const r = await page.request.fetch(path, {
    method,
    data,
    headers: {
      ...(csrf ? { "X-CSRF-Token": csrf } : {}),
      "Idempotency-Key": randomUUID(),
    },
  });
  diary.push({
    method,
    path: path.replace(
      /\/hooks\/git\/bitbucket\/[^/?]+/,
      "/hooks/git/bitbucket/[fixture-token]",
    ),
    status: r.status(),
  });
  expect(r.status(), `${method} ${path}: ${r.status()} ${await r.text()}`).toBe(
    expected,
  );
  return r.status() === 204 ? null : r.json();
}
async function login(page: Page, email: string) {
  await request(
    page,
    "/api/v1/auth/login",
    "POST",
    {
      email,
      password: fixture.password,
      client_kind: "web",
    },
    201,
  );
}
async function event(
  page: Page,
  hook: any,
  kind: string,
  comment = 9,
  id = randomUUID(),
  extra = false,
) {
  const body = JSON.stringify({
    repository: {
      uuid: fixture.repository_uuid,
      workspace: { uuid: fixture.workspace_uuid },
    },
    pullrequest: { id: 7 },
    comment: { id: comment },
    ...(extra ? { changed: true } : {}),
  });
  const r = await page.request.post(hook.payload_url, {
    data: body,
    headers: {
      "Content-Type": "application/json",
      "X-Event-Key": kind,
      "X-Hook-UUID": "55555555-5555-4555-8555-555555555555",
      "X-Request-UUID": id,
      "X-Hub-Signature": `sha256=${createHmac("sha256", hook.secret).update(body).digest("hex")}`,
    },
  });
  diary.push({
    method: "POST",
    path: "/api/v1/hooks/git/bitbucket/[fixture-token]",
    status: r.status(),
  });
  return r;
}

test("Bitbucket actual OAuth, scoped repository, signed worker and explicit UNKNOWN recovery", async ({
  page,
  browser,
}, info) => {
  test.setTimeout(150000);
  try {
    await login(page, fixture.owner_email);
    const detailPath = `${base}/integrations/bitbucket`;
    await page.goto(
      `/o/${fixture.organization_id}/settings/integrations/bitbucket`,
    );
    const oauth = page.waitForEvent("popup");
    await page
      .getByRole("button", { name: "授权 Bitbucket workspace", exact: true })
      .click();
    const popup = await oauth;
    await expect(
      popup.getByRole("heading", { name: "确认 Bitbucket 组织授权" }),
    ).toBeVisible();
    await popup.getByLabel("Bitbucket workspace", { exact: true }).click();
    await popup
      .getByRole("option", {
        name: "Controlled Workspace · controlled",
        exact: true,
      })
      .click();
    await popup.getByRole("checkbox", { name: /我同意将此 workspace/ }).check();
    await popup
      .getByRole("button", { name: "确认组织授权", exact: true })
      .click();
    await expect(page.getByText(/组织发送者：Fixture Sender/)).toBeVisible();
    const detail = await request(page, detailPath);
    expect(detail.provider.availability).toBe("available");
    const connection = detail.connections.find(
      (c: any) => c.status === "ACTIVE",
    );
    const conn = `${detailPath}/connections/${connection.id}`;
    const memberContext = await browser.newContext({
      baseURL: "http://127.0.0.1:4203",
      locale: "zh-CN",
      ignoreHTTPSErrors: true,
      proxy: {
        server: "http://127.0.0.1:18154",
        bypass: "127.0.0.1,localhost",
      },
    });
    const member = await memberContext.newPage();
    await login(member, fixture.member_email);
    await request(member, `${conn}/repositories`, "GET", undefined, 403);
    await member.goto(`/o/${fixture.organization_id}/settings/account-binding`);
    const row = member.getByTestId("binding-bitbucket");
    const personalEvent = member.waitForEvent("popup");
    await row.getByRole("button", { name: "绑定", exact: true }).click();
    const personal = await personalEvent;
    await expect(
      personal.getByRole("heading", { name: "Bitbucket 本人身份关联" }),
    ).toBeVisible();
    await expect(
      personal.getByText(/33333333-3333-4333-8333-333333333333/),
    ).toBeVisible();
    await expect(
      personal.getByRole("button", { name: "确认绑定本人身份" }),
    ).toBeDisabled();
    await personal
      .getByRole("checkbox", { name: /我确认这是本人的 Bitbucket 身份/ })
      .check();
    await personal.getByRole("button", { name: "确认绑定本人身份" }).click();
    await expect(
      row.getByRole("button", { name: "已连接", exact: true }),
    ).toBeVisible();
    let bindings = await request(member, `${base}/me/account-bindings`);
    const bound = bindings.providers.find((p: any) => p.id === "bitbucket");
    expect(bound.instances[0].binding).toBeTruthy();
    expect(bound.instances[0].binding.display_name).toBe("Fixture Member");
    expect(bound.instances[0].binding.detail).toBe(fixture.workspace_uuid);
    if (fixture.unconfigured_url) {
      const missing = await request(
        member,
        `${fixture.unconfigured_url}/v1/organizations/${fixture.organization_id}/me/account-bindings`,
      );
      const item = missing.providers.find((p: any) => p.id === "bitbucket");
      expect(item.organization_ready).toBe(false);
      expect(item.instances[0].binding).toBeTruthy();
    }
    await member.screenshot({
      path: info.outputPath("personal-bound.png"),
      fullPage: true,
    });
    await page
      .getByRole("button", { name: "仓库与 Webhook", exact: true })
      .click();
    let dialog = page.getByRole("dialog", {
      name: "仓库与 Webhook",
      exact: true,
    });
    await dialog.getByRole("button", { name: "选择仓库", exact: true }).click();
    const editor = page.getByRole("dialog", {
      name: "仓库关联设置",
      exact: true,
    });
    await editor
      .getByRole("button", { name: "加载更多仓库", exact: true })
      .click();
    await expect(
      editor.getByText("已加载 2 个仓库", { exact: true }),
    ).toBeVisible();
    await editor.getByLabel("Bitbucket 仓库", { exact: true }).click();
    await page
      .getByRole("option", {
        name: "controlled/private-repo · 私有仓库",
        exact: true,
      })
      .click();
    await editor
      .getByRole("checkbox", { name: fixture.space_name, exact: true })
      .check();
    await expect(
      editor.getByRole("checkbox", { name: "关联 Pull Request", exact: true }),
    ).toBeChecked();
    await expect(
      editor.getByRole("checkbox", { name: /关联提交/ }),
    ).not.toBeChecked();
    await expect(
      editor.getByRole("checkbox", {
        name: "在私有仓库 PR 发送任务回链",
        exact: true,
      }),
    ).toBeChecked();
    await expect(
      editor.getByRole("checkbox", {
        name: "在公开仓库 PR 发送任务回链",
        exact: true,
      }),
    ).not.toBeChecked();
    await expect(
      editor.getByRole("button", { name: "保存", exact: true }),
    ).toBeDisabled();
    await editor
      .getByRole("checkbox", { name: /我同意这些空间的成员可看到/ })
      .check();
    await editor
      .getByRole("checkbox", { name: /我确认使用组织授权账号 Fixture Sender/ })
      .check();
    await editor.getByRole("button", { name: "保存", exact: true }).click();
    await expect(editor).toBeHidden();
    await dialog.getByRole("button", { name: "查看手工 Webhook 配置" }).click();
    await expect(
      dialog.getByText("尚未生成回调地址与签名密钥。查看不会自动生成。", {
        exact: true,
      }),
    ).toBeVisible();
    await dialog
      .getByRole("checkbox", { name: /我理解影响，并将手工配置/ })
      .check();
    await dialog
      .getByRole("button", { name: "生成回调地址与密钥", exact: true })
      .click();
    await expect(
      dialog.getByLabel("Payload URL", { exact: true }),
    ).not.toHaveValue("");
    let hook = await request(page, `${conn}/webhook`);
    const original = hook.payload_url;
    await dialog
      .getByRole("checkbox", { name: /我理解影响，并将手工配置/ })
      .check();
    await dialog
      .getByRole("button", { name: "轮换回调地址与密钥", exact: true })
      .click();
    await expect
      .poll(async () => (await request(page, `${conn}/webhook`)).payload_url)
      .not.toBe(original);
    hook = await request(page, `${conn}/webhook`);
    const bad = await page.request.post(hook.payload_url, {
      data: "{}",
      headers: {
        "Content-Type": "application/json",
        "X-Hub-Signature": "sha256=" + "0".repeat(64),
      },
    });
    expect(bad.status()).toBe(401);
    const delivery = randomUUID();
    expect(
      (await event(page, hook, "pullrequest:created", 9, delivery)).status(),
    ).toBe(200);
    expect(
      (await event(page, hook, "pullrequest:created", 9, delivery)).status(),
    ).toBe(200);
    expect(
      (
        await event(page, hook, "pullrequest:created", 9, delivery, true)
      ).status(),
    ).toBe(409);
    await expect
      .poll(
        async () =>
          (await request(page, `${conn}/jobs`)).jobs.some(
            (j: any) =>
              j.kind === "BACKLINK" && j.state === "UNKNOWN" && j.can_retry,
          ),
        { timeout: 25000 },
      )
      .toBe(true);
    const unknown = (await request(page, `${conn}/jobs`)).jobs.find(
      (j: any) => j.kind === "BACKLINK" && j.state === "UNKNOWN",
    );
    await request(
      page,
      `${conn}/jobs/retry`,
      "POST",
      {
        kind: "BACKLINK",
        id: unknown.id,
        version: unknown.version,
        confirm_unsent: false,
      },
      400,
    );
    await expect(
      dialog.getByRole("button", { name: "核查并申请重发", exact: true }),
    ).toBeVisible({ timeout: 10000 });
    await dialog
      .getByRole("button", { name: "核查并申请重发", exact: true })
      .click();
    await expect(
      dialog.getByRole("button", { name: "确认未发送并重新发送" }),
    ).toBeDisabled();
    await page.screenshot({
      path: info.outputPath("unknown-requires-confirmation.png"),
      fullPage: true,
      mask: [
        dialog.getByLabel("Payload URL", { exact: true }),
        dialog.getByLabel("签名 Secret", { exact: true }),
      ],
    });
    await dialog
      .getByRole("checkbox", { name: /我已核对源 PR 并确认未发送/ })
      .check();
    await dialog.getByRole("button", { name: "确认未发送并重新发送" }).click();
    await expect
      .poll(async () =>
        (await request(page, `${conn}/jobs`)).jobs.some(
          (j: any) => j.kind === "BACKLINK" && j.state === "SENT",
        ),
      )
      .toBe(true);
    expect(
      (await event(page, hook, "pullrequest:comment_created")).status(),
    ).toBe(200);
    const activityPath = `${base}/spaces/${fixture.space_id}/tasks/${fixture.task_id}/activities`;
    await expect
      .poll(async () =>
        (await request(page, activityPath)).activities.some(
          (a: any) =>
            a.kind === "task.git_comment" &&
            a.safe_diff.comment_id === 9 &&
            a.safe_diff.author_user_id === fixture.member_id,
        ),
      )
      .toBe(true);
    const links = await request(
      page,
      `${base}/spaces/${fixture.space_id}/tasks/${fixture.task_id}/git-links`,
    );
    expect(JSON.stringify(links)).toContain("controlled/private-repo");
    const outside = await request(
      page,
      `${base}/spaces/${fixture.outside_space_id}/tasks/${fixture.outside_task_id}/git-links`,
    );
    expect(JSON.stringify(outside)).not.toContain("bitbucket");
    await page.goto(`/o/${fixture.organization_id}/tasks/${fixture.task_key}`);
    await expect(
      page.getByText("Bitbucket 关联", { exact: true }),
    ).toBeVisible();
    await expect(
      page.getByText("Fixture Member 的 Bitbucket PR #7 评论 #9", {
        exact: true,
      }),
    ).toBeVisible();
    await expect(
      page.getByText("source comment body must never be copied", {
        exact: true,
      }),
    ).toHaveCount(0);
    await page.screenshot({
      path: info.outputPath("linked-pr-comment-author.png"),
      fullPage: true,
    });
    await row.getByRole("button", { name: "已连接", exact: true }).click();
    await member.getByRole("menuitem", { name: /断开/ }).click();
    await member
      .getByRole("dialog")
      .getByRole("button", { name: /断开/ })
      .click();
    await expect(
      row.getByRole("button", { name: "绑定", exact: true }),
    ).toBeVisible();
    bindings = await request(member, `${base}/me/account-bindings`);
    expect(
      bindings.providers.find((p: any) => p.id === "bitbucket").instances[0]
        .binding,
    ).toBeNull();
    expect(
      (await event(page, hook, "pullrequest:comment_created", 10)).status(),
    ).toBe(200);
    await expect
      .poll(
        async () =>
          (await request(page, activityPath)).activities.filter(
            (a: any) => a.kind === "task.git_comment",
          ).length,
      )
      .toBe(2);
    const activities = (await request(page, activityPath)).activities.filter(
      (a: any) => a.kind === "task.git_comment",
    );
    expect(
      activities.find((a: any) => a.safe_diff.comment_id === 9).safe_diff
        .author_user_id,
    ).toBe(fixture.member_id);
    expect(
      activities.find((a: any) => a.safe_diff.comment_id === 10).safe_diff
        .author_user_id,
    ).toBeFalsy();
    writeFileSync(fixturePath + ".sender-change", "controlled sender change");
    await page.goto(
      `/o/${fixture.organization_id}/settings/integrations/bitbucket`,
    );
    const again = page.waitForEvent("popup");
    await page.getByRole("button", { name: "重新授权", exact: true }).click();
    const reauth = await again;
    await expect(reauth.getByText(/原发送者：Fixture Sender/)).toBeVisible();
    await expect(
      reauth.getByText(/新发送者：New Fixture Sender/),
    ).toBeVisible();
    await expect(
      reauth.getByLabel("Bitbucket workspace", { exact: true }),
    ).toBeDisabled();
    await reauth
      .getByRole("checkbox", { name: /我同意将此 workspace/ })
      .check();
    await expect(
      reauth.getByRole("button", { name: "确认组织授权", exact: true }),
    ).toBeDisabled();
    await reauth
      .getByRole("checkbox", {
        name: "我明确同意更换后续回链的发送者。",
        exact: true,
      })
      .check();
    await reauth
      .getByRole("button", { name: "确认组织授权", exact: true })
      .click();
    await expect(
      page.getByText(/组织发送者：New Fixture Sender/),
    ).toBeVisible();
    await page.screenshot({
      path: info.outputPath("reauthorized-sender.png"),
      fullPage: true,
    });
    await page.getByRole("button", { name: "断开", exact: true }).click();
    const disconnect = page.getByRole("dialog");
    await disconnect.getByRole("textbox").fill("断开 Bitbucket");
    await disconnect.getByRole("button", { name: "断开", exact: true }).click();
    await expect(
      page.getByRole("button", {
        name: "授权 Bitbucket workspace",
        exact: true,
      }),
    ).toBeVisible();
    await page.screenshot({
      path: info.outputPath("disconnected-history-preserved.png"),
      fullPage: true,
    });
    expect(
      (await request(page, activityPath)).activities.filter(
        (a: any) => a.kind === "task.git_comment",
      ),
    ).toHaveLength(2);
    await memberContext.close();
  } finally {
    writeFileSync(
      info.outputPath("http-summary.json"),
      JSON.stringify(diary, null, 2),
    );
  }
});
