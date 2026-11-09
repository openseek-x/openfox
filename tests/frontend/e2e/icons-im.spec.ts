import { expect, test, type Page } from "@playwright/test";
import {
  agent,
  device,
  entry,
} from "../../../packages/app/src/agents/fixtures";

const orgId = "00000000-0000-0000-0000-000000000001";
const iconId = "00000000-0000-0000-0000-000000000002";
const base = `/api/v1/organizations/${orgId}`;
const iconURL = `${base}/icons/${iconId}`;
const png = Buffer.from(
  "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+a8ZkAAAAASUVORK5CYII=",
  "base64",
);

async function mock(page: Page) {
  const state = {
    agent: agent("a1", { name: "研发助手" }),
    space: {
      id: "s1",
      name: "研发空间",
      prefix: "QA",
      visibility: "shared",
      version: 1,
      created_at: "2026-11-07T06:00:00Z",
      updated_at: "2026-11-07T06:00:00Z",
      icon: null as string | null,
      purpose: "software_development",
      features: {
        views: true,
        projects: true,
        iterations: false,
        triage: false,
      },
      multi_assignee: false,
      estimation: {
        scale: "notUsed",
        extended: false,
        allow_zero: false,
        count_unestimated: false,
      },
      status: "active",
      retired_at: null,
      position: 0,
      my_role: "OWNER",
      can_manage: true,
      member_count: 1,
      task_type_count: 1,
      label_count: 0,
      task_count: 0,
    },
    writes: [] as { path: string; body: Record<string, unknown> }[],
  };
  await page.addInitScript(
    (org) => localStorage.setItem(`openfox:onboarding:${org}`, "{}"),
    orgId,
  );
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    let body: unknown;
    let status = 200;
    if (path === iconURL) {
      await route.fulfill({ contentType: "image/png", body: png });
      return;
    }
    if (path === `${base}/icons` && request.method() === "POST") {
      body = {
        id: iconId,
        url: iconURL,
        content_type: "image/png",
        size_bytes: png.length,
      };
      status = 201;
    } else if (path === "/api/v1/me")
      body = { id: "u1", email: "qa@example.test" };
    else if (path === "/api/v1/me/organizations")
      body = {
        organizations: [
          { id: orgId, name: "OpenFox UI QA", role: "OWNER", slug: "qa" },
        ],
      };
    else if (path === "/api/v1/me/preferences")
      body = {
        theme: "light",
        locale: "zh-CN",
        outline_color: 0,
        send_shortcut: "enter",
        command_shortcuts: {},
        version: 1,
      };
    else if (path === `${base}/spaces`) body = { spaces: [state.space] };
    else if (path === `${base}/spaces/s1`) {
      if (request.method() === "PATCH") {
        const patch = request.postDataJSON();
        state.writes.push({ path, body: patch });
        state.space = {
          ...state.space,
          ...patch,
          version: state.space.version + 1,
        };
      }
      body = state.space;
    } else if (path === `${base}/agent-directory`)
      body = {
        entries: [
          entry("a1", {
            name: "研发助手",
            avatar: state.agent.avatar,
            mine: true,
          }),
        ],
        archived_count: 0,
        truncated: false,
      };
    else if (path === `${base}/spaces/s1/agents/a1`) body = state.agent;
    else if (path === `${base}/spaces/s1/agents/a1/profile`) {
      const patch = request.postDataJSON();
      state.writes.push({ path, body: patch });
      state.agent = {
        ...state.agent,
        ...patch,
        profile_version: state.agent.profile_version + 1,
      };
      body = state.agent;
    } else if (path === `${base}/devices`)
      body = { devices: [device("d1")], next_cursor: null };
    else if (path === `${base}/integrations/feishu/bots`)
      body = {
        app_connected: true,
        assistants: [],
        agents: [],
        can_bind_agent: true,
        builtin_prompt: "",
      };
    else if (path.endsWith("/conversations"))
      body = { conversations: [], next_cursor: null };
    else if (path.endsWith("/direct-chats"))
      body = { chats: [], next_before: "" };
    else if (path.endsWith("/inbox/stats"))
      body = {
        counts: {
          inbox: 0,
          inbox_unread: 0,
          inbox_important_unread: 0,
          inbox_other_unread: 0,
          later: 0,
          cleared: 0,
        },
      };
    else {
      status = 404;
      body = {
        error: { code: "NOT_FOUND", message: "fixture", request_id: "qa" },
      };
    }
    await route.fulfill({ status, json: body });
  });
  return state;
}

test("Agent image upload persists and the IM picker keeps its parent open", async ({
  page,
}, testInfo) => {
  const state = await mock(page);
  await page.goto(`/o/${orgId}/agents?agentSettings=a1`);
  const drawer = page.getByRole("complementary", { name: "研发助手 的设置" });
  await drawer.getByRole("button", { name: "选择头像" }).click();
  await page
    .getByLabel("上传图片", { exact: true })
    .setInputFiles({ name: "avatar.png", mimeType: "image/png", buffer: png });
  await expect
    .poll(() => state.writes.some((write) => write.path.endsWith("/profile")))
    .toBe(true);
  await expect(
    drawer.getByRole("button", { name: "选择头像" }).locator("img"),
  ).toHaveAttribute("src", iconURL);
  await page.reload();
  const image = drawer.getByRole("button", { name: "选择头像" }).locator("img");
  await expect(image).toBeVisible();
  await expect
    .poll(() =>
      image.evaluate((node) => (node as HTMLImageElement).naturalWidth),
    )
    .toBeGreaterThan(0);
  await drawer.getByRole("button", { name: /触发 & IM 集成/ }).click();
  await drawer.getByRole("button", { name: "添加 IM 集成" }).click();
  const parent = page.getByRole("dialog", { name: "添加 IM 集成" });
  await parent.getByRole("button", { name: "创建飞书智能体" }).click();
  const child = page.getByRole("dialog", { name: "创建飞书智能体" });
  await expect(
    child.getByRole("button", { name: "Agent", exact: true }),
  ).toBeDisabled();
  await expect(child.getByRole("button", { name: "下一步" })).toBeEnabled();
  await page.screenshot({ path: testInfo.outputPath("agent-im-binding.png") });
  await page.keyboard.press("Escape");
  await expect(child).toHaveCount(0);
  await expect(parent).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(parent).toHaveCount(0);
  await expect(drawer).toBeVisible();
});

test("space emoji and uploaded icon persist and display in navigation", async ({
  page,
}, testInfo) => {
  const state = await mock(page);
  await page.goto(`/o/${orgId}/settings/spaces/s1/general`);
  await page.getByRole("button", { name: "选择图标" }).click();
  await page.getByRole("tab", { name: "表情", exact: true }).click();
  await page.getByRole("button", { name: "🚀", exact: true }).click();
  await expect.poll(() => state.space.icon).toBe("emoji:🚀");
  await page.reload();
  await expect(page.getByRole("button", { name: "选择图标" })).toHaveText("🚀");
  await page.getByRole("button", { name: "选择图标" }).click();
  await page.getByRole("tab", { name: "上传", exact: true }).click();
  await page
    .getByLabel("上传", { exact: true })
    .setInputFiles({ name: "space.png", mimeType: "image/png", buffer: png });
  await expect.poll(() => state.space.icon).toBe(`upload:${iconId}`);
  await page.reload();
  await expect(
    page.getByRole("button", { name: "选择图标" }).locator("img"),
  ).toHaveAttribute("src", iconURL);
  await expect(
    page
      .getByRole("navigation", { name: "设置导航" })
      .locator(`img[src="${iconURL}"]`),
  ).toHaveCount(1);
  await page.getByRole("button", { name: "选择图标" }).click();
  await page.screenshot({
    path: testInfo.outputPath("space-upload-picker.png"),
  });
});
