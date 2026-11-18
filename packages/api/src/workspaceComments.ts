import type { HttpClient } from "./http";
import type { components } from "./schema.gen";
type S = components["schemas"];
export type AnchorResolution = S["AnchorResolution"];
export type ResolveAnchorRequest = S["ResolveAnchorRequest"];
export type WorkspaceCommentThread = S["WorkspaceCommentThread"];
export type WorkspaceCommentSource = S["WorkspaceCommentSource"];
export type WorkspaceCommentMutation = S["WorkspaceCommentMutation"];
export type WorkspaceCommentPost = S["WorkspaceCommentPost"];
export function createWorkspaceCommentsApi(http: HttpClient) {
  const path = (org: string, conversation: string) =>
    `/v1/organizations/${encodeURIComponent(org)}/conversations/${encodeURIComponent(conversation)}/comments`;
  return {
    workspaceComments: {
      resolve: (
        org: string,
        conversation: string,
        thread: string,
        id: string,
        input: ResolveAnchorRequest,
      ) =>
        http.request<AnchorResolution>(
          `${path(org, conversation)}/${encodeURIComponent(thread)}/resolutions/${encodeURIComponent(id)}`,
          { method: "PUT", body: input, timeoutMs: 60000 },
        ),
      list: (
        org: string,
        conversation: string,
        cursor = "",
        signal?: AbortSignal,
      ) =>
        http.request<S["WorkspaceCommentPage"]>(
          `${path(org, conversation)}${cursor ? `?cursor=${encodeURIComponent(cursor)}` : ""}`,
          { signal, maxResponseBytes: 4 * 1024 * 1024 },
        ),
      create: (
        org: string,
        conversation: string,
        id: string,
        source: WorkspaceCommentSource,
      ) =>
        http.request<WorkspaceCommentThread>(
          `${path(org, conversation)}/${encodeURIComponent(id)}`,
          { method: "PUT", body: source },
        ),
      update: (
        org: string,
        conversation: string,
        id: string,
        mutation: WorkspaceCommentMutation,
      ) =>
        http.request<WorkspaceCommentThread>(
          `${path(org, conversation)}/${encodeURIComponent(id)}`,
          { method: "PATCH", body: mutation },
        ),
      screenshot: (org: string, id: string) =>
        http.requestBlob(
          `/v1/organizations/${encodeURIComponent(org)}/attachments/${encodeURIComponent(id)}`,
          { maxBytes: 4 * 1024 * 1024 },
        ),
    },
  };
}
