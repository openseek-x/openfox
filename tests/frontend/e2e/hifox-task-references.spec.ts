import { expect, test, type Page } from "@playwright/test";

test.skip(
  process.env.OPENFOX_HIFOX_BATCH6_E2E !== "1",
  "requires an isolated migrated database and controlled conversation fixture",
);
const org = "00000000-0000-0000-0000-0000000000a1";
const space = "00000000-0000-0000-0000-0000000000aa";
const recipient = "00000000-0000-0000-0000-0000000000c3";
const conversation = "00000000-0000-0000-0000-000000003c03";
const base = `/api/v1/organizations/${org}`;
const targetPath = `${base}/conversations/${conversation}`;

async function login(page: Page, email: string) {
  await page.goto("/login");
  await page.getByLabel("邮箱", { exact: true }).fill(email);
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
}

async function request(
  page: Page,
  path: string,
  method = "GET",
  data?: unknown,
) {
  const csrf = (await page.context().cookies()).find(
    (c) => c.name === "openfox_csrf",
  )?.value;
  const response = await page.request.fetch(path, {
    method,
    data,
    headers: csrf ? { "X-CSRF-Token": csrf } : {},
  });
  expect(response.ok(), `${method} ${path}: ${response.status()}`).toBeTruthy();
  return response.status() === 204 ? null : response.json();
}

test("T04 task reference is passive, persistent and follows each reader's current permissions", async ({
  page,
  browser,
}) => {
  await login(page, "alice@example.test");
  const source = await request(page, `${base}/spaces`, "POST", {
    name: "T04 private source",
    visibility: "private",
    copy_from_space_id: space,
  });
  const types = await request(page, `${base}/spaces/${source.id}/task-types`);
  let task = await request(page, `${base}/spaces/${source.id}/tasks`, "POST", {
    title: "T04 confidential task",
    type_id: types.task_types[0].id,
  });
  const taskPath = `${base}/spaces/${source.id}/tasks/${task.id}`;
  await request(page, `${targetPath}/members/${recipient}`, "PUT", {
    access_level: "COMMENTER",
  });
  const before = await request(page, `${targetPath}/messages`);
  await page.goto(
    `/o/${org}/spaces/${source.id}/tasks?task=${encodeURIComponent(task.display_id)}`,
  );
  const openForward = async () => {
    await page.getByRole("button", { name: "更多操作", exact: true }).click();
    await page
      .getByRole("menuitem", { name: "转发到会话", exact: true })
      .click();
    return page.getByRole("dialog", { name: "转发到会话", exact: true });
  };
  let dialog = await openForward();
  await dialog.getByRole("button", { name: "取消", exact: true }).click();
  expect((await request(page, `${targetPath}/messages`)).messages).toHaveLength(
    before.messages.length,
  );
  dialog = await openForward();
  await dialog
    .getByRole("radio", { name: "C03 private source", exact: true })
    .check();
  const sent = page.waitForResponse(
    (r) =>
      r.request().method() === "POST" &&
      r.url().endsWith(`${conversation}/references`),
  );
  await dialog.getByRole("button", { name: "确认转发", exact: true }).click();
  const response = await sent;
  expect(response.status()).toBe(201);
  const message = await response.json();
  expect(message.kind).toBe("REFERENCE");
  expect(message.run_id).toBeNull();
  expect(message.payload).toBeUndefined();
  await expect(dialog).toBeHidden();
  const messages = (await request(page, `${targetPath}/messages`)).messages;
  expect(messages).toHaveLength(before.messages.length + 1);
  await page.goto(`/o/${org}/chat/${conversation}`);
  let card = page.locator(`[data-seq="${message.seq}"]`);
  await expect(card.getByRole("link")).toContainText(task.title);
  await expect(
    card.getByRole("button", { name: "引用回复", exact: true }),
  ).toHaveCount(0);
  await page.reload();
  await expect(card.getByRole("link")).toContainText(task.title);

  const context = await browser.newContext({
    baseURL: page.url().split("/o/")[0],
    locale: "zh-CN",
  });
  try {
    const reader = await context.newPage();
    await login(reader, "carol@example.test");
    await reader.goto(`/o/${org}/chat/${conversation}`);
    const readerCard = reader.locator(`[data-seq="${message.seq}"]`);
    await expect(
      readerCard.getByText("任务不可用", { exact: true }),
    ).toBeVisible();
    const hidden = (
      await request(reader, `${targetPath}/messages`)
    ).messages.find((m: { id: string }) => m.id === message.id);
    for (const value of [task.title, task.id, source.id])
      expect(JSON.stringify(hidden)).not.toContain(value);

    await request(
      page,
      `${base}/spaces/${source.id}/members/${recipient}`,
      "PUT",
      { role: "MEMBER", status: "ACTIVE" },
    );
    await reader.reload();
    await expect(readerCard.getByRole("link")).toContainText(task.title);
    task = await request(page, taskPath, "PATCH", {
      version: task.version,
      title: "T04 current renamed title",
    });
    // A source event refreshes the open card rather than preserving a copied title.
    await expect(readerCard.getByRole("link")).toContainText(task.title);
    await request(
      page,
      `${base}/spaces/${source.id}/members/${recipient}`,
      "DELETE",
    );
    await expect(
      readerCard.getByText("任务不可用", { exact: true }),
    ).toBeVisible();
    await expect(readerCard.getByRole("link")).toHaveCount(0);
    await reader.reload();
    await expect(
      readerCard.getByText("任务不可用", { exact: true }),
    ).toBeVisible();
    await request(page, `${taskPath}?version=${task.version}`, "DELETE");
    await page.reload();
    card = page.locator(`[data-seq="${message.seq}"]`);
    await expect(card.getByText("任务不可用", { exact: true })).toBeVisible();
  } finally {
    await context.close();
  }
});
