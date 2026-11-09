import { useSortable } from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import { memo } from "react";

import type { Task } from "@openfox/api";
import { useI18n, useT } from "@openfox/i18n";
import { cn } from "@openfox/ui";

import type { DisplaySettings } from "../model";
import {
  AssigneeStack,
  DueChip,
  LabelPills,
  SubtaskChip,
  displayIdOf,
} from "../parts";
import { formatMonthDay } from "../primitives/dates";
import { PriorityIcon } from "../primitives/PriorityIcon";
import { StatusIcon } from "../primitives/StatusIcon";
import { TaskTypeIcon } from "../primitives/TaskTypeIcon";
import { useUpdateTask } from "../queries";
import { TaskMenu, statusFields } from "../TaskMenu";
import type { TaskViewProps } from "../views";
import { EstimateChip } from "../../spaces/EstimateField";
import { useTaskReadOnlyReason } from "../../spaces/readonly";

// 看板卡片由按状态分列的看板与分组看板共用。

/** 拖动时卡片携带的数据 */
export function cardLabel(data: unknown): string {
  const task = (data as { task?: Task } | undefined)?.task;
  return task ? `${displayIdOf(task)} ${task.title}` : "";
}

export const SortableCard = memo(function SortableCard({
  task,
  orgId,
  columns,
  display,
  onOpen,
  cell,
  dragDisabled = false,
}: TaskViewProps & {
  task: Task;
  /** 分组看板的单元格键：同一任务可出现在多个单元格，拖动 id 要带上它 */
  cell?: string;
  dragDisabled?: boolean;
}) {
  const update = useUpdateTask(orgId);
  // 已停用空间的卡片不能拖动。
  const readOnlyReason = useTaskReadOnlyReason(orgId, task.space_id);
  const readOnly = Boolean(readOnlyReason);
  const {
    attributes,
    listeners,
    setNodeRef,
    transform,
    transition,
    isDragging,
  } = useSortable({
    id: cell === undefined ? task.id : `${cell}#${task.id}`,
    data: { task, statusId: task.status_id, cell },
    disabled: readOnly || dragDisabled,
  });
  return (
    <div
      ref={setNodeRef}
      title={readOnlyReason}
      data-readonly={readOnly || undefined}
      style={{
        transform: CSS.Transform.toString(transform),
        transition,
      }}
      {...attributes}
      {...listeners}
      aria-label={`${displayIdOf(task)} ${task.title}`}
      onKeyDown={(event) => {
        if (event.key === "Enter") {
          event.preventDefault();
          onOpen(task);
          return;
        }
        listeners?.onKeyDown?.(event);
      }}
      onClick={() => onOpen(task)}
      className={cn(
        "group relative cursor-pointer rounded-lg outline-none focus-visible:shadow-focus",
        isDragging && "opacity-40",
      )}
    >
      <CardBody task={task} display={display} />
      <TaskMenu
        orgId={orgId}
        task={task}
        statuses={columns}
        onOpen={() => onOpen(task)}
        onMove={(status) =>
          update.mutate({
            task,
            patch: { status_id: status.id },
            optimistic: statusFields(status),
          })
        }
        className="absolute top-1.5 right-1.5 bg-surface-card opacity-0 group-focus-within:opacity-100 group-hover:opacity-100"
      />
    </div>
  );
});

/** 卡片内容：编号 + 处理人、状态 + 类型 + 标题、胶囊、创建日期 + 优先级。 */
export const CardBody = memo(function CardBody({
  task,
  display,
  overlay = false,
}: {
  task: Task;
  display: DisplaySettings;
  overlay?: boolean;
}) {
  const t = useT();
  const { locale } = useI18n();
  const show = (p: DisplaySettings["properties"][number]) =>
    display.properties.includes(p);
  return (
    <article
      className={cn(
        "flex flex-col gap-[var(--density-card-gap,6px)] rounded-lg border border-line-default bg-surface-card p-[var(--density-card-padding,10px)] shadow-card",
        overlay && "rotate-1 shadow-popover",
      )}
    >
      <div className="flex h-5 items-center gap-2">
        {show("id") && (
          <span className="min-w-0 truncate text-caption text-fg-muted">
            {displayIdOf(task)}
          </span>
        )}
        <span className="ml-auto pr-6">
          {show("assignee") && <AssigneeStack task={task} />}
        </span>
      </div>
      <div className="flex items-start gap-1.5">
        {show("status") && (
          <StatusIcon
            category={task.status_category}
            color={task.status_color}
            className="mt-0.5"
          />
        )}
        <TaskTypeIcon
          icon={task.type_icon}
          label={task.type_name}
          className="mt-0.5 text-fg-muted"
        />
        <span className="min-w-0 flex-1 text-body font-medium break-words">
          {task.title}
        </span>
      </div>
      <div className="flex flex-wrap items-center gap-1">
        {show("subtasks") && <SubtaskChip task={task} />}
        {show("start") && task.start_date && (
          <span className="text-caption text-fg-muted">
            {formatMonthDay(task.start_date, locale)} →
          </span>
        )}
        {show("due") && <DueChip date={task.due_date} />}
        {show("estimate") && <EstimateChip value={task.estimate} />}
        {show("labels") && <LabelPills task={task} />}
      </div>
      <div className="flex items-center gap-2 text-caption text-fg-muted">
        {show("created") && (
          <span>
            {t("tasks.createdOn", {
              date: formatMonthDay(new Date(task.created_at), locale),
            })}
          </span>
        )}
        {show("updated") && (
          <span>{formatMonthDay(new Date(task.updated_at), locale)}</span>
        )}
        {show("priority") && task.priority !== "NONE" && (
          <PriorityIcon priority={task.priority} labelled className="ml-auto" />
        )}
      </div>
    </article>
  );
});
