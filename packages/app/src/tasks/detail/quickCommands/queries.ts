import { useQuery, type QueryClient } from "@tanstack/react-query";

import {
  QUICK_COMMAND_LIMIT,
  isApiError,
  type AgentDirectoryEntry,
  type QuickCommand,
  type SpaceAgent,
} from "@openfox/api";
import type { Translate } from "@openfox/i18n";

import { useMe } from "../../../agents/queries";
import { keys, useApi } from "../../../context";
import { useAgentDirectory, useSpaceAgents } from "../../queries";

// 快捷指令的查询与小工具（缓存键按空间放在 keys.quickCommands）。

export function useQuickCommands(
  orgId: string,
  spaceId: string,
  enabled = true,
) {
  const api = useApi();
  return useQuery({
    queryKey: keys.quickCommands(orgId, spaceId),
    queryFn: () => api.quickCommands.list(orgId, spaceId),
    enabled: enabled && Boolean(spaceId),
  });
}

export const refreshQuickCommands = (
  queryClient: QueryClient,
  orgId: string,
  spaceId: string,
) =>
  queryClient.invalidateQueries({
    queryKey: keys.quickCommands(orgId, spaceId),
  });

/** 菜单里的顺序：私有在前，共享在后 */
export const menuCommands = (
  list: { private: QuickCommand[]; shared: QuickCommand[] } | undefined,
) => [...(list?.private ?? []), ...(list?.shared ?? [])];

/** 把接口错误转成提示文案 */
export function quickCommandError(t: Translate, error: unknown): string {
  if (isApiError(error, "QUICK_COMMAND_LIMIT"))
    return t("tasks.quickCommands.errors.limit", { max: QUICK_COMMAND_LIMIT });
  if (isApiError(error, "INVALID_QUICK_COMMAND_TARGET"))
    return t("tasks.quickCommands.errors.target");
  if (isApiError(error, "VERSION_CONFLICT"))
    return t("tasks.quickCommands.errors.conflict");
  if (isApiError(error, "ACTION_FORBIDDEN"))
    return t("tasks.quickCommands.errors.forbidden");
  return t("tasks.quickCommands.errors.generic");
}

export interface QuickCommandTargets {
  agents: SpaceAgent[];
  crews: AgentDirectoryEntry[];
}

/**
 * 表单可选的目标：本空间未归档、共享或本人私有的 Agent（不含内置 Agent）与小队。
 * 规则与服务端校验一致（服务端仍是兜底，400 INVALID_QUICK_COMMAND_TARGET）。
 */
export function useQuickCommandTargets(
  orgId: string,
  spaceId: string,
  enabled = true,
): QuickCommandTargets {
  const me = useMe().data?.id;
  const agents = useSpaceAgents(orgId, enabled ? spaceId : undefined).data;
  const directory = useAgentDirectory(orgId, enabled).data?.entries;
  const usable = (item: {
    visibility: string;
    owner_user_id: string;
    archived_at?: string | null;
  }) =>
    !item.archived_at &&
    (item.visibility === "SHARED" || item.owner_user_id === me);
  return {
    agents: (agents ?? []).filter(
      (agent) => !agent.builtin_kind && usable(agent),
    ),
    crews: (directory ?? []).filter(
      (entry) =>
        entry.type === "crew" && entry.space_id === spaceId && usable(entry),
    ),
  };
}
