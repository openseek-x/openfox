import type { HttpClient } from "./http";
import type { components } from "./schema.gen";

// 阶段 5d · 市场：全平台共用的只读目录（技能、Agent 模板、精选包、分类）与文件预览。所有公开类型取自生成契约。
type S = components["schemas"];

export type MarketAgentTarget = S["MarketAgentTarget"];
export type MarketText = S["MarketText"];
export type MarketCategory = S["MarketCategory"];
export type MarketPack = S["MarketPack"];
export type MarketCatalog = S["MarketCatalog"];
export type MarketSkill = S["MarketSkill"];
export type MarketSkillFileEntry = S["MarketSkillFileEntry"];
export type MarketSkillDetail = S["MarketSkillDetail"];
export type MarketSkillFileContent = S["MarketSkillFileContent"];
export type MarketTemplate = S["MarketTemplate"];
export type MarketTemplateSkillRef = S["MarketTemplateSkillRef"];
export type MarketTemplateDetail = S["MarketTemplateDetail"];
export type MarketTemplateApplyResult = S["MarketTemplateApplyResult"];

export type MarketSkillSort = "default" | "downloads" | "newest" | "name";

export interface MarketSkillQuery {
  category?: string;
  q?: string;
  sort?: MarketSkillSort;
  limit?: number;
  cursor?: string;
}

export interface MarketTemplateQuery {
  category?: string;
  q?: string;
  limit?: number;
  cursor?: string;
}

export interface MarketPage<T> {
  items: T[];
  next_cursor: string | null;
  total: number;
}

const base = (orgId: string) =>
  `/v1/organizations/${encodeURIComponent(orgId)}/market`;
const skillPath = (orgId: string, id: string) =>
  `${base(orgId)}/skills/${encodeURIComponent(id)}`;

/** 市场方法组，由 createApi 展开到顶层。 */
export function createMarketApi(http: HttpClient) {
  return {
    market: {
      agentTargets: (orgId: string, q: string, after: string) =>
        http.request<{ items: MarketAgentTarget[]; next_cursor: string }>(
          `${base(orgId)}/agent-targets`,
          { query: { q: q || undefined, after: after || undefined } },
        ),
      catalog: (orgId: string) =>
        http.request<MarketCatalog>(`${base(orgId)}/catalog`),
      skills: (orgId: string, query: MarketSkillQuery = {}) =>
        http.request<MarketPage<MarketSkill>>(`${base(orgId)}/skills`, {
          query: { ...query },
        }),
      /** 按 ID 批量取（≤ 100）。 */
      skillsByIds: (orgId: string, ids: readonly string[]) =>
        http.request<MarketPage<MarketSkill>>(`${base(orgId)}/skills`, {
          query: { ids: ids.join(",") },
        }),
      skill: (orgId: string, id: string) =>
        http.request<MarketSkillDetail>(skillPath(orgId, id)),
      skillFile: (orgId: string, id: string, path: string) =>
        http.request<MarketSkillFileContent>(`${skillPath(orgId, id)}/file`, {
          query: { path },
        }),
      /** 下载地址（附件下载，直接作为链接 href）。 */
      downloadUrl: (orgId: string, id: string, path: string) =>
        http.url(`${skillPath(orgId, id)}/download`, { path }),
      /** 取文件原文为 Blob（图片预览）。 */
      downloadBlob: (
        orgId: string,
        id: string,
        path: string,
        maxBytes: number,
      ) =>
        http.requestBlob(`${skillPath(orgId, id)}/download`, {
          query: { path },
          maxBytes,
        }),
      templates: (orgId: string, query: MarketTemplateQuery = {}) =>
        http.request<MarketPage<MarketTemplate>>(`${base(orgId)}/templates`, {
          query: { ...query },
        }),
      template: (orgId: string, id: string) =>
        http.request<MarketTemplateDetail>(
          `${base(orgId)}/templates/${encodeURIComponent(id)}`,
        ),
    },
  };
}
