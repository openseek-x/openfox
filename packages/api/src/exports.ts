import type { HttpClient } from "./http";
import type { components } from "./schema.gen";

// 阶段 7b · 导出任务数据。所有公开类型取自生成契约。
type S = components["schemas"];

export type ExportSpace = S["ExportSpace"];
export type ExportSpaceList = S["ExportSpaceList"];
export type ExportRequest = S["ExportRequest"];
export type ExportJob = S["ExportJob"];
export type ExportJobList = S["ExportJobList"];
export type ExportFormat = ExportRequest["format"];
export type ExportStatus = ExportJob["status"];

const base = (orgId: string) =>
  `/v1/organizations/${encodeURIComponent(orgId)}`;

/** 导出任务数据方法组，由 createApi 展开到顶层。 */
export function createExportsApi(http: HttpClient) {
  return {
    exportTasks: {
      spaces: (orgId: string) =>
        http.request<ExportSpaceList>(`${base(orgId)}/export-spaces`),
      list: (orgId: string) =>
        http.request<ExportJobList>(`${base(orgId)}/exports`),
      start: (orgId: string, input: ExportRequest, idempotencyKey: string) =>
        http.request<ExportJob>(`${base(orgId)}/exports`, {
          method: "POST",
          body: input,
          idempotencyKey,
        }),
      get: (orgId: string, exportId: string) =>
        http.request<ExportJob>(
          `${base(orgId)}/exports/${encodeURIComponent(exportId)}`,
        ),
      /** 下载地址：浏览器带登录 cookie 直接访问，响应是 attachment。 */
      downloadUrl: (orgId: string, exportId: string) =>
        http.url(
          `${base(orgId)}/exports/${encodeURIComponent(exportId)}/download`,
        ),
    },
  };
}
