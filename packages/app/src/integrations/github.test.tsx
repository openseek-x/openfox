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

// GitHub 详情页：OAuth 绑定、连接状态、Webhook 指引、设置与个人账号。

const o = "/v1/organizations/o1/integrations";

const githubProvider = {
  id: "github",
  name: "GitHub",
  category: "git",
  availability: "available",
  template: "B",
  capabilities: ["sync", "pr_link", "personal_account"],
  configured: true,
  bindable: true,
  summary: { connections: 0, status: "none" },
};

const conn = (extra: Record<string, unknown> = {}) => ({
  id: "c1",
  provider: "github",
  scope: "ORGANIZATION",
  external_id: "openfox-dev",
  display_name: "openfox-dev",
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
  version: 3,
  ...extra,
});

const detail = (extra: Record<string, unknown> = {}, canManage = true) => ({
  provider: githubProvider,
  can_manage: canManage,
  connections: [],
  personal_connections: [],
  sync_links: [],
  ...extra,
});

const settingsBody = (
  extra: Record<string, unknown> = {},
  canManage = true,
) => ({
  provider: "github",
  settings: {
    link_pull_requests: true,
    link_commits: false,
    pr_backlink_private: true,
    pr_backlink_public: false,
    branch_format: "username/identifier-title",
    ...extra,
  },
  can_manage: canManage,
  version: 2,
  updated_at: ts,
});

const webhookBody = (extra: Record<string, unknown> = {}) => ({
  connection_id: "c1",
  payload_url: "https://x.example/api/v1/hooks/git/github/tok",
  content_type: "application/json",
  secret: "s3cr3t-value",
  events: ["pull_request", "push"],
  last_delivery_at: null,
  recent: [],
  version: 3,
  ...extra,
});

/** 详情、设置按默认值返回；more 优先。 */
function route(
  body: unknown,
  more?: Route,
  settings: unknown = settingsBody(),
): Route {
  return (call) => {
    const custom = more?.(call);
    if (custom) return custom;
    if (call.method === "GET" && call.path === `${o}/github`)
      return json(200, body);
    if (call.method === "GET" && call.path === `${o}/github/settings`)
      return json(200, settings);
    return undefined;
  };
}

const start = (call: { method: string; path: string }) =>
  call.method === "POST" && call.path === `${o}/github/authorize`;

const message = (data: unknown) =>
  act(() => {
    window.dispatchEvent(
      new MessageEvent("message", { data, origin: window.location.origin }),
    );
  });

const session = (extra: Record<string, unknown> = {}) => ({
  id: "s1",
  provider: "github",
  purpose: "organization",
  status: "PENDING",
  error_code: "",
  accounts: [],
  expires_at: ts,
  ...extra,
});

let openSpy: ReturnType<typeof vi.spyOn>;
beforeEach(() => {
  localStorage.clear();
  openSpy = vi
    .spyOn(window, "open")
    .mockReturnValue({ closed: false } as unknown as Window);
});
afterEach(() => vi.restoreAllMocks());

describe("GitHub 绑定流程", () => {
  it("弹窗授权、message 通知、选择组织并完成绑定", async () => {
    const user = userEvent.setup();
    let status = "PENDING";
    const calls = renderSettings(
      "/o/o1/settings/integrations/github",
      route(detail(), (call) => {
        if (start(call))
          return json(200, {
            authorize_url: "https://github.example/login/oauth/authorize?x=1",
            session_id: "s1",
            expires_at: ts,
          });
        if (call.method === "GET" && call.path === `${o}/oauth/s1`)
          return json(
            200,
            session({
              status,
              accounts: [
                {
                  login: "openfox-dev",
                  kind: "organization",
                  avatar_url: "",
                  connection_id: null,
                },
                {
                  login: "lin",
                  kind: "user",
                  avatar_url: "",
                  connection_id: "c9",
                },
              ],
            }),
          );
        if (call.method === "POST" && call.path === `${o}/oauth/s1/complete`)
          return json(200, { connections: [conn()] });
        return undefined;
      }),
    );
    await user.click(
      await screen.findByRole("button", { name: "绑定 GitHub 组织" }),
    );
    await waitFor(() => expect(openSpy).toHaveBeenCalledTimes(1));
    const [url, , features] = openSpy.mock.calls[0]!;
    expect(url).toBe("https://github.example/login/oauth/authorize?x=1");
    expect(features).toContain("width=980");
    expect(features).toContain("height=760");
    expect(findCall(calls, "POST", `${o}/github/authorize`)?.body).toEqual({
      purpose: "organization",
    });
    expect(await screen.findByText("正在等待 GitHub 授权完成…")).toBeVisible();

    // 其它来源或类型不符的消息被忽略
    status = "SELECTING";
    message({ type: "other", provider: "github", session: "s1" });
    expect(screen.queryByRole("dialog")).toBeNull();
    message({
      type: "openfox:oauth",
      provider: "github",
      session: "s1",
      status: "ok",
      reason: "",
    });
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText("选择要绑定的 GitHub 组织")).toBeVisible();
    expect(within(dialog).getByText("已绑定 · 将更新授权")).toBeVisible();
    const confirm = within(dialog).getByRole("button", { name: "绑定所选" });
    expect(confirm).toBeDisabled();
    await user.click(
      within(dialog).getByRole("checkbox", { name: "openfox-dev" }),
    );
    await user.click(within(dialog).getByRole("checkbox", { name: "lin" }));
    await user.click(confirm);
    await waitFor(() =>
      expect(findCall(calls, "POST", `${o}/oauth/s1/complete`)?.body).toEqual({
        accounts: ["openfox-dev", "lin"],
      }),
    );
    expect(await screen.findByText("已绑定 GitHub")).toBeVisible();
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("轮询兜底：没有 message 也能进入选择", async () => {
    const user = userEvent.setup();
    let polls = 0;
    renderSettings(
      "/o/o1/settings/integrations/github",
      route(detail(), (call) => {
        if (start(call))
          return json(200, {
            authorize_url: "https://github.example/a",
            session_id: "s1",
            expires_at: ts,
          });
        if (call.method === "GET" && call.path === `${o}/oauth/s1`) {
          polls += 1;
          return json(
            200,
            session(
              polls >= 2
                ? {
                    status: "SELECTING",
                    accounts: [
                      {
                        login: "acme",
                        kind: "organization",
                        avatar_url: "",
                        connection_id: null,
                      },
                    ],
                  }
                : {},
            ),
          );
        }
        return undefined;
      }),
    );
    await user.click(
      await screen.findByRole("button", { name: "绑定 GitHub 组织" }),
    );
    expect(
      await screen.findByRole("dialog", {}, { timeout: 6000 }),
    ).toBeVisible();
    expect(polls).toBeGreaterThanOrEqual(2);
  }, 10000);

  it("弹窗被拦截时提示手动打开链接", async () => {
    const user = userEvent.setup();
    openSpy.mockReturnValue(null);
    renderSettings(
      "/o/o1/settings/integrations/github",
      route(detail(), (call) => {
        if (start(call))
          return json(200, {
            authorize_url: "https://github.example/manual",
            session_id: "s1",
            expires_at: ts,
          });
        if (call.method === "GET" && call.path === `${o}/oauth/s1`)
          return json(200, session());
        return undefined;
      }),
    );
    await user.click(
      await screen.findByRole("button", { name: "绑定 GitHub 组织" }),
    );
    const link = await screen.findByRole("link", {
      name: "打开 GitHub 授权页",
    });
    expect(link).toHaveAttribute("href", "https://github.example/manual");
    expect(link).toHaveAttribute("rel", "noopener noreferrer");
  });

  it.each([
    ["FAILED", "access_denied", "你取消了授权"],
    ["FAILED", "exchange_failed", "GitHub 授权失败，请重试"],
    ["FAILED", "account_mismatch", "授权的 GitHub 账号无法访问该组织"],
    ["FAILED", "invalid_state", "授权已过期，请重试"],
    ["EXPIRED", "", "授权已过期，请重试"],
  ])("会话 %s / %s 显示中文原因", async (status, code, text) => {
    const user = userEvent.setup();
    renderSettings(
      "/o/o1/settings/integrations/github",
      route(detail(), (call) => {
        if (start(call))
          return json(200, {
            authorize_url: "https://github.example/a",
            session_id: "s1",
            expires_at: ts,
          });
        if (call.method === "GET" && call.path === `${o}/oauth/s1`)
          return json(200, session({ status, error_code: code }));
        return undefined;
      }),
    );
    await user.click(
      await screen.findByRole("button", { name: "绑定 GitHub 组织" }),
    );
    expect(await screen.findByText(text)).toBeVisible();
  });

  it("未配置或非管理员时绑定按钮禁用", async () => {
    const body = detail({}, false);
    renderSettings("/o/o1/settings/integrations/github", route(body), "MEMBER");
    expect(
      await screen.findByRole("button", { name: "绑定 GitHub 组织" }),
    ).toBeDisabled();
  });
});

describe("GitHub 连接列表", () => {
  it("启用者、状态按钮与重新授权", async () => {
    const user = userEvent.setup();
    const calls = renderSettings(
      "/o/o1/settings/integrations/github",
      route(
        detail({
          connections: [
            conn(),
            conn({
              id: "c2",
              display_name: "acme",
              external_id: "acme",
              status: "NEEDS_ATTENTION",
            }),
            conn({
              id: "c3",
              display_name: "old",
              external_id: "old",
              status: "DISCONNECTED",
            }),
          ],
        }),
        (call) =>
          start(call)
            ? json(200, {
                authorize_url: "https://github.example/a",
                session_id: "s1",
                expires_at: ts,
              })
            : call.path === `${o}/oauth/s1`
              ? json(200, session({ purpose: "reauthorize" }))
              : undefined,
      ),
    );
    expect(await screen.findByText("启用者")).toBeVisible();
    expect(screen.getByText("启用于 2026-10-20")).toBeVisible();
    expect(screen.getByText("已绑定")).toBeVisible();
    expect(screen.getByText("需要重新授权")).toBeVisible();
    expect(screen.getByText("已断开")).toBeVisible();
    expect(screen.getByRole("button", { name: "重新连接" })).toBeVisible();
    await user.click(screen.getByRole("button", { name: "重新授权" }));
    await waitFor(() =>
      expect(findCall(calls, "POST", `${o}/github/authorize`)?.body).toEqual({
        purpose: "reauthorize",
        connection_id: "c2",
      }),
    );
  });

  it("断开确认文案与短语", async () => {
    const user = userEvent.setup();
    const calls = renderSettings(
      "/o/o1/settings/integrations/github",
      route(detail({ connections: [conn()] }), (call) =>
        call.method === "POST" && call.path === `${o}/connections/c1/disconnect`
          ? json(200, conn({ status: "DISCONNECTED" }))
          : undefined,
      ),
    );
    await user.click(
      await screen.findByRole("button", { name: "openfox-dev 的更多操作" }),
    );
    await user.click(screen.getByRole("menuitem", { name: "断开" }));
    const dialog = screen.getByRole("dialog");
    expect(
      within(dialog).getByText(
        "确定断开 openfox-dev 吗？来自该组织的 Pull Request 关联和任务回链将停止工作",
      ),
    ).toBeVisible();
    await user.type(
      within(dialog).getByRole("textbox", { name: "确认短语" }),
      "断开 GitHub",
    );
    await user.click(within(dialog).getByRole("button", { name: "断开" }));
    await waitFor(() =>
      expect(
        findCall(calls, "POST", `${o}/connections/c1/disconnect`)?.body,
      ).toEqual({ version: 3, confirm_phrase: "断开 GitHub" }),
    );
  });
});

describe("Webhook 指引", () => {
  const webhookRoute =
    (more?: Route): Route =>
    (call) =>
      call.method === "GET" && call.path === `${o}/connections/c1/webhook`
        ? json(200, webhookBody())
        : more?.(call);

  it("展开后显示字段，密钥默认掩码，可显示、复制，重新生成需二次确认", async () => {
    const user = userEvent.setup();
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      value: { writeText },
      configurable: true,
    });
    const calls = renderSettings(
      "/o/o1/settings/integrations/github",
      route(
        detail({ connections: [conn()] }),
        webhookRoute((call) =>
          call.method === "POST" &&
          call.path === `${o}/connections/c1/webhook/rotate`
            ? json(200, webhookBody({ secret: "new-secret", version: 4 }))
            : undefined,
        ),
      ),
    );
    await user.click(
      await screen.findByRole("button", { name: "配置 Webhook" }),
    );
    expect(
      await screen.findByRole("textbox", { name: "Payload URL" }),
    ).toHaveValue("https://x.example/api/v1/hooks/git/github/tok");
    expect(screen.getByRole("textbox", { name: "Content type" })).toHaveValue(
      "application/json",
    );
    const secret = screen.getByRole("textbox", { name: "Secret" });
    expect(secret).not.toHaveValue("s3cr3t-value");
    await user.click(screen.getByRole("button", { name: "显示" }));
    expect(secret).toHaveValue("s3cr3t-value");
    await user.click(screen.getByRole("button", { name: "复制 Secret" }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith("s3cr3t-value"));

    // 重新生成：先确认
    await user.click(screen.getByRole("button", { name: "重新生成" }));
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByText(/旧地址立即失效/)).toBeVisible();
    expect(calls.some((c) => c.path.endsWith("/webhook/rotate"))).toBe(false);
    await user.click(within(dialog).getByRole("button", { name: "重新生成" }));
    await waitFor(() =>
      expect(
        findCall(calls, "POST", `${o}/connections/c1/webhook/rotate`)?.body,
      ).toEqual({ version: 3 }),
    );
  });

  it("已接通显示最近一次时间和投递表", async () => {
    const user = userEvent.setup();
    renderSettings(
      "/o/o1/settings/integrations/github",
      route(
        detail({
          connections: [conn({ webhook_last_delivery_at: ts })],
        }),
        (call) =>
          call.method === "GET" && call.path === `${o}/connections/c1/webhook`
            ? json(
                200,
                webhookBody({
                  last_delivery_at: ts,
                  recent: [
                    {
                      event: "pull_request",
                      action: "opened",
                      status: "PROCESSED",
                      detail: "",
                      linked_count: 2,
                      received_at: ts,
                    },
                  ],
                }),
              )
            : undefined,
      ),
    );
    await user.click(
      await screen.findByRole("button", { name: /Webhook 已接通 · 最近一次/ }),
    );
    expect(await screen.findByText("pull_request.opened")).toBeVisible();
    expect(screen.getByText("已处理")).toBeVisible();
  });

  it("非管理员不请求也不显示密钥，只有提示", async () => {
    const user = userEvent.setup();
    const calls = renderSettings(
      "/o/o1/settings/integrations/github",
      route(detail({ connections: [conn()] }, false), webhookRoute()),
      "MEMBER",
    );
    await user.click(
      await screen.findByRole("button", { name: "配置 Webhook" }),
    );
    expect(
      screen.getByText("只有组织所有者和管理员能查看 Webhook 配置"),
    ).toBeVisible();
    expect(screen.queryByRole("textbox", { name: "Secret" })).toBeNull();
    expect(calls.some((c) => c.path.endsWith("/webhook"))).toBe(false);
  });
});

describe("PR / Commit 关联设置", () => {
  it("开关一改就保存并带 version；绑定为空时显示提示条", async () => {
    const user = userEvent.setup();
    const calls = renderSettings(
      "/o/o1/settings/integrations/github",
      route(detail(), (call) =>
        call.method === "PUT" && call.path === `${o}/github/settings`
          ? json(200, settingsBody({ link_commits: true }))
          : undefined,
      ),
    );
    expect(
      await screen.findByText(
        "需配置 Webhook · 该功能需要在 GitHub 中配置 Webhook",
      ),
    ).toBeVisible();
    const commits = await screen.findByRole("switch", {
      name: "关联 Git Commit",
    });
    expect(
      screen.getByRole("switch", { name: "关联 Pull Request" }),
    ).toBeChecked();
    expect(commits).not.toBeChecked();
    await user.click(commits);
    await waitFor(() =>
      expect(findCall(calls, "PUT", `${o}/github/settings`)?.body).toEqual({
        version: 2,
        settings: {
          link_pull_requests: true,
          link_commits: true,
          pr_backlink_private: true,
          pr_backlink_public: false,
          branch_format: "username/identifier-title",
        },
      }),
    );
    await waitFor(() =>
      expect(
        screen.getByRole("switch", { name: "关联 Git Commit" }),
      ).toBeChecked(),
    );
  });

  it("分支格式下拉保存；版本冲突时提示并刷新", async () => {
    const user = userEvent.setup();
    const calls = renderSettings(
      "/o/o1/settings/integrations/github",
      route(detail(), (call) =>
        call.method === "PUT" ? apiError(409, "VERSION_CONFLICT") : undefined,
      ),
    );
    await user.click(await screen.findByRole("button", { name: "分支格式" }));
    await user.click(screen.getByRole("option", { name: "identifier" }));
    expect(await screen.findByText("内容已被他人修改，已刷新")).toBeVisible();
    expect(
      (
        findCall(calls, "PUT", `${o}/github/settings`)?.body as {
          settings: { branch_format: string };
        }
      ).settings.branch_format,
    ).toBe("identifier");
    await waitFor(() =>
      expect(
        calls.filter(
          (c) => c.method === "GET" && c.path === `${o}/github/settings`,
        ).length,
      ).toBeGreaterThan(1),
    );
  });

  it("非管理员开关与下拉禁用", async () => {
    renderSettings(
      "/o/o1/settings/integrations/github",
      route(detail({}, false), undefined, settingsBody({}, false)),
      "MEMBER",
    );
    expect(
      await screen.findByRole("switch", { name: "关联 Git Commit" }),
    ).toBeDisabled();
    expect(screen.getByRole("button", { name: "分支格式" })).toBeDisabled();
  });
});

describe("个人 GitHub 账号", () => {
  it("未绑定时关联账号走 personal；已绑定可断开且无需确认短语", async () => {
    const user = userEvent.setup();
    const calls = renderSettings(
      "/o/o1/settings/integrations/github",
      route(detail(), (call) =>
        start(call)
          ? json(200, {
              authorize_url: "https://github.example/a",
              session_id: "s1",
              expires_at: ts,
            })
          : call.path === `${o}/oauth/s1`
            ? json(
                200,
                session({
                  purpose: "personal",
                  status: "COMPLETED",
                  connection_id: "p1",
                }),
              )
            : undefined,
      ),
    );
    expect(
      await screen.findByText(
        "关联后，你在 GitHub 上提交的 Pull Request 会显示为你的操作",
      ),
    ).toBeVisible();
    await user.click(screen.getByRole("button", { name: "关联 GitHub 账号" }));
    await waitFor(() =>
      expect(findCall(calls, "POST", `${o}/github/authorize`)?.body).toEqual({
        purpose: "personal",
      }),
    );
    expect(await screen.findByText("已关联 GitHub 账号")).toBeVisible();
  });

  it("已绑定显示 login，断开不需要确认短语", async () => {
    const user = userEvent.setup();
    const calls = renderSettings(
      "/o/o1/settings/integrations/github",
      route(
        detail({
          personal_connections: [
            conn({
              id: "p1",
              scope: "PERSONAL",
              display_name: "lin",
              external_id: "42",
              account_kind: "user",
              version: 2,
            }),
          ],
        }),
        (call) =>
          call.method === "POST" &&
          call.path === `${o}/connections/p1/disconnect`
            ? json(200, conn({ id: "p1", status: "DISCONNECTED" }))
            : undefined,
      ),
    );
    expect(await screen.findByText("lin")).toBeVisible();
    expect(
      screen.queryByRole("button", { name: "关联 GitHub 账号" }),
    ).toBeNull();
    await user.click(screen.getByRole("button", { name: "lin 的更多操作" }));
    await user.click(screen.getByRole("menuitem", { name: "断开" }));
    await user.click(
      within(screen.getByRole("dialog")).getByRole("button", { name: "断开" }),
    );
    await waitFor(() =>
      expect(
        findCall(calls, "POST", `${o}/connections/p1/disconnect`)?.body,
      ).toEqual({
        version: 2,
      }),
    );
  });
});

describe("OAuth 完成页", () => {
  afterEach(() => {
    Object.defineProperty(window, "opener", {
      value: null,
      configurable: true,
    });
  });

  it("有同源 opener 时 postMessage 并关闭窗口", async () => {
    const postMessage = vi.fn();
    const close = vi.spyOn(window, "close").mockImplementation(() => undefined);
    Object.defineProperty(window, "opener", {
      value: { postMessage },
      configurable: true,
    });
    renderSettings(
      "/integrations/oauth/complete?provider=github&session=s1&status=ok&reason=",
      () => undefined,
    );
    await waitFor(() => expect(postMessage).toHaveBeenCalledTimes(1));
    expect(postMessage).toHaveBeenCalledWith(
      {
        type: "openfox:oauth",
        provider: "github",
        session: "s1",
        status: "ok",
        reason: "",
      },
      window.location.origin,
    );
    expect(close).toHaveBeenCalled();
  });

  it("没有 opener 时显示结果与返回链接", async () => {
    localStorage.setItem("openfox:last-org", "o1");
    renderSettings(
      "/integrations/oauth/complete?provider=github&session=s1&status=error&reason=access_denied",
      () => undefined,
    );
    expect(await screen.findByText("授权失败")).toBeVisible();
    expect(screen.getByText("你取消了授权")).toBeVisible();
    expect(screen.getByRole("link", { name: "返回集成页" })).toHaveAttribute(
      "href",
      "/o/o1/settings/integrations/github",
    );
  });
});
