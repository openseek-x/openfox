import { useRef } from "react";

import {
  keepPreviousData,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";

import {
  isApiError,
  type Skill,
  type SkillCreateInput,
  type SkillListQuery,
  type SkillRuntimeImport,
  type SkillRuntimeImportInput,
  type SkillSaveInput,
  type SkillUploadFile,
} from "@openfox/api";
import type { MessageKey, Translate } from "@openfox/i18n";

import { keys, useApi } from "../context";
import { IMPORT_POLL_LIMIT_MS, IMPORT_POLL_MS } from "./model";

// 技能的查询与写操作。写操作结束后整体失效该空间的技能查询（含 Agent 装配）。

export function useSkills(
  orgId: string,
  spaceId: string,
  filters: SkillListQuery,
) {
  const api = useApi();
  return useQuery({
    queryKey: keys.skillList(orgId, spaceId, filters),
    queryFn: () => api.skills.list(orgId, spaceId, filters),
    enabled: Boolean(spaceId),
    // 换排序 / 筛选 / 搜索词时保留上一份列表，避免整页闪回骨架
    placeholderData: keepPreviousData,
  });
}

export function useSkill(orgId: string, spaceId: string, skillId: string) {
  const api = useApi();
  return useQuery({
    queryKey: keys.skill(orgId, spaceId, skillId),
    queryFn: () => api.skills.get(orgId, spaceId, skillId),
    enabled: Boolean(spaceId && skillId),
    // 编辑内容由本地状态持有，重新拉取不应打断输入
    refetchOnWindowFocus: false,
  });
}

export function useSkillFile(
  orgId: string,
  spaceId: string,
  skillId: string,
  path: string,
) {
  const api = useApi();
  return useQuery({
    queryKey: keys.skillFile(orgId, spaceId, skillId, path),
    queryFn: () => api.skills.getFile(orgId, spaceId, skillId, path),
    enabled: Boolean(spaceId && skillId && path),
    refetchOnWindowFocus: false,
  });
}

function useInvalidateSkills(orgId: string, spaceId: string) {
  const queryClient = useQueryClient();
  return () =>
    void queryClient.invalidateQueries({
      queryKey: keys.skills(orgId, spaceId),
    });
}

export function useCreateSkill(orgId: string, spaceId: string) {
  const api = useApi();
  const invalidate = useInvalidateSkills(orgId, spaceId);
  return useMutation({
    mutationFn: (input: SkillCreateInput) =>
      api.skills.create(orgId, spaceId, input),
    onSettled: invalidate,
  });
}

export function useUploadSkill(orgId: string, spaceId: string) {
  const api = useApi();
  const invalidate = useInvalidateSkills(orgId, spaceId);
  return useMutation({
    mutationFn: (files: SkillUploadFile[]) =>
      api.skills.upload(orgId, spaceId, files),
    onSettled: invalidate,
  });
}

export function useUrlImport(orgId: string, spaceId: string) {
  const api = useApi();
  const invalidate = useInvalidateSkills(orgId, spaceId);
  return useMutation({
    mutationFn: (url: string) => api.skills.importFromUrl(orgId, spaceId, url),
    onSettled: invalidate,
  });
}

export function useSaveSkillFiles(
  orgId: string,
  spaceId: string,
  skillId: string,
) {
  const api = useApi();
  const invalidate = useInvalidateSkills(orgId, spaceId);
  return useMutation({
    mutationFn: (input: SkillSaveInput) =>
      api.skills.saveFiles(orgId, spaceId, skillId, input),
    onSettled: invalidate,
  });
}

export function useDeleteSkill(orgId: string, spaceId: string) {
  const api = useApi();
  const invalidate = useInvalidateSkills(orgId, spaceId);
  return useMutation({
    mutationFn: (skill: Pick<Skill, "id" | "version">) =>
      api.skills.remove(orgId, spaceId, skill),
    onSettled: invalidate,
  });
}

/** 整体替换 Agent 装配的技能。 */
export function useSetAgentSkills(
  orgId: string,
  spaceId: string,
  agentId: string,
) {
  const api = useApi();
  const invalidate = useInvalidateSkills(orgId, spaceId);
  return useMutation({
    mutationFn: (skillIds: string[]) =>
      api.agentSkills.set(orgId, spaceId, agentId, skillIds),
    onSettled: invalidate,
  });
}

export function useAgentSkills(
  orgId: string,
  spaceId: string,
  agentId: string,
) {
  const api = useApi();
  return useQuery({
    queryKey: keys.agentSkills(orgId, spaceId, agentId),
    queryFn: () => api.agentSkills.list(orgId, spaceId, agentId),
    enabled: Boolean(spaceId && agentId),
  });
}

/** 发起 Runtime 导入。 */
export function useStartRuntimeImport(orgId: string, spaceId: string) {
  const api = useApi();
  return useMutation({
    mutationFn: (input: SkillRuntimeImportInput) =>
      api.skills.createRuntimeImport(orgId, spaceId, input),
  });
}

/** 轮询导入进度：还有 pending 项（status = running）时每秒一次，终态或超过兜底上限即停止。 */
export function useRuntimeImportProgress(
  orgId: string,
  spaceId: string,
  commandId: string | null,
  initial?: SkillRuntimeImport,
) {
  const api = useApi();
  const startedAt = useRef(Date.now());
  return useQuery({
    queryKey: [...keys.skills(orgId, spaceId), "import", commandId ?? ""],
    queryFn: () => api.skills.getRuntimeImport(orgId, spaceId, commandId!),
    initialData: initial,
    enabled: Boolean(commandId),
    retry: false,
    refetchOnWindowFocus: false,
    refetchInterval: (query) => {
      const data = query.state.data;
      if (query.state.status === "error") return false;
      if (data && data.status !== "running") return false;
      if (Date.now() - startedAt.current > IMPORT_POLL_LIMIT_MS) return false;
      return IMPORT_POLL_MS;
    },
  });
}

export function useCancelRuntimeImport(orgId: string, spaceId: string) {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (commandId: string) =>
      api.skills.cancelRuntimeImport(orgId, spaceId, commandId),
    onSuccess: (result) =>
      queryClient.setQueryData(
        [...keys.skills(orgId, spaceId), "import", result.command_id],
        result,
      ),
  });
}

/** 按错误码本地化的文案；message 变量是服务端消息。 */
const ERROR_KEYS: Record<string, MessageKey> = {
  SKILL_URL_FORBIDDEN: "skills.errors.urlForbidden",
  SKILL_URL_INVALID: "skills.errors.urlInvalid",
  SKILL_URL_FETCH_FAILED: "skills.errors.fetchFailed",
  SKILL_TOO_LARGE: "skills.runtime.reasons.SKILL_TOO_LARGE",
  SKILL_INVALID: "skills.runtime.reasons.SKILL_INVALID",
  SKILL_NAME_INVALID: "skills.runtime.reasons.SKILL_NAME_INVALID",
  SKILL_LIMIT: "skills.runtime.reasons.SKILL_LIMIT",
  SPACE_RETIRED: "skills.runtime.reasons.SPACE_RETIRED",
};

/**
 * 错误文案：已知错误码用本地化文案；其他优先用服务端 message，没有时用兜底文案。
 * 409 冲突（重名、已有导入等，不含已本地化的码）追加一句建议。
 */
export function skillErrorText(
  t: Translate,
  error: unknown,
  fallback: MessageKey,
  conflictHint?: MessageKey,
): string {
  if (isApiError(error)) {
    const key = ERROR_KEYS[error.code];
    if (key) return t(key, { message: error.message });
  }
  const base = isApiError(error) && error.message ? error.message : t(fallback);
  if (conflictHint && isApiError(error) && error.status === 409)
    return `${base} ${t(conflictHint)}`;
  return base;
}
