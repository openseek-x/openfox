import type { HttpClient } from "./http";
import type { components } from "./schema.gen";
type S = components["schemas"];
export type DiscordBot = S["DiscordBot"];
export type DiscordBotsView = S["DiscordBotsView"];
export type DiscordConnectInput = S["DiscordConnectInput"];
export type DiscordPreparation = S["DiscordPreparation"];
export type DiscordIdentity = S["DiscordIdentity"];
export type DiscordBindingSession = S["DiscordBindingSession"];
export type DiscordChannel = S["DiscordChannel"];
export type DiscordChannelInput = S["DiscordChannelInput"];
export type DiscordGuildPage = S["DiscordGuildPage"];
export type DiscordRemoteChannel = S["DiscordRemoteChannel"];
export type DiscordDeliveryPage = S["DiscordDeliveryPage"];
const part = encodeURIComponent;
const base = (org: string) =>
  `/v1/organizations/${part(org)}/integrations/discord`;
export function createDiscordApi(http: HttpClient) {
  const post = <T>(url: string, body: unknown, signal?: AbortSignal) =>
    http.request<T>(url, { method: "POST", body, signal, timeoutMs: 50_000 });
  return {
    discord: {
      bots: (org: string, signal?: AbortSignal) =>
        http.request<DiscordBotsView>(`${base(org)}/bots`, { signal }),
      bot: (org: string, id: string, signal?: AbortSignal) =>
        http.request<DiscordBot>(`${base(org)}/bots/${part(id)}`, { signal }),
      prepare: (
        org: string,
        input: DiscordConnectInput,
        signal?: AbortSignal,
      ) => post<DiscordPreparation>(`${base(org)}/preparations`, input, signal),
      preparation: (org: string, id: string, signal?: AbortSignal) =>
        http.request<DiscordPreparation>(
          `${base(org)}/preparations/${part(id)}`,
          { signal },
        ),
      confirm: (
        org: string,
        id: string,
        input: S["DiscordConfirmInput"],
        signal?: AbortSignal,
      ) =>
        post<DiscordBot>(
          `${base(org)}/preparations/${part(id)}/confirm`,
          input,
          signal,
        ),
      disconnect: (
        org: string,
        id: string,
        version: number,
        signal?: AbortSignal,
      ) =>
        post<void>(
          `${base(org)}/bots/${part(id)}/disconnect`,
          { version },
          signal,
        ),
      personalTargets: (org: string, signal?: AbortSignal) =>
        http.request<DiscordBotsView>(`${base(org)}/personal-targets`, {
          signal,
        }),
      personal: (org: string, signal?: AbortSignal) =>
        http.request<DiscordIdentity[]>(`${base(org)}/personal`, { signal }),
      issueBinding: (org: string, bot: string, signal?: AbortSignal) =>
        post<DiscordBindingSession>(
          `${base(org)}/bots/${part(bot)}/binding-sessions`,
          {},
          signal,
        ),
      binding: (org: string, id: string, signal?: AbortSignal) =>
        http.request<DiscordBindingSession>(
          `${base(org)}/binding-sessions/${part(id)}`,
          { signal },
        ),
      confirmBinding: (
        org: string,
        id: string,
        version: number,
        signal?: AbortSignal,
      ) =>
        post<DiscordIdentity>(
          `${base(org)}/binding-sessions/${part(id)}/confirm`,
          { version },
          signal,
        ),
      unbind: (
        org: string,
        id: string,
        version: number,
        signal?: AbortSignal,
      ) =>
        http.request<void>(`${base(org)}/personal/${part(id)}`, {
          method: "DELETE",
          body: { version },
          signal,
        }),
      guilds: (
        org: string,
        bot: string,
        after?: string,
        signal?: AbortSignal,
      ) =>
        http.request<DiscordGuildPage>(
          `${base(org)}/bots/${part(bot)}/guilds`,
          { query: { after }, signal },
        ),
      remoteChannels: (
        org: string,
        bot: string,
        guild: string,
        signal?: AbortSignal,
      ) =>
        http.request<DiscordRemoteChannel[]>(
          `${base(org)}/bots/${part(bot)}/guilds/${part(guild)}/channels`,
          { signal },
        ),
      channels: (org: string, bot: string, signal?: AbortSignal) =>
        http.request<DiscordChannel[]>(
          `${base(org)}/bots/${part(bot)}/channels`,
          { signal },
        ),
      enableChannel: (
        org: string,
        bot: string,
        input: DiscordChannelInput,
        signal?: AbortSignal,
      ) =>
        post<DiscordChannel>(
          `${base(org)}/bots/${part(bot)}/channels`,
          input,
          signal,
        ),
      disableChannel: (
        org: string,
        bot: string,
        id: string,
        version: number,
        signal?: AbortSignal,
      ) =>
        http.request<void>(
          `${base(org)}/bots/${part(bot)}/channels/${part(id)}`,
          { method: "DELETE", body: { version }, signal },
        ),
      deliveries: (
        org: string,
        bot: string,
        after?: string,
        signal?: AbortSignal,
      ) =>
        http.request<DiscordDeliveryPage>(
          `${base(org)}/bots/${part(bot)}/deliveries`,
          { query: { after }, signal },
        ),
    },
  };
}
