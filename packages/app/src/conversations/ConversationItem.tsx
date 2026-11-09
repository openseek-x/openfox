import { useEffect, useRef, useState, type ReactNode } from "react";
import { Link } from "react-router-dom";

import type { ConversationSummary } from "@openfox/api";
import { useI18n, useT } from "@openfox/i18n";
import { Icon, IconButton, Menu, MenuItem, cn } from "@openfox/ui";

import { AgentAvatar } from "../agents/AgentAvatar";
import { formatRelativeTime } from "../tasks/primitives/dates";
import {
  conversationTitle,
  displayStatus,
  plainPreview,
  statusMeta,
  type DisplayStatus,
} from "./model";

/** 列表状态字颜色：等你回复为红、等你审阅为黄。 */
const statusText: Record<DisplayStatus, string> = {
  WORKING: "text-accent",
  QUEUED: "text-accent",
  WAITING: "text-danger",
  REVIEW: "text-warning",
  ERROR: "text-danger",
  DONE: "text-info",
  DISCARDED: "text-fg-muted",
  ABORTED: "text-fg-muted",
  IDLE: "text-fg-muted",
};

/** 状态字：图标 + 文案（Sidebar/ConversationItem 第二行）；IDLE 不显示 */
export function StatusWord({
  status,
  className,
}: {
  status: DisplayStatus;
  className?: string;
}) {
  const t = useT();
  const meta = statusMeta(status);
  if (!meta) return null;
  return (
    <span
      className={cn(
        "inline-flex shrink-0 items-center gap-1",
        statusText[status],
        className,
      )}
    >
      <Icon name={meta.icon} size={12} />
      {t(meta.label)}
    </span>
  );
}

/** 行内重命名输入框：Enter 保存、Esc 取消、失焦保存 */
export function InlineRename({
  initial,
  onSave,
  onCancel,
}: {
  initial: string;
  onSave: (title: string) => void;
  onCancel: () => void;
}) {
  const t = useT();
  const [value, setValue] = useState(initial);
  const ref = useRef<HTMLInputElement | null>(null);
  const done = useRef(false);
  useEffect(() => {
    ref.current?.focus();
    ref.current?.select();
  }, []);
  const finish = (save: boolean) => {
    if (done.current) return;
    done.current = true;
    if (save && value.trim() !== initial.trim()) onSave(value);
    else onCancel();
  };
  return (
    <input
      ref={ref}
      aria-label={t("chat.row.renameLabel")}
      value={value}
      maxLength={200}
      onChange={(event) => setValue(event.target.value)}
      onKeyDown={(event) => {
        if (event.key === "Enter" && !event.nativeEvent.isComposing) {
          event.preventDefault();
          finish(true);
        } else if (event.key === "Escape") {
          event.preventDefault();
          event.stopPropagation();
          finish(false);
        }
      }}
      onBlur={() => finish(true)}
      className="h-7 w-full rounded-md border border-line-focus bg-surface-card px-2 text-body outline-none"
    />
  );
}

export interface RowMenuItem {
  key: string;
  label: string;
  icon: ReactNode;
  danger?: boolean;
  onSelect: () => void;
}

/** 行的 ··· 菜单；右键行时也打开同一个菜单 */
export function RowMenu({
  label,
  items,
  open,
  onOpenChange,
}: {
  label: string;
  items: RowMenuItem[];
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
}) {
  const toggleRef = useRef<(() => void) | null>(null);
  const openRef = useRef(false);
  // 只响应外部的「打开」请求（右键），处理后复位
  useEffect(() => {
    if (!open) return;
    if (!openRef.current) toggleRef.current?.();
    onOpenChange?.(false);
  }, [open, onOpenChange]);
  if (items.length === 0) return null;
  return (
    <Menu
      label={label}
      placement="bottom-end"
      trigger={({ ref, toggle, open: isOpen }) => {
        toggleRef.current = toggle;
        openRef.current = isOpen;
        return (
          <IconButton
            ref={ref}
            label={label}
            icon={<Icon name="ellipsis" size={14} />}
            size="28"
            variant="ghost"
            aria-expanded={isOpen}
            onClick={(event) => {
              event.preventDefault();
              event.stopPropagation();
              toggle();
            }}
            className={cn(
              "size-6 opacity-0 group-hover:opacity-100 focus-visible:opacity-100",
              isOpen && "opacity-100",
            )}
          />
        );
      }}
    >
      {items.map((item) => (
        <MenuItem
          key={item.key}
          icon={item.icon}
          danger={item.danger}
          onSelect={item.onSelect}
        >
          {item.label}
        </MenuItem>
      ))}
    </Menu>
  );
}

/**
 * 会话行：标题 + Agent 头像；第二行状态字、摘要、时间。
 * 未读时左侧红点；悬停时时间换成「完成」勾（可完成时）与 ··· 菜单。
 */
export function ConversationItem({
  to,
  conversation,
  selected,
  renaming,
  onRename,
  onRenameEnd,
  onComplete,
  menuItems = [],
}: {
  to: string;
  conversation: ConversationSummary;
  selected?: boolean;
  renaming?: boolean;
  onRename?: (title: string) => void;
  onRenameEnd?: () => void;
  onComplete?: () => void;
  menuItems?: RowMenuItem[];
}) {
  const t = useT();
  const { locale } = useI18n();
  const [menuRequest, setMenuRequest] = useState(false);
  const title = conversationTitle(conversation);
  const preview = plainPreview(conversation.last_message?.preview ?? "");
  const time = conversation.last_message?.created_at ?? conversation.updated_at;
  const label = title ?? t("chat.untitled");

  return (
    <div
      className={cn(
        "group relative rounded-lg",
        selected ? "bg-surface-selected" : "hover:bg-surface-hover",
      )}
      onContextMenu={(event) => {
        if (menuItems.length === 0) return;
        event.preventDefault();
        setMenuRequest(true);
      }}
    >
      {conversation.unread && (
        <span
          aria-label={t("chat.list.unread")}
          className="absolute top-1/2 -left-1 size-1.5 -translate-y-1/2 rounded-full bg-danger"
        />
      )}
      {renaming && onRename ? (
        <div className="px-2 py-[var(--density-chat-padding,6px)]">
          <InlineRename
            initial={title ?? ""}
            onSave={onRename}
            onCancel={() => onRenameEnd?.()}
          />
        </div>
      ) : (
        <Link
          to={to}
          aria-current={selected ? "page" : undefined}
          className="flex flex-col gap-0.5 rounded-lg px-2 py-[var(--density-chat-padding,6px)] text-left outline-none focus-visible:ring-2 focus-visible:ring-line-focus"
        >
          <span className="flex items-center gap-1.5">
            {conversation.task && (
              <Icon
                name="openfox-clipboard-task"
                size={14}
                className="shrink-0 text-accent"
              />
            )}
            <span
              className={cn(
                "min-w-0 flex-1 truncate text-body",
                conversation.unread ? "font-semibold" : "font-medium",
              )}
            >
              {label}
            </span>
            <AgentAvatar
              name={conversation.agent.name}
              avatar={conversation.agent.avatar}
              size={20}
            />
          </span>
          <span className="flex items-center gap-1.5 text-caption text-fg-muted">
            <StatusWord status={displayStatus(conversation)} />
            <span className="min-w-0 flex-1 truncate">{preview}</span>
            <span
              className={cn(
                "shrink-0",
                (onComplete || menuItems.length > 0) && "group-hover:invisible",
              )}
            >
              {formatRelativeTime(time, locale)}
            </span>
          </span>
        </Link>
      )}
      {!renaming && (onComplete || menuItems.length > 0) && (
        <div className="absolute right-1.5 bottom-1 flex items-center gap-0.5">
          {onComplete && (
            <IconButton
              label={t("chat.row.complete")}
              icon={<Icon name="check" size={14} />}
              size="28"
              variant="ghost"
              onClick={onComplete}
              className="size-6 opacity-0 group-hover:opacity-100 focus-visible:opacity-100"
            />
          )}
          <RowMenu
            label={t("chat.row.actions")}
            items={menuItems}
            open={menuRequest}
            onOpenChange={setMenuRequest}
          />
        </div>
      )}
    </div>
  );
}
