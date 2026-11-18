import type { HttpClient } from "./http";
import type { components } from "./schema.gen";
type S = components["schemas"];
export type ZohoSession = S["ZohoOAuthSession"];
export type ZohoStartInput = S["ZohoOAuthStartInput"];
export type ZohoProjectsPage = S["ZohoProjectsPage"];
const base = (org: string) => `/v1/organizations/${encodeURIComponent(org)}`;
export const zohoApi = (http: HttpClient) => ({
  start: (org: string, input: ZohoStartInput) =>
    http.request<S["ZohoOAuthStart"]>(
      `${base(org)}/integrations/zoho_bugtracker/oauth/start`,
      { method: "POST", body: input },
    ),
  session: (org: string, id: string, signal?: AbortSignal) =>
    http.request<ZohoSession>(
      `${base(org)}/integrations/zoho_bugtracker/oauth/sessions/${encodeURIComponent(id)}`,
      { signal },
    ),
  complete: (org: string, id: string, portal?: string) =>
    http.request<S["IntegrationConnection"]>(
      `${base(org)}/integrations/zoho_bugtracker/oauth/sessions/${encodeURIComponent(id)}/complete`,
      {
        method: "POST",
        body: portal ? { portal_id: portal } : {},
        timeoutMs: 40_000,
      },
    ),
  projects: (
    org: string,
    connection: string,
    index: number,
    signal?: AbortSignal,
  ) =>
    http.request<ZohoProjectsPage>(
      `${base(org)}/imports/zoho_bugtracker/projects`,
      {
        query: { connection_id: connection, index },
        signal,
        timeoutMs: 40_000,
      },
    ),
  createImport: (
    org: string,
    connection: string,
    project: string,
    target: string | S["ImportNewSpace"],
    key: string,
  ) =>
    http.request<S["ImportJob"]>(
      `${base(org)}/imports/zoho_bugtracker/project`,
      {
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
      },
    ),
});
export function zohoAuthorizeURL(raw: string): boolean {
  try {
    const u = new URL(raw);
    return (
      u.origin === "https://accounts.zoho.com" &&
      u.pathname === "/oauth/v2/auth" &&
      !u.username &&
      !u.password &&
      !u.hash
    );
  } catch {
    return false;
  }
}
