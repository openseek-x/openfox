import type { HttpClient } from "./http";
import type { components } from "./schema.gen";
type S = components["schemas"];
export type DirectChat = S["DirectChat"];
export type DirectChatMessage = S["DirectChatMessage"];
export type DirectChatFile = S["DirectChatFile"];
export function createDirectChatApi(http: HttpClient) {
  const base = (org: string) =>
    `/v1/organizations/${encodeURIComponent(org)}/direct-chats`;
  const chat = (org: string, id: string) =>
    `${base(org)}/${encodeURIComponent(id)}`;
  const file = (org: string, id: string, fid: string) =>
    `${chat(org, id)}/attachments/${encodeURIComponent(fid)}`;
  return {
    directChats: {
      list: (org: string, before?: string) =>
        http.request<S["DirectChatPage"]>(base(org), {
          query: { before, limit: 50 },
        }),
      ensure: (org: string, user: string) =>
        http.request<DirectChat>(base(org), {
          method: "POST",
          body: { user_id: user },
        }),
      get: (org: string, id: string) => http.request<DirectChat>(chat(org, id)),
      messages: (
        org: string,
        id: string,
        before?: number | { around: string } | { after: number },
      ) =>
        http.request<S["DirectChatMessages"]>(`${chat(org, id)}/messages`, {
          query: {
            ...(typeof before === "object" ? before : { before }),
            limit: 50,
          },
        }),
      send: (org: string, id: string, input: S["DirectChatSend"]) =>
        http.request<DirectChatMessage>(`${chat(org, id)}/messages`, {
          method: "POST",
          body: input,
        }),
      read: (org: string, id: string, seq: number) =>
        http.request<void>(`${chat(org, id)}/read`, {
          method: "POST",
          body: { seq },
        }),
      upload: (org: string, id: string, value: File) => {
        const body = new FormData();
        body.set("file", value);
        return http.request<DirectChatFile>(`${chat(org, id)}/attachments`, {
          method: "POST",
          body,
        });
      },
      removeFile: (org: string, id: string, fid: string) =>
        http.request<void>(file(org, id, fid), { method: "DELETE" }),
      fileUrl: (org: string, id: string, fid: string, preview = false) =>
        http.url(
          `${file(org, id, fid)}${preview ? "?disposition=inline" : ""}`,
        ),
    },
  };
}
