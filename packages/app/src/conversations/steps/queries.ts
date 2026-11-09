import {
  useQuery,
  useQueryClient,
  type QueryClient,
} from "@tanstack/react-query";

import {
  isTerminalRunState,
  type Run,
  type RunEvent,
  type RunEventsResult,
  type RunState,
} from "@openfox/api";

import { keys, useApi } from "../../context";

// Run 执行过程的查询与缓存更新。事件缓存只追加：实时事件直接并入缓存，不触发整体 refetch。

/** 运行中的 Run 详情兜底轮询间隔（主要靠 run.state 实时更新）。 */
const RUN_POLL_MS = 60_000;

/**
 * 合并事件：按 id 与 (generation, sequence) 去重，按 (generation, sequence) 排序；没有新增时返回原数组。
 * 不按 stream_seq 排：它的序列带缓存，不同连接取号与时间顺序无关。
 */
export function mergeRunEvents(
  base: readonly RunEvent[],
  incoming: readonly RunEvent[],
): RunEvent[] {
  const seen = new Set<string>();
  for (const event of base) {
    seen.add(event.id);
    seen.add(`${event.generation}:${event.sequence}`);
  }
  const added = incoming.filter((event) => {
    const position = `${event.generation}:${event.sequence}`;
    if (seen.has(event.id) || seen.has(position)) return false;
    seen.add(event.id);
    seen.add(position);
    return true;
  });
  if (added.length === 0) return base as RunEvent[];
  return [...base, ...added].sort(
    (a, b) => a.generation - b.generation || a.sequence - b.sequence,
  );
}

export function useRun(orgId: string, runId: string) {
  const api = useApi();
  return useQuery({
    queryKey: keys.run(orgId, runId),
    queryFn: ({ signal }) => api.runs.get(orgId, runId, signal),
    enabled: Boolean(orgId && runId),
    refetchInterval: (query) =>
      isTerminalRunState(query.state.data?.state) ? false : RUN_POLL_MS,
  });
}

export function useRunEvents(orgId: string, runId: string) {
  const api = useApi();
  const queryClient = useQueryClient();
  return useQuery({
    queryKey: keys.runSub(orgId, runId, "events"),
    queryFn: async ({ signal }): Promise<RunEventsResult> => {
      const fetched = await api.runs.allEvents(orgId, runId, signal);
      // 拉取期间经实时通道写进缓存的事件可能不在快照里，合并后再返回
      const live = queryClient.getQueryData<RunEventsResult>(
        keys.runSub(orgId, runId, "events"),
      );
      return live
        ? { ...fetched, events: mergeRunEvents(fetched.events, live.events) }
        : fetched;
    },
    enabled: Boolean(orgId && runId),
    // 事件只追加，缓存由实时通道维护；不随窗口聚焦重拉
    staleTime: Infinity,
    refetchOnWindowFocus: false,
  });
}

export function useRunResult(orgId: string, runId: string, enabled: boolean) {
  const api = useApi();
  return useQuery({
    queryKey: keys.runSub(orgId, runId, "result"),
    queryFn: () => api.runs.result(orgId, runId),
    enabled: Boolean(orgId && runId) && enabled,
  });
}

export function useRunArtifacts(
  orgId: string,
  runId: string,
  enabled: boolean,
) {
  const api = useApi();
  return useQuery({
    queryKey: keys.runSub(orgId, runId, "artifacts"),
    queryFn: () => api.runs.artifacts(orgId, runId),
    enabled: Boolean(orgId && runId) && enabled,
  });
}

/**
 * 实时通道收到 run.event：并入对应 Run 的事件缓存。
 * 只更新已存在的查询（有组件在看或刚看过）；首次拉取进行中时先写入缓存，拉取完成后由 queryFn 合并。
 * 返回是否写入。
 */
export function appendRunEvent(
  queryClient: QueryClient,
  orgId: string,
  event: unknown,
): boolean {
  if (!isRunEvent(event)) return false;
  const key = keys.runSub(orgId, event.run_id, "events");
  if (!queryClient.getQueryCache().find({ queryKey: key, exact: true }))
    return false;
  queryClient.setQueryData<RunEventsResult>(key, (current) => {
    if (!current) return { events: [event], truncated: false };
    const events = mergeRunEvents(current.events, [event]);
    return events === current.events ? current : { ...current, events };
  });
  return true;
}

const RUN_STATES: ReadonlySet<string> = new Set<RunState>([
  "QUEUED",
  "PREPARING",
  "RUNNING",
  "CANCEL_REQUESTED",
  "SUCCEEDED",
  "FAILED",
  "CANCELED",
]);

/**
 * 实时通道收到 run.state（组织事件，entity = run，id = Run ID）：按版本更新缓存里的 Run 状态。
 * 进入终态时失效 Run 详情（取 finished_at / duration_ms / usage）、结果与产物。
 */
export function applyRunState(
  queryClient: QueryClient,
  orgId: string,
  event: {
    id?: unknown;
    version?: unknown;
    payload?: unknown;
  },
): void {
  if (typeof event.id !== "string" || !event.id) return;
  const runId = event.id;
  const payload = (
    typeof event.payload === "object" && event.payload !== null
      ? event.payload
      : {}
  ) as Record<string, unknown>;
  const state = typeof payload.state === "string" ? payload.state : null;
  const version = typeof event.version === "number" ? event.version : null;
  const key = keys.run(orgId, runId);
  const current = queryClient.getQueryData<Run>(key);
  if (current && state && RUN_STATES.has(state)) {
    // 丢弃过期事件；没有版本号时只接受状态变化
    const stale = version !== null && version <= current.version;
    if (!stale) {
      const next: Run = {
        ...current,
        state: state as RunState,
        version: version ?? current.version,
      };
      if (typeof payload.failure_code === "string")
        next.failure_code = payload.failure_code as Run["failure_code"];
      if (typeof payload.wait_reason === "string") {
        if (payload.wait_reason) next.wait_reason = payload.wait_reason;
        else delete next.wait_reason;
      }
      queryClient.setQueryData<Run>(key, next);
    }
  }
  if (state && isTerminalRunState(state as RunState)) {
    void queryClient.invalidateQueries({ queryKey: key, exact: true });
    void queryClient.invalidateQueries({
      queryKey: keys.runSub(orgId, runId, "result"),
    });
    void queryClient.invalidateQueries({
      queryKey: keys.runSub(orgId, runId, "artifacts"),
    });
  }
}

function isRunEvent(value: unknown): value is RunEvent {
  if (typeof value !== "object" || value === null) return false;
  const event = value as Record<string, unknown>;
  return (
    typeof event.id === "string" &&
    typeof event.run_id === "string" &&
    event.run_id !== "" &&
    typeof event.type === "string" &&
    typeof event.generation === "number" &&
    typeof event.sequence === "number" &&
    typeof event.stream_seq === "number" &&
    typeof event.safe_payload === "object" &&
    event.safe_payload !== null
  );
}
