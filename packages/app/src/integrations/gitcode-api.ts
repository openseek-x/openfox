import type {
  HttpClient,
  IntegrationConnection,
  IntegrationSyncLink,
  IntegrationRepository,
} from "@openfox/api";
export interface GitCodeRepository {
  id: string;
  connection_id: string;
  sync_link_id: string;
  remote_repository_id: string;
  path: string;
  visibility: "public" | "private";
  active: boolean;
  hook_generation: number;
  hook_id: string;
  hook_state: string;
  error_code: string;
  version: number;
  watermark: string;
  issue_write_status: "UNVERIFIED" | "VERIFIED" | "DENIED";
  comment_write_status: "UNVERIFIED" | "VERIFIED" | "DENIED";
}
export interface GitCodeWork {
  id: string;
  repository_id: string;
  kind: string;
  state: string;
  remote_id: string;
  error_code: string;
  next_attempt_at: string | null;
}
export interface GitCodeConfiguration {
  version: number;
  subject: string;
  login: string;
  organization_login: string;
  repositories: GitCodeRepository[];
  links: IntegrationSyncLink[];
  work: GitCodeWork[];
  next_repository_after: string;
  next_work_before: string;
}
export interface GitCodeDiscovery {
  principal: { id: string; login: string; name: string };
  organizations: { id: string; login: string; name: string }[];
}
const base = (org: string) =>
  `/v1/organizations/${encodeURIComponent(org)}/integrations`;
const conn = (org: string, id: string) =>
  `${base(org)}/connections/${encodeURIComponent(id)}/gitcode`;
export const gitcodeApi = (http: HttpClient) => ({
  discover: (org: string, token: string) =>
    http.request<GitCodeDiscovery>(`${base(org)}/gitcode/organizations`, {
      method: "POST",
      body: { access_token: token },
      timeoutMs: 45_000,
    }),
  save: (
    org: string,
    token: string,
    organization: string,
    connection?: { id: string; version: number },
  ) =>
    http.request<IntegrationConnection>(
      connection
        ? `${conn(org, connection.id)}/credentials`
        : `${base(org)}/gitcode/connections`,
      {
        method: connection ? "PUT" : "POST",
        body: {
          access_token: token,
          external_organization_id: organization,
          ...(connection ? { version: connection.version } : {}),
        },
        timeoutMs: 45_000,
      },
    ),
  config: (org: string, id: string, repositoryAfter = "", workBefore = "") =>
    http.request<GitCodeConfiguration>(`${conn(org, id)}/configuration`, {
      query: { repository_after: repositoryAfter, work_before: workBefore },
    }),
  repositories: (org: string, id: string, q: string, page: number) =>
    http.request<{ repositories: IntegrationRepository[]; has_more: boolean }>(
      `${conn(org, id)}/repositories`,
      { query: { q, page }, timeoutMs: 45_000 },
    ),
  add: (
    org: string,
    id: string,
    input: {
      version: number;
      repository_id: string;
      repository: string;
      space_id: string;
      direction: "INBOUND" | "BIDIRECTIONAL";
      import_existing: boolean;
    },
  ) =>
    http.request<IntegrationSyncLink>(`${conn(org, id)}/repositories`, {
      method: "POST",
      body: input,
      timeoutMs: 45_000,
    }),
  remove: (
    org: string,
    id: string,
    repository: GitCodeRepository,
    deleteHook: boolean,
  ) =>
    http.request<void>(
      `${conn(org, id)}/repositories/${encodeURIComponent(repository.id)}`,
      {
        method: "DELETE",
        body: { version: repository.version, delete_remote_hook: deleteHook },
      },
    ),
  history: (org: string, id: string, repository: GitCodeRepository) =>
    http.request<void>(
      `${conn(org, id)}/repositories/${encodeURIComponent(repository.id)}/history`,
      { method: "POST", body: { version: repository.version } },
    ),
  reconcile: (org: string, id: string, work: string, version: number) =>
    http.request<void>(
      `${conn(org, id)}/work/${encodeURIComponent(work)}/reconcile`,
      { method: "POST", body: { version } },
    ),
});
