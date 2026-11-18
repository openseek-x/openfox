import { expect, test, type Page } from "@playwright/test";
test.skip(
  process.env.OPENFOX_HIFOX_C04_E2E !== "1",
  "requires isolated C04 fixtures",
);
const org = "00000000-0000-0000-0000-0000000000a1";
const source = "00000000-0000-0000-0000-000000003c03";
const agent = "00000000-0000-0000-0000-0000000001a8";
const base = `/api/v1/organizations/${org}`;
async function request(
  page: Page,
  path: string,
  method = "GET",
  body?: unknown,
) {
  const csrf = (await page.context().cookies()).find(
    (c) => c.name === "openfox_csrf",
  )?.value;
  const response = await page.request.fetch(path, {
    method,
    data: body,
    headers: csrf ? { "X-CSRF-Token": csrf } : {},
  });
  expect(response.ok(), `${method} ${path}: ${response.status()}`).toBeTruthy();
  return response.json();
}
test("C04 creates only the selected prefix after explicit choices, remains private and survives reload", async ({
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
  const original = await request(
    page,
    `${base}/conversations/${source}/messages`,
  );
  const before = await request(page, `${base}/conversations?limit=100`);
  await page.goto(`/o/${org}/chat/${source}`);
  const selected = page.locator(
    '[data-message-id="00000000-0000-0000-0000-000000003c32"]',
  );
  await selected.getByRole("button", { name: "从此处分支" }).click();
  let dialog = page.getByRole("dialog");
  await expect(
    dialog.getByRole("button", { name: "创建分支会话" }),
  ).toBeDisabled();
  await dialog.getByRole("button", { name: "取消" }).click();
  expect(
    (await request(page, `${base}/conversations?limit=100`)).conversations
      .length,
  ).toBe(before.conversations.length);
  await selected.getByRole("button", { name: "从此处分支" }).click();
  dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("来源撤权或删除后副本仍保留");
  await expect(dialog).toContainText("附件和工作目录中的文件不复制");
  await dialog
    .getByRole("combobox", { name: "目标 Agent", exact: true })
    .selectOption(agent);
  await expect(
    dialog.getByRole("button", { name: "创建分支会话" }),
  ).toBeDisabled();
  await dialog
    .getByRole("combobox", { name: "本次工作目录", exact: true })
    .selectOption("DEFAULT");
  const createdPromise = page.waitForResponse(
    (r) =>
      r.url().endsWith(`/conversations/${source}/fork`) &&
      r.request().method() === "POST",
  );
  await dialog.getByRole("button", { name: "创建分支会话" }).click();
  const response = await createdPromise;
  expect(response.status()).toBe(201);
  const fork = await response.json();
  expect(fork.visibility).toBe("PRIVATE");
  expect(fork.fork_prefix_seq).toBe(2);
  expect(fork).not.toHaveProperty("parent_conversation_id");
  await page.waitForURL(new RegExp(`/chat/${fork.id}`));
  await expect(page.getByRole("note")).toContainText("不复制");
  await expect(
    page
      .locator("[data-message-id]")
      .filter({ hasText: "C03 later private context" }),
  ).toHaveCount(0);
  const copied = await request(
    page,
    `${base}/conversations/${fork.id}/messages`,
  );
  expect(copied.messages.map((m: { body: string }) => m.body)).toEqual(
    original.messages.slice(0, 2).map((m: { body: string }) => m.body),
  );
  for (const m of copied.messages) {
    expect(m.is_fork_history).toBe(true);
    expect(m.run_id).toBeNull();
    expect(m.attachments).toEqual([]);
    expect(original.messages.some((o: { id: string }) => o.id === m.id)).toBe(
      false,
    );
  }
  expect((await request(page, `${base}/conversations/${fork.id}`)).title).toBe(
    "分支会话",
  );
  await page.reload();
  await expect(page.getByRole("note")).toContainText("最近 20 条");
  await expect(
    page
      .locator("[data-message-id]")
      .filter({ hasText: "C03 selected Agent text" }),
  ).toBeVisible();
  expect(
    await request(page, `${base}/conversations/${source}/messages`),
  ).toEqual(original);
});
