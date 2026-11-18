import type { HttpClient } from "./http";
import type { components, operations } from "./schema.gen";

// 阶段 3c · 专项接口。所有公开类型取自生成契约。
type S = components["schemas"];
type JsonBody<K extends keyof operations> =
  NonNullable<operations[K]["requestBody"]> extends {
    content: { "application/json": infer B };
  }
    ? B
    : never;

export type Project = S["Project"];
export type ProjectStatus = S["ProjectStatus"];
export type ProjectSpace = S["ProjectSpace"];
export type ProjectDistributionItem = S["ProjectDistributionItem"];
export type ProjectCreateInput = JsonBody<"createProject">;
export type ProjectPatchInput = JsonBody<"updateProject">;

const org = (id: string) => `/v1/organizations/${encodeURIComponent(id)}`;
const seg = encodeURIComponent;
const projectPath = (orgId: string, projectId: string) =>
  `${org(orgId)}/projects/${seg(projectId)}`;

export function createProjectsApi(http: HttpClient) {
  return {
    projects: {
      list: (
        orgId: string,
        query: { space_id: string; status?: ProjectStatus; limit?: number },
      ) =>
        http.request<{ projects: Project[]; next_cursor: string }>(
          `${org(orgId)}/projects`,
          { query },
        ),
      get: (orgId: string, projectId: string) =>
        http.request<Project>(projectPath(orgId, projectId)),
      create: (orgId: string, input: ProjectCreateInput) =>
        http.request<Project>(`${org(orgId)}/projects`, {
          method: "POST",
          body: input,
        }),
      update: (orgId: string, projectId: string, input: ProjectPatchInput) =>
        http.request<Project>(projectPath(orgId, projectId), {
          method: "PATCH",
          body: input,
        }),
      remove: (orgId: string, projectId: string, version: number) =>
        http.request<void>(projectPath(orgId, projectId), {
          method: "DELETE",
          query: { version },
        }),
      deleteImpact: (orgId: string, projectId: string) =>
        http.request<{ task_count: number }>(
          `${projectPath(orgId, projectId)}/delete-impact`,
        ),
      addSpace: (orgId: string, projectId: string, spaceId: string) =>
        http.request<void>(`${projectPath(orgId, projectId)}/spaces`, {
          method: "POST",
          body: { space_id: spaceId },
        }),
      removeSpace: (orgId: string, projectId: string, spaceId: string) =>
        http.request<void>(
          `${projectPath(orgId, projectId)}/spaces/${seg(spaceId)}`,
          { method: "DELETE" },
        ),
      setPrimarySpace: (orgId: string, projectId: string, spaceId: string) =>
        http.request<void>(
          `${projectPath(orgId, projectId)}/spaces/${seg(spaceId)}/primary`,
          { method: "PUT" },
        ),
      setLabels: (orgId: string, projectId: string, labelIds: string[]) =>
        http.request<void>(`${projectPath(orgId, projectId)}/labels`, {
          method: "PUT",
          body: { label_ids: labelIds },
        }),
    },
  };
}
