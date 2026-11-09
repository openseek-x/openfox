import { useQueryClient } from "@tanstack/react-query";
import type { ReactNode } from "react";

import type { Task, TaskBatchPatch, TaskStatus } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Icon, IconButton, Tooltip } from "@openfox/ui";

import {
  ReadOnlyContext,
  ReadOnlyTip,
  useTaskReadOnlyReason,
} from "../../spaces/readonly";
import { usePriorityLevels } from "../../organization/queries";
import {
  Picker,
  LabelDot,
  assigneeGroups,
  labelGroups,
  priorityGroups,
  refFromValue,
  statusGroups,
  type PickerTriggerProps,
} from "../pickers";
import {
  latestTask,
  usePickerLabels,
  useSpaceAgents,
  useSpaceTypes,
  useStatuses,
  type Person,
} from "../queries";
import type { TaskSelection } from "./selection";
import { useBatchUpdateTasks } from "./useBatchUpdate";

// 列表 / 表格底部的批量操作栏（沿用标签管理页批量栏的浮动工具条样式）。

/** 与服务端上限一致（POST .../tasks/batch-update 一次最多 100 个任务） */
export const BULK_MAX = 100;

/** 选中任务共同可用的状态：每个任务类型工作流的交集，按全局状态顺序；有任务类型找不到工作流时为空。 */
export function commonStatuses(
  tasks: Pick<Task, "type_id">[],
  types: { id: string; statuses: { status_id: string }[] }[],
  statuses: TaskStatus[],
): TaskStatus[] {
  if (tasks.length === 0) return [];
  const sets = [...new Set(tasks.map((task) => task.type_id))].map(
    (typeId) =>
      new Set(
        types
          .find((item) => item.id === typeId)
          ?.statuses.map((s) => s.status_id) ?? [],
      ),
  );
  return statuses.filter((status) => sets.every((ids) => ids.has(status.id)));
}

export function BulkActionBar({
  orgId,
  selection,
  people,
}: {
  orgId: string;
  selection: TaskSelection;
  people: Person[];
}) {
  const t = useT();
  const queryClient = useQueryClient();
  // 选中的任务从任务缓存里取（列表的各分组分别加载，选择只存 id）
  const tasks = [...selection.selected]
    .map((id) => latestTask(queryClient, orgId, id))
    .filter((task): task is Task => Boolean(task));
  const spaceIds = [...new Set(tasks.map((task) => task.space_id))];
  const spaceId = spaceIds.length === 1 ? spaceIds[0] : undefined;
  const readOnlyReason = useTaskReadOnlyReason(orgId, spaceId);
  const readOnly = Boolean(readOnlyReason);
  const types = useSpaceTypes(orgId, spaceId);
  const statuses = useStatuses(orgId);
  const labels = usePickerLabels(orgId, spaceId);
  const spaceAgents = useSpaceAgents(orgId, spaceId);
  const priorityLevels = usePriorityLevels();
  const batch = useBatchUpdateTasks(orgId);
  if (selection.size === 0) return null;

  const agents = (spaceAgents.data ?? []).filter((agent) => !agent.archived_at);
  const available = commonStatuses(
    tasks,
    types.data ?? [],
    statuses.data ?? [],
  );
  const present = new Map<string, Task["label_items"][number]>();
  for (const task of tasks)
    for (const item of task.label_items) present.set(item.id, item);

  // 整体不可用的原因：多个空间、超过上限；只读空间用 ReadOnlyTip 提示
  const blocked =
    spaceIds.length > 1
      ? t("tasks.bulk.multipleSpaces")
      : tasks.length > BULK_MAX
        ? t("tasks.bulk.tooMany", { max: BULK_MAX })
        : null;
  const disabled = Boolean(blocked) || readOnly || batch.isPending || !spaceId;

  const submit = (patch: TaskBatchPatch) => {
    if (!spaceId || disabled) return;
    batch.mutate(
      { spaceId, tasks, patch },
      // 成功后清空选择并留在当前页面；失败保留选择以便重试
      { onSuccess: () => selection.clear() },
    );
  };

  const action = (
    label: string,
    picker: (trigger: (props: PickerTriggerProps) => ReactNode) => ReactNode,
    reason?: string | null,
  ) => {
    const off = disabled || Boolean(reason);
    const tip = blocked ?? reason;
    const node = picker(({ ref, toggle, open }) => (
      <Button
        ref={ref}
        variant="outline"
        size="28"
        aria-expanded={open}
        disabled={off}
        onClick={toggle}
      >
        {label}
      </Button>
    ));
    if (readOnly)
      return <ReadOnlyTip key={label}>{<span>{node}</span>}</ReadOnlyTip>;
    if (off && tip)
      return (
        <Tooltip key={label} content={tip}>
          <span tabIndex={0} aria-label={`${label}: ${tip}`}>
            {node}
          </span>
        </Tooltip>
      );
    return <span key={label}>{node}</span>;
  };

  const assigneeValue = (value: string, kind: "HUMAN" | "AGENT") => {
    const ref = refFromValue(value);
    submit({
      assignee: { kind, id: ref && ref.kind === kind ? ref.id : null },
    });
  };

  return (
    <ReadOnlyContext.Provider value={readOnlyReason ?? false}>
      <div
        role="toolbar"
        aria-label={t("tasks.bulk.bar")}
        className="sticky bottom-4 z-20 mx-auto mt-2 flex w-fit max-w-full flex-wrap items-center gap-1.5 rounded-xl border border-line-default bg-surface-card py-1.5 pr-1.5 pl-3.5 shadow-popover"
      >
        <span className="pr-2 text-body text-fg-secondary">
          {t("tasks.bulk.selected", { count: selection.size })}
        </span>
        {action(
          t("tasks.bulk.status"),
          (trigger) => (
            <Picker
              label={t("tasks.bulk.status")}
              groups={statusGroups(available)}
              selected={[]}
              onSelect={(id) => submit({ status_id: id })}
              trigger={trigger}
            />
          ),
          available.length === 0 && !types.isLoading && !statuses.isLoading
            ? t("tasks.bulk.noCommonStatus")
            : null,
        )}
        {action(t("tasks.bulk.priority"), (trigger) => (
          <Picker
            label={t("tasks.bulk.priority")}
            groups={priorityGroups(t, priorityLevels)}
            selected={[]}
            onSelect={(value) =>
              submit({ priority: value as TaskBatchPatch["priority"] })
            }
            trigger={trigger}
          />
        ))}
        {action(t("tasks.bulk.assignee"), (trigger) => (
          <Picker
            label={t("tasks.bulk.assignee")}
            groups={assigneeGroups(t, people, [], { agents: false })}
            selected={[]}
            onSelect={(value) => assigneeValue(value, "HUMAN")}
            trigger={trigger}
          />
        ))}
        {action(t("tasks.bulk.executor"), (trigger) => (
          <Picker
            label={t("tasks.bulk.executor")}
            groups={assigneeGroups(t, [], agents, { humans: false })}
            selected={[]}
            onSelect={(value) => assigneeValue(value, "AGENT")}
            trigger={trigger}
          />
        ))}
        {action(t("tasks.bulk.addLabel"), (trigger) => (
          <Picker
            label={t("tasks.bulk.addLabel")}
            groups={labelGroups(labels.data ?? [])}
            selected={[]}
            onSelect={(id) => submit({ labels: { add: [id] } })}
            trigger={trigger}
          />
        ))}
        {action(
          t("tasks.bulk.removeLabel"),
          (trigger) => (
            <Picker
              label={t("tasks.bulk.removeLabel")}
              groups={[
                {
                  options: [...present.values()].map((item) => ({
                    value: item.id,
                    label: item.name,
                    icon: <LabelDot color={item.color} />,
                  })),
                },
              ]}
              selected={[]}
              onSelect={(id) => submit({ labels: { remove: [id] } })}
              trigger={trigger}
            />
          ),
          present.size === 0 ? t("tasks.bulk.noLabels") : null,
        )}
        <IconButton
          label={t("tasks.bulk.clear")}
          icon={<Icon name="x" />}
          size="28"
          onClick={selection.clear}
        />
      </div>
    </ReadOnlyContext.Provider>
  );
}
