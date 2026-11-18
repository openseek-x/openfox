import type { HttpClient } from "./http";
import type { components, operations } from "./schema.gen";

// 阶段 3c · 视图与图标接口。所有公开类型取自生成契约。
type S = components["schemas"];
type JsonBody<K extends keyof operations> =
  NonNullable<operations[K]["requestBody"]> extends {
    content: { "application/json": infer B };
  }
    ? B
    : never;

export type View = S["View"];
export type ViewVisibility = S["ViewVisibility"];
export type ViewIconKind = S["ViewIconKind"];
export type ViewMode = "list" | "board" | "table";
export type UploadedIcon = S["UploadedIcon"];
export type ViewCreateInput = JsonBody<"createView">;
export type ViewPatchInput = JsonBody<"updateView">;

const org = (id: string) => `/v1/organizations/${encodeURIComponent(id)}`;
const seg = encodeURIComponent;
const viewPath = (orgId: string, viewId: string) =>
  `${org(orgId)}/views/${seg(viewId)}`;

export function createViewsApi(http: HttpClient) {
  return {
    views: {
      list: async (orgId: string, spaceId: string) =>
        (
          await http.request<{ views: View[] }>(
            `${org(orgId)}/spaces/${seg(spaceId)}/views`,
          )
        ).views,
      get: (orgId: string, viewId: string) =>
        http.request<View>(viewPath(orgId, viewId)),
      create: (orgId: string, spaceId: string, input: ViewCreateInput) =>
        http.request<View>(`${org(orgId)}/spaces/${seg(spaceId)}/views`, {
          method: "POST",
          body: input,
        }),
      update: (orgId: string, viewId: string, input: ViewPatchInput) =>
        http.request<View>(viewPath(orgId, viewId), {
          method: "PATCH",
          body: input,
        }),
      remove: (orgId: string, viewId: string, version: number) =>
        http.request<void>(viewPath(orgId, viewId), {
          method: "DELETE",
          query: { version },
        }),
      pinMine: (orgId: string, viewId: string, pinned: boolean) =>
        http.request<void>(`${viewPath(orgId, viewId)}/pin-mine`, {
          method: "PUT",
          body: { pinned },
        }),
      reorderMine: (orgId: string, spaceId: string, viewIds: string[]) =>
        http.request<void>(
          `${org(orgId)}/spaces/${seg(spaceId)}/views/reorder-mine`,
          { method: "PUT", body: { view_ids: viewIds } },
        ),
      setModeMine: (orgId: string, viewId: string, mode: ViewMode | null) =>
        http.request<void>(`${viewPath(orgId, viewId)}/view-mode-mine`, {
          method: "PUT",
          body: { view_mode: mode },
        }),
    },
    icons: {
      upload: (orgId: string, file: File) => {
        const body = new FormData();
        body.append("file", file);
        return http.request<UploadedIcon>(`${org(orgId)}/icons`, {
          method: "POST",
          body,
        });
      },
    },
  };
}
