import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { device } from "../agents/fixtures";
import {
  apiError,
  findCall,
  json,
  renderSettings,
  ts,
  type Route,
} from "../settings/testing";

// 钉钉：与飞书共用页面组件，字段改为 AppKey / AppSecret，没有链接预览，指引是钉钉自己的 5 步。

const o = "/v1/organizations/o1/integrations";

const provider = {
  id: "dingtalk",
  name: "钉钉",
  category: "collaboration",
  availability: "available",
  template: "C1",
  capabilities: ["notifications"],
  configured: true,
  bindable: true,
  summary: { connections: 0, status: "none" },
};

const guide = {
  open_platform_url: "https://open-dev.dingtalk.com/app",
  redirect_uri: "https://x.example/api/v1/integrations/dingtalk/callback",
  link_preview_pattern: "",
  scopes_json: '{"permissions":["qyapi_robot_sendmsg","Contact.User.Read"]}',
  events: [],
  callbacks: ["/v1.0/im/bot/messages/get"],
};

const app = {
  connection_id: "c1",
  app_id: "dingabcdef123456",
  bot_name: "dingabcdef123456",
  bot_avatar_url: "",
  status: "CONNECTED",
  status_reason: "",
  last_connected_at: ts,
  last_event_at: null,
  last_error: "",
  link_preview_enabled: true,
  credentials_hint: "****1234",
  created_at: ts,
  updated_at: ts,
  version: 1,
};

const generic = {
  provider,
  can_manage: true,
  connections: [],
  personal_connections: [],
  sync_links: [],
};

function route(view: unknown, more?: Route): Route {
  return (call) => {
    const custom = more?.(call);
    if (custom) return custom;
    if (call.method === "GET" && call.path === `${o}/dingtalk/app`)
      return json(200, view);
    if (call.method === "GET" && call.path === `${o}/dingtalk`)
      return json(200, generic);
    return undefined;
  };
}

const page = "/o/o1/settings/integrations/dingtalk";

beforeEach(() => localStorage.clear());
afterEach(() => vi.restoreAllMocks());

describe("钉钉 · 绑定分区", () => {
  it("绑定对话框用 AppKey / AppSecret，校验 ding 前缀并提交到 /dingtalk/app", async () => {
    const user = userEvent.setup();
    let put: unknown;
    const calls = renderSettings(
      page,
      route({ can_manage: true, app: null, personal: null, guide }, (call) => {
        if (call.method === "PUT" && call.path === `${o}/dingtalk/app`) {
          put = call.body;
          return json(200, { ...app, status: "PENDING" });
        }
        return undefined;
      }),
    );
    expect(await screen.findByText(/尚未绑定钉钉应用/)).toBeVisible();
    await user.click(await screen.findByRole("button", { name: /绑定钉钉/ }));
    const dialog = await screen.findByRole("dialog");
    const key = within(dialog).getByLabelText("AppKey（Client ID）");
    const secret = within(dialog).getByLabelText("AppSecret（Client Secret）");
    const submit = within(dialog).getByRole("button", { name: "保存并连接" });
    await user.type(key, "cli_abcdef123456");
    await user.type(secret, "secret-0123456789ab");
    expect(within(dialog).getByText(/AppKey 以 ding 开头/)).toBeVisible();
    expect(submit).toBeDisabled();
    await user.clear(key);
    await user.type(key, "dingabcdef123456");
    await user.click(submit);
    await waitFor(() =>
      expect(put).toEqual({
        app_id: "dingabcdef123456",
        app_secret: "secret-0123456789ab",
      }),
    );
    expect(findCall(calls, "GET", `${o}/feishu/app`)).toBeUndefined();
  });

  it("已绑定：显示 AppKey，没有任务链接预览，配置指引是钉钉的步骤", async () => {
    const user = userEvent.setup();
    renderSettings(
      page,
      route({ can_manage: true, app, personal: null, guide }),
    );
    expect(await screen.findByText("AppKey：dingabcdef123456")).toBeVisible();
    expect(screen.queryByText("任务链接预览")).toBeNull();
    await user.click(
      await screen.findByRole("button", { name: "助手连接选项" }),
    );
    await user.click(await screen.findByRole("menuitem", { name: "配置指引" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText("创建企业内部应用")).toBeVisible();
    expect(within(dialog).getByText(/Contact\.User\.Read/)).toBeVisible();
    expect(within(dialog).getByText("/v1.0/im/bot/messages/get")).toBeVisible();
    expect(within(dialog).queryByText(/长连接接收事件/)).toBeNull();
  });
});

// ---------- 机器人：AppKey + AppSecret 直连（钉钉没有扫码创建接口） ----------

const botsView = {
  app_connected: true,
  assistants: [
    { space_id: "s1", space_name: "默认空间", can_manage: true, bot: null },
  ],
  agents: [],
  can_bind_agent: true,
  builtin_prompt: "你是 OpenFox 任务助手。",
};

const createdBot = {
  id: "b1",
  kind: "TASK_ASSISTANT",
  connection_id: "c9",
  app_id: "dingbot000000001",
  bot_name: "dingbot000000001",
  bot_avatar_url: "",
  status: "CONNECTING",
  status_reason: "",
  agent: { id: "ag0", name: "任务助手" },
  space: { id: "s1", name: "默认空间" },
  extra_prompt: "",
  runtime: null,
  can_manage: true,
  created_at: ts,
  version: 1,
};

function botsRoute(more?: Route): Route {
  return route(
    {
      can_manage: true,
      app: { ...app },
      personal: null,
      guide,
    },
    (call) => {
      const custom = more?.(call);
      if (custom) return custom;
      if (call.method === "GET" && call.path === `${o}/dingtalk/bots`)
        return json(200, botsView);
      if (call.method === "GET" && call.path === "/v1/organizations/o1/devices")
        return json(200, { devices: [device("d1")], next_cursor: null });
      return undefined;
    },
  );
}

/** 打开任务助手的绑定对话框，选好 Runtime，进入凭证面板。 */
async function openCredentialPanel(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByRole("button", { name: "绑定" }));
  const dialog = await screen.findByRole("dialog");
  // 钉钉没有扫码：副标题说明的是填写 AppKey 与 AppSecret
  expect(
    within(dialog).getByText(/填写钉钉应用的 AppKey 与 AppSecret/),
  ).toBeVisible();
  await user.click(within(dialog).getByRole("button", { name: "Runtime" }));
  await user.click(await screen.findByRole("option", { name: /Codex/ }));
  await user.click(within(dialog).getByRole("button", { name: "下一步" }));
  return screen.findByRole("dialog");
}

describe("钉钉 · 任务助手已在飞书绑定", () => {
  it("该行显示已在飞书绑定并禁用「绑定」，其它空间不受影响", async () => {
    renderSettings(
      page,
      botsRoute((call) =>
        call.method === "GET" && call.path === `${o}/dingtalk/bots`
          ? json(200, {
              ...botsView,
              assistants: [
                {
                  space_id: "s1",
                  space_name: "默认空间",
                  can_manage: true,
                  bot: null,
                  bound_provider: "feishu",
                },
                {
                  space_id: "s2",
                  space_name: "另一个空间",
                  can_manage: true,
                  bot: null,
                },
              ],
            })
          : undefined,
      ),
    );
    expect(
      await screen.findByText(
        "已在飞书绑定任务助手。每个空间只能有一个任务助手，先在飞书移除后再绑定。",
      ),
    ).toBeVisible();
    const buttons = screen.getAllByRole("button", { name: "绑定" });
    expect(buttons[0]).toBeDisabled();
    expect(buttons[1]).toBeEnabled();
  });
});

describe("钉钉 · 机器人凭证面板", () => {
  it("提交成功：POST /dingtalk/bots 带 AppKey / AppSecret，提示并关闭", async () => {
    const user = userEvent.setup();
    let body: unknown;
    const calls = renderSettings(
      page,
      botsRoute((call) => {
        if (call.method === "POST" && call.path === `${o}/dingtalk/bots`) {
          body = call.body;
          return json(201, createdBot);
        }
        return undefined;
      }),
    );
    const dialog = await openCredentialPanel(user);
    expect(
      within(dialog).getByText(/钉钉官方没有扫码创建应用的接口/),
    ).toBeVisible();
    const connect = within(dialog).getByRole("button", { name: "连接" });
    expect(connect).toBeDisabled();
    const key = within(dialog).getByLabelText("AppKey（Client ID）");
    await user.type(key, "cli_wrongprefix1");
    expect(within(dialog).getByText(/AppKey 以 ding 开头/)).toBeVisible();
    expect(connect).toBeDisabled();
    await user.clear(key);
    await user.type(key, "dingbot000000001");
    const secret = within(dialog).getByLabelText("AppSecret（Client Secret）");
    await user.type(secret, "x".repeat(129));
    expect(connect).toBeDisabled();
    expect(within(dialog).getByText(/应用密钥需为 16–128 位/)).toBeVisible();
    expect(body).toBeUndefined();
    await user.clear(secret);
    await user.type(secret, "bot-secret-0123456789");
    await user.click(connect);
    await waitFor(() =>
      expect(body).toMatchObject({
        purpose: "task_assistant",
        space_id: "s1",
        app_key: "dingbot000000001",
        app_secret: "bot-secret-0123456789",
      }),
    );
    expect(
      findCall(calls, "POST", `${o}/dingtalk/registrations`),
    ).toBeUndefined();
    expect(await screen.findByText("已绑定钉钉任务助手")).toBeVisible();
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it.each([
    [422, "FEISHU_CREDENTIALS_INVALID", /AppKey 或 AppSecret 不正确/],
    [409, "FEISHU_APP_IN_USE", /已经绑定到其他 OpenFox 组织/],
    [502, "FEISHU_UNAVAILABLE", /暂时连不上钉钉开放平台/],
    [
      409,
      "FEISHU_ASSISTANT_EXISTS",
      /每个空间只能有一个任务助手，请先移除已有的任务助手后再绑定/,
    ],
  ])(
    "校验失败 %s %s：错误显示在面板内，对话框保持打开",
    async (status, code, text) => {
      const user = userEvent.setup();
      renderSettings(
        page,
        botsRoute((call) =>
          call.method === "POST" && call.path === `${o}/dingtalk/bots`
            ? apiError(status, code)
            : undefined,
        ),
      );
      const dialog = await openCredentialPanel(user);
      await user.type(
        within(dialog).getByLabelText("AppKey（Client ID）"),
        "dingbot000000001",
      );
      await user.type(
        within(dialog).getByLabelText("AppSecret（Client Secret）"),
        "bot-secret-0123456789",
      );
      await user.click(within(dialog).getByRole("button", { name: "连接" }));
      expect(await within(dialog).findByText(text)).toBeVisible();
      expect(screen.getByRole("dialog")).toBeVisible();
      expect(
        within(dialog).getByRole("button", { name: "连接" }),
      ).toBeEnabled();
    },
  );
});
