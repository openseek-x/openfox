import { useState } from "react";

import type { SpaceDetail, Task, TaskStatus } from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Icon,
  IconButton,
  Menu,
  MenuItem,
  MenuLabel,
  MenuSeparator,
  cn,
} from "@openfox/ui";

import { displayIdOf, taskUrl, useCopy } from "./parts";
import { StatusIcon } from "./primitives/StatusIcon";
import { useTaskReadOnlyReason } from "../spaces/readonly";
import { MoveTaskDialog } from "./detail/MoveTaskDialog";
import {
  MoveSpaceOptions,
  MoveToSpaceTrigger,
  useMovePermission,
} from "./detail/moveToSpace";

/** 卡片 / 行的「···」菜单：打开任务、移动到（状态）、复制链接、复制编号。 */
export function TaskMenu({
  orgId,
  task,
  statuses,
  onOpen,
  onMove,
  className,
}: {
  orgId: string;
  task: Task;
  statuses: TaskStatus[];
  onOpen: () => void;
  onMove: (status: TaskStatus) => void;
  className?: string;
}) {
  const t = useT();
  const copy = useCopy();
  const [level, setLevel] = useState<"root" | "move" | "space">("root");
  // 已停用空间的任务不能移动状态。
  const readOnlyReason = useTaskReadOnlyReason(orgId, task.space_id);
  const readOnly = Boolean(readOnlyReason);
  // 移动到空间：卡片上移动后不跳转，只刷新列表。
  const movePermission = useMovePermission(orgId, task);
  const [moveTarget, setMoveTarget] = useState<SpaceDetail | null>(null);
  return (
    <>
      {moveTarget && (
        // 对话框经 portal 渲染，但 React 事件仍沿组件树冒泡到卡片：截住，避免点对话框时打开任务或开始拖动
        <span
          className="contents"
          onClick={(event) => event.stopPropagation()}
          onPointerDown={(event) => event.stopPropagation()}
          onKeyDown={(event) => event.stopPropagation()}
        >
          <MoveTaskDialog
            orgId={orgId}
            task={task}
            initialTarget={moveTarget}
            onClose={() => setMoveTarget(null)}
          />
        </span>
      )}
      <Menu
        label={t("tasks.board.cardMenu")}
        placement="bottom-end"
        trigger={({ ref, toggle, open }) => (
          <IconButton
            ref={ref}
            label={t("tasks.board.cardMenu")}
            icon={<Icon name="ellipsis" />}
            size="28"
            active={open}
            className={className}
            onPointerDown={(event) => event.stopPropagation()}
            onKeyDown={(event) => event.stopPropagation()}
            onClick={(event) => {
              event.stopPropagation();
              setLevel("root");
              toggle();
            }}
          />
        )}
      >
        {(close) =>
          level === "space" ? (
            <MoveSpaceOptions
              orgId={orgId}
              task={task}
              contain={{ close }}
              onPick={setMoveTarget}
            />
          ) : level === "root" ? (
            <>
              <MenuItem icon={<Icon name="external-link" />} onSelect={onOpen}>
                {t("tasks.board.open")}
              </MenuItem>
              <button
                type="button"
                role="menuitem"
                aria-haspopup="menu"
                aria-disabled={readOnly || undefined}
                title={readOnlyReason}
                onClick={(event) => {
                  // 不关闭菜单，切换到状态列表
                  event.stopPropagation();
                  if (!readOnly) setLevel("move");
                }}
                className={cn(
                  "flex min-h-[34px] w-full items-center gap-2.5 rounded-lg py-1.5 pr-2 pl-2.5 text-left text-body outline-none",
                  "hover:bg-surface-active focus-visible:bg-surface-active focus-visible:shadow-none",
                  readOnly && "cursor-not-allowed opacity-50",
                )}
              >
                <Icon name="arrow-right" className="text-fg-muted" />
                <span className="flex-1">{t("tasks.board.moveTo")}</span>
                <Icon name="chevron-right" className="text-fg-muted" />
              </button>
              <MoveToSpaceTrigger
                reason={movePermission.reason}
                onOpen={() => setLevel("space")}
              />
              <MenuSeparator />
              <MenuItem
                icon={<Icon name="link" />}
                onSelect={() =>
                  void copy(taskUrl(orgId, task), t("tasks.copied.link"))
                }
              >
                {t("tasks.board.copyLink")}
              </MenuItem>
              <MenuItem
                icon={<Icon name="copy" />}
                onSelect={() =>
                  void copy(displayIdOf(task), t("tasks.copied.id"))
                }
              >
                {t("tasks.board.copyId")}
              </MenuItem>
            </>
          ) : (
            <>
              <MenuLabel>{t("tasks.board.moveTo")}</MenuLabel>
              {statuses.map((status) => (
                <MenuItem
                  key={status.id}
                  icon={
                    <StatusIcon
                      category={status.category}
                      color={status.color}
                    />
                  }
                  selected={status.id === task.status_id}
                  onSelect={() => {
                    if (status.id !== task.status_id) onMove(status);
                  }}
                >
                  {status.name}
                </MenuItem>
              ))}
            </>
          )
        }
      </Menu>
    </>
  );
}

/** 改状态时乐观显示的字段 */
export function statusFields(status: TaskStatus): Partial<Task> {
  return {
    status_id: status.id,
    status_name: status.name,
    status_category: status.category,
    status_color: status.color ?? null,
  };
}
