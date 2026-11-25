import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { describe, it, expect } from "vitest";
import {
  createApi,
  HttpClient,
  type QQConfiguration,
  type QQBot,
  type QQBotDetail,
  type QQBindingSession,
  type QQQRSession,
} from "@openfox/api";
import { I18nProvider } from "@openfox/i18n";
import { ApiProvider } from "../context";
import {
  json,
  apiError,
  renderSettings,
  type Call,
  ts,
} from "../settings/testing";
import { QQSections } from "./QQSections";
import { QQBindingDialog, QQConfirmAction } from "./QQBindingDialogs";
import { qqOfficialURL } from "./qq-model";
const base = "/v1/organizations/o1/integrations/qq";
const bot: QQBot = {
  id: "bot",
  connection_id: "connection",
  agent_id: "a1",
  space_id: "s1",
  name: "QQ fixture",
  app_id: "appid",
  state: "ACTIVE",
  status_reason: "",
  version: 3,
  generation: 1,
  credential_version: 1,
  url_verified: true,
  can_manage: true,
  created_at: ts,
};
const binding: QQBindingSession = {
  id: "proof",
  bot_id: bot.id,
  purpose: "PRIVATE_CODE",
  created_by: "u1",
  owner_user_id: "u1",
  status: "OBSERVED",
  version: 2,
  expires_at: "2099-01-01T00:00:00Z",
  receipt_id: "receipt",
  group_epoch_id: "",
  binding_id: "",
  chat_id: "qq-subject",
  subject_id: "qq-subject",
  member_role: "",
};
function fixture(
  overrides: Partial<QQConfiguration> = {},
  extra?: (call: Call) => Response | undefined,
) {
  const config: QQConfiguration = {
    configured: true,
    qr_configured: true,
    bots: [{ ...bot }],
    bindings: [],
    binding_sessions: [],
    ...overrides,
  };
  const detail: QQBotDetail = {
    bot: config.bots[0] ?? bot,
    groups: [],
    bindings: [],
    candidates: [],
    qr_sessions: [],
    replies: [],
  };
  const calls: Call[] = [];
  const transport = async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), "https://fixture.invalid");
    const call: Call = {
      path: url.pathname.replace(/^\/api/, ""),
      query: url.searchParams,
      method: init?.method ?? "GET",
      body: init?.body ? JSON.parse(String(init.body)) : undefined,
      headers: new Headers(init?.headers),
    };
    calls.push(call);
    const result = extra?.(call);
    if (result) return result;
    if (call.path === `${base}/configuration`) return json(200, config);
    if (call.path === `${base}/bots/bot`) return json(200, detail);
    if (call.path === `${base}/binding-sessions/proof`)
      return json(200, binding);
    if (call.path === "/v1/me") return json(200, { id: "u1" });
    if (call.path === "/v1/me/organizations")
      return json(200, { organizations: [{ id: "o1", role: "OWNER" }] });
    if (call.path.endsWith("/agent-directory"))
      return json(200, {
        entries: [
          {
            id: "a1",
            name: "Owned Agent",
            type: "agent",
            mine: true,
            archived_at: null,
          },
        ],
      });
    return apiError(404, "NOT_FOUND");
  };
  return { calls, config, detail, transport };
}
function mount(
  f: ReturnType<typeof fixture>,
  element = <QQSections orgId="o1" />,
) {
  const cache = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  render(
    <I18nProvider initialLocale="zh-CN">
      <QueryClientProvider client={cache}>
        <ApiProvider api={createApi(new HttpClient("/api", f.transport))}>
          {element}
        </ApiProvider>
      </QueryClientProvider>
    </I18nProvider>,
  );
  return cache;
}
describe("QQ integration", () => {
  it("account settings use QQ proof controls without app management or generic OAuth", async () => {
    const f = fixture();
    const calls = renderSettings("/o/o1/settings/account-binding", (c) => {
      if (c.path === "/v1/organizations/o1/me/account-bindings")
        return json(200, {
          providers: [
            {
              id: "qq",
              name: "QQ",
              availability: "available",
              method: "link",
              organization_ready: true,
              binding: null,
            },
          ],
          jira_sites: [],
          email: "fixture@example.invalid",
        });
      if (c.path === `${base}/configuration`) return json(200, f.config);
      if (c.path === `${base}/bots/bot/binding-sessions` && c.method === "POST")
        return json(201, binding);
      if (c.path === `${base}/binding-sessions/proof`)
        return json(200, binding);
      return undefined;
    });
    await userEvent.click(
      await screen.findByRole(
        "button",
        { name: "我的 QQ 身份" },
        { timeout: 5000 },
      ),
    );
    const dialog = screen.getByRole("dialog", { name: "我的 QQ 身份" });
    await userEvent.click(
      await within(dialog).findByRole("button", {
        name: "验证我的 QQ 单聊身份",
      }),
    );
    await waitFor(() =>
      expect(
        calls.find(
          (c) =>
            c.method === "POST" &&
            c.path === `${base}/bots/bot/binding-sessions`,
        )?.body,
      ).toMatchObject({ purpose: "PRIVATE_CODE", version: bot.version }),
    );
    expect(
      screen.queryByRole("button", { name: "管理 QQ 连接" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "连接 Agent 到 QQ" }),
    ).not.toBeInTheDocument();
    expect(
      calls
        .filter((c) => c.method === "POST")
        .every((c) => c.path.includes("/integrations/qq/")),
    ).toBe(true);
  });

  it("Agent entry filters other bots and creates only its preselected target", async () => {
    const f = fixture(
      {
        bots: [bot, { ...bot, id: "other", agent_id: "a2", name: "Other QQ" }],
      },
      (c) => {
        if (c.path.endsWith("/agent-directory"))
          return json(200, {
            entries: [
              {
                id: "a1",
                name: "Owned Agent",
                type: "agent",
                mine: true,
                archived_at: null,
              },
              {
                id: "a2",
                name: "Other Agent",
                type: "agent",
                mine: true,
                archived_at: null,
              },
            ],
          });
        if (c.path === `${base}/bots` && c.method === "POST")
          return json(201, bot);
        return undefined;
      },
    );
    mount(f, <QQSections orgId="o1" agentId="a1" />);
    expect(await screen.findByText("QQ fixture")).toBeInTheDocument();
    expect(screen.queryByText("Other QQ")).not.toBeInTheDocument();
    await userEvent.click(
      screen.getByRole("button", { name: "连接 Agent 到 QQ" }),
    );
    const dialog = screen.getByRole("dialog", { name: "连接 Agent 到 QQ" });
    await userEvent.type(within(dialog).getByLabelText("QQ 连接名称"), "My QQ");
    await userEvent.click(within(dialog).getByRole("button", { name: "创建" }));
    await waitFor(() =>
      expect(
        f.calls.find((c) => c.path === `${base}/bots` && c.method === "POST")
          ?.body,
      ).toMatchObject({ agent_id: "a1" }),
    );
    expect(screen.queryByText("Other Agent")).not.toBeInTheDocument();
  });

  it("blocks QR creation until verified deployment is configured", async () => {
    const f = fixture({ qr_configured: false });
    mount(f);
    expect(
      await screen.findByRole("button", { name: "连接 Agent 到 QQ" }),
    ).toBeDisabled();
    expect(f.calls.filter((c) => c.method !== "GET")).toHaveLength(0);
  });
  it("ordinary users see binding but no manager controls", async () => {
    const f = fixture({ bots: [{ ...bot, can_manage: false }] });
    mount(f);
    expect(
      await screen.findByRole("button", { name: "验证我的 QQ 单聊身份" }),
    ).toBeEnabled();
    expect(
      screen.queryByRole("button", { name: "管理 QQ 连接" }),
    ).not.toBeInTheDocument();
  });
  it("reveals callback only by explicit POST and keeps it out of query cache", async () => {
    const f = fixture({}, (c) =>
      c.path.endsWith("/webhook")
        ? json(200, {
            url: "https://fixture.invalid/api/v1/hooks/qq/OWNED_SECRET",
          })
        : undefined,
    );
    const cache = mount(f);
    await userEvent.click(
      await screen.findByRole("button", { name: "管理 QQ 连接" }),
    );
    await userEvent.click(
      await screen.findByRole("button", { name: "显示回调地址" }),
    );
    expect(f.calls.some((c) => c.path.endsWith("/webhook"))).toBe(false);
    const dialog = screen.getByRole("dialog", { name: "配置 QQ 回调" });
    await userEvent.click(
      within(dialog).getByRole("button", { name: "显示回调地址" }),
    );
    expect(await screen.findByText(/OWNED_SECRET/)).toBeInTheDocument();
    expect(
      JSON.stringify(
        cache
          .getQueryCache()
          .getAll()
          .map((q) => q.state.data),
      ),
    ).not.toContain("OWNED_SECRET");
    expect(f.calls.find((c) => c.path.endsWith("/webhook"))).toMatchObject({
      method: "POST",
      body: { version: 3 },
    });
    await userEvent.click(
      within(dialog).getAllByRole("button", { name: "关闭" })[0]!,
    );
    expect(screen.queryByText(/OWNED_SECRET/)).not.toBeInTheDocument();
  });
  it("requires explicit group-wide consent and submits the observed proof version", async () => {
    const group = {
      ...binding,
      purpose: "GROUP_DISCOVERY" as const,
      member_role: "admin",
      chat_id: "group-one",
    };
    const f = fixture({}, (c) =>
      c.path === `${base}/binding-sessions/proof`
        ? json(200, group)
        : c.path.endsWith("/confirm")
          ? json(200, { ...group, status: "CONFIRMED", version: 3 })
          : undefined,
    );
    mount(
      f,
      <QQBindingDialog
        orgId="o1"
        initial={group}
        refresh={() => {}}
        onClose={() => {}}
      />,
    );
    const confirm = await screen.findByRole("button", { name: "确认授权此群" });
    expect(confirm).toBeDisabled();
    await userEvent.click(
      screen.getByRole("checkbox", {
        name: "我同意 Agent 回复对该 QQ 群内所有成员可见",
      }),
    );
    await waitFor(() => expect(confirm).toBeEnabled());
    await userEvent.click(confirm);
    await waitFor(() =>
      expect(f.calls.find((c) => c.path.endsWith("/confirm"))).toMatchObject({
        body: { version: 2, public_reply_consent: true },
      }),
    );
  });
  it("another member cannot accept a mapped identity invitation", async () => {
    const invitation = {
      ...binding,
      purpose: "GROUP_ASSIGN" as const,
      owner_user_id: "u2",
      group_epoch_id: "group",
    };
    const f = fixture({}, (c) =>
      c.path === `${base}/binding-sessions/proof`
        ? json(200, invitation)
        : undefined,
    );
    mount(
      f,
      <QQBindingDialog
        orgId="o1"
        initial={invitation}
        refresh={() => {}}
        onClose={() => {}}
      />,
    );
    expect(
      await screen.findByRole("button", { name: "确认绑定本人身份" }),
    ).toBeDisabled();
  });
  it("renders only fixed official QR and never opens it automatically", async () => {
    const v: QQQRSession = {
      id: "qr",
      bot_id: "bot",
      status: "WAITING_SCAN",
      status_reason: "",
      version: 2,
      generation: 1,
      qr_url: "https://q.qq.com/qqbot/openclaw/connect.html?task_id=fixture",
      has_candidate: false,
      expires_at: "2099-01-01T00:00:00Z",
    };
    const f = fixture({}, (c) =>
      c.path === `${base}/qr/qr` ? json(200, v) : undefined,
    );
    f.detail.qr_sessions = [v];
    mount(f);
    await userEvent.click(
      await screen.findByRole("button", { name: "管理 QQ 连接" }),
    );
    await userEvent.click(
      await screen.findByRole("button", { name: "QQ 官方扫码连接" }),
    );
    expect(
      await screen.findByRole("img", { name: "QQ 官方扫码连接" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: "打开腾讯官方连接页面" }),
    ).toHaveAttribute("href", v.qr_url);
    expect(
      qqOfficialURL(
        "https://q.qq.com.attacker.test/qqbot/openclaw/connect.html",
      ),
    ).toBeNull();
    expect(f.calls.filter((c) => c.method !== "GET")).toHaveLength(0);
  });
  it("UNKNOWN close requires external inspection and blocks a stale request from replay", async () => {
    let submissions = 0;
    const f = fixture();
    mount(
      f,
      <QQConfirmAction
        label="关闭未知记录"
        description="结果未知"
        remote
        refresh={() => {}}
        execute={async () => {
          submissions++;
          throw Object.assign(new Error("stale"), { status: 409 });
        }}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: "关闭未知记录" }));
    const confirm = screen.getByRole("button", { name: "确认" });
    expect(confirm).toBeDisabled();
    await userEvent.click(
      screen.getByRole("checkbox", {
        name: "我已检查腾讯开放平台，了解本地操作不会删除远端应用",
      }),
    );
    await userEvent.click(confirm);
    await waitFor(() => expect(confirm).toBeDisabled());
    expect(submissions).toBe(1);
  });
});
