import type { HttpClient } from "./http";
import type { components } from "./schema.gen";

// 阶段 12e · 组织运维：审计日志与资源配额。公开类型取自生成契约。
type S = components["schemas"];

export type AuditEvent = S["AuditEvent"];
export type RuntimePolicy = S["RuntimePolicy"];
export type RuntimeConfig = S["RuntimeConfig"];
export type RuntimeUsage = S["RuntimeUsage"];

export interface AuditEventPage {
  events: AuditEvent[];
  /** 没有更多时为 null */
  next_cursor: string | null;
}

export interface AuditEventQuery {
  limit?: number;
  cursor?: string;
  actor_id?: string;
  /** 精确匹配；以 * 结尾表示前缀匹配 */
  action?: string;
  resource_kind?: string;
  /** RFC 3339；范围左闭右开 */
  from?: string;
  to?: string;
}

const org = (orgId: string) => `/v1/organizations/${encodeURIComponent(orgId)}`;

export function createGovernanceApi(http: HttpClient) {
  return {
    auditEvents: {
      /** 仅组织 OWNER/ADMIN；空字符串的筛选项不会发送 */
      list: (orgId: string, query: AuditEventQuery, signal?: AbortSignal) =>
        http.request<AuditEventPage>(`${org(orgId)}/audit-events`, {
          query: {
            limit: query.limit ?? "",
            cursor: query.cursor ?? "",
            actor_id: query.actor_id ?? "",
            action: query.action ?? "",
            resource_kind: query.resource_kind ?? "",
            from: query.from ?? "",
            to: query.to ?? "",
          },
          signal,
        }),
    },
    runtimeConfig: {
      get: (orgId: string, signal?: AbortSignal) =>
        http.request<RuntimeConfig>(`${org(orgId)}/runtime-config`, { signal }),
      /** 整份策略替换；expected_version 不是当前版本时返回 409 VERSION_CONFLICT */
      update: (
        orgId: string,
        body: { expected_version: number; policy: RuntimePolicy },
      ) =>
        http.request<RuntimeConfig>(`${org(orgId)}/runtime-config`, {
          method: "PATCH",
          body,
        }),
      /** 当前用量；任何组织成员可读 */
      usage: (orgId: string, signal?: AbortSignal) =>
        http.request<RuntimeUsage>(`${org(orgId)}/runtime-config/usage`, {
          signal,
        }),
    },
  };
}
