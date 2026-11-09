import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";

import { createApi, HttpClient, type AgentRunSummary } from "@openfox/api";
import { I18nProvider } from "@openfox/i18n";
import { ToastProvider } from "@openfox/ui";

import { ApiProvider } from "../context";
import { json } from "../settings/testing";
import { agent, entry, TS } from "./fixtures";
import { AgentSettingsHost } from "./settings/AgentSettingsDrawer";

// Agent 设置全屏「运行历史」页签：?agentTab=runs 直接打开；行显示状态、会话 / 任务编号、开始时间、耗时、token；
// 点击行尾「打开会话」跳到对应会话并带 ?run= 定位，点击行主区域打开记录弹窗。

const ORG = "/v1/organizations/o1";

function runSummary(
  id: string,
  extra: Partial<AgentRunSummary> = {},
): AgentRunSummary {
  return {
    id,
    space_id: "s1",
    conversation_id: "c1",
    conversation_title: "修复测试",
    task_id: null,
    task_display_id: null,
    agent_id: "a1",
    actor_id: "u1",
    state: "SUCCEEDED",
    wait_reason: "",
    reason: "",
    config_version: 1,
    created_at: TS,
    updated_at: TS,
    finished_at: TS,
    started_at: TS,
    duration_ms: 78_000,
    usage: {
      input: 12_000,
      cached_input: 0,
      cache_write_input: 0,
      output: 500,
      reasoning_output: 0,
    },
    ...extra,
  };
}

function Probe() {
  const location = useLocation();
  return (
    <p data-testid="location">{`${location.pathname}${location.search}`}</p>
  );
}

function renderHost(path: string, runs: AgentRunSummary[]) {
  const transport = vi.fn((input: RequestInfo | URL) => {
    const url = new URL(String(input), "http://localhost");
    const p = url.pathname.replace(/^\/api/, "");
    if (p === `${ORG}/agent-directory`)
      return Promise.resolve(
        json(200, {
          entries: [entry("a1", { name: "熊孩子" })],
          archived_count: 0,
          truncated: false,
        }),
      );
    if (p === `${ORG}/spaces/s1/agents/a1`)
      return Promise.resolve(json(200, agent("a1", { name: "熊孩子" })));
    if (p === `${ORG}/spaces/s1/agents/a1/runs`)
      return Promise.resolve(json(200, { runs, next_cursor: null }));
    return Promise.resolve(
      json(404, { error: { code: "NOT_FOUND", message: "x" } }),
    );
  });
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  render(
    <MemoryRouter initialEntries={[path]}>
      <I18nProvider initialLocale="zh-CN">
        <ApiProvider
          api={createApi(
            new HttpClient("/api", transport as unknown as typeof fetch),
          )}
        >
          <QueryClientProvider client={client}>
            <ToastProvider>
              <Routes>
                <Route
                  path="/o/:orgId/agents"
                  element={<AgentSettingsHost orgId="o1" />}
                />
                <Route
                  path="/o/:orgId/chat/:conversationId"
                  element={<Probe />}
                />
              </Routes>
            </ToastProvider>
          </QueryClientProvider>
        </ApiProvider>
      </I18nProvider>
    </MemoryRouter>,
  );
}

describe("Agent 运行历史页签", () => {
  it("?agentTab=runs 以全屏打开运行历史；行显示状态、任务编号、标题、耗时、token；点击跳到会话并定位 Run", async () => {
    const user = userEvent.setup();
    renderHost("/o/o1/agents?agentSettings=a1&agentTab=runs", [
      runSummary("r1", { task_display_id: "WEB-12" }),
      runSummary("r2", {
        conversation_id: "c2",
        conversation_title: null,
        state: "RUNNING",
        finished_at: null,
        duration_ms: null,
        usage: null,
      }),
      runSummary("r3", {
        conversation_id: "c3",
        conversation_title: "排队的对话",
        state: "QUEUED",
        wait_reason: "AGENT_CHAT_CONCURRENCY",
        started_at: null,
        finished_at: null,
        duration_ms: null,
        usage: null,
      }),
    ]);
    const drawer = await screen.findByRole("complementary", {
      name: "熊孩子 的设置",
    });
    expect(
      within(drawer).getByRole("tab", { name: "运行历史" }),
    ).toHaveAttribute("aria-selected", "true");
    const first = await within(drawer).findByRole("button", {
      name: "查看记录：修复测试",
    });
    expect(first).toHaveTextContent("已完成");
    expect(first).toHaveTextContent("WEB-12");
    expect(first).toHaveTextContent("1 分 18 秒");
    expect(first).toHaveTextContent("12.5K");
    const second = within(drawer).getByRole("button", {
      name: "查看记录：未命名对话",
    });
    expect(second).toHaveTextContent("运行中");
    // 没有耗时和用量时显示「—」
    expect(within(second).getAllByText("—")).toHaveLength(2);
    // 排队原因：聊天并发已满时状态旁提示原因
    const queued = within(drawer).getByRole("button", {
      name: "查看记录：排队的对话",
    });
    expect(within(queued).getByTitle("聊天并发已满")).toHaveTextContent(
      "排队中",
    );
    // 行主区域打开记录弹窗，行尾「打开会话」按钮跳转到会话
    await user.click(
      within(drawer).getByRole("button", { name: "打开会话：修复测试" }),
    );
    expect(screen.getByTestId("location")).toHaveTextContent(
      "/o/o1/chat/c1?run=r1",
    );
  });
});
