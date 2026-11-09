import { useMutation, useQueryClient } from "@tanstack/react-query";

import {
  isApiError,
  newIdempotencyKey,
  type Task,
  type TaskBatchPatch,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import { useToast } from "@openfox/ui";

import { keys, useApi } from "../../context";
import { applyTaskToCache, latestTask, taskErrorMessage } from "../queries";

export interface BatchUpdateVars {
  spaceId: string;
  tasks: Task[];
  patch: TaskBatchPatch;
}

/**
 * 批量修改任务（服务端单事务）：不做乐观更新，结果以服务端为准。
 * 与单条任务写操作同一个串行范围，发送时取缓存里的最新 version；每次提交一个新的 Idempotency-Key。
 * 成功：写回缓存、失效任务查询并提示「已更新 N 个任务」；失败：提示原因，版本冲突时刷新列表。
 */
export function useBatchUpdateTasks(orgId: string) {
  const api = useApi();
  const t = useT();
  const toast = useToast();
  const queryClient = useQueryClient();
  return useMutation({
    scope: { id: `task-write:${orgId}` },
    mutationFn: ({ spaceId, tasks, patch }: BatchUpdateVars) =>
      api.tasks.batchUpdate(
        orgId,
        spaceId,
        {
          tasks: tasks.map((task) => ({
            id: task.id,
            version:
              latestTask(queryClient, orgId, task.id)?.version ?? task.version,
          })),
          patch,
        },
        newIdempotencyKey(),
      ),
    onSuccess: (result) => {
      for (const saved of result.tasks)
        applyTaskToCache(queryClient, orgId, saved);
      toast(t("tasks.bulk.updated", { count: result.tasks.length }));
      void queryClient.invalidateQueries({ queryKey: keys.tasks(orgId) });
    },
    onError: (error) => {
      toast(
        isApiError(error, "VERSION_CONFLICT")
          ? t("tasks.bulk.conflict")
          : taskErrorMessage(t, error),
        { tone: "error" },
      );
      if (isApiError(error))
        void queryClient.invalidateQueries({ queryKey: keys.tasks(orgId) });
    },
  });
}
