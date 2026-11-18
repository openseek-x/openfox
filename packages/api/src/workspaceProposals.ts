import type { HttpClient } from "./http";
import type { components } from "./schema.gen";
type S = components["schemas"];
export type WorkspaceProposal = S["WorkspaceProposal"];
export type WorkspaceProposalDecision = S["WorkspaceProposalDecision"];
export type ProposalSide = "base" | "candidate";
export function createWorkspaceProposalsApi(http: HttpClient) {
  const path = (org: string, conversation: string, id?: string) =>
    `/v1/organizations/${encodeURIComponent(org)}/conversations/${encodeURIComponent(conversation)}/proposals${id ? `/${encodeURIComponent(id)}` : ""}`;
  return {
    workspaceProposals: {
      list: (o: string, c: string, cursor = "", signal?: AbortSignal) =>
        http.request<S["WorkspaceProposalPage"]>(
          `${path(o, c)}${cursor ? `?cursor=${encodeURIComponent(cursor)}` : ""}`,
          { signal, maxResponseBytes: 8 * 1024 * 1024 },
        ),
      get: (o: string, c: string, id: string, signal?: AbortSignal) =>
        http.request<WorkspaceProposal>(path(o, c, id), { signal }),
      decide: (
        o: string,
        c: string,
        id: string,
        decision: WorkspaceProposalDecision,
      ) =>
        http.request<WorkspaceProposal>(`${path(o, c, id)}/decision`, {
          method: "POST",
          body: decision,
        }),
      restore: (
        o: string,
        c: string,
        id: string,
        body: S["WorkspaceProposalRestore"],
      ) =>
        http.request<WorkspaceProposal>(`${path(o, c, id)}/restore`, {
          method: "PUT",
          body,
        }),
      content: (
        o: string,
        c: string,
        id: string,
        side: ProposalSide,
        signal?: AbortSignal,
      ) =>
        http.requestBlob(`${path(o, c, id)}/content?side=${side}`, {
          signal,
          maxBytes: 10 * 1024 * 1024,
        }),
      office: (
        o: string,
        c: string,
        id: string,
        side: ProposalSide,
        signal?: AbortSignal,
      ) =>
        http.request<S["OfficeView"]>(`${path(o, c, id)}/office?side=${side}`, {
          signal,
          maxResponseBytes: 16 * 1024 * 1024,
        }),
      officePreview: (
        o: string,
        c: string,
        id: string,
        side: ProposalSide,
        signal?: AbortSignal,
        format = "pdf",
      ) =>
        http.requestBlob(
          `${path(o, c, id)}/office-preview?side=${side}&format=${format}`,
          { signal, maxBytes: 32 * 1024 * 1024, timeoutMs: 75_000 },
        ),
    },
  };
}
