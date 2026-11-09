import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { device, entry } from "../agents/fixtures";
import {
  apiError,
  findCall,
  json,
  renderSettings,
  ts,
  type Route,
} from "../settings/testing";

// 飞书任务助手与绑定 Agents、扫码面板。

const o = "/v1/organizations/o1/integrations";
const page = "/o/o1/settings/integrations/feishu";

const provider = {
  id: "feishu",
  name: "飞书",
  category: "collaboration",
  availability: "available",
  template: "C1",
  capabilities: ["notifications"],
  configured: true,
  bindable: true,
  summary: { connections: 1, status: "ok" },
};

const appView = {
  can_manage: true,
  app: null,
  personal: null,
  guide: {
    open_platform_url: "https://open.feishu.cn/app",
    redirect_uri: "https://x.example/cb",
    link_preview_pattern: "https://x.example/*",
    scopes_json: "{}",
    events: [],
    callbacks: [],
  },
};

const bot = (extra: Record<string, unknown> = {}) => ({
  id: "b1",
  kind: "TASK_ASSISTANT",
  connection_id: "c1",
  app_id: "cli_abcdef123456",
  bot_name: "OpenFox 任务助手",
  bot_avatar_url: "",
  status: "CONNECTED",
  status_reason: "",
  agent: { id: "ag0", name: "任务助手" },
  space: { id: "s1", name: "默认空间" },
  extra_prompt: "",
  runtime: {
    computer_id: "d1",
    computer_name: "电脑 d1",
    grant_id: "g-d1",
    runtime: "codex",
    model: "default",
  },
  can_manage: true,
  created_at: ts,
  version: 3,
  ...extra,
});

const agentBot = (id: string, name: string, extra = {}) =>
  bot({
    id,
    kind: "AGENT",
    agent: { id: `ag-${id}`, name },
    space: null,
    runtime: null,
    ...extra,
  });

const botsView = (extra: Record<string, unknown> = {}) => ({
  app_connected: true,
  assistants: [
    { space_id: "s1", space_name: "默认空间", can_manage: true, bot: null },
  ],
  agents: [],
  can_bind_agent: true,
  builtin_prompt: "你是 OpenFox 任务助手。\n请按上下文创建任务。",
  ...extra,
});

const registration = (extra: Record<string, unknown> = {}) => ({
  id: "r1",
  purpose: "task_assistant",
  status: "PENDING",
  error_code: "",
  verification_url: "https://open.feishu.cn/verify?code=ABCD",
  expires_at: new Date(Date.now() + 600_000).toISOString(),
  poll_interval: 1,
  bot_id: null,
  ...extra,
});

function route(bots: unknown, more?: Route): Route {
  return (call) => {
    const custom = more?.(call);
    if (custom) return custom;
    if (call.method === "GET" && call.path === `${o}/feishu/bots`)
      return json(200, bots);
    if (call.method === "GET" && call.path === `${o}/feishu/app`)
      return json(200, appView);
    if (call.method === "GET" && call.path === `${o}/feishu`)
      return json(200, {
        provider,
        can_manage: true,
        connections: [],
        personal_connections: [],
        sync_links: [],
      });
    if (call.method === "GET" && call.path === "/v1/organizations/o1/devices")
      return json(200, { devices: [device("d1")], next_cursor: null });
    return undefined;
  };
}

beforeEach(() => localStorage.clear());
afterEach(() => vi.restoreAllMocks());

/** 在绑定对话框里选好 Runtime 并点「下一步」。 */
async function chooseRuntimeAndNext(user: ReturnType<typeof userEvent.setup>) {
  const dialog = await screen.findByRole("dialog");
  await user.click(within(dialog).getByRole("button", { name: "Runtime" }));
  await user.click(await screen.findByRole("option", { name: /Codex/ }));
  await user.click(within(dialog).getByRole("button", { name: "下一步" }));
}

describe("飞书 · 任务助手分区", () => {
  it("未绑定且可管理：绑定按钮可用", async () => {
    renderSettings(page, route(botsView()));
    expect(
      await screen.findByText("OpenFox 任务助手 - 默认空间"),
    ).toBeVisible();
    expect(screen.getByRole("button", { name: "绑定" })).toBeEnabled();
  });

  it("不能管理：禁用并说明原因", async () => {
    renderSettings(
      page,
      route(
        botsView({
          assistants: [
            {
              space_id: "s1",
              space_name: "默认空间",
              can_manage: false,
              bot: null,
            },
          ],
        }),
      ),
    );
    expect(await screen.findByText("只有空间管理员可以绑定")).toBeVisible();
    expect(screen.getByRole("button", { name: "绑定" })).toBeDisabled();
  });

  it("组织未绑定飞书应用：独立 Agent 可扫码，任务助手仍要求组织应用", async () => {
    renderSettings(page, route(botsView({ app_connected: false })));
    expect(
      (await screen.findAllByText("需先由管理员绑定飞书应用")).length,
    ).toBeGreaterThan(0);
    expect(screen.getByRole("button", { name: "绑定" })).toBeDisabled();
    expect(
      screen.getByRole("button", { name: "创建飞书智能体" }),
    ).toBeEnabled();
  });

  it.each([
    ["CONNECTING", "连接中"],
    ["CONNECTED", "已连接"],
    ["FAILED", "连接失败"],
  ])("已绑定：状态 %s 显示「%s」", async (status, text) => {
    renderSettings(
      page,
      route(
        botsView({
          assistants: [
            {
              space_id: "s1",
              space_name: "默认空间",
              can_manage: true,
              bot: bot({ status }),
            },
          ],
        }),
      ),
    );
    expect(await screen.findByText("已绑定飞书")).toBeVisible();
    expect(screen.getByText(text)).toBeVisible();
    expect(screen.queryByRole("button", { name: "重试" }) !== null).toBe(
      status === "FAILED",
    );
  });

  it("FAILED 行点重试调用 retry", async () => {
    const user = userEvent.setup();
    const calls = renderSettings(
      page,
      route(
        botsView({
          assistants: [
            {
              space_id: "s1",
              space_name: "默认空间",
              can_manage: true,
              bot: bot({ status: "FAILED" }),
            },
          ],
        }),
        (call) =>
          call.method === "POST" && call.path === `${o}/feishu/bots/b1/retry`
            ? json(200, bot({ status: "CONNECTING" }))
            : undefined,
      ),
    );
    await user.click(await screen.findByRole("button", { name: "重试" }));
    await waitFor(() =>
      expect(
        findCall(calls, "POST", `${o}/feishu/bots/b1/retry`),
      ).toBeDefined(),
    );
  });
});

describe("飞书 · 绑定任务助手与扫码", () => {
  it("Runtime 必选；附加提示词默认收起；可查看内置提示词", async () => {
    const user = userEvent.setup();
    renderSettings(page, route(botsView()));
    await user.click(await screen.findByRole("button", { name: "绑定" }));
    const dialog = await screen.findByRole("dialog");
    expect(
      within(dialog).getByText(/选择运行「默认空间」任务助手的 Runtime/),
    ).toBeVisible();
    expect(
      within(dialog).getByRole("button", { name: "下一步" }),
    ).toBeDisabled();
    expect(within(dialog).queryByRole("textbox")).toBeNull();
    await user.click(
      within(dialog).getByRole("button", { name: "附加提示词" }),
    );
    expect(
      within(dialog).getByPlaceholderText("点击填写附加提示词"),
    ).toBeVisible();
    expect(
      within(dialog).getByText(/系统已内置了在飞书里创建任务相关的提示词/),
    ).toBeVisible();
    await user.click(
      within(dialog).getByRole("button", { name: "查看内置提示词" }),
    );
    expect(await screen.findByText("内置提示词")).toBeVisible();
    expect(screen.getByText(/请按上下文创建任务/)).toBeVisible();
  });

  it("下一步发起扫码：显示生成中、二维码和链接；轮询到 COMPLETED 后提示并关闭", async () => {
    const user = userEvent.setup();
    let startBody: unknown;
    let polls = 0;
    const calls = renderSettings(
      page,
      route(botsView(), (call) => {
        if (
          call.method === "POST" &&
          call.path === `${o}/feishu/registrations`
        ) {
          startBody = call.body;
          return json(201, registration());
        }
        if (
          call.method === "GET" &&
          call.path === `${o}/feishu/registrations/r1`
        ) {
          polls++;
          return json(
            200,
            polls > 1
              ? registration({
                  status: "COMPLETED",
                  bot_id: "b1",
                  verification_url: "",
                })
              : registration(),
          );
        }
        return undefined;
      }),
    );
    await user.click(await screen.findByRole("button", { name: "绑定" }));
    const dialog = await screen.findByRole("dialog");
    await user.click(
      within(dialog).getByRole("button", { name: "附加提示词" }),
    );
    await user.type(
      within(dialog).getByPlaceholderText("点击填写附加提示词"),
      "约定：标题写中文",
    );
    await chooseRuntimeAndNext(user);
    expect(
      await screen.findByRole("img", { name: "飞书授权二维码" }),
    ).toBeVisible();
    expect(startBody).toEqual({
      purpose: "task_assistant",
      space_id: "s1",
      runtime: { computer_id: "d1", grant_id: "g-d1", runtime: "codex" },
      extra_prompt: "约定：标题写中文",
    });
    expect(screen.getByText("等待你在飞书中完成授权…")).toBeVisible();
    expect(
      screen.getByText("使用飞书扫码，或打开链接，然后完成飞书智能体的创建。"),
    ).toBeVisible();
    expect(screen.getByRole("button", { name: /打开链接/ })).toBeVisible();
    expect(
      await screen.findByText("已绑定飞书任务助手", {}, { timeout: 5000 }),
    ).toBeVisible();
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(
      calls.filter((c) => c.method === "GET" && c.path === `${o}/feishu/bots`)
        .length,
    ).toBeGreaterThan(1);
  });

  it.each([
    ["expired", "链接在授权前已过期，请重新发起。"],
    ["access_denied", "已在飞书中取消授权。"],
    ["rejected", "飞书拒绝了该请求，请重试。"],
    ["agent_connected", "此 Agent 已经连接飞书，不能重复创建。"],
    ["assistant_exists", "该空间已经有任务助手。"],
    ["session_expired", "会话已过期，请重新发起。"],
    ["superseded", "会话已过期，请重新发起。"],
    ["save_failed", "保存失败，请重试。"],
  ])("终态错误 %s 显示文案并可重新发起", async (code, text) => {
    const user = userEvent.setup();
    let starts = 0;
    renderSettings(
      page,
      route(botsView(), (call) => {
        if (
          call.method === "POST" &&
          call.path === `${o}/feishu/registrations`
        ) {
          starts++;
          return json(
            201,
            starts === 1
              ? registration({
                  status: "FAILED",
                  error_code: code,
                  verification_url: "",
                })
              : registration({ id: "r2" }),
          );
        }
        return undefined;
      }),
    );
    await user.click(await screen.findByRole("button", { name: "绑定" }));
    await chooseRuntimeAndNext(user);
    expect(await screen.findByText(text)).toBeVisible();
    await user.click(screen.getByRole("button", { name: "重新发起" }));
    expect(
      await screen.findByRole("img", { name: "飞书授权二维码" }),
    ).toBeVisible();
    expect(starts).toBe(2);
  });

  it("发起失败：FEISHU_ASSISTANT_EXISTS 显示在面板里", async () => {
    const user = userEvent.setup();
    renderSettings(
      page,
      route(botsView(), (call) =>
        call.method === "POST" && call.path === `${o}/feishu/registrations`
          ? apiError(409, "FEISHU_ASSISTANT_EXISTS")
          : undefined,
      ),
    );
    await user.click(await screen.findByRole("button", { name: "绑定" }));
    await chooseRuntimeAndNext(user);
    expect(
      await screen.findByText(
        "该空间已经有任务助手。每个空间只能有一个任务助手，请先移除已有的任务助手后再绑定。",
      ),
    ).toBeVisible();
  });

  it("空间的任务助手已在别的服务商绑定：该行禁用并说明先在那边移除", async () => {
    renderSettings(
      page,
      route(
        botsView({
          assistants: [
            {
              space_id: "s1",
              space_name: "默认空间",
              can_manage: true,
              bot: null,
              bound_provider: "dingtalk",
            },
            {
              space_id: "s2",
              space_name: "另一个空间",
              can_manage: true,
              bot: null,
            },
          ],
        }),
      ),
    );
    expect(
      await screen.findByText(
        "已在钉钉绑定任务助手。每个空间只能有一个任务助手，先在钉钉移除后再绑定。",
      ),
    ).toBeVisible();
    const buttons = screen.getAllByRole("button", { name: "绑定" });
    expect(buttons).toHaveLength(2);
    expect(buttons[0]).toBeDisabled();
    expect(buttons[1]).toBeEnabled();
  });

  it("点取消调用 cancel 并关闭", async () => {
    const user = userEvent.setup();
    const calls = renderSettings(
      page,
      route(botsView(), (call) => {
        if (call.method === "POST" && call.path === `${o}/feishu/registrations`)
          return json(201, registration());
        if (
          call.method === "GET" &&
          call.path === `${o}/feishu/registrations/r1`
        )
          return json(200, registration());
        if (
          call.method === "POST" &&
          call.path === `${o}/feishu/registrations/r1/cancel`
        )
          return json(200, registration({ status: "CANCELLED" }));
        return undefined;
      }),
    );
    await user.click(await screen.findByRole("button", { name: "绑定" }));
    await chooseRuntimeAndNext(user);
    await screen.findByRole("img", { name: "飞书授权二维码" });
    await user.click(screen.getByRole("button", { name: "取消" }));
    await waitFor(() =>
      expect(
        findCall(calls, "POST", `${o}/feishu/registrations/r1/cancel`),
      ).toBeDefined(),
    );
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("按 Esc 关闭时仍在等待则调用 cancel", async () => {
    const user = userEvent.setup();
    const calls = renderSettings(
      page,
      route(botsView(), (call) => {
        if (call.method === "POST" && call.path === `${o}/feishu/registrations`)
          return json(201, registration());
        if (
          call.method === "GET" &&
          call.path === `${o}/feishu/registrations/r1`
        )
          return json(200, registration());
        if (
          call.method === "POST" &&
          call.path === `${o}/feishu/registrations/r1/cancel`
        )
          return json(200, registration({ status: "CANCELLED" }));
        return undefined;
      }),
    );
    await user.click(await screen.findByRole("button", { name: "绑定" }));
    await chooseRuntimeAndNext(user);
    await screen.findByRole("img", { name: "飞书授权二维码" });
    await user.keyboard("{Escape}");
    await waitFor(() =>
      expect(
        findCall(calls, "POST", `${o}/feishu/registrations/r1/cancel`),
      ).toBeDefined(),
    );
  });
});

describe("飞书 · 编辑与移除任务助手", () => {
  const bound = botsView({
    assistants: [
      {
        space_id: "s1",
        space_name: "默认空间",
        can_manage: true,
        bot: bot({ extra_prompt: "旧约定" }),
      },
    ],
  });

  it("编辑：预填并带 version 保存", async () => {
    const user = userEvent.setup();
    let body: unknown;
    renderSettings(
      page,
      route(bound, (call) => {
        if (call.method === "PATCH" && call.path === `${o}/feishu/bots/b1`) {
          body = call.body;
          return json(200, bot());
        }
        return undefined;
      }),
    );
    await user.click(
      await screen.findByRole("button", { name: "任务助手选项" }),
    );
    await user.click(await screen.findByRole("menuitem", { name: "编辑" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText("编辑任务助手")).toBeVisible();
    const text = within(dialog).getByPlaceholderText("点击填写附加提示词");
    expect(text).toHaveValue("旧约定");
    await user.clear(text);
    await user.type(text, "新约定");
    await user.click(within(dialog).getByRole("button", { name: "保存" }));
    await waitFor(() =>
      expect(body).toEqual({
        runtime: {
          computer_id: "d1",
          grant_id: "g-d1",
          runtime: "codex",
          model: "default",
        },
        extra_prompt: "新约定",
        version: 3,
      }),
    );
    expect(await screen.findByText("任务助手已更新")).toBeVisible();
  });

  it("移除：确认后带 version 调用", async () => {
    const user = userEvent.setup();
    let body: unknown;
    renderSettings(
      page,
      route(bound, (call) => {
        if (
          call.method === "POST" &&
          call.path === `${o}/feishu/bots/b1/remove`
        ) {
          body = call.body;
          return json(204);
        }
        return undefined;
      }),
    );
    await user.click(
      await screen.findByRole("button", { name: "任务助手选项" }),
    );
    await user.click(await screen.findByRole("menuitem", { name: "移除助手" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText("移除任务助手？")).toBeVisible();
    await user.click(within(dialog).getByRole("button", { name: "移除" }));
    await waitFor(() => expect(body).toEqual({ version: 3 }));
    expect(await screen.findByText("已移除任务助手")).toBeVisible();
  });
});

describe("飞书 · 绑定 Agents", () => {
  const directory = {
    entries: [
      entry("a1", { name: "我的 Agent", mine: true }),
      entry("a2", { name: "别人的 Agent", mine: false }),
      entry("a3", { name: "已连接的", mine: true }),
      entry("a4", { name: "已归档的", mine: true, archived_at: ts }),
      entry("c1", { type: "crew", name: "某小队", mine: true }),
    ],
    archived_count: 1,
    truncated: false,
  };
  const withDirectory: Route = (call) =>
    call.method === "GET" &&
    call.path === "/v1/organizations/o1/agent-directory"
      ? json(200, directory)
      : undefined;

  it("空列表显示空态", async () => {
    renderSettings(page, route(botsView(), withDirectory));
    expect(
      await screen.findByText("还没有在飞书绑定任何 Agent。"),
    ).toBeVisible();
    expect(
      screen.getByText("连接 Agent 后，即可在飞书中接收消息并回复。"),
    ).toBeVisible();
  });

  it("下拉过滤：管理员看到全部可连接的 Agent，排除小队、已归档、已连接", async () => {
    const user = userEvent.setup();
    renderSettings(
      page,
      route(
        botsView({
          agents: [
            agentBot("b2", "已连接的", {
              agent: { id: "a3", name: "已连接的" },
            }),
          ],
        }),
        (call) => {
          const r = withDirectory(call);
          if (r) return r;
          return undefined;
        },
      ),
    );
    // 把 a3 的 id 对上已连接的 bot
    await screen.findByText("已连接的 Agent");
    await user.click(screen.getByRole("button", { name: "创建飞书智能体" }));
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: "Agent" }));
    const names = (await screen.findAllByRole("option")).map(
      (el) => el.textContent ?? "",
    );
    expect(names.some((n) => n.includes("我的 Agent"))).toBe(true);
    expect(names.some((n) => n.includes("别人的 Agent"))).toBe(true);
    expect(names.some((n) => n.includes("已归档的"))).toBe(false);
    expect(names.some((n) => n.includes("某小队"))).toBe(false);
    expect(names.some((n) => n.includes("已连接的"))).toBe(false);
  });

  it("非管理员只看到自己的 Agent；选择后发起 agent 扫码", async () => {
    const user = userEvent.setup();
    let startBody: unknown;
    renderSettings(
      page,
      route(botsView(), (call) => {
        const r = withDirectory(call);
        if (r) return r;
        if (
          call.method === "POST" &&
          call.path === `${o}/feishu/registrations`
        ) {
          startBody = call.body;
          return json(201, registration({ purpose: "agent" }));
        }
        return undefined;
      }),
      "MEMBER",
    );
    await user.click(
      await screen.findByRole("button", { name: "创建飞书智能体" }),
    );
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: "Agent" }));
    const names = (await screen.findAllByRole("option")).map(
      (el) => el.textContent ?? "",
    );
    expect(names.some((n) => n.includes("别人的 Agent"))).toBe(false);
    await user.click(await screen.findByRole("option", { name: /我的 Agent/ }));
    await user.click(within(dialog).getByRole("button", { name: "下一步" }));
    expect(
      await screen.findByRole("img", { name: "飞书授权二维码" }),
    ).toBeVisible();
    expect(startBody).toEqual({ purpose: "agent", agent_id: "a1" });
  });

  it("没有权限：按钮禁用并说明", async () => {
    renderSettings(
      page,
      route(botsView({ can_bind_agent: false }), withDirectory),
    );
    expect(
      await screen.findByText(
        "只有组织所有者、管理员或 Agent 的所有者可以创建",
      ),
    ).toBeVisible();
    expect(
      screen.getByRole("button", { name: "创建飞书智能体" }),
    ).toBeDisabled();
  });

  it("已连接列表：状态、重试与断开", async () => {
    const user = userEvent.setup();
    let body: unknown;
    renderSettings(
      page,
      route(
        botsView({
          agents: [
            agentBot("b2", "熊孩子", { status: "CONNECTED" }),
            agentBot("b3", "架构师", { status: "FAILED" }),
            agentBot("b4", "测试员", { status: "CONNECTING" }),
          ],
        }),
        (call) => {
          if (
            call.method === "POST" &&
            call.path === `${o}/feishu/bots/b2/remove`
          ) {
            body = call.body;
            return json(204);
          }
          return undefined;
        },
      ),
    );
    expect(await screen.findByText("熊孩子")).toBeVisible();
    expect(screen.getByText("架构师")).toBeVisible();
    expect(screen.getByText("连接失败")).toBeVisible();
    expect(screen.getByText("连接中")).toBeVisible();
    expect(screen.getAllByRole("button", { name: "重试" })).toHaveLength(1);
    await user.click(screen.getByRole("button", { name: "熊孩子 的更多操作" }));
    await user.click(await screen.findByRole("menuitem", { name: "断开连接" }));
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: "断开连接" }));
    await waitFor(() => expect(body).toEqual({ version: 3 }));
    expect(await screen.findByText("已断开连接")).toBeVisible();
  });
});
