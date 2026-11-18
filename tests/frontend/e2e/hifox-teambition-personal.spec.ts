import { readFileSync } from "node:fs";
import { expect, test } from "@playwright/test";

const fixturePath = process.env.OPENFOX_P02_BROWSER_FILE;
test.skip(!fixturePath, "requires the isolated Teambition TLS/HTTP/PG harness");
const fixture = fixturePath
  ? JSON.parse(readFileSync(fixturePath, "utf8"))
  : {};
test.use({
  proxy: fixture.proxy
    ? { server: fixture.proxy, bypass: "127.0.0.1,localhost" }
    : undefined,
  ignoreHTTPSErrors: true,
});

test("P02 real popup SSO, explicit confirmation, refresh, disconnect and invalid callback", async ({
  page,
  context,
}) => {
  const login = await page.request.post("/api/v1/auth/login", {
    data: {
      email: fixture.email,
      password: fixture.password,
      client_kind: "web",
    },
  });
  expect(login.status()).toBe(201);
  const accountPath = `/o/${fixture.organization_id}/settings/account-binding`;
  await page.goto(accountPath);
  await page.evaluate(
    (org) =>
      localStorage.setItem(
        `openfox:onboarding:${org}`,
        JSON.stringify({ step: "done" }),
      ),
    fixture.organization_id,
  );
  const row = page.getByTestId("binding-teambition");
  await expect(
    row.getByRole("button", { name: "绑定", exact: true }),
  ).toBeEnabled();
  const popupPromise = page.waitForEvent("popup");
  await row.getByRole("button", { name: "绑定", exact: true }).click();
  const popup = await popupPromise;
  await expect(popup).toHaveURL(
    /https:\/\/open.teambition.com\/api\/oauth\/sso/,
  );
  await popup
    .getByRole("link", { name: "Authorize local test identity" })
    .click();
  await expect(
    popup.getByRole("heading", { name: "确认 Teambition 本人身份" }),
  ).toBeVisible();
  await expect(
    popup.getByText("Teambition member · site-member@example.test"),
  ).toBeVisible();
  const completionURL = popup.url();
  expect(completionURL).not.toMatch(/[?&](code|state)=/);
  await popup.reload();
  await expect(
    popup.getByRole("button", { name: "确认绑定此身份" }),
  ).toBeVisible();
  await expect(
    row.getByRole("button", { name: "已连接", exact: true }),
  ).toHaveCount(0);
  await popup.screenshot({
    path: "docs/development-records/evidence/p02/browser-confirmation.png",
  });
  await popup.getByRole("button", { name: "确认绑定此身份" }).click();
  await expect.poll(() => popup.isClosed()).toBe(true);
  await expect(
    row.getByRole("button", { name: "已连接", exact: true }),
  ).toBeVisible();
  await page.reload();
  await expect(row).toContainText("Teambition member");
  const refreshed = await context.newPage();
  await refreshed.goto(completionURL);
  await expect(refreshed.getByText("已连接", { exact: true })).toBeVisible();
  await expect(
    refreshed.getByRole("button", { name: "确认绑定此身份" }),
  ).toHaveCount(0);
  await refreshed.close();
  await row.getByRole("button", { name: "已连接", exact: true }).click();
  await page
    .getByRole("menuitem", { name: "断开 p02-company", exact: true })
    .click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "断开连接", exact: true })
    .click();
  await expect(
    row.getByRole("button", { name: "绑定", exact: true }),
  ).toBeEnabled();
  await page.goto(
    "/api/v1/integrations/teambition/callback?state=invalid&code=fixture-invalid-code",
  );
  await expect(
    page.getByText("授权未完成或已失效，请返回重新发起。"),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "确认绑定此身份" }),
  ).toHaveCount(0);
  await page.screenshot({
    path: "docs/development-records/evidence/p02/browser-rejected.png",
  });
});
