import type { HttpClient } from "./http";
import type { components, operations } from "./schema.gen";

// 阶段 3b · 迭代接口。所有公开类型取自生成契约。
type S = components["schemas"];
type JsonBody<K extends keyof operations> =
  NonNullable<operations[K]["requestBody"]> extends {
    content: { "application/json": infer B };
  }
    ? B
    : never;
type Query<K extends keyof operations> = NonNullable<
  operations[K]["parameters"]["query"]
>;

export type Iteration = S["Iteration"];
export type IterationLink = S["IterationLink"];
export type IterationSettings = S["IterationSettings"];
export type IterationMode = S["IterationMode"];
export type IterationStatus = S["IterationStatus"];
export type IterationSettingsInput = JsonBody<"updateIterationSettings">;
export type IterationCreateInput = JsonBody<"createIteration">;
export type IterationPatchInput = JsonBody<"updateIteration">;
export type IterationDatesInput = JsonBody<"updateIterationDates">;
export type IterationCompleteInput = JsonBody<"completeIteration">;
export type IterationTaskBatchInput = JsonBody<"updateIterationTasks">;
export type IterationLinkInput = JsonBody<"createIterationLink">;
export type IterationListQuery = Query<"listIterations">;

const org = (id: string) => `/v1/organizations/${encodeURIComponent(id)}`;
const seg = encodeURIComponent;
const spacePath = (orgId: string, spaceId: string) =>
  `${org(orgId)}/spaces/${seg(spaceId)}`;
const iterationPath = (orgId: string, spaceId: string, iterationId: string) =>
  `${spacePath(orgId, spaceId)}/iterations/${seg(iterationId)}`;

export function createIterationsApi(http: HttpClient) {
  return {
    iterationSettings: {
      get: (orgId: string, spaceId: string) =>
        http.request<IterationSettings>(
          `${spacePath(orgId, spaceId)}/iteration-settings`,
        ),
      update: (orgId: string, spaceId: string, input: IterationSettingsInput) =>
        http.request<IterationSettings>(
          `${spacePath(orgId, spaceId)}/iteration-settings`,
          { method: "PATCH", body: input },
        ),
    },
    iterations: {
      list: async (
        orgId: string,
        spaceId: string,
        query: IterationListQuery = {},
      ) =>
        http.request<{ iterations: Iteration[]; next_cursor: string }>(
          `${spacePath(orgId, spaceId)}/iterations`,
          { query },
        ),
      get: (orgId: string, spaceId: string, iterationId: string) =>
        http.request<Iteration>(iterationPath(orgId, spaceId, iterationId)),
      create: (orgId: string, spaceId: string, input: IterationCreateInput) =>
        http.request<Iteration>(`${spacePath(orgId, spaceId)}/iterations`, {
          method: "POST",
          body: input,
        }),
      update: (
        orgId: string,
        spaceId: string,
        iterationId: string,
        input: IterationPatchInput,
      ) =>
        http.request<Iteration>(iterationPath(orgId, spaceId, iterationId), {
          method: "PATCH",
          body: input,
        }),
      updateDates: (
        orgId: string,
        spaceId: string,
        iterationId: string,
        input: IterationDatesInput,
      ) =>
        http.request<Iteration>(
          `${iterationPath(orgId, spaceId, iterationId)}/dates`,
          { method: "POST", body: input },
        ),
      remove: (
        orgId: string,
        spaceId: string,
        iterationId: string,
        version: number,
      ) =>
        http.request<void>(iterationPath(orgId, spaceId, iterationId), {
          method: "DELETE",
          query: { version },
        }),
      start: (
        orgId: string,
        spaceId: string,
        iterationId: string,
        version: number,
      ) =>
        http.request<Iteration>(
          `${iterationPath(orgId, spaceId, iterationId)}/start`,
          { method: "POST", body: { version } },
        ),
      complete: (
        orgId: string,
        spaceId: string,
        iterationId: string,
        input: IterationCompleteInput,
      ) =>
        http.request<Iteration>(
          `${iterationPath(orgId, spaceId, iterationId)}/complete`,
          { method: "POST", body: input },
        ),
      updateTasks: (
        orgId: string,
        spaceId: string,
        iterationId: string,
        input: IterationTaskBatchInput,
      ) =>
        http.request<{ updated: number }>(
          `${iterationPath(orgId, spaceId, iterationId)}/tasks`,
          { method: "POST", body: input },
        ),
      addLink: (
        orgId: string,
        spaceId: string,
        iterationId: string,
        input: IterationLinkInput,
      ) =>
        http.request<IterationLink>(
          `${iterationPath(orgId, spaceId, iterationId)}/links`,
          { method: "POST", body: input },
        ),
      removeLink: (
        orgId: string,
        spaceId: string,
        iterationId: string,
        linkId: string,
      ) =>
        http.request<void>(
          `${iterationPath(orgId, spaceId, iterationId)}/links/${seg(linkId)}`,
          { method: "DELETE" },
        ),
    },
  };
}
