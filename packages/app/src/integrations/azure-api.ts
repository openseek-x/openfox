import type {
  HttpClient,
  IntegrationConnection,
  ImportJob,
  ImportConfig,
  ImportAnalysis,
  ImportNewSpace,
} from "@openfox/api";
export const azureUUID = (s: string) =>
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(s);
export function azureAuthorizeURL(raw: string) {
  try {
    const u = new URL(raw);
    return (
      u.origin === "https://login.microsoftonline.com" &&
      /^\/[0-9a-f-]{36}\/oauth2\/v2\.0\/authorize$/i.test(u.pathname) &&
      azureUUID(u.pathname.split("/")[1] ?? "") &&
      !u.username &&
      !u.password &&
      !u.hash
    );
  } catch {
    return false;
  }
}
export interface AzureSession {
  id: string;
  purpose: "organization" | "personal" | "reauthorize";
  status: "PENDING" | "SELECTING" | "COMPLETED" | "FAILED" | "EXPIRED";
  error_code: string;
  expires_at: string;
  tenant_id?: string;
  account_id?: string;
  identity?: { id: string; display_name: string };
  accounts: { id: string; name: string }[];
  affected_accounts: { id: string; name: string }[];
  binding_id?: string;
}
export interface AzureProject {
  id: string;
  name: string;
  visibility: string;
  state: string;
}
export interface AzureProjectsPage {
  projects: AzureProject[];
  next_offset: string;
  connection_version: number;
  grant_generation: number;
}
export interface AzureRepository {
  project_id: string;
  project_name: string;
  repository_id: string;
  repository_name: string;
}
export interface AzureHook {
  id: string;
  project_id: string;
  repository_id?: string;
  event_type: string;
  state: string;
  desired: boolean;
  generation: number;
  remote_generation: number;
  last_error: string;
  subscription_id?: string;
  manual_cleanup: boolean;
}
export interface AzureHooksConfig {
  version: number;
  connection_version: number;
  grant_generation: number;
  repositories: AzureRepository[];
  hooks: AzureHook[];
}
export interface AzureBacklink {
  id: string;
  link_id: string;
  project_id: string;
  repository_id: string;
  pr_number: number;
  state: string;
  last_error: string;
  thread_id?: number;
  comment_id?: number;
}
export interface AzureWorkPage {
  work: {
    id: string;
    project_id: string;
    repository_id: string;
    kind: string;
    object_id: number;
    state: string;
    last_error: string;
    version: number;
  }[];
  backlinks: AzureBacklink[];
  next_work_after: string;
  next_backlink_after: string;
}
export interface AzureMetadata {
  types: {
    type: { name: string; referenceName: string };
    states: { name: string; category: string }[];
  }[];
  fields: {
    name: string;
    referenceName: string;
    type: string;
    isIdentity: boolean;
  }[];
  priority_field?: string;
}
export interface AzureSyncMappings {
  version: number;
  types: NonNullable<ImportConfig["types"]>;
  statuses: ImportConfig["statuses"];
  priorities: NonNullable<ImportConfig["priorities"]>;
  analysis: ImportAnalysis;
  space_id: string;
}
const base = (org: string) => `/v1/organizations/${encodeURIComponent(org)}`;
const connection = (org: string, id: string) =>
  `${base(org)}/integrations/azure_devops/connections/${encodeURIComponent(id)}`;
export const azureApi = (http: HttpClient) => ({
  start: (
    org: string,
    input: {
      purpose: AzureSession["purpose"];
      tenant_id?: string;
      connection_id?: string;
      version?: number;
    },
  ) =>
    http.request<{
      session_id: string;
      authorize_url: string;
      expires_at: string;
    }>(`${base(org)}/integrations/azure_devops/oauth/start`, {
      method: "POST",
      body: input,
    }),
  session: (org: string, id: string) =>
    http.request<AzureSession>(
      `${base(org)}/integrations/azure_devops/oauth/sessions/${encodeURIComponent(id)}`,
    ),
  complete: (org: string, id: string, account?: string) =>
    http.request<IntegrationConnection>(
      `${base(org)}/integrations/azure_devops/oauth/sessions/${encodeURIComponent(id)}/complete`,
      {
        method: "POST",
        body: account ? { account_id: account } : {},
        timeoutMs: 45_000,
      },
    ),
  projects: (org: string, id: string, offset: string, signal?: AbortSignal) =>
    http.request<AzureProjectsPage>(
      `${base(org)}/imports/azure_devops/projects`,
      {
        query: { connection_id: id, ...(offset ? { offset } : {}) },
        signal,
        timeoutMs: 45_000,
      },
    ),
  metadata: (org: string, id: string, project: string) =>
    http.request<{ project: AzureProject; metadata: AzureMetadata }>(
      `${base(org)}/imports/azure_devops/metadata`,
      { query: { connection_id: id, project_id: project }, timeoutMs: 45_000 },
    ),
  createImport: (
    org: string,
    id: string,
    project: string,
    target: string | ImportNewSpace,
    history: boolean,
    priority: string,
    key: string,
  ) =>
    http.request<ImportJob>(`${base(org)}/imports/azure_devops/project`, {
      method: "POST",
      body: {
        connection_id: id,
        project_id: project,
        ...(typeof target === "string"
          ? { space_id: target }
          : { new_space: target }),
        include_history: history,
        priority_field: priority,
      },
      idempotencyKey: key,
      timeoutMs: 45_000,
    }),
  config: (org: string, id: string) =>
    http.request<AzureHooksConfig>(`${connection(org, id)}/repositories`),
  availableRepositories: (org: string, id: string, project: string) =>
    http.request<{
      repositories: { id: string; name: string; project: AzureProject }[];
    }>(`${connection(org, id)}/repositories/available`, {
      query: { project_id: project },
      timeoutMs: 45_000,
    }),
  saveRepositories: (
    org: string,
    id: string,
    config: AzureHooksConfig,
    repos: AzureRepository[],
  ) =>
    http.request<AzureHooksConfig>(`${connection(org, id)}/repositories`, {
      method: "PUT",
      body: {
        version: config.version,
        connection_version: config.connection_version,
        grant_generation: config.grant_generation,
        repositories: repos.map((r) => ({
          project_id: r.project_id,
          repository_id: r.repository_id,
        })),
      },
      timeoutMs: 45_000,
    }),
  repair: (org: string, id: string, hook: AzureHook) =>
    http.request<{ scheduled: boolean }>(
      `${connection(org, id)}/hooks/${encodeURIComponent(hook.id)}/repair`,
      {
        method: "POST",
        body: { generation: hook.generation },
        timeoutMs: 45_000,
      },
    ),
  work: (org: string, id: string, workAfter = "", backlinkAfter = "") =>
    http.request<AzureWorkPage>(`${connection(org, id)}/work`, {
      query: { work_after: workAfter, backlink_after: backlinkAfter },
    }),
  retryBacklink: (
    org: string,
    id: string,
    backlink: string,
    config: AzureHooksConfig,
    action: "reconcile" | "resend",
    confirm?: string,
  ) =>
    http.request<{ scheduled: boolean }>(
      `${connection(org, id)}/backlinks/${encodeURIComponent(backlink)}/retry`,
      {
        method: "POST",
        body: {
          action,
          confirm,
          connection_version: config.connection_version,
          grant_generation: config.grant_generation,
        },
      },
    ),
  syncMappings: (org: string, job: string) =>
    http.request<AzureSyncMappings>(
      `${base(org)}/imports/${encodeURIComponent(job)}/azure-mappings`,
    ),
  saveSyncMappings: (
    org: string,
    job: string,
    input: Pick<
      AzureSyncMappings,
      "version" | "types" | "statuses" | "priorities"
    >,
  ) =>
    http.request<ImportJob>(
      `${base(org)}/imports/${encodeURIComponent(job)}/azure-mappings`,
      { method: "PUT", body: input },
    ),
  history: (org: string, job: string, version: number) =>
    http.request<ImportJob>(
      `${base(org)}/imports/${encodeURIComponent(job)}/azure-history`,
      { method: "POST", body: { version } },
    ),
});
export function nextAzurePage(
  last: AzureProjectsPage,
  pages: AzureProjectsPage[],
  params: string[],
) {
  return last.next_offset &&
    pages.length < 50 &&
    pages.reduce((n, p) => n + p.projects.length, 0) < 5000 &&
    !params.includes(last.next_offset)
    ? last.next_offset
    : undefined;
}
