import type { HttpClient } from "./http";
import type { components } from "./schema.gen";

type S = components["schemas"];
export type SlackConfiguration = S["SlackConfiguration"];
export type SlackApp = S["SlackApp"];
export type SlackIdentity = S["SlackIdentity"];
export type SlackOAuthInput = S["SlackOAuthInput"];
export type SlackOAuthSession = S["SlackOAuthSession"];
export type SlackCreateAppInput = S["SlackCreateAppInput"];
export type SlackVersionInput = S["SlackVersionInput"];
export type SlackRecoverAppInput = S["SlackRecoverAppInput"];
export type SlackConfigureInput = S["SlackConfigureInput"];
export type SlackCancelInput = S["SlackCancelInput"];
export type SlackChannel = S["SlackChannel"];
export type SlackChannelInput = S["SlackChannelInput"];
export type SlackChannelsPage = S["SlackChannelsPage"];
export type SlackDelivery = S["SlackDelivery"];
export type SlackDeliveriesPage = S["SlackDeliveriesPage"];
export type SlackResolveDeliveryInput = S["SlackResolveDeliveryInput"];
const part = encodeURIComponent;
const base = (org: string) =>
  `/v1/organizations/${part(org)}/integrations/slack`;
export function createSlackApi(http: HttpClient) {
  const post = <T>(url: string, body: unknown, signal?: AbortSignal) =>
    http.request<T>(url, { method: "POST", body, signal, timeoutMs: 35_000 });
  return {
    slack: {
      configuration: (org: string, signal?: AbortSignal) =>
        http.request<SlackConfiguration>(`${base(org)}/configuration`, {
          signal,
        }),
      startOAuth: (org: string, input: SlackOAuthInput, signal?: AbortSignal) =>
        post<SlackOAuthSession>(`${base(org)}/oauth/start`, input, signal),
      session: (org: string, id: string, signal?: AbortSignal) =>
        http.request<SlackOAuthSession>(
          `${base(org)}/oauth/sessions/${part(id)}`,
          { signal },
        ),
      createApp: (
        org: string,
        input: SlackCreateAppInput,
        signal?: AbortSignal,
      ) => post<SlackApp>(`${base(org)}/apps/operations`, input, signal),
      operation: (org: string, id: string, signal?: AbortSignal) =>
        http.request<SlackApp>(`${base(org)}/apps/operations/${part(id)}`, {
          signal,
        }),
      cancel: (
        org: string,
        id: string,
        input: SlackCancelInput,
        signal?: AbortSignal,
      ) =>
        post<SlackApp>(
          `${base(org)}/apps/operations/${part(id)}/cancel`,
          input,
          signal,
        ),
      recover: (
        org: string,
        id: string,
        input: SlackRecoverAppInput,
        signal?: AbortSignal,
      ) =>
        post<SlackApp>(
          `${base(org)}/apps/operations/${part(id)}/recover`,
          input,
          signal,
        ),
      configure: (
        org: string,
        id: string,
        input: SlackConfigureInput,
        signal?: AbortSignal,
      ) =>
        post<SlackApp>(
          `${base(org)}/apps/operations/${part(id)}/configure`,
          input,
          signal,
        ),
      reauthorize: (
        org: string,
        id: string,
        input: SlackOAuthInput,
        signal?: AbortSignal,
      ) =>
        post<SlackOAuthSession>(
          `${base(org)}/installations/${part(id)}/reauthorize`,
          input,
          signal,
        ),
      disconnect: (
        org: string,
        id: string,
        input: SlackVersionInput,
        signal?: AbortSignal,
      ) =>
        post<SlackApp>(
          `${base(org)}/installations/${part(id)}/disconnect`,
          input,
          signal,
        ),
      unbind: (
        org: string,
        id: string,
        input: SlackVersionInput,
        signal?: AbortSignal,
      ) =>
        http.request<void>(`${base(org)}/personal/${part(id)}`, {
          method: "DELETE",
          body: input,
          signal,
        }),
      channels: (org: string, id: string, cursor = "", signal?: AbortSignal) =>
        http.request<SlackChannelsPage>(
          `${base(org)}/bots/${part(id)}/channels`,
          { query: { cursor }, signal },
        ),
      channel: (
        org: string,
        id: string,
        channel: string,
        input: SlackChannelInput,
        signal?: AbortSignal,
      ) =>
        http.request<SlackChannel>(
          `${base(org)}/bots/${part(id)}/channels/${part(channel)}`,
          { method: "PATCH", body: input, signal, timeoutMs: 35_000 },
        ),
      resolveDelivery: (
        org: string,
        appID: string,
        deliveryID: string,
        input: SlackResolveDeliveryInput,
        signal?: AbortSignal,
      ) =>
        post<SlackDelivery>(
          `${base(org)}/apps/${part(appID)}/deliveries/${part(deliveryID)}/resolve`,
          input,
          signal,
        ),
      deliveries: (
        org: string,
        id: string,
        cursor = "",
        signal?: AbortSignal,
      ) =>
        http.request<SlackDeliveriesPage>(
          `${base(org)}/apps/${part(id)}/deliveries`,
          { query: { cursor }, signal },
        ),
    },
  };
}
