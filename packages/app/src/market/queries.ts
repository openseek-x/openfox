import {
  keepPreviousData,
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";

import {
  isApiError,
  type MarketSkillQuery,
  type MarketTemplateQuery,
} from "@openfox/api";
import type { MessageKey, Translate } from "@openfox/i18n";

import { keys, useApi } from "../context";
import { skillErrorText } from "../skills/queries";
import { MARKET_PAGE_SIZE } from "./model";

// 市场的查询与写操作。目录是只读共享数据，前端按组织入口缓存。

const CATALOG_STALE_MS = 60_000;

export function useMarketCatalog(orgId: string) {
  const api = useApi();
  return useQuery({
    queryKey: keys.marketCatalog(orgId),
    queryFn: () => api.market.catalog(orgId),
    staleTime: CATALOG_STALE_MS,
  });
}

/** 技能列表：用 next_cursor 逐页加载；换筛选时保留上一份，避免整页闪回骨架。 */
export function useMarketSkills(
  orgId: string,
  filters: Omit<MarketSkillQuery, "cursor" | "limit">,
  enabled = true,
) {
  const api = useApi();
  return useInfiniteQuery({
    queryKey: keys.marketSkills(orgId, filters),
    queryFn: ({ pageParam }) =>
      api.market.skills(orgId, {
        ...filters,
        limit: MARKET_PAGE_SIZE,
        ...(pageParam ? { cursor: pageParam } : {}),
      }),
    initialPageParam: "",
    getNextPageParam: (last) => last.next_cursor ?? undefined,
    placeholderData: keepPreviousData,
    enabled,
  });
}

export function useMarketTemplates(
  orgId: string,
  filters: Omit<MarketTemplateQuery, "cursor" | "limit">,
  enabled = true,
) {
  const api = useApi();
  return useInfiniteQuery({
    queryKey: keys.marketTemplates(orgId, filters),
    queryFn: ({ pageParam }) =>
      api.market.templates(orgId, {
        ...filters,
        limit: MARKET_PAGE_SIZE,
        ...(pageParam ? { cursor: pageParam } : {}),
      }),
    initialPageParam: "",
    getNextPageParam: (last) => last.next_cursor ?? undefined,
    placeholderData: keepPreviousData,
    enabled,
  });
}

export function useMarketSkill(orgId: string, id: string) {
  const api = useApi();
  return useQuery({
    queryKey: keys.marketSkill(orgId, id),
    queryFn: () => api.market.skill(orgId, id),
    enabled: Boolean(id),
    refetchOnWindowFocus: false,
  });
}

export function useMarketSkillFile(
  orgId: string,
  id: string,
  path: string,
  enabled = true,
) {
  const api = useApi();
  return useQuery({
    queryKey: keys.marketSkillFile(orgId, id, path),
    queryFn: () => api.market.skillFile(orgId, id, path),
    enabled: Boolean(id && path) && enabled,
    refetchOnWindowFocus: false,
  });
}

/** 图片：经下载接口取 Blob（由调用方生成并回收 objectURL）。 */
export function useMarketSkillBlob(
  orgId: string,
  id: string,
  path: string,
  maxBytes: number,
  enabled = true,
) {
  const api = useApi();
  return useQuery({
    queryKey: keys.marketSkillBlob(orgId, id, path),
    queryFn: () => api.market.downloadBlob(orgId, id, path, maxBytes),
    enabled: Boolean(id && path) && enabled,
    refetchOnWindowFocus: false,
    gcTime: 0,
  });
}

export function useMarketTemplate(orgId: string, id: string) {
  const api = useApi();
  return useQuery({
    queryKey: keys.marketTemplate(orgId, id),
    queryFn: () => api.market.template(orgId, id),
    enabled: Boolean(id),
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

/** 把市场技能复制到空间。 */
export function useAddMarketSkill(orgId: string) {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: { spaceId: string; marketSkillId: string }) =>
      api.skills.addFromMarket(orgId, input.spaceId, input.marketSkillId),
    onSettled: (_data, _error, input) =>
      void queryClient.invalidateQueries({
        queryKey: keys.skills(orgId, input.spaceId),
      }),
  });
}

/** 把空间里的市场技能更新到市场当前版本。 */
export function useUpdateMarketSkill(orgId: string, spaceId: string) {
  const api = useApi();
  const invalidate = useInvalidateSkills(orgId, spaceId);
  return useMutation({
    mutationFn: (input: { skillId: string; version: number }) =>
      api.skills.updateFromMarket(orgId, spaceId, input.skillId, input.version),
    onSettled: invalidate,
  });
}

/** 用模板创建 Agent 之后，把模板引用的技能装进空间并装配。 */
export function useApplyMarketTemplate(orgId: string) {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: {
      spaceId: string;
      agentId: string;
      templateId: string;
    }) =>
      api.agentSkills.applyMarketTemplate(
        orgId,
        input.spaceId,
        input.agentId,
        input.templateId,
      ),
    onSettled: (_data, _error, input) =>
      void queryClient.invalidateQueries({
        queryKey: keys.skills(orgId, input.spaceId),
      }),
  });
}

const ERROR_KEYS: Record<string, MessageKey> = {
  SKILL_MARKET_EXISTS: "market.errors.exists",
  SKILL_UP_TO_DATE: "market.errors.upToDate",
  SKILL_NOT_FROM_MARKET: "market.errors.notFromMarket",
  SKILL_NAME_TAKEN: "market.errors.nameTaken",
  VERSION_CONFLICT: "market.errors.versionConflict",
  SKILL_LIMIT: "market.errors.skillLimit",
  AGENT_SKILL_NAME_TAKEN: "market.errors.agentNameTaken",
  AGENT_SKILL_LIMIT: "market.errors.agentSkillLimit",
};

/** 市场相关写操作的错误文案：市场错误码本地化，其余走技能的通用处理。 */
export function marketErrorText(
  t: Translate,
  error: unknown,
  fallback: MessageKey,
): string {
  if (isApiError(error)) {
    if (error.status === 404) return t("market.errors.notFound");
    const key = ERROR_KEYS[error.code];
    if (key) return t(key);
  }
  return skillErrorText(t, error, fallback);
}

/** 已有的空间技能 ID（SKILL_MARKET_EXISTS 时 error.details.skill_id）。 */
export function existingSkillId(error: unknown): string | null {
  if (!isApiError(error) || error.code !== "SKILL_MARKET_EXISTS") return null;
  const id = error.details?.skill_id;
  return typeof id === "string" ? id : null;
}
