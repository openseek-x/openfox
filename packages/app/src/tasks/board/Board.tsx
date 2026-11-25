import {
  DndContext,
  DragOverlay,
  KeyboardSensor,
  PointerSensor,
  useDroppable,
  useSensor,
  useSensors,
  type Announcements,
  type DragEndEvent,
  type DragStartEvent,
} from "@dnd-kit/core";
import {
  SortableContext,
  arrayMove,
  sortableKeyboardCoordinates,
  verticalListSortingStrategy,
} from "@dnd-kit/sortable";
import { useQueryClient, type InfiniteData } from "@tanstack/react-query";
import { useId, useMemo, useState } from "react";

import type { Task, TaskPageResult, TaskStatus } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Icon, IconButton, Spinner, cn } from "@openfox/ui";

import { keys } from "../../context";
import { pointerFirstCollision } from "../../dnd/collision";
import { applyGroupOrder, isStatusBoard } from "../grouping";
import { planBoardMove } from "../model";
import { LoadMore } from "../parts";
import { StatusIcon } from "../primitives/StatusIcon";
import { useTaskPages, useUpdateTask } from "../queries";
import { statusFields } from "../TaskMenu";
import type { BoardProps, TaskViewProps } from "../views";
import { CardBody, SortableCard, cardLabel as label } from "./Card";
import { BoardColumnJump } from "./BoardColumnJump";
import { GroupedBoard } from "./GroupedBoard";
import { useBoardPosition } from "./useBoardPosition";

const COLUMN_PREFIX = "column:";

/**
 * 看板入口。阶段 9e：按状态分列且行不分组时用原来的状态看板（每列单独分页）；
 * 其他分组用分组看板（一次分页、在前端按列 / 行分组）。
 */
export function Board(props: BoardProps) {
  return isStatusBoard(props.display) ? (
    <StatusBoard key={props.orgId + ":" + props.source.scope} {...props} />
  ) : (
    <GroupedBoard {...props} />
  );
}

/** 每个工作流状态占一列，仅为有任务的列加载卡片。 */
function StatusBoard(props: BoardProps) {
  const boardId = useId();
  const { orgId, source, params, columns, counts, display } = props;
  const t = useT();
  const queryClient = useQueryClient();
  const update = useUpdateTask(orgId);
  const [dragging, setDragging] = useState<Task | null>(null);
  const allowReorder = source.manualSort && display.sort === "manual";

  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 4 } }),
    useSensor(KeyboardSensor, {
      coordinateGetter: sortableKeyboardCoordinates,
      // 回车留给「打开任务」，空格拿起 / 放下
      keyboardCodes: {
        start: ["Space"],
        cancel: ["Escape"],
        end: ["Space"],
      },
    }),
  );

  const orderedColumns = applyGroupOrder(columns, display.groupOrder?.status);

  const firstColumn = orderedColumns.find(
    (column) => (counts.get(column.id) ?? 0) > 0,
  );
  const board = useBoardPosition(firstColumn && `${boardId}-${firstColumn.id}`);

  const columnTasks = (statusId: string): Task[] => {
    const data = queryClient.getQueryData<
      InfiniteData<TaskPageResult<Task>, string>
    >(keys.taskList(orgId, source.scope, params, statusId));
    return data?.pages.flatMap((p) => p.tasks) ?? [];
  };
  const statusName = (id: string | undefined) =>
    columns.find((c) => c.id === id)?.name ?? "";

  const targetOf = (overId: string, overData: unknown) => {
    if (overId.startsWith(COLUMN_PREFIX))
      return { statusId: overId.slice(COLUMN_PREFIX.length), overTask: null };
    const statusId = (overData as { statusId?: string } | undefined)?.statusId;
    return statusId ? { statusId, overTask: overId } : null;
  };

  const onDragStart = ({ active }: DragStartEvent) => {
    setDragging((active.data.current as { task?: Task })?.task ?? null);
  };

  const onDragEnd = ({ active, over }: DragEndEvent) => {
    setDragging(null);
    const task = (active.data.current as { task?: Task })?.task;
    if (!task || !over) return;
    const target = targetOf(String(over.id), over.data.current);
    if (!target) return;
    const list = columnTasks(target.statusId);
    let toIndex: number;
    if (target.overTask === null) {
      toIndex = list.filter((x) => x.id !== task.id).length;
    } else if (target.statusId === task.status_id) {
      const ids = list.map((x) => x.id);
      const moved = arrayMove(
        ids,
        ids.indexOf(task.id),
        ids.indexOf(target.overTask),
      );
      toIndex = moved.indexOf(task.id);
    } else {
      toIndex = Math.max(
        0,
        list.findIndex((x) => x.id === target.overTask),
      );
    }
    const patch = planBoardMove(
      {
        taskId: task.id,
        fromStatusId: task.status_id,
        toStatusId: target.statusId,
        toIndex,
      },
      list,
      allowReorder,
    );
    if (!patch) return;
    const status = columns.find((c) => c.id === target.statusId);
    update.mutate({
      task,
      patch,
      optimistic: status ? statusFields(status) : undefined,
      index: toIndex,
    });
  };

  const announcements: Announcements = {
    onDragStart: ({ active }) =>
      t("tasks.board.picked", { task: label(active.data.current) }),
    onDragOver: ({ active, over }) =>
      over
        ? t("tasks.board.over", {
            task: label(active.data.current),
            column: statusName(
              targetOf(String(over.id), over.data.current)?.statusId,
            ),
          })
        : undefined,
    onDragEnd: ({ active, over }) =>
      over
        ? t("tasks.board.dropped", {
            task: label(active.data.current),
            column: statusName(
              targetOf(String(over.id), over.data.current)?.statusId,
            ),
          })
        : t("tasks.board.cancelled", { task: label(active.data.current) }),
    onDragCancel: ({ active }) =>
      t("tasks.board.cancelled", { task: label(active.data.current) }),
  };

  return (
    <DndContext
      sensors={sensors}
      collisionDetection={pointerFirstCollision}
      onDragStart={onDragStart}
      onDragEnd={onDragEnd}
      onDragCancel={() => setDragging(null)}
      accessibility={{
        announcements,
        screenReaderInstructions: { draggable: t("tasks.board.instructions") },
      }}
    >
      <BoardColumnJump
        toolbar={props.toolbar}
        boardId={boardId}
        options={orderedColumns.map((column) => ({
          value: column.id,
          label: column.name,
          icon: <StatusIcon category={column.category} color={column.color} />,
          count: counts.get(column.id) ?? 0,
        }))}
      />
      <div
        ref={board}
        role="region"
        aria-label={t("tasks.board.label")}
        className="flex min-h-0 flex-1 gap-3 overflow-x-auto px-4 pb-4"
      >
        {orderedColumns.map((column) => (
          <Column
            key={column.id}
            {...props}
            column={column}
            headingId={`${boardId}-${column.id}`}
            count={counts.get(column.id) ?? 0}
          />
        ))}
      </div>
      <DragOverlay>
        {dragging && <CardBody task={dragging} display={display} overlay />}
      </DragOverlay>
    </DndContext>
  );
}

function Column({
  column,
  headingId,
  count,
  ...props
}: TaskViewProps & { column: TaskStatus; headingId: string; count: number }) {
  const t = useT();
  const { orgId, source, params, onCreate } = props;
  const pages = useTaskPages(orgId, source, params, column.id, count > 0);
  const tasks = useMemo(
    () => (count > 0 ? (pages.data?.pages.flatMap((p) => p.tasks) ?? []) : []),
    [count, pages.data],
  );
  const { setNodeRef, isOver } = useDroppable({
    id: `${COLUMN_PREFIX}${column.id}`,
  });
  return (
    <section
      aria-label={`${column.name} ${count}`}
      className="flex w-[280px] shrink-0 flex-col rounded-xl bg-surface-muted/60"
    >
      <header className="flex h-10 items-center gap-2 px-3">
        <StatusIcon category={column.category} color={column.color} />
        <h3
          id={headingId}
          tabIndex={-1}
          className="min-w-0 truncate rounded-sm text-body font-medium"
        >
          {column.name}
        </h3>
        <span className="text-body-sm text-fg-muted" data-testid="column-count">
          {count}
        </span>
        {onCreate && (
          <IconButton
            label={t("tasks.newInStatus", { status: column.name })}
            icon={<Icon name="plus" />}
            size="28"
            className="ml-auto"
            onClick={() => onCreate(column.id)}
          />
        )}
      </header>
      <div
        ref={setNodeRef}
        className={cn(
          "flex min-h-[80px] flex-1 flex-col gap-2 overflow-y-auto px-2 pb-2",
          isOver && "rounded-lg bg-surface-active/50",
        )}
      >
        {pages.isLoading && <Spinner className="mx-auto my-4" />}
        <SortableContext
          id={column.id}
          items={tasks.map((task) => task.id)}
          strategy={verticalListSortingStrategy}
        >
          {tasks.map((task) => (
            <SortableCard key={task.id} task={task} {...props} />
          ))}
        </SortableContext>
        <LoadMore
          hasMore={count > 0 && Boolean(pages.hasNextPage)}
          loading={pages.isFetchingNextPage}
          onLoad={() => void pages.fetchNextPage()}
        />
      </div>
    </section>
  );
}
