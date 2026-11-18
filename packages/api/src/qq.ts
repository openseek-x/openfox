import type { HttpClient } from "./http";
import type { components } from "./schema.gen";
type S = components["schemas"];
export type QQConfiguration = S["QQConfiguration"];
export type QQBot = S["QQBot"];
export type QQBotDetail = S["QQBotDetail"];
export type QQCreateBotInput = S["QQCreateBotInput"];
export type QQConnectInput = S["QQConnectInput"];
export type QQQRSession = S["QQQRSession"];
export type QQQRStartInput = S["QQQRStartInput"];
export type QQQRActionInput = S["QQQRActionInput"];
export type QQBinding = S["QQBinding"];
export type QQBindingSession = S["QQBindingSession"];
export type QQStartBindingInput = S["QQStartBindingInput"];
export type QQConfirmBindingInput = S["QQConfirmBindingInput"];
export type QQGroup = S["QQGroup"];
export type QQCandidate = S["QQCandidate"];
export type QQReplyDiagnostic = S["QQReplyDiagnostic"];

const part = encodeURIComponent;
const base = (org: string) => `/v1/organizations/${part(org)}/integrations/qq`;
export function createQQApi(http: HttpClient) {
  const get = <T>(url: string, signal?: AbortSignal) =>
    http.request<T>(url, { signal });
  const post = <T>(url: string, body: unknown, signal?: AbortSignal) =>
    http.request<T>(url, { method: "POST", body, signal, timeoutMs: 35_000 });
  return {
    qq: {
      configuration: (org: string, signal?: AbortSignal) =>
        get<QQConfiguration>(`${base(org)}/configuration`, signal),
      create: (org: string, input: QQCreateBotInput, signal?: AbortSignal) =>
        post<QQBot>(`${base(org)}/bots`, input, signal),
      detail: (org: string, bot: string, signal?: AbortSignal) =>
        get<QQBotDetail>(`${base(org)}/bots/${part(bot)}`, signal),
      credentials: (
        org: string,
        bot: string,
        input: QQConnectInput,
        signal?: AbortSignal,
      ) =>
        post<QQBot>(
          `${base(org)}/bots/${part(bot)}/credentials`,
          input,
          signal,
        ),
      disconnect: (
        org: string,
        bot: string,
        version: number,
        signal?: AbortSignal,
      ) =>
        post<QQBot>(
          `${base(org)}/bots/${part(bot)}/disconnect`,
          { version },
          signal,
        ),
      release: (
        org: string,
        bot: string,
        input: QQQRActionInput,
        signal?: AbortSignal,
      ) => post<QQBot>(`${base(org)}/bots/${part(bot)}/release`, input, signal),
      webhook: (
        org: string,
        bot: string,
        version: number,
        signal?: AbortSignal,
      ) =>
        post<{ url: string }>(
          `${base(org)}/bots/${part(bot)}/webhook`,
          { version },
          signal,
        ),
      startQR: (
        org: string,
        bot: string,
        input: QQQRStartInput,
        signal?: AbortSignal,
      ) =>
        post<QQQRSession>(`${base(org)}/bots/${part(bot)}/qr`, input, signal),
      qr: (org: string, id: string, signal?: AbortSignal) =>
        get<QQQRSession>(`${base(org)}/qr/${part(id)}`, signal),
      cancelQR: (
        org: string,
        id: string,
        input: QQQRActionInput,
        signal?: AbortSignal,
      ) =>
        post<QQQRSession>(`${base(org)}/qr/${part(id)}/cancel`, input, signal),
      recoverQR: (
        org: string,
        id: string,
        version: number,
        signal?: AbortSignal,
      ) =>
        post<QQBot>(`${base(org)}/qr/${part(id)}/recover`, { version }, signal),
      startBinding: (
        org: string,
        bot: string,
        input: QQStartBindingInput,
        signal?: AbortSignal,
      ) =>
        post<QQBindingSession>(
          `${base(org)}/bots/${part(bot)}/binding-sessions`,
          input,
          signal,
        ),
      binding: (org: string, id: string, signal?: AbortSignal) =>
        get<QQBindingSession>(
          `${base(org)}/binding-sessions/${part(id)}`,
          signal,
        ),
      confirmBinding: (
        org: string,
        id: string,
        input: QQConfirmBindingInput,
        signal?: AbortSignal,
      ) =>
        post<QQBindingSession>(
          `${base(org)}/binding-sessions/${part(id)}/confirm`,
          input,
          signal,
        ),
      cancelBinding: (
        org: string,
        id: string,
        version: number,
        signal?: AbortSignal,
      ) =>
        post<QQBindingSession>(
          `${base(org)}/binding-sessions/${part(id)}/cancel`,
          { version },
          signal,
        ),
      revoke: (
        org: string,
        id: string,
        version: number,
        signal?: AbortSignal,
      ) =>
        post<void>(
          `${base(org)}/bindings/${part(id)}/revoke`,
          { version },
          signal,
        ),
      disableGroup: (
        org: string,
        id: string,
        version: number,
        signal?: AbortSignal,
      ) =>
        post<void>(
          `${base(org)}/groups/${part(id)}/disable`,
          { version },
          signal,
        ),
    },
  };
}
