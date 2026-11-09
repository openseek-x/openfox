import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";
import { createApi, HttpClient } from "@openfox/api";
import { I18nProvider } from "@openfox/i18n";
import { ToastProvider } from "@openfox/ui";
import { ApiProvider } from "../../context";
import { entry } from "../fixtures";
import { json } from "../../settings/testing";
import { AgentImDialog } from "./AgentImDialog";

function mount({
  mine = true,
  canBind = true,
  connected = true,
  bound = false,
} = {}) {
  const calls: { path: string; method: string; body: unknown }[] = [];
  const transport = vi.fn(
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input);
      const method = init?.method ?? "GET";
      const body =
        typeof init?.body === "string" ? JSON.parse(init.body) : null;
      calls.push({ path, method, body });
      if (path.endsWith("/me/organizations"))
        return json(200, { organizations: [{ id: "o1", role: "MEMBER" }] });
      if (path.endsWith("/agent-directory"))
        return json(200, {
          entries: [
            entry("a1", { name: "Target", mine }),
            entry("a2", { name: "Other", mine: true }),
          ],
          archived_count: 0,
          truncated: false,
        });
      if (path.endsWith("/bots"))
        return method === "POST" && path.endsWith("/wecom/bots")
          ? json(201, {
              id: "b1",
              kind: "AGENT",
              agent: { id: "a1", name: "Target" },
              status: "CONNECTING",
            })
          : json(200, {
          app_connected: connected,
          assistants: [],
          can_bind_agent: canBind,
          builtin_prompt: "",
          agents: bound
            ? [
                {
                  id: "b1",
                  agent: { id: "a1", name: "Target bot" },
                  status: "CONNECTED",
                  can_manage: false,
                },
                {
                  id: "b2",
                  agent: { id: "a2", name: "Other bot" },
                  status: "CONNECTED",
                  can_manage: false,
                },
              ]
            : [],
            });
      if (path.endsWith("/wechat/bindings"))
        return json(200, {
          configured: true,
          can_bind: canBind,
          max_bindings: 20,
          bindings: [],
        });
      if (path.includes("/registrations"))
        return json(method === "POST" ? 201 : 200, {
          id: "r1",
          purpose: "agent",
          status: path.endsWith("/cancel") ? "CANCELLED" : "PENDING",
          verification_url: "https://example.test/verify",
          expires_at: new Date(Date.now() + 600_000).toISOString(),
          poll_interval: 60,
          bot_id: null,
          error_code: "",
        });
      if (path.includes("/agent-bindings/qr"))
        return json(method === "POST" ? 201 : 200, {
          id: "q1",
          agent_id: "a1",
          status: path.endsWith("/cancel") ? "CANCELLED" : "PENDING",
          qr_content: "https://example.test/qr",
          expires_at: new Date(Date.now() + 600_000).toISOString(),
          error_code: "",
          binding_id: null,
        });
      return json(404, { error: { code: "NOT_FOUND", message: "not found" } });
    },
  );
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  render(
    <MemoryRouter>
      <I18nProvider initialLocale="zh-CN">
        <QueryClientProvider client={queryClient}>
          <ApiProvider api={createApi(new HttpClient("/api", transport))}>
            <ToastProvider>
              <AgentImDialog
                orgId="o1"
                agentId="a1"
                name="Target"
                onClose={vi.fn()}
              />
            </ToastProvider>
          </ApiProvider>
        </QueryClientProvider>
      </I18nProvider>
    </MemoryRouter>,
  );
  return calls;
}

describe("Agent IM integration entry", () => {
  it("connects a WeCom bot with BotID and Secret", async () => {
    const user = userEvent.setup();
    const calls = mount();
    await user.click(screen.getByRole("tab", { name: "企业微信" }));
    await user.click(
      await screen.findByRole("button", { name: "创建企业微信智能体" }),
    );
    const dialog = screen.getByRole("dialog", {
      name: "创建企业微信智能体",
    });
    await user.click(within(dialog).getByRole("button", { name: "下一步" }));
    await user.type(within(dialog).getByRole("textbox", { name: "BotID" }), "bot-1");
    await user.type(within(dialog).getByLabelText("Secret"), "secret-value");
    await user.click(within(dialog).getByRole("button", { name: "连接机器人" }));
    await waitFor(() =>
      expect(
        calls.find(
          (call) =>
            call.path.endsWith("/wecom/bots") && call.method === "POST",
        )?.body,
      ).toEqual({
        purpose: "agent",
        agent_id: "a1",
        bot_id: "bot-1",
        secret: "secret-value",
      }),
    );
    expect(calls.some((call) => call.path.includes("/registrations"))).toBe(false);
  });

  it.each(["feishu", "lark"])(
    "preselects the current Agent and creates/cancels a %s registration",
    async (provider) => {
      const user = userEvent.setup();
      const calls = mount();
      const label = provider === "lark" ? "Lark" : "飞书";
      if (provider === "lark")
        await user.click(screen.getByRole("tab", { name: "Lark" }));
      await user.click(
        await screen.findByRole("button", { name: `创建${label}智能体` }),
      );
      const dialog = screen.getByRole("dialog", { name: `创建${label}智能体` });
      await waitFor(() =>
        expect(
          within(dialog).getByRole("button", { name: "下一步" }),
        ).toBeEnabled(),
      );
      expect(
        within(dialog).getByRole("button", { name: "Agent" }),
      ).toBeDisabled();
      await user.click(within(dialog).getByRole("button", { name: "下一步" }));
      await waitFor(() =>
        expect(
          calls.some(
            (call) =>
              call.path.endsWith(`/${provider}/registrations`) &&
              call.method === "POST" &&
              JSON.stringify(call.body) ===
                JSON.stringify({ purpose: "agent", agent_id: "a1" }),
          ),
        ).toBe(true),
      );
      await screen.findByRole("img", { name: `${label}授权二维码` });
      await user.keyboard("{Escape}");
      await waitFor(() =>
        expect(
          calls.some((call) =>
            call.path.endsWith(`/${provider}/registrations/r1/cancel`),
          ),
        ).toBe(true),
      );
      expect(
        screen.getByRole("dialog", { name: "添加 IM 集成" }),
      ).toBeVisible();
    },
  );

  it("does not allow binding an Agent absent from the permitted directory", async () => {
    const user = userEvent.setup();
    const calls = mount({ mine: false });
    await user.click(
      await screen.findByRole("button", { name: "创建飞书智能体" }),
    );
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Agent" })).toBeDisabled(),
    );
    expect(screen.getByRole("button", { name: "下一步" })).toBeDisabled();
    expect(calls.filter((call) => call.method === "POST")).toHaveLength(0);
  });

  it("shows only this Agent's binding and prevents duplicate creation", async () => {
    mount({ bound: true });
    expect(await screen.findByText("Target bot")).toBeVisible();
    expect(screen.queryByText("Other bot")).toBeNull();
    expect(
      screen.getByRole("button", { name: "创建飞书智能体" }),
    ).toBeDisabled();
  });

  it("keeps DingTalk's organization-connection prerequisite", async () => {
    const user = userEvent.setup();
    const calls = mount({ connected: false });
    await user.click(screen.getByRole("tab", { name: "钉钉" }));
    expect(
      await screen.findByRole("button", { name: "创建钉钉智能体" }),
    ).toBeDisabled();
    expect(calls.filter((call) => call.method === "POST")).toHaveLength(0);
  });

  it("uses the current Agent for WeChat scanning and cancels on close", async () => {
    const user = userEvent.setup();
    const calls = mount();
    await user.click(screen.getByRole("tab", { name: "微信" }));
    await user.click(await screen.findByRole("button", { name: "扫码绑定" }));
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "下一步" })).toBeEnabled(),
    );
    expect(screen.getByRole("button", { name: "Agent" })).toBeDisabled();
    await user.click(screen.getByRole("button", { name: "下一步" }));
    await waitFor(() =>
      expect(
        calls.some(
          (call) =>
            call.path.endsWith("/wechat/agent-bindings/qr") &&
            call.method === "POST" &&
            JSON.stringify(call.body) === JSON.stringify({ agent_id: "a1" }),
        ),
      ).toBe(true),
    );
    await user.keyboard("{Escape}");
    await waitFor(() =>
      expect(
        calls.some((call) =>
          call.path.endsWith("/wechat/agent-bindings/qr/q1/cancel"),
        ),
      ).toBe(true),
    );
  });
});
