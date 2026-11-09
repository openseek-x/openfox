import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import type {
  ApprovalMode,
  ConversationOverrideOptions,
  ConversationSummary,
} from "@openfox/api";
import { I18nProvider } from "@openfox/i18n";
import { ToastProvider } from "@openfox/ui";

import { agent, device, entry } from "../agents/fixtures";
import { apiError, json, renderSettings, type Call } from "../settings/testing";
import { ORG, chatBase, message, summary } from "./fixtures";
import { OverrideControls } from "./OverrideControls";
import {
  APPROVAL_MODES,
  agentDefaultModel,
  effectiveOf,
  isNoop,
  patchFor,
  requestBody,
} from "./overrides";

// 对话输入框里 Runtime / 模型 / 权限模式的就地切换。

afterEach(() => localStorage.clear());

const ALL: ApprovalMode[] = [...APPROVAL_MODES];
const OPTIONS: ConversationOverrideOptions = {
  computer_online: true,
  runtimes: [
    {
      provider: "codex",
      runnable: true,
      models: [{ id: "gpt-5.1", display_name: "GPT 5.1", is_default: true }],
      approval_modes: ALL,
    },
    {
      provider: "claude-code",
      runnable: true,
      models: [{ id: "sonnet", display_name: "Sonnet", is_default: false }],
      approval_modes: ALL,
    },
    { provider: "opencode", runnable: false, models: [], approval_modes: ALL },
    {
      provider: "pi",
      runnable: true,
      models: [],
      approval_modes: ["FOLLOW_RUNTIME"],
    },
  ],
};

const DETAIL = `${ORG}/conversations/c1`;
const OPTIONS_PATH = `${DETAIL}/override-options`;
const OVERRIDES_PATH = `${DETAIL}/overrides`;

/** 摘要：默认 codex / default / FOLLOW_RUNTIME，extra 里可带覆盖与有效值 */
function conv(extra: Partial<ConversationSummary> = {}) {
  return summary("c1", extra);
}

/** 对话页公共路由：摘要、消息、可选项；overrides 的 PATCH 由 patch 处理 */
function open(
  path: string,
  options: {
    summary?: ConversationSummary;
    options?: () => Response;
    patch?: (call: Call) => Response;
    extra?: (call: Call) => Response | undefined;
  } = {},
) {
  const current = options.summary ?? conv();
  return renderSettings(
    path,
    chatBase((call) => {
      const custom = options.extra?.(call);
      if (custom) return custom;
      if (call.method === "PATCH" && call.path === OVERRIDES_PATH)
        return options.patch?.(call) ?? json(200, current);
      if (call.method !== "GET") return undefined;
      if (call.path === OPTIONS_PATH)
        return options.options?.() ?? json(200, OPTIONS);
      if (call.path === `${DETAIL}/messages`)
        return json(200, { messages: [], next_cursor: 0 });
      if (call.path === DETAIL) return json(200, current);
      if (call.path === `${ORG}/conversations`)
        return json(200, { conversations: [current], next_cursor: null });
      return undefined;
    }),
  );
}

const patches = (calls: Call[]) =>
  calls.filter((c) => c.method === "PATCH" && c.path === OVERRIDES_PATH);
const gets = (calls: Call[], path: string) =>
  calls.filter((c) => c.method === "GET" && c.path === path);

const runtimeButton = (name: RegExp | string) =>
  screen.findByRole("button", { name });

describe("输入框里的 Runtime / 模型 / 权限模式", () => {
  it("模型子菜单只显示交集深度，选择后原子保存模型与深度", async () => {
    const user = userEvent.setup();
    const calls = open("/o/o1/chat/c1", {
      options: () =>
        json(200, {
          computer_online: true,
          runtimes: [
            {
              ...OPTIONS.runtimes[0]!,
              reasoning_efforts: ["DEFAULT", "HIGH", "EXTRA_HIGH"],
              models: [
                {
                  id: "gpt-5.1",
                  display_name: "GPT 5.1",
                  is_default: true,
                  reasoning_efforts: ["DEFAULT", "HIGH"],
                  default_reasoning_effort: "HIGH",
                },
              ],
            },
          ],
        }),
    });
    await user.click(await runtimeButton("模型：默认"));
    await user.click(
      await screen.findByRole("button", { name: "GPT 5.1：选择思考深度" }),
    );
    const menu = await screen.findByRole("menu", {
      name: "GPT 5.1：选择思考深度",
    });
    expect(
      within(menu)
        .getAllByRole("menuitemradio")
        .map((item) => item.textContent),
    ).toEqual(["默认", "高默认"]);
    expect(
      within(menu).getByRole("menuitemradio", { name: /^高/ }),
    ).toHaveAttribute("aria-checked", "false");
    expect(patches(calls)).toHaveLength(0);
    await user.click(within(menu).getByRole("menuitemradio", { name: /^高/ }));
    await waitFor(() => expect(patches(calls)).toHaveLength(1));
    expect(patches(calls)[0]!.body).toEqual({
      model: "gpt-5.1",
      reasoning_effort: "HIGH",
      expected_version: 0,
    });
    await waitFor(() =>
      expect(screen.queryByRole("menu")).not.toBeInTheDocument(),
    );
  });

  it("旧电脑不展示不可执行的深度子菜单", async () => {
    open("/o/o1/chat/c1");
    await userEvent.click(await runtimeButton("模型：默认"));
    expect(
      screen.queryByRole("button", { name: /选择思考深度/ }),
    ).not.toBeInTheDocument();
  });

  it.each([undefined, "LOW", "EXTRA_HIGH", "ULTRA", "UNKNOWN", "DEFAULT"])(
    "默认标记仅使用模型明确且可用的深度：%s",
    async (reportedDefault) => {
      const user = userEvent.setup();
      const calls = open("/o/o1/chat/c1", {
        options: () =>
          json(200, {
            computer_online: true,
            runtimes: [
              {
                ...OPTIONS.runtimes[0]!,
                reasoning_efforts: ["DEFAULT", "LOW", "HIGH"],
                models: [
                  {
                    id: "gpt-5.1",
                    display_name: "GPT 5.1",
                    is_default: true,
                    reasoning_efforts: [
                      "DEFAULT",
                      "LOW",
                      "HIGH",
                      "EXTRA_HIGH",
                      "ULTRA",
                    ],
                    default_reasoning_effort: reportedDefault,
                  },
                ],
              },
            ],
          }),
      });
      await user.click(await runtimeButton("模型：默认"));
      await user.click(
        await screen.findByRole("button", { name: "默认：选择思考深度" }),
      );
      const menu = within(
        await screen.findByRole("menu", { name: "默认：选择思考深度" }),
      );
      expect(
        menu.getAllByRole("menuitemradio").map((item) => item.textContent),
      ).toEqual(["默认", reportedDefault === "LOW" ? "低默认" : "低", "高"]);
      expect(menu.getByRole("menuitemradio", { name: "默认" })).toHaveAttribute(
        "aria-checked",
        "true",
      );
      expect(patches(calls)).toHaveLength(0);
    },
  );

  it("搜索匹配模型名称或 id，空结果回车不写入，刷新重新获取且不关闭菜单", async () => {
    const user = userEvent.setup();
    let refreshed = false;
    const calls = open("/o/o1/chat/c1", {
      options: () =>
        json(200, {
          ...OPTIONS,
          runtimes: [
            {
              ...OPTIONS.runtimes[0]!,
              models: [
                ...OPTIONS.runtimes[0]!.models,
                ...(refreshed
                  ? [
                      {
                        id: "new-id",
                        display_name: "New model",
                        is_default: false,
                      },
                    ]
                  : []),
              ],
            },
          ],
        }),
    });
    await user.click(await runtimeButton("模型：默认"));
    const menu = await screen.findByRole("menu");
    const input = within(menu).getByRole("textbox", { name: "搜索模型" });
    await waitFor(() => expect(input).toHaveFocus());
    await user.type(input, "GPT");
    expect(within(menu).getAllByRole("menuitemradio")).toHaveLength(1);
    expect(
      within(menu).getByRole("menuitemradio", { name: "GPT 5.1" }),
    ).toBeVisible();
    await user.clear(input);
    await user.type(input, "missing-model{Enter}");
    expect(within(menu).queryByRole("menuitemradio")).not.toBeInTheDocument();
    expect(patches(calls)).toHaveLength(0);
    refreshed = true;
    await user.click(
      within(menu).getByRole("button", { name: "刷新模型列表" }),
    );
    await waitFor(() => expect(gets(calls, OPTIONS_PATH)).toHaveLength(2));
    expect(menu).toBeVisible();
    await user.clear(input);
    await user.type(input, "new-id");
    expect(
      await within(menu).findByRole("menuitemradio", { name: "New model" }),
    ).toBeEnabled();
    await user.keyboard("{Enter}");
    await waitFor(() => expect(patches(calls)).toHaveLength(1));
    expect(patches(calls)[0]!.body).toEqual({
      model: "new-id",
      expected_version: 0,
    });
  });

  it("三个按钮显示有效值；Runtime 菜单列出可选项，不可运行的禁用并写明原因", async () => {
    const user = userEvent.setup();
    open("/o/o1/chat/c1");
    await user.click(await runtimeButton("Runtime：Codex"));
    const menu = await screen.findByRole("menu", {
      name: "本对话使用的 Runtime",
    });
    expect(within(menu).getByText("Agent 默认：Codex")).toBeInTheDocument();
    expect(within(menu).queryByRole("note")).not.toBeInTheDocument();
    const items = within(menu).getAllByRole("menuitemradio");
    expect(items.map((item) => item.textContent)).toEqual([
      "Codex",
      "Claude Code",
      "OpenCode当前不可运行（未登录或不可用）",
      "pi",
    ]);
    expect(
      within(menu).getByRole("menuitemradio", { name: /OpenCode/ }),
    ).toHaveAttribute("aria-disabled", "true");
    expect(
      within(menu).getByRole("menuitemradio", { name: "Codex" }),
    ).toHaveAttribute("aria-checked", "true");
    expect(
      within(menu).queryByRole("menuitem", { name: "恢复 Agent 默认" }),
    ).not.toBeInTheDocument();
    expect(within(menu).queryByText("本对话")).not.toBeInTheDocument();
    // 另两个按钮
    expect(screen.getByRole("button", { name: "模型：默认" })).toBeVisible();
    expect(
      screen.getByRole("button", { name: "权限模式：跟随 Runtime" }),
    ).toBeVisible();
  });

  it("选 Runtime：只发 runtime 与 expected_version，成功后按钮标出「本对话」", async () => {
    const user = userEvent.setup();
    const after = conv({
      overrides: {
        runtime: "claude-code",
        model: null,
        approval_mode: null,
        version: 1,
      },
      effective: {
        runtime: "claude-code",
        model: "default",
        approval_mode: "FOLLOW_RUNTIME",
      },
    });
    const calls = open("/o/o1/chat/c1", { patch: () => json(200, after) });
    await user.click(await runtimeButton("Runtime：Codex"));
    await user.click(
      within(await screen.findByRole("menu")).getByRole("menuitemradio", {
        name: "Claude Code",
      }),
    );
    await waitFor(() => expect(patches(calls)).toHaveLength(1));
    // 不带 model：后端把只覆盖 Runtime 视为 default，前端不另发模型请求
    expect(patches(calls)[0]!.body).toEqual({
      runtime: "claude-code",
      expected_version: 0,
    });
    const button = await runtimeButton("Runtime：Claude Code（本对话）");
    expect(within(button).getByTestId("override-dot")).toBeInTheDocument();
    await user.click(button);
    const menu = await screen.findByRole("menu");
    expect(within(menu).getByText("Agent 默认：Codex")).toBeInTheDocument();
    expect(within(menu).getByText("本对话")).toBeInTheDocument();
    expect(patches(calls)).toHaveLength(1);
    await user.click(
      within(menu).getByRole("menuitemradio", { name: "Codex" }),
    );
    await waitFor(() => expect(patches(calls)).toHaveLength(2));
    expect(patches(calls)[1]!.body).toEqual({
      runtime: null,
      expected_version: 1,
    });
  });

  it("已有模型覆盖时换 Runtime，同一个请求里清除模型覆盖", async () => {
    const user = userEvent.setup();
    const current = conv({
      overrides: {
        runtime: null,
        model: "gpt-5.1",
        approval_mode: null,
        version: 3,
      },
      effective: {
        runtime: "codex",
        model: "gpt-5.1",
        approval_mode: "FOLLOW_RUNTIME",
      },
    });
    const calls = open("/o/o1/chat/c1", { summary: current });
    await user.click(await runtimeButton("Runtime：Codex"));
    await user.click(
      within(await screen.findByRole("menu")).getByRole("menuitemradio", {
        name: "Claude Code",
      }),
    );
    await waitFor(() => expect(patches(calls)).toHaveLength(1));
    expect(patches(calls)[0]!.body).toEqual({
      runtime: "claude-code",
      model: null,
      expected_version: 3,
    });
  });

  it("模型菜单：默认 + 当前 Runtime 的模型；选模型发 model；恢复默认发 null", async () => {
    const user = userEvent.setup();
    const current = conv({
      overrides: {
        runtime: null,
        model: "gpt-5.1",
        approval_mode: null,
        version: 2,
      },
      effective: {
        runtime: "codex",
        model: "gpt-5.1",
        approval_mode: "FOLLOW_RUNTIME",
      },
    });
    const calls = open("/o/o1/chat/c1", { summary: current });
    await user.click(await runtimeButton("模型：GPT 5.1（本对话）"));
    const menu = await screen.findByRole("menu", {
      name: "本对话使用的模型",
    });
    expect(within(menu).getByText("Agent 默认：默认")).toBeInTheDocument();
    expect(
      within(menu)
        .getAllByRole("menuitemradio")
        .map((item) => item.textContent),
    ).toEqual(["默认", "GPT 5.1本对话"]);
    await user.click(within(menu).getByRole("menuitemradio", { name: "默认" }));
    await waitFor(() => expect(patches(calls)).toHaveLength(1));
    expect(patches(calls)[0]!.body).toEqual({
      model: null,
      expected_version: 2,
    });
  });

  it("模型菜单：Runtime 没有上报模型时只有「默认」并说明", async () => {
    const user = userEvent.setup();
    open("/o/o1/chat/c1", {
      summary: conv({
        effective: {
          runtime: "pi",
          model: "default",
          approval_mode: "FOLLOW_RUNTIME",
        },
        overrides: {
          runtime: "pi",
          model: null,
          approval_mode: null,
          version: 1,
        },
      }),
    });
    await user.click(await runtimeButton("模型：默认"));
    const menu = await screen.findByRole("menu");
    expect(within(menu).getAllByRole("menuitemradio")).toHaveLength(1);
    expect(
      within(menu).getByText("这个 Runtime 没有上报模型列表"),
    ).toBeInTheDocument();
  });

  it("权限菜单：四档各有说明，当前 Runtime 不支持的档位禁用并提示", async () => {
    const user = userEvent.setup();
    const calls = open("/o/o1/chat/c1", {
      summary: conv({
        effective: {
          runtime: "pi",
          model: "default",
          approval_mode: "FOLLOW_RUNTIME",
        },
        overrides: {
          runtime: "pi",
          model: null,
          approval_mode: null,
          version: 1,
        },
      }),
    });
    await user.click(await runtimeButton("权限模式：跟随 Runtime"));
    const menu = await screen.findByRole("menu", { name: "本对话的权限模式" });
    expect(
      within(menu).getByText("Agent 默认：跟随 Runtime"),
    ).toBeInTheDocument();
    const modes = within(menu).getAllByRole("menuitemradio");
    expect(modes).toHaveLength(4);
    expect(modes[0]).toHaveTextContent("沿用 Runtime 现有的默认权限");
    for (const item of modes.slice(1)) {
      expect(item).toHaveAttribute("aria-disabled", "true");
      expect(item).toHaveTextContent("这台电脑上的 pi 不支持");
    }
    await user.click(within(menu).getByRole("menuitemradio", { name: /只读/ }));
    expect(patches(calls)).toHaveLength(0);
  });

  it("权限菜单：完全访问可选，请求体带 approval_mode；选 Agent 默认值等于恢复（null）", async () => {
    const user = userEvent.setup();
    const calls = open("/o/o1/chat/c1");
    await user.click(await runtimeButton("权限模式：跟随 Runtime"));
    const menu = await screen.findByRole("menu");
    expect(
      within(menu).getByRole("menuitemradio", { name: /完全访问/ }),
    ).not.toHaveAttribute("aria-disabled");
    await user.click(
      within(menu).getByRole("menuitemradio", { name: /完全访问/ }),
    );
    await waitFor(() => expect(patches(calls)).toHaveLength(1));
    expect(patches(calls)[0]!.body).toEqual({
      approval_mode: "FULL_ACCESS",
      expected_version: 0,
    });
  });

  it("409 VERSION_CONFLICT：提示「设置已被他人修改，已刷新」并刷新摘要", async () => {
    const user = userEvent.setup();
    const calls = open("/o/o1/chat/c1", {
      patch: () => apiError(409, "VERSION_CONFLICT"),
    });
    await user.click(await runtimeButton("Runtime：Codex"));
    const before = gets(calls, DETAIL).length;
    await user.click(
      within(await screen.findByRole("menu")).getByRole("menuitemradio", {
        name: "Claude Code",
      }),
    );
    expect(
      await screen.findByText("设置已被他人修改，已刷新"),
    ).toBeInTheDocument();
    await waitFor(() =>
      expect(gets(calls, DETAIL).length).toBeGreaterThan(before),
    );
  });

  it("422 RUNTIME_NOT_AVAILABLE：提示并重新读取可选项；MODEL_NOT_AVAILABLE 同样", async () => {
    const user = userEvent.setup();
    let code = "RUNTIME_NOT_AVAILABLE";
    const calls = open("/o/o1/chat/c1", {
      patch: () => apiError(422, code),
    });
    await user.click(await runtimeButton("Runtime：Codex"));
    const before = gets(calls, OPTIONS_PATH).length;
    await user.click(
      within(await screen.findByRole("menu")).getByRole("menuitemradio", {
        name: "Claude Code",
      }),
    );
    expect(
      await screen.findByText("这个选项已不可用，已刷新可选项"),
    ).toBeInTheDocument();
    await waitFor(() =>
      expect(gets(calls, OPTIONS_PATH).length).toBeGreaterThan(before),
    );
    code = "MODEL_NOT_AVAILABLE";
    const mid = gets(calls, OPTIONS_PATH).length;
    await user.click(await runtimeButton("模型：默认"));
    await user.click(
      within(await screen.findByRole("menu")).getByRole("menuitemradio", {
        name: "GPT 5.1",
      }),
    );
    await waitFor(() => expect(patches(calls)).toHaveLength(2));
    await waitFor(() =>
      expect(gets(calls, OPTIONS_PATH).length).toBeGreaterThan(mid),
    );
  });

  it("其他错误沿用对话的通用错误文案", async () => {
    const user = userEvent.setup();
    open("/o/o1/chat/c1", {
      patch: () => apiError(409, "CONVERSATION_CLOSED"),
    });
    await user.click(await runtimeButton("权限模式：跟随 Runtime"));
    await user.click(
      within(await screen.findByRole("menu")).getByRole("menuitemradio", {
        name: /只读/,
      }),
    );
    expect(
      await screen.findByText("会话已完成，重新打开后才能继续"),
    ).toBeInTheDocument();
  });

  it("运行中：三个选择器没有额外页脚，仍可选择选项", async () => {
    const user = userEvent.setup();
    open("/o/o1/chat/c1", {
      summary: conv({
        status: "WORKING",
        run: {
          id: "r1",
          state: "RUNNING",
          wait_reason: "",
          started_at: "2026-10-29T08:00:00Z",
          finished_at: null,
        },
      }),
    });
    for (const name of [
      "Runtime：Codex",
      "模型：默认",
      "权限模式：跟随 Runtime",
    ]) {
      await user.click(await runtimeButton(name));
      const menu = within(await screen.findByRole("menu"));
      expect(menu.queryByRole("note")).not.toBeInTheDocument();
      expect(menu.queryByRole("separator")).not.toBeInTheDocument();
      expect(menu.queryByRole("menuitem")).not.toBeInTheDocument();
      expect(menu.getAllByRole("menuitemradio").length).toBeGreaterThan(0);
      await user.keyboard("{Escape}");
    }
  });

  it("没有发消息权限（可选项接口 403）：三个控件只读，只展示当前值", async () => {
    const user = userEvent.setup();
    const calls = open("/o/o1/chat/c1", {
      options: () => apiError(403, "ACTION_FORBIDDEN"),
    });
    const runtime = await runtimeButton("Runtime：Codex");
    await waitFor(() => expect(runtime).toBeDisabled());
    expect(screen.getByRole("button", { name: "模型：默认" })).toBeDisabled();
    expect(
      screen.getByRole("button", { name: "权限模式：跟随 Runtime" }),
    ).toBeDisabled();
    await user.click(runtime);
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    expect(patches(calls)).toHaveLength(0);
  });

  it("对话已完成：控件只读，也不请求可选项", async () => {
    const calls = open("/o/o1/chat/c1", {
      summary: conv({ lifecycle: "COMPLETED" }),
    });
    const runtime = await runtimeButton("Runtime：Codex");
    expect(runtime).toBeDisabled();
    expect(gets(calls, OPTIONS_PATH)).toHaveLength(0);
  });

  it("可选项加载失败：弹出层显示失败与重试，选项不可点", async () => {
    const user = userEvent.setup();
    const calls = open("/o/o1/chat/c1", {
      options: () => apiError(500, "INTERNAL_ERROR"),
    });
    await user.click(await runtimeButton("Runtime：Codex"));
    const menu = await screen.findByRole("menu");
    expect(await within(menu).findByText("可选项加载失败")).toBeVisible();
    expect(
      within(menu).getByRole("menuitemradio", { name: "Codex" }),
    ).toHaveAttribute("aria-checked", "true");
    const before = gets(calls, OPTIONS_PATH).length;
    await user.click(within(menu).getByRole("button", { name: "重试" }));
    await waitFor(() =>
      expect(gets(calls, OPTIONS_PATH).length).toBeGreaterThan(before),
    );
  });

  it("电脑离线：提示可选项来自最近一次上报", async () => {
    const user = userEvent.setup();
    open("/o/o1/chat/c1", {
      options: () => json(200, { ...OPTIONS, computer_online: false }),
    });
    await user.click(await runtimeButton("Runtime：Codex"));
    expect(
      await within(await screen.findByRole("menu")).findByText(
        "电脑当前离线，可选项来自它最近一次上报",
      ),
    ).toBeVisible();
  });

  it("三个菜单不提供设置跳转，页面设置入口仍可打开 Agent 设置", async () => {
    const user = userEvent.setup();
    open("/o/o1/chat/c1");
    for (const name of [
      "Runtime：Codex",
      "模型：默认",
      "权限模式：跟随 Runtime",
    ]) {
      await user.click(await runtimeButton(name));
      const menu = within(await screen.findByRole("menu"));
      expect(menu.queryByText("修改 Agent 默认设置")).not.toBeInTheDocument();
      expect(menu.queryByText("恢复 Agent 默认")).not.toBeInTheDocument();
      expect(menu.queryByText("从下一条消息起生效")).not.toBeInTheDocument();
      await user.keyboard("{Escape}");
    }
    await user.click(screen.getByRole("button", { name: "设置" }));
    expect(
      await screen.findByRole("complementary", { name: /的设置$/ }),
    ).toBeInTheDocument();
  });

  it("小队对话不显示这三个控件", async () => {
    const calls = open("/o/o1/chat/c1", {
      summary: conv({ crew_id: "k1", effective: null, agent_defaults: null }),
      extra: (call) =>
        call.method === "GET" && call.path === `${ORG}/spaces/s1/crews/k1`
          ? json(200, {
              id: "k1",
              name: "产品小队",
              members: [],
              leader: null,
            })
          : undefined,
    });
    await screen.findByRole("textbox", { name: "输入消息" });
    expect(screen.queryByRole("button", { name: /^Runtime：/ })).toBeNull();
    expect(screen.queryByRole("button", { name: /^模型：/ })).toBeNull();
    expect(screen.queryByRole("button", { name: /^权限模式：/ })).toBeNull();
    expect(gets(calls, OPTIONS_PATH)).toHaveLength(0);
  });

  it("覆盖修改写的系统消息（英文）原样显示在时间线里", async () => {
    const body =
      "lin changed this conversation's settings: Runtime set to claude-code. The change takes effect from the next message; a run already in progress is not affected.";
    open("/o/o1/chat/c1", {
      extra: (call) =>
        call.method === "GET" && call.path === `${DETAIL}/messages`
          ? json(200, {
              messages: [message(2, { author_kind: "SYSTEM", body })],
              next_cursor: 0,
            })
          : undefined,
    });
    expect(await screen.findByText(body)).toBeInTheDocument();
  });

  it("页面上不露出文案键（三个弹出层逐个检查）", async () => {
    const user = userEvent.setup();
    open("/o/o1/chat/c1", {
      summary: conv({
        overrides: {
          runtime: "claude-code",
          model: "sonnet",
          approval_mode: "READ_ONLY",
          version: 4,
        },
        effective: {
          runtime: "claude-code",
          model: "sonnet",
          approval_mode: "READ_ONLY",
        },
      }),
    });
    for (const name of [
      "Runtime：Claude Code（本对话）",
      "模型：Sonnet（本对话）",
      "权限模式：只读（本对话）",
    ]) {
      await user.click(await runtimeButton(name));
      await screen.findByRole("menu");
      expect(document.body.textContent).not.toMatch(
        /chat\.overrides|agents\.sections|chat\.composer/,
      );
      await user.keyboard("{Escape}");
    }
  });
});

describe("新对话（还没有会话 id）", () => {
  const A2_PATH = `${ORG}/spaces/s1/agents/a2`;
  const tool = device("d1", {
    runtimes: [
      {
        ...device("d1").runtimes[0]!,
        provider: "codex",
        models: [{ id: "gpt-5.1", display_name: "GPT 5.1", is_default: true }],
        approval_modes: ALL,
      },
      {
        ...device("d1").runtimes[0]!,
        provider: "claude-code",
        models: [{ id: "sonnet", display_name: "Sonnet", is_default: false }],
        approval_modes: ALL,
      },
    ],
  });

  function openNew(options: {
    patch?: (call: Call) => Response;
    devices?: () => Response;
  }) {
    return renderSettings(
      "/o/o1/chat?agent=a2",
      chatBase((call) => {
        if (call.method === "POST" && call.path === `${ORG}/conversations`)
          return json(201, {
            ...summary("new"),
            id: (call.body as { id: string }).id,
          });
        const overrides = /\/conversations\/([^/]+)\/overrides$/.exec(
          call.path,
        );
        if (call.method === "PATCH" && overrides)
          return (
            options.patch?.(call) ??
            json(
              200,
              summary(overrides[1]!, {
                overrides: {
                  runtime: "claude-code",
                  model: null,
                  approval_mode: "READ_ONLY",
                  version: 1,
                },
              }),
            )
          );
        const posted = /\/conversations\/([^/]+)\/messages$/.exec(call.path);
        if (call.method === "POST" && posted)
          return json(
            201,
            message(1, { conversation_id: posted[1]!, body: "你好" }),
          );
        if (posted) return json(200, { messages: [], next_cursor: 0 });
        if (call.method === "GET" && call.path === `${ORG}/devices`)
          return (
            options.devices?.() ??
            json(200, { devices: [tool], next_cursor: null })
          );
        if (call.method === "GET" && call.path === A2_PATH)
          return json(200, agent("a2", { name: "阿虎" }));
        const detail = /\/conversations\/([^/]+)$/.exec(call.path);
        if (detail && call.path !== `${ORG}/conversations`)
          return json(200, summary(detail[1]!, { agent_id: "a2" }));
        if (call.path === `${ORG}/conversations`)
          return json(200, { conversations: [], next_cursor: null });
        return undefined;
      }),
    );
  }

  const order = (calls: Call[]) =>
    calls
      .filter((c) => c.method !== "GET")
      .map((c) => `${c.method} ${c.path.replace(/new|[0-9a-f-]{36}/, "{id}")}`);

  it("选择先暂存，按 create → PATCH overrides → 发送消息的顺序请求", async () => {
    const user = userEvent.setup();
    const calls = openNew({});
    await user.click(await runtimeButton("Runtime：Codex"));
    await user.click(
      within(await screen.findByRole("menu")).getByRole("menuitemradio", {
        name: "Claude Code",
      }),
    );
    // 暂存：按钮标出「本对话」，此时还没有任何写请求
    await user.click(await runtimeButton("权限模式：跟随 Runtime"));
    await user.click(
      within(await screen.findByRole("menu")).getByRole("menuitemradio", {
        name: /只读/,
      }),
    );
    await runtimeButton("Runtime：Claude Code（本对话）");
    await runtimeButton("权限模式：只读（本对话）");
    expect(calls.filter((c) => c.method !== "GET")).toHaveLength(0);

    await user.type(
      screen.getByRole("textbox", { name: "输入消息" }),
      "你好{Enter}",
    );
    await waitFor(() =>
      expect(
        calls.some((c) => c.method === "POST" && c.path.endsWith("/messages")),
      ).toBe(true),
    );
    expect(order(calls)).toEqual([
      "POST /v1/organizations/o1/conversations",
      "PATCH /v1/organizations/o1/conversations/{id}/overrides",
      "POST /v1/organizations/o1/conversations/{id}/messages",
    ]);
    const patch = calls.find((c) => c.method === "PATCH")!;
    expect(patch.body).toEqual({
      runtime: "claude-code",
      approval_mode: "READ_ONLY",
      expected_version: 0,
    });
  });

  it("没有选择时不发 PATCH", async () => {
    const user = userEvent.setup();
    const calls = openNew({});
    await user.type(
      await screen.findByRole("textbox", { name: "输入消息" }),
      "你好{Enter}",
    );
    await waitFor(() =>
      expect(
        calls.some((c) => c.method === "POST" && c.path.endsWith("/messages")),
      ).toBe(true),
    );
    expect(calls.some((c) => c.method === "PATCH")).toBe(false);
  });

  it("PATCH 失败：不发送消息，toast 说明原因，草稿保留；修改后可再发", async () => {
    const user = userEvent.setup();
    let failing = true;
    const calls = openNew({
      patch: () =>
        failing
          ? apiError(422, "RUNTIME_NOT_AVAILABLE")
          : json(
              200,
              summary("new", {
                overrides: {
                  runtime: null,
                  model: null,
                  approval_mode: "READ_ONLY",
                  version: 1,
                },
              }),
            ),
    });
    await user.click(await runtimeButton("权限模式：跟随 Runtime"));
    await user.click(
      within(await screen.findByRole("menu")).getByRole("menuitemradio", {
        name: /只读/,
      }),
    );
    const box = await screen.findByRole("textbox", { name: "输入消息" });
    await user.type(box, "你好{Enter}");
    expect(
      await screen.findByText(
        "对话设置没有保存，消息未发送：这个选项已不可用，已刷新可选项",
      ),
    ).toBeInTheDocument();
    expect(box).toHaveValue("你好");
    expect(
      calls.some((c) => c.method === "POST" && c.path.endsWith("/messages")),
    ).toBe(false);
    // 对话已建好；重试时再次 PATCH（仍是 expected_version 0），成功后才发消息
    failing = false;
    await user.type(box, "{Enter}");
    await waitFor(() =>
      expect(
        calls.some((c) => c.method === "POST" && c.path.endsWith("/messages")),
      ).toBe(true),
    );
    const sent = calls.filter((c) => c.method === "PATCH");
    expect(sent).toHaveLength(2);
    expect(sent[1]!.body).toEqual({
      approval_mode: "READ_ONLY",
      expected_version: 0,
    });
    expect(
      calls.filter(
        (c) => c.method === "POST" && c.path === `${ORG}/conversations`,
      ),
    ).toHaveLength(1);
  });

  it("看不到 Agent 绑定的电脑（读不到可选项）：控件只读，只展示 Agent 默认值", async () => {
    const calls = openNew({
      devices: () => json(200, { devices: [], next_cursor: null }),
    });
    const runtime = await runtimeButton("Runtime：Codex");
    await waitFor(() => expect(runtime).toBeDisabled());
    expect(calls.filter((c) => c.method !== "GET")).toHaveLength(0);
  });
});

describe("OverrideControls（展示组件）", () => {
  const defaults = {
    runtime: "codex",
    model: "default",
    approval_mode: "FOLLOW_RUNTIME" as const,
  };
  const selection = { runtime: null, model: null, approval_mode: null };

  function show(props: Partial<Parameters<typeof OverrideControls>[0]>) {
    const onChange = vi.fn();
    render(
      <I18nProvider initialLocale="zh-CN">
        <ToastProvider>
          <OverrideControls
            defaults={defaults}
            selection={selection}
            effective={defaults}
            options={OPTIONS}
            optionsStatus="ready"
            readOnly={false}
            pending={false}
            onChange={onChange}
            {...props}
          />
        </ToastProvider>
      </I18nProvider>,
    );
    return onChange;
  }

  it("修改进行中：三个按钮都禁用，不能连发", () => {
    const onChange = show({ pending: true });
    for (const button of screen.getAllByRole("button")) {
      expect(button).toBeDisabled();
    }
    expect(onChange).not.toHaveBeenCalled();
  });

  it("刷新过程中禁用重复刷新，仍保留当前模型的选中状态", async () => {
    const onRefresh = vi.fn();
    show({ optionsRefreshing: true, onRetryOptions: onRefresh });
    await userEvent.click(screen.getByRole("button", { name: "模型：默认" }));
    const menu = await screen.findByRole("menu");
    const refresh = within(menu).getByRole("button", { name: "刷新模型列表" });
    expect(refresh).toBeDisabled();
    expect(refresh).toHaveAttribute("aria-busy", "true");
    expect(
      within(menu).getByRole("menuitemradio", { name: "默认" }),
    ).toHaveAttribute("aria-checked", "true");
    await userEvent.click(refresh);
    expect(onRefresh).not.toHaveBeenCalled();
  });

  it("只读：按钮禁用，悬停说明原因", () => {
    show({ readOnly: true });
    for (const button of screen.getAllByRole("button"))
      expect(button).toBeDisabled();
  });

  it("英文界面：文案齐全，不露出文案键", async () => {
    const user = userEvent.setup();
    render(
      <I18nProvider initialLocale="en-US">
        <ToastProvider>
          <OverrideControls
            defaults={defaults}
            selection={{ ...selection, approval_mode: "READ_ONLY" }}
            effective={{ ...defaults, approval_mode: "READ_ONLY" }}
            options={OPTIONS}
            optionsStatus="ready"
            readOnly={false}
            pending={false}
            onChange={() => undefined}
          />
        </ToastProvider>
      </I18nProvider>,
    );
    await user.click(
      screen.getByRole("button", {
        name: "Permission mode: Read-only (this conversation)",
      }),
    );
    const menu = await screen.findByRole("menu");
    expect(
      within(menu).getByText("Agent default: Follow runtime"),
    ).toBeVisible();
    expect(within(menu).getByText("This conversation")).toBeVisible();
    expect(
      within(menu).queryByRole("menuitem", { name: "Restore agent default" }),
    ).not.toBeInTheDocument();
    expect(
      within(menu).queryByRole("menuitem", {
        name: "Edit agent default settings",
      }),
    ).not.toBeInTheDocument();
    expect(within(menu).queryByRole("note")).not.toBeInTheDocument();
    expect(document.body.textContent).not.toMatch(/chat\.overrides/);
  });
});

describe("覆盖换算（纯函数）", () => {
  const defaults = {
    runtime: "codex",
    model: "gpt-5.1",
    approval_mode: "READ_ONLY" as const,
    reasoning_effort: "DEFAULT" as const,
  };
  const none = {
    runtime: null,
    model: null,
    approval_mode: null,
    reasoning_effort: null,
  };

  it("有效值与后端规则一致：只覆盖不同的 Runtime 时模型为 default", () => {
    expect(effectiveOf(defaults, none)).toEqual(defaults);
    expect(effectiveOf(defaults, { ...none, runtime: "claude-code" })).toEqual({
      runtime: "claude-code",
      model: "default",
      approval_mode: "READ_ONLY",
      reasoning_effort: "DEFAULT",
    });
    expect(effectiveOf(defaults, { ...none, runtime: "codex" }).model).toBe(
      "gpt-5.1",
    );
    expect(agentDefaultModel(defaults, { ...none, runtime: "pi" })).toBe(
      "default",
    );
  });

  it("选 Agent 默认值就是恢复（null）；换 Runtime 时一并清除旧模型覆盖", () => {
    expect(patchFor("runtime", "codex", defaults, none)).toEqual({
      runtime: null,
    });
    expect(
      patchFor("runtime", "claude-code", defaults, { ...none, model: "x" }),
    ).toEqual({ runtime: "claude-code", model: null });
    expect(patchFor("approval_mode", "READ_ONLY", defaults, none)).toEqual({
      approval_mode: null,
    });
    expect(patchFor("model", "gpt-5.1", defaults, none)).toEqual({
      model: null,
    });
    expect(patchFor("model", "o3", defaults, none)).toEqual({ model: "o3" });
  });

  it("没有变化时是 no-op；请求体只带发生变化的字段", () => {
    expect(isNoop({ runtime: null }, none)).toBe(true);
    expect(isNoop({ model: "o3" }, none)).toBe(false);
    expect(requestBody({ approval_mode: null }, 5)).toEqual({
      approval_mode: null,
      expected_version: 5,
    });
  });

  it("切换模型或 Runtime 清除旧深度，单独重置深度不改模型", () => {
    const selection = {
      ...none,
      model: "gpt-5.1",
      reasoning_effort: "HIGH" as const,
    };
    expect(patchFor("runtime", "pi", defaults, selection)).toEqual({
      runtime: "pi",
      model: null,
      reasoning_effort: null,
    });
    expect(patchFor("model", "o3", defaults, selection)).toEqual({
      model: "o3",
      reasoning_effort: null,
    });
    expect(
      patchFor("reasoning_effort", "DEFAULT", defaults, selection),
    ).toEqual({ reasoning_effort: null });
    expect(requestBody({ reasoning_effort: "HIGH" }, 8)).toEqual({
      reasoning_effort: "HIGH",
      expected_version: 8,
    });
  });
});

describe("Agent 设置页的权限模式默认", () => {
  it("四档下拉：保存带 approval_mode，电脑不支持的档位置灰", async () => {
    const user = userEvent.setup();
    const record: Call[] = [];
    renderSettings(
      "/o/o1/agents?agentSettings=a1",
      chatBase((call) => {
        if (call.method === "GET" && call.path === `${ORG}/agent-directory`)
          return json(200, {
            entries: [entry("a1", { name: "熊孩子" })],
            archived_count: 0,
            truncated: false,
          });
        if (call.method === "GET" && call.path === `${ORG}/devices`)
          return json(200, {
            devices: [
              device("d1", {
                runtimes: [
                  {
                    ...device("d1").runtimes[0]!,
                    approval_modes: ["FOLLOW_RUNTIME", "READ_ONLY"],
                  },
                ],
              }),
            ],
            next_cursor: null,
          });
        if (call.method === "GET" && call.path === `${ORG}/spaces/s1/agents/a1`)
          return json(200, agent("a1", { name: "熊孩子" }));
        if (call.method === "GET" && call.path.endsWith("/a1/env-vars"))
          return json(200, {
            env_vars: [],
            can_view_values: true,
            secrets_available: true,
          });
        if (call.method === "GET" && call.path.endsWith("/a1/repos"))
          return json(200, { repos: [] });
        if (
          call.method === "POST" &&
          call.path.endsWith("/a1/config-versions")
        ) {
          record.push(call);
          return json(201, agent("a1", { name: "熊孩子", version: 6 }));
        }
        return undefined;
      }),
    );
    const drawer = await screen.findByRole("complementary", {
      name: "熊孩子 的设置",
    });
    await user.click(
      within(drawer).getByRole("button", { name: /电脑 & Runtime/ }),
    );
    await user.click(
      await within(drawer).findByRole("button", { name: "权限模式默认" }),
    );
    const list = await screen.findByRole("listbox", { name: "权限模式默认" });
    const options = within(list).getAllByRole("option");
    expect(options.map((item) => item.getAttribute("aria-disabled"))).toEqual([
      null,
      null,
      "true",
      "true",
    ]);
    expect(options[2]).toHaveTextContent("这台电脑上的 Codex 不支持");
    await user.click(within(list).getByRole("option", { name: /只读/ }));
    await waitFor(() => expect(record).toHaveLength(1));
    expect(record[0]!.body).toMatchObject({ approval_mode: "READ_ONLY" });
  });
});
