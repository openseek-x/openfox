import type { HttpClient } from "./http";
import type { components } from "./schema.gen";
type S = components["schemas"];
export type CodeCommitCredentialInput = S["CodeCommitCredentialInput"];
export type CodeCommitRepository = S["CodeCommitRepository"];
export type CodeCommitConfiguration = S["CodeCommitConfiguration"];
export type CodeCommitRepositoryPage = S["CodeCommitRepositoryPage"];
export type CodeCommitRepositoryInput = S["CodeCommitRepositoryInput"];
export type CodeCommitVersionInput = S["CodeCommitVersionInput"];
export type CodeCommitBridgeResult = S["CodeCommitBridgeResult"];
export type CodeCommitJob = S["CodeCommitJob"];
export type CodeCommitJobPage = S["CodeCommitJobPage"];
export type CodeCommitReconcileInput = S["CodeCommitReconcileInput"];
const part = encodeURIComponent;
const base = (org: string) => `/v1/organizations/${part(org)}/integrations`;
const connection = (org: string, id: string) =>
  `${base(org)}/connections/${part(id)}/codecommit`;
export function createCodeCommitApi(http: HttpClient) {
  return {
    codecommit: {
      regions: (org: string, signal?: AbortSignal) =>
        http.request<{ regions: string[] }>(`${base(org)}/codecommit/regions`, {
          signal,
        }),
      create: (
        org: string,
        input: CodeCommitCredentialInput,
        signal?: AbortSignal,
      ) =>
        http.request<S["IntegrationConnection"]>(
          `${base(org)}/codecommit/connections`,
          { method: "POST", body: input, signal, timeoutMs: 35_000 },
        ),
      credentials: (
        org: string,
        id: string,
        input: CodeCommitCredentialInput,
        signal?: AbortSignal,
      ) =>
        http.request<S["IntegrationConnection"]>(
          `${connection(org, id)}/credentials`,
          { method: "PUT", body: input, signal, timeoutMs: 35_000 },
        ),
      configuration: (
        org: string,
        id: string,
        after = "",
        signal?: AbortSignal,
      ) =>
        http.request<CodeCommitConfiguration>(
          `${connection(org, id)}/configuration`,
          { query: { repository_after: after }, signal },
        ),
      repositories: (
        org: string,
        id: string,
        cursor = "",
        signal?: AbortSignal,
      ) =>
        http.request<CodeCommitRepositoryPage>(
          `${connection(org, id)}/repositories`,
          { query: { cursor }, signal, timeoutMs: 35_000 },
        ),
      enable: (
        org: string,
        id: string,
        input: CodeCommitRepositoryInput,
        signal?: AbortSignal,
      ) =>
        http.request<CodeCommitBridgeResult>(
          `${connection(org, id)}/repositories`,
          { method: "POST", body: input, signal, timeoutMs: 35_000 },
        ),
      disable: (
        org: string,
        id: string,
        repositoryId: string,
        input: CodeCommitVersionInput,
        signal?: AbortSignal,
      ) =>
        http.request<CodeCommitBridgeResult>(
          `${connection(org, id)}/repositories/${part(repositoryId)}/disable`,
          { method: "POST", body: input, signal },
        ),
      reconcileRepository: (
        org: string,
        id: string,
        repositoryId: string,
        input: CodeCommitVersionInput,
        signal?: AbortSignal,
      ) =>
        http.request<CodeCommitBridgeResult>(
          `${connection(org, id)}/repositories/${part(repositoryId)}/reconcile`,
          { method: "POST", body: input, signal },
        ),
      rotate: (
        org: string,
        id: string,
        repositoryId: string,
        input: CodeCommitVersionInput,
        signal?: AbortSignal,
      ) =>
        http.request<CodeCommitBridgeResult>(
          `${connection(org, id)}/repositories/${part(repositoryId)}/bridge-token`,
          { method: "POST", body: input, signal },
        ),
      jobs: (org: string, id: string, before = "", signal?: AbortSignal) =>
        http.request<CodeCommitJobPage>(`${connection(org, id)}/work`, {
          query: { before },
          signal,
        }),
      reconcile: (
        org: string,
        id: string,
        jobId: string,
        input: CodeCommitReconcileInput,
        signal?: AbortSignal,
      ) =>
        http.request<{ reconciled: boolean }>(
          `${connection(org, id)}/work/${part(jobId)}/reconcile`,
          { method: "POST", body: input, signal },
        ),
    },
  };
}
