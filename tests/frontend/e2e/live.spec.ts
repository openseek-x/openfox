import { expect, test, type Page } from "@playwright/test";

// 端到端：验证码登录 → 新建任务（指派 carol）→ 看板拖动改状态 → 评论 @carol → carol 的收件箱。
// 只在 tests/frontend/run-live.sh 启动的真实后端上运行（它设置万能验证码与种子账号）。
const org = "00000000-0000-0000-0000-0000000000a1";
const space = "00000000-0000-0000-0000-0000000000aa";
const otherOrg = "00000000-0000-0000-0000-0000000000b1";

async function login(page: Page, email: string) {
  await page.goto("/login");
  await page.getByLabel("邮箱", { exact: true }).pressSequentially(email);
  await page.getByRole("button", { name: "继续" }).click();
  await page.getByLabel("第 1 位").waitFor();
  await page
    .getByLabel("第 1 位")
    .pressSequentially(process.env.OPENFOX_LIVE_OTP_CODE!);
  await page.waitForURL(/\/o\//);
  // 种子组织不是通过首登流程创建的，跳过首登向导
  await page.evaluate(
    (key) => localStorage.setItem(key, JSON.stringify({ step: "done" })),
    `openfox:onboarding:${org}`,
  );
}

test("验证码登录、建任务、看板拖动、评论 @、收件箱与租户隔离", async ({
  page,
  browser,
}) => {
  test.skip(
    !process.env.OPENFOX_LIVE_OTP_CODE,
    "Run tests/frontend/run-live.sh against a disposable test database",
  );
  const title = `Live task ${Date.now()}`;
  await login(page, process.env.OPENFOX_LIVE_EMAIL ?? "alice@example.test");

  // 新建任务：快捷键 C，指派给 carol，优先级高
  await page.goto(`/o/${org}/spaces/${space}/tasks`);
  await expect(page.getByRole("heading", { name: "任务" })).toBeVisible();
  await page.keyboard.press("c");
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("textbox", { name: "标题" }).pressSequentially(title);
  await dialog.getByRole("button", { name: /^处理人/ }).click();
  await page.keyboard.type("carol");
  await page.getByRole("option", { name: /carol@example\.test/ }).click();
  await dialog.getByRole("button", { name: /^优先级/ }).click();
  await page.getByRole("option", { name: "高" }).click();
  const created = page.waitForResponse(
    (r) => r.request().method() === "POST" && r.url().endsWith("/tasks"),
  );
  await dialog.getByRole("button", { name: "创 建" }).click();
  expect((await created).status()).toBe(201);

  // 看板拖动：从 Ready for work 拖到折叠的 Building 列
  const card = page.getByRole("button", { name: new RegExp(title) }).first();
  await expect(card).toBeVisible();
  const target = page.getByText("Building", { exact: true }).first();
  const from = (await card.boundingBox())!;
  const to = (await target.boundingBox())!;
  const moved = page.waitForResponse(
    (r) => r.request().method() === "PATCH" && r.url().includes("/tasks/"),
  );
  await page.mouse.move(from.x + from.width / 2, from.y + 20);
  await page.mouse.down();
  await page.mouse.move(from.x + from.width / 2 + 10, from.y + 30, {
    steps: 5,
  });
  await page.mouse.move(to.x + to.width / 2, to.y + to.height / 2, {
    steps: 20,
  });
  await page.mouse.up();
  const patch = await moved;
  expect(patch.status()).toBe(200);
  expect(patch.request().postDataJSON()).toMatchObject({
    status_id: "00000000-0000-0000-0000-000000000113",
  });
  await page.reload();
  await expect(
    page.getByRole("region", { name: /^Building 1$/ }).getByText(title),
  ).toBeVisible();

  // 详情抽屉：评论并 @carol
  await page
    .getByRole("button", { name: new RegExp(title) })
    .first()
    .click();
  const drawer = page.getByRole("dialog");
  const box = drawer.getByRole("textbox", { name: "评论..." });
  await box.click();
  await box.pressSequentially("请看一下 @car");
  await page.getByRole("option", { name: /carol@example\.test/ }).click();
  await box.pressSequentially("谢谢");
  const commented = page.waitForResponse(
    (r) => r.request().method() === "POST" && r.url().endsWith("/comments"),
  );
  await drawer.getByRole("button", { name: "发送评论" }).click();
  const comment = await commented;
  expect(comment.status()).toBe(201);
  expect(comment.request().postDataJSON()).toMatchObject({
    body: "请看一下 @carol@example.test 谢谢",
    mentions: [{ user_id: "00000000-0000-0000-0000-0000000000c3" }],
  });
  await expect(
    drawer.getByText("请看一下 @carol@example.test 谢谢"),
  ).toBeVisible();

  // 跨租户：界面不进入别的组织；直接请求别的组织的接口返回 404
  await page.goto(`/o/${otherOrg}/spaces/${space}/tasks`);
  await page.waitForURL(new RegExp(`/o/${org}`));
  const denied = await page.request.get(
    `/api/v1/organizations/${otherOrg}/spaces/${space}/tasks`,
  );
  expect(denied.status()).toBe(404);

  // carol 的收件箱：@ 归「重要」，点开进入任务详情
  const context = await browser.newContext();
  const carol = await context.newPage();
  await login(carol, "carol@example.test");
  await carol.goto(`/o/${org}/inbox`);
  const row = carol.getByRole("listitem").filter({ hasText: title });
  await expect(row).toContainText("@了你");
  await carol.getByRole("radio", { name: /^重要/ }).click();
  await expect(row).toBeVisible();
  await row.getByRole("button", { name: new RegExp(title) }).click();
  await expect(carol).toHaveURL(/\?task=/);
  await expect(carol.getByRole("dialog")).toContainText(title);
  await context.close();
});

test("桌面验收空间可开启分拣，创建时可以保留或改选默认状态", async ({
  page,
}) => {
  test.skip(
    !process.env.OPENFOX_LIVE_OTP_CODE,
    "Requires a disposable fixture database",
  );
  await login(page, "alice@example.test");
  await page.goto(`/o/${org}/settings/spaces/${space}/triage`);
  const toggle = page.getByRole("switch", { name: "启用分拣台" });
  await toggle.click();
  await expect(toggle).toBeChecked();

  let queuedID = "";
  for (const changeStatus of [false, true]) {
    await page.goto(`/o/${org}/spaces/${space}/triage`);
    await page
      .getByRole("button", { name: "创建待分拣任务", exact: true })
      .first()
      .click();
    const dialog = page.getByRole("dialog");
    const chip = dialog.getByRole("button", { name: "状态: 待分拣" });
    await expect(chip).toBeVisible();
    if (changeStatus) {
      await chip.click();
      await page.getByRole("option", { name: "Building", exact: true }).click();
    }
    await dialog
      .getByRole("textbox", { name: "标题" })
      .fill(`Triage default ${Date.now()}`);
    const created = page.waitForResponse(
      (r) => r.request().method() === "POST" && r.url().endsWith("/tasks"),
    );
    await dialog.getByRole("button", { name: /^创\s*建$/ }).click();
    const response = await created;
    expect(response.status()).toBe(201);
    const task = await response.json();
    if (changeStatus) {
      expect(task.status_name).toBe("Building");
      expect(task.triage ?? null).toBeNull();
    } else {
      expect(task).toMatchObject({
        status_category: "TRIAGE",
        triage: { source: "member" },
      });
      queuedID = task.id;
    }
  }
  await page.goto(`/o/${org}/settings/spaces/${space}/triage`);
  await toggle.click();
  await page.getByRole("button", { name: "继续" }).click();
  await expect(toggle).not.toBeChecked();
  const after = await page.request.get(
    `/api/v1/organizations/${org}/spaces/${space}/tasks/${queuedID}`,
  );
  expect(await after.json()).toMatchObject({
    status_category: "BACKLOG",
    status_name: "待规划",
  });
});

test("分拣台从表单提交接受、拒绝、重复，待处理数更新且决定可重新读取", async ({
  page,
}) => {
  test.skip(
    !process.env.OPENFOX_LIVE_OTP_CODE,
    "Requires a disposable fixture database",
  );
  await login(page, "alice@example.test");
  const url = `/o/${org}/spaces/${space}/triage`;
  await page.goto(`/o/${org}/settings/spaces/${space}/triage`);
  const enabled = page.getByRole("switch", { name: "启用分拣台" });
  await expect(enabled).toBeVisible();
  if (!(await enabled.isChecked())) await enabled.click();
  await expect(enabled).toBeChecked();
  let target = { id: "", display_id: "" };
  for (const [action, label] of [
    ["accept", "接受"],
    ["decline", "拒绝"],
    ["duplicate", "标记重复"],
  ] as const) {
    await page.goto(url);
    await page
      .getByRole("button", { name: "创建待分拣任务", exact: true })
      .first()
      .click();
    const dialog = page.getByRole("dialog");
    const title = `决定验收 ${action} ${Date.now()}`;
    await dialog.getByRole("textbox", { name: "标题" }).fill(title);
    const created = page.waitForResponse(
      (r) => r.request().method() === "POST" && r.url().endsWith("/tasks"),
    );
    await dialog.getByRole("button", { name: /^创\s*建$/ }).click();
    const response = await created;
    expect(response.status()).toBe(201);
    const task = await response.json();
    await page.goto(url + "?task=" + task.display_id);
    await page.getByRole("button", { name: label, exact: true }).click();
    const decided = page.waitForResponse(
      (r) =>
        r.request().method() === "POST" &&
        r.url().endsWith("/triage/" + action),
    );
    if (action === "duplicate") {
      const search = dialog.getByRole("combobox");
      // 上一次打开过的目标在刷新后的「最近」中仍可选。
      await expect(
        dialog
          .getByRole("option")
          .filter({ hasText: target.display_id.toUpperCase() }),
      ).toBeVisible();
      await search.fill(target.display_id);
      await expect(dialog.getByRole("option")).toHaveCount(1);
      await expect(dialog.getByRole("option")).toContainText(
        target.display_id.toUpperCase(),
      );
      await search.press("Enter");
    } else {
      await dialog.getByRole("textbox", { name: "评论" }).fill("分拣验收");
      if (action === "decline")
        await dialog.getByRole("switch", { name: "订阅更新" }).click();
      else
        await expect(
          dialog.getByRole("button", { name: "状态: 待规划" }),
        ).toBeVisible();
      await dialog.getByRole("button", { name: label, exact: true }).click();
    }
    expect((await decided).status()).toBe(200);
    await expect(dialog).not.toBeVisible();
    await expect(page.getByLabel("0 个待分拣任务")).toBeVisible();
    await expect(
      page.getByRole("button", { name: new RegExp(title) }),
    ).toHaveAttribute("aria-pressed", "true");
    await expect(
      page.getByRole("button", { name: label, exact: true }),
    ).toHaveCount(0);
    await page.goto(url);
    await expect(
      page.getByRole("heading", { name: "暂无待分拣任务" }),
    ).toBeVisible();
    const persisted = await page.request.get(
      `/api/v1/organizations/${org}/spaces/${space}/tasks/${task.id}`,
    );
    expect(persisted.status()).toBe(200);
    const result = await persisted.json();
    expect(result.triage ?? null).toBeNull();
    expect(result.status_category).toBe(
      { accept: "BACKLOG", decline: "CANCELLED", duplicate: "DUPLICATE" }[
        action
      ],
    );
    if (action === "accept") target = task;
    if (action === "duplicate") expect(result.duplicate_of.id).toBe(target.id);
  }
});
