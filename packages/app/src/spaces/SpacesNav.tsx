import {
  DndContext,
  DragOverlay,
  KeyboardSensor,
  PointerSensor,
  useSensor,
  useSensors,
  type DragEndEvent,
  type DragStartEvent,
} from "@dnd-kit/core";
import {
  SortableContext,
  arrayMove,
  sortableKeyboardCoordinates,
  useSortable,
  verticalListSortingStrategy,
} from "@dnd-kit/sortable";
import { CSS as DndCSS } from "@dnd-kit/utilities";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useRef, useState } from "react";
import {
  NavLink,
  useLocation,
  useNavigate,
  useSearchParams,
} from "react-router-dom";

import type { SpaceDetail } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Icon, Menu, MenuItem, MenuSeparator, cn, useToast } from "@openfox/ui";

import { keys, useApi } from "../context";
import { pointerFirstCollision } from "../dnd/collision";
import {
  isOrgAdmin,
  joinableSpaces,
  joinedSpaces,
  spaceErrorMessage,
  useOrgRole,
  useSpaceList,
} from "./queries";
import { SpaceIcon } from "./SpaceIcon";

export const spaceSettingsPath = (
  orgId: string,
  spaceId: string,
  section = "general",
) => `/o/${orgId}/settings/spaces/${spaceId}/${section}`;

/** 把 activeId 拖到 overId 的位置，返回新的 id 顺序（未变化时返回 null）。 */
export function reorderIds(
  ids: readonly string[],
  activeId: string,
  overId: string | null,
): string[] | null {
  if (!overId || activeId === overId) return null;
  const from = ids.indexOf(activeId);
  const to = ids.indexOf(overId);
  if (from < 0 || to < 0) return null;
  return arrayMove([...ids], from, to);
}

/** 侧栏只显示已加入的空间；提交时把未加入的空间按原顺序接在后面，得到当前用户的完整顺序。 */
export function fullOrder(
  all: readonly SpaceDetail[],
  joinedOrder: readonly string[],
): string[] {
  const joined = new Set(joinedOrder);
  return [
    ...joinedOrder,
    ...all.filter((s) => !joined.has(s.id)).map((s) => s.id),
  ];
}

/** 重排空间：乐观更新列表缓存，失败回滚并提示。 */
export function useReorderSpaces(orgId: string) {
  const api = useApi();
  const queryClient = useQueryClient();
  const toast = useToast();
  const t = useT();
  return useMutation({
    scope: { id: `space-order:${orgId}` },
    mutationFn: (ids: string[]) => api.spaces.reorder(orgId, ids),
    onMutate: async (ids: string[]) => {
      await queryClient.cancelQueries({
        queryKey: keys.spaces(orgId),
        exact: true,
      });
      const previous = queryClient.getQueryData<SpaceDetail[]>(
        keys.spaces(orgId),
      );
      if (previous) {
        const byId = new Map(previous.map((space) => [space.id, space]));
        queryClient.setQueryData<SpaceDetail[]>(
          keys.spaces(orgId),
          ids.flatMap((id, position) => {
            const space = byId.get(id);
            return space ? [{ ...space, position }] : [];
          }),
        );
      }
      return { previous };
    },
    onError: (error, _ids, context) => {
      if (context?.previous)
        queryClient.setQueryData(keys.spaces(orgId), context.previous);
      toast(spaceErrorMessage(t, error, "spaces.nav.reorderFailed"), {
        tone: "error",
      });
    },
    onSettled: () =>
      void queryClient.invalidateQueries({
        queryKey: keys.spaces(orgId),
        exact: true,
      }),
  });
}

/** 打开新建空间向导（由 OrgLayout 里的 CreateSpaceHost 根据 ?createSpace=1 渲染）。 */
export function useOpenCreateSpace() {
  const [params, setParams] = useSearchParams();
  return () => {
    const next = new URLSearchParams(params);
    next.set("createSpace", "1");
    setParams(next);
  };
}

/** 加入公开空间：以 MEMBER 身份把自己加进去。 */
export function useJoinSpace(orgId: string) {
  const api = useApi();
  const queryClient = useQueryClient();
  const toast = useToast();
  const t = useT();
  const navigate = useNavigate();
  const me = useQuery({ queryKey: keys.me, queryFn: api.me.get });
  return useMutation({
    mutationFn: (space: SpaceDetail) =>
      api.spaceMembership.put(orgId, space.id, me.data!.id, "MEMBER"),
    onSuccess: async (_member, space) => {
      toast(t("spaces.nav.joined", { name: space.name }));
      await queryClient.invalidateQueries({ queryKey: keys.spaces(orgId) });
      void queryClient.invalidateQueries({
        queryKey: keys.spaceMembers(orgId, space.id),
      });
      navigate(spaceSettingsPath(orgId, space.id));
    },
    onError: () => toast(t("spaces.nav.joinFailed"), { tone: "error" }),
  });
}

/**
 * 「创建空间」/「加入或创建空间」：
 * 有可加入的公开空间时展开 224 宽菜单，否则直接打开新建向导。
 */
export function JoinOrCreateSpace({
  orgId,
  variant,
}: {
  orgId: string;
  /** row：设置导航里的文字行；icon：主导航分组标题右侧的「+」 */
  variant: "row" | "icon";
}) {
  const t = useT();
  const spaces = useSpaceList(orgId);
  const openCreate = useOpenCreateSpace();
  const join = useJoinSpace(orgId);
  const joinable = joinableSpaces(spaces.data);
  // 新建空间仅组织 OWNER / ADMIN；其他人只能加入公开空间
  const canCreate = isOrgAdmin(useOrgRole(orgId));
  const label =
    joinable.length > 0 ? t("spaces.nav.joinOrCreate") : t("spaces.nav.create");
  const rowClass =
    "flex h-[30px] w-full items-center gap-2 rounded-lg px-2 text-body text-fg-default hover:bg-surface-hover";
  const iconClass =
    "flex size-5 items-center justify-center rounded-md text-fg-muted hover:bg-surface-muted hover:text-fg-default";
  if (joinable.length === 0 && !canCreate) return null;
  if (joinable.length === 0)
    return variant === "row" ? (
      <button type="button" className={rowClass} onClick={openCreate}>
        <Icon name="plus" className="text-fg-secondary" />
        <span className="truncate">{label}</span>
      </button>
    ) : (
      <button
        type="button"
        aria-label={t("spaces.nav.joinOrCreate")}
        title={t("spaces.nav.joinOrCreate")}
        className={iconClass}
        onClick={openCreate}
      >
        <Icon name="plus" size={14} />
      </button>
    );
  return (
    <Menu
      label={t("spaces.nav.joinOrCreate")}
      className="w-[224px]"
      trigger={({ ref, toggle, open }) => (
        <button
          ref={ref}
          type="button"
          aria-expanded={open}
          aria-label={variant === "icon" ? label : undefined}
          title={variant === "icon" ? label : undefined}
          onClick={toggle}
          className={variant === "row" ? rowClass : iconClass}
        >
          <Icon
            name="plus"
            size={variant === "row" ? 16 : 14}
            className={variant === "row" ? "text-fg-secondary" : undefined}
          />
          {variant === "row" && <span className="truncate">{label}</span>}
        </button>
      )}
    >
      {canCreate && (
        <>
          <MenuItem icon={<Icon name="plus" />} onSelect={openCreate}>
            {t("spaces.nav.createNew")}
          </MenuItem>
          <MenuSeparator />
        </>
      )}
      {joinable.map((space) => (
        <MenuItem
          key={space.id}
          icon={<SpaceIcon icon={space.icon} size={14} />}
          disabled={join.isPending}
          onSelect={() => join.mutate(space)}
        >
          {space.name}
        </MenuItem>
      ))}
    </Menu>
  );
}

/**
 * 设置导航的「我的空间」分组：已加入的空间按个人顺序排列，整行即拖拽把手；
 * 当前空间展开二级页。点击行进入空间设置概览。
 */
export function MySpacesNavGroup({ orgId }: { orgId: string }) {
  const t = useT();
  const spaces = useSpaceList(orgId);
  const reorder = useReorderSpaces(orgId);
  const [dragging, setDragging] = useState<string | null>(null);
  // 拖拽结束后紧接着的那次 click 不跳转
  const justDragged = useRef(false);
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 5 } }),
    useSensor(KeyboardSensor, {
      coordinateGetter: sortableKeyboardCoordinates,
    }),
  );
  const joined = joinedSpaces(spaces.data);
  const ids = joined.map((space) => space.id);
  const draggingSpace = joined.find((space) => space.id === dragging);

  const onDragStart = (event: DragStartEvent) => {
    setDragging(String(event.active.id));
    justDragged.current = true;
  };
  const onDragEnd = (event: DragEndEvent) => {
    setDragging(null);
    window.setTimeout(() => (justDragged.current = false), 0);
    const next = reorderIds(
      ids,
      String(event.active.id),
      event.over ? String(event.over.id) : null,
    );
    if (next && spaces.data) reorder.mutate(fullOrder(spaces.data, next));
  };

  return (
    <div className="flex flex-col gap-0.5">
      <div className="px-2.5 pb-1 text-caption text-fg-muted">
        {t("spaces.nav.group")}
      </div>
      <DndContext
        sensors={sensors}
        collisionDetection={pointerFirstCollision}
        accessibility={{
          screenReaderInstructions: { draggable: t("spaces.nav.dragHint") },
        }}
        onDragStart={onDragStart}
        onDragEnd={onDragEnd}
        onDragCancel={() => {
          setDragging(null);
          justDragged.current = false;
        }}
      >
        <SortableContext items={ids} strategy={verticalListSortingStrategy}>
          <ul
            className="flex flex-col gap-0.5"
            aria-label={t("spaces.nav.group")}
          >
            {joined.map((space) => (
              <SpaceNavRow
                key={space.id}
                orgId={orgId}
                space={space}
                justDragged={justDragged}
              />
            ))}
          </ul>
        </SortableContext>
        <DragOverlay>
          {draggingSpace && (
            <div className="flex h-[30px] items-center gap-2 rounded-lg bg-surface-card px-2 text-body shadow-popover">
              <SpaceIcon icon={draggingSpace.icon} />
              <span className="truncate">{draggingSpace.name}</span>
            </div>
          )}
        </DragOverlay>
      </DndContext>
      <JoinOrCreateSpace orgId={orgId} variant="row" />
    </div>
  );
}

function SpaceNavRow({
  orgId,
  space,
  justDragged,
}: {
  orgId: string;
  space: SpaceDetail;
  justDragged: React.MutableRefObject<boolean>;
}) {
  const t = useT();
  const location = useLocation();
  const {
    attributes,
    listeners,
    setNodeRef,
    transform,
    transition,
    isDragging,
  } = useSortable({ id: space.id });
  const base = `/o/${orgId}/settings/spaces/${space.id}`;
  // 设置侧栏里空间没有二级页：该空间的任何设置页都高亮这一行
  const active =
    location.pathname === base || location.pathname.startsWith(`${base}/`);
  return (
    <li
      ref={setNodeRef}
      style={{ transform: DndCSS.Translate.toString(transform), transition }}
      className={cn(isDragging && "opacity-50")}
    >
      <NavLink
        to={spaceSettingsPath(orgId, space.id)}
        onClick={(event) => {
          if (justDragged.current) event.preventDefault();
        }}
        className={cn(
          "flex h-[30px] cursor-grab items-center gap-2 rounded-lg px-2 text-body hover:bg-surface-hover active:cursor-grabbing",
          active ? "bg-surface-active font-medium" : "text-fg-default",
        )}
        // dnd-kit 默认把拖拽源标成 role=button；这里保留链接语义，只沿用其余无障碍属性
        {...{ ...attributes, role: undefined }}
        {...listeners}
        aria-label={space.name}
      >
        <SpaceIcon icon={space.icon} />
        <span className="min-w-0 flex-1 truncate">{space.name}</span>
        {space.visibility === "private" && (
          <Icon
            name="lock"
            size={12}
            className="text-fg-muted"
            label={t("spaces.access.private")}
          />
        )}
      </NavLink>
    </li>
  );
}
