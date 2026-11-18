import type { HttpClient } from "./http";
import type { components } from "./schema.gen";

// 阶段 12g · 分享快照。公开类型取自生成契约。
type S = components["schemas"];

export type ShareSnapshotMeta = S["ShareSnapshotMeta"];
export type ShareSnapshotCreated = S["ShareSnapshotCreated"];
export type ShareSnapshotCreateRequest = S["ShareSnapshotCreateRequest"];
export type ShareSnapshotView = S["ShareSnapshotView"];
export type ShareSnapshotItem = S["ShareSnapshotItem"];
export type ShareScope = ShareSnapshotMeta["scope"];
export type ShareExpiry = NonNullable<ShareSnapshotCreateRequest["expiry"]>;

/** 脱敏命中位置（服务端 422 SENSITIVE_CONTENT_DETECTED 的 details.findings），不含命中原文。 */
export interface ShareFinding {
  rule: string;
  /** 0 表示标题，其余为条目序号（从 1 开始） */
  item: number;
  line: number;
  column: number;
}

const base = (orgId: string) =>
  `/v1/organizations/${encodeURIComponent(orgId)}/share-snapshots`;

export function createShareSnapshotsApi(http: HttpClient) {
  return {
    shareSnapshots: {
      list: async (orgId: string) =>
        (
          await http.request<{ share_snapshots: ShareSnapshotMeta[] }>(
            base(orgId),
          )
        ).share_snapshots,
      create: (
        orgId: string,
        input: ShareSnapshotCreateRequest,
        idempotencyKey?: string,
      ) =>
        http.request<ShareSnapshotCreated>(base(orgId), {
          method: "POST",
          body: input,
          idempotencyKey,
        }),
      recover: (orgId: string, id: string) =>
        http.request<ShareSnapshotCreated>(
          `${base(orgId)}/${encodeURIComponent(id)}/link`,
        ),
      revoke: (orgId: string, id: string) =>
        http.request<ShareSnapshotMeta>(
          `${base(orgId)}/${encodeURIComponent(id)}`,
          { method: "DELETE" },
        ),
      /** 访问者读取：令牌放请求体而不是 URL，避免进入访问日志与 Referer。 */
      view: (token: string) =>
        http.request<ShareSnapshotView>("/v1/share-snapshots/view", {
          method: "POST",
          body: { token },
        }),
    },
  };
}

/** 从 ApiError.details 取脱敏命中列表。 */
export function shareFindings(details?: Record<string, unknown>) {
  const raw = details?.findings;
  return Array.isArray(raw) ? (raw as ShareFinding[]) : [];
}
