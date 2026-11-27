import type { Task, TaskStatus } from "@openfox/api";
import { useT } from "@openfox/i18n";

import { useMarkDuplicate } from "../queries";
import { TaskSearchDialog } from "./TaskSearchDialog";

/** 「重复」类别的状态不能直接切换：必须选定重复的目标任务（OPE-17）。 */
export function isDuplicateStatus(
  status: Pick<TaskStatus, "category"> | undefined,
): boolean {
  return status?.category === "DUPLICATE";
}

/** 工作流里排序最前的重复类别状态；没有时返回 undefined（不能标记为重复）。 */
export function firstDuplicateStatus(
  workflow: TaskStatus[],
): TaskStatus | undefined {
  return workflow.find(isDuplicateStatus);
}

/** 批量修改、创建任务等无法逐个选择目标的入口，状态候选里去掉重复类别。 */
export function withoutDuplicateStatuses<
  T extends Pick<TaskStatus, "category">,
>(statuses: T[]): T[] {
  return statuses.filter((status) => !isDuplicateStatus(status));
}

/** 待标记为重复的任务及选中的重复状态（省略时由服务端取第一个）。 */
export interface DuplicateTarget {
  task: Task;
  statusId?: string;
}

/**
 * 选择重复的目标任务并提交「标记为重复」。复用分拣台的目标选择器，候选只含同空间、未被标记为重复的任务。
 * 关闭（取消）时不改任何东西；成功后关闭。
 */
export function MarkDuplicateDialog({
  orgId,
  target,
  onClose,
}: {
  orgId: string;
  target: DuplicateTarget;
  onClose: () => void;
}) {
  const t = useT();
  const mark = useMarkDuplicate(orgId);
  const { task, statusId } = target;
  return (
    <TaskSearchDialog
      orgId={orgId}
      spaceId={task.space_id}
      duplicatePicker
      busy={mark.isPending}
      title={t("tasks.detail.markDuplicate")}
      excludeIds={[task.id]}
      onClose={onClose}
      onPick={(picked) =>
        mark.mutate(
          { task, targetId: picked.id, statusId },
          { onSuccess: onClose },
        )
      }
    />
  );
}
