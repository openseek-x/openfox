import { deviceRegistration } from "./fixtures/deviceRegistration";
import { generateKeyPairSync, sign } from "node:crypto";
import { expect, test, type Page } from "@playwright/test";
test.skip(
  process.env.OPENFOX_HIFOX_R03_E2E !== "1",
  "requires isolated R03 server, PG and ephemeral key",
);
const org = "00000000-0000-0000-0000-0000000000a1",
  space = "00000000-0000-0000-0000-0000000000aa",
  base = `/api/v1/organizations/${org}`;
async function request(
  page: Page,
  path: string,
  method = "GET",
  body?: unknown,
) {
  const csrf = (await page.context().cookies()).find(
    (v) => v.name === "openfox_csrf",
  )?.value;
  const response = await page.request.fetch(path, {
    method,
    data: body,
    headers: csrf ? { "X-CSRF-Token": csrf } : {},
  });
  expect(
    response.ok(),
    `${method} ${path} ${response.status()} ${await response.text()}`,
  ).toBeTruthy();
  return response.status() === 204 ? null : response.json();
}
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
async function computer(page: Page) {
  const keys = generateKeyPairSync("ed25519"),
    keyID = "r03-browser-key";
  const token = await request(
    page,
    `${base}/devices/registration-tokens`,
    "POST",
    { name: "R03 controlled computer", scope: "ORGANIZATION" },
  );
  const device = await request(page, "/api/v1/device/register", "POST", {
    ...deviceRegistration(token.token, keyID, keys),
  });
  const challenge = await request(
    page,
    "/api/v1/device/auth/challenge",
    "POST",
    { device_id: device.device_id, key_id: keyID },
  );
  const pieces = [Buffer.from("openfox-device-auth-v1")];
  for (const value of [
    Buffer.from(challenge.challenge_id),
    Buffer.from(device.device_id),
    Buffer.from(keyID),
    Buffer.from(challenge.nonce, "base64url"),
    Buffer.from(challenge.challenge, "base64url"),
  ]) {
    const len = Buffer.alloc(4);
    len.writeUInt32BE(value.length);
    pieces.push(len, value);
  }
  const session = await request(page, "/api/v1/device/auth/token", "POST", {
    challenge_id: challenge.challenge_id,
    device_id: device.device_id,
    key_id: keyID,
    signature: sign(null, Buffer.concat(pieces), keys.privateKey).toString(
      "base64url",
    ),
  });
  const heartbeat = async (ready = true) => {
    const response = await page.request.post("/api/v1/device/heartbeat", {
      headers: { Authorization: `Bearer ${session.token}` },
      data: {
        center_epoch: device.center_epoch,
        protocol_major: 1,
        protocol_minor: 0,
        daemon_version: "controlled-r03-browser",
        operating_system: "darwin",
        architecture: "arm64",
        capabilities: {
          runtimes: {
            codebuddy: {
              version: ready ? "2.184.0" : "2.185.0",
              runnable: false,
              protocol_ready: ready,
              requires_run_configuration: ready,
              login_status: "UNKNOWN",
              reason: ready
                ? "CodeBuddy requires explicit run configuration"
                : "CodeBuddy requires version 2.184.0",
              approval_modes: ["READ_ONLY"],
              collaboration_modes: ["DEFAULT", "PLAN"],
              models: [
                {
                  id: "default",
                  display_name: "Configured CODEBUDDY_MODEL",
                  is_default: true,
                },
              ],
              models_error: "Explicit model required",
            },
          },
        },
      },
    });
    expect(response.ok()).toBeTruthy();
  };
  await heartbeat();
  return {
    ...(await request(page, `${base}/devices/${device.device_id}`)),
    heartbeat,
  };
}
test("R03 configured protocol state, masked defaults, read-only controls and persisted model", async ({
  page,
  browser,
}) => {
  await login(page, "alice@example.test");
  const device = await computer(page);
  await page.goto(`/o/${org}/settings/computers?computer=${device.id}`);
  await expect(
    page.getByText("已检测，运行前检查配置", { exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: /CodeBuddy.*诊断/ }).click();
  const card = page
    .getByRole("button", { name: /CodeBuddy.*诊断/ })
    .locator("..");
  await expect(card).toContainText("UNKNOWN");
  await card
    .getByRole("button", { name: "Runtime 默认配置", exact: true })
    .click();
  let dialog = page.getByRole("dialog");
  await dialog.getByLabel("机器默认模型").fill("browser-r03-model");
  for (const [name, value] of Object.entries({
    CODEBUDDY_API_KEY: "CONTROLLED-BROWSER-R03-SECRET",
    CODEBUDDY_BASE_URL: "http://127.0.0.1:1/v1",
    CODEBUDDY_MODEL: "browser-r03-default",
  })) {
    await dialog.getByLabel("变量名称").fill(name);
    await dialog.getByLabel("新秘密值").fill(value);
    await dialog.getByRole("button", { name: "应用变量" }).click();
  }
  await dialog.getByRole("button", { name: "保存", exact: true }).click();
  await expect(dialog).toHaveCount(0);
  const defaults = await request(
    page,
    `${base}/devices/${device.id}/runtime-defaults/codebuddy`,
  );
  expect(defaults.env_vars).toHaveLength(3);
  expect(JSON.stringify(defaults)).not.toContain(
    "CONTROLLED-BROWSER-R03-SECRET",
  );
  await page.reload();
  await page.getByRole("button", { name: /CodeBuddy.*诊断/ }).click();
  await card
    .getByRole("button", { name: "Runtime 默认配置", exact: true })
    .click();
  dialog = page.getByRole("dialog");
  await expect(dialog.getByLabel("机器默认模型")).toHaveValue(
    "browser-r03-model",
  );
  await expect(
    dialog.getByText("CODEBUDDY_API_KEY", { exact: true }),
  ).toBeVisible();
  await expect(dialog.getByLabel("新秘密值")).toHaveValue("");
  await dialog.getByRole("button", { name: "取消" }).click();
  const agent = await request(page, `${base}/spaces/${space}/agents`, "POST", {
    name: "R03 browser Agent",
    kind: "CODE",
    visibility: "PRIVATE",
    config: {
      computer_id: device.id,
      grant_id: device.grant_id,
      runtime: "codebuddy",
      model: "default",
    },
  });
  const agentPath = `${base}/spaces/${space}/agents/${agent.id}`;
  await page.goto(`/o/${org}/agents?agentSettings=${agent.id}`);
  const drawer = page.getByRole("complementary", {
    name: "R03 browser Agent 的设置",
  });
  await drawer.getByRole("button", { name: /电脑 & Runtime/ }).click();
  await expect(drawer.getByText(/仅支持 macOS arm64 \/ 2.184.0/)).toBeVisible();
  await drawer
    .getByRole("button", { name: "权限模式默认", exact: true })
    .click();
  const modes = page.getByRole("listbox", { name: "权限模式默认" });
  await expect(modes.getByRole("option").nth(2)).toHaveAttribute(
    "aria-disabled",
    "true",
  );
  await expect(modes.getByRole("option").nth(3)).toHaveAttribute(
    "aria-disabled",
    "true",
  );
  await page.keyboard.press("Escape");
  await drawer
    .getByRole("button", { name: "协作模式默认", exact: true })
    .click();
  await page.getByRole("option", { name: /计划模式/ }).click();
  await expect
    .poll(
      async () => (await request(page, agentPath)).config.collaboration_mode,
    )
    .toBe("PLAN");
  await drawer.getByRole("button", { name: "默认模型", exact: true }).click();
  await page.getByRole("option", { name: "手动设置模型", exact: true }).click();
  await drawer
    .getByLabel("手动设置模型", { exact: true })
    .fill("browser-r03-explicit");
  await drawer
    .getByRole("button", { name: "保存", exact: true })
    .first()
    .click();
  await expect
    .poll(async () => (await request(page, agentPath)).config.model)
    .toBe("browser-r03-explicit");
  await page.reload();
  await drawer.getByRole("button", { name: /电脑 & Runtime/ }).click();
  await expect(drawer.getByLabel("手动设置模型", { exact: true })).toHaveValue(
    "browser-r03-explicit",
  );
  const other = await browser.newContext();
  const member = await other.newPage();
  await login(member, "carol@example.test");
  expect(
    (
      await member.request.get(
        `${base}/devices/${device.id}/runtime-defaults/codebuddy`,
      )
    ).status(),
  ).toBe(404);
  await other.close();
  await device.heartbeat(false);
  await page.goto(`/o/${org}/settings/computers?computer=${device.id}`);
  await expect(
    page.getByText("已检测，运行前检查配置", { exact: true }),
  ).toHaveCount(0);
  await expect(page.getByText(/requires version 2.184.0/)).toBeVisible();
});
