import { expect, test } from "@playwright/test";
import {
  agent,
  device,
  entry,
} from "../../../packages/app/src/agents/fixtures";

test("目录头像独立打开资料，快捷操作保留 Agent 身份", async ({
  page,
}, testInfo) => {
  const profile = agent("a1", {
    name: "小九",
    title: "前端工程师",
    description: "工作台测试",
    allow_assign: false,
  });
  await page.addInitScript(() =>
    localStorage.setItem("openfox:onboarding:o1", "{}"),
  );
  await page.route("**/api/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    let body: unknown = {};
    if (path.endsWith("/me")) body = { id: "u1", email: "qa@example.test" };
    else if (path.endsWith("/me/organizations"))
      body = { organizations: [{ id: "o1", name: "OpenFox", role: "OWNER" }] };
    else if (path.endsWith("/preferences"))
      body = {
        locale: "zh-CN",
        theme: "light",
        density: "comfortable",
        command_shortcuts: {},
      };
    else if (path.endsWith("/agent-directory"))
      body = {
        entries: [entry("a1", { name: profile.name })],
        archived_count: 0,
        truncated: false,
      };
    else if (path.endsWith("/agents/a1")) body = profile;
    else if (path.endsWith("/spaces"))
      body = {
        spaces: [
          {
            id: "s1",
            name: "研发空间",
            prefix: "DEV",
            status: "active",
            features: {},
          },
        ],
      };
    else if (path.endsWith("/direct-chats")) body = { chats: [] };
    else if (path.endsWith("/members"))
      body = {
        members: [
          {
            user_id: "u1",
            email: "qa@example.test",
            role: "OWNER",
            status: "ACTIVE",
          },
        ],
      };
    else if (path.endsWith("/devices"))
      body = { devices: [device("d1", { reserved_slots: 1 })] };
    else if (path.endsWith("/tasks")) body = { tasks: [], next_cursor: null };
    else if (path.endsWith("/favorites")) body = { favorites: [] };
    else if (path.endsWith("/conversations")) body = { conversations: [] };
    else if (path.endsWith("/inbox/stats")) body = { counts: {} };
    await route.fulfill({ json: body });
  });
  await page.goto("/o/o1/agents");
  const trigger = page.getByRole("button", { name: "查看 小九 的信息" });
  await trigger.click();
  const card = page.getByRole("dialog", { name: "查看 小九 的信息" });
  await expect(card.getByText("工作台测试")).toBeVisible();
  await expect(card.getByRole("button").first()).toBeDisabled();
  await expect(page).not.toHaveURL(/agentSettings/);
  await card.screenshot({ path: testInfo.outputPath("agent-info.png") });
  await page.keyboard.press("Escape");
  await expect(card).toBeHidden();
  profile.allow_assign = true;
  await page.reload();
  await trigger.press("Enter");
  await card.getByRole("button", { name: "新建或指派任务" }).click();
  await expect(
    page.getByRole("dialog", { name: "指派任务给 小九" }),
  ).toBeVisible();
  await page.keyboard.press("Escape");
  await trigger.click();
  await card.getByRole("button", { name: "设置" }).click();
  await expect(page).toHaveURL(/agentSettings=a1/);
  await page.goto("/o/o1/agents");
  await trigger.press("Enter");
  await card.getByRole("button", { name: "对话" }).click();
  await expect(page).toHaveURL(/chat\?agent=a1/);
});
