import type { HttpClient } from "./http";

const maxRuntimeSessionPackageBytes = 20 * 1024 * 1024;

export interface RuntimeSessionExport {
  id: string;
  runtime: string;
  status: "PENDING" | "READY" | "FAILED";
  expires_at: string;
  wrapped_key?: string;
  error_code?: string;
}

const conversationExportsPath = (orgId: string, conversationId: string) =>
  `/v1/organizations/${encodeURIComponent(orgId)}/conversations/${encodeURIComponent(conversationId)}/runtime-session-exports`;

export function createRuntimeSessionExportsApi(http: HttpClient) {
  return {
    runtimeSessionExports: {
      create: (orgId: string, conversationId: string, publicKey: string) =>
        http.request<RuntimeSessionExport>(
          conversationExportsPath(orgId, conversationId),
          { method: "POST", body: { public_key: publicKey } },
        ),
      get: (
        orgId: string,
        conversationId: string,
        exportId: string,
        signal?: AbortSignal,
      ) =>
        http.request<RuntimeSessionExport>(
          `${conversationExportsPath(orgId, conversationId)}/${encodeURIComponent(exportId)}`,
          { signal },
        ),
      content: (
        orgId: string,
        conversationId: string,
        exportId: string,
        signal?: AbortSignal,
      ) =>
        http.requestBlob(
          `${conversationExportsPath(orgId, conversationId)}/${encodeURIComponent(exportId)}/content`,
          { maxBytes: maxRuntimeSessionPackageBytes, signal },
        ),
    },
  };
}
