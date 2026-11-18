import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";
import { createApi, HttpClient, type DiscordBot } from "@openfox/api";
import { I18nProvider } from "@openfox/i18n";
import { ApiProvider } from "../context";
import { json, apiError, type Call } from "../settings/testing";
import { DiscordConnectDialog } from "./DiscordConnectDialog";
import { DiscordBindingDialog } from "./DiscordBindingDialog";
import {
  DiscordChannelsDialog,
  DiscordDeliveriesDialog,
} from "./DiscordChannelsDialog";
import { discordInstallURL } from "./discord-hooks";
const bot: DiscordBot = {
  id: "b1",
  connection_id: "c1",
  kind: "AGENT",
  agent: { id: "a1", name: "Agent" },
  remote_bot_id: "18446744073709551615",
  application_id: "18446744073709551615",
  username: "Bot",
  name: "Bot",
  state: "ACTIVE",
  gateway_state: "CONNECTED",
  message_content_enabled: true,
  last_error: "",
  version: 1,
  installation_url: "",
  gap_count: 0,
  gap_reason: "",
  can_manage: true,
};
function mount(
  element: React.ReactNode,
  extra: (c: Call) => Response | undefined,
) {
  const calls: Call[] = [];
  const cache = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
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
    const r = extra(c);
    if (r) return r;
    if (c.path === "/v1/me") return json(200, { id: "u1" });
    if (c.path === "/v1/me/organizations")
      return json(200, { organizations: [{ id: "o1", role: "OWNER" }] });
    if (c.path.endsWith("/agent-directory"))
      return json(200, {
        entries: [
          {
            id: "a1",
            name: "Agent",
            type: "agent",
            mine: true,
            space_id: "s1",
            archived_at: null,
          },
        ],
      });
    if (c.path.endsWith("/spaces")) return json(200, { spaces: [] });
    if (c.path.endsWith("/devices")) return json(200, { devices: [] });
    return apiError(404, "NOT_FOUND");
  };
  const view = render(
    <I18nProvider initialLocale="en-US">
      <QueryClientProvider client={cache}>
        <ApiProvider api={createApi(new HttpClient("/api", transport))}>
          {element}
        </ApiProvider>
      </QueryClientProvider>
    </I18nProvider>,
  );
  return { ...view, cache, calls };
}
describe("Discord control UI", () => {
  it("requires explicit verified identity confirmation and does not cache the Token", async () => {
    const user = userEvent.setup();
    const m = mount(
      <DiscordConnectDialog
        orgId="o1"
        target={{ kind: "AGENT", agentId: "a1" }}
        refresh={() => {}}
        onClose={() => {}}
      />,
      (c) =>
        c.path.endsWith("/preparations")
          ? json(200, {
              id: "p1",
              kind: "AGENT",
              remote_bot_id: bot.remote_bot_id,
              application_id: bot.application_id,
              username: "Verified Bot",
              bot_name: "Verified Bot",
              message_content_enabled: true,
              status: "READY",
              version: 1,
              expires_at: "2099-01-01T00:00:00Z",
            })
          : c.path.endsWith("/confirm")
            ? json(200, bot)
            : undefined,
    );
    await user.type(screen.getByLabelText("Bot Token"), "synthetic-ui-token");
    await user.click(
      screen.getByRole("button", { name: "Verify credentials" }),
    );
    const confirm = await screen.findByRole("button", {
      name: "Confirm connection",
    });
    expect(confirm).toBeDisabled();
    expect(
      screen.queryByDisplayValue("synthetic-ui-token"),
    ).not.toBeInTheDocument();
    expect(
      JSON.stringify(
        m.cache
          .getQueryCache()
          .getAll()
          .map((q) => q.state.data),
      ),
    ).not.toContain("synthetic-ui-token");
    expect(m.cache.getMutationCache().getAll()).toHaveLength(0);
    await user.click(
      screen.getByLabelText("I confirm this is the bot to connect"),
    );
    await user.click(confirm);
    await waitFor(() =>
      expect(m.calls.some((c) => c.path.endsWith("/confirm"))).toBe(true),
    );
    const prepare = m.calls.find((c) => c.path.endsWith("/preparations"))!;
    expect((prepare.body as { operation_key: string }).operation_key).toMatch(
      /^[0-9a-f-]{36}$/,
    );
  });
  it("keeps the one-time binding code out of query cache and gates self confirmation", async () => {
    const user = userEvent.setup(),
      m = mount(
        <DiscordBindingDialog
          orgId="o1"
          botId="b1"
          refresh={() => {}}
          onClose={() => {}}
        />,
        (c) =>
          c.path.endsWith("/binding-sessions")
            ? json(200, {
                id: "s1",
                bot_id: "b1",
                status: "WAITING",
                version: 1,
                expires_at: "2099-01-01T00:00:00Z",
                code: "/bind synthetic-one-time",
              })
            : c.path.endsWith("/binding-sessions/s1")
              ? json(200, {
                  id: "s1",
                  bot_id: "b1",
                  status: "CANDIDATE",
                  version: 2,
                  expires_at: "2099-01-01T00:00:00Z",
                  candidate_user_id: "18446744073709551615",
                  candidate_name: "Controlled Person",
                })
              : undefined,
      );
    await user.click(screen.getByRole("button", { name: "Bind my identity" }));
    await screen.findByText("/bind synthetic-one-time");
    await screen.findByText(/Controlled Person/);
    expect(
      screen.getByRole("button", { name: "Confirm this is me" }),
    ).toBeDisabled();
    expect(
      JSON.stringify(
        m.cache
          .getQueryCache()
          .getAll()
          .map((q) => q.state.data),
      ),
    ).not.toContain("synthetic-one-time");
    expect(m.cache.getMutationCache().getAll()).toHaveLength(0);
  });
  it("requires both independent channel consents and sends string IDs", async () => {
    const user = userEvent.setup(),
      m = mount(
        <DiscordChannelsDialog orgId="o1" bot={bot} onClose={() => {}} />,
        (c) =>
          c.path.endsWith("/personal")
            ? json(200, [
                {
                  id: "i1",
                  bot_id: "b1",
                  remote_user_id: "6000001",
                  active: true,
                  version: 1,
                },
              ])
            : c.path.endsWith("/guilds")
              ? json(200, {
                  guilds: [
                    { id: "18446744073709551615", name: "Server", owner: true },
                  ],
                })
              : c.path.includes("/guilds/")
                ? json(200, [
                    {
                      id: "18446744073709551614",
                      guild_id: "18446744073709551615",
                      name: "Channel",
                    },
                  ])
                : c.path.endsWith("/channels")
                  ? json(200, [])
                  : undefined,
      );
    await user.click(await screen.findByLabelText("Server"));
    await user.click(await screen.findByRole("option", { name: /Server ·/ }));
    await user.click(screen.getByLabelText("Text channel"));
    await user.click(await screen.findByRole("option", { name: /Channel ·/ }));
    const enable = screen.getByRole("button", { name: "Enable channel" });
    expect(enable).toBeDisabled();
    await user.click(screen.getByLabelText(/Allow public replies/));
    expect(enable).toBeDisabled();
    await user.click(screen.getByLabelText(/Allow up to 20 messages/));
    await user.click(enable);
    await waitFor(() =>
      expect(m.calls.find((c) => c.method === "POST")?.body).toEqual({
        guild_id: "18446744073709551615",
        channel_id: "18446744073709551614",
        public_reply_consent: true,
        history_consent: true,
      }),
    );
  });
  it("shows UNKNOWN as read-only and does not offer resend", async () => {
    mount(
      <DiscordDeliveriesDialog orgId="o1" botId="b1" onClose={() => {}} />,
      (c) =>
        c.path.endsWith("/deliveries")
          ? json(200, {
              items: [
                {
                  id: "d1",
                  channel_id: "7000002",
                  reply_to_message_id: "9000001",
                  state: "UNKNOWN",
                  reason: "delivery_unknown",
                  created_at: "2026-11-15T00:00:00Z",
                },
              ],
              next: "",
            })
          : undefined,
    );
    await screen.findByText(/Delivery unknown/);
    expect(
      screen.queryByRole("button", { name: /resend|retry/i }),
    ).not.toBeInTheDocument();
  });
  it("only accepts the official constrained installation link", () => {
    const good =
      "https://discord.com/oauth2/authorize?client_id=123&scope=bot&permissions=68608&integration_type=0";
    expect(discordInstallURL(good, "123")).toBe(good);
    expect(
      discordInstallURL(good.replace("discord.com", "evil.test"), "123"),
    ).toBeUndefined();
    expect(discordInstallURL(good, "456")).toBeUndefined();
  });
});
