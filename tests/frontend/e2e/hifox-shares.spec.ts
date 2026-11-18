import { expect, test, type Page } from "@playwright/test";

test.skip(
  process.env.OPENFOX_HIFOX_BATCH5_E2E !== "1",
  "requires the isolated batch 5 fixture and test encryption key",
);
const org = "00000000-0000-0000-0000-0000000000a1";
const conversation = "00000000-0000-0000-0000-000000003c03";
const base = `/api/v1/organizations/${org}`;
const sourcePath = `${base}/conversations/${conversation}`;

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
  body?: unknown,
) {
  const csrf = (await page.context().cookies()).find(
    (cookie) => cookie.name === "openfox_csrf",
  )?.value;
  const response = await page.request.fetch(path, {
    method,
    data: body,
    headers: csrf ? { "X-CSRF-Token": csrf } : {},
  });
  // Do not print bearer tokens from share responses on failure.
  expect(response.ok(), `${method} ${path}: ${response.status()}`).toBeTruthy();
  return response.json();
}

test("C03 single user and Agent messages remain recoverable after refresh and revocable", async ({
  page,
  browser,
}) => {
  await login(page, "alice@example.test");
  const selectedUser = "C03 selected member reply";
  const userMessage = await request(page, `${sourcePath}/messages`, "POST", {
    client_message_id: crypto.randomUUID(),
    body: selectedUser,
    reply_to_id: "00000000-0000-0000-0000-000000003c31",
  });
  const contextOptions = {
    baseURL: page.url().split("/o/")[0],
    locale: "zh-CN",
  };
  const memberContext = await browser.newContext(contextOptions);
  const anonymousContext = await browser.newContext(contextOptions);
  try {
    const member = await memberContext.newPage();
    await login(member, "carol@example.test");
    expect((await member.request.get(sourcePath)).status()).toBe(404);
    const anonymous = await anonymousContext.newPage();
    for (const [seq, body, title] of [
      [2, "C03 selected Agent text", "C03 Agent snapshot"],
      [userMessage.seq, selectedUser, "C03 member snapshot"],
    ] as const) {
      await page.goto(`/o/${org}/chat/${conversation}`);
      const shareButton = page
        .locator(`[data-seq="${seq}"]`)
        .getByRole("button", { name: "分享这条消息", exact: true });
      await shareButton.click();
      let dialog = page.getByRole("dialog", { name: "分享快照", exact: true });
      await dialog.getByLabel("标题（可选）", { exact: true }).fill(title);
      await dialog
        .getByRole("button", { name: "创建链接", exact: true })
        .click();
      dialog = page.getByRole("dialog", { name: "链接已创建", exact: true });
      const link = await dialog
        .getByRole("textbox", { name: "分享链接", exact: true })
        .inputValue();
      expect(new URL(link).pathname).toBe("/share");
      expect(new URL(link).search).toBe("");
      const view = await request(page, "/api/v1/share-snapshots/view", "POST", {
        token: new URL(link).hash.slice(1),
      });
      expect(view.items).toHaveLength(1);
      expect(view.items[0].body).toBe(body);
      expect(view.scope).toBe("ORGANIZATION");
      expect(JSON.stringify(view).includes("C03 earlier private context")).toBe(
        false,
      );
      expect(JSON.stringify(view).includes("C03 later private context")).toBe(
        false,
      );
      await anonymous.goto(link);
      await expect(
        anonymous.getByRole("heading", { name: "该内容不可用" }),
      ).toBeVisible();
      await expect(
        anonymous.getByRole("link", { name: "登录后查看" }),
      ).toHaveAttribute("href", "/login");
      await member.goto(link);
      await expect(member.getByText(body, { exact: true })).toBeVisible();
      await expect(member.getByRole("listitem")).toHaveCount(1);
      await expect(
        member.getByText("C03 earlier private context", { exact: true }),
      ).toHaveCount(0);

      // Lose the creation dialog's in-memory URL; recover from persisted ciphertext.
      await page.reload();
      await shareButton.click();
      await page
        .getByRole("dialog", { name: "分享快照", exact: true })
        .getByRole("button", { name: "管理分享链接", exact: true })
        .click();
      const management = page.getByRole("dialog", {
        name: "分享链接",
        exact: true,
      });
      const row = management.getByRole("listitem").filter({ hasText: title });
      await row.getByRole("button", { name: "复制链接", exact: true }).click();
      const restored = management.getByRole("textbox", {
        name: "分享链接",
        exact: true,
      });
      await expect(restored).toBeVisible();
      expect(
        (await restored.inputValue()) === link,
        "recovered bearer must be identical without logging its value",
      ).toBe(true);
      await row
        .getByRole("button", { name: `撤销 ${title}`, exact: true })
        .click();
      await page
        .getByRole("dialog", { name: "撤销这个分享链接？", exact: true })
        .getByRole("button", { name: "撤销", exact: true })
        .click();
      await expect(row.getByText("已撤销", { exact: true })).toBeVisible();
      await member.reload();
      await expect(
        member.getByRole("heading", { name: "该内容不可用" }),
      ).toBeVisible();
    }
  } finally {
    await memberContext.close();
    await anonymousContext.close();
  }
});
