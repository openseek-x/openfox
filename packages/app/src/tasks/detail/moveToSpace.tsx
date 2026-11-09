import { useEffect, useRef, useState, type ReactNode } from "react";
import { useLocation, useNavigate } from "react-router-dom";

import type { SpaceDetail, Task, TaskMoveResult } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Icon, TextInput, cn, handleMenuKeyDown, useToast } from "@openfox/ui";

import { effectiveShortcut, isMacPlatform } from "../../me/shortcuts";
import { useMyPreferences } from "../../me/queries";
import { matchesShortcut } from "../../search/model";
import { canWriteTasksIn, useTaskReadOnlyReason } from "../../spaces/readonly";
import { useOrgRole, useSpaceList } from "../../spaces/queries";
import { moveTargets } from "../../spaces/settings/DangerZone";
import { useTaskParam } from "../parts";
import { MoveTaskDialog } from "./MoveTaskDialog";

// 任务「移动到空间」入口：权限判断、目标空间列表、菜单里的空间选项、快捷键。
// 详情更多菜单与看板卡片菜单只挂载这里导出的组件，逻辑都在本文件与 MoveTaskDialog.tsx。

/** 空间超过这个数时，空间列表顶部显示搜索框。 */
export const MOVE_SEARCH_THRESHOLD = 8;

/**
 * 当前用户能否在该空间写任务（与后端 policy.authorizeObject 一致）：
 * 显式角色 OWNER / EDITOR / MEMBER 可写，VIEWER 只读；没有显式角色时，公开空间对非访客（GUEST）成员可写。
 */
export { canWriteTasksIn } from "../../spaces/readonly";

/**
 * 目标空间候选：复用停用空间的 moveTargets 过滤（非自身、未停用、共享或有角色），再去掉自己只能查看的空间。
 * 当前空间放在最前，灰显不可选。
 */
export function moveTargetOptions(
  spaces: readonly SpaceDetail[],
  currentId: string,
  orgRole: string | undefined,
): { space: SpaceDetail; current: boolean }[] {
  const current = spaces.find((space) => space.id === currentId);
  const targets = moveTargets(spaces, currentId).filter((space) =>
    canWriteTasksIn(space, orgRole),
  );
  return [
    ...(current ? [{ space: current, current: true }] : []),
    ...targets.map((space) => ({ space, current: false })),
  ];
}

/** 按名称或前缀过滤（不区分大小写）。 */
export function filterSpaces<T extends { space: SpaceDetail }>(
  items: readonly T[],
  query: string,
): T[] {
  const q = query.trim().toLowerCase();
  if (!q) return [...items];
  return items.filter(
    ({ space }) =>
      space.name.toLowerCase().includes(q) ||
      space.prefix.toLowerCase().includes(q),
  );
}

/**
 * 能否移动该任务（规则与「删除任务」一致）：源空间已停用时禁用；自己在源空间只能查看（访客、VIEWER）时禁用。
 * reason 是禁用原因，用作菜单项提示。空间列表或组织角色还没加载时先放行，后端仍会兜底校验。
 */
export function useMovePermission(
  orgId: string,
  task: Pick<Task, "space_id">,
): { allowed: boolean; reason?: string } {
  const t = useT();
  const readOnlyReason = useTaskReadOnlyReason(orgId, task.space_id);
  const spaces = useSpaceList(orgId);
  const orgRole = useOrgRole(orgId);
  if (readOnlyReason)
    return {
      allowed: false,
      reason:
        readOnlyReason === t("spaces.errors.retired")
          ? readOnlyReason
          : t("tasks.move.noPermission"),
    };
  const source = spaces.data?.find((space) => space.id === task.space_id);
  if (source && orgRole !== undefined && !canWriteTasksIn(source, orgRole))
    return { allowed: false, reason: t("tasks.move.noPermission") };
  return { allowed: true };
}

/** 菜单里的「移动到空间」子菜单入口：点击不关闭菜单，切换到空间列表；禁用时悬停显示原因。 */
export function MoveToSpaceTrigger({
  reason,
  onOpen,
}: {
  /** 有值时禁用 */
  reason?: string;
  onOpen: () => void;
}) {
  const t = useT();
  const disabled = Boolean(reason);
  return (
    <button
      type="button"
      role="menuitem"
      aria-haspopup="menu"
      aria-disabled={disabled || undefined}
      title={reason}
      onClick={(event) => {
        event.stopPropagation();
        if (!disabled) onOpen();
      }}
      className={cn(
        "flex min-h-[34px] w-full items-center gap-2.5 rounded-lg py-1.5 pr-2 pl-2.5 text-left text-body outline-none",
        "hover:bg-surface-active focus-visible:bg-surface-active focus-visible:shadow-none",
        disabled && "cursor-not-allowed opacity-50",
      )}
    >
      <Icon name="folder-input" className="text-fg-muted" />
      <span className="flex-1">{t("tasks.move.menu")}</span>
      <Icon name="chevron-right" className="text-fg-muted" />
    </button>
  );
}

/**
 * 菜单里的目标空间列表：每项「空间名 + 前缀」，当前空间灰显不可选；超过 8 个空间时顶部有搜索框。
 * contain：菜单挂在可点击的卡片里时传入，列表内的点击 / 按键 / 按下都不冒泡到卡片，选中后由这里调用 close。
 */
export function MoveSpaceOptions({
  orgId,
  task,
  onPick,
  contain,
}: {
  orgId: string;
  task: Pick<Task, "space_id">;
  onPick: (space: SpaceDetail) => void;
  contain?: { close: () => void };
}) {
  const t = useT();
  const spaces = useSpaceList(orgId);
  const orgRole = useOrgRole(orgId);
  const [query, setQuery] = useState("");
  const all = moveTargetOptions(spaces.data ?? [], task.space_id, orgRole);
  const searchable = (spaces.data?.length ?? 0) > MOVE_SEARCH_THRESHOLD;
  const items = filterSpaces(all, query);
  const hasTargets = all.some((item) => !item.current);
  const stop = (event: { stopPropagation: () => void }) => {
    if (contain) event.stopPropagation();
  };
  return (
    <div
      onClick={stop}
      onPointerDown={stop}
      onKeyDown={(event) => {
        if (!contain) return;
        // 冒泡被截住，菜单的方向键导航在这里补上
        handleMenuKeyDown(event);
        event.stopPropagation();
      }}
    >
      <MoveSpaceList
        items={items}
        searchable={searchable}
        query={query}
        onQuery={setQuery}
        empty={hasTargets ? t("tasks.move.noMatch") : t("tasks.move.noTargets")}
        role="menuitem"
        onPick={(space) => {
          contain?.close();
          onPick(space);
        }}
      />
    </div>
  );
}

/**
 * 快捷键 move_to_space（默认 mod+shift+m，可在设置 / 快捷键里改）：任务详情打开时按下，打开移动对话框的「选择目标」一步。
 * 与命令面板一样从账号偏好读按键；已有模态对话框时不响应；抽屉叠在分拣台上时只响应一次（defaultPrevented）。
 */
export function useMoveShortcut(
  permission: { allowed: boolean; reason?: string },
  open: () => void,
) {
  const toast = useToast();
  const prefs = useMyPreferences();
  const shortcut = effectiveShortcut(
    prefs.data?.command_shortcuts ?? {},
    "move_to_space",
  );
  const latest = useRef({ permission, open, toast });
  latest.current = { permission, open, toast };
  useEffect(() => {
    const mac = isMacPlatform();
    const onKey = (event: KeyboardEvent) => {
      if (event.defaultPrevented || !matchesShortcut(event, shortcut, mac))
        return;
      if (document.querySelector('[role="dialog"][aria-modal="true"]')) return;
      event.preventDefault();
      const current = latest.current;
      if (!current.permission.allowed) {
        current.toast(current.permission.reason ?? "", { tone: "error" });
        return;
      }
      current.open();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [shortcut]);
}

/**
 * 移动成功后的去向（用户 2026-11-04 确认「跳到目标空间」）：
 * - 抽屉开在源空间的页面上（`/o/<org>/spaces/<源空间>/...`）：跳到目标空间的任务页并打开新编号（replace，
 *   不留回到源空间旧任务的历史；关闭抽屉后停在目标空间任务页）；
 * - 其他页面（收件箱、我的任务、Agent 面板等跨空间页面）与任务全页：只换成新编号，交给 useTaskParam。
 */
export function useAfterMove(orgId: string) {
  const location = useLocation();
  const navigate = useNavigate();
  const taskParam = useTaskParam();
  return (result: TaskMoveResult) => {
    // 只看当前地址：移动成功时详情缓存已写成目标空间，不能再用任务的 space_id 判断来源。
    const inSpacePage =
      location.pathname.startsWith(`/o/${orgId}/spaces/`) &&
      new URLSearchParams(location.search).has("task");
    if (!inSpacePage) {
      taskParam.open(result.display_id);
      return;
    }
    const target = `/o/${orgId}/spaces/${result.task.space_id}/tasks`;
    navigate(`${target}?task=${encodeURIComponent(result.display_id)}`, {
      replace: true,
    });
  };
}

/**
 * 任务详情（抽屉 / 全页）的移动入口：菜单项、空间列表、对话框与快捷键。
 * 成功后的去向见 useAfterMove。
 */
export function useMoveTaskEntry(
  orgId: string,
  task: Task,
  onOpenOptions: () => void,
) {
  const permission = useMovePermission(orgId, task);
  const afterMove = useAfterMove(orgId);
  const [dialog, setDialog] = useState<{ target: SpaceDetail | null } | null>(
    null,
  );
  useMoveShortcut(permission, () => setDialog({ target: null }));
  return {
    trigger: (
      <MoveToSpaceTrigger reason={permission.reason} onOpen={onOpenOptions} />
    ),
    options: (
      <MoveSpaceOptions
        orgId={orgId}
        task={task}
        onPick={(space) => setDialog({ target: space })}
      />
    ),
    dialog: dialog && (
      <MoveTaskDialog
        orgId={orgId}
        task={task}
        initialTarget={dialog.target}
        onClose={() => setDialog(null)}
        onMoved={afterMove}
      />
    ),
  };
}

/** 空间列表（菜单与对话框的「选择目标」一步共用）。 */
export function MoveSpaceList({
  items,
  searchable,
  query,
  onQuery,
  empty,
  role,
  onPick,
}: {
  items: { space: SpaceDetail; current: boolean }[];
  searchable: boolean;
  query: string;
  onQuery: (value: string) => void;
  empty: string;
  /** 菜单里为 menuitem；对话框里不传，按普通按钮渲染 */
  role?: "menuitem";
  onPick: (space: SpaceDetail) => void;
}): ReactNode {
  const t = useT();
  const selectable = items.filter((item) => !item.current);
  return (
    <>
      {searchable && (
        <div className="p-1">
          <TextInput
            variant="labeled"
            aria-label={t("tasks.move.searchLabel")}
            placeholder={t("tasks.move.searchPlaceholder")}
            leading={<Icon name="search" size={14} />}
            value={query}
            onChange={(event) => onQuery(event.target.value)}
            data-autofocus=""
          />
        </div>
      )}
      {selectable.length === 0 && (
        <p className="px-2.5 py-2 text-body-sm text-fg-muted">{empty}</p>
      )}
      {items.map(({ space, current }) => (
        <button
          key={space.id}
          type="button"
          role={role}
          aria-disabled={current || undefined}
          title={current ? t("tasks.move.currentSpace") : undefined}
          onClick={() => {
            if (!current) onPick(space);
          }}
          className={cn(
            "flex min-h-[34px] w-full items-center gap-2.5 rounded-lg py-1.5 pr-2 pl-2.5 text-left text-body outline-none",
            "hover:bg-surface-active focus-visible:bg-surface-active focus-visible:shadow-none",
            current && "cursor-not-allowed opacity-50 hover:bg-transparent",
          )}
        >
          <Icon name="folder" className="text-fg-muted" />
          <span className="min-w-0 flex-1 truncate">{space.name}</span>
          <span className="text-caption text-fg-muted uppercase">
            {space.prefix}
          </span>
        </button>
      ))}
    </>
  );
}
