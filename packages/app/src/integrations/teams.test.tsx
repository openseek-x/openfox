import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";
import {
  createApi,
  HttpClient,
  type IntegrationDetail,
  type TeamsBot,
} from "@openfox/api";
import { I18nProvider } from "@openfox/i18n";
import { ApiProvider } from "../context";
import { TeamsSections, TeamsScopesDialog } from "./TeamsSections";
import { TeamsBindingDialog } from "./TeamsPersonal";
import { TeamsConnectDialog } from "./TeamsDialogs";
import { json, apiError, type Call } from "../settings/testing";
const base = "/v1/organizations/o1/integrations/teams";
const bot: TeamsBot = {
  id: "b1",
  connection_id: "c1",
  kind: "AGENT",
  agent: { id: "a1", name: "Agent one" },
  name: "Teams fixture",
  tenant_id: "11111111-1111-4111-8111-111111111111",
  app_id: "22222222-2222-4222-8222-222222222222",
  version: 4,
  state: "ACTIVE",
  status_reason: "",
  endpoint: "https://example.test/api/v1/hooks/teams/b1/activities",
  can_manage: true,
  manifest: {
    version: "1.0.0",
    developer_name: "Fixture",
    website_url: "https://example.test",
    privacy_url: "https://example.test/privacy",
    terms_url: "https://example.test/terms",
  },
};
const target = {
  bot_id: bot.id,
  name: bot.name,
  kind: bot.kind,
  agent: bot.agent,
  can_bind: true,
};
const detail: IntegrationDetail = {
  provider: {
    id: "teams",
    name: "Microsoft Teams",
    category: "collaboration",
    availability: "available",
    template: "C2",
    configured: true,
    bindable: true,
    capabilities: [],
    summary: { connections: 0, status: "none" },
  },
  can_manage: true,
  connections: [],
  personal_connections: [],
  sync_links: [],
};
function setup(
  element: React.ReactNode,
  route?: (c: Call) => Response | Promise<Response> | undefined,
) {
  const calls: Call[] = [],
    cache = new QueryClient({
      defaultOptions: { queries: { retry: false, gcTime: 0 } },
    });
  const transport = async (input: RequestInfo | URL, init?: RequestInit) => {
    const u = new URL(String(input), "http://localhost"),
      c: Call = {
        path: u.pathname.replace(/^\/api/, ""),
        method: init?.method ?? "GET",
        body:
          typeof init?.body === "string" ? JSON.parse(init.body) : undefined,
        query: u.searchParams,
        headers: new Headers(init?.headers),
      };
    calls.push(c);
    const custom = route?.(c);
    if (custom) return custom;
    if (c.path === "/v1/me") return json(200, { id: "u1" });
    if (c.path === "/v1/me/organizations")
      return json(200, { organizations: [{ id: "o1", role: "OWNER" }] });
    if (c.path.endsWith("/agent-directory"))
      return json(200, {
        entries: [
          {
            id: "a1",
            name: "Agent one",
            type: "agent",
            mine: true,
            archived_at: null,
          },
        ],
      });
    if (c.path.endsWith("/spaces"))
      return json(200, { spaces: [{ id: "s1", name: "Space one" }] });
    if (c.path.endsWith("/devices")) return json(200, { devices: [] });
    if (c.path === base + "/bots") return json(200, { bots: [] });
    if (c.path === base + "/personal-targets") return json(200, [target]);
    if (c.path === base + "/personal-bindings") return json(200, []);
    if (c.path.endsWith("/scopes")) return json(200, { scopes: [] });
    if (c.path.endsWith("/deliveries")) return json(200, []);
    return apiError(404, "NOT_FOUND");
  };
  const view = render(
    <I18nProvider initialLocale="zh-CN">
      <QueryClientProvider client={cache}>
        <ApiProvider
          api={createApi(new HttpClient("/api", transport as typeof fetch))}
        >
          {element}
        </ApiProvider>
      </QueryClientProvider>
    </I18nProvider>,
  );
  return { calls, cache, ...view };
}
describe("Teams control UI", () => {
  it("uses personal targets without requiring bot management and keeps deployment truth", async () => {
    const { calls } = setup(
      <TeamsSections
        orgId="o1"
        detail={{
          ...detail,
          can_manage: false,
          provider: { ...detail.provider, configured: false },
        }}
      />,
    );
    expect(
      await screen.findByRole("button", { name: "绑定本人" }),
    ).toBeEnabled();
    expect(
      screen.getByRole("button", { name: "连接 Teams Bot" }),
    ).toBeDisabled();
    expect(
      await screen.findByText("暂无你可管理的 Teams Bot。"),
    ).toBeInTheDocument();
    expect(calls.some((c) => c.path === base + "/personal-targets")).toBe(true);
    expect(
      screen.queryByRole("button", { name: /同步|导入|通知/ }),
    ).not.toBeInTheDocument();
  });
  it("keeps one-time challenge out of queries/storage and hides it on close", async () => {
    const code = "/openfox bind fixture-one-time-command",
      user = userEvent.setup();
    const { cache, unmount, calls } = setup(
      <TeamsBindingDialog
        orgId="o1"
        target={target}
        onClose={() => {}}
        onSaved={() => {}}
      />,
      (c) =>
        c.path.endsWith("/binding-challenges")
          ? json(200, {
              id: "ch1",
              bot_id: "b1",
              command: code,
              expires_at: new Date(Date.now() + 600000).toISOString(),
            })
          : undefined,
    );
    await user.click(
      screen.getByRole("button", { name: "生成一次性绑定指令" }),
    );
    expect(await screen.findByText(code)).toBeInTheDocument();
    expect(
      JSON.stringify(
        cache
          .getQueryCache()
          .getAll()
          .map((q) => q.state.data),
      ),
    ).not.toContain(code);
    expect(JSON.stringify(localStorage)).not.toContain(code);
    expect(JSON.stringify(sessionStorage)).not.toContain(code);
    expect(
      calls.filter((c) => c.path.endsWith("/binding-challenges")),
    ).toHaveLength(1);
    await user.click(screen.getByRole("button", { name: "隐藏指令" }));
    expect(screen.queryByText(code)).not.toBeInTheDocument();
    unmount();
  });
  it("clears visible secret before request and never caches the mutation", async () => {
    const user = userEvent.setup(),
      saved = vi.fn();
    let finish!: (r: Response) => void;
    const pending = new Promise<Response>((r) => (finish = r));
    const { cache, calls } = setup(
      <TeamsConnectDialog
        orgId="o1"
        bot={bot}
        onClose={() => {}}
        onSaved={saved}
      />,
      (c) =>
        c.method === "PUT" && c.path.endsWith("/credentials")
          ? pending
          : undefined,
    );
    await user.type(screen.getByLabelText("Client Secret"), "synthetic-secret");
    await user.click(screen.getByRole("button", { name: "核验并保存" }));
    expect(screen.getByLabelText("Client Secret")).toHaveValue("");
    expect(cache.getMutationCache().getAll()).toHaveLength(0);
    expect(calls.find((c) => c.method === "PUT")?.body).toEqual({
      version: 4,
      secret: "synthetic-secret",
    });
    finish(json(200, bot));
    await waitFor(() => expect(saved).toHaveBeenCalledOnce());
  });
  it("requires explicit public reply consent and sends current version", async () => {
    const user = userEvent.setup();
    const { calls } = setup(
      <TeamsScopesDialog orgId="o1" bot={bot} onClose={() => {}} />,
      (c) =>
        c.path.endsWith("/scopes")
          ? json(200, {
              scopes: [
                {
                  id: "scope1",
                  kind: "group",
                  conversation_id: "opaque;group/thread",
                  version: 7,
                  enabled: false,
                  public_reply_consent: false,
                  last_seen_at: "2026-11-15T00:00:00Z",
                },
              ],
            })
          : c.path.endsWith("/scopes/scope1")
            ? json(200, {})
            : undefined,
    );
    const button = await screen.findByRole("button", { name: "开启回复" });
    expect(button).toBeDisabled();
    await user.click(
      screen.getByRole("checkbox", { name: "我同意在此群或频道公开回复" }),
    );
    await user.click(button);
    await waitFor(() =>
      expect(calls.find((c) => c.method === "PUT")?.body).toEqual({
        version: 7,
        enabled: true,
        public_reply_consent: true,
      }),
    );
  });
  it("shows unknown as stopped and renders untrusted plain text without resend", async () => {
    setup(<TeamsScopesDialog orgId="o1" bot={bot} onClose={() => {}} />, (c) =>
      c.path.endsWith("/deliveries")
        ? json(200, [
            {
              id: "d1",
              state: "UNKNOWN",
              reason: "delivery_unknown",
              body: '<img src="https://unsafe.invalid/x">',
              created_at: "2026-11-15T00:00:00Z",
            },
          ])
        : undefined,
    );
    expect(
      await screen.findByText(/发送结果未知，已停止自动重试/),
    ).toBeInTheDocument();
    expect(
      screen.getByText('<img src="https://unsafe.invalid/x">'),
    ).toBeInTheDocument();
    expect(screen.queryByRole("img")).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /重发|重试/ }),
    ).not.toBeInTheDocument();
  });
  it("distinguishes credentials from actual incoming activity and delivery", async () => {
    setup(<TeamsSections orgId="o1" detail={detail} />, (c) =>
      c.path === base + "/bots"
        ? json(200, {
            bots: [{ ...bot, credentials_verified_at: "2026-11-15T00:00:00Z" }],
          })
        : undefined,
    );
    expect(
      await screen.findByText(/凭据校验成功不代表应用已安装/),
    ).toBeInTheDocument();
    expect(screen.getAllByText("尚无此项连通证据")).toHaveLength(2);
    expect(screen.queryByText(/已确认回复送达：/)).not.toBeInTheDocument();
  });
});
