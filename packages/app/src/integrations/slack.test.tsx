import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";
import {
  createApi,
  HttpClient,
  type SlackApp,
  type SlackConfiguration,
  type SlackDelivery,
} from "@openfox/api";
import { I18nProvider } from "@openfox/i18n";
import { ApiProvider } from "../context";
import {
  apiError,
  json,
  ts,
  renderSettings,
  type Call,
} from "../settings/testing";
import { SlackSections } from "./SlackSections";
import { SlackSession } from "./SlackOAuth";
import {
  slackAuthorizeURL,
  slackSettingsURL,
  slackID,
  slackBodyFits,
} from "./slack-model";
import { slackKey } from "./slack-hooks";

const base = "/v1/organizations/o1/integrations/slack";
const app: SlackApp = {
  id: "central",
  connection_id: "conn",
  parent_app_id: "",
  bot_id: "bot-central",
  kind: "CENTRAL",
  name: "Fixture workspace",
  external_app_id: "A123",
  team_id: "T123",
  owner_slack_user_id: "U123",
  bot_user_id: "UBOT",
  agent_id: "",
  space_id: "",
  created_by: "u1",
  state: "ACTIVE",
  status_reason: "",
  version: 3,
  generation: 2,
  url_verified: true,
  installed: true,
  events_url:
    "https://fixture.invalid/api/v1/integrations/slack/events/central",
  can_manage: true,
  created_at: ts,
};
const dynamic: SlackApp = {
  ...app,
  id: "dynamic",
  parent_app_id: app.id,
  kind: "AGENT",
  agent_id: "a1",
  name: "Fixture Agent",
  state: "WAITING_INSTALL",
  external_app_id: "A456",
  bot_id: "bot-dynamic",
  installed: false,
  url_verified: false,
};
function fixture(
  overrides: Partial<SlackConfiguration> = {},
  extra?: (
    c: Call,
    i?: RequestInit,
  ) => Response | Promise<Response> | undefined,
) {
  const config: SlackConfiguration = {
    configured: true,
    can_manage: true,
    oauth_redirect_uri:
      "https://fixture.invalid/api/v1/integrations/slack/callback",
    identity_redirect_uri:
      "https://fixture.invalid/api/v1/integrations/slack/identity/callback",
    central_events_url: app.events_url,
    apps: [{ ...app }],
    identities: [
      {
        id: "identity",
        connection_id: "personal",
        parent_app_id: "central",
        owner_user_id: "u1",
        team_id: "T123",
        slack_user_id: "U123",
        active: true,
        version: 2,
      },
    ],
    blocked_targets: [],
    ...overrides,
  };
  const calls: Call[] = [];
  const transport = async (input: RequestInfo | URL, init?: RequestInit) => {
    const u = new URL(String(input), "https://fixture.invalid"),
      c: Call = {
        path: u.pathname.replace(/^\/api/, ""),
        method: init?.method ?? "GET",
        body: init?.body ? JSON.parse(String(init.body)) : undefined,
        query: u.searchParams,
        headers: new Headers(init?.headers),
      };
    calls.push(c);
    const response = extra?.(c, init);
    if (response) return response;
    if (c.path === `${base}/configuration`) return json(200, config);
    if (c.path.endsWith("/oauth/start"))
      return json(201, {
        id: "session1",
        app_id: "central",
        purpose: (c.body as { purpose: string }).purpose,
        status: "PENDING",
        status_reason: "",
        authorize_url: "https://slack.com/oauth/v2/authorize?state=synthetic",
        expires_at: "2099-01-01T00:00:00Z",
      });
    if (c.path.endsWith("/oauth/sessions/session1"))
      return json(200, {
        id: "session1",
        app_id: "central",
        purpose: "organization",
        status: "PENDING",
        status_reason: "",
        authorize_url: "https://slack.com/oauth/v2/authorize?state=synthetic",
        expires_at: "2099-01-01T00:00:00Z",
      });
    if (c.path === `${base}/apps/operations`) {
      config.apps.push({ ...dynamic });
      return json(202, dynamic);
    }
    if (c.path.endsWith("/channels") && c.method === "GET")
      return json(200, { channels: [], next_cursor: "" });
    if (c.path.endsWith("/deliveries"))
      return json(200, { deliveries: [], next_cursor: "" });
    if (c.path === "/v1/me") return json(200, { id: "u1" });
    if (c.path === "/v1/me/organizations")
      return json(200, { organizations: [{ id: "o1", role: "OWNER" }] });
    if (c.path.endsWith("/agent-directory"))
      return json(200, {
        entries: [
          {
            id: "a1",
            name: "First Agent",
            type: "agent",
            mine: true,
            space_id: "s1",
            archived_at: null,
          },
          {
            id: "a2",
            name: "Second Agent",
            type: "agent",
            mine: true,
            space_id: "s1",
            archived_at: null,
          },
        ],
      });
    if (c.path.endsWith("/spaces"))
      return json(200, { spaces: [{ id: "s1", name: "Fixture space" }] });
    if (c.path.endsWith("/devices")) return json(200, { devices: [] });
    return apiError(404, "NOT_FOUND");
  };
  return { config, calls, transport };
}
function mount(
  f: ReturnType<typeof fixture>,
  element = <SlackSections orgId="o1" />,
) {
  const cache = new QueryClient({
    defaultOptions: {
      queries: { retry: false, staleTime: Infinity },
      mutations: { retry: false },
    },
  });
  const api = createApi(new HttpClient("/api", f.transport));
  const view = render(
    <I18nProvider initialLocale="zh-CN">
      <QueryClientProvider client={cache}>
        <ApiProvider api={api}>{element}</ApiProvider>
      </QueryClientProvider>
    </I18nProvider>,
  );
  return { ...view, cache };
}
async function openCreate(user: ReturnType<typeof userEvent.setup>) {
  await user.click(
    await screen.findByRole("button", { name: "创建 Agent App" }),
  );
  await user.click(screen.getByLabelText("Agent"));
  await user.click(await screen.findByRole("option", { name: "First Agent" }));
  await user.type(screen.getByLabelText("Slack App 名称"), "My Agent");
  await user.type(
    screen.getByLabelText("Configuration refresh token"),
    "synthetic-refresh-only",
  );
  await user.click(
    screen.getByRole("checkbox", {
      name: "我了解该凭据的应用管理权限和轮换影响",
    }),
  );
}

describe("Slack control UI", () => {
  it("checks fixed Slack OAuth origin/path and known settings IDs", () => {
    expect(
      slackAuthorizeURL(
        "https://slack.com/oauth/v2/authorize?state=a",
        "organization",
      ),
    ).toBe(true);
    expect(
      slackAuthorizeURL(
        "https://slack.com/openid/connect/authorize?state=a",
        "personal",
      ),
    ).toBe(true);
    for (const url of [
      "https://evil.invalid/oauth/v2/authorize",
      "https://slack.com@evil.invalid/oauth/v2/authorize",
      "https://slack.com/oauth/v2/authorize#x",
      "http://slack.com/oauth/v2/authorize",
      "https://slack.com/api/apps.delete",
    ])
      expect(slackAuthorizeURL(url, "organization")).toBe(false);
    expect(slackSettingsURL("A123")).toBe("https://api.slack.com/apps/A123");
    expect(slackSettingsURL("A123/../../x")).toBeUndefined();
  });
  it("keeps opaque product IDs within 64 code points and applies the actual UTF-8 request budget", () => {
    expect(slackID("opaque-lowercase-id")).toBe(true);
    expect(slackID("🦊".repeat(64))).toBe(true);
    expect(slackID("🦊".repeat(65))).toBe(false);
    expect(slackID("a\nb")).toBe(false);
    expect(slackID("with space")).toBe(false);
    expect(slackBodyFits("a".repeat(8190))).toBe(true);
    expect(slackBodyFits("a".repeat(8191))).toBe(false);
    expect(slackBodyFits({ extra_prompt: "文".repeat(4000) })).toBe(false);
  });
  it("displays real missing configuration and hides organization management for members", async () => {
    const f = fixture({
      configured: false,
      can_manage: false,
      apps: [{ ...app, can_manage: false }],
      identities: [],
    });
    mount(f);
    expect(await screen.findByText(/服务器尚未完整配置/)).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "安装工作区应用" }),
    ).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "绑定本人身份" })).toBeDisabled();
    expect(f.calls.filter((c) => c.method !== "GET")).toHaveLength(0);
  });
  it("opens personal-only controls from account settings and unbinds the observed identity version", async () => {
    const user = userEvent.setup(),
      f = fixture();
    const calls = renderSettings("/o/o1/settings/account-binding", (c) => {
      if (c.path === "/v1/organizations/o1/me/account-bindings")
        return json(200, {
          providers: [
            {
              id: "slack",
              name: "Slack",
              availability: "available",
              method: "oauth",
              organization_ready: true,
              binding: null,
            },
          ],
          jira_sites: [],
          email: "fixture@example.invalid",
        });
      if (c.path === `${base}/configuration`) return json(200, f.config);
      if (c.path === `${base}/personal/identity` && c.method === "DELETE") {
        f.config.identities = [];
        return json(204);
      }
      return undefined;
    });
    await user.click(
      await screen.findByRole("button", { name: "管理 Slack 身份" }),
    );
    expect(
      await screen.findByRole("button", { name: "解除本人绑定" }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "安装工作区应用" }),
    ).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "解除本人绑定" }));
    const dialogs = screen.getAllByRole("dialog");
    await user.click(
      within(dialogs[dialogs.length - 1]!).getByRole("button", {
        name: "解除本人绑定",
      }),
    );
    await waitFor(() =>
      expect(calls.find((c) => c.method === "DELETE")?.body).toEqual({
        version: 2,
      }),
    );
    expect(calls.find((c) => c.method === "DELETE")?.path).toBe(
      `${base}/personal/identity`,
    );
    expect(
      await screen.findByRole("button", { name: "绑定本人身份" }),
    ).toBeInTheDocument();
  });
  it("keeps manual URL verification and OAuth installation separate", async () => {
    mount(fixture({ apps: [app, dynamic] }));
    const row = await screen.findByRole("article", { name: "Fixture Agent" });
    expect(
      within(row).getByText(/等待请求 URL 核验.*等待 OAuth 安装/),
    ).toBeInTheDocument();
    expect(within(row).queryByText("已启用")).not.toBeInTheDocument();
    expect(
      within(row).getByRole("link", { name: "打开 Slack App 设置" }),
    ).toHaveAttribute("href", "https://api.slack.com/apps/A456");
  });
  it("submits explicit refresh proof once, clears it, and never caches secrets", async () => {
    const user = userEvent.setup(),
      f = fixture(),
      v = mount(f);
    await openCreate(user);
    await user.click(screen.getByRole("button", { name: "创建独立应用" }));
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
    const sent = f.calls.find(
      (c) => c.method === "POST" && c.path === `${base}/apps/operations`,
    )!;
    expect(sent.body).toMatchObject({
      parent_app_id: "central",
      parent_version: 3,
      purpose: "agent",
      agent_id: "a1",
      configuration_refresh_token: "synthetic-refresh-only",
    });
    expect(v.cache.getMutationCache().getAll()).toHaveLength(0);
    expect(
      JSON.stringify(
        v.cache
          .getQueryCache()
          .getAll()
          .map((q) => q.state.data),
      ),
    ).not.toContain("synthetic-refresh-only");
    expect(JSON.stringify(localStorage)).not.toContain(
      "synthetic-refresh-only",
    );
    expect(JSON.stringify(sessionStorage)).not.toContain(
      "synthetic-refresh-only",
    );
  });
  it("filters targets reserved by another provider before accepting a refresh token", async () => {
    const user = userEvent.setup();
    mount(
      fixture({
        blocked_targets: [
          { kind: "AGENT", agent_id: "a1", space_id: "s1", provider: "feishu" },
        ],
      }),
    );
    await user.click(
      await screen.findByRole("button", { name: "创建 Agent App" }),
    );
    await user.click(screen.getByLabelText("Agent"));
    expect(
      await screen.findByRole("option", { name: "Second Agent" }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("option", { name: "First Agent" }),
    ).not.toBeInTheDocument();
  });
  it("clears submitted secret on conflict and blocks stale resubmission", async () => {
    const user = userEvent.setup(),
      f = fixture({}, (c) =>
        c.method === "POST" && c.path === `${base}/apps/operations`
          ? apiError(409, "VERSION_CONFLICT")
          : undefined,
      );
    mount(f);
    await openCreate(user);
    await user.click(screen.getByRole("button", { name: "创建独立应用" }));
    expect(await screen.findByText(/状态或授权已变化/)).toBeInTheDocument();
    expect(screen.getByLabelText("Configuration refresh token")).toHaveValue(
      "",
    );
    expect(screen.getByRole("button", { name: "创建独立应用" })).toBeDisabled();
    expect(f.calls.filter((c) => c.method === "POST")).toHaveLength(1);
  });
  it("treats a malformed success response as uncertain rather than permitting another credential exchange", async () => {
    const user = userEvent.setup(),
      f = fixture({}, (c) =>
        c.method === "POST" && c.path === `${base}/apps/operations`
          ? new Response("partial-json", {
              status: 202,
              headers: { "Content-Type": "application/json" },
            })
          : undefined,
      );
    mount(f);
    await openCreate(user);
    await user.click(screen.getByRole("button", { name: "创建独立应用" }));
    expect(await screen.findByText(/请求结果不确定/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "创建独立应用" })).toBeDisabled();
    expect(screen.getByLabelText("Configuration refresh token")).toHaveValue(
      "",
    );
    expect(f.calls.filter((c) => c.method === "POST")).toHaveLength(1);
  });
  it("closing a pending secret request aborts it and ignores the late result", async () => {
    let resolve!: (r: Response) => void, signal: AbortSignal | undefined;
    const user = userEvent.setup(),
      f = fixture({}, (c, i) => {
        if (c.method === "POST" && c.path === `${base}/apps/operations`) {
          signal = i?.signal ?? undefined;
          return new Promise<Response>((r) => (resolve = r));
        }
        return undefined;
      }),
      v = mount(f);
    await openCreate(user);
    await user.click(screen.getByRole("button", { name: "创建独立应用" }));
    await user.click(
      within(screen.getByRole("dialog")).getByRole("button", { name: "取消" }),
    );
    expect(signal?.aborted).toBe(true);
    await act(async () => resolve(json(202, dynamic)));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(v.cache.getMutationCache().getAll()).toHaveLength(0);
  });
  it("UNKNOWN cancellation requires explicit external-cleanup acknowledgement", async () => {
    const user = userEvent.setup(),
      unknown = { ...dynamic, state: "CREATE_UNKNOWN" };
    const f = fixture({ apps: [app, unknown] }, (c) =>
      c.path.endsWith("/cancel")
        ? json(200, { ...unknown, state: "CANCELLED" })
        : undefined,
    );
    mount(f);
    const row = await screen.findByRole("article", { name: "Fixture Agent" });
    await user.click(
      within(row).getByRole("button", { name: "取消并释放绑定" }),
    );
    const dialog = screen.getByRole("dialog"),
      button = within(dialog).getByRole("button", { name: "取消并释放绑定" });
    expect(button).toBeDisabled();
    await user.click(within(dialog).getByRole("checkbox"));
    await user.click(button);
    await waitFor(() =>
      expect(f.calls.some((c) => c.path.endsWith("/cancel"))).toBe(true),
    );
    expect(f.calls.find((c) => c.path.endsWith("/cancel"))!.body).toEqual({
      version: 3,
      confirm_external_cleanup: true,
    });
  });
  it("retains an explicit reinstall and confirmed release path after local disconnect", async () => {
    const user = userEvent.setup(),
      disconnected = {
        ...dynamic,
        state: "DISCONNECTED",
        installed: false,
        url_verified: true,
      };
    const f = fixture({ apps: [app, disconnected] }, (c) =>
      c.path.endsWith("/cancel")
        ? json(200, { ...disconnected, state: "CANCELLED" })
        : undefined,
    );
    mount(f);
    const row = await screen.findByRole("article", { name: "Fixture Agent" });
    await user.click(within(row).getByRole("button", { name: "重新安装" }));
    await waitFor(() =>
      expect(f.calls.some((c) => c.path.endsWith("/oauth/start"))).toBe(true),
    );
    expect(
      f.calls.find((c) => c.path.endsWith("/oauth/start"))?.body,
    ).toMatchObject({ purpose: "app", app_id: "dynamic", version: 3 });
    const session = screen.getByRole("dialog");
    await user.click(
      within(session).getAllByRole("button", { name: "关闭" })[0]!,
    );
    await user.click(
      within(row).getByRole("button", { name: "取消并释放绑定" }),
    );
    const dialog = screen.getByRole("dialog"),
      release = within(dialog).getByRole("button", { name: "取消并释放绑定" });
    expect(release).toBeDisabled();
    await user.click(within(dialog).getByRole("checkbox"));
    await user.click(release);
    await waitFor(() =>
      expect(f.calls.find((c) => c.path.endsWith("/cancel"))?.body).toEqual({
        version: 3,
        confirm_external_cleanup: true,
      }),
    );
    expect(
      f.calls.filter(
        (c) => c.method === "POST" && c.path === `${base}/apps/operations`,
      ),
    ).toHaveLength(0);
  });
  it("requires public channel consent and sends its exact current version", async () => {
    const user = userEvent.setup(),
      active = {
        ...dynamic,
        state: "ACTIVE",
        installed: true,
        url_verified: true,
      };
    const f = fixture({ apps: [app, active] }, (c) =>
      c.method === "PATCH"
        ? json(200, {
            id: "row1",
            channel_id: "C123",
            name: "channel",
            enabled: true,
            version: 1,
            generation: 1,
            public_reply_consent: true,
            last_seen_at: ts,
            space_id: "s1",
          })
        : undefined,
    );
    mount(f);
    const row = await screen.findByRole("article", { name: "Fixture Agent" });
    await user.click(
      within(row).getByRole("button", { name: "频道公开回复授权" }),
    );
    await user.type(screen.getByLabelText("Slack 频道 ID"), "C123");
    const button = screen.getByRole("button", { name: "启用频道回复" });
    expect(button).toBeDisabled();
    await user.click(screen.getByRole("checkbox", { name: /频道内所有成员/ }));
    await user.click(button);
    await waitFor(() =>
      expect(f.calls.some((c) => c.method === "PATCH")).toBe(true),
    );
    expect(f.calls.find((c) => c.method === "PATCH")!.body).toEqual({
      version: 0,
      enabled: true,
      public_reply_consent: true,
    });
  });
  it("only offers verify/skip for UNKNOWN and preserves the exact timestamp and version without resending", async () => {
    const user = userEvent.setup();
    let delivery: SlackDelivery = {
      id: "delivery1",
      app_id: "dynamic",
      source_kind: "REPLY",
      part: 1,
      state: "UNKNOWN",
      status_reason: "",
      channel_id: "C123",
      thread_ts: "1710000000.000001",
      remote_ts: "",
      created_at: ts,
      version: 7,
    };
    const f = fixture(
      {
        apps: [
          app,
          { ...dynamic, state: "ACTIVE", installed: true, url_verified: true },
        ],
      },
      (c) => {
        if (c.path.endsWith("/deliveries"))
          return json(200, {
            deliveries: [
              delivery,
              { ...delivery, id: "done", part: 2, state: "DONE" },
            ],
            next_cursor: "",
          });
        if (c.path.endsWith("/resolve")) {
          delivery = { ...delivery, version: 8 };
          return json(200, delivery);
        }
        return undefined;
      },
    );
    mount(f);
    const row = await screen.findByRole("article", { name: "Fixture Agent" });
    await user.click(within(row).getByRole("button", { name: "发送记录" }));
    await user.click(
      await screen.findByRole("button", { name: "核对已有消息" }),
    );
    const dialogs = screen.getAllByRole("dialog"),
      dialog = dialogs[dialogs.length - 1]!,
      button = within(dialog).getByRole("button", { name: "核对已有消息" });
    expect(button).toBeDisabled();
    await user.type(
      within(dialog).getByLabelText("原消息 Slack 时间戳"),
      "1710000000.000100",
    );
    await user.click(button);
    expect(await screen.findByText(/未能确认匹配消息/)).toBeInTheDocument();
    expect(f.calls.find((c) => c.path.endsWith("/resolve"))?.body).toEqual({
      version: 7,
      action: "verify",
      remote_ts: "1710000000.000100",
    });
    expect(
      screen.getAllByRole("button", { name: "核对已有消息" }),
    ).toHaveLength(1);
    expect(
      screen.queryByRole("button", { name: /重发/ }),
    ).not.toBeInTheDocument();
    expect(f.calls.filter((c) => c.method === "POST")).toHaveLength(1);
  });
  it("requires a deliberate skip confirmation and keeps sent parts visible", async () => {
    const user = userEvent.setup();
    let current: SlackDelivery = {
      id: "delivery1",
      app_id: "dynamic",
      source_kind: "REPLY",
      part: 2,
      state: "UNKNOWN",
      status_reason: "",
      channel_id: "C123",
      thread_ts: "1710000000.000001",
      remote_ts: "",
      created_at: ts,
      version: 7,
    };
    const f = fixture(
      {
        apps: [
          app,
          { ...dynamic, state: "ACTIVE", installed: true, url_verified: true },
        ],
      },
      (c) => {
        if (c.path.endsWith("/deliveries"))
          return json(200, {
            deliveries: [
              { ...current, id: "first", part: 1, state: "DONE" },
              current,
            ],
            next_cursor: "",
          });
        if (c.path.endsWith("/resolve")) {
          current = { ...current, state: "SKIPPED", version: 8 };
          return json(200, current);
        }
        return undefined;
      },
    );
    mount(f);
    const row = await screen.findByRole("article", { name: "Fixture Agent" });
    await user.click(within(row).getByRole("button", { name: "发送记录" }));
    await user.click(
      await screen.findByRole("button", { name: "停止后续发送" }),
    );
    const dialogs = screen.getAllByRole("dialog"),
      dialog = dialogs[dialogs.length - 1]!;
    expect(
      within(dialog).getByText(/停止同一来源剩余未完成的分片/),
    ).toBeInTheDocument();
    expect(f.calls.filter((c) => c.method === "POST")).toHaveLength(0);
    await user.click(
      within(dialog).getByRole("button", { name: "停止后续发送" }),
    );
    await screen.findByText("已记录停止操作。已经发送的分片保留。");
    await waitFor(() =>
      expect(
        screen.queryByRole("button", { name: "停止后续发送" }),
      ).not.toBeInTheDocument(),
    );
    expect(screen.getByText(/已确认发送 · 分片 1/)).toBeInTheDocument();
    expect(f.calls.find((c) => c.path.endsWith("/resolve"))?.body).toEqual({
      version: 7,
      action: "skip",
      remote_ts: "",
    });
  });
  it("keeps NOTICE and UNFURL unknown deliveries read-only", async () => {
    const user = userEvent.setup();
    const f = fixture(
      {
        apps: [
          app,
          { ...dynamic, state: "ACTIVE", installed: true, url_verified: true },
        ],
      },
      (c) =>
        c.path.endsWith("/deliveries")
          ? json(200, {
              deliveries: ["NOTICE", "UNFURL"].map((kind) => ({
                id: kind,
                app_id: "dynamic",
                source_kind: kind,
                part: 1,
                state: "UNKNOWN",
                status_reason: "",
                channel_id: "C123",
                thread_ts: "",
                remote_ts: "",
                created_at: ts,
                version: 1,
              })),
              next_cursor: "",
            })
          : undefined,
    );
    mount(f);
    const row = await screen.findByRole("article", { name: "Fixture Agent" });
    await user.click(within(row).getByRole("button", { name: "发送记录" }));
    expect(await screen.findAllByText(/结果未知 · 分片 1/)).toHaveLength(2);
    expect(
      screen.queryByRole("button", { name: "核对已有消息" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "停止后续发送" }),
    ).not.toBeInTheDocument();
    expect(f.calls.every((c) => c.method === "GET")).toBe(true);
  });
  it("does not replay a stale manual-resolution version after a 409", async () => {
    const user = userEvent.setup();
    let version = 7;
    const f = fixture(
      {
        apps: [
          app,
          { ...dynamic, state: "ACTIVE", installed: true, url_verified: true },
        ],
      },
      (c) => {
        if (c.path.endsWith("/deliveries"))
          return json(200, {
            deliveries: [
              {
                id: "delivery1",
                app_id: "dynamic",
                source_kind: "REPLY",
                part: 1,
                state: "UNKNOWN",
                status_reason: "",
                channel_id: "C123",
                thread_ts: "",
                remote_ts: "",
                created_at: ts,
                version,
              },
            ],
            next_cursor: "",
          });
        if (c.path.endsWith("/resolve")) {
          version = 9;
          return apiError(409, "VERSION_CONFLICT");
        }
        return undefined;
      },
    );
    mount(f);
    const row = await screen.findByRole("article", { name: "Fixture Agent" });
    await user.click(within(row).getByRole("button", { name: "发送记录" }));
    await user.click(
      await screen.findByRole("button", { name: "停止后续发送" }),
    );
    const dialogs = screen.getAllByRole("dialog"),
      dialog = dialogs[dialogs.length - 1]!;
    await user.click(
      within(dialog).getByRole("button", { name: "停止后续发送" }),
    );
    expect(await screen.findByText(/状态或授权已变化/)).toBeInTheDocument();
    expect(
      within(dialog).getByRole("button", { name: "停止后续发送" }),
    ).toBeDisabled();
    expect(f.calls.filter((c) => c.path.endsWith("/resolve"))).toHaveLength(1);
  });
  it("clears a recovery form when current target management is revoked", async () => {
    const user = userEvent.setup(),
      pending = { ...dynamic, state: "CREATE_UNKNOWN" };
    const f = fixture({ apps: [app, pending] }),
      v = mount(f);
    const row = await screen.findByRole("article", { name: "Fixture Agent" });
    await user.click(within(row).getByRole("button", { name: "恢复已知应用" }));
    await user.type(
      screen.getByLabelText("Client Secret"),
      "synthetic-client-secret",
    );
    await user.type(
      screen.getByLabelText("Configuration refresh token"),
      "synthetic-refresh-secret",
    );
    act(() =>
      v.cache.setQueryData(slackKey("o1"), {
        ...f.config,
        apps: [app, { ...pending, can_manage: false }],
      }),
    );
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
    expect(document.body.textContent).not.toContain("synthetic-client-secret");
    expect(v.cache.getMutationCache().getAll()).toHaveLength(0);
    expect(f.calls.every((c) => c.method === "GET")).toBe(true);
  });
  it("does not trust a callback status and only accepts persisted DONE", async () => {
    const f = fixture({}, (c) =>
      c.path.includes("oauth/sessions")
        ? json(200, {
            id: "s1",
            app_id: "central",
            purpose: "personal",
            status: "UNKNOWN",
            status_reason: "",
            authorize_url: "",
            expires_at: "2099-01-01T00:00:00Z",
          })
        : undefined,
    );
    mount(f, <SlackSession orgId="o1" sessionId="s1" />);
    expect(await screen.findByText(/授权交换结果未知/)).toBeInTheDocument();
    expect(screen.queryByText("Slack 授权已完成")).not.toBeInTheDocument();
    expect(f.calls.every((c) => c.method === "GET")).toBe(true);
  });
});
