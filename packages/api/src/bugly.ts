import type { HttpClient } from "./http";
import type { components } from "./schema.gen";

type S = components["schemas"];
export type BuglyConfiguration = S["BuglyConfiguration"];
export type BuglyBinding = S["BuglyBinding"];
export type BuglyDelivery = S["BuglyDelivery"];
export type BuglyBindingCreate = S["BuglyBindingCreate"];
export type BuglyBindingUpdate = S["BuglyBindingUpdate"];
export type BuglySecret = S["BuglySecret"];
export type BuglyConnectionAction =
  "enable" | "disable" | "rotate" | "disconnect";
export type BuglyBindingAction = "pause" | "resume" | "remove";

const segment = encodeURIComponent;
const base = (orgId: string) =>
  `/v1/organizations/${segment(orgId)}/integrations/bugly`;
const connection = (orgId: string, id: string) =>
  `${base(orgId)}/connections/${segment(id)}`;

export function createBuglyApi(http: HttpClient) {
  return {
    bugly: {
      configuration: (orgId: string, signal?: AbortSignal) =>
        http.request<BuglyConfiguration>(`${base(orgId)}/configuration`, {
          signal,
        }),
      create: (orgId: string) =>
        http.request<S["IntegrationConnection"]>(`${base(orgId)}/connections`, {
          method: "POST",
          body: {},
        }),
      change: (
        orgId: string,
        id: string,
        action: BuglyConnectionAction,
        version: number,
      ) =>
        http.request<S["IntegrationConnection"]>(
          `${connection(orgId, id)}/${action}`,
          { method: "POST", body: { version } },
        ),
      // Call only from an explicit user action. Keep the secret out of query
      // and mutation caches; the server emits no-store / no-referrer headers.
      secret: (
        orgId: string,
        id: string,
        version: number,
        signal?: AbortSignal,
      ) =>
        http.request<BuglySecret>(`${connection(orgId, id)}/secret`, {
          method: "POST",
          body: { version },
          signal,
        }),
      createBinding: (orgId: string, id: string, input: BuglyBindingCreate) =>
        http.request<BuglyBinding>(`${connection(orgId, id)}/bindings`, {
          method: "POST",
          body: input,
        }),
      updateBinding: (
        orgId: string,
        id: string,
        bindingId: string,
        input: BuglyBindingUpdate,
      ) =>
        http.request<BuglyBinding>(
          `${connection(orgId, id)}/bindings/${segment(bindingId)}`,
          { method: "PATCH", body: input },
        ),
      changeBinding: (
        orgId: string,
        id: string,
        bindingId: string,
        action: BuglyBindingAction,
        version: number,
      ) =>
        http.request<BuglyBinding>(
          `${connection(orgId, id)}/bindings/${segment(bindingId)}/${action}`,
          { method: "POST", body: { version } },
        ),
    },
  };
}
