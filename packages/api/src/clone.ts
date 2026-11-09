import type { HttpClient } from "./http";
import type { components } from "./schema.gen";

// 阶段 7a · 克隆空间到另一个组织。所有公开类型取自生成契约。
type S = components["schemas"];

export type CloneTarget = S["CloneTarget"];
export type CloneTargetList = S["CloneTargetList"];
export type CloneRequest = S["CloneRequest"];
export type CloneIssue = S["CloneIssue"];
export type ClonePreview = S["ClonePreview"];
export type CloneJob = S["CloneJob"];
export type CloneJobStatus = CloneJob["status"];
export type CloneJobPhase = CloneJob["phase"];

const spacePath = (orgId: string, spaceId: string) =>
  `/v1/organizations/${encodeURIComponent(orgId)}/spaces/${encodeURIComponent(spaceId)}/clone`;
const jobPath = (orgId: string, jobId: string) =>
  `/v1/organizations/${encodeURIComponent(orgId)}/clone-jobs/${encodeURIComponent(jobId)}`;

/** 克隆空间方法组，由 createApi 展开到顶层。任务挂在目标组织下，get / cancel 的 orgId 是目标组织。 */
export function createCloneApi(http: HttpClient) {
  return {
    spaceClone: {
      targets: (orgId: string, spaceId: string) =>
        http.request<CloneTargetList>(`${spacePath(orgId, spaceId)}/targets`),
      preview: (orgId: string, spaceId: string, input: CloneRequest) =>
        http.request<ClonePreview>(`${spacePath(orgId, spaceId)}/preview`, {
          method: "POST",
          body: input,
          timeoutMs: 60_000,
        }),
      start: (
        orgId: string,
        spaceId: string,
        input: CloneRequest,
        idempotencyKey: string,
      ) =>
        http.request<CloneJob>(spacePath(orgId, spaceId), {
          method: "POST",
          body: input,
          idempotencyKey,
          timeoutMs: 120_000,
        }),
      get: (targetOrgId: string, jobId: string) =>
        http.request<CloneJob>(jobPath(targetOrgId, jobId)),
      cancel: (targetOrgId: string, jobId: string) =>
        http.request<CloneJob>(`${jobPath(targetOrgId, jobId)}/cancel`, {
          method: "POST",
        }),
    },
  };
}
