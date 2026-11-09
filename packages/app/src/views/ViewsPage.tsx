import {
  DndContext,
  KeyboardSensor,
  PointerSensor,
  useSensor,
  useSensors,
  type Announcements,
  type DragEndEvent,
} from "@dnd-kit/core";
import {
  SortableContext,
  arrayMove,
  sortableKeyboardCoordinates,
  useSortable,
  verticalListSortingStrategy,
} from "@dnd-kit/sortable";
import { CSS as DndCSS } from "@dnd-kit/utilities";
import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";

import type { View } from "@openfox/api";
import { useI18n, useT } from "@openfox/i18n";
import {
  Button,
  ConfirmDialog,
  Icon,
  Menu,
  MenuItem,
  Spinner,
  cn,
  useToast,
} from "@openfox/ui";

import { pointerFirstCollision } from "../dnd/collision";
import { keys, useApi } from "../context";
import { canWriteSpace, useSpaceDetail } from "../spaces/queries";
import { RetiredBanner } from "../spaces/settings/parts";
import { queryToConditions, type FilterQuery } from "../tasks/model";
import { formatMonthDay } from "../tasks/primitives/dates";
import { TaskState } from "../tasks/TaskWorkspace";
import { ViewIcon } from "./ViewIcon";
import { useViewMutation, useViews } from "./queries";

/** 视图列表 /o/:org/spaces/:space/views。 */
export function ViewsPage() {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const queryClient = useQueryClient();
  const { orgId = "", spaceId = "" } = useParams();
  const space = useSpaceDetail(orgId, spaceId);
  const enabled = Boolean(space.data?.features.views);
  const list = useViews(orgId, spaceId, enabled);
  const navigate = useNavigate();
  const [deleting, setDeleting] = useState<View | null>(null);
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 4 } }),
    useSensor(KeyboardSensor, {
      coordinateGetter: sortableKeyboardCoordinates,
    }),
  );
  const reorder = useViewMutation(orgId, (ids: string[]) =>
    api.views.reorderMine(orgId, spaceId, ids),
  );
  const remove = useViewMutation(orgId, (view: View) =>
    api.views.remove(orgId, view.id, view.version),
  );

  if (space.isLoading || (enabled && list.isLoading)) {
    return (
      <TaskState busy icon={<Spinner />} title={t("tasks.empty.loading")} />
    );
  }
  if (!space.data) return <TaskState title={t("spaces.loadFailed")} />;
  if (!enabled) {
    return (
      <TaskState
        title={t("views.disabledTitle")}
        desc={t("views.disabledDesc")}
        actions={
          space.data.can_manage ? (
            <Link
              className="text-body text-accent hover:underline"
              to={`/o/${orgId}/settings/spaces/${spaceId}`}
            >
              {t("views.enable")}
            </Link>
          ) : undefined
        }
      />
    );
  }
  const canWrite = canWriteSpace(space.data);
  const listPath = `/o/${orgId}/spaces/${spaceId}/views`;
  const views = list.data ?? [];
  const groups = [
    {
      key: "personal",
      views: views.filter((v) => v.visibility === "personal"),
    },
    { key: "space", views: views.filter((v) => v.visibility === "space") },
  ] as const;

  const nameOf = (id: string | number) =>
    views.find((v) => v.id === id)?.name ?? "";
  const announcements: Announcements = {
    onDragStart: ({ active }) =>
      t("views.dnd.picked", { name: nameOf(active.id) }),
    onDragOver: () => "",
    onDragEnd: ({ active }) =>
      t("views.dnd.dropped", { name: nameOf(active.id) }),
    onDragCancel: ({ active }) =>
      t("views.dnd.cancelled", { name: nameOf(active.id) }),
  };

  const onDragEnd =
    (group: (typeof groups)[number]) => (event: DragEndEvent) => {
      const ids = group.views.map((v) => v.id);
      const from = ids.indexOf(String(event.active.id));
      const to = ids.indexOf(String(event.over?.id));
      if (from < 0 || to < 0 || from === to) return;
      const moved = arrayMove(ids, from, to);
      const others = groups
        .filter((g) => g.key !== group.key)
        .flatMap((g) => g.views.map((v) => v.id));
      const all =
        group.key === "personal"
          ? [...moved, ...others]
          : [...others, ...moved];
      // 先在缓存里按新顺序排好，避免等刷新时跳回去
      queryClient.setQueryData<View[]>(
        keys.viewList(orgId, spaceId),
        (current) =>
          current && all.map((id) => current.find((v) => v.id === id)!),
      );
      reorder.mutate(all);
    };

  const create = (
    <Button
      size="28"
      disabled={!canWrite}
      leading={<Icon name="plus" />}
      onClick={() => navigate(`${listPath}/new`)}
    >
      {t("views.new")}
    </Button>
  );

  return (
    <div className="flex h-full flex-col">
      <header className="flex h-11 shrink-0 items-center gap-2 border-b border-line-default px-4">
        <h1 className="font-medium">
          {space.data.name} · {t("views.title")}
        </h1>
        <div className="ml-auto">{create}</div>
      </header>
      {space.data.status === "retired" && (
        <div className="px-4 pt-3">
          <RetiredBanner message={t("spaces.task.retiredBanner")} />
        </div>
      )}
      {views.length === 0 ? (
        <TaskState
          icon={<Icon name="layout-grid" size={28} className="text-fg-muted" />}
          title={t("views.empty.title")}
          desc={t("views.empty.desc")}
          actions={create}
        />
      ) : (
        <div className="min-h-0 flex-1 overflow-auto p-4">
          <div className="mx-auto flex max-w-[900px] flex-col gap-6">
            {groups
              .filter((group) => group.views.length > 0)
              .map((group) => (
                <section key={group.key} className="flex flex-col gap-2">
                  <h2 className="px-1 text-body-sm font-medium text-fg-muted">
                    {t(`views.visibility.${group.key}`)}
                  </h2>
                  <DndContext
                    sensors={sensors}
                    collisionDetection={pointerFirstCollision}
                    accessibility={{
                      announcements,
                      screenReaderInstructions: {
                        draggable: t("views.dnd.instructions"),
                      },
                    }}
                    onDragEnd={onDragEnd(group)}
                  >
                    <SortableContext
                      items={group.views.map((v) => v.id)}
                      strategy={verticalListSortingStrategy}
                    >
                      <ul className="flex flex-col rounded-xl border border-line-default">
                        {group.views.map((view) => (
                          <ViewRow
                            key={view.id}
                            orgId={orgId}
                            spaceId={spaceId}
                            view={view}
                            onEdit={() =>
                              navigate(`${listPath}/${view.id}/edit`)
                            }
                            onDelete={() => setDeleting(view)}
                          />
                        ))}
                      </ul>
                    </SortableContext>
                  </DndContext>
                </section>
              ))}
          </div>
        </div>
      )}
      <ConfirmDialog
        open={Boolean(deleting)}
        danger
        title={t("views.delete.title", { name: deleting?.name ?? "" })}
        description={t("views.delete.description")}
        confirmLabel={t("views.delete.confirm")}
        loading={remove.isPending}
        onCancel={() => setDeleting(null)}
        onConfirm={() =>
          deleting &&
          remove.mutate(deleting, {
            onSuccess: () => {
              toast(t("views.delete.done"));
              setDeleting(null);
            },
          })
        }
      />
    </div>
  );
}

function ViewRow({
  orgId,
  spaceId,
  view,
  onEdit,
  onDelete,
}: {
  orgId: string;
  spaceId: string;
  view: View;
  onEdit: () => void;
  onDelete: () => void;
}) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const { locale } = useI18n();
  const pin = useViewMutation(orgId, (pinned: boolean) =>
    api.views.pinMine(orgId, view.id, pinned),
  );
  const {
    attributes,
    listeners,
    setNodeRef,
    setActivatorNodeRef,
    transform,
    transition,
    isDragging,
  } = useSortable({ id: view.id });
  const filterCount = queryToConditions(view.filters as FilterQuery).length;
  const link = `${window.location.origin}/o/${orgId}/spaces/${spaceId}/views/${view.id}`;
  return (
    <li
      ref={setNodeRef}
      style={{ transform: DndCSS.Translate.toString(transform), transition }}
      className={cn(
        "flex h-14 items-center gap-3 border-b border-line-soft bg-surface-card px-3 first:rounded-t-xl last:rounded-b-xl last:border-b-0",
        isDragging && "relative z-10 shadow-popover",
      )}
    >
      <button
        ref={setActivatorNodeRef}
        type="button"
        aria-label={t("views.dragHandle", { name: view.name })}
        className="flex size-6 cursor-grab items-center justify-center rounded-md text-fg-subtle hover:bg-surface-muted active:cursor-grabbing"
        {...attributes}
        {...listeners}
      >
        <Icon name="grip-vertical" />
      </button>
      <ViewIcon view={view} />
      <Link
        to={`/o/${orgId}/spaces/${spaceId}/views/${view.id}`}
        className="flex min-w-0 flex-1 flex-col"
      >
        <span className="flex items-center gap-1 truncate font-medium">
          {view.name}
          {view.pinned && (
            <Icon
              name="pin"
              size={12}
              className="text-fg-muted"
              label={t("views.pinned")}
            />
          )}
        </span>
        <span className="truncate text-caption text-fg-muted">
          {filterCount > 0
            ? t("views.filterCount", { count: filterCount })
            : t("views.noFilters")}
          {view.description && ` · ${view.description}`}
        </span>
      </Link>
      <span className="hidden truncate text-body-sm text-fg-secondary md:block md:w-44">
        {view.created_by.email}
      </span>
      <span className="w-14 text-right text-body-sm text-fg-muted">
        {formatMonthDay(new Date(view.created_at), locale)}
      </span>
      <Menu
        label={`${view.name} ${t("views.actions")}`}
        trigger={({ ref, toggle }) => (
          <button
            ref={ref}
            type="button"
            aria-label={`${view.name} ${t("views.actions")}`}
            onClick={toggle}
            className="flex size-7 items-center justify-center rounded-md text-fg-muted hover:bg-surface-hover"
          >
            <Icon name="more" />
          </button>
        )}
      >
        <MenuItem
          icon={<Icon name="link" />}
          onSelect={() =>
            void navigator.clipboard
              ?.writeText(link)
              .then(() => toast(t("views.linkCopied")))
          }
        >
          {t("views.copyLink")}
        </MenuItem>
        <MenuItem
          icon={<Icon name="pencil" />}
          disabled={!view.can_edit}
          onSelect={onEdit}
        >
          {t("views.edit")}
        </MenuItem>
        <MenuItem
          icon={<Icon name={view.pinned ? "pin-off" : "pin"} />}
          onSelect={() => pin.mutate(!view.pinned)}
        >
          {view.pinned ? t("views.unpin") : t("views.pin")}
        </MenuItem>
        <MenuItem
          danger
          icon={<Icon name="trash-2" />}
          disabled={!view.can_edit}
          onSelect={onDelete}
        >
          {t("views.delete.action")}
        </MenuItem>
      </Menu>
    </li>
  );
}
