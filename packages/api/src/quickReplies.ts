import type { HttpClient } from "./http";
import type { components } from "./schema.gen";

// 阶段 7d · 快捷回复（个人 + 组织两级）。公开类型取自生成契约。
type S = components["schemas"];

export type QuickReply = S["QuickReply"];
export type QuickReplyList = S["QuickReplyList"];
export type QuickReplyScope = QuickReply["scope"];

/** 每级最多的快捷回复条数（服务端强制）。 */
export const QUICK_REPLY_LIMIT = 50;
export const QUICK_REPLY_TITLE_MAX = 40;
export const QUICK_REPLY_BODY_MAX = 2000;

const base = (orgId: string) =>
  `/v1/organizations/${encodeURIComponent(orgId)}/quick-replies`;

export function createQuickRepliesApi(http: HttpClient) {
  return {
    quickReplies: {
      list: (orgId: string) => http.request<QuickReplyList>(base(orgId)),
      /** id 由客户端生成：超时重试不会重复创建 */
      create: (
        orgId: string,
        input: {
          id?: string;
          scope: QuickReplyScope;
          title: string;
          body: string;
        },
      ) =>
        http.request<QuickReply>(base(orgId), { method: "POST", body: input }),
      update: (
        orgId: string,
        id: string,
        input: { version: number; title?: string; body?: string },
      ) =>
        http.request<QuickReply>(`${base(orgId)}/${encodeURIComponent(id)}`, {
          method: "PATCH",
          body: input,
        }),
      remove: (orgId: string, id: string) =>
        http.request<void>(`${base(orgId)}/${encodeURIComponent(id)}`, {
          method: "DELETE",
        }),
      /** ids 必须是该级别当前全部条目，否则 409 VERSION_CONFLICT */
      reorder: async (orgId: string, scope: QuickReplyScope, ids: string[]) =>
        (
          await http.request<{ quick_replies: QuickReply[] }>(
            `${base(orgId)}/order`,
            { method: "PUT", body: { scope, ids } },
          )
        ).quick_replies,
    },
  };
}
