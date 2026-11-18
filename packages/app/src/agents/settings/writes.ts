import { useQueryClient } from "@tanstack/react-query";

import type {
  AgentConfigInput,
  AgentProfilePatch,
  SpaceAgent,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import { useToast } from "@openfox/ui";

import { keys, useApi } from "../../context";
import { configInputFrom } from "../model";
import { storeAgent, useOptimisticMutation } from "../queries";

type ProfileChanges = Omit<AgentProfilePatch, "profile_version">;
type ConfigChanges = Partial<AgentConfigInput>;

/** 取缓存里最新的 Agent（乐观锁版本以缓存为准，避免连续修改时用到旧版本）。 */
function latest(
  queryClient: ReturnType<typeof useQueryClient>,
  orgId: string,
  agent: SpaceAgent,
): SpaceAgent {
  return (
    queryClient.getQueryData<SpaceAgent>(keys.agent(orgId, agent.id)) ?? agent
  );
}

/**
 * 设置抽屉的两类写操作：
 * - 资料（名称、职位、描述、头像、触发开关、访问权限）：PATCH /profile，带 profile_version；
 * - 配置（指令、运行设置、电脑 & Runtime、Git 提交人）：新建配置版本，带 expected_agent_version。
 * 都是乐观更新，失败回滚并提示，409 时刷新。
 */
export function useAgentWrites(orgId: string, agent: SpaceAgent) {
  const api = useApi();
  const t = useT();
  const toast = useToast();
  const queryClient = useQueryClient();
  const queryKey = keys.agent(orgId, agent.id);

  const profile = useOptimisticMutation<
    { changes: ProfileChanges; silent?: boolean },
    SpaceAgent,
    SpaceAgent
  >(orgId, {
    queryKey,
    mutationFn: ({ changes }) => {
      const current = latest(queryClient, orgId, agent);
      return api.agents.updateProfile(orgId, current.space_id, current.id, {
        ...changes,
        profile_version: current.profile_version,
      });
    },
    optimistic: (data, { changes }) => ({ ...data, ...changes }) as SpaceAgent,
    onSuccess: (saved, { silent }) => {
      storeAgent(queryClient, orgId, saved);
      if (!silent) toast(t("agents.toast.saved"));
    },
  });

  const config = useOptimisticMutation<ConfigChanges, SpaceAgent, SpaceAgent>(
    orgId,
    {
      queryKey,
      mutationFn: (changes) => {
        const current = latest(queryClient, orgId, agent);
        if (!current.config) throw new Error("agent has no config");
        return api.agents.createConfigVersion(
          orgId,
          current.space_id,
          current.id,
          {
            ...configInputFrom(current.config),
            ...changes,
            expected_agent_version: current.version,
          },
        );
      },
      optimistic: (data, changes) =>
        data.config
          ? ({ ...data, config: { ...data.config, ...changes } } as SpaceAgent)
          : data,
      onSuccess: (saved) => {
        storeAgent(queryClient, orgId, saved);
        toast(t("agents.toast.saved"));
      },
    },
  );

  return {
    /** 资料修改；成功返回新 Agent，失败返回 undefined（错误已提示） */
    patchProfile: (changes: ProfileChanges, silent = false) =>
      profile
        .mutateAsync({ changes, silent })
        .catch((): SpaceAgent | undefined => undefined),
    saveConfig: (changes: ConfigChanges) =>
      config
        .mutateAsync(changes)
        .catch((): SpaceAgent | undefined => undefined),
    profilePending: profile.isPending,
    configPending: config.isPending,
  };
}
