import type { HttpClient } from "./http";
import type { components } from "./schema.gen";

// 阶段 8e · 全局搜索（命令面板）。所有公开类型取自生成契约。
type S = components["schemas"];

export type SearchType = S["SearchType"];
export type SearchItem = S["SearchItem"];
export type SearchGroup = S["SearchGroup"];
export type SearchResponse = S["SearchResponse"];

export interface SearchQuery {
  q: string;
  /** 缺省为全部类型 */
  types?: readonly SearchType[];
  spaceId?: string;
  spaceIds?: readonly string[];
  offset?: number;
  filters?: Record<string, string | readonly string[]>;
  /** 每类最多条数，默认 5；分页模式上限 50 */
  limit?: number;
}

export function createSearchApi(http: HttpClient) {
  return {
    /** 全局搜索：专属任务与专项支持空关键词；每类结果已按调用者的读权限过滤 */
    search: {
      query: (orgId: string, query: SearchQuery, signal?: AbortSignal) =>
        http.request<SearchResponse>(
          `/v1/organizations/${encodeURIComponent(orgId)}/search`,
          {
            query: {
              ...query.filters,
              q: query.q,
              space_ids: query.spaceIds,
              offset: query.offset,
              types:
                query.types && query.types.length ? query.types : undefined,
              space_id: query.spaceId,
              limit: query.limit,
            },
            signal,
          },
        ),
    },
  };
}
