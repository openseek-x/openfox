import { expect, test } from "@playwright/test";
import { agent, device } from "../../../packages/app/src/agents/fixtures";
import { createTranslator } from "../../../packages/i18n/src/translate";

const organization = "/api/v1/organizations/o1";
const timestamp = "2026-11-08T12:00:00Z";

for (const [locale, theme] of [
  ["zh-CN", "light"],
  ["en-US", "dark"],
] as const) {
  test(`assistant storage scope and nested dialogs ${locale} ${theme}`, async ({
    page,
  }, testInfo) => {
    const translate = createTranslator(locale);
    const assistant = agent("assistant", {
      builtin_kind: "ASSISTANT",
      name: "OpenFox assistant",
    });
    assistant.config = { ...assistant.config!, computer_id: "storage-device" };
    const target = {
      ...device("storage-device", {
        name: "Storage QA computer",
        online: false,
      }),
      bound_agents: [],
      created_at: timestamp,
      timezone: null,
      storage: {
        total_bytes: 4194304,
        worktree_bytes: 2097152,
        worktree_count: 2,
        codex_home_bytes: 1048576,
        journal_bytes: 1048576,
        measured_at: timestamp,
        partial: false,
        categories: [{ key: "conversation", bytes: 2097152, count: 2 }],
      },
    };
    const writes: string[] = [];
    await page.addInitScript(() =>
      localStorage.setItem("openfox:onboarding:o1", "{}"),
    );
    await page.route("**/api/**", async (route) => {
      const request = route.request();
      const path = new URL(request.url()).pathname;
      if (!["GET", "HEAD"].includes(request.method())) writes.push(path);
      let body: unknown = {};
      if (path === "/api/v1/me")
        body = { id: "u1", email: "storage@example.test" };
      else if (path === "/api/v1/me/organizations")
        body = {
          organizations: [
            { id: "o1", name: "Storage QA", role: "OWNER", slug: "qa" },
          ],
        };
      else if (path === "/api/v1/me/preferences")
        body = {
          theme,
          locale,
          density: "comfortable",
          outline_color: 0,
          send_shortcut: "enter",
          command_shortcuts: {},
          version: 1,
        };
      else if (path === `${organization}/assistant`) body = { assistant };
      else if (path === `${organization}/assistant/sessions`)
        body = { sessions: [] };
      else if (path === `${organization}/devices/storage-device`) body = target;
      else if (path.endsWith("/spaces")) body = { spaces: [] };
      else if (path.endsWith("/devices"))
        body = { devices: [target], next_cursor: null };
      else if (path.endsWith("/agent-directory"))
        body = { entries: [], truncated: false, archived_count: 0 };
      else if (path.endsWith("/conversations"))
        body = { conversations: [], next_cursor: null };
      else if (path.endsWith("/direct-chats")) body = { chats: [] };
      else if (path.endsWith("/members"))
        body = { members: [], next_cursor: null };
      else if (path.endsWith("/favorites")) body = { favorites: [] };
      else if (path.endsWith("/inbox/stats"))
        body = { counts: { inbox: 0, inbox_unread: 0, later: 0, cleared: 0 } };
      else if (path.endsWith("/conversation-workspace-options"))
        body = {
          device_id: target.id,
          directories: [],
          supported: false,
          can_reuse: false,
          can_set_baseline: false,
          git_directories: [],
          can_manage_directory: false,
          recent_directories: [],
        };
      await route.fulfill({ json: body });
    });
    await page.goto("/o/o1/assistant?session=new");
    await page
      .getByRole("button", { name: translate("chat.header.more"), exact: true })
      .click();
    await page
      .getByRole("menuitem", {
        name: translate("chat.assistant.more.storage"),
        exact: true,
      })
      .click();
    const overview = page.getByRole("dialog", {
      name: translate("chat.assistant.storageTitle"),
      exact: true,
    });
    await expect(overview.getByText(target.name)).toBeVisible();
    await expect(
      overview.getByText(translate("chat.assistant.storageScope")),
    ).toBeVisible();
    await expect(
      overview.getByText(translate("connect.storage.total"), { exact: true }),
    ).toBeVisible();
    await page.screenshot({
      path: testInfo.outputPath("assistant-storage.png"),
    });
    await overview
      .getByRole("button", {
        name: translate("connect.storage.manage"),
        exact: true,
      })
      .click();
    const manager = page.getByRole("dialog", {
      name: translate("connect.storage.manage"),
      exact: true,
    });
    await expect(manager).toBeVisible();
    await manager
      .getByRole("button", {
        name: translate("connect.storage.byTime"),
        exact: true,
      })
      .click();
    const filter = page.getByRole("dialog", {
      name: translate("connect.storage.byTime"),
      exact: true,
    });
    await expect(
      filter.getByRole("button", {
        name: translate("connect.storage.preview"),
        exact: true,
      }),
    ).toBeDisabled();
    await page.keyboard.press("Escape");
    await expect(filter).not.toBeVisible();
    await expect(overview).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(overview).not.toBeVisible();
    expect(writes).toEqual([]);
  });
}
