import type { HttpClient } from "./http";
import type { components } from "./schema.gen";
import type { IntegrationConnection, IntegrationWebhook } from "./integrations";

type S = components["schemas"];
export type BitbucketOAuthSession = S["BitbucketOAuthSession"];
export type BitbucketOAuthStartInput = S["BitbucketOAuthStartInput"];
export type BitbucketOAuthCompleteInput = S["BitbucketOAuthCompleteInput"];
export type BitbucketJobDiagnostic = S["BitbucketJobDiagnostic"];
export type BitbucketRetryJobInput = S["BitbucketRetryJobInput"];
export type BitbucketRepository = S["BitbucketRepository"];
export type BitbucketRepositorySelection = S["BitbucketRepositorySelection"];
export type BitbucketRepositoryInput = S["BitbucketRepositoryInput"];
const conn = (org: string, connection: string) =>
  `/v1/organizations/${encodeURIComponent(org)}/integrations/bitbucket/connections/${encodeURIComponent(connection)}`;
const base = (org: string) =>
  `/v1/organizations/${encodeURIComponent(org)}/integrations/bitbucket/oauth`;

export const bitbucketApi = (http: HttpClient) => ({
  jobs: (org: string, connection: string) =>
    http.request<{ jobs: BitbucketJobDiagnostic[] }>(
      `${conn(org, connection)}/jobs`,
    ),
  retryJob: (org: string, connection: string, input: BitbucketRetryJobInput) =>
    http.request<void>(`${conn(org, connection)}/jobs/retry`, {
      method: "POST",
      body: input,
    }),
  selections: (org: string, connection: string) =>
    http.request<S["BitbucketRepositorySelections"]>(
      `${conn(org, connection)}/repositories`,
    ),
  repositories: (
    org: string,
    connection: string,
    cursor: string,
    signal?: AbortSignal,
  ) =>
    http.request<S["BitbucketRepositoriesPage"]>(
      `${conn(org, connection)}/repositories/available`,
      { query: cursor ? { cursor } : undefined, signal, timeoutMs: 40_000 },
    ),
  saveRepository: (
    org: string,
    connection: string,
    input: BitbucketRepositoryInput,
  ) =>
    http.request<BitbucketRepositorySelection>(
      `${conn(org, connection)}/repositories`,
      { method: "PUT", body: input, timeoutMs: 40_000 },
    ),
  removeRepository: (
    org: string,
    connection: string,
    selection: string,
    version: number,
  ) =>
    http.request<BitbucketRepositorySelection>(
      `${conn(org, connection)}/repositories/${encodeURIComponent(selection)}`,
      { method: "DELETE", body: { version } },
    ),
  webhook: (org: string, connection: string) =>
    http.request<IntegrationWebhook>(`${conn(org, connection)}/webhook`),
  rotateWebhook: (org: string, connection: string, version: number) =>
    http.request<IntegrationWebhook>(
      `${conn(org, connection)}/webhook/rotate`,
      { method: "POST", body: { version, confirm: true } },
    ),
  start: (org: string, input: BitbucketOAuthStartInput) =>
    http.request<S["BitbucketOAuthStart"]>(`${base(org)}/start`, {
      method: "POST",
      body: input,
    }),
  session: (org: string, id: string) =>
    http.request<BitbucketOAuthSession>(
      `${base(org)}/sessions/${encodeURIComponent(id)}`,
    ),
  complete: (org: string, id: string, input: BitbucketOAuthCompleteInput) =>
    http.request<IntegrationConnection>(
      `${base(org)}/sessions/${encodeURIComponent(id)}/complete`,
      {
        method: "POST",
        body: input,
        timeoutMs: 40_000,
      },
    ),
});
