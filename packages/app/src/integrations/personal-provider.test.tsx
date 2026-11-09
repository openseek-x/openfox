import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it } from "vitest";

import { findCall, json, renderSettings, ts, type Route } from "../settings/testing";

const base = "/v1/organizations/o1/integrations";

const provider = (id: string, name: string, template: string) => ({
  id,
  name,
  category: id === "wecom" ? "collaboration" : "task",
  availability: "available",
  template,
  capabilities: ["personal_account"],
  configured: true,
  bindable: true,
  summary: { connections: 1, status: "ok" },
});

const connection = (id: string, service: string, scope = "ORGANIZATION") => ({
  id,
  provider: service,
  scope,
  external_id: "company-1",
  display_name: "Company 1",
  account_kind: "instance",
  status: "ACTIVE",
  status_reason: "",
  credentials_hint: "",
  created_by: "u1",
  created_by_email: "member@openfox.test",
  created_at: ts,
  updated_at: ts,
  disconnected_at: null,
  version: 1,
});

describe("第三方个人绑定", () => {
  beforeEach(() => localStorage.clear());

  it("普通成员用个人令牌绑定 PingCode 企业连接", async () => {
    const user = userEvent.setup();
    let bound = false;
    const route: Route = (call) => {
      if (call.path === `${base}/pingcode` && call.method === "GET")
        return json(200, {
          provider: provider("pingcode", "PingCode", "A"),
          can_manage: false,
          connections: [connection("parent-1", "pingcode")],
          personal_connections: bound
            ? [
                {
                  ...connection("personal-1", "pingcode", "PERSONAL"),
                  account_kind: "sync_comments",
                  owner_user_id: "u1",
                  parent_connection_id: "parent-1",
                  display_name: "Lin",
                },
              ]
            : [],
          sync_links: [],
        });
      if (call.path === `${base}/pingcode-personal` && call.method === "PUT") {
        bound = true;
        return json(200, connection("personal-1", "pingcode", "PERSONAL"));
      }
      return undefined;
    };
    const calls = renderSettings(
      "/o/o1/settings/integrations/pingcode",
      route,
      "MEMBER",
    );
    await user.click(
      await screen.findByRole("button", { name: "绑定个人令牌" }),
    );
    await user.type(
      screen.getByLabelText("个人访问令牌"),
      "user-token",
    );
    await user.click(screen.getAllByRole("button", { name: "绑定个人令牌" })[1]!);
    await waitFor(() =>
      expect(findCall(calls, "PUT", `${base}/pingcode-personal`)?.body).toEqual({
        connection_id: "parent-1",
        version: 0,
        token: "user-token",
      }),
    );
    expect(await screen.findByText("Lin")).toBeVisible();
  });

  it("企业微信个人绑定码仅由本人主动领取并显示", async () => {
    const user = userEvent.setup();
    const route: Route = (call) => {
      if (call.path === `${base}/wecom` && call.method === "GET")
        return json(200, {
          provider: provider("wecom", "企业微信", "C2"),
          can_manage: false,
          connections: [],
          personal_connections: [],
          sync_links: [],
        });
      if (call.path === `${base}/wecom/bots` && call.method === "GET")
        return json(200, {
          app_connected: false,
          assistants: [],
          agents: [
            {
              id: "bot-1",
              bot_name: "需求助手",
              agent: { id: "agent-1", name: "需求助手" },
              status: "CONNECTED",
            },
          ],
          can_bind_agent: false,
          builtin_prompt: "",
        });
      if (call.path === `${base}/wecom/personal` && call.method === "GET")
        return json(200, { bindings: [] });
      if (call.path === `${base}/wecom/personal/codes` && call.method === "POST")
        return json(200, {
          bot_id: "bot-1",
          code: "VERIFY-123456",
          expires_at: ts,
        });
      return undefined;
    };
    const calls = renderSettings(
      "/o/o1/settings/integrations/wecom",
      route,
      "MEMBER",
    );
    await user.click(await screen.findByRole("button", { name: "获取验证码" }));
    await waitFor(() =>
      expect(findCall(calls, "POST", `${base}/wecom/personal/codes`)?.body).toEqual({
        bot_id: "bot-1",
      }),
    );
    expect(await screen.findByText("VERIFY-123456")).toBeVisible();
  });
});
