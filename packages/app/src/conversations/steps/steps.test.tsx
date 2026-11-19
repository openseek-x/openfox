import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import {
  createApi,
  HttpClient,
  type Run,
  type RunArtifact,
  type RunEvent,
  type RunResult,
  type Step,
} from "@openfox/api";
import { I18nProvider, createTranslator } from "@openfox/i18n";
import { ToastProvider } from "@openfox/ui";

import { ApiProvider, keys } from "../../context";
import { createEventBatcher } from "../../realtime/useOrgEvents";
import { json, type Call } from "../../settings/testing";
import { FileAttachmentCard, ModelErrorCard } from "./cards";
import {
  FAILURE_CODES,
  directRetryDirectory,
  failureMessageKeys,
  isDuplicateRunError,
  formatDuration,
} from "./format";
import { foldRunEvents } from "./model";
import { mergeRunEvents } from "./queries";
import { RunTimeline, RunTimelineView } from "./RunTimeline";
import {
  ReconciliationRow,
  RunStatusRow,
  TurnItemRow,
  waitReasonKey,
} from "./rows";

const ORG = "/v1/organizations/o1";
const TS = "2026-10-29T08:00:00Z";
const zh = createTranslator("zh-CN");
const en = createTranslator("en-US");

let seq = 0;
function stepEvent(
  value: Partial<Step> & Pick<Step, "id" | "kind" | "state">,
  extra: Partial<RunEvent> = {},
): RunEvent {
  seq++;
  return {
    id: `ev-${seq}`,
    organization_id: "o1",
    run_id: "r1",
    generation: 1,
    sequence: seq,
    stream_seq: seq,
    type: "step",
    safe_payload: { data: { schema: 1, step: value } },
    occurred_at: TS,
    received_at: TS,
    ...extra,
  };
}

function legacy(
  type: string,
  text?: string,
  data?: Record<string, unknown>,
): RunEvent {
  seq++;
  return {
    id: `ev-${seq}`,
    organization_id: "o1",
    run_id: "r1",
    generation: 1,
    sequence: seq,
    stream_seq: seq,
    type,
    safe_payload: { text, data },
    occurred_at: TS,
    received_at: TS,
  };
}

function run(extra: Partial<Run> = {}): Run {
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
    state: "RUNNING",
    generation: 1,
    reconciliation_required: false,
    reason: "",
    version: 3,
    created_at: TS,
    updated_at: TS,
    failure_code: "",
    started_at: TS,
    usage: null,
    duration_ms: null,
    ...extra,
  };
}

function Providers({
  children,
  client,
  transport,
}: {
  children: ReactNode;
  client?: QueryClient;
  transport?: typeof fetch;
}) {
  const queryClient =
    client ??
    new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const api = createApi(
    new HttpClient(
      "/api",
      transport ??
        ((() =>
          Promise.reject(new Error("no network"))) as unknown as typeof fetch),
    ),
  );
  return (
    <MemoryRouter>
      <I18nProvider initialLocale="zh-CN">
        <ApiProvider api={api}>
          <QueryClientProvider client={queryClient}>
            <ToastProvider>{children}</ToastProvider>
          </QueryClientProvider>
        </ApiProvider>
      </I18nProvider>
    </MemoryRouter>
  );
}

function renderTurn(events: RunEvent[], terminal = false) {
  const turn = foldRunEvents(events, { terminal });
  return render(
    <Providers>
      {turn.items.map((item) => (
        <TurnItemRow key={item.key} item={item} />
      ))}
    </Providers>,
  );
}

afterEach(() => {
  vi.useRealTimers();
});

describe("formatDuration", () => {
  it("< 10 秒一位小数，10–59 秒取整，≥ 60 秒分秒；英文 xs / xm ys", () => {
    expect(formatDuration(1200, zh)).toBe("1.2 秒");
    expect(formatDuration(3000, zh)).toBe("3 秒");
    expect(formatDuration(0, zh)).toBe("0 秒");
    expect(formatDuration(-5, zh)).toBe("0 秒");
    expect(formatDuration(9960, zh)).toBe("10 秒");
    expect(formatDuration(12_400, zh)).toBe("12 秒");
    expect(formatDuration(59_600, zh)).toBe("1 分 0 秒");
    expect(formatDuration(78_000, zh)).toBe("1 分 18 秒");
    expect(formatDuration(1200, en)).toBe("1.2s");
    expect(formatDuration(78_000, en)).toBe("1m 18s");
  });
});

describe("failure_code 映射", () => {
  it("覆盖契约全部 RunFailureCode，中英文都有标题和说明；未知码用通用文案", () => {
    for (const code of FAILURE_CODES) {
      const k = failureMessageKeys(code);
      expect(k.title).toBe(`steps.error.titles.${code}`);
      for (const t of [zh, en]) {
        expect(t(k.title)).not.toBe(k.title);
        expect(t(k.description)).not.toBe(k.description);
      }
    }
    expect(failureMessageKeys("").title).toBe("steps.error.titles.generic");
    expect(failureMessageKeys("NEW_CODE").title).toBe(
      "steps.error.titles.generic",
    );
  });
});

describe("步骤行", () => {
  it("Exec 各状态的标题：运行中 / 已运行 / 运行命令（Settled）/ 失败 / 中断 / 被拒绝", () => {
    renderTurn([
      stepEvent({
        id: "a",
        kind: "exec",
        state: "running",
        command: "pnpm test",
      }),
      stepEvent({
        id: "b",
        kind: "exec",
        state: "done",
        command: "git status",
      }),
      stepEvent({
        id: "c",
        kind: "exec",
        state: "failed",
        command: "pnpm build",
        exit_code: 2,
      }),
      stepEvent({
        id: "d",
        kind: "exec",
        state: "interrupted",
        command: "pnpm install",
      }),
      stepEvent({
        id: "e",
        kind: "exec",
        state: "denied",
        command: "rm -rf node_modules",
      }),
    ]);
    expect(screen.getByText("正在运行命令")).toBeInTheDocument();
    expect(screen.getByText("已运行命令")).toBeInTheDocument();
    expect(screen.getByText("运行命令失败")).toBeInTheDocument();
    expect(screen.getByText("运行命令已中断")).toBeInTheDocument();
    expect(screen.getByText("运行命令被拒绝")).toBeInTheDocument();
  });

  it("回合结束后已完成的命令显示「运行命令」", () => {
    renderTurn(
      [
        stepEvent({
          id: "b",
          kind: "exec",
          state: "done",
          command: "git status",
        }),
      ],
      true,
    );
    expect(screen.getByText("运行命令")).toBeInTheDocument();
  });

  it("Exec 展开：cwd、$ 命令、输出、退出码 · 耗时；收起后隐藏；复制命令", async () => {
    const user = userEvent.setup();
    const writeText = vi.fn(() => Promise.resolve());
    Object.defineProperty(navigator, "clipboard", {
      value: { writeText },
      configurable: true,
    });
    renderTurn([
      stepEvent({
        id: "x",
        kind: "exec",
        state: "done",
        command: "git status --short",
        cwd: "/Users/dev/OpenFox",
        output: "## main\n M a.ts",
        exit_code: 0,
        duration_ms: 1200,
      }),
    ]);
    const header = screen.getByRole("button", { name: /已运行命令/ });
    expect(header).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByTestId("exec-output")).not.toBeInTheDocument();
    await user.click(header);
    expect(header).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByText("/Users/dev/OpenFox")).toBeInTheDocument();
    expect(screen.getByTestId("exec-output")).toHaveTextContent("## main");
    expect(screen.getByText("退出码：0 · 1.2 秒")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "复制命令" }));
    expect(writeText).toHaveBeenCalledWith("git status --short");
    expect(await screen.findByText("已复制命令")).toBeInTheDocument();
    await user.click(header);
    expect(screen.queryByTestId("exec-output")).not.toBeInTheDocument();
  });

  it("Exec 运行中默认展开，被拒绝 / 中断且没有输出时给出说明", async () => {
    const user = userEvent.setup();
    renderTurn([
      stepEvent({
        id: "r",
        kind: "exec",
        state: "running",
        command: "pnpm test",
        output: "vitest run",
      }),
      stepEvent({
        id: "d",
        kind: "exec",
        state: "denied",
        command: "rm -rf x",
        terminated_before_output: true,
      }),
    ]);
    expect(screen.getByTestId("exec-output")).toHaveTextContent("vitest run");
    expect(screen.getByText("命令在产生输出前被拒绝。")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /运行命令被拒绝/ }));
    expect(
      screen.queryByText("命令在产生输出前被拒绝。"),
    ).not.toBeInTheDocument();
  });

  it("Exec 运行中输出自动跟随到底；用户向上滚动后不再跟随", () => {
    const height = vi
      .spyOn(HTMLElement.prototype, "scrollHeight", "get")
      .mockImplementation(function (this: HTMLElement) {
        return (this.textContent?.length ?? 0) * 10;
      });
    vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(100);
    const base = [
      stepEvent({
        id: "x",
        kind: "exec",
        state: "running",
        command: "t",
        output: "line1\n",
      }),
    ];
    const view = (events: RunEvent[]) => (
      <Providers>
        {foldRunEvents(events).items.map((item) => (
          <TurnItemRow key={item.key} item={item} />
        ))}
      </Providers>
    );
    const { rerender } = render(view(base));
    const output = screen.getByTestId("exec-output");
    const more = [
      ...base,
      stepEvent({
        id: "x",
        kind: "exec",
        state: "running",
        output_append: "line2\nline3\n",
      }),
    ];
    rerender(view(more));
    expect(output.scrollTop).toBe(output.scrollHeight);
    // 用户滚到中间
    output.scrollTop = 0;
    output.dispatchEvent(new Event("scroll"));
    rerender(
      view([
        ...more,
        stepEvent({
          id: "x",
          kind: "exec",
          state: "running",
          output_append: "line4\n",
        }),
      ]),
    );
    expect(output.scrollTop).toBe(0);
    expect(height).toHaveBeenCalled();
  });

  it("PrepEnv 运行中展开显示进度条与子步骤，结束后收起并显示耗时", () => {
    const { unmount } = renderTurn([
      stepEvent({
        id: "prep",
        kind: "prep_env",
        state: "running",
        progress: 0.5,
        substeps: [
          { kind: "workdir", state: "done" },
          {
            kind: "repo",
            state: "done",
            label: "已基于 main 准备仓库 OpenFox",
          },
          { kind: "runtime", state: "running" },
        ],
      }),
    ]);
    expect(screen.getByText("正在准备环境")).toBeInTheDocument();
    expect(screen.getByRole("progressbar")).toHaveAttribute(
      "aria-valuenow",
      "50",
    );
    expect(screen.getByText("工作目录已就绪")).toBeInTheDocument();
    expect(
      screen.getByText("已基于 main 准备仓库 OpenFox"),
    ).toBeInTheDocument();
    expect(screen.getByText("正在启动 Runtime")).toBeInTheDocument();
    unmount();
    renderTurn([
      stepEvent({
        id: "prep",
        kind: "prep_env",
        state: "done",
        duration_ms: 2400,
        substeps: [{ kind: "workdir", state: "done" }],
      }),
    ]);
    expect(screen.getByText("环境就绪")).toBeInTheDocument();
    expect(screen.getByText("· 2.4 秒")).toBeInTheDocument();
    expect(screen.queryByText("工作目录已就绪")).not.toBeInTheDocument();
  });

  it("准备成功的详情使用简短项目名，支持键盘展开并保留自定义标签", async () => {
    const user = userEvent.setup();
    renderTurn([
      stepEvent({
        id: "prep",
        kind: "prep_env",
        state: "done",
        duration_ms: 600,
        substeps: [
          { kind: "workdir", state: "done" },
          {
            kind: "repo",
            state: "done",
            label: "已基于 main 准备仓库 OpenFox",
          },
          { kind: "runtime", state: "done", label: "codex" },
          { kind: "session", state: "done" },
        ],
      }),
    ]);
    const trigger = screen.getByRole("button", { name: /环境就绪/ });
    expect(trigger).toHaveAttribute("aria-expanded", "false");
    await user.tab();
    expect(trigger).toHaveFocus();
    await user.keyboard("{Enter}");
    const details = screen.getByRole("list", { name: "环境准备详情" });
    expect(trigger).toHaveAttribute("aria-controls", details.id);
    expect(
      within(details)
        .getAllByRole("listitem")
        .map((item) => item.textContent),
    ).toEqual(["工作目录", "已基于 main 准备仓库 OpenFox", "Codex", "会话"]);
    expect(screen.queryByText("工作目录已就绪")).not.toBeInTheDocument();
    expect(screen.queryByText("会话已就绪")).not.toBeInTheDocument();
    await user.keyboard(" ");
    expect(
      screen.queryByRole("list", { name: "环境准备详情" }),
    ).not.toBeInTheDocument();
  });

  it.each([
    ["failed", "准备环境失败", "工作目录 · 失败"],
    ["interrupted", "准备环境已中断", "工作目录 · 已中断"],
    ["denied", "准备环境被拒绝", "工作目录 · 被拒绝"],
  ] as const)(
    "准备子步骤 %s 自动展示真实状态，不称已就绪",
    (state, title, detail) => {
      renderTurn([
        stepEvent({
          id: "prep",
          kind: "prep_env",
          state,
          substeps: [{ kind: "workdir", state }],
        }),
      ]);
      expect(screen.getByRole("button", { name: title })).toHaveAttribute(
        "aria-expanded",
        "true",
      );
      expect(screen.getByText(detail)).toBeVisible();
      expect(screen.queryByText("工作目录已就绪")).not.toBeInTheDocument();
    },
  );

  it("准备状态从运行转成功时自动收起，手动展开后可看到成功项目", async () => {
    const running = stepEvent({
      id: "prep",
      kind: "prep_env",
      state: "running",
      substeps: [{ kind: "session", state: "running" }],
    });
    const done = stepEvent({
      id: "prep",
      kind: "prep_env",
      state: "done",
      duration_ms: 800,
      substeps: [{ kind: "session", state: "done" }],
    });
    const ui = (events: RunEvent[]) => (
      <Providers>
        <TurnItemRow item={foldRunEvents(events).items[0]!} />
      </Providers>
    );
    const { rerender } = render(ui([running]));
    expect(screen.getByText("正在创建会话")).toBeVisible();
    rerender(ui([running, done]));
    const trigger = screen.getByRole("button", { name: /环境就绪/ });
    expect(trigger).toHaveAttribute("aria-expanded", "false");
    expect(
      screen.queryByRole("list", { name: "环境准备详情" }),
    ).not.toBeInTheDocument();
    await userEvent.click(trigger);
    expect(screen.getByText("会话")).toBeVisible();
  });

  it("Thinking 默认收起，展开后显示思考内容", async () => {
    const user = userEvent.setup();
    renderTurn([
      stepEvent({
        id: "t",
        kind: "thinking",
        state: "done",
        text: "先看目录结构",
      }),
    ]);
    expect(screen.queryByText("先看目录结构")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /思考/ }));
    expect(screen.getByText("先看目录结构")).toBeInTheDocument();
  });

  it("Patch：文件名 + +N/−M，展开后 diff 行着色", async () => {
    const user = userEvent.setup();
    renderTurn([
      stepEvent({
        id: "p",
        kind: "patch",
        state: "done",
        files: [
          {
            path: "packages/workbench/src/hifox-chat-nav.tsx",
            kind: "update",
            additions: 2,
            deletions: 1,
            diff: "--- a/x\n+++ b/x\n@@ -1,2 +1,3 @@\n const a = 1;\n-  old\n+  new\n+  more\n",
          },
        ],
      }),
    ]);
    expect(screen.getByText("hifox-chat-nav.tsx")).toBeInTheDocument();
    expect(screen.getByText("+2")).toBeInTheDocument();
    expect(screen.getByText("−1")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /已编辑/ }));
    expect(
      screen.getByText("packages/workbench/src/hifox-chat-nav.tsx"),
    ).toBeInTheDocument();
    const lines = document.querySelectorAll("[data-diff]");
    expect([...lines].map((line) => line.getAttribute("data-diff"))).toEqual([
      "hunk",
      "ctx",
      "del",
      "add",
      "add",
    ]);
  });

  it("Activity：读取（行号）、搜索（结果数，展开结果）、网页搜索、网页读取（只允许 http 链接）", async () => {
    const user = userEvent.setup();
    renderTurn([
      stepEvent({
        id: "r",
        kind: "read",
        state: "done",
        path: "src/hifox-sidebar.tsx",
        line_start: 640,
        line_end: 720,
      }),
      stepEvent({
        id: "s",
        kind: "search",
        state: "done",
        query: "ChatNav",
        scope: "packages/workbench/src",
        results: [
          { file: "hifox-chat-nav.tsx", matches: 3 },
          { file: "app.tsx", matches: 1 },
        ],
        total_files: 4,
      }),
      stepEvent({
        id: "ws",
        kind: "web_search",
        state: "done",
        query: "Slack 侧栏",
        results: [{ title: "Docs", url: "https://example.com/docs" }],
      }),
      stepEvent({
        id: "wf",
        kind: "web_fetch",
        state: "done",
        url: "javascript:alert(1)",
      }),
    ]);
    expect(screen.getByText("已读取")).toBeInTheDocument();
    expect(screen.getByText("第 640–720 行")).toBeInTheDocument();
    expect(screen.getByText("· 4 条结果")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /已搜索内容/ }));
    expect(
      screen.getByText("搜索范围：packages/workbench/src"),
    ).toBeInTheDocument();
    expect(screen.getByText("3 处匹配")).toBeInTheDocument();
    expect(screen.getByText("…还有 2 个文件")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /已搜索网页/ }));
    expect(screen.getByRole("link", { name: /Docs/ })).toHaveAttribute(
      "href",
      "https://example.com/docs",
    );
    expect(screen.getByText("javascript:alert(1)").closest("a")).toBeNull();
  });

  it("Todo 显示已完成 a/b，StepGroup 运行中展开、点击收起", async () => {
    const user = userEvent.setup();
    renderTurn([
      stepEvent({
        id: "plan",
        kind: "todo",
        state: "running",
        items: [
          { text: "梳理实现", status: "completed" },
          { text: "补齐渲染", status: "in_progress" },
          { text: "更新记录", status: "pending" },
        ],
      }),
      stepEvent({
        id: "g",
        kind: "step_group",
        state: "running",
        title: "运行命令、编辑文件",
      }),
      stepEvent({
        id: "gx",
        kind: "exec",
        state: "running",
        command: "pwd",
        group_id: "g",
      }),
    ]);
    expect(screen.getByText("已完成 1/3")).toBeInTheDocument();
    expect(screen.getByText("补齐渲染")).toBeInTheDocument();
    const group = screen.getByRole("button", { name: /运行命令、编辑文件/ });
    expect(group).toHaveAttribute("aria-expanded", "true");
    // 分组内运行中的命令默认收起
    expect(
      screen.getByRole("button", { name: /正在运行命令/ }),
    ).toHaveAttribute("aria-expanded", "false");
    await user.click(group);
    expect(screen.queryByText("pwd")).not.toBeInTheDocument();
  });

  it("Write：文件名，展开后路径、未写入说明与内容", async () => {
    const user = userEvent.setup();
    renderTurn([
      stepEvent({
        id: "w",
        kind: "write_file",
        state: "done",
        path: "SeekSeek/heart.html",
        content: "<!DOCTYPE html>",
        applied: false,
      }),
    ]);
    await user.click(screen.getByRole("button", { name: /写入/ }));
    expect(screen.getByText("SeekSeek/heart.html")).toBeInTheDocument();
    expect(screen.getByText("尚未写入磁盘。")).toBeInTheDocument();
    // 内容以代码块显示，按路径识别语言（高亮懒加载，按文本内容断言）
    expect(screen.getByText("HTML")).toBeInTheDocument();
    expect(document.querySelector("pre[data-lang]")).toHaveTextContent(
      "<!DOCTYPE html>",
    );
  });

  it("Write：超过 30 行的内容折叠，可展开全部", async () => {
    const user = userEvent.setup();
    const content = Array.from({ length: 40 }, (_, i) => `line ${i}`).join(
      "\n",
    );
    renderTurn([
      stepEvent({
        id: "w",
        kind: "write_file",
        state: "done",
        path: "src/main.go",
        content,
      }),
    ]);
    await user.click(screen.getByRole("button", { name: /写入/ }));
    expect(screen.getByText("Go")).toBeInTheDocument();
    const expand = screen.getByRole("button", { name: "展开全部 40 行" });
    expect(expand).toHaveAttribute("aria-expanded", "false");
    await user.click(expand);
    expect(screen.getByRole("button", { name: "收起代码" })).toHaveAttribute(
      "aria-expanded",
      "true",
    );
  });

  it("Edit 合成的 diff：裸 @@ 作分隔，@@ 之后以 --- 开头的是删除行", async () => {
    const user = userEvent.setup();
    renderTurn([
      stepEvent({
        id: "e",
        kind: "patch",
        state: "done",
        files: [
          {
            path: "db/init.sql",
            kind: "update",
            additions: 2,
            deletions: 2,
            diff: "@@\n-one\n+1\n@@\n--- sql\n+--- keep\n",
          },
        ],
      }),
    ]);
    await user.click(screen.getByRole("button", { name: /已编辑/ }));
    const lines = document.querySelectorAll("[data-diff]");
    expect([...lines].map((line) => line.getAttribute("data-diff"))).toEqual([
      "del",
      "add",
      "hunk",
      "del",
      "add",
    ]);
    expect(screen.getByText("-- sql")).toBeInTheDocument();
    expect(screen.getByText("--- keep")).toBeInTheDocument();
  });

  it("RunStatus 各状态文案", () => {
    render(
      <Providers>
        <RunStatusRow kind="processing" durationMs={12_000} />
        <RunStatusRow kind="thinking" durationMs={5000} />
        <RunStatusRow kind="done" durationMs={65_000} />
        <RunStatusRow kind="failed" durationMs={8000} />
        <RunStatusRow kind="cancelled" durationMs={3000} />
      </Providers>,
    );
    const texts = screen.getAllByRole("status").map((el) => el.textContent);
    expect(texts).toEqual([
      "处理中12 秒",
      "正在思考",
      "已完成1 分 5 秒",
      "执行失败8 秒",
      "用户已取消3 秒",
    ]);
  });

  it("待核对：非管理员只见提示，管理员需二次确认后才标记失败", async () => {
    const user = userEvent.setup();
    const markFailed = vi.fn();
    const { rerender } = render(
      <Providers>
        <ReconciliationRow reason="PID_REUSE" canResolve={false} />
      </Providers>,
    );
    expect(screen.getByText(/待核对/)).toBeInTheDocument();
    expect(screen.getByText("请联系管理员处理。")).toBeInTheDocument();
    expect(screen.queryByRole("button")).not.toBeInTheDocument();

    rerender(
      <Providers>
        <ReconciliationRow
          reason="UNKNOWN_PROCESS"
          canResolve
          onMarkFailed={markFailed}
        />
      </Providers>,
    );
    expect(screen.queryByText("请联系管理员处理。")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "标记为失败" }));
    expect(markFailed).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "确认标记为失败" }));
    expect(markFailed).toHaveBeenCalledTimes(1);
  });

  it("待核对：运行中的 Run 显示待核对而不是处理中", () => {
    render(
      <Providers>
        <RunTimelineView
          turn={foldRunEvents([], { terminal: false })}
          run={run({ state: "RUNNING", reconciliation_required: true })}
        />
      </Providers>,
    );
    expect(screen.getByText(/待核对/)).toBeInTheDocument();
    expect(screen.queryByText("处理中")).not.toBeInTheDocument();
  });

  // 阶段 12d（G-03）需求变更：未知原因由“只显示排队中”改为显示通用文案；空原因仍只显示排队中。
  it("排队原因：已知原因显示专门文案，未知原因显示通用文案，空原因与非排队状态不显示", () => {
    render(
      <Providers>
        <RunStatusRow kind="queued" waitReason="AGENT_CHAT_CONCURRENCY" />
        <RunStatusRow kind="queued" waitReason="SOMETHING_NEW" />
        <RunStatusRow kind="queued" waitReason="" />
        <RunStatusRow
          kind="processing"
          waitReason="AGENT_CHAT_CONCURRENCY"
          durationMs={1000}
        />
      </Providers>,
    );
    const texts = screen.getAllByRole("status").map((el) => el.textContent);
    expect(texts).toEqual([
      "排队中· 聊天并发已满",
      "排队中· 等待条件满足",
      "排队中",
      "处理中1 秒",
    ]);
  });

  it("排队原因：全部已知原因都有中英文专门文案，未知原因回退通用文案", () => {
    const reasons = [
      "ORGANIZATION_CAPACITY",
      "AGENT_CONCURRENCY",
      "AGENT_CHAT_CONCURRENCY",
      "COMPUTER_CONCURRENCY",
      "CONVERSATION_BUSY",
      "WORKDIR_BUSY",
      "AWAITING_COMPUTER",
      "COMPUTER_OFFLINE",
      "TASK_GATE",
      "OFFER_EXPIRED",
      "RUNTIME_UNAVAILABLE",
    ];
    const en = createTranslator("en-US");
    const unknownZh = zh("connect.waitReason.UNKNOWN");
    const unknownEn = en("connect.waitReason.UNKNOWN");
    expect(unknownZh).toBe("等待条件满足");
    expect(unknownEn).toBe("Waiting for conditions to be met");
    for (const reason of reasons) {
      const key = waitReasonKey(reason);
      expect(key, reason).toBe(`connect.waitReason.${reason}`);
      const zhText = zh(key!);
      const enText = en(key!);
      expect(zhText, reason).not.toBe(key);
      expect(zhText, reason).not.toBe(unknownZh);
      // 英文缺键会回退到中文：要求英文文案不含中文字符。
      expect(enText, reason).not.toMatch(/[\u4e00-\u9fff]/);
      expect(enText, reason).not.toBe(unknownEn);
    }
    expect(waitReasonKey("SOMETHING_NEW")).toBe("connect.waitReason.UNKNOWN");
    expect(waitReasonKey("")).toBeNull();
    expect(waitReasonKey(null)).toBeNull();
  });

  it("排队原因：任务门禁与目录占用显示对应文案", () => {
    render(
      <Providers>
        <RunStatusRow kind="queued" waitReason="TASK_GATE" />
        <RunStatusRow kind="queued" waitReason="WORKDIR_BUSY" />
      </Providers>,
    );
    const texts = screen.getAllByRole("status").map((el) => el.textContent);
    expect(texts).toEqual([
      "排队中· 等待任务进入待办或进行中",
      "排队中· 工作目录正被其他运行占用",
    ]);
  });
});

describe("截断与重命名", () => {
  it("exec 输出被截断时提示「输出已截断」；patch 重命名显示新路径", async () => {
    const user = userEvent.setup();
    renderTurn([
      stepEvent({
        id: "x",
        kind: "exec",
        state: "done",
        command: "cat big.log",
        output: "...tail",
        truncated: true,
      }),
      stepEvent({
        id: "p",
        kind: "patch",
        state: "done",
        files: [
          {
            path: "src/old.ts",
            kind: "update",
            move_path: "src/new.ts",
            additions: 1,
            deletions: 1,
            diff: "@@\n-a\n+b",
          },
        ],
      }),
    ]);
    await user.click(screen.getByRole("button", { name: /已运行命令/ }));
    expect(
      screen.getByText("输出已截断，只保留了末尾部分。"),
    ).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /已编辑/ }));
    expect(screen.getByText("移动到 src/new.ts")).toBeInTheDocument();
  });
});

describe("mergeRunEvents", () => {
  it("按 (generation, sequence) 排序去重，不按 stream_seq；同位置不同 id 视为重复", () => {
    const at = (
      generation: number,
      sequence: number,
      streamSeq: number,
      id = `m-${generation}-${sequence}`,
    ) => ({
      ...stepEvent({ id: "s", kind: "thinking", state: "running" }),
      id,
      generation,
      sequence,
      stream_seq: streamSeq,
    });
    const base = [at(1, 2, 1), at(1, 3, 2)];
    const merged = mergeRunEvents(base, [
      at(1, 1, 99),
      at(1, 3, 2),
      at(1, 2, 50, "other-id"),
      at(2, 1, 3),
    ]);
    expect(merged.map((e) => [e.generation, e.sequence])).toEqual([
      [1, 1],
      [1, 2],
      [1, 3],
      [2, 1],
    ]);
    expect(mergeRunEvents(base, [at(1, 2, 1)])).toBe(base);
  });
});

describe("卡片", () => {
  it("ModelError：标题、说明、错误信息，重试回调，复制错误信息", async () => {
    const user = userEvent.setup();
    const onRetry = vi.fn();
    const writeText = vi.fn(() => Promise.resolve());
    Object.defineProperty(navigator, "clipboard", {
      value: { writeText },
      configurable: true,
    });
    render(
      <Providers>
        <ModelErrorCard
          failureCode="PROVIDER_OVERLOADED"
          detail="529 overloaded_error: Overloaded"
          onRetry={onRetry}
        />
      </Providers>,
    );
    const card = screen.getByRole("alert");
    expect(within(card).getByText("模型服务请求失败")).toBeInTheDocument();
    expect(within(card).getByText(/模型服务暂时繁忙/)).toBeInTheDocument();
    expect(
      within(card).getByText("529 overloaded_error: Overloaded"),
    ).toBeInTheDocument();
    await user.click(within(card).getByRole("button", { name: "重试" }));
    expect(onRetry).toHaveBeenCalledOnce();
    expect(
      within(card).getByText("529 overloaded_error: Overloaded"),
    ).not.toBeVisible();
    await user.click(within(card).getByText("诊断详情"));
    expect(
      within(card).getByText("529 overloaded_error: Overloaded"),
    ).toBeVisible();
    await user.click(
      within(card).getByRole("button", { name: "复制错误信息" }),
    );
    expect(writeText).toHaveBeenCalledWith(
      "PROVIDER_OVERLOADED: 529 overloaded_error: Overloaded",
    );
  });

  it("ModelError 不传 onRetry 时没有重试按钮；未知码用通用文案", () => {
    render(
      <Providers>
        <ModelErrorCard failureCode="" />
      </Providers>,
    );
    expect(screen.getByText("执行失败")).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "重试" }),
    ).not.toBeInTheDocument();
  });

  it("FileAttachment：名称、类型、大小，打开与下载；过期时不给链接", () => {
    const artifact: RunArtifact = {
      content_id: "content-f1",
      content_revision: 1,
      head_artifact_id: "f1",
      organization_id: "o1",
      id: "f1",
      run_id: "r1",
      name: "年会活动方案.docx",
      size_bytes: 128 * 1024,
      sha256: "x",
      mime_type:
        "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
      state: "AVAILABLE",
      pinned: false,
      version: 1,
      created_at: TS,
    };
    const { unmount } = render(
      <Providers>
        <FileAttachmentCard artifact={artifact} href="/api/v1/x" />
      </Providers>,
    );
    expect(screen.getByText("Word 文档 · 128 KB")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "打开" })).toHaveAttribute(
      "href",
      "/api/v1/x",
    );
    expect(screen.getByRole("link", { name: "下载" })).toHaveAttribute(
      "download",
      "年会活动方案.docx",
    );
    unmount();
    render(
      <Providers>
        <FileAttachmentCard
          artifact={{ ...artifact, state: "EXPIRED" }}
          href="/api/v1/x"
        />
      </Providers>,
    );
    expect(screen.getByText("文件已过期或不可用")).toBeInTheDocument();
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });
});

describe("RunTimelineView", () => {
  it("运行中：显示步骤与 RunStatus 计时；正在思考时不计时", () => {
    vi.useFakeTimers({
      now: new Date("2026-10-29T08:00:12Z"),
      toFake: ["Date", "setInterval", "clearInterval"],
    });
    const events = [stepEvent({ id: "t", kind: "thinking", state: "running" })];
    const { rerender } = render(
      <Providers>
        <RunTimelineView turn={foldRunEvents(events)} run={run()} />
      </Providers>,
    );
    expect(screen.getByRole("status", { name: "" })).toHaveTextContent(
      "正在思考",
    );
    expect(screen.getAllByText("正在思考")).toHaveLength(1);
    expect(screen.queryByRole("button", { name: /思考/ })).toBeNull();
    const more = [
      ...events,
      stepEvent({ id: "t", kind: "thinking", state: "done", text: " \n" }),
    ];
    rerender(
      <Providers>
        <RunTimelineView turn={foldRunEvents(more)} run={run()} />
      </Providers>,
    );
    expect(screen.getByText("处理中")).toBeInTheDocument();
    expect(screen.queryByText("思考")).not.toBeInTheDocument();
    expect(screen.getByText("12 秒")).toBeInTheDocument();
    act(() => vi.advanceTimersByTime(3000));
    expect(screen.getByText("15 秒")).toBeInTheDocument();
  });

  it("结束后：默认保留工具摘要和最终回复；showFinalText=false 时不显示回复", async () => {
    const user = userEvent.setup();
    const events = [
      stepEvent({
        id: "x",
        kind: "exec",
        state: "done",
        command: "git status",
      }),
      legacy("output", "测试已全部通过"),
      stepEvent({
        id: "turn_end",
        kind: "turn_end",
        state: "done",
        final_state: "done",
        duration_ms: 78_000,
      }),
    ];
    const turn = foldRunEvents(events);
    const { unmount } = render(
      <Providers>
        <RunTimelineView
          turn={turn}
          run={run({ state: "SUCCEEDED" })}
          result={
            {
              id: "result",
              git_summary: { v: 1, repo: false, available: true },
            } as RunResult
          }
          renderText={(text) => <em>{text}</em>}
        />
      </Providers>,
    );
    const summary = screen.getByRole("button", { name: /已完成 1 分 18 秒/ });
    expect(summary).toHaveAttribute("aria-expanded", "false");
    expect(screen.getByText("git status")).toBeInTheDocument();
    expect(screen.getByText("测试已全部通过").tagName).toBe("EM");
    expect(
      screen.queryByRole("region", { name: "变更摘要" }),
    ).not.toBeInTheDocument();
    expect(screen.queryByText("无仓库，未统计")).not.toBeInTheDocument();
    await user.click(summary);
    expect(screen.getByText("git status")).toBeInTheDocument();
    expect(screen.getByText("运行命令")).toBeInTheDocument();
    unmount();
    render(
      <Providers>
        <RunTimelineView
          turn={turn}
          run={run({ state: "SUCCEEDED" })}
          showFinalText={false}
        />
      </Providers>,
    );
    expect(screen.queryByText("测试已全部通过")).not.toBeInTheDocument();
  });

  it("失败：收尾行为执行失败，显示 ModelError 卡片（错误信息来自 failed 事件）", () => {
    const turn = foldRunEvents([
      legacy("failed", "模型服务繁忙", { detail: "529 overloaded" }),
    ]);
    render(
      <Providers>
        <RunTimelineView
          turn={turn}
          run={run({
            state: "FAILED",
            failure_code: "PROVIDER_OVERLOADED",
            duration_ms: 8000,
          })}
          onRetry={() => undefined}
        />
      </Providers>,
    );
    expect(screen.getByText("执行失败 8 秒")).toBeInTheDocument();
    expect(screen.getByText("529 overloaded")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "重试" })).toBeInTheDocument();
  });

  it.each([
    ["a1", "u1", true],
    ["old-assistant", "u1", false],
    ["a1", "old-owner", false],
  ])(
    "小助手重试只允许当前 Agent 与所有者：%s / %s",
    (agentId, actorId, allowed) => {
      render(
        <Providers>
          <RunTimelineView
            turn={foldRunEvents([legacy("failed", "运行失败")])}
            run={run({ state: "FAILED", agent_id: agentId, actor_id: actorId })}
            onRetry={() => undefined}
            retryScope={{ agent_id: "a1", actor_id: "u1" }}
          />
        </Providers>,
      );
      expect(Boolean(screen.queryByRole("button", { name: "重试" }))).toBe(
        allowed,
      );
    },
  );

  it("取消：用户已取消 n 秒", () => {
    const turn = foldRunEvents([
      stepEvent({
        id: "turn_end",
        kind: "turn_end",
        state: "interrupted",
        final_state: "cancelled",
        duration_ms: 3000,
      }),
    ]);
    render(
      <Providers>
        <RunTimelineView turn={turn} run={run({ state: "CANCELED" })} />
      </Providers>,
    );
    expect(screen.getByText("用户已取消 3 秒")).toBeInTheDocument();
  });
});

describe("<RunTimeline>", () => {
  function server(pages: RunEvent[][], state: Run["state"] = "RUNNING") {
    const calls: Call[] = [];
    const transport = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), "http://localhost");
      const call: Call = {
        method: init?.method ?? "GET",
        path: url.pathname.replace(/^\/api/, ""),
        query: url.searchParams,
        body: undefined,
        headers: new Headers(init?.headers),
      };
      calls.push(call);
      if (call.path === `${ORG}/runs/r1`)
        return Promise.resolve(json(200, run({ state })));
      if (call.path === `${ORG}/runs/r1/events`) {
        const after = Number(call.query.get("after") ?? "0");
        const index =
          after === 0
            ? 0
            : pages.findIndex((page) => page[0] && page[0].stream_seq > after);
        const page = pages[index] ?? [];
        const last =
          index < pages.length - 1
            ? page[page.length - 1]?.stream_seq
            : undefined;
        return Promise.resolve(
          json(200, { events: page, ...(last ? { next_cursor: last } : {}) }),
        );
      }
      if (call.path === `${ORG}/runs/r1/result`)
        return Promise.resolve(
          json(404, { error: { code: "NOT_FOUND", message: "x" } }),
        );
      if (call.path === `${ORG}/runs/r1/artifacts`)
        return Promise.resolve(json(200, { artifacts: [] }));
      return Promise.resolve(
        json(404, { error: { code: "NOT_FOUND", message: "x" } }),
      );
    });
    return { calls, transport: transport as unknown as typeof fetch };
  }

  it("分页拉全事件；实时 run.event 追加进缓存不触发 refetch；run.state 更新 Run 状态", async () => {
    const page1 = [
      stepEvent({
        id: "prep",
        kind: "prep_env",
        state: "done",
        duration_ms: 2400,
      }),
      stepEvent({
        id: "x",
        kind: "exec",
        state: "running",
        command: "pnpm test",
      }),
    ];
    const page2 = [
      stepEvent({
        id: "x",
        kind: "exec",
        state: "running",
        output_append: "vitest run\n",
      }),
    ];
    const { calls, transport } = server([page1, page2]);
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    render(
      <Providers client={client} transport={transport}>
        <RunTimeline orgId="o1" runId="r1" />
      </Providers>,
    );
    expect(await screen.findByTestId("exec-output")).toHaveTextContent(
      "vitest run",
    );
    expect(screen.queryByText("环境就绪")).not.toBeInTheDocument();
    const eventCalls = () =>
      calls.filter((call) => call.path.endsWith("/events"));
    expect(eventCalls().map((call) => call.query.get("after"))).toEqual([
      "0",
      String(page1[1]!.stream_seq),
    ]);

    const batcher = createEventBatcher(client, "o1", 0);
    act(() => {
      batcher.push(
        "run.event",
        stepEvent({
          id: "x",
          kind: "exec",
          state: "running",
          output_append: "✓ a.test.ts\n",
        }),
      );
    });
    // TanStack 的通知是异步批量的，等下一轮渲染
    await waitFor(() =>
      expect(screen.getByTestId("exec-output")).toHaveTextContent(
        "✓ a.test.ts",
      ),
    );
    // 重复投递同一事件不会重复追加
    const dup = stepEvent({
      id: "x",
      kind: "exec",
      state: "running",
      output_append: "once\n",
    });
    act(() => {
      batcher.push("run.event", dup);
      batcher.push("run.event", dup);
    });
    await waitFor(() =>
      expect(
        screen.getByTestId("exec-output").textContent?.match(/once/g),
      ).toHaveLength(1),
    );
    // 其他 Run 的事件不进入这个缓存
    act(() => {
      batcher.push("run.event", {
        ...stepEvent({
          id: "y",
          kind: "exec",
          state: "running",
          command: "other",
        }),
        run_id: "r2",
      });
    });
    expect(screen.queryByText("other")).not.toBeInTheDocument();
    expect(eventCalls()).toHaveLength(2);

    act(() => {
      batcher.push("run.state", {
        type: "run.state",
        entity: "run",
        id: "r1",
        version: 4,
        payload: {
          state: "CANCEL_REQUESTED",
          wait_reason: "",
          failure_code: "",
        },
      });
    });
    expect(await screen.findByText("正在取消")).toBeInTheDocument();
    expect(client.getQueryData<Run>(keys.run("o1", "r1"))?.version).toBe(4);
    // 过期版本被丢弃
    act(() => {
      batcher.push("run.state", {
        type: "run.state",
        entity: "run",
        id: "r1",
        version: 2,
        payload: { state: "RUNNING" },
      });
    });
    expect(client.getQueryData<Run>(keys.run("o1", "r1"))?.state).toBe(
      "CANCEL_REQUESTED",
    );
    batcher.dispose();
  });

  it("加载失败时显示重试", async () => {
    const transport = vi.fn(() =>
      Promise.resolve(json(500, { error: { code: "INTERNAL", message: "x" } })),
    ) as unknown as typeof fetch;
    render(
      <Providers transport={transport}>
        <RunTimeline orgId="o1" runId="r1" />
      </Providers>,
    );
    expect(await screen.findByText("加载执行过程失败")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "重试" })).toBeInTheDocument();
  });

  it("Run 已终态时拉取结果与产物；没有结果（404）按 null 处理并显示收尾行", async () => {
    const { calls, transport } = server(
      [
        [
          stepEvent({
            id: "turn_end",
            kind: "turn_end",
            state: "done",
            final_state: "done",
            duration_ms: 5000,
          }),
        ],
      ],
      "SUCCEEDED",
    );
    render(
      <Providers transport={transport}>
        <RunTimeline orgId="o1" runId="r1" />
      </Providers>,
    );
    expect(await screen.findByText("已完成 5 秒")).toBeInTheDocument();
    await waitFor(() => {
      expect(calls.some((call) => call.path.endsWith("/result"))).toBe(true);
      expect(calls.some((call) => call.path.endsWith("/artifacts"))).toBe(true);
    });
  });
});

describe("阶段 12d F-08：改为直接在源目录运行并重试", () => {
  const failedRun = (extra: Partial<Run> = {}) =>
    run({
      state: "FAILED",
      failure_code: "WORKTREE_PREP_FAILED",
      actor_id: "u1",
      ...extra,
    });
  const terminalTurn = () => foldRunEvents([], { terminal: true });

  it("WORKTREE_PREP_FAILED：确认框说明不再隔离，取消不触发，确认后只调用一次", async () => {
    const user = userEvent.setup();
    const onRetryDirect = vi.fn();
    render(
      <Providers>
        <ModelErrorCard
          failureCode="WORKTREE_PREP_FAILED"
          onRetry={vi.fn()}
          onRetryDirect={onRetryDirect}
          directDirectory="team/openfox"
        />
      </Providers>,
    );
    const entry = screen.getByRole("button", {
      name: "改为直接在源目录运行并重试",
    });
    // 点击入口只打开确认框，不会直接切换。
    await user.click(entry);
    expect(onRetryDirect).not.toHaveBeenCalled();
    const dialog = await screen.findByRole("dialog");
    expect(
      within(dialog).getByText("改为直接在源目录运行？"),
    ).toBeInTheDocument();
    expect(dialog.textContent).toContain(
      "Agent 将直接修改源目录中的文件，不再隔离",
    );
    expect(
      within(dialog).getByText("源目录：team/openfox"),
    ).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "取消" }));
    expect(onRetryDirect).not.toHaveBeenCalled();
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
    await user.click(entry);
    await user.click(
      within(await screen.findByRole("dialog")).getByRole("button", {
        name: "直接运行并重试",
      }),
    );
    expect(onRetryDirect).toHaveBeenCalledOnce();
  });

  it("其他失败码或未提供回调时不显示入口", () => {
    render(
      <Providers>
        <ModelErrorCard
          failureCode="REPO_UNAVAILABLE"
          onRetryDirect={vi.fn()}
        />
        <ModelErrorCard failureCode="WORKTREE_PREP_FAILED" onRetry={vi.fn()} />
      </Providers>,
    );
    expect(
      screen.queryByRole("button", { name: "改为直接在源目录运行并重试" }),
    ).not.toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: "重试" })).toHaveLength(1);
  });

  it("时间线：只有原输入作者本人看到入口，确认后以该 Run 调用", async () => {
    const user = userEvent.setup();
    const onRetry = vi.fn();
    const { unmount } = render(
      <Providers>
        <RunTimelineView
          turn={terminalTurn()}
          run={failedRun()}
          directRetry={{ actorId: "u2", directory: "team/openfox", onRetry }}
        />
      </Providers>,
    );
    expect(screen.getByText("准备工作目录失败")).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "改为直接在源目录运行并重试" }),
    ).not.toBeInTheDocument();
    unmount();

    render(
      <Providers>
        <RunTimelineView
          turn={terminalTurn()}
          run={failedRun()}
          directRetry={{ actorId: "u1", directory: "team/openfox", onRetry }}
        />
        <RunTimelineView
          turn={terminalTurn()}
          run={failedRun({ id: "r2", failure_code: "QUEUE_TIMEOUT" })}
          directRetry={{ actorId: "u1", directory: "team/openfox", onRetry }}
        />
      </Providers>,
    );
    const entries = screen.getAllByRole("button", {
      name: "改为直接在源目录运行并重试",
    });
    expect(entries).toHaveLength(1);
    await user.click(entries[0]!);
    await user.click(
      within(await screen.findByRole("dialog")).getByRole("button", {
        name: "直接运行并重试",
      }),
    );
    expect(onRetry).toHaveBeenCalledExactlyOnceWith("r1");
  });

  it("源目录：WORKTREE 选择用其目录；其他选择不提供；未选择时仅 EXPLICIT + WORKTREE + 单根", () => {
    const config = (extra: Record<string, unknown> = {}) => ({
      directory_mode: "EXPLICIT",
      directory_roots: ["team/openfox"],
      workdir_isolation: "WORKTREE",
      ...extra,
    });
    expect(
      directRetryDirectory(
        { mode: "WORKTREE", directory: "team/app/sub" },
        config(),
      ),
    ).toBe("team/app/sub");
    expect(
      directRetryDirectory({ mode: "DIRECT", directory: "team/app" }, config()),
    ).toBeNull();
    expect(directRetryDirectory({ mode: "REUSE" }, config())).toBeNull();
    expect(directRetryDirectory(undefined, config())).toBe("team/openfox");
    expect(
      directRetryDirectory(undefined, config({ workdir_isolation: "DIRECT" })),
    ).toBeNull();
    expect(
      directRetryDirectory(
        undefined,
        config({ directory_roots: ["team/a", "team/b"] }),
      ),
    ).toBeNull();
    expect(
      directRetryDirectory(undefined, config({ directory_mode: "WORKSPACE" })),
    ).toBeNull();
    expect(directRetryDirectory(undefined, undefined)).toBeNull();
  });
});

describe("通用工具与过程层级", () => {
  it("工具保留参数、结果与稳定 ID，重复/乱序事件不重复显示", () => {
    const start = stepEvent({
      id: "tool",
      kind: "tool_call",
      state: "running",
      tool_name: "tasks.get",
      tool_input: '{"id":42}',
    });
    const end = stepEvent({
      id: "tool",
      kind: "tool_call",
      state: "done",
      output: "result",
    });
    const turn = foldRunEvents([end, start, end]);
    expect(turn.items).toHaveLength(1);
    expect(turn.items[0]).toMatchObject({
      step: {
        tool_name: "tasks.get",
        tool_input: '{"id":42}',
        output: "result",
        status: "done",
      },
    });
  });

  it("工具卡支持键盘展开与复制，参数结果只作为文本显示，不呈现 Shell", async () => {
    const user = userEvent.setup();
    const writeText = vi.fn(() => Promise.resolve());
    Object.defineProperty(navigator, "clipboard", {
      value: { writeText },
      configurable: true,
    });
    const { container } = renderTurn([
      stepEvent({
        id: "tool",
        kind: "tool_call",
        state: "done",
        tool_name: "github.search",
        tool_input: '{"id":9007199254740993,"query":"test"}',
        output: "<img src=x onerror=alert(1)>",
        duration_ms: 40,
      }),
    ]);
    const trigger = screen.getByRole("button", { name: /github.search/ });
    expect(trigger).toHaveAttribute("aria-expanded", "false");
    trigger.focus();
    await user.keyboard("{Enter}");
    expect(
      document.getElementById(trigger.getAttribute("aria-controls")!),
    ).not.toBeNull();
    expect(screen.getByText("参数")).toBeInTheDocument();
    expect(screen.getByText("结果")).toBeInTheDocument();
    expect(
      screen.getByText("<img src=x onerror=alert(1)>"),
    ).toBeInTheDocument();
    expect(container.querySelector("img")).toBeNull();
    expect(screen.queryByText("$")).not.toBeInTheDocument();
    expect(screen.queryByText("运行命令")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "复制工具参数" }));
    expect(writeText).toHaveBeenCalledWith(
      '{"id":9007199254740993,"query":"test"}',
    );
    expect(
      screen.getByText('{"id":9007199254740993,"query":"test"}'),
    ).toBeInTheDocument();
    trigger.focus();
    await user.keyboard(" ");
    expect(trigger).toHaveAttribute("aria-expanded", "false");
  });

  it.each(["failed", "denied", "interrupted"] as const)(
    "%s 工具默认展开，不把缺失参数伪造为对象",
    (state) => {
      renderTurn([
        stepEvent({
          id: "tool",
          kind: "tool_call",
          state,
          tool_name: "tasks.get",
          output: "unavailable",
          input_truncated: true,
          truncated: true,
        }),
      ]);
      expect(screen.getByRole("button", { name: /tasks.get/ })).toHaveAttribute(
        "aria-expanded",
        "true",
      );
      expect(screen.getByText("unavailable")).toBeInTheDocument();
      expect(screen.queryByText("参数")).not.toBeInTheDocument();
      expect(
        screen.getByText("参数未完整显示，内容过长或格式不支持。"),
      ).toBeInTheDocument();
      expect(
        screen.getByText("结果过长，仅显示部分内容。"),
      ).toBeInTheDocument();
    },
  );

  it("仅分组相邻工具，完成后保留说明，成功准备藏于过程详情，失败可见", async () => {
    const user = userEvent.setup();
    const events = [
      stepEvent({ id: "prep", kind: "prep_env", state: "done" }),
      stepEvent({
        id: "a",
        kind: "tool_call",
        state: "done",
        tool_name: "files.list",
      }),
      stepEvent({ id: "b", kind: "read", state: "done", path: "README.md" }),
      stepEvent({
        id: "note",
        kind: "agent_text",
        state: "done",
        text: "找到项目说明，继续检查。",
      }),
      stepEvent({
        id: "c",
        kind: "tool_call",
        state: "failed",
        tool_name: "tasks.update",
        output: "permission denied",
      }),
      stepEvent({
        id: "d",
        kind: "tool_call",
        state: "done",
        tool_name: "tasks.get",
      }),
    ];
    const turn = foldRunEvents(events, { terminal: true });
    const { container } = render(
      <Providers>
        <RunTimelineView turn={turn} run={run({ state: "SUCCEEDED" })} />
      </Providers>,
    );
    expect(screen.queryByText("环境就绪")).not.toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "已执行 2 项操作" }),
    ).toHaveAttribute("aria-expanded", "false");
    expect(
      screen.getByRole("button", { name: "2 项操作 · 存在异常" }),
    ).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByText("permission denied")).toBeInTheDocument();
    expect(container.textContent!.indexOf("已执行 2 项操作")).toBeLessThan(
      container.textContent!.indexOf("找到项目说明"),
    );
    expect(container.textContent!.indexOf("找到项目说明")).toBeLessThan(
      container.textContent!.indexOf("tasks.update"),
    );
    await user.click(screen.getByRole("button", { name: "已完成" }));
    expect(screen.getByText("环境就绪")).toBeInTheDocument();
    expect(screen.getByText("files.list")).toBeInTheDocument();
  });

  it("用户手动展开的工具在运行结束后保持展开", async () => {
    const user = userEvent.setup();
    const event = stepEvent({
      id: "tool",
      kind: "tool_call",
      state: "running",
      tool_name: "tasks.get",
    });
    const renderView = (events: RunEvent[]) => (
      <Providers>
        <RunTimelineView turn={foldRunEvents(events)} />
      </Providers>
    );
    const { rerender } = render(renderView([event]));
    await user.click(screen.getByRole("button", { name: /tasks.get/ }));
    expect(screen.getByText("等待工具返回结果…")).toBeInTheDocument();
    rerender(
      renderView([
        event,
        stepEvent({
          id: "tool",
          kind: "tool_call",
          state: "done",
          output: "ready",
        }),
      ]),
    );
    expect(screen.getByRole("button", { name: /tasks.get/ })).toHaveAttribute(
      "aria-expanded",
      "true",
    );
    expect(screen.getByText("ready")).toBeInTheDocument();
    const next = stepEvent({
      id: "second",
      kind: "tool_call",
      state: "running",
      tool_name: "tasks.list",
    });
    rerender(
      renderView([
        event,
        stepEvent({
          id: "tool",
          kind: "tool_call",
          state: "done",
          output: "ready",
        }),
        next,
      ]),
    );
    expect(screen.getByRole("button", { name: /tasks.get/ })).toHaveAttribute(
      "aria-expanded",
      "true",
    );
    expect(screen.getByText("ready")).toBeVisible();
    const group = screen.getByRole("button", { name: /正在执行 · 2 项操作/ });
    await user.click(group);
    expect(screen.getByText("ready")).not.toBeVisible();
    await user.click(group);
    expect(screen.getByText("ready")).toBeVisible();
  });
});

describe("output failure presentation", () => {
  it("recognizes only the supervisor output-limit code prefix", () => {
    expect(
      failureMessageKeys(
        "INTERNAL",
        "OUTPUT_LIMIT: runtime: output limit exceeded",
      ).title,
    ).toBe("steps.error.outputLimitTitle");
    expect(
      failureMessageKeys("RATE_LIMITED", "OUTPUT_LIMIT: detail").title,
    ).toBe("steps.error.titles.RATE_LIMITED");
    expect(failureMessageKeys("INTERNAL", "tool said OUTPUT_LIMIT").title).toBe(
      "steps.error.titles.INTERNAL",
    );
  });

  it("collapses diagnostics and preserves partial replies", async () => {
    const detail = "OUTPUT_LIMIT: runtime: output limit exceeded";
    const result = {
      status: "FAILED",
      summary: detail,
      output: "",
    } as RunResult;
    expect(isDuplicateRunError(detail, result)).toBe(true);
    expect(isDuplicateRunError("Partial answer", result)).toBe(false);
    expect(
      isDuplicateRunError(detail, { ...result, output: "Partial answer" }),
    ).toBe(false);
    expect(
      isDuplicateRunError(detail, { ...result, status: "SUCCEEDED" }),
    ).toBe(false);
    const turn = foldRunEvents([legacy("failed", detail)]);
    turn.finalText = detail;
    render(
      <Providers>
        <RunTimelineView
          turn={turn}
          run={run({ state: "FAILED", failure_code: "INTERNAL" })}
          result={result}
        />
      </Providers>,
    );
    expect(screen.getByText("运行输出超出限制")).toBeVisible();
    expect(screen.getAllByText(detail)).toHaveLength(1);
    expect(screen.getByText(detail)).not.toBeVisible();
    await userEvent.click(screen.getByText("诊断详情"));
    expect(screen.getByText(detail)).toBeVisible();
  });

  it("shows interrupted reads without claiming completion", () => {
    renderTurn([
      stepEvent({
        id: "image",
        kind: "read",
        state: "interrupted",
        path: "screen.png",
      }),
    ]);
    expect(screen.getByText("读取已中断")).toBeVisible();
    expect(screen.queryByText("已读取")).not.toBeInTheDocument();
  });

  it("uses command descriptions while preserving the full command", async () => {
    renderTurn([
      stepEvent({
        id: "shell",
        kind: "exec",
        state: "done",
        title: "检查页面语法",
        command: "node --check /tmp/resume.js",
      }),
    ]);
    expect(screen.getByText("检查页面语法")).toBeVisible();
    await userEvent.click(screen.getByRole("button", { name: /检查页面语法/ }));
    expect(screen.getByText(/node --check/)).toBeVisible();
  });
});
