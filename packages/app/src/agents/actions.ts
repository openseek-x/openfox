import { useQueryClient } from "@tanstack/react-query";

import {
  newIdempotencyKey,
  type AgentDirectory,
  type AgentDirectoryEntry,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import { useToast } from "@openfox/ui";

import { keys, useApi } from "../context";
import {
  agentErrorMessage,
  invalidateAgentLists,
  storeAgent,
  useOptimisticMutation,
} from "./queries";

type Target = Pick<AgentDirectoryEntry, "type" | "id">;

const sameTarget = (entry: Target, target: Target) =>
  entry.type === target.type && entry.id === target.id;

function patchEntry(
  data: AgentDirectory,
  target: Target,
  patch: Partial<AgentDirectoryEntry>,
): AgentDirectory {
  return {
    ...data,
    entries: data.entries.map((entry) =>
      sameTarget(entry, target) ? { ...entry, ...patch } : entry,
    ),
  };
}

const targetBody = (target: Target) =>
  target.type === "agent" ? { agent_id: target.id } : { crew_id: target.id };

/** 置顶：乐观更新目录缓存，失败回滚并提示「无法更新置顶状态」。 */
export function usePinEntry(orgId: string) {
  const api = useApi();
  const t = useT();
  return useOptimisticMutation<
    { target: Target; pinned: boolean },
    unknown,
    AgentDirectory
  >(orgId, {
    queryKey: keys.agentDirectory(orgId),
    mutationFn: ({ target, pinned }) =>
      api.agents.setPin(orgId, { ...targetBody(target), pinned }),
    optimistic: (data, { target, pinned }) =>
      patchEntry(data, target, { pinned }),
    errorMessage: t("agents.toast.pinFailed"),
  });
}

/** 收藏到侧栏（☆）：乐观更新目录缓存与详情缓存。 */
export function useFavoriteEntry(orgId: string) {
  const api = useApi();
  const t = useT();
  const queryClient = useQueryClient();
  return useOptimisticMutation<
    { target: Target; favorite: boolean },
    unknown,
    AgentDirectory
  >(orgId, {
    queryKey: keys.agentDirectory(orgId),
    mutationFn: ({ target, favorite }) =>
      favorite
        ? api.agents.favorite(orgId, targetBody(target))
        : api.agents.unfavorite(orgId, targetBody(target)),
    optimistic: (data, { target, favorite }) =>
      patchEntry(data, target, { favorite }),
    onSuccess: (_void, { target, favorite }) => {
      const key =
        target.type === "agent"
          ? keys.agent(orgId, target.id)
          : keys.crew(orgId, target.id);
      queryClient.setQueryData(key, (old: object | undefined) =>
        old ? { ...old, favorite } : old,
      );
    },
    errorMessage: t("agents.toast.favoriteFailed"),
  });
}

/** 归档 / 恢复：Agent 走旧 PATCH（archived + version），小队走 crew PATCH。 */
export function useArchiveEntry(orgId: string) {
  const api = useApi();
  const t = useT();
  const toast = useToast();
  const queryClient = useQueryClient();
  return useOptimisticMutation<
    {
      target: Target & { space_id: string; name: string; version: number };
      archived: boolean;
    },
    unknown,
    AgentDirectory
  >(orgId, {
    queryKey: keys.agentDirectory(orgId),
    mutationFn: ({ target, archived }) =>
      target.type === "agent"
        ? api.agents.update(orgId, target.space_id, target.id, {
            archived,
            version: target.version,
          })
        : api.crews.update(orgId, target.space_id, target.id, {
            archived,
            version: target.version,
          }),
    optimistic: (data, { target, archived }) =>
      archived
        ? {
            ...data,
            entries: data.entries.filter((e) => !sameTarget(e, target)),
            archived_count: data.archived_count + 1,
          }
        : data,
    onSuccess: (_result, { target, archived }) => {
      toast(
        t(archived ? "agents.toast.archived" : "agents.toast.restored", {
          name: target.name,
        }),
      );
      void queryClient.invalidateQueries({
        queryKey:
          target.type === "agent"
            ? keys.agent(orgId, target.id)
            : keys.crew(orgId, target.id),
      });
      invalidateAgentLists(queryClient, orgId);
    },
  });
}

/** 复制 Agent：成功后写入缓存并返回新 Agent。 */
export function useCopyAgent(orgId: string) {
  const api = useApi();
  const t = useT();
  const toast = useToast();
  const queryClient = useQueryClient();
  return async (agent: { id: string; space_id: string }) => {
    try {
      const copy = await api.agents.copy(
        orgId,
        agent.space_id,
        agent.id,
        newIdempotencyKey(),
      );
      storeAgent(queryClient, orgId, copy);
      toast(t("agents.toast.copied", { name: copy.name }));
      return copy;
    } catch (error) {
      toast(agentErrorMessage(t, error), { tone: "error" });
      return null;
    }
  };
}
