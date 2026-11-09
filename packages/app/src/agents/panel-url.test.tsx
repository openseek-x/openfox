import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {
  MemoryRouter,
  Route,
  Routes,
  useLocation,
  useNavigationType,
} from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import {
  createApi,
  HttpClient,
  type AgentRunSummary,
  type Run,
  type RunEvent,
} from "@openfox/api";
import { I18nProvider } from "@openfox/i18n";
import { ToastProvider } from "@openfox/ui";

import { ApiProvider } from "../context";
import { json } from "../settings/testing";
import { agent, entry, TS } from "./fixtures";
import { readAgentPanel, writeAgentPanel } from "./params";
import { AgentSettingsHost } from "./settings/AgentSettingsDrawer";

// Agent 面板展开 / 页签写入地址（agentPanel=full、agentTab），兼容旧的 ?agentTab=runs；
// 运行历史「查看记录」弹窗（agentRun=<runId>）：打开、全部展开、复制全部、关闭，加载 / 空 / 403 / 404 / 失败、分页加载。

const ORG = "/v1/organizations/o1";
const BASE = "/o/o1/agents?agentSettings=a1";

describe("readAgentPanel / writeAgentPanel", () => {
  const read = (search: string) => readAgentPanel(new URLSearchParams(search));
  const write = (
    search: string,
    state: Parameters<typeof writeAgentPanel>[1],
  ) => writeAgentPanel(new URLSearchParams(search), state).toString();

  it("读：默认收起在设置页签；agentPanel=full 展开；页签只在展开时生效", () => {
    expect(read("agentSettings=a1")).toEqual({
      expanded: false,
      tab: "settings",
      runId: null,
    });
    expect(read("agentPanel=full")).toEqual({
      expanded: true,
      tab: "settings",
      runId: null,
    });
    expect(read("agentPanel=full&agentTab=tasks").tab).toBe("tasks");
    // 未知页签按设置处理
    expect(read("agentPanel=full&agentTab=bogus").tab).toBe("settings");
    expect(read("agentPanel=other").expanded).toBe(false);
  });

  it("读：旧链接 ?agentTab=runs（没有 agentPanel）等同展开 + 运行历史", () => {
    expect(read("agentSettings=a1&agentTab=runs")).toEqual({
      expanded: true,
      tab: "runs",
      runId: null,
    });
  });

  it("读：agentRun 只在展开且停在运行历史时生效", () => {
    expect(read("agentPanel=full&agentTab=runs&agentRun=r1").runId).toBe("r1");
    expect(read("agentPanel=full&agentTab=tasks&agentRun=r1").runId).toBeNull();
    expect(read("agentRun=r1").runId).toBeNull();
  });

  it("写：展开写 agentPanel=full，设置页签不写 agentTab；收起去掉面板参数，保留其他参数", () => {
    expect(write("agentSettings=a1", { expanded: true })).toBe(
      "agentSettings=a1&agentPanel=full",
    );
    expect(write("agentSettings=a1", { expanded: true, tab: "runs" })).toBe(
      "agentSettings=a1&agentPanel=full&agentTab=runs",
    );
    expect(
      write("agentSettings=a1&agentTab=runs", {
        expanded: true,
        tab: "runs",
        runId: "r1",
      }),
    ).toBe("agentSettings=a1&agentPanel=full&agentTab=runs&agentRun=r1");
    expect(
      write("x=1&agentSettings=a1&agentPanel=full&agentTab=runs&agentRun=r1", {
        expanded: false,
      }),
    ).toBe("x=1&agentSettings=a1");
    // 非运行历史页签不写 agentRun
    expect(write("", { expanded: true, tab: "tasks", runId: "r1" })).toBe(
      "agentPanel=full&agentTab=tasks",
    );
  });
});

function runSummary(id: string, title: string | null): AgentRunSummary {
  return {
    id,
    space_id: "s1",
    conversation_id: "c1",
    conversation_title: title,
    task_id: null,
    task_display_id: "WEB-12",
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
    duration_ms: 1000,
    usage: null,
  };
}

function runDetail(): Run {
  return {
    id: "r1",
    organization_id: "o1",
    space_id: "s1",
    conversation_id: "c1",
    agent_id: "a1",
    computer_id: "d1",
    grant_id: "g1",
    config_version: 1,
    grant_version: 1,
    actor_id: "u1",
    state: "SUCCEEDED",
    generation: 1,
    reconciliation_required: false,
    reason: "",
    version: 3,
    created_at: TS,
    updated_at: TS,
    failure_code: "",
    started_at: TS,
    usage: null,
    duration_ms: 1000,
  };
}

let seq = 0;
function execEvent(id: string, command: string, output: string): RunEvent {
  seq++;
  return {
    id: `ev-${seq}`,
    organization_id: "o1",
    run_id: "r1",
    generation: 1,
    sequence: seq,
    stream_seq: seq,
    type: "step",
    safe_payload: {
      data: {
        schema: 1,
        step: {
          id,
          kind: "exec",
          state: "done",
          command,
          output,
          exit_code: 0,
        },
      },
    },
    occurred_at: TS,
    received_at: TS,
  };
}

const notFound = () =>
  json(404, { error: { code: "NOT_FOUND", message: "x" } });

function Probe() {
  const location = useLocation();
  const type = useNavigationType();
  return (
    <>
      <p data-testid="location">{`${location.pathname}${location.search}`}</p>
      <p data-testid="nav-type">{type}</p>
    </>
  );
}

const where = () => screen.getByTestId("location").textContent;
const navType = () => screen.getByTestId("nav-type").textContent;

interface HostOptions {
  /** 事件页：按 after 游标返回；默认一页两条命令 */
  events?: (after: string) => Response | Promise<Response>;
  run?: () => Response;
}

function renderHost(path: string, options: HostOptions = {}) {
  const calls: string[] = [];
  const transport = vi.fn((input: RequestInfo | URL) => {
    const url = new URL(String(input), "http://localhost");
    const p = url.pathname.replace(/^\/api/, "");
    calls.push(`${p}${url.search}`);
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
      return Promise.resolve(
        json(200, { runs: [runSummary("r1", "修复测试")], next_cursor: null }),
      );
    if (p === `${ORG}/runs/r1`)
      return Promise.resolve(options.run?.() ?? json(200, runDetail()));
    if (p === `${ORG}/runs/r1/events`) {
      const after = url.searchParams.get("after") ?? "0";
      if (options.events) return Promise.resolve(options.events(after));
      return Promise.resolve(
        json(200, {
          events: [
            execEvent("x1", "pnpm test", "全部通过"),
            execEvent("x2", "git status", "## main"),
          ],
        }),
      );
    }
    // 还没有结果（404 → null）
    if (p === `${ORG}/runs/r1/result`) return Promise.resolve(notFound());
    if (p === `${ORG}/runs/r1/artifacts`)
      return Promise.resolve(json(200, { artifacts: [] }));
    return Promise.resolve(notFound());
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
                  element={
                    <>
                      <AgentSettingsHost orgId="o1" />
                      <Probe />
                    </>
                  }
                />
              </Routes>
            </ToastProvider>
          </QueryClientProvider>
        </ApiProvider>
      </I18nProvider>
    </MemoryRouter>,
  );
  return { calls };
}

const drawer = () =>
  screen.findByRole("complementary", { name: "熊孩子 的设置" });
const transcript = () => screen.findByRole("dialog", { name: /修复测试/ });

afterEach(() => {
  localStorage.clear();
  vi.restoreAllMocks();
});

describe("Agent 面板状态写入地址", () => {
  it("展开、切页签、收起都用 replace 写入；收起去掉 agentPanel 和 agentTab", async () => {
    const user = userEvent.setup();
    renderHost(`${BASE}&x=1`);
    const panel = await drawer();
    expect(within(panel).queryByRole("tab")).toBeNull();
    await user.click(within(panel).getByRole("button", { name: "展开" }));
    expect(where()).toBe(`${BASE}&x=1&agentPanel=full`);
    expect(navType()).toBe("REPLACE");
    await user.click(within(panel).getByRole("tab", { name: "任务" }));
    expect(where()).toBe(`${BASE}&x=1&agentPanel=full&agentTab=tasks`);
    await user.click(within(panel).getByRole("tab", { name: "运行历史" }));
    expect(where()).toBe(`${BASE}&x=1&agentPanel=full&agentTab=runs`);
    await user.click(within(panel).getByRole("tab", { name: "设置" }));
    expect(where()).toBe(`${BASE}&x=1&agentPanel=full`);
    await user.click(within(panel).getByRole("button", { name: "收起" }));
    expect(where()).toBe(`${BASE}&x=1`);
    expect(navType()).toBe("REPLACE");
  });

  it("刷新（直接用地址打开）恢复展开和页签", async () => {
    renderHost(`${BASE}&agentPanel=full&agentTab=tasks`);
    const panel = await drawer();
    expect(within(panel).getByRole("tab", { name: "任务" })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(
      within(panel).getByRole("button", { name: "收起" }),
    ).toBeInTheDocument();
  });

  it("旧链接 ?agentTab=runs：展开在运行历史；之后写入的是新格式", async () => {
    const user = userEvent.setup();
    renderHost(`${BASE}&agentTab=runs`);
    const panel = await drawer();
    expect(
      within(panel).getByRole("tab", { name: "运行历史" }),
    ).toHaveAttribute("aria-selected", "true");
    await user.click(within(panel).getByRole("tab", { name: "任务" }));
    expect(where()).toBe(`${BASE}&agentPanel=full&agentTab=tasks`);
  });

  it("关闭抽屉去掉全部面板参数", async () => {
    const user = userEvent.setup();
    renderHost(
      `/o/o1/agents?x=1&agentSettings=a1&agentPanel=full&agentTab=runs`,
    );
    const panel = await drawer();
    await user.click(within(panel).getByRole("button", { name: "关闭" }));
    await waitFor(() => expect(where()).toBe("/o/o1/agents?x=1"));
  });
});

const RUNS = `${BASE}&agentPanel=full&agentTab=runs`;

describe("运行记录弹窗", () => {
  it("点行打开弹窗（写 agentRun）；标题、状态；全部展开；复制全部；关闭回到运行历史", async () => {
    const user = userEvent.setup();
    const writeText = vi.fn((_: string) => Promise.resolve());
    Object.defineProperty(navigator, "clipboard", {
      value: { writeText },
      configurable: true,
    });
    renderHost(RUNS);
    const panel = await drawer();
    await user.click(
      await within(panel).findByRole("button", { name: "查看记录：修复测试" }),
    );
    expect(where()).toBe(`${RUNS}&agentRun=r1`);
    expect(navType()).toBe("REPLACE");
    const dialog = await transcript();
    expect(dialog).toHaveTextContent("WEB-12 修复测试");
    // 描述行：状态 · 开始时间
    await waitFor(() =>
      expect(dialog).toHaveAccessibleDescription(/^已完成 · /),
    );
    const headers = await within(dialog).findAllByRole("button", {
      name: /^运行命令/,
    });
    expect(headers).toHaveLength(2);
    for (const header of headers)
      expect(header).toHaveAttribute("aria-expanded", "false");
    await user.click(within(dialog).getByRole("button", { name: "全部展开" }));
    for (const header of within(dialog).getAllByRole("button", {
      name: /^运行命令/,
    }))
      expect(header).toHaveAttribute("aria-expanded", "true");
    expect(within(dialog).getAllByTestId("exec-output")).toHaveLength(2);

    await user.click(within(dialog).getByRole("button", { name: "复制全部" }));
    expect(writeText).toHaveBeenCalledTimes(1);
    const text = writeText.mock.calls[0]![0];
    expect(text).toContain("WEB-12 修复测试");
    expect(text).toContain("pnpm test");
    expect(text).toContain("## main");
    expect(await screen.findByText("已复制执行记录")).toBeInTheDocument();

    // 底部「关闭」按钮（标题栏也有一个关闭图标）
    const closes = within(dialog).getAllByRole("button", { name: "关闭" });
    await user.click(closes[closes.length - 1]!);
    await waitFor(() => expect(where()).toBe(RUNS));
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(await drawer()).toBeInTheDocument();
  });

  it("刷新带 agentRun 的地址直接打开弹窗", async () => {
    renderHost(`${RUNS}&agentRun=r1`);
    expect(await transcript()).toBeInTheDocument();
  });

  it("加载中显示占位；没有事件时显示空状态，全部展开 / 复制全部不可用", async () => {
    let release: (r: Response) => void = () => {};
    renderHost(`${RUNS}&agentRun=r1`, {
      events: () => new Promise<Response>((resolve) => (release = resolve)),
    });
    const dialog = await transcript();
    expect(
      await within(dialog).findByRole("status", { name: "正在加载执行记录" }),
    ).toBeInTheDocument();
    release(json(200, { events: [] }));
    expect(
      await within(dialog).findByText("这次运行还没有执行记录"),
    ).toBeInTheDocument();
    expect(
      within(dialog).getByRole("button", { name: "全部展开" }),
    ).toBeDisabled();
    expect(
      within(dialog).getByRole("button", { name: "复制全部" }),
    ).toBeDisabled();
  });

  it.each([
    [403, "FORBIDDEN", "你没有权限查看这次运行的记录。"],
    [404, "NOT_FOUND", "找不到这次运行的记录，可能已被删除或你没有权限查看。"],
  ])("运行详情返回 %s：显示对应提示", async (status, code, message) => {
    renderHost(`${RUNS}&agentRun=r1`, {
      run: () => json(status, { error: { code, message: "x" } }),
    });
    const dialog = await transcript();
    expect(await within(dialog).findByRole("alert")).toHaveTextContent(message);
    expect(within(dialog).queryByRole("button", { name: "重试" })).toBeNull();
  });

  it("其他错误：显示加载失败并可重试", async () => {
    const user = userEvent.setup();
    let fail = true;
    renderHost(`${RUNS}&agentRun=r1`, {
      events: () =>
        fail
          ? json(500, { error: { code: "INTERNAL", message: "x" } })
          : json(200, { events: [execEvent("x9", "make check", "ok")] }),
    });
    const dialog = await transcript();
    expect(await within(dialog).findByRole("alert")).toHaveTextContent(
      "执行记录加载失败",
    );
    fail = false;
    await user.click(within(dialog).getByRole("button", { name: "重试" }));
    expect(
      await within(dialog).findByRole("button", { name: /make check/ }),
    ).toBeInTheDocument();
  });

  it("分页：每页最多 100 条，点「加载更多记录」再取下一页", async () => {
    const user = userEvent.setup();
    const first = execEvent("p1", "第一页命令", "a");
    const second = execEvent("p2", "第二页命令", "b");
    const { calls } = renderHost(`${RUNS}&agentRun=r1`, {
      events: (after) =>
        after === "0"
          ? json(200, { events: [first], next_cursor: first.stream_seq })
          : json(200, { events: [second] }),
    });
    const dialog = await transcript();
    expect(
      await within(dialog).findByRole("button", { name: /第一页命令/ }),
    ).toBeInTheDocument();
    expect(
      within(dialog).queryByRole("button", { name: /第二页命令/ }),
    ).toBeNull();
    const eventCalls = () => calls.filter((c) => c.includes("/runs/r1/events"));
    expect(eventCalls()).toEqual([`${ORG}/runs/r1/events?after=0&limit=100`]);
    await user.click(
      within(dialog).getByRole("button", { name: "加载更多记录" }),
    );
    expect(
      await within(dialog).findByRole("button", { name: /第二页命令/ }),
    ).toBeInTheDocument();
    expect(eventCalls()[1]).toBe(
      `${ORG}/runs/r1/events?after=${first.stream_seq}&limit=100`,
    );
    expect(
      within(dialog).queryByRole("button", { name: "加载更多记录" }),
    ).toBeNull();
  });
});
