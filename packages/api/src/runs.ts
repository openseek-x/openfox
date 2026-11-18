import { ApiError, isApiError } from "./errors";
import type { HttpClient } from "./http";
import type { components, operations } from "./schema.gen";

// 阶段 2b · Run 执行过程：详情 / 事件（分页拉全）/ 结果 / 产物 / 取消。
// 类型全部来自 schema.gen.ts（契约 getRun / listRunEvents / getRunResult / listRunArtifacts /
// downloadRunArtifact / cancelRun），不手写。

type S = components["schemas"];
/** 契约里某个接口 200 响应的 JSON 类型 */
type Ok<K extends keyof operations> = operations[K]["responses"] extends {
  200: { content: { "application/json": infer B } };
}
  ? B
  : never;

export type Run = Ok<"getRun">;
export type RunState = S["RunState"];
export type RunFailureCode = S["RunFailureCode"];
export type RunUsage = S["RunUsage"];
export type ReconciliationCase = S["ReconciliationCase"];
export type ReconciliationEvidence = S["ReconciliationEvidence"];
export type ReconcilingRun = S["ReconcilingRun"];
export type RunResult = Ok<"getRunResult">;
export type RunArtifact = S["RunArtifact"];
export type RunEvent = S["RunStreamEvent"];
export type RunEventType = S["RunEventType"];
export type RunEventPayload = S["RunEventPayload"];
export type StepEventData = S["StepEventData"];
export type Step = S["Step"];
export type StepKind = S["StepKind"];
export type StepState = S["StepState"];
export type StepPrepSubstep = S["StepPrepSubstep"];
export type StepPatchFile = S["StepPatchFile"];
export type StepSearchResult = S["StepSearchResult"];
export type StepWebResult = S["StepWebResult"];
export type StepTodoItem = S["StepTodoItem"];

/** 统一事件流里 Run 执行事件的 SSE 事件名（data = RunStreamEvent）。 */
export const RUN_EVENT_TYPE = "run.event";

/** Run 的终态。 */
export const TERMINAL_RUN_STATES: readonly RunState[] = [
  "SUCCEEDED",
  "FAILED",
  "CANCELED",
];

export const isTerminalRunState = (state: RunState | undefined) =>
  state !== undefined && TERMINAL_RUN_STATES.includes(state);

/** 一次拉全的结果；truncated 表示达到页数上限，后面还有事件没拉。 */
export interface RunEventsResult {
  events: RunEvent[];
  truncated: boolean;
}

/**
 * 事件分页：后端单页上限 100；单条事件 ≤ 64 KiB，满页可能超过客户端 2 MiB 响应上限，
 * 此时这一页改用小页重试。最多翻 RUN_EVENT_MAX_PAGES 页，防止异常游标导致无限请求。
 */
const RUN_EVENT_PAGE = 100;
const RUN_EVENT_SMALL_PAGE = 20;
const RUN_EVENT_MAX_PAGES = 300;

const org = (id: string) => `/v1/organizations/${encodeURIComponent(id)}`;
const runPath = (orgId: string, runId: string) =>
  `${org(orgId)}/runs/${encodeURIComponent(runId)}`;

/** next_cursor 经 parseJson 转成了字符串；空、0 或缺省表示没有下一页。 */
function nextCursor(page: { next_cursor?: unknown }): string | null {
  const raw = page.next_cursor;
  if (raw === undefined || raw === null) return null;
  const value = String(raw);
  return value === "" || value === "0" ? null : value;
}

/** Run 方法组，由 createApi 展开到顶层。 */
export function createRunsApi(http: HttpClient) {
  const eventPage = (
    orgId: string,
    runId: string,
    after: string,
    limit: number,
    signal?: AbortSignal,
  ) =>
    http.request<Ok<"listRunEvents">>(`${runPath(orgId, runId)}/events`, {
      query: { after, limit },
      signal,
    });
  return {
    runs: {
      triggerNow: (orgId: string, runId: string, idempotencyKey: string) =>
        http.request<Ok<"triggerRunNow">>(
          `${runPath(orgId, runId)}/trigger-now`,
          {
            method: "POST",
            idempotencyKey,
          },
        ),
      get: (orgId: string, runId: string, signal?: AbortSignal) =>
        http.request<Run>(runPath(orgId, runId), { signal }),
      /** 一页事件（按 stream_seq 升序），after 为上一页的 next_cursor */
      events: (orgId: string, runId: string, after = "0", limit = 100) =>
        eventPage(orgId, runId, after, limit),
      /** 从头拉全部事件 */
      allEvents: async (
        orgId: string,
        runId: string,
        signal?: AbortSignal,
      ): Promise<RunEventsResult> => {
        const events: RunEvent[] = [];
        let after = "0";
        for (let page = 0; page < RUN_EVENT_MAX_PAGES; page++) {
          let result: Ok<"listRunEvents">;
          try {
            result = await eventPage(
              orgId,
              runId,
              after,
              RUN_EVENT_PAGE,
              signal,
            );
          } catch (error) {
            if (!isApiError(error) || error.code !== "INVALID_RESPONSE")
              throw error;
            result = await eventPage(
              orgId,
              runId,
              after,
              RUN_EVENT_SMALL_PAGE,
              signal,
            );
          }
          events.push(...result.events);
          const next = nextCursor(result);
          if (!next) return { events, truncated: false };
          after = next;
        }
        return { events, truncated: true };
      },
      /** 结果；还没有结果（运行中、未上报）时返回 null */
      result: async (orgId: string, runId: string) => {
        try {
          return await http.request<RunResult>(
            `${runPath(orgId, runId)}/result`,
          );
        } catch (error) {
          if (error instanceof ApiError && error.status === 404) return null;
          throw error;
        }
      },
      artifacts: async (orgId: string, runId: string) =>
        (
          await http.request<Ok<"listRunArtifacts">>(
            `${runPath(orgId, runId)}/artifacts`,
          )
        ).artifacts,
      /** 产物下载地址（downloadRunArtifact：二进制，响应带 Content-Disposition: attachment） */
      artifactUrl: (orgId: string, runId: string, artifactId: string) =>
        http.url(
          `${runPath(orgId, runId)}/artifacts/${encodeURIComponent(artifactId)}`,
        ),
      /** 阶段 10d：会话内所有 Run 的产物（listConversationArtifacts，created_at 倒序，最多 200 个） */
      conversationArtifacts: async (
        orgId: string,
        conversationId: string,
        signal?: AbortSignal,
      ) =>
        (
          await http.request<Ok<"listConversationArtifacts">>(
            `${org(orgId)}/conversations/${encodeURIComponent(conversationId)}/artifacts`,
            { signal },
          )
        ).artifacts,
      resolveArtifactPreview: (
        orgId: string,
        conversationId: string,
        source: { artifact_id: string } | { content_id: string },
        signal?: AbortSignal,
      ) =>
        http.request<RunArtifact>(
          `${org(orgId)}/conversations/${encodeURIComponent(conversationId)}/artifact-preview?${new URLSearchParams(source)}`,
          { signal },
        ),
      /** 阶段 10d：取产物内容用于预览（超过 maxBytes 抛 INVALID_RESPONSE；过期 404） */
      artifactBlob: (
        orgId: string,
        runId: string,
        artifactId: string,
        maxBytes: number,
        signal?: AbortSignal,
      ) =>
        http.requestBlob(
          `${runPath(orgId, runId)}/artifacts/${encodeURIComponent(artifactId)}`,
          { maxBytes, signal },
        ),
      /** 阶段 12a：Run 的核对案（待核对原因与人工入口所需的 case id / version） */
      reconciliationCases: async (
        orgId: string,
        runId: string,
        signal?: AbortSignal,
      ) =>
        (
          await http.request<Ok<"listRunReconciliationCases">>(
            `${runPath(orgId, runId)}/reconciliation`,
            { signal },
          )
        ).cases,
      /** 阶段 12a：管理员把待核对的 Run 标为失败；案已被处理时 already_resolved = true */
      markReconciliationFailed: (
        orgId: string,
        runId: string,
        caseId: string,
        body: { version: number; detail?: string },
      ) =>
        http.request<Ok<"markRunReconciliationFailed">>(
          `${runPath(orgId, runId)}/reconciliation/${encodeURIComponent(caseId)}/mark-failed`,
          { method: "POST", body },
        ),
      /** 阶段 12e：本组织待核对的运行（仅组织 OWNER/ADMIN；不依赖会话可见性，不含会话内容）；deviceId 给出时只列该电脑上的 */
      reconcilingRuns: async (
        orgId: string,
        deviceId?: string,
        signal?: AbortSignal,
      ) =>
        (
          await http.request<Ok<"listReconcilingRuns">>(
            `/v1/organizations/${encodeURIComponent(orgId)}/reconciling-runs`,
            { query: { device_id: deviceId ?? "" }, signal },
          )
        ).runs,
      /** 阶段 12e：Run 的核对证据（仅组织 OWNER/ADMIN） */
      reconciliationEvidence: async (
        orgId: string,
        runId: string,
        signal?: AbortSignal,
      ) =>
        (
          await http.request<Ok<"listRunReconciliationEvidence">>(
            `${runPath(orgId, runId)}/reconciliation/evidence`,
            { signal },
          )
        ).evidence,
      /** 阶段 12e：接受已有结果（RESULT_ACCEPTED）；案已被处理时 already_resolved = true */
      acceptReconciliationResult: (
        orgId: string,
        runId: string,
        caseId: string,
        body: { version: number; evidence_id: string },
      ) =>
        http.request<Ok<"resolveRunReconciliationCase">>(
          `${runPath(orgId, runId)}/reconciliation/${encodeURIComponent(caseId)}/resolve`,
          { method: "POST", body: { ...body, resolution: "RESULT_ACCEPTED" } },
        ),
      /** 取消；必须带幂等键，重放返回 replayed = true */
      cancel: (orgId: string, runId: string, idempotencyKey: string) =>
        http.request<Ok<"cancelRun">>(`${runPath(orgId, runId)}/cancel`, {
          method: "POST",
          idempotencyKey,
        }),
    },
  };
}
