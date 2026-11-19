import { expect, test, type Page } from "@playwright/test";
test.skip(
  process.env.OPENFOX_HIFOX_R06_E2E !== "1",
  "requires isolated R06 fixture and ephemeral key",
);
const org = "00000000-0000-0000-0000-0000000000a1",
  space = "00000000-0000-0000-0000-0000000000aa";
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
  expect(
    response.ok(),
    `${method} ${path} = ${response.status()}`,
  ).toBeTruthy();
  return response.json();
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
test("R06 owner saves masked Runtime defaults and the conversation inherits them without showing source details", async ({
  page,
  browser,
}) => {
  await login(page, "alice@example.test");
  const registration = await request(
    page,
    `${base}/devices/registration-tokens`,
    "POST",
    { name: "R06 browser computer", scope: "ORGANIZATION" },
  );
  const registered = await request(page, "/api/v1/device/register", "POST", {
    token: registration.token,
    key_id: "r06-browser-key",
    public_key: Buffer.alloc(32, 7).toString("base64url"),
  });
  const device = await request(page, `${base}/devices/${registered.device_id}`);
  const model = "controlled-r06-model";
  await page.goto(`/o/${org}/settings/computers?computer=${device.id}`);
  const diagnostics = page.getByRole("button", { name: /Codex.*诊断/ });
  if (await diagnostics.count()) await diagnostics.click();
  await page
    .getByRole("button", { name: "Runtime 默认配置", exact: true })
    .first()
    .click();
  let dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("仅当前组织");
  await dialog.getByLabel("机器默认模型").fill(model);
  await dialog.getByLabel("变量名称").fill("CONTROLLED_API_KEY");
  await dialog.getByLabel("新秘密值").fill("controlled-browser-secret");
  await dialog.getByRole("button", { name: "应用变量" }).click();
  await dialog.getByRole("button", { name: "保存", exact: true }).click();
  await expect(dialog).toHaveCount(0);
  const saved = await request(
    page,
    `${base}/devices/${device.id}/runtime-defaults/codex`,
  );
  expect(saved.model).toBe(model);
  expect(saved.env_vars.map((v: { name: string }) => v.name)).toContain(
    "CONTROLLED_API_KEY",
  );
  expect(JSON.stringify(saved)).not.toContain("controlled-browser-secret");
  await page.reload();
  if (await diagnostics.count()) await diagnostics.click();
  await page
    .getByRole("button", { name: "Runtime 默认配置", exact: true })
    .first()
    .click();
  dialog = page.getByRole("dialog");
  await expect(dialog.getByLabel("机器默认模型")).toHaveValue(model);
  await expect(
    dialog.getByText("CONTROLLED_API_KEY", { exact: true }),
  ).toBeVisible();
  await expect(dialog.getByLabel("新秘密值")).toHaveValue("");
  await dialog.getByRole("button", { name: "取消" }).click();
  const agent = await request(page, `${base}/spaces/${space}/agents`, "POST", {
    name: "R06 browser Agent",
    kind: "CODE",
    visibility: "PRIVATE",
    config: {
      computer_id: device.id,
      grant_id: device.grant_id,
      runtime: "codex",
      model: "default",
    },
  });
  expect(agent.config.model_source).toBe("EXPLICIT");
  await page.goto(
    `/o/${org}/agents?agentSettings=${agent.id}&agentSection=runtime`,
  );
  await page
    .getByRole("button", {
      name: "电脑 & Runtime 设置电脑、Runtime，注入环境变量、自定义参数等。",
      exact: true,
    })
    .click();
  await page.getByRole("button", { name: "默认模型", exact: true }).click();
  await page.getByRole("option", { name: "继承机器模型", exact: true }).click();
  await expect
    .poll(
      async () =>
        (await request(page, `${base}/spaces/${space}/agents/${agent.id}`))
          .config.model_source,
    )
    .toBe("MACHINE");
  const conversation = await request(page, `${base}/conversations`, "POST", {
    space_id: space,
    agent_id: agent.id,
  });
  await page.goto(`/o/${org}/chat/${conversation.id}`);
  expect(
    (await request(page, `${base}/conversations/${conversation.id}`)).effective,
  ).toMatchObject({
    model,
    model_source: "computer",
    environment: [{ name: "CONTROLLED_API_KEY", source: "computer" }],
  });
  await expect(page.getByRole("textbox", { name: "输入消息" })).toBeVisible();
  await expect(
    page.getByText("有效模型与变量来源", { exact: true }),
  ).toHaveCount(0);
  await expect(
    page.getByText("CONTROLLED_API_KEY", { exact: true }),
  ).toHaveCount(0);
  const other = await browser.newContext();
  const member = await other.newPage();
  await login(member, "carol@example.test");
  const denied = await member.request.get(
    `${base}/devices/${device.id}/runtime-defaults/codex`,
  );
  expect(denied.status()).toBe(404);
  await member.goto(`/o/${org}/settings/computers?computer=${device.id}`);
  await expect(
    member.getByRole("button", { name: "Runtime 默认配置", exact: true }),
  ).toHaveCount(0);
  await other.close();
});
