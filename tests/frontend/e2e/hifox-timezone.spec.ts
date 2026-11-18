import { expect, test } from "@playwright/test";

test.skip(
  process.env.OPENFOX_HIFOX_BATCH6_E2E !== "1",
  "requires a disposable database with hifox-timezone-fixture.sql",
);
test.use({ timezoneId: "Europe/Paris" });
const org = "00000000-0000-0000-0000-0000000000a1";

test("R12 remote device zones and unknown survive refresh independently of browser zone", async ({
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
  expect(
    await page.evaluate(() => Intl.DateTimeFormat().resolvedOptions().timeZone),
  ).toBe("Europe/Paris");
  for (const [suffix, zone] of [
    ["11", "Asia/Shanghai"],
    ["22", "America/New_York"],
    ["33", null],
  ]) {
    const device = `00000000-0000-0000-0000-0000000000${suffix}`;
    const response = await page.request.get(
      `/api/v1/organizations/${org}/devices/${device}`,
    );
    expect(response.status()).toBe(200);
    expect((await response.json()).timezone).toBe(zone);
    await page.goto(`/o/${org}/settings/computers?computer=${device}`);
    await expect(
      page.getByText(zone || "未知（后台服务未上报）", { exact: true }),
    ).toBeVisible();
    await expect(
      page.getByText(
        "由这台电脑的后台服务上报；Agent 工作时间使用其单独设置的时区。",
        { exact: true },
      ),
    ).toBeVisible();
    await page.reload();
    await expect(
      page.getByText(zone || "未知（后台服务未上报）", { exact: true }),
    ).toBeVisible();
  }
});
