import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  findCall,
  json,
  renderSettings,
  ts,
  type Route,
} from "../settings/testing";
import { brandText } from "./im-provider";

// Lark：与飞书共用同一套页面组件，请求发往 /integrations/lark/...，文案里的「飞书」换成 Lark。

const o = "/v1/organizations/o1/integrations";

const provider = {
  id: "lark",
  name: "Lark",
  category: "collaboration",
  availability: "available",
  template: "C1",
  capabilities: ["notifications"],
  configured: true,
  bindable: true,
  summary: { connections: 0, status: "none" },
};

const guide = {
  open_platform_url: "https://open.larksuite.com/app",
  redirect_uri: "https://x.example/api/v1/integrations/lark/callback",
  link_preview_pattern: "https://x.example/o/*/spaces/*/tasks*",
  scopes_json: '{"scopes":{"tenant":["im:message"]}}',
  events: ["im.message.receive_v1"],
  callbacks: ["url.preview.get"],
};

const view = { can_manage: true, app: null, personal: null, guide };
const generic = {
  provider,
  can_manage: true,
  connections: [],
  personal_connections: [],
  sync_links: [],
};

const route: Route = (call) => {
  if (call.method === "GET" && call.path === `${o}/lark/app`)
    return json(200, view);
  if (call.method === "GET" && call.path === `${o}/lark`)
    return json(200, generic);
  if (call.method === "PUT" && call.path === `${o}/lark/app`)
    return json(200, {
      connection_id: "c1",
      app_id: "cli_abcdef123456",
      bot_name: "OpenFox 助手",
      bot_avatar_url: "",
      status: "PENDING",
      status_reason: "",
      last_connected_at: null,
      last_event_at: null,
      last_error: "",
      link_preview_enabled: true,
      credentials_hint: "****1234",
      created_at: ts,
      updated_at: ts,
      version: 1,
    });
  return undefined;
};

beforeEach(() => localStorage.clear());
afterEach(() => vi.restoreAllMocks());

describe("brandText", () => {
  it("lark 把飞书 / Feishu 换成 Lark，飞书保持不变", () => {
    expect(brandText("lark", "尚未绑定飞书应用。")).toBe("尚未绑定Lark应用。");
    expect(brandText("lark", "Bind Feishu")).toBe("Bind Lark");
    expect(brandText("feishu", "尚未绑定飞书应用。")).toBe(
      "尚未绑定飞书应用。",
    );
  });
});

describe("Lark · 绑定分区", () => {
  it("请求 lark 路径，文案显示 Lark，提交到 /lark/app", async () => {
    const user = userEvent.setup();
    const calls = renderSettings("/o/o1/settings/integrations/lark", route);
    expect(await screen.findByText(/尚未绑定Lark应用/)).toBeVisible();
    expect(findCall(calls, "GET", `${o}/lark/app`)).toBeDefined();
    expect(findCall(calls, "GET", `${o}/feishu/app`)).toBeUndefined();
    await user.click(await screen.findByRole("button", { name: /绑定Lark/ }));
    const dialog = await screen.findByRole("dialog");
    await user.type(
      within(dialog).getByLabelText("App ID"),
      "cli_abcdef123456",
    );
    await user.type(
      within(dialog).getByLabelText("App Secret"),
      "secret-0123456789",
    );
    await user.click(
      within(dialog).getByRole("button", { name: "保存并连接" }),
    );
    await waitFor(() =>
      expect(findCall(calls, "PUT", `${o}/lark/app`)).toBeDefined(),
    );
  });
});
