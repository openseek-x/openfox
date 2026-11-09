import {
  DndContext,
  KeyboardSensor,
  PointerSensor,
  closestCenter,
  useSensor,
  useSensors,
  type DragEndEvent,
} from "@dnd-kit/core";
import {
  SortableContext,
  sortableKeyboardCoordinates,
  useSortable,
  verticalListSortingStrategy,
} from "@dnd-kit/sortable";
import { CSS as DndCSS } from "@dnd-kit/utilities";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useNavigate, useParams } from "react-router-dom";

import type { SpaceDetail } from "@openfox/api";
import { useI18n, useT } from "@openfox/i18n";
import {
  Button,
  Icon,
  IconButton,
  Menu,
  MenuItem,
  Select,
  TextInput,
  cn,
  useToast,
} from "@openfox/ui";

import { keys, useApi } from "../../context";
import {
  isOrgAdmin,
  putSpace,
  spaceErrorMessage,
  useOrgRole,
} from "../queries";
import { SpaceIcon } from "../SpaceIcon";
import {
  fullOrder,
  reorderIds,
  spaceSettingsPath,
  useOpenCreateSpace,
  useReorderSpaces,
} from "../SpacesNav";
import { DeleteDialog, moveTargets, RetireDialog } from "./DangerZone";

type Filter = "active" | "retired";

// 列：拖拽柄 20 / 名称 / 任务 96 / 成员 96 / 创建时间 112 / 操作 32
const GRID =
  "grid grid-cols-[20px_minmax(0,1fr)_96px_96px_112px_32px] items-center gap-4";

/**
 * 管理 → 空间：活跃 / 已停用空间列表，是已停用空间的唯一入口。
 * 组织所有者 / 管理员在「活跃」且没有搜索时可以拖拽调整顺序（与侧栏同一个顺序）。
 */
export function SpacesListPage() {
  const t = useT();
  const api = useApi();
  const { orgId = "" } = useParams();
  const orgRole = useOrgRole(orgId);
  const openCreate = useOpenCreateSpace();
  const reorder = useReorderSpaces(orgId);
  const [filter, setFilter] = useState<Filter>("active");
  const [query, setQuery] = useState("");
  const active = useQuery({
    queryKey: keys.spaces(orgId),
    queryFn: () => api.spaces.list(orgId),
  });
  const retired = useQuery({
    queryKey: keys.spacesByStatus(orgId, "retired"),
    queryFn: () => api.spaces.list(orgId, "retired"),
    enabled: filter === "retired",
  });
  const source = filter === "active" ? active : retired;
  const q = query.trim().toLowerCase();
  const rows = (source.data ?? []).filter(
    (space) =>
      !q ||
      space.name.toLowerCase().includes(q) ||
      space.prefix.toLowerCase().includes(q),
  );
  const draggable = isOrgAdmin(orgRole) && filter === "active" && !q;
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 5 } }),
    useSensor(KeyboardSensor, {
      coordinateGetter: sortableKeyboardCoordinates,
    }),
  );
  const onDragEnd = (event: DragEndEvent) => {
    const ids = rows.map((space) => space.id);
    const next = reorderIds(
      ids,
      String(event.active.id),
      event.over ? String(event.over.id) : null,
    );
    if (next && active.data) reorder.mutate(fullOrder(active.data, next));
  };

  return (
    <div className="mx-auto flex w-full max-w-[1100px] flex-col gap-5 px-6 pt-12 pb-12">
      <header className="flex items-center gap-4">
        <h2 className="flex-1 text-[20px] leading-7 font-semibold text-fg-default">
          {t("spaces.list.title")}
        </h2>
        {isOrgAdmin(orgRole) && (
          <Button size="32" leading={<Icon name="plus" />} onClick={openCreate}>
            {t("spaces.nav.create")}
          </Button>
        )}
      </header>
      <div className="flex items-center gap-2">
        <TextInput
          aria-label={t("spaces.list.filterPlaceholder")}
          placeholder={t("spaces.list.filterPlaceholder")}
          leading={<Icon name="search" size={14} />}
          className="!w-56"
          value={query}
          onChange={(event) => setQuery(event.target.value)}
        />
        <Select
          aria-label={t("spaces.list.filterLabel")}
          value={filter}
          className="!w-[120px]"
          onChange={(value) => setFilter(value as Filter)}
          options={[
            { value: "active", label: t("spaces.list.active") },
            { value: "retired", label: t("spaces.list.retired") },
          ]}
        />
      </div>
      {!source.isFetched ? null : rows.length === 0 ? (
        <p className="py-24 text-center text-body text-fg-muted">
          {t(
            filter === "active"
              ? "spaces.list.emptyActive"
              : "spaces.list.emptyRetired",
          )}
        </p>
      ) : (
        <div role="table" aria-label={t("spaces.list.title")}>
          <div
            role="row"
            className={cn(
              GRID,
              "h-10 rounded-lg bg-surface-muted/50 px-4 text-caption text-fg-muted",
            )}
          >
            <span />
            <span role="columnheader" className="pl-10">
              {t("spaces.list.name")}
            </span>
            <span role="columnheader">{t("spaces.list.tasks")}</span>
            <span role="columnheader">{t("spaces.list.members")}</span>
            <span role="columnheader">{t("spaces.list.createdAt")}</span>
            <span role="columnheader" className="sr-only">
              {t("spaces.list.actions")}
            </span>
          </div>
          <DndContext
            sensors={sensors}
            collisionDetection={closestCenter}
            onDragEnd={onDragEnd}
          >
            <SortableContext
              items={rows.map((space) => space.id)}
              strategy={verticalListSortingStrategy}
            >
              {rows.map((space) => (
                <SpaceRow
                  key={space.id}
                  orgId={orgId}
                  space={space}
                  draggable={draggable}
                  activeSpaces={active.data ?? []}
                />
              ))}
            </SortableContext>
          </DndContext>
        </div>
      )}
    </div>
  );
}

function SpaceRow({
  orgId,
  space,
  draggable,
  activeSpaces,
}: {
  orgId: string;
  space: SpaceDetail;
  draggable: boolean;
  activeSpaces: SpaceDetail[];
}) {
  const t = useT();
  const { formatDate } = useI18n();
  const navigate = useNavigate();
  const sortable = useSortable({ id: space.id, disabled: !draggable });
  const isRetired = space.status === "retired";
  const open = () => navigate(spaceSettingsPath(orgId, space.id));
  return (
    <div
      ref={sortable.setNodeRef}
      role="row"
      tabIndex={0}
      style={{
        transform: DndCSS.Translate.toString(sortable.transform),
        transition: sortable.transition,
      }}
      onClick={(event) => {
        if (!(event.target as HTMLElement).closest("button,[role=menu]"))
          open();
      }}
      onKeyDown={(event) => {
        if (event.key === "Enter" && event.target === event.currentTarget)
          open();
      }}
      className={cn(
        GRID,
        "group min-h-[61px] cursor-pointer rounded-lg p-4 hover:bg-surface-hover",
        sortable.isDragging &&
          "relative z-10 bg-accent-soft opacity-90 shadow-card",
      )}
    >
      <span role="cell">
        {draggable && (
          <button
            type="button"
            aria-label={t("spaces.list.dragHandle", { name: space.name })}
            {...sortable.attributes}
            {...sortable.listeners}
            className="flex cursor-grab text-fg-muted opacity-40 group-hover:opacity-70 active:cursor-grabbing"
          >
            <Icon name="grip-vertical" size={16} />
          </button>
        )}
      </span>
      <span role="cell" className="flex min-w-0 items-center gap-2">
        <span className="flex size-7 shrink-0 items-center justify-center">
          <SpaceIcon icon={space.icon} size={20} />
        </span>
        <span
          className={cn(
            "truncate text-body font-medium",
            isRetired ? "text-fg-muted italic" : "text-fg-default",
          )}
        >
          {space.name}
        </span>
        {space.visibility === "private" && (
          <Icon name="lock" size={14} className="shrink-0 text-fg-muted/60" />
        )}
        <span className="shrink-0 rounded bg-surface-muted px-1.5 font-mono text-caption text-fg-secondary">
          {space.prefix.toUpperCase()}
        </span>
      </span>
      <span role="cell" className="text-body-sm text-fg-muted">
        {space.task_count}
      </span>
      <span role="cell" className="text-body-sm text-fg-muted">
        {space.member_count}
      </span>
      <span role="cell" className="text-body-sm text-fg-muted">
        {formatDate(space.created_at, {
          year: "numeric",
          month: "short",
          day: "numeric",
        })}
      </span>
      <span role="cell">
        <SpaceRowMenu orgId={orgId} space={space} activeSpaces={activeSpaces} />
      </span>
    </div>
  );
}

/** 行菜单：查看任务 / 设置 / 停用或恢复 / 删除（只有已停用空间可删除；最后一个活跃空间不能停用）。 */
function SpaceRowMenu({
  orgId,
  space,
  activeSpaces,
}: {
  orgId: string;
  space: SpaceDetail;
  activeSpaces: SpaceDetail[];
}) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [dialog, setDialog] = useState<"retire" | "delete" | null>(null);
  const isRetired = space.status === "retired";
  const lastActive = !isRetired && activeSpaces.length <= 1;
  const restore = useMutation({
    mutationFn: () =>
      api.spaces.restore(orgId, space.id, { version: space.version }),
    onSuccess: (updated) => {
      putSpace(queryClient, orgId, updated);
      void queryClient.invalidateQueries({ queryKey: keys.spaces(orgId) });
      void queryClient.invalidateQueries({ queryKey: keys.tasks(orgId) });
      toast(t("spaces.danger.restored", { name: space.name }));
    },
    onError: (error) =>
      toast(
        spaceErrorMessage(t, error, "spaces.danger.restoreFailed", {
          name: space.name,
        }),
        { tone: "error" },
      ),
  });
  return (
    <>
      <Menu
        label={t("spaces.list.actions")}
        placement="bottom-end"
        className="w-44 min-w-0"
        trigger={({ ref, open, toggle }) => (
          <IconButton
            ref={ref}
            label={t("spaces.list.actions")}
            icon={<Icon name="ellipsis" />}
            size="28"
            active={open}
            aria-haspopup="menu"
            aria-expanded={open}
            onClick={toggle}
          />
        )}
      >
        {!isRetired && (
          <MenuItem
            icon={<Icon name="list-checks" />}
            onSelect={() => navigate(`/o/${orgId}/spaces/${space.id}/tasks`)}
          >
            {t("spaces.list.viewTasks")}
          </MenuItem>
        )}
        <MenuItem
          icon={<Icon name="settings-2" />}
          onSelect={() => navigate(spaceSettingsPath(orgId, space.id))}
        >
          {t("spaces.list.settings")}
        </MenuItem>
        {space.can_manage &&
          (isRetired ? (
            <MenuItem
              icon={<Icon name="archive-restore" />}
              disabled={restore.isPending}
              onSelect={() => restore.mutate()}
            >
              {t("spaces.danger.restoreAction")}
            </MenuItem>
          ) : (
            <MenuItem
              icon={<Icon name="archive" />}
              disabled={lastActive}
              disabledReason={t("spaces.danger.retireLastTooltip")}
              onSelect={() => setDialog("retire")}
            >
              {t("spaces.danger.retireAction")}
            </MenuItem>
          ))}
        {space.can_manage && (
          <MenuItem
            icon={<Icon name="trash-2" />}
            danger
            disabled={!isRetired}
            disabledReason={
              lastActive ? t("spaces.danger.deleteLastTooltip") : undefined
            }
            onSelect={() => setDialog("delete")}
          >
            {t("spaces.danger.deleteAction")}
          </MenuItem>
        )}
      </Menu>
      {dialog === "retire" && (
        <RetireDialog
          orgId={orgId}
          space={space}
          targets={moveTargets(activeSpaces, space.id)}
          onClose={() => setDialog(null)}
        />
      )}
      {dialog === "delete" && (
        <DeleteDialog
          orgId={orgId}
          space={space}
          onClose={() => setDialog(null)}
        />
      )}
    </>
  );
}
