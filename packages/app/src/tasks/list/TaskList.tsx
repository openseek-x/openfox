import { memo, useMemo, useState } from "react";

import type { Task, TaskStatus } from "@openfox/api";
import { useI18n, useT } from "@openfox/i18n";
import { Icon, Spinner, cn } from "@openfox/ui";

import { GroupGlyph, useGroupName } from "../board/GroupedBoard";
import { BulkActionBar } from "../bulk/BulkActionBar";
import { SelectBox } from "../bulk/SelectBox";
import {
  TaskSelectionContext,
  useSelectionContext,
  useTaskSelection,
} from "../bulk/selection";
import {
  applyGroupOrder,
  groupTasks,
  isTrailingGroup,
  orderedGroups,
} from "../grouping";
import type { DisplaySettings, GroupField } from "../model";
import {
  AssigneeStack,
  DueChip,
  LabelPills,
  LoadMore,
  SubtaskChip,
  displayIdOf,
} from "../parts";
import { formatMonthDay } from "../primitives/dates";
import { PriorityIcon } from "../primitives/PriorityIcon";
import { StatusIcon } from "../primitives/StatusIcon";
import { TaskTypeIcon } from "../primitives/TaskTypeIcon";
import { useTaskPages, useUpdateTask } from "../queries";
import { TaskMenu, statusFields } from "../TaskMenu";
import type { TaskViewProps } from "../views";
import { EstimateChip } from "../../spaces/EstimateField";

/**
 * 列表：按状态分组，分组可折叠；collapsed_by_default 的状态默认折叠。
 * 阶段 10c · 行首复选框（悬停、聚焦或已有选中时显示），同一分组内 Shift 连选；选中后底部出现批量操作栏。
 */
export function TaskList(props: TaskViewProps) {
  const t = useT();
  const { columns, counts, display } = props;
  const selection = useTaskSelection();
  const [toggled, setToggled] = useState<Set<string>>(() => new Set());
  const groups = applyGroupOrder(columns, display.groupOrder?.status).filter(
    (c) => display.showEmptyColumns || (counts.get(c.id) ?? 0) > 0,
  );
  const showProp = (p: DisplaySettings["properties"][number]) =>
    display.properties.includes(p);
  const hasDateHeader =
    showProp("start") ||
    showProp("due") ||
    showProp("created") ||
    showProp("updated");
  return (
    <div
      role="region"
      aria-label={t("tasks.list.label")}
      className="flex min-h-0 flex-1 flex-col"
    >
      {hasDateHeader && groups.length > 0 && (
        <div
          aria-hidden
          className="flex h-7 shrink-0 items-center gap-2 border-b border-line-soft px-4 text-caption text-fg-muted"
        >
          <span className="min-w-0 flex-1">{t("tasks.fields.task")}</span>
          <span className="flex shrink-0 items-center gap-1.5">
            {showProp("start") && (
              <span className="w-16 shrink-0 text-right">
                {t("tasks.fields.startDate")}
              </span>
            )}
            {showProp("due") && (
              <span className="w-[92px] shrink-0 text-right">
                {t("tasks.fields.dueDate")}
              </span>
            )}
            {showProp("created") && (
              <span className="w-16 shrink-0 text-right">
                {t("tasks.fields.created")}
              </span>
            )}
            {showProp("updated") && (
              <span className="w-16 shrink-0 text-right">
                {t("tasks.fields.updated")}
              </span>
            )}
          </span>
          {/* 为行尾更多操作按钮留出等宽占位，保证列头与单元格垂直对齐 */}
          <span className="w-7 shrink-0" />
        </div>
      )}
      <div className="min-h-0 flex-1 overflow-y-auto pb-4">
        <TaskSelectionContext.Provider value={selection}>
          {groups.map((column) => {
            const collapsed =
              column.collapsed_by_default !== toggled.has(column.id);
            return (
              <Group
                key={column.id}
                {...props}
                column={column}
                count={counts.get(column.id) ?? 0}
                collapsed={collapsed}
                onToggle={() =>
                  setToggled((prev) => {
                    const next = new Set(prev);
                    if (next.has(column.id)) next.delete(column.id);
                    else next.add(column.id);
                    return next;
                  })
                }
              />
            );
          })}
        </TaskSelectionContext.Provider>
        <BulkActionBar
          orgId={props.orgId}
          selection={selection}
          people={props.people}
        />
      </div>
    </div>
  );
}

function Group({
  column,
  count,
  collapsed,
  onToggle,
  ...props
}: TaskViewProps & {
  column: TaskStatus;
  count: number;
  collapsed: boolean;
  onToggle: () => void;
}) {
  const t = useT();
  const { orgId, source, params } = props;
  const pages = useTaskPages(
    orgId,
    source,
    params,
    column.id,
    count > 0 && !collapsed,
  );
  const tasks = useMemo(
    () => pages.data?.pages.flatMap((p) => p.tasks) ?? [],
    [pages.data],
  );
  const order = useMemo(() => tasks.map((task) => task.id), [tasks]);
  return (
    <section aria-label={`${column.name} ${count}`}>
      <h3>
        <button
          type="button"
          aria-expanded={!collapsed}
          aria-label={t("tasks.list.toggleGroup", { status: column.name })}
          onClick={onToggle}
          className="sticky top-0 z-10 flex h-[var(--density-group-row,36px)] w-full items-center gap-2 border-b border-line-subtle bg-surface-canvas/95 px-4 text-left text-body-sm font-semibold backdrop-blur-xs"
        >
          <Icon
            name="chevron-down"
            size={14}
            className={cn(
              "text-fg-muted transition-transform",
              collapsed && "-rotate-90",
            )}
          />
          <StatusIcon category={column.category} color={column.color} />
          {column.name}
          <span className="text-caption font-normal tabular-nums text-fg-muted">
            {count}
          </span>
        </button>
      </h3>
      {!collapsed && (
        <ul>
          {pages.isLoading && (
            <li className="flex justify-center py-3">
              <Spinner />
            </li>
          )}
          {props.display.listSubgroup === "none" ? (
            tasks.map((task) => (
              <Row key={task.id} task={task} order={order} {...props} />
            ))
          ) : (
            <Subgroups
              tasks={tasks}
              field={props.display.listSubgroup}
              {...props}
            />
          )}
          <li>
            <LoadMore
              hasMore={Boolean(pages.hasNextPage)}
              loading={pages.isFetchingNextPage}
              onLoad={() => void pages.fetchNextPage()}
            />
          </li>
        </ul>
      )}
    </section>
  );
}

/**
 * 阶段 9e · 状态分组下的二级分组：用本状态已加载的任务在前端分组，只显示有任务的子组；
 * 多处理人 / 多小队的任务在每个所属子组里各出现一次。
 */
function Subgroups({
  tasks,
  field,
  ...props
}: TaskViewProps & { tasks: Task[]; field: GroupField }) {
  const groupName = useGroupName();
  const ctx = {
    columns: props.columns,
    people: props.people,
    agents: props.agents,
    refNames: props.refNames,
  };
  const map = groupTasks(tasks, field, ctx);
  const groups = applyGroupOrder(
    orderedGroups(field, tasks, ctx),
    props.display.groupOrder?.[field],
    isTrailingGroup,
  ).filter((g) => map.has(g.key));
  return groups.map((group) => {
    const list = map.get(group.key)!;
    return (
      <li
        key={group.key}
        role="group"
        aria-label={`${groupName(group)} ${list.length}`}
      >
        <div
          aria-hidden
          className="flex h-8 items-center gap-2 border-b border-line-subtle px-6 text-body-sm text-fg-secondary"
        >
          <GroupGlyph group={group} />
          <span className="min-w-0 truncate">{groupName(group)}</span>
          <span className="text-fg-muted">{list.length}</span>
        </div>
        <ul>
          {list.map((task) => (
            <Row
              key={task.id}
              task={task}
              order={list.map((item) => item.id)}
              {...props}
            />
          ))}
        </ul>
      </li>
    );
  });
}

const Row = memo(function Row({
  task,
  orgId,
  columns,
  display,
  refNames,
  onOpen,
  order,
}: TaskViewProps & { task: Task; order: readonly string[] }) {
  const t = useT();
  const { locale } = useI18n();
  const update = useUpdateTask(orgId);
  const selection = useSelectionContext();
  const checked = Boolean(selection?.has(task.id));
  const show = (p: DisplaySettings["properties"][number]) =>
    display.properties.includes(p);
  const displayId = displayIdOf(task);
  const startFormatted = task.start_date
    ? formatMonthDay(task.start_date, locale)
    : "";
  const createdFormatted = formatMonthDay(new Date(task.created_at), locale);
  const updatedFormatted = formatMonthDay(new Date(task.updated_at), locale);
  const startTip = startFormatted
    ? t("tasks.list.dateTip", {
        label: t("tasks.fields.startDate"),
        date: startFormatted,
      })
    : t("tasks.fields.startDate");
  const createdTip = t("tasks.list.dateTip", {
    label: t("tasks.fields.created"),
    date: createdFormatted,
  });
  const updatedTip = t("tasks.list.dateTip", {
    label: t("tasks.fields.updated"),
    date: updatedFormatted,
  });
  const priorityTip =
    task.priority === "NONE"
      ? t("tasks.priorityNone")
      : t("tasks.list.fieldTip", {
          label: t("tasks.fields.priority"),
          value: t(`tasks.priority.${task.priority}`),
        });
  return (
    <li
      aria-selected={selection ? checked : undefined}
      className={cn(
        "group relative flex h-[var(--density-list-row,44px)] items-center gap-2 border-b border-line-subtle px-4 transition-colors duration-100 motion-reduce:transition-none",
        checked ? "bg-surface-selected" : "hover:bg-surface-hover",
      )}
    >
      <button
        type="button"
        onClick={() => onOpen(task)}
        className="absolute inset-0 z-0 outline-none focus-visible:shadow-focus"
        aria-label={`${displayId} ${task.title}`}
      />
      {selection && (
        <SelectBox
          label={t("tasks.bulk.select", { task: displayId })}
          checked={checked}
          onToggle={(on, range) =>
            selection.toggle(task.id, on, { range, order })
          }
          className={cn(
            "relative z-[1]",
            selection.size === 0 && "of-row-actions",
          )}
        />
      )}
      <span className="pointer-events-none relative flex min-w-0 flex-1 items-center gap-2 [&_[aria-haspopup=dialog]]:pointer-events-auto">
        {show("priority") && (
          <span
            title={priorityTip}
            onClick={() => onOpen(task)}
            className="pointer-events-auto inline-flex size-4 shrink-0 cursor-pointer items-center justify-center"
          >
            {task.priority === "NONE" ? (
              <svg
                viewBox="0 0 16 16"
                width={16}
                height={16}
                role="img"
                aria-label={t("tasks.priorityNone")}
                focusable="false"
                className="shrink-0"
              >
                <rect
                  x="3"
                  y="11"
                  width="10"
                  height="2"
                  rx="1"
                  className="fill-line-strong"
                />
              </svg>
            ) : (
              <PriorityIcon priority={task.priority} labelled />
            )}
          </span>
        )}
        {show("id") && (
          <span
            title={displayId}
            onClick={() => onOpen(task)}
            className="pointer-events-auto w-24 shrink-0 cursor-pointer truncate whitespace-nowrap text-caption text-fg-muted tabular-nums"
          >
            {displayId}
          </span>
        )}
        {show("status") && (
          <StatusIcon
            category={task.status_category}
            color={task.status_color}
            labelled
          />
        )}
        <TaskTypeIcon
          icon={task.type_icon}
          label={task.type_name}
          className="text-fg-muted"
        />
        <span className="min-w-0 truncate text-body">{task.title}</span>
        {show("subtasks") && <SubtaskChip task={task} />}
        <span className="ml-auto flex shrink-0 items-center gap-1.5">
          {show("labels") && <LabelPills task={task} max={2} />}
          {show("assignee") && <AssigneeStack task={task} />}
          {show("estimate") && <EstimateChip value={task.estimate} />}
          {show("iteration") && task.iteration_id && (
            <span className="max-w-28 truncate text-caption text-fg-muted">
              {refNames?.get(task.iteration_id)}
            </span>
          )}
          {show("project") && task.project_id && (
            <span className="max-w-28 truncate text-caption text-fg-muted">
              {refNames?.get(task.project_id)}
            </span>
          )}
          {show("start") && (
            <span
              title={startTip}
              aria-label={startTip}
              onClick={() => onOpen(task)}
              className={cn(
                "pointer-events-auto w-16 shrink-0 cursor-pointer text-right text-caption tabular-nums",
                startFormatted ? "text-fg-muted" : "text-fg-subtle",
              )}
            >
              {startFormatted || "—"}
            </span>
          )}
          {show("due") && (
            <span
              onClick={() => onOpen(task)}
              className="pointer-events-auto flex w-[92px] shrink-0 cursor-pointer justify-end"
            >
              {task.due_date ? (
                <DueChip date={task.due_date} />
              ) : (
                <span
                  title={t("tasks.fields.dueDate")}
                  aria-label={t("tasks.fields.dueDate")}
                  className="text-caption text-fg-subtle"
                >
                  —
                </span>
              )}
            </span>
          )}
          {show("created") && (
            <span
              title={createdTip}
              aria-label={createdTip}
              onClick={() => onOpen(task)}
              className="pointer-events-auto w-16 shrink-0 cursor-pointer text-right text-caption text-fg-muted tabular-nums"
            >
              {createdFormatted}
            </span>
          )}
          {show("updated") && (
            <span
              title={updatedTip}
              aria-label={updatedTip}
              onClick={() => onOpen(task)}
              className="pointer-events-auto w-16 shrink-0 cursor-pointer text-right text-caption text-fg-muted tabular-nums"
            >
              {updatedFormatted}
            </span>
          )}
        </span>
      </span>
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
        className="relative of-row-actions"
      />
    </li>
  );
});
