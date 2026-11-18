import { chmodSync, readFileSync, writeFileSync } from "node:fs";
import { expect, test, type Page, type BrowserContext } from "@playwright/test";

// Run only with TestZohoBrowserHarness and a private Playwright config that maps
// accounts.zoho.com to its owned TLS listener and trusts that listener's SPKI.
// No browser route mocks, real Zoho credentials, broad TLS bypass or traces.
const fixturePath = process.env.OPENFOX_I06_BROWSER_FILE;
test.skip(
  !fixturePath,
  "requires the exclusive Zoho HTTP/PG/worker/TLS harness",
);
interface Fixture {
  web_url: string;
  organization_id: string;
  space_id: string;
  owner_email: string;
  member_email: string;
  member_id: string;
  password: string;
}
type HTTPFact = {
  origin: "openfox" | "zoho_authorize" | "unexpected";
  hostname?: "accounts.zoho.com";
  method: string;
  path: string;
  status: number;
};
const httpFacts: HTTPFact[] = [];
const popupStates: Array<{ flow: string; classification: string }> = [];
let openfoxOrigin = "";
function recordHTTP(
  method: string,
  raw: string,
  status: number,
  document = false,
) {
  const url = new URL(raw, openfoxOrigin || "http://127.0.0.1");
  const own = url.origin === openfoxOrigin;
  const auth = url.origin === "https://accounts.zoho.com";
  if (!document && !url.pathname.startsWith("/api/") && !auth) return;
  // Never retain query (OAuth code/state), fragment, headers or response bodies.
  const path = own
    ? url.pathname.replace(
        /\/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}(?=\/|$)/gi,
        "/{id}",
      )
    : auth && url.pathname === "/oauth/v2/auth"
      ? "/oauth/v2/auth"
      : "/{other-document}";
  httpFacts.push({
    origin: own ? "openfox" : auth ? "zoho_authorize" : "unexpected",
    ...(auth ? { hostname: "accounts.zoho.com" as const } : {}),
    method,
    path,
    status,
  });
}
function observeHTTP(context: BrowserContext) {
  context.on("response", (response) =>
    recordHTTP(
      response.request().method(),
      response.url(),
      response.status(),
      response.request().resourceType() === "document",
    ),
  );
  context.on("requestfailed", (request) =>
    recordHTTP(
      request.method(),
      request.url(),
      0,
      request.resourceType() === "document",
    ),
  );
}
async function waitForConfirmation(popup: Page, heading: string, flow: string) {
  const deadline = Date.now() + 90_000;
  while (Date.now() < deadline) {
    const url = new URL(popup.url());
    const expected =
      url.origin === openfoxOrigin &&
      url.pathname === "/integrations/oauth/complete";
    const text = await popup
      .locator("body")
      .innerText({ timeout: 1000 })
      .catch(() => "");
    const realSignIn =
      url.origin === "https://accounts.zoho.com" &&
      /sign in|登录|登入|Zoho Accounts/i.test(text);
    const classification = realSignIn
      ? "unexpected_provider_signin"
      : expected
        ? "openfox_confirmation"
        : url.protocol === "about:"
          ? "initial_blank"
          : url.origin === "https://accounts.zoho.com"
            ? "authorization_redirect_pending"
            : "unexpected_document";
    if (
      popupStates.at(-1)?.flow !== flow ||
      popupStates.at(-1)?.classification !== classification
    )
      popupStates.push({ flow, classification });
    if (realSignIn)
      throw new Error(
        "Controlled OAuth listener was bypassed: provider sign-in appeared; no credentials entered.",
      );
    if (
      expected &&
      (await popup
        .getByRole("heading", { name: heading, exact: true })
        .isVisible())
    )
      return;
    await popup.waitForTimeout(250);
  }
  throw new Error(
    "Controlled OAuth confirmation timed out; see redacted popup classifications.",
  );
}
async function get(page: Page, path: string) {
  const response = await page.request.get(path);
  recordHTTP("GET", path, response.status());
  expect(response.status()).toBe(200);
  return response.json();
}
async function login(page: Page, fixture: Fixture, email: string) {
  const response = await page.request.post("/api/v1/auth/login", {
    data: { email, password: fixture.password, client_kind: "web" },
  });
  recordHTTP("POST", "/api/v1/auth/login", response.status());
  expect(response.status()).toBe(201);
  await page.goto(`/o/${fixture.organization_id}/settings/account-binding`);
  await page.evaluate(
    (org) =>
      localStorage.setItem(
        `openfox:onboarding:${org}`,
        JSON.stringify({ step: "done" }),
      ),
    fixture.organization_id,
  );
}

test("I06 organization and personal OAuth, explicit Bug mappings and durable source reporter", async ({
  page,
  browser,
}) => {
  test.setTimeout(240_000);
  const fixture: Fixture = JSON.parse(readFileSync(fixturePath!, "utf8"));
  const base = `/api/v1/organizations/${fixture.organization_id}`;
  const settings = `/o/${fixture.organization_id}/settings/integrations/zoho_bugtracker`;
  const proof: Record<string, unknown> = {
    real_api_pg_worker: true,
    mocked_browser_requests: false,
    real_zoho_account: false,
  };
  const memberContext = await browser.newContext({
    baseURL: fixture.web_url,
    locale: "zh-CN",
  });
  httpFacts.length = 0;
  popupStates.length = 0;
  openfoxOrigin = new URL(fixture.web_url).origin;
  expect(new URL(openfoxOrigin).hostname).toBe("127.0.0.1");
  observeHTTP(page.context());
  observeHTTP(memberContext);
  try {
    await login(page, fixture, fixture.owner_email);
    await page.goto(settings);
    await expect(page.getByText(/旧版 BugTracker REST/)).toBeVisible();
    const popupEvent = page.waitForEvent("popup");
    await page
      .getByRole("button", { name: "连接 Zoho 门户", exact: true })
      .click();
    const popup = await popupEvent;
    await waitForConfirmation(popup, "确认 Zoho 门户授权", "organization");
    await expect(
      popup.getByRole("button", { name: "确认连接门户", exact: true }),
    ).toBeDisabled();
    await popup.getByRole("button", { name: "Zoho 门户", exact: true }).click();
    await popup
      .getByRole("option", { name: "Controlled portal · 2063927", exact: true })
      .click();
    await popup.getByRole("checkbox").check();
    await popup
      .getByRole("button", { name: "确认连接门户", exact: true })
      .click();
    await expect(
      page.getByText("Controlled portal", { exact: true }),
    ).toBeVisible({ timeout: 90_000 });
    proof.organization_confirmed = true;

    const member = await memberContext.newPage();
    await login(member, fixture, fixture.member_email);
    await member.reload();
    const row = member.getByTestId("binding-zoho_bugtracker");
    const personalPopup = member.waitForEvent("popup");
    await row.getByRole("button", { name: "绑定", exact: true }).click();
    const identity = await personalPopup;
    await waitForConfirmation(identity, "Zoho 本人身份", "personal");
    await expect(
      identity.getByText(/Verified reporter · 2060759/),
    ).toBeVisible();
    await identity
      .getByRole("button", { name: "确认这是我的身份", exact: true })
      .click();
    await expect(
      row.getByRole("button", { name: "已连接", exact: false }),
    ).toBeVisible({ timeout: 90_000 });
    proof.personal_explicit_confirmation = true;

    await page.goto(
      `/o/${fixture.organization_id}/settings/import-export?source=zoho_bugtracker&method=api&space=${fixture.space_id}`,
    );
    await page.getByRole("button", { name: "继续", exact: true }).click();
    await page.getByRole("button", { name: "Zoho 门户", exact: true }).click();
    await page
      .getByRole("option", { name: "Controlled portal", exact: true })
      .click();
    await expect(
      page.getByText("已列出 1 个活动项目", { exact: true }),
    ).toBeVisible({ timeout: 60_000 });
    await page.getByRole("button", { name: "项目", exact: true }).click();
    await page
      .getByRole("option", { name: "Controlled bugs", exact: true })
      .click();
    const createdEvent = page.waitForResponse(
      (r) =>
        r.request().method() === "POST" &&
        r.url().endsWith("/imports/zoho_bugtracker/project"),
    );
    await page.getByRole("button", { name: "下一步", exact: true }).click();
    const createdResponse = await createdEvent;
    expect(createdResponse.status()).toBe(201);
    const job = await createdResponse.json();
    await expect
      .poll(async () => (await get(page, `${base}/imports/${job.id}`)).phase, {
        timeout: 90_000,
      })
      .toBe("");
    await expect(
      page.getByRole("button", { name: "下一步", exact: true }),
    ).toBeEnabled({ timeout: 15_000 });
    await page.getByRole("button", { name: "下一步", exact: true }).click();
    await expect(
      page.getByRole("heading", { name: "选择任务", exact: true }),
    ).toBeVisible();
    await page.getByRole("button", { name: "下一步", exact: true }).click();
    await page
      .getByRole("button", { name: "类型「Bug」映射", exact: true })
      .click();
    await page
      .getByRole("option", { name: "空间默认类型", exact: true })
      .click();
    const statuses = (await get(page, `${base}/task-statuses`))
      .statuses as Array<{ id: string; name: string; category: string }>;
    const todo = statuses.find((s) => s.category === "TODO")!;
    const done = statuses.find((s) => s.category === "DONE")!;
    const sourceStatuses = page.getByRole("button", {
      name: "Source status 导入为",
      exact: true,
    });
    await expect(sourceStatuses).toHaveCount(2);
    for (const [index, target] of [todo, done].entries()) {
      await sourceStatuses.nth(index).click();
      await page
        .getByRole("option", { name: target.name, exact: true })
        .click();
    }
    await page
      .getByRole("button", {
        name: "Severity「Severity: Source severity」映射",
        exact: true,
      })
      .click();
    await page.getByRole("option", { name: "高", exact: true }).click();
    await page.getByRole("button", { name: "下一步", exact: true }).click();
    await expect(
      page.getByRole("heading", { name: "映射用户", exact: true }),
    ).toBeVisible();
    await expect(page.getByText("本人已验证", { exact: true })).toBeVisible();
    const mappingPreview = await get(page, `${base}/imports/${job.id}`);
    const verifiedReporter = mappingPreview.analysis.users.find(
      (user: { source_id: string }) => user.source_id === "us:2060759",
    );
    expect(verifiedReporter?.verified_binding?.user_id).toBe(fixture.member_id);
    expect(verifiedReporter?.verified_binding?.id).toMatch(/^[0-9a-f-]{36}$/i);
    await page.getByRole("button", { name: "下一步", exact: true }).click();
    await expect(page.getByRole("switch", { name: /持续同步/ })).toHaveCount(0);
    await page.getByRole("button", { name: "下一步", exact: true }).click();
    const configured = await get(page, `${base}/imports/${job.id}`);
    expect(configured.config.types).toEqual([
      { source: "Bug", use_default: true },
    ]);
    expect(configured.config.priorities).toEqual([
      { source: "5", priority: "HIGH" },
    ]);
    expect(configured.config.statuses).toEqual(
      expect.arrayContaining([
        { source: "11", status_id: todo.id },
        { source: "12", status_id: done.id },
      ]),
    );
    expect(configured.config.users).toEqual(
      expect.arrayContaining([
        expect.objectContaining({
          source_id: "us:2060759",
          mode: "member",
          user_id: fixture.member_id,
          verified_binding_id: verifiedReporter.verified_binding.id,
        }),
      ]),
    );
    expect(configured.config.options.continuous_sync ?? false).toBe(false);
    await page.getByRole("button", { name: "开始导入", exact: true }).click();
    await expect
      .poll(async () => (await get(page, `${base}/imports/${job.id}`)).status, {
        timeout: 90_000,
      })
      .toBe("SUCCEEDED");
    proof.explicit_source_id_mappings = true;
    const imported = (
      await get(page, `${base}/spaces/${fixture.space_id}/tasks`)
    ).tasks.filter((t: { title: string }) =>
      t.title.startsWith("Bug <script>safe text</script>"),
    );
    expect(imported).toHaveLength(2);
    expect(
      imported.every(
        (t: { priority: string; assignee_user_id?: string }) =>
          t.priority === "HIGH" && !t.assignee_user_id,
      ),
    ).toBe(true);
    await page.goto(
      `/o/${fixture.organization_id}/tasks/${imported[0].display_id}`,
    );
    await expect(
      page.getByText(/来源报告人：Verified reporter（US · 2060759）/),
    ).toBeVisible();
    await expect(page.getByText(/映射成员：/)).toBeVisible();
    await expect(
      page.getByText("UNVERIFIED BODY MUST NOT IMPORT", { exact: true }),
    ).toHaveCount(0);
    await page.reload();
    await expect(
      page.getByText(/来源报告人：Verified reporter（US · 2060759）/),
    ).toBeVisible();
    proof.reporter_snapshot_and_reload = true;
    // Safe terminal page only: no OAuth page, input credentials or secret URLs.
    await page.screenshot({
      path: `${fixturePath}.task-timeline.png`,
      fullPage: false,
    });
    chmodSync(`${fixturePath}.task-timeline.png`, 0o600);
    proof.safe_task_timeline_screenshot = true;

    await page.goto(settings);
    await page.getByRole("button", { name: "断开", exact: true }).click();
    const dialog = page.getByRole("dialog");
    await dialog
      .getByLabel("确认短语", { exact: true })
      .fill("断开 Zoho BugTracker");
    await dialog.getByRole("button", { name: "断开", exact: true }).click();
    await expect(dialog).toHaveCount(0);
    const remaining = (
      await get(page, `${base}/spaces/${fixture.space_id}/tasks`)
    ).tasks;
    expect(
      imported.every((task: { id: string }) =>
        remaining.some((v: { id: string }) => v.id === task.id),
      ),
    ).toBe(true);
    proof.disconnect_retains_tasks = true;
    proof.completed = true;
  } finally {
    await memberContext.close();
    writeFileSync(
      `${fixturePath}.browser-proof.json`,
      JSON.stringify(proof, null, 2),
      { mode: 0o600 },
    );
    writeFileSync(
      `${fixturePath}.http-summary.json`,
      JSON.stringify(
        {
          recorded_fields: [
            "origin_class",
            "fixed_auth_hostname",
            "method",
            "path_template_without_query",
            "status",
          ],
          popup_states: popupStates,
          requests: httpFacts,
          failures: httpFacts.filter(
            (fact) => fact.status === 0 || fact.status >= 400,
          ),
          status_zero:
            "transport cancelled or failed; no response body or error text retained",
        },
        null,
        2,
      ),
      { mode: 0o600 },
    );
    writeFileSync(`${fixturePath}.stop`, "stop", { mode: 0o600 });
  }
});
