import { expect, test, type Locator, type Page } from "@playwright/test";
import {
  detail,
  first,
  mockProfile,
  second,
  tasksPath,
} from "./fixtures/agentProfile";

const chat = "/o/o1/chat/c1";
const agents = "/o/o1/agents";
const trigger = (root: Page | Locator, name: string) =>
  root.getByRole("button", { name: `查看 ${name} 的信息`, exact: true });
const card = (page: Page, name: string) =>
  page.getByRole("dialog", { name: `查看 ${name} 的信息`, exact: true });
const turn = (page: Page) =>
  page.locator('[data-slot="agent-turn"]').filter({ hasText: "独立回复" });

async function checkCard(page: Page, name: string) {
  const dialog = card(page, name);
  await expect(
    dialog.getByRole("heading", { name, exact: true }),
  ).toBeVisible();
  await expect(
    dialog.getByText(name === "白榆" ? "乙的独立简介" : "甲的独立简介"),
  ).toBeVisible();
  await expect(dialog.locator(".of-agent-cover")).toHaveCSS(
    "background-color",
    "rgb(23, 58, 54)",
  );
  return dialog;
}

async function clickCenter(page: Page, target: Locator) {
  await target.scrollIntoViewIfNeeded();
  const rect = await target.boundingBox();
  if (!rect) throw new Error("无法确定点击目标的位置");
  await page.mouse.click(rect.x + rect.width / 2, rect.y + rect.height / 2);
}

for (const [list, path, name, title] of [
  ["任务列表", tasksPath + "list", "青禾", "任务标题 1"],
  ["收件箱", "/o/o1/inbox", "白榆", "通知中的任务"],
] as const) {
  test(`C01 ${list}的头像与标题分别响应鼠标与键盘`, async ({ page }) => {
    await mockProfile(page);
    for (const keyboard of [false, true]) {
      await page.goto(path);
      const row = page.getByRole("listitem").filter({ hasText: title });
      const avatar = trigger(row, name);
      const rowButton = row.getByRole("button", {
        name: new RegExp(`OPEN-1 ${title}`, "i"),
      });
      if (keyboard) {
        await avatar.focus();
        await expect(avatar).toBeFocused();
        await page.keyboard.press("Space");
      } else await clickCenter(page, avatar);
      await checkCard(page, name);
      await expect(page).not.toHaveURL(/task=|\/tasks\/OPEN-1/i);
      await page.keyboard.press("Escape");
      if (keyboard) {
        await rowButton.focus();
        await expect(rowButton).toBeFocused();
        await page.keyboard.press("Enter");
      } else await clickCenter(page, row.getByText(title, { exact: true }));
      await expect(page.getByRole("textbox", { name: "任务标题" })).toHaveValue(
        "任务标题 1",
      );
      await expect(page).toHaveURL(/(?:task=|\/tasks\/)OPEN-1/i);
      await expect(card(page, name)).toHaveCount(0);
    }
  });
}

test("C02 同一历史回复在退队前后显示原作者，资料缺失时保留原作者 ID", async ({
  page,
}) => {
  const flags: { crewCase: "current" | "former"; denied?: boolean } = {
    crewCase: "current",
  };
  await mockProfile(page, flags);
  for (const membership of ["current", "former"] as const) {
    flags.crewCase = membership;
    await page.goto(chat);
    const reply = turn(page);
    const avatar = trigger(reply, "白榆");
    await expect(avatar.getByRole("img", { name: "白榆" })).toHaveCSS(
      "background-color",
      "rgb(37, 99, 235)",
    );
    await expect(reply.getByText("白榆", { exact: true })).toBeVisible();
    await avatar.click();
    await checkCard(page, "白榆");
  }
  flags.denied = true;
  await page.reload();
  const unknown = trigger(turn(page), "未知作者");
  await expect(turn(page).getByText("青禾", { exact: true })).toHaveCount(0);
  const requested = page.waitForRequest((req) =>
    new URL(req.url()).pathname.endsWith("/agents/" + second),
  );
  await unknown.click();
  await requested;
  await expect(card(page, "未知作者").getByRole("status")).toBeVisible();
});

for (const mode of ["viewer", "exclusive"] as const) {
  test(
    mode === "viewer"
      ? "C03 只读成员可查看资料，任务入口禁用"
      : "C04 专属成员可查看资料，独立指派与单聊禁用",
    async ({ page }) => {
      const viewer = mode === "viewer";
      const writes = await mockProfile(
        page,
        viewer ? { role: "VIEWER" } : { crewCase: "exclusive" },
      );
      if (viewer) {
        await page.goto(detail);
        await expect(
          page.getByRole("textbox", { name: "任务标题" }),
        ).toHaveAttribute("readonly", "");
      }
      await page.goto(viewer ? agents : chat);
      await trigger(viewer ? page : turn(page), "白榆").click();
      const dialog = await checkCard(page, "白榆");
      const actions = [dialog.getByRole("button", { name: "新建或指派任务" })];
      if (!viewer) actions.push(dialog.getByRole("button", { name: "对话" }));
      const url = page.url();
      for (const action of actions) {
        await expect(action).toBeDisabled();
        await clickCenter(page, action);
        await expect(
          page.getByRole("dialog", { name: "指派任务给 白榆" }),
        ).toHaveCount(0);
        await expect(page).toHaveURL(url);
      }
      expect(
        writes.filter((w) =>
          /\/tasks(?:\/|$)|\/conversations$|\/messages$/.test(w.path),
        ),
      ).toEqual([]);
    },
  );
}

test("C05 精确朗读候选姓名，鼠标和键盘只插入一次，头像保留光标", async ({
  page,
}) => {
  const writes = await mockProfile(page, { crewCase: "current" });
  await page.goto(chat);
  const input = page.getByRole("textbox", { name: "输入消息" });
  for (const method of ["click", "keyboard", "avatar"]) {
    await input.fill("前文  后文");
    await input.evaluate((el) =>
      (el as HTMLTextAreaElement).setSelectionRange(3, 3),
    );
    await page.keyboard.insertText("@");
    const option = page.getByRole("option", { name: "白榆", exact: true });
    await expect(option).toBeVisible();
    if (method === "avatar") {
      await trigger(option, "白榆").click();
      await checkCard(page, "白榆");
      await expect(input).toHaveValue("前文 @ 后文");
      await expect(input).toBeFocused();
      expect(
        await input.evaluate(
          (el) => (el as HTMLTextAreaElement).selectionStart,
        ),
      ).toBe(4);
      await page.keyboard.press("Escape");
      await expect(card(page, "白榆")).toHaveCount(0);
      await expect(input).toBeFocused();
      await expect(input).toHaveValue("前文 @ 后文");
    } else {
      if (method === "click")
        await option.getByText("白榆", { exact: true }).click();
      else {
        await input.press("ArrowDown");
        await expect(option).toHaveAttribute("aria-selected", "true");
        await input.press("Enter");
      }
      await expect(input).toHaveValue("前文 @白榆  后文");
      await expect(input).toBeFocused();
      await expect(card(page, "白榆")).toHaveCount(0);
    }
  }
  expect(writes.filter((w) => w.path.endsWith("/messages"))).toEqual([]);
});

test("C06 可写普通成员可以新建、指派、单聊并打开目标设置", async ({ page }) => {
  const writes = await mockProfile(page);
  const open = async () => {
    await page.goto(agents);
    await trigger(page, "白榆").click();
    return checkCard(page, "白榆");
  };
  const palette = page.getByRole("dialog", { name: "指派任务给 白榆" });
  await (await open()).getByRole("button", { name: "新建或指派任务" }).click();
  await palette.getByRole("button", { name: "新建任务" }).click();
  const create = page.getByRole("dialog", { name: /新建任务/ });
  await create.getByRole("textbox", { name: "标题" }).fill("新任务");
  await create
    .getByRole("button", { name: /创\s*建/ })
    .last()
    .click();
  await expect
    .poll(
      () =>
        writes.find(
          (w) => w.method === "POST" && w.path.endsWith("/spaces/s1/tasks"),
        )?.body.assignees,
    )
    .toEqual([{ kind: "AGENT", id: second }]);
  await (await open()).getByRole("button", { name: "新建或指派任务" }).click();
  await palette.getByRole("option", { name: /任务标题 1/ }).click();
  await expect
    .poll(
      () =>
        writes.find(
          (w) => w.method === "PATCH" && w.path.endsWith("/tasks/task-1"),
        )?.body.assignees,
    )
    .toEqual([
      { kind: "AGENT", id: first },
      { kind: "AGENT", id: second },
    ]);
  await (await open()).getByRole("button", { name: "对话" }).click();
  const input = page.getByRole("textbox", { name: "输入消息" });
  await input.fill("独立单聊首条消息");
  await input.press("Enter");
  await expect
    .poll(
      () =>
        writes.find(
          (w) => w.method === "POST" && w.path.endsWith("/conversations"),
        )?.body,
    )
    .toMatchObject({ space_id: "s1", agent_id: second });
  const session = writes.find(
    (w) => w.method === "POST" && w.path.endsWith("/conversations"),
  )!;
  await expect
    .poll(
      () =>
        writes.find((w) =>
          w.path.endsWith(`/conversations/${session.body.id}/messages`),
        )?.body.body,
    )
    .toBe("独立单聊首条消息");
  await (await open()).getByRole("button", { name: "设置" }).click();
  await expect(page).toHaveURL(new RegExp("agentSettings=" + second));
  await expect(
    page
      .getByRole("complementary", { name: "白榆 的设置" })
      .getByText("乙的独立简介", { exact: true }),
  ).toBeVisible();
});

test("R01 详情、评论与提及、活动、选择器、表格、看板卡片与列头八个入口", async ({
  page,
}) => {
  const writes = await mockProfile(page, { grouped: true });
  const visit = async (root: Locator, name: string) => {
    await trigger(root, name).click();
    await checkCard(page, name);
    await page.keyboard.press("Escape");
  };
  await page.goto(detail);
  const assignees = page.getByRole("button", { name: "处理人", exact: true });
  await visit(assignees, "青禾");
  const comment = page
    .getByText("评审记录", { exact: false })
    .filter({ has: page.locator("[data-mention]") })
    .first();
  await trigger(page, "白榆").first().click();
  await checkCard(page, "白榆");
  await page.keyboard.press("Escape");
  await visit(comment, "青禾");
  await visit(
    page.getByRole("listitem").filter({ hasText: "测试成员" }),
    "青禾",
  );
  await assignees.click();
  const candidate = page.getByRole("option", { name: /白榆/ });
  await visit(candidate, "白榆");
  await expect(candidate).toHaveAttribute("aria-selected", "false");
  expect(writes.filter((w) => w.method === "PATCH")).toEqual([]);
  await page.goto(tasksPath + "table");
  await visit(
    page.getByRole("row").filter({
      has: page.getByRole("button", { name: "任务标题 1", exact: true }),
    }),
    "青禾",
  );
  await page.goto(tasksPath + "board");
  const task = page.getByRole("button", {
    name: "OPEN-1 任务标题 1",
    exact: true,
  });
  const before = await task.boundingBox();
  await visit(task, "青禾");
  await expect(page.getByRole("textbox", { name: "任务标题" })).toHaveCount(0);
  await visit(
    page.locator("header").filter({
      has: page.getByRole("heading", { name: "青禾", exact: true }),
    }),
    "青禾",
  );
  expect(await task.boundingBox()).toEqual(before);
});

test("目录截断后按 ID 核对普通与专属成员", async ({ page }) => {
  const flags: { truncated: boolean; crewCase: "current" | "exclusive" } = {
    truncated: true,
    crewCase: "current",
  };
  await mockProfile(page, flags);
  for (const mode of ["current", "exclusive"] as const) {
    flags.crewCase = mode;
    await page.goto(chat);
    await trigger(turn(page), "白榆").click();
    const dialog = await checkCard(page, "白榆");
    const startChat = dialog.getByRole("button", { name: "对话" });
    const assign = dialog.getByRole("button", { name: "新建或指派任务" });
    if (mode === "current") {
      await expect(assign).toBeEnabled();
      await startChat.click();
      await expect(
        page.getByRole("textbox", { name: "输入消息" }),
      ).toBeVisible();
      await expect(page).toHaveURL(new RegExp("agent=" + second));
    } else {
      await expect(assign).toBeDisabled();
      await expect(startChat).toBeDisabled();
    }
  }
});

test("指派面板按编号搜索也服从卡片所在空间", async ({ page }) => {
  const writes = await mockProfile(page);
  await page.route("**/tasks/by-display-id/OTHER-1", (route) =>
    route.fulfill({
      json: {
        id: "other",
        space_id: "s2",
        display_id: "OTHER-1",
        title: "其他空间任务",
      },
    }),
  );
  await page.goto(agents);
  await trigger(page, "白榆").click();
  await (
    await checkCard(page, "白榆")
  )
    .getByRole("button", { name: "新建或指派任务" })
    .click();
  const palette = page.getByRole("dialog", { name: "指派任务给 白榆" });
  const result = page.waitForResponse((response) =>
    response.url().endsWith("/tasks/by-display-id/OTHER-1"),
  );
  await palette.getByRole("combobox").fill("OTHER-1");
  await result;
  await expect(palette.getByRole("option")).toHaveCount(0);
  expect(writes.filter((w) => w.method === "PATCH")).toEqual([]);
});
