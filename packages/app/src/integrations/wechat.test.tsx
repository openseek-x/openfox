import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { entry } from "../agents/fixtures";
import {
  findCall,
  json,
  renderSettings,
  ts,
  type Route,
} from "../settings/testing";

// 微信（ClawBot / iLink）扫码绑定 Agent：扫码弹窗各状态、已绑定列表、解绑。

const o = "/v1/organizations/o1/integrations";
const qr = `${o}/wechat/agent-bindings/qr`;
const page = "/o/o1/settings/integrations/wechat";

const provider = {
  id: "wechat",
  name: "微信",
  category: "collaboration",
  availability: "available",
  template: "C3",
  capabilities: ["agent_binding", "personal"],
  configured: true,
  bindable: true,
  summary: { connections: 0, status: "none" },
};

const binding = (extra: Record<string, unknown> = {}) => ({
  id: "b1",
  agent: { id: "a1", name: "我的 Agent" },
  wechat_user: "wx***chat",
  initiator: { id: "u1", name: "lin@openfox.dev" },
  status: "ACTIVE",
  status_reason: "",
  connected: true,
  can_manage: true,
  created_at: ts,
  version: 2,
  ...extra,
});

const view = (extra: Record<string, unknown> = {}) => ({
  configured: true,
  can_bind: true,
  max_bindings: 20,
  bindings: [],
  ...extra,
});

const session = (extra: Record<string, unknown> = {}) => ({
  id: "q1",
  agent_id: "a1",
  status: "PENDING",
  qr_content: "https://liteapp.weixin.qq.com/q/fake",
  error_code: "",
  binding_id: null,
  expires_at: ts,
  created_at: ts,
  finished_at: null,
  ...extra,
});

const directory = {
  entries: [
    entry("a1", { name: "我的 Agent", mine: true }),
    entry("a2", { name: "别人的 Agent", mine: false }),
  ],
  archived_count: 0,
  truncated: false,
};

function route(bindings: unknown, more?: Route): Route {
  return (call) => {
    const custom = more?.(call);
    if (custom) return custom;
    if (call.method === "GET" && call.path === `${o}/wechat/bindings`)
      return json(200, bindings);
    if (call.method === "GET" && call.path === `${o}/wechat`)
      return json(200, {
        provider,
        can_manage: true,
        connections: [],
        personal_connections: [],
        sync_links: [],
      });
    if (
      call.method === "GET" &&
      call.path === "/v1/organizations/o1/agent-directory"
    )
      return json(200, directory);
    return undefined;
  };
}

beforeEach(() => localStorage.clear());
afterEach(() => vi.restoreAllMocks());

async function openDialog(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByRole("button", { name: "扫码绑定" }));
  const dialog = await screen.findByRole("dialog");
  await user.click(within(dialog).getByRole("button", { name: "Agent" }));
  await user.click(await screen.findByRole("option", { name: /我的 Agent/ }));
  await user.click(within(dialog).getByRole("button", { name: "下一步" }));
  return dialog;
}

describe("微信 · 已绑定列表", () => {
  it("空态与已绑定行（脱敏微信、状态）", async () => {
    renderSettings(page, route(view()));
    expect(await screen.findByText("还没有绑定任何微信。")).toBeVisible();
  });

  it("显示绑定、已连接与失效提示", async () => {
    renderSettings(
      page,
      route(
        view({
          bindings: [
            binding(),
            binding({
              id: "b2",
              agent: { id: "a2", name: "第二个" },
              status: "EXPIRED",
              connected: false,
            }),
          ],
        }),
      ),
    );
    expect(await screen.findByText("我的 Agent")).toBeVisible();
    expect(screen.getByText("已连接")).toBeVisible();
    expect(screen.getByText("登录已失效")).toBeVisible();
    expect(screen.getByText("微信登录已失效，请重新扫码。")).toBeVisible();
    expect(screen.getAllByText(/wx\*\*\*chat/).length).toBe(2);
    expect(screen.getByRole("button", { name: "重新扫码" })).toBeVisible();
  });

  it("未配置时禁用扫码绑定并说明原因", async () => {
    renderSettings(page, route(view({ configured: false })));
    expect(
      await screen.findByRole("button", { name: "扫码绑定" }),
    ).toBeDisabled();
    expect(screen.getAllByText(/服务器尚未配置微信/).length).toBeGreaterThan(0);
  });

  it("解绑：确认后带版本提交", async () => {
    const user = userEvent.setup();
    let body: unknown;
    const calls = renderSettings(
      page,
      route(view({ bindings: [binding()] }), (call) => {
        if (
          call.method === "POST" &&
          call.path === `${o}/wechat/bindings/b1/remove`
        ) {
          body = call.body;
          return { status: 204, body: "" } as never;
        }
        return undefined;
      }),
    );
    await user.click(
      await screen.findByRole("button", { name: "我的 Agent 的更多操作" }),
    );
    await user.click(await screen.findByRole("menuitem", { name: "解绑" }));
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: "解绑" }));
    await waitFor(() => expect(body).toEqual({ version: 2 }));
    expect(calls.length).toBeGreaterThan(0);
  });
});

describe("微信 · 扫码弹窗", () => {
  it("StrictMode 只申请一个二维码，旧副作用不会取消或覆盖当前会话", async () => {
    const user = userEvent.setup();
    let starts = 0;
    const calls = renderSettings(
      page,
      route(view(), (call) => {
        if (call.method === "POST" && call.path === qr) {
          starts++;
          return json(201, session());
        }
        if (call.method === "GET" && call.path === `${qr}/q1`)
          return json(200, session({ status: "SCANNED" }));
        return undefined;
      }),
      "OWNER",
      true,
    );
    await openDialog(user);
    expect(
      await screen.findByText("已扫码，请在微信里确认登录。"),
    ).toBeVisible();
    expect(starts).toBe(1);
    expect(
      calls.filter((c) => c.method === "GET" && c.path === `${qr}/q1`),
    ).toHaveLength(1);
    expect(findCall(calls, "POST", `${qr}/q1/cancel`)).toBeUndefined();
  });

  it("显示二维码，已扫码后确认成功并关闭", async () => {
    const user = userEvent.setup();
    let polls = 0;
    renderSettings(
      page,
      route(view(), (call) => {
        if (call.method === "POST" && call.path === qr)
          return json(201, session());
        if (call.method === "GET" && call.path === `${qr}/q1`) {
          polls++;
          return json(
            200,
            polls === 1
              ? session({ status: "SCANNED" })
              : session({ status: "CONFIRMED", binding_id: "b1" }),
          );
        }
        return undefined;
      }),
    );
    await openDialog(user);
    expect(
      await screen.findByRole("img", { name: "微信绑定二维码" }),
    ).toBeVisible();
    expect(await screen.findByText("微信已绑定")).toBeVisible();
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("需要验证码：提交后继续", async () => {
    const user = userEvent.setup();
    let verifyBody: unknown;
    renderSettings(
      page,
      route(view(), (call) => {
        if (call.method === "POST" && call.path === qr)
          return json(201, session({ status: "NEED_VERIFY" }));
        if (call.method === "POST" && call.path === `${qr}/q1/verify`) {
          verifyBody = call.body;
          return json(200, session({ status: "CONFIRMED", binding_id: "b1" }));
        }
        return undefined;
      }),
    );
    const dialog = await openDialog(user);
    await user.type(
      await within(dialog).findByRole("textbox", { name: "验证码" }),
      "123456",
    );
    await user.click(within(dialog).getByRole("button", { name: "提交" }));
    await waitFor(() => expect(verifyBody).toEqual({ code: "123456" }));
  });

  it("过期：显示原因并可重新生成", async () => {
    const user = userEvent.setup();
    let starts = 0;
    renderSettings(
      page,
      route(view(), (call) => {
        if (call.method === "POST" && call.path === qr) {
          starts++;
          return json(
            201,
            starts === 1
              ? session({ status: "EXPIRED", finished_at: ts })
              : session(),
          );
        }
        if (call.method === "GET" && call.path === `${qr}/q1`)
          return json(200, session());
        return undefined;
      }),
    );
    const dialog = await openDialog(user);
    expect(
      await within(dialog).findByText("二维码已过期，请重新生成。"),
    ).toBeVisible();
    await user.click(
      within(dialog).getByRole("button", { name: "重新生成二维码" }),
    );
    expect(
      await screen.findByRole("img", { name: "微信绑定二维码" }),
    ).toBeVisible();
    expect(starts).toBe(2);
  });

  it("非白名单主机等失败给出说明；关闭时取消进行中的会话", async () => {
    const user = userEvent.setup();
    const calls = renderSettings(
      page,
      route(view(), (call) => {
        if (call.method === "POST" && call.path === qr)
          return json(201, session());
        if (call.method === "GET" && call.path === `${qr}/q1`)
          return json(200, session({ status: "SCANNED" }));
        if (call.method === "POST" && call.path === `${qr}/q1/cancel`)
          return json(200, session({ status: "CANCELLED" }));
        return undefined;
      }),
    );
    const dialog = await openDialog(user);
    expect(
      await within(dialog).findByText("已扫码，请在微信里确认登录。"),
    ).toBeVisible();
    await user.click(within(dialog).getByRole("button", { name: "取消" }));
    await waitFor(() =>
      expect(findCall(calls, "POST", `${qr}/q1/cancel`)).toBeTruthy(),
    );
  });
});
