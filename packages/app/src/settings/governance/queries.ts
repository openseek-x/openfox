import { useQuery } from "@tanstack/react-query";

import { useApi } from "../../context";

// 阶段 12e · 组织配额与用量的查询键，配额页与对话里的「队列已满」提示共用。

export const runtimeConfigKey = (orgId: string) =>
  ["org", orgId, "runtime-config"] as const;
export const runtimeUsageKey = (orgId: string) =>
  ["org", orgId, "runtime-usage"] as const;

/** 用量是读取时刻的快照，没有实时事件，30 秒刷新一次 */
export const RUNTIME_USAGE_POLL_MS = 30_000;

export function useRuntimeUsage(orgId: string, enabled = true) {
  const api = useApi();
  return useQuery({
    queryKey: runtimeUsageKey(orgId),
    queryFn: ({ signal }) => api.runtimeConfig.usage(orgId, signal),
    enabled: enabled && Boolean(orgId),
    refetchInterval: RUNTIME_USAGE_POLL_MS,
    // 用量只用于提示，失败时不重试、不打扰用户
    retry: false,
  });
}
