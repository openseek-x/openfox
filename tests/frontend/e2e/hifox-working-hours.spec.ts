import { expect, test, type Page } from "@playwright/test";
test.skip(
  process.env.OPENFOX_HIFOX_A04_E2E !== "1",
  "isolated A04 fixtures required",
);
const org = "00000000-0000-0000-0000-0000000000a1",
  space = "00000000-0000-0000-0000-0000000000aa",
  agent = "00000000-0000-0000-0000-0000000001a8";
const base = `/api/v1/organizations/${org}`;
async function request(
  page: Page,
  path: string,
  method = "GET",
  data?: unknown,
) {
  const csrf = (await page.context().cookies()).find(
    (c) => c.name === "openfox_csrf",
  )?.value;
  const r = await page.request.fetch(path, {
    method,
    data,
    headers: csrf ? { "X-CSRF-Token": csrf } : {},
  });
  expect(
    r.ok(),
    `${method} ${path}: ${r.status()} ${await r.text()}`,
  ).toBeTruthy();
  return r.status() === 204 ? null : r.json();
}
async function login(page: Page) {
  await page.goto("/login");
  await page
    .getByLabel("\u90ae\u7bb1", { exact: true })
    .fill("alice@example.test");
  await page.getByRole("button", { name: "\u7ee7\u7eed" }).click();
  await page
    .getByLabel("\u7b2c 1 \u4f4d")
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
test("A04 persists schedule, defers a real assignment, and releases the same run", async ({
  page,
}) => {
  await login(page);
  await page.goto(`/o/${org}/agents?agentSettings=${agent}`);
  const section = page.locator("#agent-section-workingHours");
  await section.getByRole("button").first().click();
  const enable = section.getByRole("switch");
  await expect(enable).not.toBeChecked();
  await enable.click();
  await section
    .getByLabel("\u6392\u73ed\u65f6\u533a\uff08IANA\uff09")
    .fill("UTC");
  const removes = section.getByRole("button", { name: /^\u5220\u9664 \u5468/ });
  while (await removes.count()) await removes.first().click();
  await expect(
    section.getByRole("checkbox", {
      name: "\u4efb\u52a1\u6307\u6d3e",
      exact: true,
    }),
  ).toBeChecked();
  await expect(
    section.getByRole("checkbox", {
      name: "\u4e0e Agent \u5355\u804a",
      exact: true,
    }),
  ).not.toBeChecked();
  const save = page.waitForResponse(
    (r) =>
      r.url().endsWith(`/agents/${agent}/config-versions`) &&
      r.request().method() === "POST",
  );
  await section
    .getByRole("button", { name: "\u4fdd\u5b58", exact: true })
    .click();
  expect((await save).status()).toBe(201);
  await page.reload();
  await section.getByRole("button").first().click();
  await expect(section.getByRole("switch")).toBeChecked();
  await expect(section.getByText("\u4f11\u606f", { exact: true })).toHaveCount(
    7,
  );
  const cfg = await request(page, `${base}/spaces/${space}/agents/${agent}`);
  expect(cfg.config.working_hours.days).toEqual([[], [], [], [], [], [], []]);
  expect(cfg.config.working_hours.timezone).toBe("UTC");
  const later = new Date(Date.now() + 2 * 86400000).toISOString();
  const task = await request(page, `${base}/spaces/${space}/tasks`, "POST", {
    title: "A04 real deferred task",
    type_id: "00000000-0000-0000-0000-000000000111",
    status_id: "00000000-0000-0000-0000-000000000112",
    assignees: [{ kind: "AGENT", id: agent }],
    assignment_not_before: later,
  });
  let conversation: any;
  await expect
    .poll(async () => {
      const list = await request(
        page,
        `${base}/conversations?task_id=${task.id}`,
      );
      conversation = list.conversations?.find(
        (c: any) => c.run?.schedule_pending,
      );
      return Boolean(conversation);
    })
    .toBeTruthy();
  const run = conversation.run.id;
  await page.goto(`/o/${org}/chat/${conversation.id}`);
  const trigger = page.getByRole("button", {
    name: "\u672c\u6b21\u7acb\u5373\u89e6\u53d1",
    exact: true,
  });
  await expect(trigger).toBeVisible();
  await expect(
    page.getByText(
      "\u6ca1\u6709\u53ef\u7528\u5de5\u4f5c\u7a97\u53e3\uff0c\u6301\u7eed\u7b49\u5f85",
      { exact: true },
    ),
  ).toBeVisible();
  const current = await request(
    page,
    `${base}/spaces/${space}/agents/${agent}`,
  );
  await request(
    page,
    `${base}/spaces/${space}/agents/${agent}/config-versions`,
    "POST",
    {
      computer_id: current.config.computer_id,
      grant_id: current.config.grant_id,
      runtime: current.config.runtime,
      model: current.config.model,
      expected_agent_version: current.version,
      working_hours: {
        ...current.config.working_hours,
        days: Array.from({ length: 7 }, () => [
          { start: "00:00", end: "24:00" },
        ]),
      },
    },
  );
  // Same QUEUED/SCHEDULED state, a changed planned timestamp must refresh over SSE.
  await expect
    .poll(async () =>
      new Date(
        (await request(page, `${base}/runs/${run}`)).not_before,
      ).getTime(),
    )
    .toBe(new Date(later).getTime());
  await expect(page.getByText(/^\u8ba1\u5212\u5f00\u59cb\uff1a/)).toBeVisible();
  await trigger.click();
  const dialog = page.getByRole("dialog", {
    name: "\u89e3\u9664\u672c\u6b21\u65f6\u95f4\u5ef6\u671f\uff1f",
  });
  await expect(dialog).toBeVisible();
  const done = page.waitForResponse((r) =>
    r.url().endsWith(`/runs/${run}/trigger-now`),
  );
  await dialog
    .getByRole("button", {
      name: "\u672c\u6b21\u7acb\u5373\u89e6\u53d1",
      exact: true,
    })
    .click();
  expect((await done).status()).toBe(200);
  await expect(trigger).toHaveCount(0);
  const detail = await request(page, `${base}/runs/${run}`);
  expect(detail.schedule_pending).toBe(false);
  expect(new Date(detail.requested_start_at).getTime()).toBe(
    new Date(later).getTime(),
  );
  expect(detail.state).toBe("QUEUED");
  await page.reload();
  await expect(trigger).toHaveCount(0);
  expect(
    (await request(page, `${base}/conversations/${conversation.id}/runs`)).runs,
  ).toHaveLength(1);
  const timed = await request(page, `${base}/spaces/${space}/tasks`, "POST", {
    title: "A04 manual UI",
    type_id: "00000000-0000-0000-0000-000000000111",
    status_id: "00000000-0000-0000-0000-000000000112",
  });
  await page.goto(`/o/${org}/tasks/${timed.display_id}`);
  await page
    .getByRole("button", { name: "\u53ec\u5524 Agent", exact: true })
    .click();
  await page
    .getByRole("menuitem", {
      name: "\u628a\u4efb\u52a1\u6307\u6d3e\u7ed9 Agent",
      exact: true,
    })
    .click();
  await page
    .getByRole("menuitem", {
      name: "\u5b9a\u65f6\u8fd0\u884c WP11 browser agent",
      exact: true,
    })
    .click();
  const choice = page.getByRole("dialog", {
    name: "\u9009\u62e9\u8fd0\u884c\u5bf9\u8bdd",
    exact: true,
  });
  await choice
    .getByLabel(
      "\u672c\u6b21\u6700\u65e9\u5f00\u59cb\u65f6\u95f4\uff08\u672c\u673a\u65f6\u533a\uff09",
    )
    .fill("2026-11-20T21:30");
  const assigned = page.waitForResponse(
    (r) =>
      r.url().endsWith(`/tasks/${timed.id}`) &&
      r.request().method() === "PATCH",
  );
  await choice
    .getByRole("button", { name: "\u5f00\u59cb\u8fd0\u884c", exact: true })
    .click();
  const patch = await assigned;
  expect(patch.ok()).toBeTruthy();
  expect(patch.request().postDataJSON().assignment_not_before).toBeTruthy();
  await expect
    .poll(async () => {
      const list = await request(
        page,
        `${base}/conversations?task_id=${timed.id}`,
      );
      return list.conversations?.[0]?.run?.requested_start_at ?? null;
    })
    .not.toBeNull();
});
