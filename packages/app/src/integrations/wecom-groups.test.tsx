import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it } from "vitest";
import {
  apiError,
  json,
  renderSettings,
  ts,
  type Route,
} from "../settings/testing";

const base = "/v1/organizations/o1/integrations/wecom";
const path = "/o/o1/settings/integrations/wecom";
const group = {
  chat_id: "wecom-group-1",
  version: 0,
  enabled: false,
  conversation_id: "",
  last_seen_at: ts,
};
function route(extra?: Route, manage = true): Route {
  return (call) => {
    const custom = extra?.(call);
    if (custom) return custom;
    if (call.path === base + "/bots")
      return json(200, {
        app_connected: true,
        can_bind_agent: false,
        builtin_prompt: "",
        agents: [],
        assistants: [
          {
            space_id: "s1",
            space_name: "默认空间",
            can_manage: manage,
            bot: {
              id: "b1",
              kind: "TASK_ASSISTANT",
              connection_id: "c1",
              app_id: "wecom-test",
              bot_name: "助手",
              status: "CONNECTED",
              status_reason: "",
              agent: { id: "a1", name: "助手" },
              space: { id: "s1", name: "默认空间" },
              can_manage: manage,
              created_at: ts,
              version: 1,
            },
          },
        ],
      });
    if (call.path === base + "/personal") return json(200, { bindings: [] });
    if (call.path === base)
      return json(200, {
        provider: {
          id: "wecom",
          name: "企业微信",
          category: "collaboration",
          availability: "available",
          template: "C1",
          configured: true,
          bindable: true,
          capabilities: ["notifications"],
          summary: { connections: 1, status: "ok" },
        },
        can_manage: manage,
        connections: [],
        personal_connections: [],
        sync_links: [],
      });
    if (call.path === base + "/bots/b1/groups")
      return json(200, { groups: [group], next_cursor: "" });
    return undefined;
  };
}
beforeEach(() => localStorage.clear());
describe("WeCom group authorization", () => {
  it("requires explicit group visibility consent and persists the selected version", async () => {
    const user = userEvent.setup();
    let saved = false;
    const calls = renderSettings(
      path,
      route((call) => {
        if (call.path !== base + "/bots/b1/groups") return;
        if (call.method === "PATCH") {
          saved = true;
          return json(200, {
            ...group,
            version: 1,
            enabled: true,
            conversation_id: "conv",
          });
        }
        return json(200, {
          groups: [{ ...group, version: saved ? 1 : 0, enabled: saved }],
          next_cursor: "",
        });
      }),
    );
    await user.click(await screen.findByRole("button", { name: "群聊授权" }));
    const dialog = await screen.findByRole("dialog");
    await user.click(
      await within(dialog).findByRole("button", { name: "开启" }),
    );
    const confirm = within(dialog).getByRole("button", { name: "同意并开启" });
    expect(confirm).toBeDisabled();
    await user.click(within(dialog).getByRole("checkbox"));
    await user.click(confirm);
    await waitFor(() =>
      expect(within(dialog).getByText("已开启")).toBeVisible(),
    );
    expect(calls.find((c) => c.method === "PATCH")?.body).toEqual({
      chat_id: group.chat_id,
      expected_version: 0,
      enabled: true,
    });
  });
  it("shows a stale-version failure and requires refresh before another enable", async () => {
    const user = userEvent.setup();
    renderSettings(
      path,
      route((c) =>
        c.method === "PATCH" ? apiError(409, "VERSION_CONFLICT") : undefined,
      ),
    );
    await user.click(await screen.findByRole("button", { name: "群聊授权" }));
    await user.click(await screen.findByRole("button", { name: "开启" }));
    await user.click(screen.getByRole("checkbox"));
    await user.click(screen.getByRole("button", { name: "同意并开启" }));
    expect(
      await screen.findByText("授权已被他人修改，请刷新后重新选择。"),
    ).toBeVisible();
    expect(screen.getByRole("button", { name: "同意并开启" })).toBeDisabled();
    await user.click(screen.getByRole("button", { name: "刷新" }));
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
  });
  it("does not expose group controls to a member without management rights", async () => {
    renderSettings(path, route(undefined, false), "MEMBER");
    await screen.findByText("OpenFox 任务助手 - 默认空间");
    expect(
      screen.queryByRole("button", { name: "群聊授权" }),
    ).not.toBeInTheDocument();
  });
});
