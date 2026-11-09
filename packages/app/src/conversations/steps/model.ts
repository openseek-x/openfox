import type { RunEvent, Step, StepKind, StepState } from "@openfox/api";

// 执行步骤（schema v1）折叠：把一次 Run 的事件流归并为一个回合（Turn）。纯函数，不依赖 React。
//
// 规则（与契约 Step 的 description 一致）：
// - 按 (generation, sequence) 排序；同一事件（id 相同或同一 generation/sequence）只取一次。
// - type = step：按 step.id 合并，后到的覆盖先到的同名字段；append / output_append 追加到 text / output，
//   携带 text / output 的事件给出完整值（替换已累积的内容）。不同 generation 的同名步骤互不合并。
// - 旧事件兼容：output → 最终回复；failed → 错误；warning → 提示行；completed / cancelled / run.result → 回合结束；
//   tool（commentary / commandExecution）只在整个 Run 没有 Runtime 上报的 step 事件（prep_env 以外）时映射为
//   agent_text / exec：新适配器同时上报新旧两种事件，避免重复显示。prep_env 由 supervisor 对所有 Runtime 生成，
//   不能作为「Runtime 会上报步骤」的信号。
// - turn_end 不作为行显示，只给出回合结束状态与耗时。
// - 回合结束后：仍为 running 的步骤视为 interrupted；exec 的 done 显示为 settled（「运行命令」）。

/** 行的显示状态：在 StepState 之外多一个 settled（回合结束后已完成的命令）。 */
export type StepStatus = StepState | "settled";

/** 合并后的步骤（不含增量字段），附带显示状态与所属 generation。 */
export interface StepView extends Omit<Step, "append" | "output_append"> {
  status: StepStatus;
  generation: number;
}

export type TurnItem =
  | { type: "step"; key: string; step: StepView }
  | { type: "group"; key: string; step: StepView; children: TurnItem[] }
  | { type: "warning"; key: string; text: string };

export type TurnFinalState = "done" | "failed" | "cancelled";

export interface Turn {
  /** 顶层条目，按首次出现的顺序；step_group 的子步骤在 children 里 */
  items: TurnItem[];
  /** 最终回复正文（旧 output 事件，多条按顺序以空行拼接）；没有时为 null */
  finalText: string | null;
  /** 回合是否已结束（turn_end、旧终态事件、run.result，或调用方告知 Run 已终态） */
  ended: boolean;
  finalState: TurnFinalState | null;
  /** turn_end 给出的耗时（毫秒）；没有时为 null，由调用方用 Run 的 duration_ms 兜底 */
  durationMs: number | null;
  /** failed 事件：text 为可读摘要，detail 为原始错误（可能为空） */
  error: { text: string; detail: string | null } | null;
  /** 当前正在运行的最后一个步骤的类型（RunStatus 显示「正在思考」等）；没有时为 null */
  activeKind: StepKind | null;
  /** 参与折叠的事件数（去重后） */
  eventCount: number;
}

export interface FoldOptions {
  /** Run 已处于终态（SUCCEEDED / FAILED / CANCELED）：即使事件里没有结束标记也按已结束处理 */
  terminal?: boolean;
}

const STEP_KINDS: ReadonlySet<string> = new Set<StepKind>([
  "prep_env",
  "thinking",
  "agent_text",
  "exec",
  "patch",
  "write_file",
  "read",
  "search",
  "web_search",
  "web_fetch",
  "todo",
  "step_group",
  "turn_end",
]);
const STEP_STATES: ReadonlySet<string> = new Set<StepState>([
  "running",
  "done",
  "failed",
  "interrupted",
  "denied",
]);

const isRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === "object" && value !== null && !Array.isArray(value);

const str = (value: unknown): string | null =>
  typeof value === "string" ? value : null;

/** 取出 type = step 事件里的步骤；schema 不是 1、kind / state 未知或 id 为空时返回 null（向前兼容：忽略）。 */
export function stepOf(event: Pick<RunEvent, "safe_payload">): Step | null {
  const data = (event.safe_payload as { data?: unknown }).data;
  if (!isRecord(data) || data.schema !== 1 || !isRecord(data.step)) return null;
  const step = data.step;
  if (
    typeof step.id !== "string" ||
    step.id === "" ||
    typeof step.kind !== "string" ||
    !STEP_KINDS.has(step.kind) ||
    typeof step.state !== "string" ||
    !STEP_STATES.has(step.state)
  )
    return null;
  return step as unknown as Step;
}

/**
 * 按 (generation, sequence) 排序并去重；不修改入参。
 * 不能用 stream_seq 或到达顺序：stream_seq 序列带缓存，不同连接取号与时间顺序无关。
 */
export function sortEvents(events: readonly RunEvent[]): RunEvent[] {
  const seen = new Set<string>();
  const unique: RunEvent[] = [];
  for (const event of events) {
    const position = `${event.generation}:${event.sequence}`;
    if (seen.has(event.id) || seen.has(position)) continue;
    seen.add(event.id);
    seen.add(position);
    unique.push(event);
  }
  return unique.sort(
    (a, b) =>
      a.generation - b.generation ||
      a.sequence - b.sequence ||
      a.stream_seq - b.stream_seq,
  );
}

type Merged = Omit<Step, "append" | "output_append">;

/** 合并同一步骤的一次上报：覆盖已出现的字段，增量追加。 */
export function mergeStep(prev: Merged | undefined, next: Step): Merged {
  const { append, output_append, ...rest } = next;
  // rest 的 id / kind / state 必定存在，只有可选字段会被去掉
  const merged: Merged = { ...prev, ...(dropUndefined(rest) as typeof rest) };
  if (append) merged.text = (rest.text ?? prev?.text ?? "") + append;
  if (output_append)
    merged.output = (rest.output ?? prev?.output ?? "") + output_append;
  return merged;
}

function dropUndefined<T extends object>(value: T): Partial<T> {
  const result: Partial<T> = {};
  for (const [key, item] of Object.entries(value))
    if (item !== undefined) (result as Record<string, unknown>)[key] = item;
  return result;
}

/** 旧 tool 事件映射为步骤；无法映射时返回 null。 */
function legacyToolStep(event: RunEvent): Step | null {
  const payload = event.safe_payload as { text?: unknown; data?: unknown };
  const data = isRecord(payload.data) ? payload.data : {};
  const text = str(payload.text) ?? "";
  const id = `legacy:${event.id}`;
  if (data.kind === "commentary")
    return text ? { id, kind: "agent_text", state: "done", text } : null;
  if (data.kind !== "commandExecution") return null;
  const exitCode =
    typeof data.exit_code === "number" ? data.exit_code : undefined;
  let state: StepState = "done";
  if (data.status === "inProgress") state = "running";
  else if (data.status === "failed") state = "failed";
  else if (data.status === "declined") state = "denied";
  else if (exitCode !== undefined && exitCode !== 0) state = "failed";
  return { id, kind: "exec", state, command: text, exit_code: exitCode };
}

const RESULT_FINAL: Record<string, TurnFinalState> = {
  SUCCEEDED: "done",
  FAILED: "failed",
  CANCELED: "cancelled",
  TIMED_OUT: "failed",
};

interface Entry {
  key: string;
  generation: number;
  step?: Merged;
  warning?: string;
}

/** 把事件流折叠为回合。空输入返回空回合（未结束，除非 terminal）。 */
export function foldRunEvents(
  events: readonly RunEvent[],
  options: FoldOptions = {},
): Turn {
  const sorted = sortEvents(events);
  const hasStepEvents = sorted.some((event) => {
    if (event.type !== "step") return false;
    const step = stepOf(event);
    return step !== null && step.kind !== "prep_env";
  });
  const latest = sorted.length ? sorted[sorted.length - 1]!.generation : 0;

  const entries: Entry[] = [];
  const byKey = new Map<string, Entry>();
  const output: string[] = [];
  let ended = false;
  let finalState: TurnFinalState | null = null;
  let durationMs: number | null = null;
  let error: Turn["error"] = null;
  let generation: number | null = null;

  const upsert = (gen: number, step: Step) => {
    const key = `${gen}:${step.id}`;
    const entry = byKey.get(key);
    if (entry) {
      entry.step = mergeStep(entry.step, step);
      return;
    }
    const created: Entry = {
      key,
      generation: gen,
      step: mergeStep(undefined, step),
    };
    byKey.set(key, created);
    entries.push(created);
  };
  const end = (state: TurnFinalState | null) => {
    ended = true;
    // turn_end / failed 等先给出的结束状态优先，run.result 只补缺
    finalState ??= state;
  };

  for (const event of sorted) {
    if (generation !== event.generation) {
      // 新 generation（Run 被重新领取）：回合级状态以最新一次尝试为准
      generation = event.generation;
      ended = false;
      finalState = null;
      durationMs = null;
      error = null;
      output.length = 0;
    }
    const payload = event.safe_payload as { text?: unknown; data?: unknown };
    const text = str(payload.text);
    const data = isRecord(payload.data) ? payload.data : {};
    switch (event.type) {
      case "step": {
        const step = stepOf(event);
        if (!step) break;
        if (step.kind === "turn_end") {
          const final = step.final_state ?? null;
          end(final);
          if (final) finalState = final;
          if (typeof step.duration_ms === "number")
            durationMs = step.duration_ms;
          break;
        }
        upsert(event.generation, step);
        break;
      }
      case "output":
        if (text) output.push(text);
        break;
      case "tool": {
        if (hasStepEvents) break;
        const step = legacyToolStep(event);
        if (step) upsert(event.generation, step);
        break;
      }
      case "warning":
        if (text) {
          const key = `warning:${event.id}`;
          const entry: Entry = {
            key,
            generation: event.generation,
            warning: text,
          };
          byKey.set(key, entry);
          entries.push(entry);
        }
        break;
      case "failed":
        error = { text: text ?? "", detail: str(data.detail) };
        end("failed");
        break;
      case "completed":
        end("done");
        break;
      case "cancelled":
        end("cancelled");
        break;
      case "run.result":
        end(RESULT_FINAL[str(data.status) ?? ""] ?? null);
        break;
      default:
        break;
    }
  }
  if (options.terminal) ended = true;

  // 显示状态
  const views = new Map<string, StepView>();
  for (const entry of entries) {
    if (!entry.step) continue;
    const superseded = entry.generation < latest;
    let status: StepStatus = entry.step.state;
    if (status === "running" && (ended || superseded)) status = "interrupted";
    else if (
      status === "done" &&
      entry.step.kind === "exec" &&
      (ended || superseded)
    )
      status = "settled";
    views.set(entry.key, {
      ...entry.step,
      status,
      generation: entry.generation,
    });
  }

  // 归组：子步骤挂到同 generation 的 step_group 下；找不到分组时留在顶层
  const items: TurnItem[] = [];
  const groups = new Map<string, Extract<TurnItem, { type: "group" }>>();
  for (const entry of entries) {
    const view = views.get(entry.key);
    if (view?.kind === "step_group") {
      const group = {
        type: "group" as const,
        key: entry.key,
        step: view,
        children: [],
      };
      groups.set(entry.key, group);
    }
  }
  for (const entry of entries) {
    if (entry.warning !== undefined) {
      items.push({ type: "warning", key: entry.key, text: entry.warning });
      continue;
    }
    const view = views.get(entry.key)!;
    const group = groups.get(entry.key);
    if (group) {
      items.push(group);
      continue;
    }
    const parent = view.group_id
      ? groups.get(`${entry.generation}:${view.group_id}`)
      : undefined;
    const item: TurnItem = { type: "step", key: entry.key, step: view };
    if (parent) parent.children.push(item);
    else items.push(item);
  }

  let activeKind: StepKind | null = null;
  if (!ended)
    for (const view of views.values())
      if (view.status === "running" && view.kind !== "step_group")
        activeKind = view.kind;

  return {
    items,
    finalText: output.length ? output.join("\n\n") : null,
    ended,
    finalState,
    durationMs,
    error,
    activeKind,
    eventCount: sorted.length,
  };
}

/** 回合里是否有可显示的步骤行（不含提示行）。 */
export const hasSteps = (turn: Turn) =>
  turn.items.some((item) => item.type !== "warning");
