import { describe, expect, it } from "vitest";

import type { RunEvent, Step } from "@openfox/api";

import {
  foldRunEvents,
  mergeStep,
  sortEvents,
  stepOf,
  type TurnItem,
} from "./model";

let streamSeq = 0;

/** 构造一条事件；sequence 必填，generation 默认 1。 */
function ev(
  sequence: number,
  type: string,
  payload: { text?: string; data?: Record<string, unknown> } = {},
  extra: Partial<RunEvent> = {},
): RunEvent {
  streamSeq++;
  return {
    id: `e-${extra.generation ?? 1}-${sequence}`,
    organization_id: "o1",
    run_id: "r1",
    generation: 1,
    sequence,
    stream_seq: streamSeq,
    type,
    safe_payload: payload,
    occurred_at: "2026-10-29T08:00:00Z",
    received_at: "2026-10-29T08:00:00Z",
    ...extra,
  };
}

const step = (
  sequence: number,
  value: Partial<Step> & Pick<Step, "id" | "kind" | "state">,
  extra: Partial<RunEvent> = {},
) => ev(sequence, "step", { data: { schema: 1, step: value } }, extra);

const steps = (items: TurnItem[]) =>
  items.flatMap((item) => (item.type === "warning" ? [] : [item.step]));

describe("foldRunEvents", () => {
  it("空输入返回未结束的空回合；terminal 时视为已结束", () => {
    const turn = foldRunEvents([]);
    expect(turn).toMatchObject({
      items: [],
      finalText: null,
      ended: false,
      finalState: null,
      durationMs: null,
      error: null,
      activeKind: null,
      eventCount: 0,
    });
    expect(foldRunEvents([], { terminal: true }).ended).toBe(true);
  });

  it("按 step.id 合并：字段覆盖，append / output_append 追加，带完整值时替换", () => {
    const turn = foldRunEvents([
      step(1, { id: "t1", kind: "thinking", state: "running" }),
      step(2, { id: "t1", kind: "thinking", state: "running", append: "先看" }),
      step(3, { id: "t1", kind: "thinking", state: "running", append: "目录" }),
      step(4, {
        id: "x1",
        kind: "exec",
        state: "running",
        command: "ls",
        cwd: ".",
      }),
      step(5, {
        id: "x1",
        kind: "exec",
        state: "running",
        output_append: "a\n",
      }),
      step(6, {
        id: "x1",
        kind: "exec",
        state: "running",
        output_append: "b\n",
      }),
    ]);
    const [thinking, exec] = steps(turn.items);
    expect(thinking).toMatchObject({ text: "先看目录", status: "running" });
    expect(thinking).not.toHaveProperty("append");
    expect(exec).toMatchObject({ command: "ls", cwd: ".", output: "a\nb\n" });
    expect(exec).not.toHaveProperty("output_append");
    expect(turn.activeKind).toBe("exec");

    const done = foldRunEvents([
      step(1, { id: "x1", kind: "exec", state: "running", command: "ls" }),
      step(2, {
        id: "x1",
        kind: "exec",
        state: "running",
        output_append: "partial",
      }),
      step(3, {
        id: "x1",
        kind: "exec",
        state: "done",
        output: "full output",
        exit_code: 0,
        duration_ms: 1200,
      }),
    ]);
    expect(steps(done.items)[0]).toMatchObject({
      command: "ls",
      output: "full output",
      exit_code: 0,
      duration_ms: 1200,
      status: "done",
    });
  });

  it("乱序与重复事件：按 (generation, sequence) 排序，同 id 或同位置只取一次", () => {
    const a = step(1, {
      id: "t1",
      kind: "agent_text",
      state: "running",
      append: "你",
    });
    const b = step(2, {
      id: "t1",
      kind: "agent_text",
      state: "running",
      append: "好",
    });
    const c = step(3, { id: "t1", kind: "agent_text", state: "done" });
    const turn = foldRunEvents([c, b, a, b, { ...a, id: "dup-same-position" }]);
    expect(steps(turn.items)[0]).toMatchObject({
      text: "你好",
      status: "done",
    });
    expect(turn.eventCount).toBe(3);
    expect(sortEvents([c, a, b]).map((e) => e.sequence)).toEqual([1, 2, 3]);
  });

  it("turn_end 结束回合，不作为行显示；回合结束后 exec done 显示为 settled，running 视为 interrupted", () => {
    const turn = foldRunEvents([
      step(1, {
        id: "prep",
        kind: "prep_env",
        state: "done",
        duration_ms: 2400,
      }),
      step(2, { id: "x1", kind: "exec", state: "done", command: "git status" }),
      step(3, {
        id: "x2",
        kind: "exec",
        state: "failed",
        command: "make",
        exit_code: 2,
      }),
      step(4, { id: "t1", kind: "thinking", state: "running" }),
      step(5, {
        id: "turn_end",
        kind: "turn_end",
        state: "done",
        final_state: "done",
        duration_ms: 78000,
      }),
    ]);
    expect(turn.ended).toBe(true);
    expect(turn.finalState).toBe("done");
    expect(turn.durationMs).toBe(78000);
    expect(steps(turn.items).map((s) => [s.id, s.status])).toEqual([
      ["prep", "done"],
      ["x1", "settled"],
      ["x2", "failed"],
      ["t1", "interrupted"],
    ]);
    expect(turn.activeKind).toBeNull();
  });

  it("运行中 exec done 仍是 done（「已运行命令」）；thinking 运行中时 activeKind = thinking", () => {
    const turn = foldRunEvents([
      step(1, { id: "x1", kind: "exec", state: "done", command: "ls" }),
      step(2, { id: "t1", kind: "thinking", state: "running" }),
    ]);
    expect(steps(turn.items).map((s) => s.status)).toEqual(["done", "running"]);
    expect(turn.activeKind).toBe("thinking");
  });

  it("覆盖全部 kind：patch / write_file / read / search / web_search / web_fetch / todo", () => {
    const turn = foldRunEvents([
      step(1, {
        id: "p",
        kind: "patch",
        state: "done",
        files: [
          {
            path: "a.ts",
            kind: "update",
            additions: 2,
            deletions: 1,
            diff: "@@\n-a\n+b\n+c",
          },
        ],
      }),
      step(2, {
        id: "w",
        kind: "write_file",
        state: "done",
        path: "h.html",
        content: "<p>",
        applied: false,
        diff_available: false,
      }),
      step(3, {
        id: "r",
        kind: "read",
        state: "done",
        path: "a.ts",
        line_start: 1,
        line_end: 9,
      }),
      step(4, {
        id: "s",
        kind: "search",
        state: "done",
        query: "Nav",
        results: [{ file: "a.ts", matches: 3 }],
        total_files: 4,
      }),
      step(5, {
        id: "ws",
        kind: "web_search",
        state: "done",
        query: "q",
        results: [{ url: "https://e.x" }],
      }),
      step(6, {
        id: "wf",
        kind: "web_fetch",
        state: "done",
        url: "https://e.x/doc",
      }),
      step(7, {
        id: "plan",
        kind: "todo",
        state: "running",
        items: [
          { text: "a", status: "completed" },
          { text: "b", status: "pending" },
        ],
      }),
      step(8, {
        id: "plan",
        kind: "todo",
        state: "done",
        items: [
          { text: "a", status: "completed" },
          { text: "b", status: "completed" },
        ],
      }),
    ]);
    const byId = Object.fromEntries(steps(turn.items).map((s) => [s.id, s]));
    expect(Object.keys(byId)).toEqual(["p", "w", "r", "s", "ws", "wf", "plan"]);
    expect(byId.p!.files?.[0]?.additions).toBe(2);
    expect(byId.w).toMatchObject({ applied: false, diff_available: false });
    expect(byId.s).toMatchObject({ total_files: 4 });
    expect(byId.plan!.items?.every((item) => item.status === "completed")).toBe(
      true,
    );
    expect(byId.plan!.status).toBe("done");
  });

  it("group_id 归组：子步骤挂到同 generation 的 step_group 下，找不到分组时留在顶层", () => {
    const turn = foldRunEvents([
      step(1, {
        id: "g1",
        kind: "step_group",
        state: "running",
        title: "运行命令，编辑文件",
      }),
      step(2, {
        id: "x1",
        kind: "exec",
        state: "done",
        command: "pwd",
        group_id: "g1",
      }),
      step(3, {
        id: "t1",
        kind: "agent_text",
        state: "done",
        text: "好",
        group_id: "missing",
      }),
      step(4, {
        id: "w1",
        kind: "write_file",
        state: "running",
        path: "a",
        group_id: "g1",
      }),
    ]);
    expect(turn.items.map((item) => item.key)).toEqual(["1:g1", "1:t1"]);
    const group = turn.items[0]!;
    expect(group.type).toBe("group");
    if (group.type !== "group") return;
    expect(group.step.title).toBe("运行命令，编辑文件");
    expect(group.children.map((child) => child.key)).toEqual(["1:x1", "1:w1"]);
  });

  it("旧事件兼容：output → 最终回复；tool commentary / commandExecution → 步骤；warning → 提示行；failed → 错误", () => {
    const turn = foldRunEvents([
      ev(1, "started"),
      ev(2, "tool", { text: "我先看看", data: { kind: "commentary" } }),
      ev(3, "tool", {
        text: "go test ./...",
        data: { kind: "commandExecution", status: "completed", exit_code: 1 },
      }),
      ev(4, "tool", {
        text: "rm -rf x",
        data: { kind: "commandExecution", status: "declined" },
      }),
      ev(5, "tool", { data: { kind: "fileChange", status: "completed" } }),
      ev(6, "warning", { text: "重连中" }),
      ev(7, "output", { text: "第一段" }),
      ev(8, "output", { text: "第二段" }),
      ev(9, "failed", {
        text: "模型服务繁忙",
        data: { detail: "529 overloaded" },
      }),
      ev(10, "unknown", { text: "?" }),
    ]);
    expect(turn.items.map((item) => item.type)).toEqual([
      "step",
      "step",
      "step",
      "warning",
    ]);
    const [text, exec, denied] = steps(turn.items);
    expect(text).toMatchObject({ kind: "agent_text", text: "我先看看" });
    expect(exec).toMatchObject({
      kind: "exec",
      command: "go test ./...",
      exit_code: 1,
      status: "failed",
    });
    expect(denied).toMatchObject({ kind: "exec", status: "denied" });
    expect(turn.finalText).toBe("第一段\n\n第二段");
    expect(turn.error).toEqual({
      text: "模型服务繁忙",
      detail: "529 overloaded",
    });
    expect(turn).toMatchObject({ ended: true, finalState: "failed" });
  });

  it("completed / cancelled / run.result 结束回合", () => {
    expect(foldRunEvents([ev(1, "completed")]).finalState).toBe("done");
    expect(foldRunEvents([ev(1, "cancelled")]).finalState).toBe("cancelled");
    expect(
      foldRunEvents([ev(1, "run.result", { data: { status: "TIMED_OUT" } })]),
    ).toMatchObject({ ended: true, finalState: "failed" });
    // turn_end 的状态优先于之后的 run.result
    expect(
      foldRunEvents([
        step(1, {
          id: "turn_end",
          kind: "turn_end",
          state: "interrupted",
          final_state: "cancelled",
          duration_ms: 3000,
        }),
        ev(2, "run.result", { data: { status: "FAILED" } }),
      ]).finalState,
    ).toBe("cancelled");
  });

  it("Run 里出现过 Runtime 步骤事件（任意 generation，prep_env 除外）就忽略全部 tool 事件；只有 prep_env 时仍走旧映射", () => {
    const both = foldRunEvents([
      step(1, { id: "c1", kind: "agent_text", state: "done", text: "说明" }),
      ev(2, "tool", { text: "说明", data: { kind: "commentary" } }),
      ev(3, "output", { text: "最终回复" }),
    ]);
    expect(both.items).toHaveLength(1);
    expect(both.finalText).toBe("最终回复");
    const prepOnly = foldRunEvents([
      step(1, { id: "prep", kind: "prep_env", state: "done" }),
      ev(2, "tool", {
        text: "ls",
        data: { kind: "commandExecution", status: "completed", exit_code: 0 },
      }),
    ]);
    expect(steps(prepOnly.items).map((s) => s.kind)).toEqual([
      "prep_env",
      "exec",
    ]);
    const otherGeneration = foldRunEvents([
      step(1, { id: "t1", kind: "thinking", state: "done" }),
      ev(
        1,
        "tool",
        { text: "说明", data: { kind: "commentary" } },
        { generation: 2, id: "g2-1" },
      ),
    ]);
    expect(steps(otherGeneration.items).map((s) => s.kind)).toEqual([
      "thinking",
    ]);
  });

  it("实测乱序：started 的 stream_seq 排在最后，仍按 (generation, sequence) 折叠", () => {
    // 真实 Codex 用例按 stream_seq 的顺序：step,step,step,output,step,completed,run.result,started
    const base = (
      sequence: number,
      streamSeq: number,
      type: string,
      payload = {},
    ) =>
      ev(sequence, type, payload, {
        stream_seq: streamSeq,
        id: `real-${sequence}`,
      });
    const events = [
      base(2, 1, "step", {
        data: {
          schema: 1,
          step: { id: "prep", kind: "prep_env", state: "running" },
        },
      }),
      base(3, 2, "step", {
        data: {
          schema: 1,
          step: {
            id: "prep",
            kind: "prep_env",
            state: "done",
            duration_ms: 2400,
          },
        },
      }),
      base(4, 3, "step", {
        data: {
          schema: 1,
          step: { id: "x", kind: "exec", state: "running", command: "ls" },
        },
      }),
      base(6, 4, "output", { text: "完成" }),
      base(5, 5, "step", {
        data: {
          schema: 1,
          step: {
            id: "x",
            kind: "exec",
            state: "done",
            command: "ls",
            output: "a",
          },
        },
      }),
      base(7, 6, "completed"),
      base(8, 7, "run.result", { data: { status: "SUCCEEDED" } }),
      base(1, 8, "started"),
    ];
    expect(sortEvents(events).map((e) => e.sequence)).toEqual([
      1, 2, 3, 4, 5, 6, 7, 8,
    ]);
    const turn = foldRunEvents(events);
    expect(steps(turn.items).map((s) => [s.id, s.status])).toEqual([
      ["prep", "done"],
      ["x", "settled"],
    ]);
    expect(steps(turn.items)[1]!.output).toBe("a");
    expect(turn).toMatchObject({
      ended: true,
      finalState: "done",
      finalText: "完成",
    });
    // 终态之后才到的增量（sequence 更小）不会覆盖终态
    const late = foldRunEvents([
      ev(3, "step", {
        data: {
          schema: 1,
          step: { id: "x", kind: "exec", state: "done", output: "full" },
        },
      }),
      ev(2, "step", {
        data: {
          schema: 1,
          step: {
            id: "x",
            kind: "exec",
            state: "running",
            output_append: "part",
          },
        },
      }),
    ]);
    expect(steps(late.items)[0]).toMatchObject({
      output: "full",
      status: "done",
    });
  });

  it("一条 commandExecution 拆成多个 read / search 步骤（id、id:1、id:2）各自独立显示", () => {
    const turn = foldRunEvents([
      step(1, { id: "item9", kind: "read", state: "running", path: "a.ts" }),
      step(2, { id: "item9:1", kind: "read", state: "running", path: "b.ts" }),
      step(3, {
        id: "item9:2",
        kind: "search",
        state: "running",
        query: "Nav",
      }),
      step(4, { id: "item9", kind: "read", state: "done", path: "a.ts" }),
      step(5, { id: "item9:1", kind: "read", state: "done", path: "b.ts" }),
      step(6, {
        id: "item9:2",
        kind: "search",
        state: "done",
        query: "Nav",
        total_files: 2,
      }),
    ]);
    expect(steps(turn.items).map((s) => [s.id, s.kind, s.status])).toEqual([
      ["item9", "read", "done"],
      ["item9:1", "read", "done"],
      ["item9:2", "search", "done"],
    ]);
  });

  it("Run 已终态但没有 turn_end：仍 running 的步骤按 interrupted 显示", () => {
    const events = [
      step(1, {
        id: "x",
        kind: "exec",
        state: "running",
        command: "sleep 100",
      }),
      step(2, { id: "t", kind: "thinking", state: "running" }),
    ];
    expect(steps(foldRunEvents(events).items).map((s) => s.status)).toEqual([
      "running",
      "running",
    ]);
    const turn = foldRunEvents(events, { terminal: true });
    expect(steps(turn.items).map((s) => s.status)).toEqual([
      "interrupted",
      "interrupted",
    ]);
    expect(turn.ended).toBe(true);
    expect(turn.activeKind).toBeNull();
  });

  it("exec 终态带 truncated、patch 带 move_path 时保留字段", () => {
    const turn = foldRunEvents([
      step(1, {
        id: "x",
        kind: "exec",
        state: "done",
        output: "tail",
        truncated: true,
      }),
      step(2, {
        id: "p",
        kind: "patch",
        state: "done",
        files: [
          {
            path: "old.ts",
            kind: "update",
            move_path: "new.ts",
            additions: 0,
            deletions: 0,
            diff: "",
          },
        ],
      }),
    ]);
    const [exec, patch] = steps(turn.items);
    expect(exec).toMatchObject({ truncated: true, output: "tail" });
    expect(patch!.files?.[0]?.move_path).toBe("new.ts");
  });

  it("新 generation：同名步骤不合并，旧尝试中运行中的步骤视为中断，回合状态以最新一次为准", () => {
    const turn = foldRunEvents([
      step(1, { id: "prep", kind: "prep_env", state: "done" }),
      step(2, { id: "x1", kind: "exec", state: "running", command: "build" }),
      ev(3, "failed", { text: "lost" }),
      step(
        1,
        { id: "prep", kind: "prep_env", state: "running" },
        { generation: 2, id: "g2-1" },
      ),
    ]);
    expect(
      steps(turn.items).map((s) => [s.generation, s.id, s.status]),
    ).toEqual([
      [1, "prep", "done"],
      [1, "x1", "interrupted"],
      [2, "prep", "running"],
    ]);
    expect(turn).toMatchObject({ ended: false, error: null, finalState: null });
  });

  it("忽略 schema 不是 1、kind / state 未知或 id 为空的步骤事件", () => {
    const bad = [
      ev(1, "step", {
        data: { schema: 2, step: { id: "a", kind: "exec", state: "done" } },
      }),
      ev(2, "step", {
        data: { schema: 1, step: { id: "b", kind: "teleport", state: "done" } },
      }),
      ev(3, "step", {
        data: { schema: 1, step: { id: "c", kind: "exec", state: "weird" } },
      }),
      ev(4, "step", {
        data: { schema: 1, step: { id: "", kind: "exec", state: "done" } },
      }),
      ev(5, "step", { data: { schema: 1 } }),
      ev(6, "step", {}),
    ];
    expect(bad.map((event) => stepOf(event))).toEqual([
      null,
      null,
      null,
      null,
      null,
      null,
    ]);
    expect(foldRunEvents(bad).items).toEqual([]);
  });

  it("mergeStep 不受 undefined 字段影响", () => {
    const merged = mergeStep(
      { id: "x", kind: "exec", state: "running", command: "ls", output: "a" },
      {
        id: "x",
        kind: "exec",
        state: "done",
        command: undefined,
        exit_code: 0,
      },
    );
    expect(merged).toEqual({
      id: "x",
      kind: "exec",
      state: "done",
      command: "ls",
      output: "a",
      exit_code: 0,
    });
  });
});
