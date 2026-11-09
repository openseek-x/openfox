import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import type {
  TriageAcceptInput,
  TriageListQuery,
  TriageTaskPage,
  TriageRuleInput,
  TriageRulePatch,
  TriageSettingsPatch,
  TriageSnoozeInput,
  components,
} from "@openfox/api";
import { isApiError } from "@openfox/api";
import { useT, type MessageKey, type Translate } from "@openfox/i18n";
import { useToast } from "@openfox/ui";

import { keys, useApi } from "../context";
import { taskErrorMessage } from "../tasks/queries";

type S = components["schemas"];
type TriageDecisionInput = S["TriageDecisionRequest"];
type TriageDuplicateInput = S["TriageDuplicateRequest"];

export function useTriageList(
  orgId: string,
  spaceId: string,
  query: TriageListQuery & Record<string, unknown>,
  enabled = true,
) {
  const api = useApi();
  return useInfiniteQuery({
    initialPageParam: "",
    getNextPageParam: (page: TriageTaskPage) => page.next_cursor || undefined,
    queryKey: keys.triageList(orgId, spaceId, query),
    queryFn: ({ pageParam }) =>
      api.triage.list(orgId, spaceId, {
        ...query,
        ...(pageParam ? { cursor: pageParam } : {}),
      }),
    enabled,
  });
}

export function useTriageCount(orgId: string, spaceId: string, enabled = true) {
  const api = useApi();
  return useQuery({
    queryKey: keys.triageCount(orgId, spaceId),
    queryFn: () => api.triage.count(orgId, spaceId),
    enabled,
  });
}

export function useTriageSettings(orgId: string, spaceId: string) {
  const api = useApi();
  return useQuery({
    queryKey: keys.triageSettings(orgId, spaceId),
    queryFn: () => api.triage.settings(orgId, spaceId),
  });
}

const TRIAGE_ERRORS: Record<string, MessageKey> = {
  STATUS_CATEGORY_MISSING: "spaces.triage.errors.categoryMissing",
  PRIORITY_REQUIRED: "spaces.triage.errors.priorityRequired",
  NOT_IN_TRIAGE: "spaces.triage.errors.notInTriage",
  INVALID_TRIAGE_RULE: "spaces.triage.errors.invalidRule",
  LIMIT_REACHED: "spaces.triage.errors.ruleLimit",
  INVALID_TRIAGE_MEMBER: "spaces.triage.errors.invalidMember",
  INVALID_DUPLICATE: "spaces.triage.errors.invalidDuplicate",
  INVALID_SNOOZE: "spaces.triage.errors.invalidSnooze",
};

/** 分拣接口的错误码有专门提示，其余沿用任务的错误提示。 */
export function triageErrorMessage(t: Translate, error: unknown): string {
  const key = isApiError(error) ? TRIAGE_ERRORS[error.code] : undefined;
  return key ? t(key) : taskErrorMessage(t, error);
}

function useTriageWrite<T>(
  orgId: string,
  spaceId: string,
  mutationFn: (value: T) => Promise<unknown>,
) {
  const queryClient = useQueryClient();
  const toast = useToast();
  const t = useT();
  return useMutation({
    mutationFn,
    onError: (error) => toast(triageErrorMessage(t, error), { tone: "error" }),
    onSettled: () => {
      void queryClient.invalidateQueries({
        queryKey: keys.triage(orgId, spaceId),
      });
      void queryClient.invalidateQueries({ queryKey: keys.tasks(orgId) });
      void queryClient.invalidateQueries({
        queryKey: keys.space(orgId, spaceId),
      });
    },
  });
}

export function useTriageSettingsMutation(orgId: string, spaceId: string) {
  const api = useApi();
  return useTriageWrite<TriageSettingsPatch>(orgId, spaceId, (input) =>
    api.triage.updateSettings(orgId, spaceId, input),
  );
}

export function useTriageMembersMutation(orgId: string, spaceId: string) {
  const api = useApi();
  return useTriageWrite<string[]>(orgId, spaceId, (userIds) =>
    api.triage.setMembers(orgId, spaceId, userIds),
  );
}

export function useTriageRuleCreate(orgId: string, spaceId: string) {
  const api = useApi();
  return useTriageWrite<TriageRuleInput>(orgId, spaceId, (input) =>
    api.triage.createRule(orgId, spaceId, input),
  );
}

export function useTriageRuleUpdate(orgId: string, spaceId: string) {
  const api = useApi();
  return useTriageWrite<TriageRulePatch & { id: string }>(
    orgId,
    spaceId,
    ({ id, ...input }) => api.triage.updateRule(orgId, spaceId, id, input),
  );
}

export function useTriageRuleDelete(orgId: string, spaceId: string) {
  const api = useApi();
  return useTriageWrite<{ id: string; version: number }>(
    orgId,
    spaceId,
    ({ id, version }) => api.triage.removeRule(orgId, spaceId, id, version),
  );
}

export function useTriageRuleEnabled(orgId: string, spaceId: string) {
  const api = useApi();
  return useTriageWrite<{ id: string; version: number; enabled: boolean }>(
    orgId,
    spaceId,
    ({ id, version, enabled }) =>
      api.triage.setRuleEnabled(orgId, spaceId, { id, version }, enabled),
  );
}

export function useTriageRuleReorder(orgId: string, spaceId: string) {
  const api = useApi();
  return useTriageWrite<string[]>(orgId, spaceId, (ruleIds) =>
    api.triage.reorderRules(orgId, spaceId, ruleIds),
  );
}

export function useTriageAccept(orgId: string, spaceId: string) {
  const api = useApi();
  return useTriageWrite<{ taskId: string; input: TriageAcceptInput }>(
    orgId,
    spaceId,
    ({ taskId, input }) => api.triage.accept(orgId, spaceId, taskId, input),
  );
}

export function useTriageDecline(orgId: string, spaceId: string) {
  const api = useApi();
  return useTriageWrite<{ taskId: string; input: TriageDecisionInput }>(
    orgId,
    spaceId,
    ({ taskId, input }) => api.triage.decline(orgId, spaceId, taskId, input),
  );
}

export function useTriageDuplicate(orgId: string, spaceId: string) {
  const api = useApi();
  return useTriageWrite<{ taskId: string; input: TriageDuplicateInput }>(
    orgId,
    spaceId,
    ({ taskId, input }) => api.triage.duplicate(orgId, spaceId, taskId, input),
  );
}

export function useTriageSnooze(orgId: string, spaceId: string) {
  const api = useApi();
  return useTriageWrite<{ taskId: string; input: TriageSnoozeInput }>(
    orgId,
    spaceId,
    ({ taskId, input }) =>
      api.triage.snooze(orgId, spaceId, taskId, input.until),
  );
}
