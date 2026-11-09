import {
  useInfiniteQuery,
  useMutation,
  useQueryClient,
  type QueryClient,
} from "@tanstack/react-query";

import { newIdempotencyKey, type Api, type Task } from "@openfox/api";

import { keys, useApi } from "../../context";

// 任务回收站：空间「最近删除」列表与恢复。
// 查询键挂在 keys.tasks 前缀下：任务实时事件（task.changed）与任务写操作的失效会一起刷新回收站。

export const TRASH_PAGE_SIZE = 50;

export const trashKey = (orgId: string, spaceId: string) =>
  [...keys.tasks(orgId), "trash", spaceId] as const;

export function useDeletedTasks(orgId: string, spaceId: string) {
  const api = useApi();
  return useInfiniteQuery({
    queryKey: trashKey(orgId, spaceId),
    queryFn: ({ pageParam }) =>
      api.tasks.deleted(
        orgId,
        spaceId,
        pageParam || undefined,
        TRASH_PAGE_SIZE,
      ),
    initialPageParam: "",
    getNextPageParam: (last) => last.next_cursor || undefined,
    enabled: Boolean(orgId && spaceId),
  });
}

export interface RestoreTaskVars {
  spaceId: string;
  taskId: string;
  /** 已删除任务的当前版本（删除请求的 version + 1） */
  version: number;
}

/** 恢复后刷新全部任务查询：列表、计数、详情、子资源与回收站。 */
export function refreshAfterRestore(queryClient: QueryClient, orgId: string) {
  void queryClient.invalidateQueries({ queryKey: keys.tasks(orgId) });
}

/**
 * 调恢复接口并刷新缓存。不依赖组件生命周期：删除提示里的「撤销」在详情抽屉关闭后仍可调用。
 * 每次调用带新的 Idempotency-Key，网络重试不会重复恢复。
 */
export async function restoreDeletedTask(
  api: Api,
  queryClient: QueryClient,
  orgId: string,
  vars: RestoreTaskVars,
): Promise<Task> {
  try {
    return await api.tasks.restore(
      orgId,
      vars.spaceId,
      vars.taskId,
      vars.version,
      newIdempotencyKey(),
    );
  } finally {
    // 成功或失败（409 已被恢复 / 版本不符）都以服务端为准刷新
    refreshAfterRestore(queryClient, orgId);
  }
}

export function useRestoreTask(orgId: string) {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    scope: { id: `task-write:${orgId}` },
    mutationFn: (vars: RestoreTaskVars) =>
      restoreDeletedTask(api, queryClient, orgId, vars),
  });
}
