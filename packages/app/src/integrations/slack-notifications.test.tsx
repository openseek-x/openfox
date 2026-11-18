import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import type { NotificationChannel, SlackConfiguration } from "@openfox/api";
import { apiError, json, renderSettings } from "../settings/testing";
import catalog from "../me/notification-policy.fixture.json";
const channels = "/v1/me/notification-channels",
  configuration = "/v1/organizations/o1/integrations/slack/configuration";
const config: SlackConfiguration = {
  configured: true,
  can_manage: false,
  oauth_redirect_uri: "",
  identity_redirect_uri: "",
  central_events_url: "",
  blocked_targets: [],
  apps: [
    {
      id: "app",
      connection_id: "c1",
      parent_app_id: "",
      bot_id: "bot",
      kind: "CENTRAL",
      name: "Workspace",
      external_app_id: "A123",
      team_id: "T123",
      owner_slack_user_id: "U123",
      bot_user_id: "UBOT",
      agent_id: "",
      space_id: "",
      created_by: "admin",
      state: "ACTIVE",
      status_reason: "",
      version: 2,
      generation: 1,
      url_verified: true,
      installed: true,
      events_url: "",
      can_manage: false,
      created_at: "2026-11-15T00:00:00Z",
    },
  ],
  identities: [
    {
      id: "personal",
      connection_id: "c2",
      parent_app_id: "app",
      owner_user_id: "u1",
      team_id: "T123",
      slack_user_id: "U123",
      active: true,
      version: 1,
    },
  ],
};
function channel(
  overrides: Partial<NotificationChannel> = {},
): NotificationChannel {
  return {
    channel: "slack",
    enabled: false,
    available: true,
    disabled_events: [],
    customized: false,
    revision: 31,
    event_overrides: { task_assigned_assignee: false },
    muted_groups: [],
    ...overrides,
  };
}

describe("Slack independent N01 preferences", () => {
  it("uses the existing 42 event keys and seven group mutes with independent revisions and direct-message consent", async () => {
    const user = userEvent.setup();
    let current = channel();
    const calls = renderSettings("/o/o1/settings/notifications", (c) => {
      if (c.path === channels) return json(200, { channels: [current] });
      if (c.path === configuration) return json(200, config);
      if (c.path === "/v1/me/inbox-preferences") return json(200, catalog);
      if (c.path === `${channels}/slack` && c.method === "PATCH") {
        current = {
          ...current,
          ...(c.body as object),
          revision: (current.revision ?? 0) + 1,
        };
        return json(200, current);
      }
      return undefined;
    });
    await user.click(await screen.findByText("Slack"));
    await screen.findByText("T123 · U123");
    const enabled = screen.getByRole("switch", { name: "开启 Slack 通知" });
    expect(enabled).not.toBeChecked();
    await user.click(enabled);
    await waitFor(() =>
      expect(calls.find((c) => c.method === "PATCH")?.body).toEqual({
        revision: 31,
        enabled: true,
      }),
    );
    expect(screen.getAllByRole("switch")).toHaveLength(51);
    const direct = screen.getByRole("switch", { name: "允许真人消息提醒" });
    expect(direct).not.toBeChecked();
    await user.click(screen.getByRole("switch", { name: "任务分配" }));
    await waitFor(() =>
      expect(calls.filter((c) => c.method === "PATCH")[1]?.body).toEqual({
        revision: 32,
        muted_groups: ["assignments"],
      }),
    );
    expect(
      screen.getByRole("switch", { name: "任务指派给你时" }),
    ).not.toBeChecked();
    await waitFor(() =>
      expect(screen.getByRole("switch", { name: "任务分配" })).toBeEnabled(),
    );
    await user.click(screen.getByRole("switch", { name: "任务分配" }));
    await waitFor(() =>
      expect(
        screen.getByRole("switch", { name: "任务指派给你时" }),
      ).toBeEnabled(),
    );
    expect(
      screen.getByRole("switch", { name: "任务指派给你时" }),
    ).not.toBeChecked();
    await user.click(direct);
    await waitFor(() =>
      expect(calls.filter((c) => c.method === "PATCH")[3]?.body).toEqual({
        revision: 34,
        direct_messages: true,
      }),
    );
    expect(
      calls
        .filter((c) => c.method === "PATCH")
        .every((c) => c.path === `${channels}/slack`),
    ).toBe(true);
  });
  it("cannot enable using another organization's availability but can turn off an existing global preference", async () => {
    const user = userEvent.setup();
    let current = channel({ enabled: true });
    const calls = renderSettings("/o/o1/settings/notifications", (c) => {
      if (c.path === channels) return json(200, { channels: [current] });
      if (c.path === configuration)
        return json(200, { ...config, identities: [] });
      if (c.path === "/v1/me/inbox-preferences") return json(200, catalog);
      if (c.path === `${channels}/slack` && c.method === "PATCH") {
        current = { ...current, ...(c.body as object), revision: 32 };
        return json(200, current);
      }
      return undefined;
    });
    await user.click(await screen.findByText("Slack"));
    const toggle = await screen.findByRole("switch", {
      name: "开启 Slack 通知",
    });
    await user.click(toggle);
    await waitFor(() => expect(toggle).toBeDisabled());
    expect(toggle).not.toBeChecked();
    expect(calls.find((c) => c.method === "PATCH")?.body).toEqual({
      revision: 31,
      enabled: false,
    });
  });
  it("refreshes on 409 without replaying stale Slack choices", async () => {
    const user = userEvent.setup();
    let current = channel({ enabled: true });
    const calls = renderSettings("/o/o1/settings/notifications", (c) => {
      if (c.path === channels) return json(200, { channels: [current] });
      if (c.path === configuration) return json(200, config);
      if (c.path === "/v1/me/inbox-preferences") return json(200, catalog);
      if (c.path === `${channels}/slack` && c.method === "PATCH") {
        current = channel({
          enabled: true,
          revision: 40,
          muted_groups: ["comments"],
        });
        return apiError(409, "VERSION_CONFLICT");
      }
      return undefined;
    });
    await user.click(await screen.findByText("Slack"));
    await screen.findByText("T123 · U123");
    await user.click(screen.getByRole("switch", { name: "任务分配" }));
    await screen.findByText("更新通知设置失败");
    await waitFor(() =>
      expect(screen.getByRole("switch", { name: "任务分配" })).toBeChecked(),
    );
    expect(calls.filter((c) => c.method === "PATCH")).toHaveLength(1);
  });
});
