import type { HttpClient } from "./http";
import type { components, operations } from "./schema.gen";

// 阶段 6c · 导入：Jira CSV 导入草稿、配置、开始 / 取消、回滚。所有公开类型取自生成契约。
type S = components["schemas"];

export type TAPDImportCredentials = S["TAPDImportCredentials"];
export type ZenTaoCredentials = S["ZenTaoCredentials"];
export type ImportSource = S["ImportSource"];
export type ImportStatus = S["ImportStatus"];
export type ImportField = S["ImportField"];
export type ImportColumn = S["ImportColumn"];
export type ImportPreview = S["ImportPreview"];
export type ImportColumnMapping = S["ImportColumnMapping"];
export type ImportStatusMapping = S["ImportStatusMapping"];
export type ImportUserMapping = S["ImportUserMapping"];
export type ImportOptions = S["ImportOptions"];
export type ImportConfig = S["ImportConfig"];
export type ImportAnalysis = S["ImportAnalysis"];
export type ImportWarning = S["ImportWarning"];
export type ImportInvitationResult = S["ImportInvitationResult"];
export type ImportJob = S["ImportJob"];
export type ImportRollbackPreview = S["ImportRollbackPreview"];
export type TaskExternalAssignee = S["TaskExternalAssignee"];
export type ImportNewSpace = S["ImportNewSpace"];
export type ImportConnectionErrorReason = S["ImportConnectionErrorReason"];
export type ExternalAccount = S["ExternalAccount"];
export type CreateJiraApiImportInput = NonNullable<
  operations["createJiraApiImport"]["requestBody"]
>["content"]["application/json"];
export type ExternalAccountQuery = NonNullable<
  operations["listExternalAccounts"]["parameters"]["query"]
>;
export type ExternalAccountList = NonNullable<
  operations["listExternalAccounts"]["responses"][200]["content"]
>["application/json"];
export type ExternalAccountLinkChange = NonNullable<
  operations["saveExternalAccountLinks"]["requestBody"]
>["content"]["application/json"]["changes"][number];
export type ExternalAccountSaveResult = NonNullable<
  operations["saveExternalAccountLinks"]["responses"][200]["content"]
>["application/json"];
export type ImportList = NonNullable<
  operations["listImports"]["responses"][200]["content"]
>["application/json"];

const base = (orgId: string) =>
  `/v1/organizations/${encodeURIComponent(orgId)}/imports`;
const externalBase = (orgId: string) =>
  `/v1/organizations/${encodeURIComponent(orgId)}/external-accounts`;
const one = (orgId: string, importId: string) =>
  `${base(orgId)}/${encodeURIComponent(importId)}`;

/** 导入方法组，由 createApi 展开到顶层。 */
export function createImportsApi(http: HttpClient) {
  return {
    imports: {
      setSync: (
        orgId: string,
        id: string,
        state: S["ImportProjectSync"]["state"],
        version: number,
      ) =>
        http.request<ImportJob>(one(orgId, id) + "/sync", {
          method: "PUT",
          body: { state, version },
        }),
      manageSync: (
        orgId: string,
        id: string,
        command: NonNullable<
          operations["updateImportProjectSync"]["requestBody"]
        >["content"]["application/json"],
      ) =>
        http.request<ImportJob>(one(orgId, id) + "/sync", {
          method: "PUT",
          body: command,
        }),
      list: (
        orgId: string,
        query: { limit?: number; cursor?: string; connection_id?: string } = {},
      ) => http.request<ImportList>(base(orgId), { query }),
      tapdSources: (
        orgId: string,
        credentials: TAPDImportCredentials,
        signal?: AbortSignal,
      ) =>
        http.request<{ id: string; name: string }[]>(
          `${base(orgId)}/tapd-sources`,
          { method: "POST", body: { credentials }, signal, timeoutMs: 40000 },
        ),
      zenSources: (orgId: string, credentials: ZenTaoCredentials) =>
        http.request<{ id: string; name: string }[]>(
          `${base(orgId)}/zentao-sources`,
          { method: "POST", body: { credentials }, timeoutMs: 40_000 },
        ),
      fromZenTao: (
        orgId: string,
        credentials: ZenTaoCredentials,
        project: string,
        target: string | ImportNewSpace,
      ) =>
        http.request<ImportJob>(`${base(orgId)}/zentao-import`, {
          method: "POST",
          body: {
            credentials,
            project_id: project,
            ...(typeof target === "string"
              ? { space_id: target }
              : { new_space: target }),
          },
          timeoutMs: 45_000,
        }),
      projects: (orgId: string, provider: string, connectionId: string) =>
        http.request<
          operations["listProviderImportProjects"]["responses"][200]["content"]["application/json"]
        >(`${base(orgId)}/${encodeURIComponent(provider)}/projects`, {
          query: { connection_id: connectionId },
          timeoutMs: 40_000,
        }),
      fromProject: (
        orgId: string,
        provider: string,
        connection: string | TAPDImportCredentials,
        project: string,
        target: string | ImportNewSpace,
      ) =>
        http.request<ImportJob>(
          `${base(orgId)}/${encodeURIComponent(provider)}/project`,
          {
            method: "POST",
            body: {
              ...(typeof connection === "string"
                ? { connection_id: connection }
                : { credentials: connection }),
              project_id: project,
              ...(typeof target === "string"
                ? { space_id: target }
                : { new_space: target }),
            },
            timeoutMs: 45_000,
          },
        ),
      /** 上传 Jira CSV，返回导入草稿（字段 file，加 space_id 或 new_space_*）。 */
      uploadJiraCsv: (
        orgId: string,
        target: string | ImportNewSpace,
        file: File,
        source:
          | "jira_csv"
          | "teambition_csv"
          | "pingcode_csv"
          | "tapd_excel" = "jira_csv",
      ) => {
        const form = new FormData();
        if (typeof target === "string") form.append("space_id", target);
        else {
          form.append("new_space_name", target.name);
          if (target.prefix) form.append("new_space_prefix", target.prefix);
          form.append("new_space_visibility", target.visibility);
        }
        form.append("file", file);
        if (source !== "jira_csv") form.append("source", source);
        return http.request<ImportJob>(
          `${base(orgId)}/${source === "jira_csv" ? "jira-csv" : "csv"}`,
          {
            method: "POST",
            body: form,
            timeoutMs: 60_000,
          },
        );
      },
      /** 用 API 凭证创建导入草稿（后台拉取条目）；凭证只提交这一次。 */
      createJiraApi: (orgId: string, input: CreateJiraApiImportInput) =>
        http.request<ImportJob>(`${base(orgId)}/jira-api`, {
          method: "POST",
          body: input,
          timeoutMs: 45_000,
        }),
      get: (orgId: string, importId: string) =>
        http.request<ImportJob>(one(orgId, importId)),
      putConfig: (
        orgId: string,
        importId: string,
        input: { config_version: number; config: ImportConfig },
      ) =>
        http.request<ImportJob>(`${one(orgId, importId)}/config`, {
          method: "PUT",
          body: input,
        }),
      start: (
        orgId: string,
        importId: string,
        input: {
          config_version: number;
          confirm_overwrite?: boolean;
          sync_ticket?: string;
        },
      ) =>
        http.request<ImportJob>(`${one(orgId, importId)}/start`, {
          method: "POST",
          body: input,
        }),
      cancel: (orgId: string, importId: string, version: number) =>
        http.request<ImportJob>(`${one(orgId, importId)}/cancel`, {
          method: "POST",
          body: { version },
        }),
      rollbackPreview: (orgId: string, importId: string) =>
        http.request<ImportRollbackPreview>(`${one(orgId, importId)}/rollback`),
      rollback: (orgId: string, importId: string, version: number) =>
        http.request<ImportJob>(`${one(orgId, importId)}/rollback`, {
          method: "POST",
          body: { version },
        }),
    },
    externalAccounts: {
      list: (orgId: string, query: ExternalAccountQuery = {}) =>
        http.request<ExternalAccountList>(externalBase(orgId), { query }),
      saveLinks: (orgId: string, changes: ExternalAccountLinkChange[]) =>
        http.request<ExternalAccountSaveResult>(
          `${externalBase(orgId)}/links`,
          { method: "POST", body: { changes } },
        ),
    },
  };
}
