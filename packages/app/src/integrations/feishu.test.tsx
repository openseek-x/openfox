import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { act } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  apiError,
  findCall,
  json,
  renderSettings,
  ts,
  type Route,
} from "../settings/testing";

// 飞书详情页、OAuth 完成页与通知页飞书行。

const o = "/v1/organizations/o1/integrations";

const provider = {
  id: "feishu",
  name: "飞书",
  category: "collaboration",
  availability: "available",
  template: "C1",
  capabilities: ["notifications"],
  configured: true,
  bindable: true,
  summary: { connections: 0, status: "none" },
};

const guide = {
  open_platform_url: "https://open.feishu.cn/app",
  redirect_uri: "https://x.example/api/v1/integrations/feishu/callback",
  link_preview_pattern: "https://x.example/o/*/spaces/*/tasks*",
  scopes_json: '{"scopes":{"tenant":["im:message"]}}',
  events: ["im.message.receive_v1"],
  callbacks: ["url.preview.get"],
};

const app = (extra: Record<string, unknown> = {}) => ({
  connection_id: "c1",
  app_id: "cli_abcdef123456",
  bot_name: "OpenFox 助手",
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
  version: 4,
  ...extra,
});

const personal = {
  connection_id: "p1",
  display_name: "林小北",
  avatar_url: "",
  created_at: ts,
  version: 2,
};

const view = (extra: Record<string, unknown> = {}, canManage = true) => ({
  can_manage: canManage,
  app: null,
  personal: null,
  guide,
  ...extra,
});

const generic = (canManage = true) => ({
  provider,
  can_manage: canManage,
  connections: [],
  personal_connections: [],
  sync_links: [],
});

function route(body: unknown, more?: Route, canManage = true): Route {
  return (call) => {
    const custom = more?.(call);
    if (custom) return custom;
    if (call.method === "GET" && call.path === `${o}/feishu/app`)
      return json(200, body);
    if (call.method === "GET" && call.path === `${o}/feishu`)
      return json(200, generic(canManage));
    return undefined;
  };
}

const page = "/o/o1/settings/integrations/feishu";

beforeEach(() => localStorage.clear());
afterEach(() => vi.restoreAllMocks());

describe("飞书 · 绑定分区", () => {
  it("未绑定：管理员可点绑定，非管理员禁用并显示只读提示", async () => {
    renderSettings(page, route(view()));
    expect(await screen.findByText("尚未绑定飞书应用。")).toBeVisible();
    expect(screen.getByRole("button", { name: "绑定飞书" })).toBeEnabled();
  });

  it("非管理员", async () => {
    renderSettings(page, route(view({}, false), undefined, false), "MEMBER");
    expect(
      await screen.findByRole("button", { name: "绑定飞书" }),
    ).toBeDisabled();
    expect(
      screen.getByText(
        "只有组织所有者和管理员能绑定飞书应用，请联系其中一位。",
      ),
    ).toBeVisible();
  });

  it("绑定对话框：App ID 格式校验与提交成功", async () => {
    const user = userEvent.setup();
    let put: unknown;
    const calls = renderSettings(
      page,
      route(view(), (call) => {
        if (call.method === "PUT" && call.path === `${o}/feishu/app`) {
          put = call.body;
          return json(200, app({ status: "PENDING" }));
        }
        return undefined;
      }),
    );
    await user.click(await screen.findByRole("button", { name: "绑定飞书" }));
    const dialog = await screen.findByRole("dialog");
    const submit = within(dialog).getByRole("button", { name: "保存并连接" });
    expect(submit).toBeDisabled();
    await user.type(within(dialog).getByLabelText("App ID"), "bad");
    await user.type(
      within(dialog).getByLabelText("App Secret"),
      "secret-0123456789",
    );
    expect(submit).toBeDisabled();
    expect(within(dialog).getByText(/App ID 以 cli_ 开头/)).toBeVisible();
    await user.clear(within(dialog).getByLabelText("App ID"));
    await user.type(
      within(dialog).getByLabelText("App ID"),
      "cli_abcdef123456",
    );
    const secret = within(dialog).getByLabelText("App Secret");
    for (const [length, valid] of [
      [15, false],
      [16, true],
      [128, true],
      [129, false],
    ] as const) {
      await user.clear(secret);
      await user.type(secret, "x".repeat(length));
      if (valid) expect(submit).toBeEnabled();
      else {
        expect(submit).toBeDisabled();
        expect(
          within(dialog).getByText(/应用密钥需为 16–128 位/),
        ).toBeVisible();
      }
    }
    expect(put).toBeUndefined();
    await user.clear(secret);
    await user.type(secret, "secret-0123456789");
    expect(within(dialog).getByLabelText("App Secret")).toHaveAttribute(
      "type",
      "password",
    );
    await user.click(submit);
    await waitFor(() =>
      expect(put).toEqual({
        app_id: "cli_abcdef123456",
        app_secret: "secret-0123456789",
      }),
    );
    expect(
      await screen.findByText("飞书应用已绑定，正在建立长连接"),
    ).toBeVisible();
    expect(findCall(calls, "PUT", `${o}/feishu/app`)).toBeDefined();
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it.each([
    [422, "FEISHU_CREDENTIALS_INVALID", "App ID 或 App Secret 不正确"],
    [422, "FEISHU_BOT_UNAVAILABLE", "没能从飞书读到机器人信息"],
    [409, "FEISHU_APP_IN_USE", "已经绑定到其他 OpenFox 组织"],
    [409, "FEISHU_APP_MISMATCH", "更新凭证时不能更换 App ID"],
    [502, "FEISHU_UNAVAILABLE", "暂时连不上飞书开放平台"],
    [409, "VERSION_CONFLICT", "飞书应用刚被其他人修改过"],
  ])("绑定失败 %s %s 显示在表单内且不关闭", async (status, code, text) => {
    const user = userEvent.setup();
    renderSettings(
      page,
      route(view(), (call) =>
        call.method === "PUT" ? apiError(status, code) : undefined,
      ),
    );
    await user.click(await screen.findByRole("button", { name: "绑定飞书" }));
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
    expect(await within(dialog).findByText(new RegExp(text))).toBeVisible();
    expect(screen.getByRole("dialog")).toBeVisible();
  });

  it("更新凭证：App ID 预填只读并带 version", async () => {
    const user = userEvent.setup();
    let put: unknown;
    renderSettings(
      page,
      route(view({ app: app() }), (call) => {
        if (call.method === "PUT") {
          put = call.body;
          return json(200, app());
        }
        return undefined;
      }),
    );
    await user.click(
      await screen.findByRole("button", { name: "助手连接选项" }),
    );
    await user.click(await screen.findByRole("menuitem", { name: "更新凭证" }));
    const dialog = await screen.findByRole("dialog");
    const id = within(dialog).getByLabelText("App ID");
    expect(id).toHaveValue("cli_abcdef123456");
    expect(id).toHaveAttribute("readonly");
    await user.type(
      within(dialog).getByLabelText("App Secret"),
      "new-secret-0123456789",
    );
    await user.click(
      within(dialog).getByRole("button", { name: "保存并连接" }),
    );
    await waitFor(() =>
      expect(put).toEqual({
        app_id: "cli_abcdef123456",
        app_secret: "new-secret-0123456789",
        version: 4,
      }),
    );
    expect(await screen.findByText("凭证已更新")).toBeVisible();
  });

  it("配置指引：复制成功与失败", async () => {
    const user = userEvent.setup();
    const write = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      value: { writeText: write },
      configurable: true,
    });
    renderSettings(page, route(view()));
    await user.click(await screen.findByRole("button", { name: "绑定飞书" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText("第 1 / 6 步")).toBeVisible();
    await user.click(
      within(dialog).getByRole("button", { name: "复制重定向 URL" }),
    );
    await waitFor(() => expect(write).toHaveBeenCalledWith(guide.redirect_uri));
    expect(await screen.findByText("已复制")).toBeVisible();
    write.mockRejectedValue(new Error("denied"));
    await user.click(
      within(dialog).getByRole("button", { name: "复制权限 JSON" }),
    );
    expect(await screen.findByText(/复制失败。请手动选中/)).toBeVisible();
    await user.click(
      within(dialog).getByRole("button", { name: "第 4 / 6 步" }),
    );
    expect(within(dialog).getAllByText("第 4 / 6 步").length).toBeGreaterThan(
      0,
    );
  });

  it("状态徽标与诊断条：已连接无事件 / 有事件", async () => {
    renderSettings(page, route(view({ app: app() })));
    expect(await screen.findByText("已连接")).toBeVisible();
    expect(screen.getByText("长连接正常 · 尚未收到事件")).toBeVisible();
  });

  it("已连接且有最近事件", async () => {
    renderSettings(
      page,
      route(view({ app: app({ last_event_at: new Date().toISOString() }) })),
    );
    expect(await screen.findByText("长连接正常 · 最近事件 刚刚")).toBeVisible();
  });

  it.each([
    ["credentials_invalid", "", "飞书应用凭证已失效"],
    ["bot_unavailable", "", "没能从飞书读到机器人信息"],
    ["connection_limit", "", "长连接数已达上限（50 个）"],
    ["", "boom", "boom"],
  ])("需要重新验证：%s", async (reason, lastError, text) => {
    renderSettings(
      page,
      route(
        view({
          app: app({
            status: "NEEDS_ATTENTION",
            status_reason: reason,
            last_error: lastError,
          }),
        }),
      ),
    );
    expect(await screen.findByText("需要重新验证")).toBeVisible();
    expect(screen.getByText(new RegExp(text))).toBeVisible();
  });

  it("待连接超过 30 秒显示排查建议和 last_error", async () => {
    const old = new Date(Date.now() - 60_000).toISOString();
    renderSettings(
      page,
      route(
        view({
          app: app({
            status: "PENDING",
            updated_at: old,
            last_error: "dial x",
          }),
        }),
      ),
    );
    expect(await screen.findByText("待连接")).toBeVisible();
    expect(screen.getByText(/正在建立长连接…/)).toBeVisible();
    expect(screen.getByText("dial x")).toBeVisible();
  });

  it("重新验证成功提示", async () => {
    const user = userEvent.setup();
    const calls = renderSettings(
      page,
      route(view({ app: app() }), (call) =>
        call.method === "POST" && call.path === `${o}/feishu/app/verify`
          ? json(200, app())
          : undefined,
      ),
    );
    await user.click(
      await screen.findByRole("button", { name: "助手连接选项" }),
    );
    await user.click(await screen.findByRole("menuitem", { name: "重新验证" }));
    expect(await screen.findByText("已重新验证")).toBeVisible();
    expect(findCall(calls, "POST", `${o}/feishu/app/verify`)).toBeDefined();
  });

  it("任务链接预览开关：管理员可改，成员只读", async () => {
    const user = userEvent.setup();
    let patch: unknown;
    renderSettings(
      page,
      route(view({ app: app() }), (call) => {
        if (call.method === "PATCH") {
          patch = call.body;
          return json(200, app({ link_preview_enabled: false }));
        }
        return undefined;
      }),
    );
    const toggle = await screen.findByRole("switch", { name: "任务链接预览" });
    await user.click(toggle);
    await waitFor(() =>
      expect(patch).toEqual({ link_preview_enabled: false, version: 4 }),
    );
  });

  it("成员看到的开关禁用", async () => {
    renderSettings(
      page,
      route(view({ app: app() }, false), undefined, false),
      "MEMBER",
    );
    expect(
      await screen.findByRole("switch", { name: "任务链接预览" }),
    ).toBeDisabled();
    expect(screen.queryByRole("button", { name: "助手连接选项" })).toBeNull();
  });

  it("断开需要确认短语「断开 飞书」", async () => {
    const user = userEvent.setup();
    let body: unknown;
    renderSettings(
      page,
      route(view({ app: app() }), (call) => {
        if (call.path === `${o}/connections/c1/disconnect`) {
          body = call.body;
          return json(200, {});
        }
        return undefined;
      }),
    );
    await user.click(
      await screen.findByRole("button", { name: "助手连接选项" }),
    );
    await user.click(await screen.findByRole("menuitem", { name: "断开" }));
    const dialog = await screen.findByRole("dialog");
    expect(
      within(dialog).getByText(/成员的飞书账号绑定一并解除/),
    ).toBeVisible();
    await user.type(within(dialog).getByRole("textbox"), "断开 飞书");
    await user.click(within(dialog).getByRole("button", { name: "断开" }));
    await waitFor(() =>
      expect(body).toEqual({ version: 4, confirm_phrase: "断开 飞书" }),
    );
  });
});

describe("飞书 · 我的飞书账号", () => {
  /** 等区块从骨架变成内容；加载前后区块节点会被替换，所以每次重新查询 */
  const region = async () => {
    await screen.findByRole("region", { name: "我的 飞书 账号" });
    await waitFor(() =>
      expect(
        screen
          .getByRole("region", { name: "我的 飞书 账号" })
          .querySelector(".animate-pulse"),
      ).toBeNull(),
    );
    return screen.getByRole("region", { name: "我的 飞书 账号" });
  };

  it("没有组织应用或需要重新验证时禁用并提示管理员", async () => {
    renderSettings(page, route(view()));
    const r = await region();
    expect(within(r).getByText("需先由管理员连接飞书助手")).toBeVisible();
    expect(
      within(r).getByRole("button", { name: "关联飞书账号" }),
    ).toBeDisabled();
  });

  it("授权弹窗、message 完成后刷新并提示", async () => {
    const user = userEvent.setup();
    const open = vi
      .spyOn(window, "open")
      .mockReturnValue({ closed: false } as unknown as Window);
    renderSettings(
      page,
      route(view({ app: app() }), (call) =>
        call.method === "POST" && call.path === `${o}/feishu/authorize`
          ? json(200, {
              authorize_url: "https://accounts.feishu.cn/oauth",
              session_id: "s1",
              expires_at: ts,
            })
          : undefined,
      ),
    );
    const r = await region();
    await user.click(within(r).getByRole("button", { name: "关联飞书账号" }));
    await waitFor(() => expect(open).toHaveBeenCalledTimes(1));
    act(() => {
      window.dispatchEvent(
        new MessageEvent("message", {
          origin: window.location.origin,
          data: {
            type: "openfox:oauth",
            provider: "feishu",
            session: "s1",
            status: "ok",
            reason: "",
          },
        }),
      );
    });
    expect(
      await screen.findByText("已绑定飞书账号，飞书通知已开启"),
    ).toBeVisible();
  });

  it("授权失败 account_in_use 提示", async () => {
    const user = userEvent.setup();
    vi.spyOn(window, "open").mockReturnValue({
      closed: false,
    } as unknown as Window);
    renderSettings(
      page,
      route(view({ app: app() }), (call) =>
        call.method === "POST" && call.path === `${o}/feishu/authorize`
          ? json(200, {
              authorize_url: "https://a",
              session_id: "s2",
              expires_at: ts,
            })
          : undefined,
      ),
    );
    await user.click(
      within(await region()).getByRole("button", { name: "关联飞书账号" }),
    );
    await waitFor(() => expect(window.open).toHaveBeenCalled());
    act(() => {
      window.dispatchEvent(
        new MessageEvent("message", {
          origin: window.location.origin,
          data: {
            type: "openfox:oauth",
            provider: "feishu",
            session: "s2",
            status: "error",
            reason: "account_in_use",
          },
        }),
      );
    });
    expect(
      await screen.findByText("这个飞书账号已经绑定到本组织的其他成员。"),
    ).toBeVisible();
  });

  it("409 FEISHU_APP_NOT_CONNECTED 提示", async () => {
    const user = userEvent.setup();
    renderSettings(
      page,
      route(view({ app: app() }), (call) =>
        call.method === "POST" && call.path === `${o}/feishu/authorize`
          ? apiError(409, "FEISHU_APP_NOT_CONNECTED")
          : undefined,
      ),
    );
    await user.click(
      within(await region()).getByRole("button", { name: "关联飞书账号" }),
    );
    expect(await screen.findByText(/本组织还没有连接飞书助手/)).toBeVisible();
  });

  it("已绑定：显示姓名，解绑需确认", async () => {
    const user = userEvent.setup();
    let body: unknown;
    renderSettings(
      page,
      route(view({ app: app(), personal }), (call) => {
        if (call.path === `${o}/connections/p1/disconnect`) {
          body = call.body;
          return json(200, {});
        }
        return undefined;
      }),
    );
    const r = await region();
    expect(within(r).getByText("林小北")).toBeVisible();
    await user.click(within(r).getByRole("button", { name: "解绑" }));
    expect(
      await screen.findByText("解绑后不再通过飞书给你发送通知。"),
    ).toBeVisible();
    const dialog = screen.getByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: "解绑" }));
    await waitFor(() => expect(body).toEqual({ version: 2 }));
    expect(await screen.findByText("已解绑飞书账号")).toBeVisible();
  });
});

describe("OAuth 完成页 · 飞书", () => {
  it("成功与 account_in_use", async () => {
    renderSettings(
      "/integrations/oauth/complete?provider=feishu&session=s1&status=ok",
      () => undefined,
    );
    expect(await screen.findByText("飞书账号已绑定")).toBeVisible();
  });

  it("account_in_use 文案", async () => {
    renderSettings(
      "/integrations/oauth/complete?provider=feishu&session=s1&status=error&reason=account_in_use",
      () => undefined,
    );
    expect(
      await screen.findByText("这个飞书账号已经绑定到本组织的其他成员。"),
    ).toBeVisible();
  });
});

describe("通知页 · 飞书行", () => {
  const channels = (feishu: Record<string, unknown> = {}) => ({
    channels: [
      {
        channel: "desktop",
        enabled: true,
        disabled_events: [],
        customized: false,
      },
      {
        channel: "browser",
        enabled: true,
        disabled_events: [],
        customized: false,
      },
      {
        channel: "feishu",
        enabled: true,
        disabled_events: [],
        customized: false,
        ...feishu,
      },
    ],
  });
  const notificationRoute = (body: unknown, more?: Route): Route =>
    route(body, (call) => {
      if (call.method === "GET" && call.path === "/v1/me/notification-channels")
        return json(200, channels());
      return more?.(call);
    });
  const notifications = "/o/o1/settings/notifications";

  it("已绑定：显示开启状态，详情里有绑定姓名并可保存开关", async () => {
    const user = userEvent.setup();
    let saved: unknown;
    renderSettings(
      notifications,
      notificationRoute(view({ app: app(), personal }), (call) => {
        if (
          call.method === "PUT" &&
          call.path === "/v1/me/notification-channels/feishu"
        ) {
          saved = call.body;
          return json(200, channels({ enabled: false }).channels[2]);
        }
        return undefined;
      }),
    );
    await user.click(await screen.findByRole("button", { name: /飞书/ }));
    expect(
      await screen.findByText("通过飞书机器人私聊发送 · 已绑定 林小北"),
    ).toBeVisible();
    await user.click(screen.getByRole("switch", { name: "显示飞书通知" }));
    await waitFor(() => expect(saved).toMatchObject({ enabled: false }));
  });

  it("未绑定：状态为未绑定，详情按钮跳转到飞书集成页", async () => {
    const user = userEvent.setup();
    renderSettings(
      notifications,
      notificationRoute(view({ app: app() }), (call) =>
        call.path === "/v1/me/notification-channels/feishu"
          ? json(500, {})
          : undefined,
      ),
    );
    const row = await screen.findByRole("button", { name: /飞书/ });
    expect(within(row).getByText("未绑定")).toBeVisible();
    await user.click(row);
    expect(
      await screen.findByRole("button", { name: "去绑定飞书账号" }),
    ).toBeVisible();
    for (const toggle of screen.getAllByRole("switch"))
      expect(toggle).toBeDisabled();
  });

  it("组织没有飞书应用：说明改为需先由管理员连接", async () => {
    const user = userEvent.setup();
    renderSettings(notifications, notificationRoute(view()));
    await user.click(await screen.findByRole("button", { name: /飞书/ }));
    expect(
      await screen.findByText(/需先由管理员连接飞书，再绑定你的飞书账号/),
    ).toBeVisible();
  });
});
