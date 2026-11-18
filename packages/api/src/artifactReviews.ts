import type { HttpClient } from "./http";
import type { components } from "./schema.gen";
type S = components["schemas"];
export type OfficeView = S["OfficeView"];
export type OfficeUnit = S["OfficeUnit"];
export type ArtifactReviewAnchor = S["ArtifactReviewAnchor"];
export type ArtifactReviewFile = S["ArtifactReviewFile"];
export type ArtifactAnnotation = S["ArtifactAnnotation"];
export type FileComment = S["FileComment"];
export type FileCommentContext = S["FileCommentContext"];
const org = (id: string) => `/v1/organizations/${encodeURIComponent(id)}`;
export function createArtifactReviewsApi(http: HttpClient) {
  return {
    artifactReviews: {
      officeWorkbook: (
        o: string,
        run: string,
        artifact: string,
        signal?: AbortSignal,
      ) =>
        http.requestBlob(
          `${org(o)}/runs/${encodeURIComponent(run)}/artifacts/${encodeURIComponent(artifact)}/office-preview?format=xlsx`,
          { signal, maxBytes: 10 * 1024 * 1024, timeoutMs: 30_000 },
        ),
      officePreview: (
        o: string,
        run: string,
        artifact: string,
        signal?: AbortSignal,
      ) =>
        http.requestBlob(
          `${org(o)}/runs/${encodeURIComponent(run)}/artifacts/${encodeURIComponent(artifact)}/office-preview`,
          { signal, maxBytes: 32 * 1024 * 1024, timeoutMs: 75_000 },
        ),
      office: (
        o: string,
        run: string,
        artifact: string,
        signal?: AbortSignal,
      ) =>
        http.request<OfficeView>(
          `${org(o)}/runs/${encodeURIComponent(run)}/artifacts/${encodeURIComponent(artifact)}/office`,
          { signal, maxResponseBytes: 16 * 1024 * 1024 },
        ),
    },
  };
}
