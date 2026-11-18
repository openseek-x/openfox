import type { HttpClient } from "./http";
import type { components } from "./schema.gen";
type S = components["schemas"];
export type TeamsBot = S["TeamsBot"];
export type TeamsConnectInput = S["TeamsConnectInput"];
export type TeamsCredentialsInput = S["TeamsCredentialsInput"];
export type TeamsManifestInput = S["TeamsManifestInput"];
export type TeamsPersonalTarget = S["TeamsPersonalTarget"];
export type TeamsPersonalBinding = S["TeamsPersonalBinding"];
export type TeamsBindingChallenge = S["TeamsBindingChallenge"];
export type TeamsScope = S["TeamsScope"];
export type TeamsDelivery = S["TeamsDelivery"];
const base = (org: string) =>
  `/v1/organizations/${encodeURIComponent(org)}/integrations/teams`;
const bot = (org: string, id: string) =>
  `${base(org)}/bots/${encodeURIComponent(id)}`;
/** Secret inputs and one-time command responses must remain outside query caches. */
export const teamsApi = (http: HttpClient) => ({
  bots: (org: string, signal?: AbortSignal) =>
    http.request<S["TeamsBotsView"]>(`${base(org)}/bots`, { signal }),
  bot: (org: string, id: string, signal?: AbortSignal) =>
    http.request<TeamsBot>(bot(org, id), { signal }),
  create: (org: string, input: TeamsConnectInput, signal?: AbortSignal) =>
    http.request<TeamsBot>(`${base(org)}/bots`, {
      method: "POST",
      body: input,
      signal,
      timeoutMs: 35_000,
    }),
  credentials: (
    org: string,
    id: string,
    input: TeamsCredentialsInput,
    signal?: AbortSignal,
  ) =>
    http.request<TeamsBot>(`${bot(org, id)}/credentials`, {
      method: "PUT",
      body: input,
      signal,
      timeoutMs: 35_000,
    }),
  remove: (org: string, id: string, version: number, signal?: AbortSignal) =>
    http.request<void>(bot(org, id), {
      method: "DELETE",
      body: { version },
      signal,
    }),
  manifest: (org: string, id: string, signal?: AbortSignal) =>
    http.requestBlob(`${bot(org, id)}/manifest.zip`, {
      maxBytes: 256 * 1024,
      signal,
    }),
  targets: (org: string, signal?: AbortSignal) =>
    http.request<TeamsPersonalTarget[]>(`${base(org)}/personal-targets`, {
      signal,
    }),
  personal: (org: string, signal?: AbortSignal) =>
    http.request<TeamsPersonalBinding[]>(`${base(org)}/personal-bindings`, {
      signal,
    }),
  issue: (org: string, id: string, signal?: AbortSignal) =>
    http.request<TeamsBindingChallenge>(`${bot(org, id)}/binding-challenges`, {
      method: "POST",
      signal,
    }),
  unbind: (org: string, id: string, version: number, signal?: AbortSignal) =>
    http.request<void>(`${bot(org, id)}/personal-binding`, {
      method: "DELETE",
      body: { version },
      signal,
    }),
  scopes: (org: string, id: string, after = "", signal?: AbortSignal) =>
    http.request<S["TeamsScopesPage"]>(`${bot(org, id)}/scopes`, {
      query: { after },
      signal,
    }),
  setScope: (
    org: string,
    id: string,
    scope: string,
    input: S["TeamsScopeInput"],
    signal?: AbortSignal,
  ) =>
    http.request<TeamsScope>(
      `${bot(org, id)}/scopes/${encodeURIComponent(scope)}`,
      { method: "PUT", body: input, signal, timeoutMs: 35_000 },
    ),
  deliveries: (org: string, id: string, signal?: AbortSignal) =>
    http.request<TeamsDelivery[]>(`${bot(org, id)}/deliveries`, { signal }),
});
