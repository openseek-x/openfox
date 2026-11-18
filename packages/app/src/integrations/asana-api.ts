import type {
  HttpClient,
  IntegrationConnection,
  ImportJob,
  ImportNewSpace,
  ImportConfig,
} from "@openfox/api";

export type AsanaPurpose = "organization" | "personal" | "reauthorize";
export interface AsanaSession {
  id: string;
  purpose: AsanaPurpose;
  status: "PENDING" | "SELECTING" | "COMPLETED" | "FAILED" | "EXPIRED";
  error_code: string;
  expires_at: string;
  identity?: {
    gid: string;
    name: string;
    email: string;
    workspaces?: { gid: string; name: string }[];
  };
  binding_id?: string;
  workspace_id?: string;
  affected_workspaces: string[];
}
export interface AsanaProjectsPage {
  projects: { id: string; name: string; key: string; description?: string }[];
  next_offset: string;
}
export interface AsanaSyncTypes {
  version: number;
  types: NonNullable<ImportConfig["types"]>;
  sources: { source: string; count: number }[];
  space_id: string;
}
const base = (org: string) => `/v1/organizations/${encodeURIComponent(org)}`;

// The shared HTTP client supplies same-origin cookies, CSRF and bounded requests.
export const asanaApi = (http: HttpClient) => ({
  syncTypes: (org: string, job: string) =>
    http.request<AsanaSyncTypes>(
      `${base(org)}/imports/${encodeURIComponent(job)}/asana/types`,
    ),
  saveSyncTypes: (
    org: string,
    job: string,
    version: number,
    types: AsanaSyncTypes["types"],
  ) =>
    http.request<ImportJob>(
      `${base(org)}/imports/${encodeURIComponent(job)}/asana/types`,
      { method: "PUT", body: { version, types } },
    ),
  start: (
    org: string,
    input: { purpose: AsanaPurpose; connection_id?: string; version?: number },
  ) =>
    http.request<{
      session_id: string;
      authorize_url: string;
      expires_at: string;
    }>(`${base(org)}/integrations/asana/oauth/start`, {
      method: "POST",
      body: input,
    }),
  session: (org: string, id: string) =>
    http.request<AsanaSession>(
      `${base(org)}/integrations/asana/oauth/sessions/${encodeURIComponent(id)}`,
    ),
  complete: (org: string, id: string, workspace?: string) =>
    http.request<IntegrationConnection>(
      `${base(org)}/integrations/asana/oauth/sessions/${encodeURIComponent(id)}/complete`,
      {
        method: "POST",
        body: workspace ? { workspace_id: workspace } : {},
        timeoutMs: 40_000,
      },
    ),
  projects: (
    org: string,
    connection: string,
    offset: string,
    signal?: AbortSignal,
  ) =>
    http.request<AsanaProjectsPage>(`${base(org)}/imports/asana/projects`, {
      query: { connection_id: connection, ...(offset ? { offset } : {}) },
      signal,
      timeoutMs: 40_000,
    }),
  createImport: (
    org: string,
    connection: string,
    project: string,
    target: string | ImportNewSpace,
    key: string,
  ) =>
    http.request<ImportJob>(`${base(org)}/imports/asana/project`, {
      method: "POST",
      body: {
        connection_id: connection,
        project_id: project,
        ...(typeof target === "string"
          ? { space_id: target }
          : { new_space: target }),
      },
      idempotencyKey: key,
      timeoutMs: 45_000,
    }),
});

export function asanaAuthorizeURL(raw: string): boolean {
  try {
    const url = new URL(raw);
    return (
      url.origin === "https://app.asana.com" &&
      url.pathname === "/-/oauth_authorize" &&
      !url.username &&
      !url.password &&
      !url.hash
    );
  } catch {
    return false;
  }
}
