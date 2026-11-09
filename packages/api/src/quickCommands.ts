import type { HttpClient } from "./http";
import type { components } from "./schema.gen";

// 阶段 9f · 任务快捷指令（按空间，私有 + 共享两组）。公开类型取自生成契约。
type S = components["schemas"];

export type QuickCommand = S["QuickCommand"];
export type QuickCommandList = S["QuickCommandList"];
export type QuickCommandTarget = S["QuickCommandTarget"];
export type QuickCommandVisibility = S["QuickCommandVisibility"];
export type QuickCommandCreateInput = S["QuickCommandCreateRequest"];
export type QuickCommandUpdateInput = S["QuickCommandUpdateRequest"];

/** 每组最多的快捷指令条数（服务端强制）。 */
export const QUICK_COMMAND_LIMIT = 50;
export const QUICK_COMMAND_NAME_MAX = 100;
export const QUICK_COMMAND_PROMPT_MAX = 4000;

const base = (orgId: string, spaceId: string) =>
  `/v1/organizations/${encodeURIComponent(orgId)}/spaces/${encodeURIComponent(spaceId)}/quick-commands`;

export function createQuickCommandsApi(http: HttpClient) {
  return {
    quickCommands: {
      list: (orgId: string, spaceId: string) =>
        http.request<QuickCommandList>(base(orgId, spaceId)),
      /** id 由客户端生成：超时重试不会重复创建 */
      create: (
        orgId: string,
        spaceId: string,
        input: QuickCommandCreateInput,
      ) =>
        http.request<QuickCommand>(base(orgId, spaceId), {
          method: "POST",
          body: input,
        }),
      update: (
        orgId: string,
        spaceId: string,
        id: string,
        input: QuickCommandUpdateInput,
      ) =>
        http.request<QuickCommand>(
          `${base(orgId, spaceId)}/${encodeURIComponent(id)}`,
          { method: "PATCH", body: input },
        ),
      remove: (orgId: string, spaceId: string, id: string) =>
        http.request<void>(
          `${base(orgId, spaceId)}/${encodeURIComponent(id)}`,
          {
            method: "DELETE",
          },
        ),
      /** ids 必须恰好是该分组里本人可编辑的全部条目，否则 409 VERSION_CONFLICT */
      reorder: async (
        orgId: string,
        spaceId: string,
        visibility: QuickCommandVisibility,
        ids: string[],
      ) =>
        (
          await http.request<{ quick_commands: QuickCommand[] }>(
            `${base(orgId, spaceId)}/order`,
            { method: "PUT", body: { visibility, ids } },
          )
        ).quick_commands,
    },
  };
}
