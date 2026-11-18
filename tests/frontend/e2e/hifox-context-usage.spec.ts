import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { randomUUID } from "node:crypto";
import { expect, test, type Page } from "@playwright/test";

test.skip(
  !process.env.OPENFOX_C05_DATABASE_URL_FILE,
  "requires isolated C05 database fixture runner",
);
const org = "00000000-0000-0000-0000-0000000000a1";
const space = "00000000-0000-0000-0000-0000000000aa";
const agent = "00000000-0000-0000-0000-0000000001a8";
const base = `/api/v1/organizations/${org}`;

// Controlled durable event fixtures only. Device-authenticated ingestion is
// separately tested in stage2 and the actual Daemon process in stage2c.
function sql(statement: string, vars: Record<string, string>) {
  const dsn = readFileSync(
    process.env.OPENFOX_C05_DATABASE_URL_FILE!,
    "utf8",
  ).trim();
  const localDatabase =
    /^user=postgres host=(?:\/private)?\/tmp\/openfox-[\w.-]+\/socket port=([0-9]{1,5}) dbname=openfox_c05_[a-f0-9]+ sslmode=disable$/.exec(
      dsn,
    );
  if (
    !localDatabase ||
    Number(localDatabase[1]) < 1 ||
    Number(localDatabase[1]) > 65535
  )
    throw new Error("not an isolated C05 database");
  execFileSync(
    "psql",
    [
      "-X",
      "-v",
      "ON_ERROR_STOP=1",
      "-d",
      dsn,
      ...Object.entries(vars).flatMap(([key, value]) => [
        "-v",
        `${key}=${value}`,
      ]),
    ],
    { input: statement, timeout: 15000, stdio: ["pipe", "pipe", "pipe"] },
  );
}
async function api(
  page: Page,
  endpoint: string,
  method = "GET",
  data?: unknown,
) {
  const csrf = (await page.context().cookies()).find(
    (c) => c.name === "openfox_csrf",
  )?.value;
  const response = await page.request.fetch(`${base}/${endpoint}`, {
    method,
    data,
    headers: csrf ? { "X-CSRF-Token": csrf } : {},
  });
  expect(response.ok(), `${method} ${endpoint}: ${response.status()}`).toBe(
    true,
  );
  return response.status() === 204 ? null : response.json();
}

test("C05 persisted samples stream, decrease, refresh and become unknown after changing model", async ({
  page,
}) => {
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
  const conversation = await api(page, "conversations", "POST", {
    space_id: space,
    agent_id: agent,
  });
  const run = randomUUID();
  sql(
    `INSERT INTO openfox.runs (organization_id,id,space_id,conversation_id,agent_id,computer_id,grant_id,config_version,grant_version,actor_id,state,started_at,finished_at)
    SELECT c.organization_id,:'run'::uuid,c.space_id,c.id,c.agent_id,a.device_id,g.id,a.current_config_version,g.permission_version,c.owner_user_id,'SUCCEEDED',clock_timestamp()-interval '1 second',clock_timestamp()
    FROM openfox.conversations c JOIN openfox.agents a ON a.organization_id=c.organization_id AND a.id=c.agent_id JOIN openfox.device_grants g ON g.organization_id=a.organization_id AND g.device_id=a.device_id WHERE c.id=:'conversation'::uuid;`,
    { run, conversation: conversation.id },
  );
  await page.goto(`/o/${org}/chat/${conversation.id}`);
  const content = page.getByLabel("运行统计", { exact: true });
  const context = content.locator('span[title^="上下文："]');
  await expect(context).toHaveText("上下文 未知");
  function sample(
    sequence: number,
    current: number | null,
    window: number | null,
  ) {
    sql(
      `INSERT INTO openfox.run_events (organization_id,run_id,generation,sequence,type,safe_payload,payload_hash,occurred_at)
      VALUES (:'org'::uuid,:'run'::uuid,1,:'sequence'::bigint,'context_usage',:'payload'::jsonb,repeat('0',64),'2026-11-13T08:00:00Z');`,
      {
        org,
        run,
        sequence: String(sequence),
        payload: JSON.stringify({
          data: {
            schema: 1,
            runtime: "codex",
            requested_model: "fixture-model",
            model: "reported-fixture-model",
            current_tokens: current,
            window_tokens: window,
          },
        }),
      },
    );
  }
  sample(1, 60000, 124518);
  await expect(context).toHaveAttribute("title", /60,000 token/);
  await expect(context).toHaveAttribute("title", /最近运行样本/);
  const sampledAt = await page.evaluate(() =>
    new Date("2026-11-13T08:00:00Z").toLocaleString("zh-CN"),
  );
  expect(await context.getAttribute("title")).toContain(sampledAt);
  sample(3, 6236, 124518);
  sample(2, 70000, 124518);
  await expect(context).toHaveAttribute("title", /6,236 token/);
  sample(4, 0, null);
  await expect(context).toHaveText("上下文 0 token");
  await expect(context).toHaveAttribute("title", /上限未知/);
  await page.reload();
  await expect(context).toHaveText("上下文 0 token");
  sample(5, -1, 124518);
  await expect(context).toHaveText("上下文 未知");
  sample(6, 130000, 124518);
  await expect(content.getByRole("progressbar")).toHaveAttribute(
    "aria-valuenow",
    "100",
  );
  await expect(content).toContainText("104.4%");
  const refreshed = await api(page, `conversations/${conversation.id}`);
  await api(page, `conversations/${conversation.id}/overrides`, "PATCH", {
    expected_version: refreshed.overrides.version,
    model: "default",
  });
  await expect(context).toHaveAttribute("title", /配置已变更，等待新运行样本/);
  await expect(content.getByRole("progressbar")).toHaveCount(0);
  await page.reload();
  await expect(context).toHaveText("上下文 未知");
  await expect(context).toHaveAttribute("title", /配置已变更，等待新运行样本/);
});
