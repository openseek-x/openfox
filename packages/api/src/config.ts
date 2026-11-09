import type { HttpClient } from "./http";
import type { components, operations } from "./schema.gen";

// 阶段 1 · 全局配置接口（标签、分组、状态、关联类型、自定义字段、组织级任务类型）。
// 类型全部来自 schema.gen.ts，不手写。

type S = components["schemas"];
type JsonBody<K extends keyof operations> =
  NonNullable<operations[K]["requestBody"]> extends {
    content: { "application/json": infer B };
  }
    ? B
    : never;

export type LabelGroup = S["LabelGroup"];
export type Label = S["Label"];
export type TaskStatus = S["TaskStatus"];
export type TaskStatusCategory = S["TaskStatusCategory"];
export type StatusTypeUsage = S["StatusTypeUsage"];
export type RelationType = S["RelationType"];
export type RelationTypeKey = S["RelationTypeKey"];
export type CustomField = S["CustomField"];
export type CustomFieldKind = S["CustomFieldKind"];
export type CustomFieldMemberScope = S["CustomFieldMemberScope"];
export type CustomFieldOption = S["CustomFieldOption"];
export type CustomFieldOptionInput = S["CustomFieldOptionInput"];
export type OrganizationTaskType = S["OrganizationTaskType"];
export type OrganizationTaskTypeSpace = S["OrganizationTaskTypeSpace"];

export type LabelScope =
  | "organization"
  | "archived"
  | "picker"
  // 阶段 3a · 空间标签（需要 space_id）
  | "space"
  | "space_archived";
export type CreateLabelGroupInput = JsonBody<"createLabelGroup">;
export type UpdateLabelGroupInput = JsonBody<"updateLabelGroup">;
export type CreateLabelInput = JsonBody<"createLabel">;
export type UpdateLabelInput = JsonBody<"updateLabel">;
export type BulkLabelsInput = JsonBody<"bulkLabels">;
export type CreateTaskStatusInput = JsonBody<"createTaskStatus">;
export type UpdateTaskStatusInput = JsonBody<"updateTaskStatus">;
export type MoveTaskStatusInput = JsonBody<"moveTaskStatus">;
export type CreateRelationTypeInput = JsonBody<"createTaskRelationType">;
export type UpdateRelationTypeInput = JsonBody<"updateTaskRelationType">;
export type CreateCustomFieldInput = JsonBody<"createCustomField">;
export type UpdateCustomFieldInput = JsonBody<"updateCustomField">;
export type UpdateCustomFieldOptionInput = JsonBody<"updateCustomFieldOption">;

const org = (id: string) => `/v1/organizations/${encodeURIComponent(id)}`;
const seg = encodeURIComponent;

/** 全局配置方法组，由 createApi 展开到顶层。 */
export function createConfigApi(http: HttpClient) {
  return {
    labelGroups: {
      /** 不传 spaceId = 组织级分组；传 spaceId = 该空间的分组（阶段 3a） */
      list: async (orgId: string, spaceId?: string) =>
        (
          await http.request<{ label_groups: LabelGroup[] }>(
            `${org(orgId)}/label-groups`,
            { query: spaceId ? { space_id: spaceId } : undefined },
          )
        ).label_groups,
      create: (
        orgId: string,
        input: CreateLabelGroupInput,
        idempotencyKey: string,
      ) =>
        http.request<LabelGroup>(`${org(orgId)}/label-groups`, {
          method: "POST",
          body: input,
          idempotencyKey,
        }),
      update: (orgId: string, groupId: string, input: UpdateLabelGroupInput) =>
        http.request<LabelGroup>(`${org(orgId)}/label-groups/${seg(groupId)}`, {
          method: "PATCH",
          body: input,
        }),
      remove: (orgId: string, groupId: string) =>
        http.request<{ removed_label_ids: string[] }>(
          `${org(orgId)}/label-groups/${seg(groupId)}`,
          { method: "DELETE" },
        ),
    },
    labels: {
      list: async (
        orgId: string,
        scope: LabelScope = "organization",
        spaceId?: string,
      ) =>
        (
          await http.request<{ labels: Label[] }>(`${org(orgId)}/labels`, {
            query: { scope, space_id: spaceId },
          })
        ).labels,
      create: (
        orgId: string,
        input: CreateLabelInput,
        idempotencyKey: string,
      ) =>
        http.request<Label>(`${org(orgId)}/labels`, {
          method: "POST",
          body: input,
          idempotencyKey,
        }),
      update: (orgId: string, labelId: string, input: UpdateLabelInput) =>
        http.request<Label>(`${org(orgId)}/labels/${seg(labelId)}`, {
          method: "PATCH",
          body: input,
        }),
      remove: (orgId: string, labelId: string) =>
        http.request<void>(`${org(orgId)}/labels/${seg(labelId)}`, {
          method: "DELETE",
        }),
      merge: (orgId: string, labelId: string, targetLabelId: string) =>
        http.request<Label>(`${org(orgId)}/labels/${seg(labelId)}/merge`, {
          method: "POST",
          body: { target_label_id: targetLabelId },
        }),
      bulk: (orgId: string, input: BulkLabelsInput) =>
        http.request<{ affected: number }>(`${org(orgId)}/labels/bulk`, {
          method: "POST",
          body: input,
        }),
    },
    taskStatuses: {
      list: async (orgId: string) =>
        (
          await http.request<{ statuses: TaskStatus[] }>(
            `${org(orgId)}/task-statuses`,
          )
        ).statuses,
      create: (
        orgId: string,
        input: CreateTaskStatusInput,
        idempotencyKey: string,
      ) =>
        http.request<TaskStatus>(`${org(orgId)}/task-statuses`, {
          method: "POST",
          body: input,
          idempotencyKey,
        }),
      update: (orgId: string, statusId: string, input: UpdateTaskStatusInput) =>
        http.request<TaskStatus>(
          `${org(orgId)}/task-statuses/${seg(statusId)}`,
          { method: "PATCH", body: input },
        ),
      /** 有任务使用时必须给 replacementStatusId */
      remove: (orgId: string, statusId: string, replacementStatusId?: string) =>
        http.request<void>(`${org(orgId)}/task-statuses/${seg(statusId)}`, {
          method: "DELETE",
          query: { replacement_status_id: replacementStatusId },
        }),
      /** 返回整份状态列表 */
      move: async (
        orgId: string,
        statusId: string,
        input: MoveTaskStatusInput,
      ) =>
        (
          await http.request<{ statuses: TaskStatus[] }>(
            `${org(orgId)}/task-statuses/${seg(statusId)}/move`,
            { method: "POST", body: input },
          )
        ).statuses,
      setDefault: async (orgId: string, statusId: string, version: number) =>
        (
          await http.request<{ statuses: TaskStatus[] }>(
            `${org(orgId)}/task-statuses/${seg(statusId)}/default`,
            { method: "POST", body: { version } },
          )
        ).statuses,
    },
    relationTypes: {
      list: async (orgId: string) =>
        (
          await http.request<{ relation_types: RelationType[] }>(
            `${org(orgId)}/task-relation-types`,
          )
        ).relation_types,
      create: (
        orgId: string,
        input: CreateRelationTypeInput,
        idempotencyKey: string,
      ) =>
        http.request<RelationType>(`${org(orgId)}/task-relation-types`, {
          method: "POST",
          body: input,
          idempotencyKey,
        }),
      update: (orgId: string, typeId: string, input: UpdateRelationTypeInput) =>
        http.request<RelationType>(
          `${org(orgId)}/task-relation-types/${seg(typeId)}`,
          { method: "PATCH", body: input },
        ),
      remove: (orgId: string, typeId: string) =>
        http.request<void>(`${org(orgId)}/task-relation-types/${seg(typeId)}`, {
          method: "DELETE",
        }),
    },
    customFields: {
      list: async (orgId: string, includeArchived = false, spaceId?: string) =>
        (
          await http.request<{ custom_fields: CustomField[] }>(
            `${org(orgId)}/custom-fields`,
            {
              query: {
                include_archived: includeArchived || undefined,
                space_id: spaceId,
              },
            },
          )
        ).custom_fields,
      create: (
        orgId: string,
        input: CreateCustomFieldInput,
        idempotencyKey: string,
      ) =>
        http.request<CustomField>(`${org(orgId)}/custom-fields`, {
          method: "POST",
          body: input,
          idempotencyKey,
        }),
      update: (orgId: string, fieldId: string, input: UpdateCustomFieldInput) =>
        http.request<CustomField>(
          `${org(orgId)}/custom-fields/${seg(fieldId)}`,
          { method: "PATCH", body: input },
        ),
      /** 已绑定任务类型时返回 409 CUSTOM_FIELD_IN_USE */
      remove: (orgId: string, fieldId: string) =>
        http.request<void>(`${org(orgId)}/custom-fields/${seg(fieldId)}`, {
          method: "DELETE",
        }),
      createOption: (
        orgId: string,
        fieldId: string,
        input: CustomFieldOptionInput,
        idempotencyKey: string,
      ) =>
        http.request<CustomFieldOption>(
          `${org(orgId)}/custom-fields/${seg(fieldId)}/options`,
          { method: "POST", body: input, idempotencyKey },
        ),
      updateOption: (
        orgId: string,
        fieldId: string,
        optionId: string,
        input: UpdateCustomFieldOptionInput,
      ) =>
        http.request<CustomFieldOption>(
          `${org(orgId)}/custom-fields/${seg(fieldId)}/options/${seg(optionId)}`,
          { method: "PATCH", body: input },
        ),
      removeOption: (orgId: string, fieldId: string, optionId: string) =>
        http.request<void>(
          `${org(orgId)}/custom-fields/${seg(fieldId)}/options/${seg(optionId)}`,
          { method: "DELETE" },
        ),
    },
    orgTaskTypes: {
      /** 按空间分组的未归档、已启用任务类型 */
      list: async (orgId: string) =>
        (
          await http.request<{ spaces: OrganizationTaskTypeSpace[] }>(
            `${org(orgId)}/task-types`,
          )
        ).spaces,
    },
    typeFields: {
      list: async (orgId: string, spaceId: string, typeId: string) =>
        (
          await http.request<{ custom_fields: CustomField[] }>(
            `${org(orgId)}/spaces/${seg(spaceId)}/task-types/${seg(typeId)}/fields`,
          )
        ).custom_fields,
      bind: (orgId: string, spaceId: string, typeId: string, fieldId: string) =>
        http.request<void>(
          `${org(orgId)}/spaces/${seg(spaceId)}/task-types/${seg(typeId)}/fields/${seg(fieldId)}`,
          { method: "PUT" },
        ),
      unbind: (
        orgId: string,
        spaceId: string,
        typeId: string,
        fieldId: string,
      ) =>
        http.request<void>(
          `${org(orgId)}/spaces/${seg(spaceId)}/task-types/${seg(typeId)}/fields/${seg(fieldId)}`,
          { method: "DELETE" },
        ),
    },
  };
}
