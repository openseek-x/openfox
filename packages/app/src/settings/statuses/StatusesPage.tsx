import {
  DndContext,
  KeyboardSensor,
  PointerSensor,
  useDroppable,
  useSensor,
  useSensors,
  type Announcements,
  type DragEndEvent,
  type DragOverEvent,
  type DragStartEvent,
  type UniqueIdentifier,
} from "@dnd-kit/core";
import {
  SortableContext,
  arrayMove,
  sortableKeyboardCoordinates,
  useSortable,
  verticalListSortingStrategy,
} from "@dnd-kit/sortable";
import { CSS as DndCSS } from "@dnd-kit/utilities";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useMemo, useRef, useState } from "react";
import { useParams } from "react-router-dom";

import type { TaskStatus, TaskStatusCategory } from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Button,
  Icon,
  IconButton,
  Menu,
  MenuItem,
  MenuSeparator,
  Tooltip,
  cn,
  useToast,
} from "@openfox/ui";

import { keys, useApi } from "../../context";
import { pointerFirstCollision } from "../../dnd/collision";
import { StatusIcon } from "../../tasks/primitives/StatusIcon";
import {
  ConfigPageHeader,
  EmptyState,
  ListSkeleton,
  useCanManage,
  useConfigMutation,
} from "../common";
import {
  DEFAULT_ALLOWED,
  STATUS_PAGE_CATEGORIES,
  applyMove,
  buildOrder,
  checkMove,
  findCategory,
  resolveDrop,
  type PageCategory,
  type StatusOrder,
} from "./order";
import { DeleteStatusDialog, StatusEditorDialog } from "./StatusDialogs";

type Editor =
  | { mode: "create"; category: PageCategory }
  | { mode: "edit"; status: TaskStatus };

/** 全局配置 · 任务状态。 */
export function StatusesPage() {
  const { orgId = "" } = useParams();
  const t = useT();
  const api = useApi();
  const queryClient = useQueryClient();
  const toast = useToast();
  const canManage = useCanManage(orgId);
  const listKey = keys.taskStatuses(orgId);
  const statusesQuery = useQuery({
    queryKey: listKey,
    queryFn: () => api.taskStatuses.list(orgId),
  });
  const statuses = useMemo(
    () => statusesQuery.data ?? [],
    [statusesQuery.data],
  );
  const byId = useMemo(
    () => new Map(statuses.map((status) => [status.id, status])),
    [statuses],
  );
  const serverOrder = useMemo(() => buildOrder(statuses), [statuses]);

  const [editor, setEditor] = useState<Editor | null>(null);
  const [deleting, setDeleting] = useState<TaskStatus | null>(null);
  // 拖拽过程中的本地顺序；松手后清空，回到服务端（或乐观更新后的）顺序
  const [dragOrder, setDragOrder] = useState<StatusOrder | null>(null);
  const dragOrderRef = useRef<StatusOrder | null>(null);
  const setDrag = (next: StatusOrder | null) => {
    dragOrderRef.current = next;
    setDragOrder(next);
  };
  const order = dragOrder ?? serverOrder;

  const categoryName = (category: TaskStatusCategory) =>
    category === "TRIAGE"
      ? t("tasks.statusCategory.TRIAGE")
      : t(`settings.statuses.categories.${category}.name`);

  // ---------- 写操作 ----------
  const move = useConfigMutation<
    { status: TaskStatus; category: PageCategory; beforeId: string | null },
    TaskStatus[],
    TaskStatus[]
  >({
    queryKey: listKey,
    mutationFn: ({ status, category, beforeId }) =>
      api.taskStatuses.move(orgId, status.id, {
        category,
        before_status_id: beforeId,
        version: status.version,
      }),
    optimistic: (list, { status, category, beforeId }) =>
      applyMove(list, status.id, category, beforeId),
    onSuccess: (list, { status, category }) => {
      queryClient.setQueryData(listKey, list);
      toast(
        category === status.category
          ? t("settings.statuses.toast.reordered")
          : t("settings.statuses.toast.moved", {
              name: status.name,
              category: categoryName(category),
            }),
      );
    },
  });

  const setDefault = useConfigMutation<TaskStatus, TaskStatus[], TaskStatus[]>({
    queryKey: listKey,
    mutationFn: (status) =>
      api.taskStatuses.setDefault(orgId, status.id, status.version),
    optimistic: (list, status) =>
      list.map((item) => ({ ...item, is_default: item.id === status.id })),
    onSuccess: (list, status) => {
      queryClient.setQueryData(listKey, list);
      toast(t("settings.statuses.toast.defaultSet", { name: status.name }));
    },
  });

  const remove = useConfigMutation<
    { status: TaskStatus; replacementId?: string },
    unknown,
    TaskStatus[]
  >({
    queryKey: listKey,
    mutationFn: ({ status, replacementId }) =>
      api.taskStatuses.remove(orgId, status.id, replacementId),
    optimistic: (list, { status }) =>
      list.filter((item) => item.id !== status.id),
    onSuccess: (_, { status }) => {
      setDeleting(null);
      toast(t("settings.statuses.toast.deleted", { name: status.name }));
    },
  });

  const commitMove = (
    status: TaskStatus,
    category: PageCategory,
    beforeId: string | null,
  ) => {
    if (checkMove(status, category) === "default-category") {
      toast(t("settings.statuses.defaultCategoryOnly"), { tone: "error" });
      return;
    }
    move.mutate({ status, category, beforeId });
  };

  const requestRemove = (status: TaskStatus) => {
    if (status.task_count > 0) setDeleting(status);
    else remove.mutate({ status });
  };

  // ---------- 拖拽 ----------
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 4 } }),
    useSensor(KeyboardSensor, {
      coordinateGetter: sortableKeyboardCoordinates,
    }),
  );
  const nameOf = (id: UniqueIdentifier) => {
    const key = String(id);
    const status = byId.get(key);
    if (status) return status.name;
    return findCategory(serverOrder, key) === key
      ? categoryName(key as PageCategory)
      : key;
  };
  const containerOf = (id: UniqueIdentifier) => {
    const current = dragOrderRef.current ?? serverOrder;
    const category = findCategory(current, String(id));
    return category ? categoryName(category) : "";
  };
  const announcements: Announcements = {
    onDragStart: ({ active }) =>
      t("settings.statuses.dnd.picked", { name: nameOf(active.id) }),
    onDragOver: ({ active, over }) =>
      over
        ? t("settings.statuses.dnd.over", {
            name: nameOf(active.id),
            category: containerOf(over.id),
          })
        : "",
    onDragEnd: ({ active, over }) =>
      over
        ? t("settings.statuses.dnd.dropped", {
            name: nameOf(active.id),
            category: containerOf(over.id),
          })
        : t("settings.statuses.dnd.cancelled", { name: nameOf(active.id) }),
    onDragCancel: ({ active }) =>
      t("settings.statuses.dnd.cancelled", { name: nameOf(active.id) }),
  };

  const onDragStart = (_event: DragStartEvent) => setDrag(serverOrder);

  // 跨分类时在拖拽过程中就把条目移到目标分类，落点在悬停项之前
  const onDragOver = ({ active, over }: DragOverEvent) => {
    const current = dragOrderRef.current;
    if (!over || !current) return;
    const activeId = String(active.id);
    const overId = String(over.id);
    const from = findCategory(current, activeId);
    const to = findCategory(current, overId);
    if (!from || !to || from === to) return;
    const target = current[to].filter((id) => id !== activeId);
    const overIndex = target.indexOf(overId);
    const index = overIndex === -1 ? target.length : overIndex;
    setDrag({
      ...current,
      [from]: current[from].filter((id) => id !== activeId),
      [to]: [...target.slice(0, index), activeId, ...target.slice(index)],
    });
  };

  const onDragEnd = ({ active, over }: DragEndEvent) => {
    const current = dragOrderRef.current;
    setDrag(null);
    if (!over || !current) return;
    const activeId = String(active.id);
    const overId = String(over.id);
    const category = findCategory(current, activeId);
    if (!category) return;
    let final = current;
    const list = current[category];
    const from = list.indexOf(activeId);
    const to = list.indexOf(overId);
    if (to !== -1 && from !== to)
      final = { ...current, [category]: arrayMove(list, from, to) };
    const drop = resolveDrop(serverOrder, final, activeId);
    const status = byId.get(activeId);
    if (drop && status) commitMove(status, drop.category, drop.beforeId);
  };

  return (
    <div className="mx-auto flex max-w-[1004px] flex-col gap-6 px-12 pt-10 pb-16">
      <div className="flex flex-col gap-4">
        <ConfigPageHeader
          title={t("settings.statuses.heading")}
          subtitle={t("settings.statuses.subtitle")}
        />
        <div className="rounded-xl border border-line-default bg-surface-card px-4 py-3 text-body text-fg-secondary">
          {t("settings.statuses.intro")}
        </div>
      </div>
      {statusesQuery.isLoading ? (
        <ListSkeleton label={t("settings.statuses.loading")} />
      ) : statusesQuery.isError ? (
        <EmptyState
          title={t("settings.statuses.loadFailed")}
          action={
            <Button
              variant="outline"
              onClick={() => void statusesQuery.refetch()}
            >
              {t("common.retry")}
            </Button>
          }
        />
      ) : (
        <DndContext
          sensors={sensors}
          collisionDetection={pointerFirstCollision}
          accessibility={{
            announcements,
            screenReaderInstructions: {
              draggable: t("settings.statuses.dnd.instructions"),
            },
          }}
          onDragStart={onDragStart}
          onDragOver={onDragOver}
          onDragEnd={onDragEnd}
          onDragCancel={() => setDrag(null)}
        >
          {STATUS_PAGE_CATEGORIES.map((category) => (
            <CategorySection
              key={category}
              category={category}
              ids={order[category]}
              byId={byId}
              canManage={canManage}
              onAdd={() => setEditor({ mode: "create", category })}
              onEdit={(status) => setEditor({ mode: "edit", status })}
              onSetDefault={(status) => setDefault.mutate(status)}
              onRemove={requestRemove}
            />
          ))}
        </DndContext>
      )}
      {editor && (
        <StatusEditorDialog
          orgId={orgId}
          status={editor.mode === "edit" ? editor.status : undefined}
          initialCategory={
            editor.mode === "create"
              ? editor.category
              : (editor.status.category as PageCategory)
          }
          onClose={() => setEditor(null)}
          onSaved={(message) => {
            setEditor(null);
            if (message) toast(message);
            void queryClient.invalidateQueries({ queryKey: listKey });
          }}
        />
      )}
      {deleting && (
        <DeleteStatusDialog
          status={deleting}
          statuses={statuses}
          busy={remove.isPending}
          onCancel={() => setDeleting(null)}
          onConfirm={(replacementId) =>
            remove.mutate({ status: deleting, replacementId })
          }
        />
      )}
    </div>
  );
}

/** 一个分类：标题 + 副标题 + 「添加状态」+ 状态卡（整张卡是落点）。 */
function CategorySection({
  category,
  ids,
  byId,
  canManage,
  onAdd,
  onEdit,
  onSetDefault,
  onRemove,
}: {
  category: PageCategory;
  ids: string[];
  byId: Map<string, TaskStatus>;
  canManage: boolean;
  onAdd: () => void;
  onEdit: (status: TaskStatus) => void;
  onSetDefault: (status: TaskStatus) => void;
  onRemove: (status: TaskStatus) => void;
}) {
  const t = useT();
  const { setNodeRef, isOver } = useDroppable({
    id: category,
    disabled: !canManage,
  });
  const headingId = `status-category-${category}`;
  const items = ids.flatMap((id) => {
    const status = byId.get(id);
    return status ? [status] : [];
  });
  return (
    <section aria-labelledby={headingId} className="flex flex-col gap-3">
      <div className="flex items-end gap-3">
        <div className="flex min-w-0 flex-1 flex-col gap-0.5">
          <h2 id={headingId} className="text-title font-medium">
            {t(`settings.statuses.categories.${category}.name`)}
          </h2>
          <p className="text-body-sm text-fg-muted">
            {t(`settings.statuses.categories.${category}.desc`)}
          </p>
        </div>
        <Button
          variant="outline"
          size="28"
          leading={<Icon name="plus" />}
          disabled={!canManage}
          onClick={onAdd}
          aria-label={t("settings.statuses.addTo", {
            category: t(`settings.statuses.categories.${category}.name`),
          })}
        >
          {t("settings.statuses.add")}
        </Button>
      </div>
      <SortableContext
        id={category}
        items={ids}
        strategy={verticalListSortingStrategy}
        disabled={!canManage}
      >
        <ul
          ref={setNodeRef}
          aria-labelledby={headingId}
          className={cn(
            "flex min-h-14 flex-col rounded-xl border border-line-default bg-surface-card",
            isOver && "border-line-focus",
          )}
        >
          {items.length === 0 && (
            <li className="flex h-14 items-center px-4 text-body-sm text-fg-muted">
              {t("settings.statuses.emptyCategory")}
            </li>
          )}
          {items.map((status) => (
            <StatusRow
              key={status.id}
              status={status}
              alone={items.length === 1}
              canManage={canManage}
              onEdit={() => onEdit(status)}
              onSetDefault={() => onSetDefault(status)}
              onRemove={() => onRemove(status)}
            />
          ))}
        </ul>
      </SortableContext>
    </section>
  );
}

function StatusRow({
  status,
  alone,
  canManage,
  onEdit,
  onSetDefault,
  onRemove,
}: {
  status: TaskStatus;
  /** 分类内唯一的状态 */
  alone: boolean;
  canManage: boolean;
  onEdit: () => void;
  onSetDefault: () => void;
  onRemove: () => void;
}) {
  const t = useT();
  const {
    attributes,
    listeners,
    setNodeRef,
    setActivatorNodeRef,
    transform,
    transition,
    isDragging,
  } = useSortable({ id: status.id, disabled: !canManage });
  const typeCount = status.type_usage.reduce(
    (sum, usage) => sum + usage.type_ids.length,
    0,
  );
  const removeReason = status.is_default
    ? t("settings.statuses.removeDisabledDefault")
    : alone
      ? t("settings.statuses.removeDisabledAlone")
      : undefined;
  const canBeDefault =
    !status.is_default && DEFAULT_ALLOWED.has(status.category);
  return (
    <li
      ref={setNodeRef}
      style={{
        transform: DndCSS.Translate.toString(transform),
        transition,
      }}
      className={cn(
        "flex h-14 items-center gap-3 border-b border-line-soft bg-surface-card px-3 last:border-b-0 first:rounded-t-xl last:rounded-b-xl",
        isDragging && "relative z-10 shadow-popover",
      )}
    >
      {canManage ? (
        <button
          ref={setActivatorNodeRef}
          type="button"
          aria-label={t("settings.statuses.dragHandle", { name: status.name })}
          className="flex size-6 cursor-grab items-center justify-center rounded-md text-fg-subtle hover:bg-surface-muted active:cursor-grabbing"
          {...attributes}
          {...listeners}
        >
          <Icon name="grip-vertical" />
        </button>
      ) : (
        <span className="w-6" />
      )}
      <span className="flex size-7 items-center justify-center rounded-lg bg-surface-muted">
        <StatusIcon category={status.category} color={status.color} />
      </span>
      <div className="flex min-w-0 flex-1 flex-col">
        <span className="flex items-center gap-1 text-body font-medium">
          <span className="truncate">{status.name}</span>
          {status.is_default && (
            <span className="text-caption font-normal text-fg-muted">
              · {t("settings.statuses.default")}
            </span>
          )}
        </span>
        <span className="flex items-center gap-1 text-caption text-fg-muted">
          <span>
            {t("settings.statuses.taskCount", { count: status.task_count })}
          </span>
          <span aria-hidden>·</span>
          <Tooltip
            content={
              status.type_usage.length === 0
                ? t("settings.statuses.noTypes")
                : status.type_usage.map((usage) => (
                    <span key={usage.space_id} className="block">
                      {t("settings.statuses.typeUsage", {
                        space: usage.space_name,
                        types: usage.type_names.join("、"),
                      })}
                    </span>
                  ))
            }
          >
            <span tabIndex={0} className="cursor-default rounded-sm">
              {t("settings.statuses.typeCount", { count: typeCount })}
            </span>
          </Tooltip>
        </span>
      </div>
      {canManage && (
        <Menu
          label={t("settings.statuses.menu.label", { name: status.name })}
          placement="bottom-end"
          trigger={({ ref, open, toggle }) => (
            <IconButton
              ref={ref}
              label={t("settings.statuses.menu.label", { name: status.name })}
              icon={<Icon name="ellipsis" />}
              size="28"
              active={open}
              aria-haspopup="menu"
              aria-expanded={open}
              onClick={toggle}
            />
          )}
        >
          {canBeDefault && (
            <MenuItem icon={<Icon name="star" />} onSelect={onSetDefault}>
              {t("settings.statuses.menu.setDefault")}
            </MenuItem>
          )}
          <MenuItem icon={<Icon name="pencil" />} onSelect={onEdit}>
            {t("settings.statuses.menu.edit")}
          </MenuItem>
          <MenuSeparator />
          <MenuItem
            icon={<Icon name="trash-2" />}
            danger
            disabled={Boolean(removeReason)}
            disabledReason={removeReason}
            onSelect={onRemove}
          >
            {t("settings.statuses.menu.remove")}
          </MenuItem>
        </Menu>
      )}
    </li>
  );
}
