import {
  DndContext,
  DragOverlay,
  KeyboardSensor,
  PointerSensor,
  useDroppable,
  useSensor,
  useSensors,
  type DragEndEvent,
  type DragStartEvent,
} from "@dnd-kit/core";
import {
  SortableContext,
  arrayMove,
  sortableKeyboardCoordinates,
  verticalListSortingStrategy,
} from "@dnd-kit/sortable";
import { useMemo, useState, type ReactNode } from "react";

import type { Task } from "@openfox/api";
import { useT, type MessageKey } from "@openfox/i18n";
import { Button, Icon, IconButton, Spinner, cn } from "@openfox/ui";

import { pointerFirstCollision } from "../../dnd/collision";
import { TaskAssigneeAvatar } from "../invitationAssignees";
import {
  ALL_ROWS_KEY,
  NONE_KEY,
  boardCells,
  canDropCell,
  cellKey,
  orderedGroups,
  splitCellKey,
  taskGroupKeys,
  type GroupContext,
  type TaskGroup,
} from "../grouping";
import { planBoardMove, type GroupField } from "../model";
import { PriorityIcon } from "../primitives/PriorityIcon";
import { StatusIcon } from "../primitives/StatusIcon";
import { useTaskPages, useUpdateTask } from "../queries";
import { statusFields } from "../TaskMenu";
import type { TaskViewProps } from "../views";
import { CardBody, SortableCard } from "./Card";

// 分组看板：一次分页加载任务（与表格同一个查询），在前端按列 / 行分组。
// 多处理人 / 多小队的任务在每个所属分组里各出现一次。
// 拖动：只有按状态分列时可以跨列（改状态）；任何情况下都不能跨行；其他字段分列时只能在列内排序（手动排序时）。

const CELL_PREFIX = "cell:";

const FIELD_LABEL: Record<GroupField, MessageKey> = {
  status: "tasks.displayMenu.groupBy.status",
  assignee: "tasks.displayMenu.groupBy.assignee",
  crew: "tasks.displayMenu.groupBy.crew",
  iteration: "tasks.displayMenu.groupBy.iteration",
  priority: "tasks.displayMenu.groupBy.priority",
};

/** 分组显示名：空值组、「其他迭代」、优先级用文案 */
export function useGroupName() {
  const t = useT();
  return (group: TaskGroup): string => {
    if (group.priority)
      return group.priority === "NONE"
        ? t("tasks.priorityNone")
        : t(`tasks.priority.${group.priority}`);
    if (group.key === NONE_KEY)
      return group.field === "crew"
        ? t("tasks.grouping.noCrew")
        : group.field === "iteration"
          ? t("tasks.grouping.noIteration")
          : t("tasks.grouping.unassigned");
    if (group.name === null) return t("tasks.grouping.otherIteration");
    return group.name;
  };
}

/** 分组标题前的图标：状态图标、处理人 / 小队头像、优先级图标 */
export function GroupGlyph({ group }: { group: TaskGroup }): ReactNode {
  if (group.status)
    return (
      <StatusIcon category={group.status.category} color={group.status.color} />
    );
  if (group.priority) return <PriorityIcon priority={group.priority} />;
  if (group.assigneeKind && group.name)
    return (
      <TaskAssigneeAvatar
        name={group.name}
        kind={group.assigneeKind}
        size={20}
      />
    );
  return null;
}

export function GroupedBoard(props: TaskViewProps) {
  const { orgId, source, params, columns, counts, display, onCreate } = props;
  const t = useT();
  const groupName = useGroupName();
  const update = useUpdateTask(orgId);
  const pages = useTaskPages(orgId, source, params, null);
  const loaded = useMemo(
    () => pages.data?.pages.flatMap((p) => p.tasks) ?? [],
    [pages.data],
  );
  const columnField: GroupField = display.boardColumns;
  const rowField = display.boardRows;
  const ctx: GroupContext = useMemo(
    () => ({
      columns,
      people: props.people,
      agents: props.agents,
      refNames: props.refNames,
    }),
    [columns, props.people, props.agents, props.refNames],
  );
  // 手动排序时单元格按排序键排列：拖动后服务端返回新键，卡片随即落到新位置
  const manual = display.sort === "manual";
  const tasks = useMemo(
    () =>
      manual
        ? [...loaded].sort((a, b) =>
            a.rank < b.rank ? -1 : a.rank > b.rank ? 1 : 0,
          )
        : loaded,
    [loaded, manual],
  );
  const cells = useMemo(
    () => boardCells(tasks, columnField, rowField, ctx),
    [tasks, columnField, rowField, ctx],
  );
  const columnGroups = useMemo(
    () => orderedGroups(columnField, tasks, ctx),
    [columnField, tasks, ctx],
  );
  const rowGroups = useMemo<TaskGroup[]>(
    () =>
      rowField === "none"
        ? [{ key: ALL_ROWS_KEY, field: "status", name: "" }]
        : orderedGroups(rowField, tasks, ctx),
    [rowField, tasks, ctx],
  );
  // 分组内的任务数（按已加载任务去重计数）；按状态分列时列头用服务端计数
  const groupCount = (field: GroupField, key: string) =>
    tasks.filter((task) => taskGroupKeys(task, field, ctx).includes(key))
      .length;
  const columnCount = (group: TaskGroup) =>
    columnField === "status"
      ? (counts.get(group.key) ?? 0)
      : groupCount(columnField, group.key);
  const visibleColumns = columnGroups.filter(
    (g) =>
      display.showEmptyColumns ||
      [...cells.keys()].some((k) => k.endsWith(`|${g.key}`)),
  );
  const visibleRows =
    rowField === "none"
      ? rowGroups
      : rowGroups.filter(
          (g) =>
            display.showEmptyRows ||
            [...cells.keys()].some((k) => k.startsWith(`${g.key}|`)),
        );
  const total = [...counts.values()].reduce((sum, n) => sum + n, 0);

  // 拖动规则：跨列只在按状态分列时允许；不能跨行；列内排序要求手动排序。
  const allowReorder = source.manualSort && manual;
  const crossColumns = columnField === "status";
  const dragDisabled = !crossColumns && !allowReorder;
  const [dragging, setDragging] = useState<{ task: Task; cell: string } | null>(
    null,
  );
  // 拖动后、服务端返回前的单元格顺序（乐观显示），请求结束后清除
  const [pending, setPending] = useState<{
    cell: string;
    ids: string[];
  } | null>(null);
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 4 } }),
    useSensor(KeyboardSensor, {
      coordinateGetter: sortableKeyboardCoordinates,
      keyboardCodes: { start: ["Space"], cancel: ["Escape"], end: ["Space"] },
    }),
  );
  const cellTasks = (key: string): Task[] => {
    const list = cells.get(key) ?? [];
    if (pending?.cell !== key) return list;
    const byId = new Map(list.map((task) => [task.id, task]));
    const ordered = pending.ids.flatMap((id) => byId.get(id) ?? []);
    return ordered.length === list.length ? ordered : list;
  };
  /** 落点所在的单元格：放在卡片上取卡片的单元格，放在空白处取单元格本身 */
  const targetOf = (overId: string, overData: unknown) => {
    if (overId.startsWith(CELL_PREFIX))
      return { cell: overId.slice(CELL_PREFIX.length), overTask: null };
    const data = overData as { cell?: string; task?: Task } | undefined;
    return data?.cell && data.task
      ? { cell: data.cell, overTask: data.task.id }
      : null;
  };
  const canDrop = (from: string, to: string) =>
    canDropCell(from, to, { crossColumns, allowReorder });

  const onDragStart = ({ active }: DragStartEvent) => {
    const data = active.data.current as { task?: Task; cell?: string };
    if (data?.task && data.cell)
      setDragging({ task: data.task, cell: data.cell });
  };
  const onDragEnd = ({ active, over }: DragEndEvent) => {
    setDragging(null);
    const data = active.data.current as { task?: Task; cell?: string };
    const task = data?.task;
    if (!task || !data.cell || !over) return;
    const target = targetOf(String(over.id), over.data.current);
    if (!target || !canDrop(data.cell, target.cell)) return;
    const crossing = target.cell !== data.cell;
    const list = cellTasks(target.cell);
    const ids = list.map((x) => x.id);
    let toIndex: number;
    if (target.overTask === null)
      toIndex = ids.filter((id) => id !== task.id).length;
    else if (!crossing)
      toIndex = arrayMove(
        ids,
        ids.indexOf(task.id),
        ids.indexOf(target.overTask),
      ).indexOf(task.id);
    else toIndex = Math.max(0, ids.indexOf(target.overTask));
    const toStatusId = crossing
      ? splitCellKey(target.cell).column
      : task.status_id;
    const patch = planBoardMove(
      { taskId: task.id, fromStatusId: task.status_id, toStatusId, toIndex },
      list,
      allowReorder,
    );
    if (!patch) return;
    if (allowReorder) {
      const rest = ids.filter((id) => id !== task.id);
      rest.splice(Math.min(toIndex, rest.length), 0, task.id);
      setPending({ cell: target.cell, ids: rest });
    }
    const status = crossing
      ? columns.find((c) => c.id === toStatusId)
      : undefined;
    update.mutate(
      { task, patch, optimistic: status ? statusFields(status) : undefined },
      { onSettled: () => setPending(null) },
    );
  };

  // 拖动时的提示：说明本分组下拖动受限的原因
  const hint = !dragging
    ? null
    : !crossColumns
      ? t("tasks.grouping.dragWithinColumn", {
          field: t(FIELD_LABEL[columnField]),
        })
      : rowField !== "none"
        ? t("tasks.grouping.dragWithinCell", {
            field: t(FIELD_LABEL[rowField]),
          })
        : null;
  const disabledHint = dragDisabled
    ? t("tasks.grouping.dragNoReorder", { field: t(FIELD_LABEL[columnField]) })
    : undefined;

  const columnHeader = (group: TaskGroup) => (
    <header className="flex h-10 items-center gap-2 px-3">
      <GroupGlyph group={group} />
      <h3 className="min-w-0 truncate text-body font-medium">
        {groupName(group)}
      </h3>
      <span className="text-body-sm text-fg-muted" data-testid="column-count">
        {columnCount(group)}
      </span>
      {onCreate && columnField === "status" && group.status && (
        <IconButton
          label={t("tasks.newInStatus", { status: groupName(group) })}
          icon={<Icon name="plus" />}
          size="28"
          className="ml-auto"
          onClick={() => onCreate(group.key)}
        />
      )}
    </header>
  );
  const cell = (row: TaskGroup, column: TaskGroup) => {
    const key = cellKey(row.key, column.key);
    return (
      <Cell
        key={key}
        cellId={key}
        label={
          rowField === "none"
            ? `${groupName(column)} ${columnCount(column)}`
            : t("tasks.grouping.cell", {
                column: groupName(column),
                row: groupName(row),
              })
        }
        tasks={cellTasks(key)}
        droppable={!dragging || canDrop(dragging.cell, key)}
        dragDisabled={dragDisabled}
        viewProps={props}
        header={rowField === "none" ? columnHeader(column) : undefined}
      />
    );
  };

  return (
    <DndContext
      sensors={sensors}
      collisionDetection={pointerFirstCollision}
      onDragStart={onDragStart}
      onDragEnd={onDragEnd}
      onDragCancel={() => setDragging(null)}
      accessibility={{
        screenReaderInstructions: { draggable: t("tasks.board.instructions") },
      }}
    >
      <div className="flex min-h-0 flex-1 flex-col">
        {(hint || disabledHint) && (
          <p
            role="status"
            className="mx-4 mb-2 flex items-center gap-1.5 rounded-lg bg-surface-muted px-3 py-1.5 text-caption text-fg-secondary"
          >
            <Icon name="info" size={14} />
            {hint ?? disabledHint}
          </p>
        )}
        <div
          role="region"
          aria-label={t("tasks.board.label")}
          className={cn(
            "flex min-h-0 flex-1 overflow-auto px-4 pb-4",
            rowField === "none" ? "gap-3" : "flex-col gap-4",
          )}
        >
          {pages.isLoading && <Spinner className="mx-auto my-4" />}
          {rowField === "none" ? (
            visibleColumns.map((column) => cell(rowGroups[0]!, column))
          ) : (
            <>
              <div className="flex gap-3">
                {visibleColumns.map((column) => (
                  <div
                    key={column.key}
                    className="w-[280px] shrink-0 rounded-xl bg-surface-muted/60"
                  >
                    {columnHeader(column)}
                  </div>
                ))}
              </div>
              {visibleRows.map((row) => (
                <section
                  key={row.key}
                  aria-label={`${groupName(row)} ${groupCount(rowField, row.key)}`}
                  className="flex flex-col gap-2"
                >
                  <h4 className="flex items-center gap-2 text-body font-medium">
                    <GroupGlyph group={row} />
                    {groupName(row)}
                    <span className="text-body-sm font-normal text-fg-muted">
                      {groupCount(rowField, row.key)}
                    </span>
                  </h4>
                  <div className="flex gap-3">
                    {visibleColumns.map((column) => cell(row, column))}
                  </div>
                </section>
              ))}
            </>
          )}
        </div>
        {pages.hasNextPage && (
          <div className="flex items-center justify-center gap-2 px-4 pb-3 text-caption text-fg-muted">
            {t("tasks.grouping.loaded", { loaded: loaded.length, total })}
            <Button
              variant="ghost-muted"
              size="28"
              loading={pages.isFetchingNextPage}
              onClick={() => void pages.fetchNextPage()}
            >
              {t("tasks.loadMore")}
            </Button>
          </div>
        )}
      </div>
      <DragOverlay>
        {dragging && (
          <CardBody task={dragging.task} display={display} overlay />
        )}
      </DragOverlay>
    </DndContext>
  );
}

/** 一个单元格（行不分组时就是一整列，带列头） */
function Cell({
  cellId,
  label,
  tasks,
  droppable,
  dragDisabled,
  viewProps,
  header,
}: {
  cellId: string;
  label: string;
  tasks: Task[];
  /** 当前拖动的卡片能否放进来；不能时不高亮 */
  droppable: boolean;
  dragDisabled: boolean;
  viewProps: TaskViewProps;
  header?: ReactNode;
}) {
  const { setNodeRef, isOver } = useDroppable({
    id: `${CELL_PREFIX}${cellId}`,
    disabled: !droppable,
  });
  return (
    <section
      aria-label={label}
      className="flex w-[280px] shrink-0 flex-col rounded-xl bg-surface-muted/60"
    >
      {header}
      <div
        ref={setNodeRef}
        className={cn(
          "flex min-h-[80px] flex-1 flex-col gap-2 overflow-y-auto p-2",
          header !== undefined && "pt-0",
          isOver && droppable && "rounded-lg bg-surface-active/50",
        )}
      >
        <SortableContext
          id={cellId}
          items={tasks.map((task) => `${cellId}#${task.id}`)}
          strategy={verticalListSortingStrategy}
        >
          {tasks.map((task) => (
            <SortableCard
              key={task.id}
              task={task}
              {...viewProps}
              cell={cellId}
              dragDisabled={dragDisabled}
            />
          ))}
        </SortableContext>
      </div>
    </section>
  );
}
