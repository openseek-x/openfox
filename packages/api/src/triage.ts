import type { HttpClient } from "./http";
import type { components, operations } from "./schema.gen";

// 阶段 3d · 分拣台接口。所有公开类型取自生成契约。
type S = components["schemas"];
type JsonBody<K extends keyof operations> =
  NonNullable<operations[K]["requestBody"]> extends {
    content: { "application/json": infer B };
  }
    ? B
    : never;

export type TriageTask = S["TriageTask"];
export type TriageTaskPage = S["TriageTaskPage"];
export type TriageSettings = S["TriageSettings"];
export type TriageSettingsPatch = S["TriageSettingsPatch"];
export type TriageMember = S["TriageMember"];
export type TriageRule = S["TriageRule"];
export type TriageCondition = S["TriageCondition"];
export type TriageConditionGroup = S["TriageConditionGroup"];
export type TriageAction = S["TriageAction"];
export type TriageRuleInput = S["TriageRuleCreateRequest"];
export type TriageRulePatch = S["TriageRulePatchRequest"];
export type TriageAcceptInput = S["TriageAcceptRequest"];
export type TriageListQuery = NonNullable<
  operations["listTriageTasks"]["parameters"]["query"]
>;
export type TriageSnoozeInput = JsonBody<"snoozeTriageTask">;

const spacePath = (orgId: string, spaceId: string) =>
  `/v1/organizations/${encodeURIComponent(orgId)}/spaces/${encodeURIComponent(spaceId)}`;
const taskPath = (orgId: string, spaceId: string, taskId: string) =>
  `${spacePath(orgId, spaceId)}/tasks/${encodeURIComponent(taskId)}`;

export function createTriageApi(http: HttpClient) {
  return {
    triage: {
      list: (orgId: string, spaceId: string, query: TriageListQuery = {}) =>
        http.request<TriageTaskPage>(`${spacePath(orgId, spaceId)}/triage`, {
          query,
        }),
      candidates: (
        orgId: string,
        spaceId: string,
        field: string,
        query: TriageListQuery,
      ) =>
        http.request<{ counts: Record<string, number>; complete: boolean }>(
          `${spacePath(orgId, spaceId)}/triage/candidates`,
          { query: { ...query, field } },
        ),
      setFavorite: (orgId: string, spaceId: string, favorite: boolean) =>
        http.request<void>(`${spacePath(orgId, spaceId)}/triage/favorite`, {
          method: favorite ? "PUT" : "DELETE",
        }),
      count: (orgId: string, spaceId: string) =>
        http.request<{
          total: number;
          snoozed: number;
          next_snooze_at?: string | null;
        }>(`${spacePath(orgId, spaceId)}/triage/count`),
      accept: (
        orgId: string,
        spaceId: string,
        taskId: string,
        input: TriageAcceptInput,
      ) =>
        http.request<S["TaskDetail"]>(
          `${taskPath(orgId, spaceId, taskId)}/triage/accept`,
          { method: "POST", body: input },
        ),
      decline: (
        orgId: string,
        spaceId: string,
        taskId: string,
        input: S["TriageDecisionRequest"],
      ) =>
        http.request<S["TaskDetail"]>(
          `${taskPath(orgId, spaceId, taskId)}/triage/decline`,
          { method: "POST", body: input },
        ),
      duplicate: (
        orgId: string,
        spaceId: string,
        taskId: string,
        input: S["TriageDuplicateRequest"],
      ) =>
        http.request<S["TaskDetail"]>(
          `${taskPath(orgId, spaceId, taskId)}/triage/duplicate`,
          { method: "POST", body: input },
        ),
      snooze: (
        orgId: string,
        spaceId: string,
        taskId: string,
        until: string | null,
      ) =>
        http.request<S["TaskDetail"]>(
          `${taskPath(orgId, spaceId, taskId)}/triage-snooze`,
          { method: "PUT", body: { until } },
        ),
      settings: (orgId: string, spaceId: string) =>
        http.request<TriageSettings>(
          `${spacePath(orgId, spaceId)}/triage-settings`,
        ),
      updateSettings: (
        orgId: string,
        spaceId: string,
        input: TriageSettingsPatch,
      ) =>
        http.request<TriageSettings>(
          `${spacePath(orgId, spaceId)}/triage-settings`,
          { method: "PATCH", body: input },
        ),
      setMembers: (orgId: string, spaceId: string, userIds: string[]) =>
        http.request<{ members: TriageMember[] }>(
          `${spacePath(orgId, spaceId)}/triage-members`,
          { method: "PUT", body: { user_ids: userIds } },
        ),
      createRule: (orgId: string, spaceId: string, input: TriageRuleInput) =>
        http.request<TriageRule>(`${spacePath(orgId, spaceId)}/triage-rules`, {
          method: "POST",
          body: input,
        }),
      updateRule: (
        orgId: string,
        spaceId: string,
        ruleId: string,
        input: TriageRulePatch,
      ) =>
        http.request<TriageRule>(
          `${spacePath(orgId, spaceId)}/triage-rules/${encodeURIComponent(ruleId)}`,
          { method: "PUT", body: input },
        ),
      removeRule: (
        orgId: string,
        spaceId: string,
        ruleId: string,
        version: number,
      ) =>
        http.request<void>(
          `${spacePath(orgId, spaceId)}/triage-rules/${encodeURIComponent(ruleId)}`,
          { method: "DELETE", query: { version } },
        ),
      setRuleEnabled: (
        orgId: string,
        spaceId: string,
        rule: Pick<TriageRule, "id" | "version">,
        enabled: boolean,
      ) =>
        http.request<TriageRule>(
          `${spacePath(orgId, spaceId)}/triage-rules/${encodeURIComponent(rule.id)}/enabled`,
          { method: "PUT", body: { version: rule.version, enabled } },
        ),
      reorderRules: (orgId: string, spaceId: string, ruleIds: string[]) =>
        http.request<{ rules: TriageRule[] }>(
          `${spacePath(orgId, spaceId)}/triage-rules/reorder`,
          { method: "PUT", body: { rule_ids: ruleIds } },
        ),
    },
  };
}
