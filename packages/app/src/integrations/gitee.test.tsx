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

// Gitee 详情页：沿用 GitHub 模板 B，OAuth 应用由部署方在服务端配置，所以没有实例地址 / 应用凭据表单。

const o = "/v1/organizations/o1/integrations";

const provider = (configured = true) => ({
  id: "gitee",
  name: "Gitee",
  category: "git",
  availability: "available",
  template: "B",
  capabilities: ["sync", "pr_link", "personal_account"],
  configured,
  bindable: configured,
  oauth_redirect_uri:
    "https://openfox.example/api/v1/integrations/gitee/callback",
  default_instance_url: "",
  summary: { connections: 0, status: "none" },
});

const conn = (
  extra: Record<string, unknown> = {},
): Record<string, unknown> => ({
  id: "c1",
  provider: "gitee",
  scope: "ORGANIZATION",
  external_id: "acme",
  display_name: "acme",
  account_kind: "organization",
  status: "ACTIVE",
  status_reason: "",
  credentials_hint: "",
  created_by: "u1",
  created_by_email: "lin@openfox.dev",
  created_at: ts,
  updated_at: ts,
  disconnected_at: null,
  avatar_url: "",
  webhook_last_delivery_at: null,
  instance_url: "",
  version: 3,
  ...extra,
});

const detail = (
  extra: Record<string, unknown> = {},
  canManage = true,
  configured = true,
) => ({
  provider: provider(configured),
  can_manage: canManage,
  connections: [],
  personal_connections: [],
  sync_links: [],
  ...extra,
});

const settingsBody = {
  provider: "gitee",
  settings: {
    link_pull_requests: true,
    link_commits: false,
    pr_backlink_private: true,
    pr_backlink_public: false,
    issue_backlink_private: true,
    issue_backlink_public: false,
    branch_format: "username/identifier-title",
  },
  can_manage: true,
  version: 2,
  updated_at: ts,
};

function route(body: unknown, more?: Route): Route {
  return (call) => {
    const custom = more?.(call);
    if (custom) return custom;
    if (call.method === "GET" && call.path === `${o}/gitee`)
      return json(200, body);
    if (call.method === "GET" && call.path === `${o}/gitee/settings`)
      return json(200, settingsBody);
    return undefined;
  };
}

const start = (call: { method: string; path: string }) =>
  call.method === "POST" && call.path === `${o}/gitee/authorize`;

const message = (data: unknown) =>
  act(() => {
    window.dispatchEvent(
      new MessageEvent("message", { data, origin: window.location.origin }),
    );
  });

const session = (status: string, accounts: unknown[] = []) => ({
  id: "s1",
  provider: "gitee",
  purpose: "organization",
  status,
  error_code: "",
  accounts,
  expires_at: ts,
});

let openSpy: ReturnType<typeof vi.spyOn>;
beforeEach(() => {
  localStorage.clear();
  openSpy = vi
    .spyOn(window, "open")
    .mockReturnValue({ closed: false } as unknown as Window);
});
afterEach(() => vi.restoreAllMocks());

describe("Gitee 未绑定", () => {
  it("绑定直接发起授权：不弹实例地址表单，请求体只有 purpose", async () => {
    const user = userEvent.setup();
    const calls = renderSettings(
      "/o/o1/settings/integrations/gitee",
      route(detail(), (call) => {
        if (start(call))
          return json(200, {
            authorize_url: "https://gitee.com/oauth/authorize?x=1",
            session_id: "s1",
            expires_at: ts,
          });
        if (call.path === `${o}/oauth/s1`) return json(200, session("PENDING"));
        return undefined;
      }),
    );
    expect(await screen.findByText("Gitee")).toBeVisible();
    await user.click(
      await screen.findByRole("button", { name: "绑定 Gitee 组织" }),
    );
    await waitFor(() =>
      expect(findCall(calls, "POST", `${o}/gitee/authorize`)?.body).toEqual({
        purpose: "organization",
      }),
    );
    expect(screen.queryByRole("dialog")).toBeNull();
    await waitFor(() => expect(openSpy).toHaveBeenCalledTimes(1));
    expect(openSpy.mock.calls[0]![0]).toBe(
      "https://gitee.com/oauth/authorize?x=1",
    );
    expect(await screen.findByText("正在等待 Gitee 授权完成…")).toBeVisible();
  });

  it("服务端未配置：提示未配置，绑定按钮禁用", async () => {
    renderSettings(
      "/o/o1/settings/integrations/gitee",
      route(detail({}, true, false)),
    );
    expect(
      await screen.findByRole("button", { name: "绑定 Gitee 组织" }),
    ).toBeDisabled();
    expect(screen.getByText("服务器尚未配置 Gitee 应用")).toBeVisible();
  });

  it("非管理员绑定按钮禁用", async () => {
    renderSettings(
      "/o/o1/settings/integrations/gitee",
      route(detail({}, false)),
      "MEMBER",
    );
    expect(
      await screen.findByRole("button", { name: "绑定 Gitee 组织" }),
    ).toBeDisabled();
  });

  it("授权回调后选择组织并完成绑定；授权失败显示 Gitee 文案", async () => {
    const user = userEvent.setup();
    let status = "PENDING";
    const calls = renderSettings(
      "/o/o1/settings/integrations/gitee",
      route(detail(), (call) => {
        if (start(call))
          return json(200, {
            authorize_url: "https://gitee.com/oauth/authorize",
            session_id: "s1",
            expires_at: ts,
          });
        if (call.method === "GET" && call.path === `${o}/oauth/s1`)
          return json(
            200,
            session(status, [
              {
                login: "acme",
                kind: "organization",
                avatar_url: "",
                connection_id: null,
              },
            ]),
          );
        if (call.method === "POST" && call.path === `${o}/oauth/s1/complete`)
          return json(200, { connections: [conn()] });
        return undefined;
      }),
    );
    await user.click(
      await screen.findByRole("button", { name: "绑定 Gitee 组织" }),
    );
    await waitFor(() => expect(openSpy).toHaveBeenCalledTimes(1));
    status = "SELECTING";
    message({
      type: "openfox:oauth",
      provider: "gitee",
      session: "s1",
      status: "ok",
      reason: "",
    });
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText("选择要绑定的 Gitee 组织")).toBeVisible();
    await user.click(within(dialog).getByRole("checkbox", { name: "acme" }));
    await user.click(within(dialog).getByRole("button", { name: "绑定所选" }));
    await waitFor(() =>
      expect(findCall(calls, "POST", `${o}/oauth/s1/complete`)?.body).toEqual({
        accounts: ["acme"],
      }),
    );
  });

  it("未配置时发起授权返回 409：不打开弹窗", async () => {
    const user = userEvent.setup();
    const calls = renderSettings(
      "/o/o1/settings/integrations/gitee",
      route(detail(), (call) =>
        start(call) ? apiError(409, "INTEGRATION_NOT_CONFIGURED") : undefined,
      ),
    );
    await user.click(
      await screen.findByRole("button", { name: "绑定 Gitee 组织" }),
    );
    await waitFor(() =>
      expect(findCall(calls, "POST", `${o}/gitee/authorize`)).toBeDefined(),
    );
    expect(openSpy).not.toHaveBeenCalled();
    expect(screen.queryByRole("dialog")).toBeNull();
  });
});

describe("Gitee 已连接", () => {
  const webhookBody = {
    connection_id: "c1",
    payload_url: "https://x.example/api/v1/hooks/git/gitee/tok",
    content_type: "",
    secret: "tok-secret",
    events: ["Push", "Pull Request", "Issues", "Comments"],
    last_delivery_at: null,
    recent: [],
    version: 3,
  };

  it("Webhook 区：Gitee 的步骤、密码 / 签名密钥说明，没有 Content type", async () => {
    const user = userEvent.setup();
    renderSettings(
      "/o/o1/settings/integrations/gitee",
      route(detail({ connections: [conn()] }), (call) =>
        call.method === "GET" && call.path === `${o}/connections/c1/webhook`
          ? json(200, webhookBody)
          : undefined,
      ),
    );
    await user.click(
      await screen.findByRole("button", { name: "配置 Webhook" }),
    );
    expect(await screen.findByRole("textbox", { name: "URL" })).toHaveValue(
      "https://x.example/api/v1/hooks/git/gitee/tok",
    );
    expect(
      screen.getByText("进入仓库（或企业）的「管理」→「WebHooks」"),
    ).toBeVisible();
    expect(
      screen.getByRole("textbox", { name: "密码 / 签名密钥" }),
    ).toBeVisible();
    expect(screen.queryByRole("textbox", { name: "Content type" })).toBeNull();
    expect(
      screen.getByText(/签名密钥模式用 HMAC-SHA256 签名并带时间戳/),
    ).toBeVisible();
    expect(
      screen.getByText(
        "在 WebHook 事件里勾选：Push、Pull Request、Issues、Comments",
      ),
    ).toBeVisible();
    // 页面里不出现原始文案键
    expect(document.body.textContent).not.toMatch(
      /integrations\.(gitee|github)\./,
    );
  });

  it("Pull Request 关联设置走 gitee 设置接口", async () => {
    const user = userEvent.setup();
    const calls = renderSettings(
      "/o/o1/settings/integrations/gitee",
      route(detail({ connections: [conn()] }), (call) =>
        call.method === "PUT" && call.path === `${o}/gitee/settings`
          ? json(200, settingsBody)
          : undefined,
      ),
    );
    const toggle = await screen.findByRole("switch", {
      name: "关联 Pull Request",
    });
    await user.click(toggle);
    await waitFor(() =>
      expect(findCall(calls, "PUT", `${o}/gitee/settings`)).toBeDefined(),
    );
  });

  it("断开确认短语是「断开 Gitee」", async () => {
    const user = userEvent.setup();
    const calls = renderSettings(
      "/o/o1/settings/integrations/gitee",
      route(detail({ connections: [conn()] }), (call) =>
        call.method === "POST" && call.path === `${o}/connections/c1/disconnect`
          ? json(200, conn({ status: "DISCONNECTED" }))
          : undefined,
      ),
    );
    await user.click(
      await screen.findByRole("button", { name: "acme 的更多操作" }),
    );
    await user.click(screen.getByRole("menuitem", { name: "断开" }));
    const dialog = screen.getByRole("dialog");
    await user.type(
      within(dialog).getByRole("textbox", { name: "确认短语" }),
      "断开 Gitee",
    );
    await user.click(within(dialog).getByRole("button", { name: "断开" }));
    await waitFor(() =>
      expect(
        findCall(calls, "POST", `${o}/connections/c1/disconnect`)?.body,
      ).toEqual({ version: 3, confirm_phrase: "断开 Gitee" }),
    );
  });

  it("NEEDS_ATTENTION 显示需要重新授权，重新授权带 connection_id", async () => {
    const user = userEvent.setup();
    const calls = renderSettings(
      "/o/o1/settings/integrations/gitee",
      route(
        detail({
          connections: [
            conn({ status: "NEEDS_ATTENTION", status_reason: "token_revoked" }),
          ],
        }),
        (call) =>
          start(call)
            ? json(200, {
                authorize_url: "https://gitee.com/oauth/authorize",
                session_id: "s3",
                expires_at: ts,
              })
            : call.path === `${o}/oauth/s3`
              ? json(200, {
                  ...session("PENDING"),
                  id: "s3",
                  purpose: "reauthorize",
                })
              : undefined,
      ),
    );
    expect(await screen.findByText("需要重新授权")).toBeVisible();
    await user.click(screen.getByRole("button", { name: "重新授权" }));
    await waitFor(() =>
      expect(findCall(calls, "POST", `${o}/gitee/authorize`)?.body).toEqual({
        purpose: "reauthorize",
        connection_id: "c1",
      }),
    );
  });
});

describe("Gitee 个人账号", () => {
  it("与 GitHub 一样：关联不带 connection_id", async () => {
    const user = userEvent.setup();
    const calls = renderSettings(
      "/o/o1/settings/integrations/gitee",
      route(detail({ connections: [conn()] }), (call) =>
        start(call)
          ? json(200, {
              authorize_url: "https://gitee.com/oauth/authorize",
              session_id: "s4",
              expires_at: ts,
            })
          : call.path === `${o}/oauth/s4`
            ? json(200, {
                ...session("PENDING"),
                id: "s4",
                purpose: "personal",
              })
            : undefined,
      ),
    );
    await user.click(
      await screen.findByRole("button", { name: "关联 Gitee 账号" }),
    );
    await waitFor(() =>
      expect(findCall(calls, "POST", `${o}/gitee/authorize`)?.body).toEqual({
        purpose: "personal",
      }),
    );
  });
});
