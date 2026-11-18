import { expect, test, type Page } from "@playwright/test";

test.skip(
  process.env.OPENFOX_HIFOX_BATCH4_E2E !== "1",
  "requires the isolated batch 4 fixture database",
);
const org = "00000000-0000-0000-0000-0000000000a1";
const space = "00000000-0000-0000-0000-0000000000aa";
const typesPath = `/api/v1/organizations/${org}/spaces/${space}/task-types`;
const settingsPath = `/o/${org}/settings/spaces/${space}/task-types`;

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
  expect(
    response.ok(),
    `${method} ${path}: ${response.status()} ${await response.text()}`,
  ).toBeTruthy();
  return response.json();
}

test("S04/S06 pointer and keyboard order, default type and commit prefix persist", async ({
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
  const initial = (await request(page, typesPath)).task_types;
  const template = initial.find(
    (type: { id: string }) =>
      type.id === "00000000-0000-0000-0000-000000000111",
  );
  const create = (name: string) =>
    request(page, typesPath, "POST", {
      name,
      status_ids: template.statuses.map(
        (status: { status_id: string }) => status.status_id,
      ),
      default_status_id: template.statuses.find(
        (status: { default: boolean }) => status.default,
      ).status_id,
    });
  const first = await create(`S04 first ${Date.now()}`);
  const second = await create(`S04 second ${Date.now()}`);
  const expected = initial.map((type: { id: string }) => type.id);
  const order = async () =>
    (await request(page, typesPath)).task_types.map(
      (type: { id: string }) => type.id,
    );
  expect(await order()).toEqual([...expected, first.id, second.id]);
  await page.goto(settingsPath);
  const handle = page.getByRole("button", {
    name: `调整「${second.name}」顺序`,
    exact: true,
  });
  await handle.focus();
  await handle.press("Space");
  const dragStatus = page
    .getByRole("status")
    .filter({ hasText: "Draggable item" });
  await expect(dragStatus).toContainText(`over droppable area ${second.id}`);
  // The keyboard sensor registers listeners and measures droppable rows after activation.
  await page.evaluate(
    () =>
      new Promise<void>((resolve) =>
        requestAnimationFrame(() => requestAnimationFrame(() => resolve())),
      ),
  );
  await handle.press("ArrowUp");
  await expect(dragStatus).toContainText(`over droppable area ${first.id}`);
  const keyboardSaved = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      response.url().endsWith(`${typesPath}/reorder`),
  );
  await handle.press("Space");
  expect((await keyboardSaved).status()).toBe(200);
  await expect.poll(order).toEqual([...expected, second.id, first.id]);
  await page.reload();
  const list = page.getByRole("list", { name: "任务类型 & 状态", exact: true });
  const names = () =>
    list
      .getByRole("button")
      .evaluateAll((nodes) =>
        nodes
          .map((node) => node.getAttribute("aria-label"))
          .filter(
            (name) => name?.startsWith("S04 ") && !name.endsWith("更多操作"),
          ),
      );
  await expect.poll(names).toEqual([second.name, first.name]);

  await handle.scrollIntoViewIfNeeded();
  const from = await handle.boundingBox();
  const to = await list
    .getByRole("button", { name: first.name, exact: true })
    .boundingBox();
  expect(from && to).toBeTruthy();
  const pointerSaved = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      response.url().endsWith(`${typesPath}/reorder`),
  );
  await page.mouse.move(from!.x + from!.width / 2, from!.y + from!.height / 2);
  await page.mouse.down();
  await page.mouse.move(from!.x + from!.width / 2, to!.y + to!.height / 2, {
    steps: 12,
  });
  await page.mouse.up();
  expect((await pointerSaved).status()).toBe(200);
  await expect.poll(order).toEqual([...expected, first.id, second.id]);
  const after = (await request(page, typesPath)).task_types;
  expect(
    after
      .filter((type: { is_default: boolean }) => type.is_default)
      .map((type: { id: string }) => type.id),
  ).toEqual(
    initial
      .filter((type: { is_default: boolean }) => type.is_default)
      .map((type: { id: string }) => type.id),
  );

  await page.goto(`${settingsPath}/${second.id}`);
  const prefix = page.getByLabel("提交类型前缀", { exact: true });
  await expect(prefix).toHaveValue("");
  await expect(prefix).toHaveAttribute("placeholder", "feat");
  await prefix.fill("Fix-UI");
  const saved = page.waitForResponse(
    (response) =>
      response.request().method() === "PATCH" &&
      response.url().endsWith(`${typesPath}/${second.id}`),
  );
  await prefix.press("Tab");
  expect((await saved).status()).toBe(200);
  await page.reload();
  await expect(prefix).toHaveValue("Fix-UI");
  await prefix.fill("bad prefix");
  await prefix.press("Tab");
  await expect(page.getByRole("alert")).toBeVisible();
  expect(
    (await request(page, typesPath)).task_types.find(
      (type: { id: string }) => type.id === second.id,
    ).commit_type,
  ).toBe("Fix-UI");
  await prefix.fill("");
  const cleared = page.waitForResponse(
    (response) =>
      response.request().method() === "PATCH" &&
      response.url().endsWith(`${typesPath}/${second.id}`),
  );
  await prefix.press("Tab");
  expect((await cleared).status()).toBe(200);
  await page.reload();
  await expect(prefix).toHaveValue("");
  expect(
    (await request(page, typesPath)).task_types.find(
      (type: { id: string }) => type.id === second.id,
    ).commit_type,
  ).toBeNull();
});
