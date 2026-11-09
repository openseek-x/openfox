import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
  type QueryClient,
  type QueryKey,
} from "@tanstack/react-query";

import {
  isApiError,
  type AgentDirectory,
  type AgentDirectoryEntry,
  type Crew,
  type SpaceAgent,
} from "@openfox/api";
import { useT, type MessageKey, type Translate } from "@openfox/i18n";
import { useToast } from "@openfox/ui";

import { keys, useApi } from "../context";

// 全部 Agent、设置抽屉、小队、成员页签共用的查询与写操作。

/** 已知错误码对应的界面文案；其余回退到通用提示。 */
const errorMessages: Record<string, MessageKey> = {
  NAME_CONFLICT: "agents.errors.nameConflict",
  NAME_TAKEN: "agents.errors.nameConflict",
  VERSION_CONFLICT: "agents.toast.conflict",
  ARCHIVED: "agents.errors.archived",
  DEVICE_SCOPE_MISMATCH: "agents.access.deviceMismatch",
  SECRETS_UNAVAILABLE: "agents.errors.secretsUnavailable",
  LIMIT_REACHED: "agents.errors.limit",
  AGENT_CREW_MEMBERSHIP_CONFLICT: "agents.crew.members.exclusiveConflict",
  CREW_LEADER_REQUIRED: "agents.crew.leaderRequired",
  CREW_LEADER_SPACE_MISMATCH: "agents.access.leaderBlocked",
  REPO_CONFLICT: "agents.sections.git.repoConflict",
  ACTION_FORBIDDEN: "agents.errors.forbidden",
};

export function agentErrorMessage(t: Translate, error: unknown): string {
  if (!isApiError(error)) return t("agents.errors.generic");
  const key = errorMessages[error.code];
  if (key) return t(key);
  if (error.uncertain) return t("agents.errors.uncertain");
  if (error.status === 403) return t("agents.errors.forbidden");
  return t("agents.errors.generic");
}

export function useMe() {
  const api = useApi();
  return useQuery({ queryKey: keys.me, queryFn: api.me.get });
}

/** 当前成员在组织里的角色 */
export function useOrgRole(orgId: string) {
  const api = useApi();
  const organizations = useQuery({
    queryKey: keys.organizations,
    queryFn: api.me.organizations,
  });
  return organizations.data?.find((item) => item.id === orgId)?.role;
}

export function useSpaces(orgId: string) {
  const api = useApi();
  return useQuery({
    queryKey: keys.spaces(orgId),
    queryFn: () => api.spaces.list(orgId),
  });
}

/** 组织级目录，与处理人选择器共用缓存。 */
export function useDirectory(orgId: string, enabled = true) {
  const api = useApi();
  return useQuery({
    queryKey: keys.agentDirectory(orgId),
    queryFn: () => api.agents.directory(orgId),
    enabled,
  });
}

export function useArchivedDirectory(orgId: string, enabled = true) {
  const api = useApi();
  return useQuery({
    queryKey: keys.agentDirectoryArchived(orgId),
    queryFn: () => api.agents.directory(orgId, true),
    enabled,
  });
}

export function useDevices(orgId: string, enabled = true) {
  const api = useApi();
  return useQuery({
    queryKey: keys.devices(orgId),
    queryFn: () => api.devices.list(orgId),
    enabled,
  });
}

// ---------- 抽屉的空间定位 ----------

/**
 * Agent / 小队的接口路径需要空间 ID，地址栏只带 ID。打开抽屉时由调用方登记已知空间；
 * 刷新页面后再从目录（含已归档、小队成员）里查找。
 */
const spaceHints = new Map<string, string>();

export function rememberSpace(id: string, spaceId: string) {
  spaceHints.set(id, spaceId);
}

function findInDirectory(
  data: AgentDirectory | undefined,
  type: "agent" | "crew",
  id: string,
): string | undefined {
  const entries: AgentDirectoryEntry[] = data?.entries ?? [];
  const direct = entries.find((e) => e.type === type && e.id === id);
  if (direct) return direct.space_id;
  // 小队专属 Agent 不单独出现在目录里，借所在小队的空间
  if (type === "agent")
    return entries.find(
      (e) => e.type === "crew" && e.members?.some((m) => m.id === id),
    )?.space_id;
  return undefined;
}

/** 解析 Agent / 小队所在空间；找不到时 notFound = true。 */
export function useEntitySpace(
  orgId: string,
  type: "agent" | "crew",
  id: string | null,
) {
  const queryClient = useQueryClient();
  const hinted = id ? spaceHints.get(id) : undefined;
  const cached = id
    ? (type === "agent"
        ? queryClient.getQueryData<SpaceAgent>(keys.agent(orgId, id))
        : queryClient.getQueryData<Crew>(keys.crew(orgId, id))
      )?.space_id
    : undefined;
  const known = hinted ?? cached;
  const directory = useDirectory(orgId, Boolean(id) && !known);
  const fromActive = id ? findInDirectory(directory.data, type, id) : undefined;
  const archived = useArchivedDirectory(
    orgId,
    Boolean(id) && !known && directory.isSuccess && !fromActive,
  );
  const fromArchived = id
    ? findInDirectory(archived.data, type, id)
    : undefined;
  const spaceId = known ?? fromActive ?? fromArchived;
  const settled =
    (directory.isSuccess || directory.isError) &&
    (Boolean(fromActive) || archived.isSuccess || archived.isError);
  return { spaceId, notFound: !spaceId && settled };
}

// ---------- Agent ----------

export function useAgent(
  orgId: string,
  spaceId: string | undefined,
  agentId: string | null,
) {
  const api = useApi();
  return useQuery({
    queryKey: keys.agent(orgId, agentId ?? ""),
    queryFn: () => api.agents.get(orgId, spaceId!, agentId!),
    enabled: Boolean(spaceId && agentId),
  });
}

export function useAgentStats(agent: SpaceAgent | undefined, orgId: string) {
  const api = useApi();
  return useQuery({
    queryKey: keys.agentSub(orgId, agent?.id ?? "", "stats"),
    queryFn: () => api.agents.stats(orgId, agent!.space_id, agent!.id),
    enabled: Boolean(agent),
  });
}

export function useEnvVars(agent: SpaceAgent | undefined, orgId: string) {
  const api = useApi();
  return useQuery({
    queryKey: keys.agentSub(orgId, agent?.id ?? "", "env-vars"),
    queryFn: () => api.agents.envVars.list(orgId, agent!.space_id, agent!.id),
    enabled: Boolean(agent),
  });
}

export function useRepos(agent: SpaceAgent | undefined, orgId: string) {
  const api = useApi();
  return useQuery({
    queryKey: keys.agentSub(orgId, agent?.id ?? "", "repos"),
    queryFn: () => api.agents.repos.list(orgId, agent!.space_id, agent!.id),
    enabled: Boolean(agent),
  });
}

export function useAgentRuns(
  agent: SpaceAgent | undefined,
  orgId: string,
  enabled: boolean,
) {
  const api = useApi();
  return useInfiniteQuery({
    queryKey: keys.agentSub(orgId, agent?.id ?? "", "runs"),
    queryFn: ({ pageParam }) =>
      api.agents.runs(orgId, agent!.space_id, agent!.id, pageParam || null),
    initialPageParam: "",
    getNextPageParam: (last) => last.next_cursor ?? undefined,
    enabled: Boolean(agent) && enabled,
  });
}

export function useCrew(
  orgId: string,
  spaceId: string | undefined,
  crewId: string | null,
) {
  const api = useApi();
  return useQuery({
    queryKey: keys.crew(orgId, crewId ?? ""),
    queryFn: () => api.crews.get(orgId, spaceId!, crewId!),
    enabled: Boolean(spaceId && crewId),
  });
}

export function useContacts(orgId: string) {
  const api = useApi();
  return useQuery({
    queryKey: keys.contacts(orgId),
    queryFn: () => api.contacts.list(orgId),
  });
}

/** Agent 或小队变化后需要刷新的列表 */
export function invalidateAgentLists(queryClient: QueryClient, orgId: string) {
  void queryClient.invalidateQueries({ queryKey: keys.agentDirectory(orgId) });
  void queryClient.invalidateQueries({ queryKey: keys.contacts(orgId) });
  void queryClient.invalidateQueries({
    queryKey: ["org", orgId, "space-agents"],
  });
}

interface OptimisticOptions<TVars, TResult, TData> {
  /** 乐观更新与失败回滚的目标查询 */
  queryKey: QueryKey;
  mutationFn: (vars: TVars) => Promise<TResult>;
  optimistic?: (data: TData, vars: TVars) => TData;
  onSuccess?: (result: TResult, vars: TVars) => void;
  /** 自定义错误处理；返回 true 表示已处理，不再弹默认提示 */
  onError?: (error: unknown, vars: TVars) => boolean | void;
  /** 默认的失败提示（缺省按错误码） */
  errorMessage?: string;
}

/**
 * 写操作：乐观更新，失败回滚并提示；遇到 409 时提示并刷新目标查询与相关列表。
 * 成功后由调用方写回服务端结果。
 */
export function useOptimisticMutation<
  TVars,
  TResult = unknown,
  TData = unknown,
>(
  orgId: string,
  {
    queryKey,
    mutationFn,
    optimistic,
    onSuccess,
    onError,
    errorMessage,
  }: OptimisticOptions<TVars, TResult, TData>,
) {
  const queryClient = useQueryClient();
  const toast = useToast();
  const t = useT();
  return useMutation({
    mutationFn,
    onMutate: async (vars: TVars) => {
      if (!optimistic) return { previous: undefined as TData | undefined };
      await queryClient.cancelQueries({ queryKey });
      const previous = queryClient.getQueryData<TData>(queryKey);
      if (previous !== undefined)
        queryClient.setQueryData<TData>(queryKey, optimistic(previous, vars));
      return { previous };
    },
    onError: (error, vars, context) => {
      if (context?.previous !== undefined)
        queryClient.setQueryData(queryKey, context.previous);
      if (isApiError(error) && error.status === 409) {
        void queryClient.invalidateQueries({ queryKey });
        invalidateAgentLists(queryClient, orgId);
      }
      if (onError?.(error, vars)) return;
      toast(
        isApiError(error, "VERSION_CONFLICT")
          ? t("agents.toast.conflict")
          : (errorMessage ?? agentErrorMessage(t, error)),
        { tone: "error" },
      );
    },
    onSuccess: (result, vars) => onSuccess?.(result, vars),
  });
}

/** 写回 Agent 详情缓存，并刷新目录、联系人等列表 */
export function storeAgent(
  queryClient: QueryClient,
  orgId: string,
  agent: SpaceAgent,
) {
  rememberSpace(agent.id, agent.space_id);
  queryClient.setQueryData(keys.agent(orgId, agent.id), agent);
  invalidateAgentLists(queryClient, orgId);
}

export function storeCrew(queryClient: QueryClient, orgId: string, crew: Crew) {
  rememberSpace(crew.id, crew.space_id);
  queryClient.setQueryData(keys.crew(orgId, crew.id), crew);
  invalidateAgentLists(queryClient, orgId);
}
