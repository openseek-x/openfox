import type { HttpClient } from "./http";
import type { components, operations } from "./schema.gen";

// 阶段 3a · 空间设置与生命周期。类型全部来自 schema.gen.ts，不手写。

type S = components["schemas"];
type JsonBody<K extends keyof operations> =
  NonNullable<operations[K]["requestBody"]> extends {
    content: { "application/json": infer B };
  }
    ? B
    : never;

export type SpaceDetail = S["SpaceDetail"];
export type SpacePurpose = S["SpacePurpose"];
export type SpaceStatus = S["SpaceStatus"];
export type SpaceRole = S["SpaceMemberRole"];
export type SpaceFeatures = S["SpaceFeatures"];
export type SpaceEstimation = S["SpaceEstimation"];
export type SpaceEstimationScale = S["EstimationScale"];
export type SpaceImpact = S["SpaceImpact"];
export type SpaceRepo = S["SpaceRepo"];
export type SpaceMemberDetail = S["SpaceMemberDetail"];
export type TaskTypeTemplate = S["TaskTypeTemplate"];
export type SpaceIdentifierCheck = S["SpaceIdentifierCheck"];
export type SpaceEstimationPreview = S["SpaceEstimationPreview"];
export type EstimateConversion = S["EstimateConversion"];
export type RetireDisposition = S["SpaceRetireRequest"]["open_tasks"];

export type CreateSpaceInput = JsonBody<"createSpace">;
export type UpdateSpaceInput = JsonBody<"updateSpace">;
export type RetireSpaceInput = JsonBody<"retireSpace">;
export type DeleteSpaceInput = JsonBody<"deleteSpace">;
export type SpaceRepoInput = JsonBody<"createSpaceRepo">;
export type SpaceRepoPatch = JsonBody<"updateSpaceRepo">;
export type UpdateEstimationInput = JsonBody<"updateSpaceEstimation">;
export type CreateSpaceTaskTypeInput = JsonBody<"createSpaceTaskType">;
export type UpdateSpaceTaskTypeInput = JsonBody<"updateSpaceTaskType">;

const org = (id: string) => `/v1/organizations/${encodeURIComponent(id)}`;
const seg = encodeURIComponent;
const sp = (orgId: string, spaceId: string) =>
  `${org(orgId)}/spaces/${seg(spaceId)}`;

/** 空间设置方法组，由 createApi 展开到顶层。 */
export function createSpacesApi(http: HttpClient) {
  return {
    spaces: {
      /** 默认只返回 active；retired = 已停用，all = 全部 */
      list: async (orgId: string, status?: "retired" | "all") =>
        (
          await http.request<{ spaces: SpaceDetail[] }>(
            `${org(orgId)}/spaces`,
            {
              query: status ? { status } : undefined,
            },
          )
        ).spaces,
      get: (orgId: string, spaceId: string) =>
        http.request<SpaceDetail>(sp(orgId, spaceId)),
      create: (
        orgId: string,
        input: CreateSpaceInput,
        idempotencyKey?: string,
      ) =>
        http.request<SpaceDetail>(`${org(orgId)}/spaces`, {
          method: "POST",
          body: input,
          idempotencyKey,
        }),
      update: (orgId: string, spaceId: string, input: UpdateSpaceInput) =>
        http.request<SpaceDetail>(sp(orgId, spaceId), {
          method: "PATCH",
          body: input,
        }),
      changeIdentifier: (
        orgId: string,
        spaceId: string,
        input: { version: number; prefix: string },
      ) =>
        http.request<SpaceDetail>(`${sp(orgId, spaceId)}/identifier`, {
          method: "POST",
          body: input,
        }),
      /** 标识符可用性预检；修改时传 spaceId，本空间的现值与历史值视为可用 */
      checkIdentifier: (orgId: string, prefix: string, spaceId?: string) =>
        http.request<SpaceIdentifierCheck>(
          `${org(orgId)}/space-identifiers/${seg(prefix)}`,
          { query: spaceId ? { space_id: spaceId } : undefined },
        ),
      impact: (orgId: string, spaceId: string) =>
        http.request<SpaceImpact>(`${sp(orgId, spaceId)}/impact`),
      retire: (orgId: string, spaceId: string, input: RetireSpaceInput) =>
        http.request<S["SpaceRetireResult"]>(`${sp(orgId, spaceId)}/retire`, {
          method: "POST",
          body: input,
        }),
      restore: (orgId: string, spaceId: string, input: { version: number }) =>
        http.request<SpaceDetail>(`${sp(orgId, spaceId)}/restore`, {
          method: "POST",
          body: input,
        }),
      remove: (orgId: string, spaceId: string, input: DeleteSpaceInput) =>
        http.request<S["SpaceDeleteResult"]>(sp(orgId, spaceId), {
          method: "DELETE",
          body: input,
        }),
      /** 当前用户的完整空间顺序 */
      reorder: (orgId: string, spaceIds: string[]) =>
        http.request<void>(`${org(orgId)}/space-order`, {
          method: "PUT",
          body: { space_ids: spaceIds },
        }),
      updateEstimation: (
        orgId: string,
        spaceId: string,
        input: UpdateEstimationInput,
      ) =>
        http.request<{ space: SpaceDetail; converted_tasks: number }>(
          `${sp(orgId, spaceId)}/estimation`,
          { method: "PATCH", body: input },
        ),
      estimationPreview: (
        orgId: string,
        spaceId: string,
        target: Pick<SpaceEstimation, "scale" | "extended" | "allow_zero">,
      ) =>
        http.request<SpaceEstimationPreview>(
          `${sp(orgId, spaceId)}/estimation/preview`,
          { query: { ...target } },
        ),
    },
    spaceRepos: {
      list: async (orgId: string, spaceId: string) =>
        (
          await http.request<{ repos: SpaceRepo[] }>(
            `${sp(orgId, spaceId)}/repos`,
          )
        ).repos,
      create: (orgId: string, spaceId: string, input: SpaceRepoInput) =>
        http.request<SpaceRepo>(`${sp(orgId, spaceId)}/repos`, {
          method: "POST",
          body: input,
        }),
      update: (
        orgId: string,
        spaceId: string,
        repoId: string,
        input: SpaceRepoPatch,
      ) =>
        http.request<SpaceRepo>(`${sp(orgId, spaceId)}/repos/${seg(repoId)}`, {
          method: "PATCH",
          body: input,
        }),
      remove: (
        orgId: string,
        spaceId: string,
        repoId: string,
        version: number,
      ) =>
        http.request<void>(`${sp(orgId, spaceId)}/repos/${seg(repoId)}`, {
          method: "DELETE",
          query: { version },
        }),
    },
    spaceMembership: {
      list: async (orgId: string, spaceId: string) =>
        (
          await http.request<{ members: SpaceMemberDetail[] }>(
            `${sp(orgId, spaceId)}/members`,
          )
        ).members,
      /** 添加成员或改角色（自己加入公开空间也用这个，role = MEMBER） */
      put: (orgId: string, spaceId: string, userId: string, role: SpaceRole) =>
        http.request<S["SpaceMember"]>(
          `${sp(orgId, spaceId)}/members/${seg(userId)}`,
          { method: "PUT", body: { role } },
        ),
      /** 移出成员；对自己调用即离开空间 */
      remove: (orgId: string, spaceId: string, userId: string) =>
        http.request<void>(`${sp(orgId, spaceId)}/members/${seg(userId)}`, {
          method: "DELETE",
        }),
    },
    spaceTaskTypes: {
      create: (
        orgId: string,
        spaceId: string,
        input: CreateSpaceTaskTypeInput,
      ) =>
        http.request<S["TaskType"]>(`${sp(orgId, spaceId)}/task-types`, {
          method: "POST",
          body: input,
        }),
      update: (
        orgId: string,
        spaceId: string,
        typeId: string,
        input: UpdateSpaceTaskTypeInput,
      ) =>
        http.request<S["TaskType"]>(
          `${sp(orgId, spaceId)}/task-types/${seg(typeId)}`,
          { method: "PATCH", body: input },
        ),
      remove: (
        orgId: string,
        spaceId: string,
        typeId: string,
        version: number,
      ) =>
        http.request<void>(`${sp(orgId, spaceId)}/task-types/${seg(typeId)}`, {
          method: "DELETE",
          query: { version },
        }),
    },
    taskTypeTemplates: {
      list: async (orgId: string, purpose?: SpacePurpose) =>
        (
          await http.request<{ templates: TaskTypeTemplate[] }>(
            `${org(orgId)}/task-type-templates`,
            { query: purpose ? { purpose } : undefined },
          )
        ).templates,
    },
  };
}
