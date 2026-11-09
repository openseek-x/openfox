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

// GitLab 自部署版 / 极狐 GitLab.cn 详情页：沿用 GitHub 模板 B，绑定改为管理员填写实例与应用凭据。

const o = "/v1/organizations/o1/integrations";

const providerOf = (id: "gitlab" | "gitlab_cn") => ({
  id,
  name: id === "gitlab" ? "GitLab 自部署版" : "极狐 GitLab.cn",
  category: "git",
  availability: "available",
  template: "B",
  capabilities: ["sync", "pr_link", "personal_account"],
  configured: true,
  bindable: true,
  oauth_redirect_uri: `https://openfox.example/api/v1/integrations/${id}/callback`,
  default_instance_url: id === "gitlab_cn" ? "https://jihulab.com" : "",
  summary: { connections: 0, status: "none" },
});

const conn = (
  id = "gitlab",
  extra: Record<string, unknown> = {},
): Record<string, unknown> => ({
  id: "c1",
  provider: id,
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
  instance_url: "https://gitlab.example.com",
  version: 3,
  ...extra,
});

const detail = (
  id: "gitlab" | "gitlab_cn" = "gitlab",
  extra: Record<string, unknown> = {},
  canManage = true,
) => ({
  provider: providerOf(id),
  can_manage: canManage,
  connections: [],
  personal_connections: [],
  sync_links: [],
  ...extra,
});

const settingsBody = (id: string) => ({
  provider: id,
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
});

function route(id: "gitlab" | "gitlab_cn", body: unknown, more?: Route): Route {
  return (call) => {
    const custom = more?.(call);
    if (custom) return custom;
    if (call.method === "GET" && call.path === `${o}/${id}`)
      return json(200, body);
    if (call.method === "GET" && call.path === `${o}/${id}/settings`)
      return json(200, settingsBody(id));
    return undefined;
  };
}

const start = (id: string) => (call: { method: string; path: string }) =>
  call.method === "POST" && call.path === `${o}/${id}/authorize`;

const message = (data: unknown) =>
  act(() => {
    window.dispatchEvent(
      new MessageEvent("message", { data, origin: window.location.origin }),
    );
  });

let openSpy: ReturnType<typeof vi.spyOn>;
beforeEach(() => {
  localStorage.clear();
  openSpy = vi
    .spyOn(window, "open")
    .mockReturnValue({ closed: false } as unknown as Window);
});
afterEach(() => vi.restoreAllMocks());

describe("GitLab 未绑定：绑定表单", () => {
  it("令牌与实例地址直接连接，不打开 OAuth 弹窗", async () => {
    const user = userEvent.setup();
    const calls = renderSettings(
      "/o/o1/settings/integrations/gitlab",
      route("gitlab", detail(), (call) =>
        call.method === "POST" && call.path === `${o}/gitlab/token`
          ? json(200, conn())
          : undefined,
      ),
    );
    await user.click(await screen.findByRole("button", { name: "绑定实例" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByRole("button", { name: "连接" })).toBeDisabled();
    expect(within(dialog).queryByLabelText("Application ID")).toBeNull();
    await user.type(
      within(dialog).getByLabelText("个人访问令牌"),
      "glpat-test",
    );
    await user.type(
      within(dialog).getByLabelText("实例地址"),
      "https://gitlab.example.com",
    );
    await user.click(within(dialog).getByRole("button", { name: "连接" }));
    await waitFor(() =>
      expect(findCall(calls, "POST", `${o}/gitlab/token`)?.body).toEqual({
        purpose: "organization",
        instance_url: "https://gitlab.example.com",
        access_token: "glpat-test",
      }),
    );
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(openSpy).not.toHaveBeenCalled();
  });

  it("极狐预填默认实例地址，可直接提交", async () => {
    const user = userEvent.setup();
    const calls = renderSettings(
      "/o/o1/settings/integrations/gitlab_cn",
      route("gitlab_cn", detail("gitlab_cn"), (call) =>
        start("gitlab_cn")(call)
          ? json(200, {
              authorize_url: "https://jihulab.com/oauth/authorize",
              session_id: "s2",
              expires_at: ts,
            })
          : call.path === `${o}/oauth/s2`
            ? json(200, {
                id: "s2",
                provider: "gitlab_cn",
                purpose: "organization",
                status: "PENDING",
                error_code: "",
                accounts: [],
                expires_at: ts,
              })
            : undefined,
      ),
    );
    await user.click(await screen.findByRole("button", { name: "绑定实例" }));
    const dialog = await screen.findByRole("dialog");
    expect(
      within(dialog).getByRole("textbox", { name: "实例地址" }),
    ).toHaveValue("https://jihulab.com");
    await user.type(within(dialog).getByLabelText("Application ID"), "id");
    await user.type(within(dialog).getByLabelText("Application Secret"), "sec");
    await user.click(
      within(dialog).getByRole("button", { name: "保存并授权" }),
    );
    await waitFor(() =>
      expect(findCall(calls, "POST", `${o}/gitlab_cn/authorize`)?.body).toEqual(
        {
          purpose: "organization",
          instance_url: "https://jihulab.com",
          application_id: "id",
          application_secret: "sec",
        },
      ),
    );
  });

  it("INVALID_INSTANCE_URL 在表单内提示，表单保留", async () => {
    const user = userEvent.setup();
    renderSettings(
      "/o/o1/settings/integrations/gitlab",
      route("gitlab", detail(), (call) =>
        call.method === "POST" && call.path === `${o}/gitlab/token`
          ? apiError(400, "INVALID_INSTANCE_URL")
          : undefined,
      ),
    );
    await user.click(await screen.findByRole("button", { name: "绑定实例" }));
    const dialog = await screen.findByRole("dialog");
    await user.type(
      within(dialog).getByRole("textbox", { name: "实例地址" }),
      "https://10.0.0.1",
    );
    await user.type(
      within(dialog).getByLabelText("个人访问令牌"),
      "glpat-test",
    );
    await user.click(within(dialog).getByRole("button", { name: "连接" }));
    expect(
      await within(dialog).findByText("实例地址必须是公网 https 地址"),
    ).toBeVisible();
    expect(openSpy).not.toHaveBeenCalled();
  });

  it("非管理员绑定按钮禁用", async () => {
    renderSettings(
      "/o/o1/settings/integrations/gitlab",
      route("gitlab", detail("gitlab", {}, false)),
      "MEMBER",
    );
    expect(
      await screen.findByRole("button", { name: "绑定实例" }),
    ).toBeDisabled();
  });

  it("授权回调后选择群组并完成绑定", async () => {
    const user = userEvent.setup();
    let status = "PENDING";
    const calls = renderSettings(
      "/o/o1/settings/integrations/gitlab_cn",
      route("gitlab_cn", detail("gitlab_cn"), (call) => {
        if (start("gitlab_cn")(call))
          return json(200, {
            authorize_url: "https://gitlab.example.com/a",
            session_id: "s1",
            expires_at: ts,
          });
        if (call.method === "GET" && call.path === `${o}/oauth/s1`)
          return json(200, {
            id: "s1",
            provider: "gitlab_cn",
            purpose: "organization",
            status,
            error_code: "",
            accounts: [
              {
                login: "acme",
                kind: "organization",
                avatar_url: "",
                connection_id: null,
              },
            ],
            expires_at: ts,
          });
        if (call.method === "POST" && call.path === `${o}/oauth/s1/complete`)
          return json(200, { connections: [conn("gitlab_cn")] });
        return undefined;
      }),
    );
    await user.click(await screen.findByRole("button", { name: "绑定实例" }));
    const form = await screen.findByRole("dialog");
    await user.type(
      within(form).getByRole("textbox", { name: "实例地址" }),
      "https://gitlab.example.com",
    );
    await user.type(within(form).getByLabelText("Application ID"), "id");
    await user.type(within(form).getByLabelText("Application Secret"), "sec");
    await user.click(within(form).getByRole("button", { name: "保存并授权" }));
    await waitFor(() => expect(openSpy).toHaveBeenCalledTimes(1));
    status = "SELECTING";
    message({
      type: "openfox:oauth",
      provider: "gitlab_cn",
      session: "s1",
      status: "ok",
      reason: "",
    });
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText("选择要绑定的群组")).toBeVisible();
    await user.click(within(dialog).getByRole("checkbox", { name: "acme" }));
    await user.click(within(dialog).getByRole("button", { name: "绑定所选" }));
    await waitFor(() =>
      expect(findCall(calls, "POST", `${o}/oauth/s1/complete`)?.body).toEqual({
        accounts: ["acme"],
      }),
    );
  });
});

describe("GitLab 已连接", () => {
  const webhookBody = (id = "gitlab") => ({
    connection_id: "c1",
    payload_url: `https://x.example/api/v1/hooks/git/${id}/tok`,
    content_type: "",
    secret: "tok-secret",
    events: [
      "Merge request events",
      "Push events",
      "Issues events",
      "Comments",
    ],
    last_delivery_at: null,
    recent: [],
    version: 3,
  });

  it("显示实例主机名；Webhook 区用 Secret token 文案且没有 Content type", async () => {
    const user = userEvent.setup();
    renderSettings(
      "/o/o1/settings/integrations/gitlab",
      route("gitlab", detail("gitlab", { connections: [conn()] }), (call) =>
        call.method === "GET" && call.path === `${o}/connections/c1/webhook`
          ? json(200, webhookBody())
          : undefined,
      ),
    );
    expect(await screen.findByText("实例 gitlab.example.com")).toBeVisible();
    expect(screen.getByText("群组")).toBeVisible();
    await user.click(
      await screen.findByRole("button", { name: "配置 Webhook" }),
    );
    expect(await screen.findByRole("textbox", { name: "URL" })).toHaveValue(
      "https://x.example/api/v1/hooks/git/gitlab/tok",
    );
    expect(screen.getByRole("textbox", { name: "Secret token" })).toBeVisible();
    expect(screen.queryByRole("textbox", { name: "Content type" })).toBeNull();
    expect(screen.getByText(/X-Gitlab-Token 明文发送/)).toBeVisible();
    expect(
      screen.getByText(
        "在 Trigger 里勾选：Merge request events、Push events、Issues events、Comments",
      ),
    ).toBeVisible();
  });

  it("合并请求关联设置走 gitlab 设置接口", async () => {
    const user = userEvent.setup();
    const calls = renderSettings(
      "/o/o1/settings/integrations/gitlab",
      route("gitlab", detail("gitlab", { connections: [conn()] }), (call) =>
        call.method === "PUT" && call.path === `${o}/gitlab/settings`
          ? json(200, settingsBody("gitlab"))
          : undefined,
      ),
    );
    expect(await screen.findByText("Git MR & Commit 关联")).toBeVisible();
    const toggle = await screen.findByRole("switch", {
      name: "关联合并请求（MR）",
    });
    await user.click(toggle);
    await waitFor(() =>
      expect(findCall(calls, "PUT", `${o}/gitlab/settings`)).toBeDefined(),
    );
  });

  it.each([
    ["gitlab", "GitLab 自部署版"],
    ["gitlab_cn", "极狐 GitLab.cn"],
  ] as const)("%s 断开确认短语取目录名称", async (id, name) => {
    const user = userEvent.setup();
    const calls = renderSettings(
      `/o/o1/settings/integrations/${id}`,
      route(id, detail(id, { connections: [conn(id)] }), (call) =>
        call.method === "POST" && call.path === `${o}/connections/c1/disconnect`
          ? json(200, conn(id, { status: "DISCONNECTED" }))
          : undefined,
      ),
    );
    await user.click(
      await screen.findByRole("button", { name: "acme 的更多操作" }),
    );
    await user.click(screen.getByRole("menuitem", { name: "断开" }));
    const dialog = screen.getByRole("dialog");
    expect(
      within(dialog).getByText(
        "确定断开 acme 吗？来自该群组的合并请求关联和任务回链将停止工作",
      ),
    ).toBeVisible();
    await user.type(
      within(dialog).getByRole("textbox", { name: "确认短语" }),
      `断开 ${name}`,
    );
    await user.click(within(dialog).getByRole("button", { name: "断开" }));
    await waitFor(() =>
      expect(
        findCall(calls, "POST", `${o}/connections/c1/disconnect`)?.body,
      ).toEqual({ version: 3, confirm_phrase: `断开 ${name}` }),
    );
  });

  it("NEEDS_ATTENTION 显示需要重新授权，重新授权带 connection_id", async () => {
    const user = userEvent.setup();
    const calls = renderSettings(
      "/o/o1/settings/integrations/gitlab",
      route(
        "gitlab",
        detail("gitlab", {
          connections: [
            conn("gitlab", {
              status: "NEEDS_ATTENTION",
              status_reason: "token_revoked",
            }),
          ],
        }),
        (call) =>
          call.method === "POST" && call.path === `${o}/gitlab/token`
            ? json(200, {
                authorize_url: "https://gitlab.example.com/a",
                session_id: "s3",
                expires_at: ts,
              })
            : call.path === `${o}/oauth/s3`
              ? json(200, {
                  id: "s3",
                  provider: "gitlab",
                  purpose: "reauthorize",
                  status: "PENDING",
                  error_code: "",
                  accounts: [],
                  expires_at: ts,
                })
              : undefined,
      ),
    );
    expect(await screen.findByText("需要重新授权")).toBeVisible();
    await user.click(screen.getByRole("button", { name: "重新授权" }));
    const tokenDialog = await screen.findByRole("dialog");
    await user.type(
      within(tokenDialog).getByLabelText("个人访问令牌"),
      "glpat-test",
    );
    await user.click(within(tokenDialog).getByRole("button", { name: "连接" }));
    await waitFor(() =>
      expect(findCall(calls, "POST", `${o}/gitlab/token`)?.body).toEqual({
        purpose: "reauthorize",
        access_token: "glpat-test",
        version: 3,
        connection_id: "c1",
      }),
    );
  });
});

describe("GitLab 个人账号", () => {
  const two = [
    conn("gitlab", { id: "c1", display_name: "acme" }),
    conn("gitlab", {
      id: "c2",
      display_name: "beta",
      instance_url: "https://git.beta.io",
    }),
  ];

  it("需要先选择实例，关联带 connection_id", async () => {
    const user = userEvent.setup();
    const calls = renderSettings(
      "/o/o1/settings/integrations/gitlab",
      route("gitlab", detail("gitlab", { connections: two }), (call) =>
        call.method === "POST" && call.path === `${o}/gitlab/token`
          ? json(200, {
              authorize_url: "https://git.beta.io/a",
              session_id: "s4",
              expires_at: ts,
            })
          : call.path === `${o}/oauth/s4`
            ? json(200, {
                id: "s4",
                provider: "gitlab",
                purpose: "personal",
                status: "PENDING",
                error_code: "",
                accounts: [],
                expires_at: ts,
              })
            : undefined,
      ),
    );
    expect(
      await screen.findByText(
        "关联后，你在 GitLab 自部署版 上提交的合并请求会显示为你的操作",
      ),
    ).toBeVisible();
    await user.click(await screen.findByRole("button", { name: "选择实例" }));
    await user.click(
      screen.getByRole("option", { name: "beta · git.beta.io" }),
    );
    await user.click(
      screen.getByRole("button", { name: "关联 GitLab 自部署版 账号" }),
    );
    const tokenDialog = await screen.findByRole("dialog");
    await user.type(
      within(tokenDialog).getByLabelText("个人访问令牌"),
      "glpat-test",
    );
    await user.click(within(tokenDialog).getByRole("button", { name: "连接" }));
    await waitFor(() =>
      expect(findCall(calls, "POST", `${o}/gitlab/token`)?.body).toEqual({
        purpose: "personal",
        access_token: "glpat-test",
        connection_id: "c2",
      }),
    );
  });

  it("没有实例时提示管理员先绑定，且没有关联按钮", async () => {
    renderSettings(
      "/o/o1/settings/integrations/gitlab",
      route("gitlab", detail()),
    );
    expect(
      await screen.findByText("管理员还没有绑定实例，暂时无法关联个人账号"),
    ).toBeVisible();
    expect(
      screen.queryByRole("button", { name: "关联 GitLab 自部署版 账号" }),
    ).toBeNull();
  });

  it("个人连接需要重新授权时可直接重新授权", async () => {
    const user = userEvent.setup();
    const calls = renderSettings(
      "/o/o1/settings/integrations/gitlab",
      route(
        "gitlab",
        detail("gitlab", {
          connections: [conn()],
          personal_connections: [
            conn("gitlab", {
              id: "p1",
              scope: "PERSONAL",
              parent_connection_id: "c1",
              external_id: "42",
              display_name: "lin",
              account_kind: "user",
              status: "NEEDS_ATTENTION",
              status_reason: "token_revoked",
            }),
          ],
        }),
        (call) =>
          call.method === "POST" && call.path === `${o}/gitlab/token`
            ? json(200, {
                authorize_url: "https://gitlab.example.com/a",
                session_id: "s5",
                expires_at: ts,
              })
            : call.path === `${o}/oauth/s5`
              ? json(200, {
                  id: "s5",
                  provider: "gitlab",
                  purpose: "reauthorize",
                  status: "PENDING",
                  error_code: "",
                  accounts: [],
                  expires_at: ts,
                })
              : undefined,
      ),
    );
    const buttons = await screen.findAllByRole("button", { name: "重新授权" });
    await user.click(buttons[buttons.length - 1]!);
    const tokenDialog = await screen.findByRole("dialog");
    await user.type(
      within(tokenDialog).getByLabelText("个人访问令牌"),
      "glpat-test",
    );
    await user.click(within(tokenDialog).getByRole("button", { name: "连接" }));
    await waitFor(() =>
      expect(findCall(calls, "POST", `${o}/gitlab/token`)?.body).toEqual({
        purpose: "reauthorize",
        access_token: "glpat-test",
        version: 3,
        connection_id: "p1",
      }),
    );
  });
});
