import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useParams } from "react-router-dom";

import {
  isApiError,
  type OrganizationSettings,
  type OrganizationSettingsPatch,
} from "@openfox/api";
import type { MessageKey, Translate } from "@openfox/i18n";

import { keys, useApi } from "../context";

// 组织设置的查询与写入。

export function useOrganizationSettings(orgId: string | undefined) {
  const api = useApi();
  return useQuery({
    queryKey: keys.organizationSettings(orgId ?? ""),
    queryFn: () => api.organizationSettings.get(orgId!),
    enabled: Boolean(orgId),
    staleTime: 60_000,
  });
}

/** 当前组织的优先级档位（4 / 5）；从路由读组织，设置未加载时按 4 级处理。 */
export function usePriorityLevels(): 4 | 5 {
  const { orgId } = useParams();
  const settings = useOrganizationSettings(orgId);
  return settings.data?.priority_levels === 5 ? 5 : 4;
}

/** 修改组织设置：带当前 version；成功后刷新组织列表（切换器）与依赖优先级的任务查询。 */
export function useUpdateOrganization(orgId: string) {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (patch: Omit<OrganizationSettingsPatch, "version">) => {
      const current = queryClient.getQueryData<OrganizationSettings>(
        keys.organizationSettings(orgId),
      );
      return api.organizationSettings.update(orgId, {
        ...patch,
        version: current?.version ?? 1,
      });
    },
    onSuccess: (settings, patch) => {
      queryClient.setQueryData(keys.organizationSettings(orgId), settings);
      void queryClient.invalidateQueries({ queryKey: keys.organizations });
      if (patch.priority_levels !== undefined) {
        void queryClient.invalidateQueries({ queryKey: keys.tasks(orgId) });
        void queryClient.invalidateQueries({ queryKey: keys.projects(orgId) });
      }
    },
    onError: () =>
      void queryClient.invalidateQueries({
        queryKey: keys.organizationSettings(orgId),
      }),
  });
}

const organizationErrors: Record<string, MessageKey> = {
  SLUG_INVALID: "organization.errors.slugInvalid",
  SLUG_RESERVED: "organization.errors.slugReserved",
  SLUG_TAKEN: "organization.errors.slugTaken",
  OWNER_REQUIRED: "organization.errors.ownerRequired",
  SOLE_MEMBER: "organization.errors.soleMember",
  CONFIRM_MISMATCH: "organization.errors.confirmMismatch",
  ALREADY_MEMBER: "organization.errors.alreadyMember",
  RESEND_COOLDOWN: "organization.errors.resendCooldown",
  SECRETS_UNAVAILABLE: "organization.errors.secretsUnavailable",
  PRIORITY_LEVEL_UNAVAILABLE: "organization.errors.priorityLevel",
};

/** 组织与成员接口的错误文案：已知错误码有专门提示，其余用 fallback。 */
export function organizationErrorMessage(
  t: Translate,
  error: unknown,
  fallback: MessageKey,
): string {
  if (isApiError(error)) {
    const key = organizationErrors[error.code];
    if (key) return t(key);
  }
  return t(fallback);
}
