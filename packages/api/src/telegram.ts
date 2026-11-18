import type { HttpClient } from "./http";
import type { components } from "./schema.gen";
type S = components["schemas"];
export type TelegramBot = S["TelegramBot"];
export type TelegramBotsView = S["TelegramBotsView"];
export type TelegramConnectInput = S["TelegramConnectInput"];
export type TelegramPreparation = S["TelegramPreparation"];
export type TelegramConfirmInput = S["TelegramConfirmInput"];
export type TelegramVersionInput = S["TelegramVersionInput"];
export type TelegramDisconnectInput = S["TelegramDisconnectInput"];
export type TelegramIdentity = S["TelegramIdentity"];
export type TelegramPersonalTarget = S["TelegramPersonalTarget"];
export type TelegramBindingSession = S["TelegramBindingSession"];
export type TelegramGroup = S["TelegramGroup"];
export type TelegramGroupDiscovery = S["TelegramGroupDiscovery"];
export type TelegramGroupInput = S["TelegramGroupInput"];
export type TelegramDeliveryDiagnostic = S["TelegramDeliveryDiagnostic"];
const part = encodeURIComponent;
const base = (org: string) =>
  `/v1/organizations/${part(org)}/integrations/telegram`;
const bot = (org: string, id: string) => `${base(org)}/bots/${part(id)}`;
/** No mutations use a query cache; callers own the lifetime of secret responses. */
export const telegramApi = (http: HttpClient) => ({
  bots: (org: string, signal?: AbortSignal) =>
    http.request<TelegramBotsView>(`${base(org)}/bots`, { signal }),
  bot: (org: string, id: string, signal?: AbortSignal) =>
    http.request<TelegramBot>(bot(org, id), { signal }),
  prepare: (org: string, input: TelegramConnectInput, signal?: AbortSignal) =>
    http.request<TelegramPreparation>(`${base(org)}/preparations`, {
      method: "POST",
      body: input,
      signal,
      timeoutMs: 35_000,
    }),
  preparation: (org: string, id: string, signal?: AbortSignal) =>
    http.request<TelegramPreparation>(`${base(org)}/preparations/${part(id)}`, {
      signal,
    }),
  confirm: (
    org: string,
    id: string,
    input: TelegramConfirmInput,
    signal?: AbortSignal,
  ) =>
    http.request<TelegramBot>(`${base(org)}/preparations/${part(id)}/confirm`, {
      method: "POST",
      body: input,
      signal,
    }),
  reconcile: (
    org: string,
    id: string,
    input: TelegramVersionInput,
    signal?: AbortSignal,
  ) =>
    http.request<void>(`${bot(org, id)}/reconcile`, {
      method: "POST",
      body: input,
      signal,
    }),
  disconnect: (
    org: string,
    id: string,
    input: TelegramDisconnectInput,
    signal?: AbortSignal,
  ) =>
    http.request<void>(`${bot(org, id)}/disconnect`, {
      method: "POST",
      body: input,
      signal,
    }),
  targets: (org: string, signal?: AbortSignal) =>
    http.request<TelegramPersonalTarget[]>(`${base(org)}/personal-targets`, {
      signal,
    }),
  personal: (org: string, signal?: AbortSignal) =>
    http.request<TelegramIdentity[]>(`${base(org)}/personal`, { signal }),
  issue: (org: string, id: string, signal?: AbortSignal) =>
    http.request<TelegramBindingSession>(`${bot(org, id)}/binding-sessions`, {
      method: "POST",
      signal,
    }),
  session: (org: string, id: string, signal?: AbortSignal) =>
    http.request<TelegramBindingSession>(
      `${base(org)}/binding-sessions/${part(id)}`,
      { signal },
    ),
  confirmIdentity: (
    org: string,
    id: string,
    input: TelegramVersionInput,
    signal?: AbortSignal,
  ) =>
    http.request<TelegramIdentity>(
      `${base(org)}/binding-sessions/${part(id)}/confirm`,
      { method: "POST", body: input, signal },
    ),
  unbind: (
    org: string,
    id: string,
    input: TelegramVersionInput,
    signal?: AbortSignal,
  ) =>
    http.request<void>(`${base(org)}/personal/${part(id)}`, {
      method: "DELETE",
      body: input,
      signal,
    }),
  discoveries: (org: string, id: string, signal?: AbortSignal) =>
    http.request<TelegramGroupDiscovery[]>(
      `${bot(org, id)}/group-discoveries`,
      { signal },
    ),
  groups: (org: string, id: string, after = "", signal?: AbortSignal) =>
    http.request<TelegramGroup[]>(`${bot(org, id)}/groups`, {
      query: { after },
      signal,
    }),
  saveGroup: (
    org: string,
    id: string,
    input: TelegramGroupInput,
    signal?: AbortSignal,
  ) =>
    http.request<TelegramGroup>(`${bot(org, id)}/groups`, {
      method: "POST",
      body: input,
      signal,
      timeoutMs: 35_000,
    }),
  deliveries: (org: string, id: string, after = "", signal?: AbortSignal) =>
    http.request<TelegramDeliveryDiagnostic[]>(`${bot(org, id)}/deliveries`, {
      query: { after },
      signal,
    }),
});
