import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";
import {
  createApi,
  HttpClient,
  type IntegrationDetail,
  type TelegramBot,
} from "@openfox/api";
import { I18nProvider } from "@openfox/i18n";
import { ApiProvider } from "../context";
import { TelegramSections } from "./TelegramSections";
import { TelegramPersonalSection } from "./TelegramPersonal";
import { TelegramConnectDialog } from "./TelegramDialogs";
import {
  TelegramGroupsDialog,
  TelegramDeliveriesDialog,
} from "./TelegramGroups";
import { telegramBindingURL } from "./telegram-model";
import { json, apiError, type Call } from "../settings/testing";

const base = "/v1/organizations/o1/integrations/telegram",
  future = "2099-11-15T00:00:00Z";
const bot: TelegramBot = {
  id: "b1",
  connection_id: "c1",
  kind: "AGENT",
  agent: { id: "a1", name: "Agent one" },
  remote_bot_id: "4503599627370495",
  username: "fixture_bot",
  name: "Fixture bot",
  state: "ACTIVE",
  control_status: "DONE",
  last_error: "",
  version: 4,
  can_manage: true,
  unsupported_topic_count: 0,
};
const target = {
  bot_id: bot.id,
  name: bot.name,
  username: bot.username,
  kind: bot.kind,
  agent: bot.agent,
  space: { id: "s1", name: "Space one" },
  can_bind: true,
};
const detail: IntegrationDetail = {
  provider: {
    id: "telegram",
    name: "Telegram",
    category: "collaboration",
    availability: "available",
    template: "C4",
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
  const calls: Call[] = [];
  const cache = new QueryClient({
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
    if (c.path === "/v1/me")
      return json(200, { id: "u1", email: "fixture@example.invalid" });
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
    if (c.path === base + "/personal") return json(200, []);
    if (
      c.path.endsWith("/groups") ||
      c.path.endsWith("/group-discoveries") ||
      c.path.endsWith("/deliveries")
    )
      return json(200, []);
    return apiError(404, "NOT_FOUND");
  };
  const api = createApi(new HttpClient("/api", transport as typeof fetch));
  const view = render(
    <I18nProvider initialLocale="zh-CN">
      <QueryClientProvider client={cache}>
        <ApiProvider api={api}>{element}</ApiProvider>
      </QueryClientProvider>
    </I18nProvider>,
  );
  return { calls, cache, ...view };
}
describe("Telegram control UI", () => {
  it("uses the dedicated personal target list for a member without managed Bots", async () => {
    const { calls } = setup(
      <TelegramSections orgId="o1" detail={{ ...detail, can_manage: false }} />,
    );
    expect(await screen.findByText("暂无你可管理的 Bot。")).toBeInTheDocument();
    expect(
      await screen.findByRole("button", { name: "绑定本人" }),
    ).toBeEnabled();
    expect(calls.some((c) => c.path === base + "/personal-targets")).toBe(true);
    expect(screen.queryByText(/同步任务/)).not.toBeInTheDocument();
  });
  it("shows missing deployment configuration without enabling connect", async () => {
    setup(
      <TelegramSections
        orgId="o1"
        detail={{
          ...detail,
          provider: { ...detail.provider, configured: false },
        }}
      />,
    );
    expect(screen.getByRole("button", { name: "连接 Bot" })).toBeDisabled();
    expect(screen.getByText(/服务尚未准备好安全保存凭据/)).toBeInTheDocument();
  });
  it("prepares a secret Token and requires a separate checked confirmation", async () => {
    let completed = false;
    const user = userEvent.setup();
    const { calls, cache } = setup(
      <TelegramConnectDialog
        orgId="o1"
        bots={[]}
        onClose={() => {}}
        onSaved={() => {
          completed = true;
        }}
      />,
      (c) => {
        if (c.path === base + "/preparations")
          return json(200, {
            id: "p1",
            kind: "AGENT",
            agent_id: "a1",
            remote_bot_id: bot.remote_bot_id,
            username: bot.username,
            bot_name: bot.name,
            status: "PENDING_CONFIRM",
            version: 1,
            expires_at: future,
          });
        if (c.path === base + "/preparations/p1/confirm")
          return json(200, { ...bot, state: "SETTING" });
        return undefined;
      },
    );
    await user.click(await screen.findByRole("button", { name: "Agent" }));
    await user.click(await screen.findByRole("option", { name: "Agent one" }));
    await user.type(
      screen.getByLabelText("BotFather Token"),
      "synthetic-token",
    );
    await user.click(screen.getByRole("button", { name: "核验 Bot" }));
    expect(await screen.findByText(bot.remote_bot_id)).toBeInTheDocument();
    expect(screen.queryByLabelText("BotFather Token")).not.toBeInTheDocument();
    const confirm = screen.getByRole("button", { name: "确认并配置 Webhook" });
    expect(confirm).toBeDisabled();
    expect(calls.filter((c) => c.path.endsWith("/confirm"))).toHaveLength(0);
    await user.click(screen.getByLabelText("我已核对 Bot 和固定目标"));
    await user.click(confirm);
    await waitFor(() => expect(completed).toBe(true));
    expect(calls.find((c) => c.path.endsWith("/confirm"))?.body).toEqual({
      version: 1,
      remote_bot_id: bot.remote_bot_id,
    });
    expect(cache.getMutationCache().getAll()).toHaveLength(0);
    expect(
      JSON.stringify(
        cache
          .getQueryCache()
          .getAll()
          .map((q) => q.state.data),
      ),
    ).not.toContain("synthetic-token");
  });
  it("requires explicit recovery consent and submits the same fixed target", async () => {
    const user = userEvent.setup();
    const { calls } = setup(
      <TelegramConnectDialog
        orgId="o1"
        bots={[]}
        bot={{ ...bot, control_status: "UNKNOWN" }}
        recover
        onClose={() => {}}
        onSaved={() => {}}
      />,
      (c) =>
        c.path === base + "/preparations"
          ? apiError(409, "TELEGRAM_WEBHOOK_CONFLICT")
          : undefined,
    );
    await user.type(
      screen.getByLabelText("BotFather Token"),
      "new-synthetic-token",
    );
    expect(screen.getByRole("button", { name: "核验 Bot" })).toBeDisabled();
    await user.click(screen.getByRole("checkbox"));
    await user.click(screen.getByRole("button", { name: "核验 Bot" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "此 Bot 已配置其他 Webhook",
    );
    expect(
      calls.find((c) => c.path === base + "/preparations")?.body,
    ).toMatchObject({
      bot_id: "b1",
      agent_id: "a1",
      kind: "AGENT",
      version: 4,
      recover_webhook: true,
    });
    expect(screen.getByLabelText("BotFather Token")).toHaveValue("");
  });
  it("keeps the private start link out of cache and confirms only a received candidate", async () => {
    const user = userEvent.setup();
    const url = `https://t.me/fixture_bot?start=bind_${"a".repeat(43)}`;
    const { calls, cache } = setup(
      <TelegramPersonalSection orgId="o1" />,
      (c) => {
        if (c.method === "POST" && c.path.endsWith("/binding-sessions"))
          return json(200, {
            id: "bs1",
            bot_id: "b1",
            status: "WAITING",
            version: 1,
            expires_at: future,
            start_url: url,
          });
        if (c.path.endsWith("/binding-sessions/bs1"))
          return json(200, {
            id: "bs1",
            bot_id: "b1",
            status: "CANDIDATE",
            version: 2,
            expires_at: future,
            candidate_user_id: "4503599627370494",
            candidate_name: "Fixture person",
          });
        if (c.path.endsWith("/binding-sessions/bs1/confirm"))
          return json(200, {
            id: "i1",
            bot_id: "b1",
            remote_user_id: "4503599627370494",
            display_name: "Fixture person",
            active: true,
            version: 1,
            confirmed_at: future,
          });
        return undefined;
      },
    );
    await user.click(await screen.findByRole("button", { name: "绑定本人" }));
    const dialog = screen.getByRole("dialog");
    await user.click(
      within(dialog).getByRole("button", { name: "生成一次性绑定入口" }),
    );
    expect(
      await within(dialog).findByRole("link", {
        name: "打开 Telegram 完成本人候选",
      }),
    ).toHaveAttribute("href", url);
    expect(
      within(dialog).queryByRole("button", { name: "确认" }),
    ).not.toBeInTheDocument();
    expect(cache.getMutationCache().getAll()).toHaveLength(0);
    expect(
      JSON.stringify(
        cache
          .getQueryCache()
          .getAll()
          .map((q) => q.state.data),
      ),
    ).not.toContain("bind_");
    await user.click(within(dialog).getByRole("button", { name: "刷新状态" }));
    expect(
      await within(dialog).findByText("4503599627370494"),
    ).toBeInTheDocument();
    expect(within(dialog).queryByRole("link")).not.toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "确认" })).toBeDisabled();
    await user.click(within(dialog).getByLabelText("确认这是我本人"));
    await user.click(within(dialog).getByRole("button", { name: "确认" }));
    await waitFor(() =>
      expect(
        calls.find((c) => c.path.endsWith("/binding-sessions/bs1/confirm"))
          ?.body,
      ).toEqual({ version: 2 }),
    );
  });
  it("requires group-wide consent, with background opt-in only for task assistants", async () => {
    const user = userEvent.setup();
    const { calls } = setup(
      <TelegramGroupsDialog
        orgId="o1"
        bot={{ ...bot, kind: "TASK_ASSISTANT" }}
        onClose={() => {}}
      />,
      (c) => {
        if (c.path.endsWith("/group-discoveries"))
          return json(200, [
            {
              id: "d1",
              bot_id: "b1",
              chat_id: "-4503599627370495",
              title: "A group",
              version: 1,
              expires_at: future,
            },
          ]);
        if (c.method === "POST" && c.path.endsWith("/groups"))
          return json(200, {
            id: "g1",
            bot_id: "b1",
            chat_id: "-4503599627370495",
            title: "A group",
            enabled: true,
            background_enabled: false,
            version: 1,
            created_at: future,
          });
        return undefined;
      },
    );
    await user.click(await screen.findByRole("radio"));
    expect(screen.getByRole("button", { name: "开启群回复" })).toBeDisabled();
    expect(
      screen.getByLabelText("接收此任务助手群的普通文字作为背景"),
    ).not.toBeChecked();
    await user.click(
      screen.getByLabelText("我接受此群的所有成员可见 Bot 的回复"),
    );
    await user.click(screen.getByRole("button", { name: "开启群回复" }));
    await waitFor(() =>
      expect(
        calls.find((c) => c.method === "POST" && c.path.endsWith("/groups"))
          ?.body,
      ).toEqual({
        discovery_id: "d1",
        connection_version: 4,
        expected_version: 0,
        enabled: true,
        public_reply_consent: true,
        background_enabled: false,
      }),
    );
  });
  it("does not offer Agent group background or arbitrary chat ID entry", async () => {
    const user = userEvent.setup();
    setup(
      <TelegramGroupsDialog orgId="o1" bot={bot} onClose={() => {}} />,
      (c) =>
        c.path.endsWith("/group-discoveries")
          ? json(200, [
              {
                id: "d1",
                bot_id: "b1",
                chat_id: "-42",
                title: "Group",
                version: 1,
                expires_at: future,
              },
            ])
          : undefined,
    );
    await user.click(await screen.findByRole("radio"));
    expect(
      screen.queryByText("接收此任务助手群的普通文字作为背景"),
    ).not.toBeInTheDocument();
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
  });
  it("never offers resend for an unknown delivery", async () => {
    setup(
      <TelegramDeliveriesDialog orgId="o1" bot={bot} onClose={() => {}} />,
      (c) =>
        c.path.endsWith("/deliveries")
          ? json(200, [
              {
                id: "job1",
                chat_id: "-42",
                reply_to_message_id: "9",
                state: "FAILED",
                last_error: "delivery_unknown",
                created_at: future,
                can_retry: false,
              },
            ])
          : undefined,
    );
    expect(
      await screen.findByText(/发送结果未知，可能已到达 Telegram/),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /重发|重试/ }),
    ).not.toBeInTheDocument();
  });
  it("clears a one-time link when its dialog is closed", async () => {
    const user = userEvent.setup();
    setup(<TelegramPersonalSection orgId="o1" />, (c) =>
      c.method === "POST" && c.path.endsWith("/binding-sessions")
        ? json(200, {
            id: "bs1",
            bot_id: "b1",
            status: "WAITING",
            version: 1,
            expires_at: future,
            start_url: `https://t.me/fixture_bot?start=bind_${"a".repeat(43)}`,
          })
        : undefined,
    );
    await user.click(await screen.findByRole("button", { name: "绑定本人" }));
    await user.click(
      screen.getByRole("button", { name: "生成一次性绑定入口" }),
    );
    expect(
      await screen.findByRole("link", { name: "打开 Telegram 完成本人候选" }),
    ).toBeInTheDocument();
    await user.click(
      within(screen.getByRole("dialog"))
        .getAllByRole("button", {
          name: "关闭",
        })
        .at(-1)!,
    );
    expect(
      screen.queryByRole("link", { name: "打开 Telegram 完成本人候选" }),
    ).not.toBeInTheDocument();
  });
});
describe("Telegram secret deep-link validation", () => {
  it("accepts only the issued exact host, Bot username and one secret parameter", () => {
    const good = `https://t.me/fixture_bot?start=bind_${"a".repeat(43)}`;
    expect(telegramBindingURL(good, "fixture_bot")).toBe(good);
    for (const bad of [
      good.replace("https:", "http:"),
      good.replace("t.me", "t.me.evil.invalid"),
      good.replace("fixture_bot", "other_bot"),
      good + "&x=1",
      good + "#fragment",
      good.replace("t.me", "user:pass@t.me"),
    ])
      expect(telegramBindingURL(bad, "fixture_bot")).toBeNull();
  });
});
