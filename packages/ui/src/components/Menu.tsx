import { useRef, useState, type KeyboardEvent, type ReactNode } from "react";

import { cn } from "../cn";
import { Icon } from "./Icon";
import { Popover, type Placement } from "./Popover";

export interface MenuItemProps {
  icon?: ReactNode;
  /** 右侧内容：快捷键、子菜单箭头、计数 */
  trailing?: ReactNode;
  /** 选中态（单选菜单）显示对勾 */
  selected?: boolean;
  danger?: boolean;
  disabled?: boolean;
  /** 禁用原因，作为 title 提示 */
  disabledReason?: string;
  description?: ReactNode;
  onSelect?: () => void;
  children: ReactNode;
}

/** 菜单项，高 34、圆角 10。 */
export function MenuItem({
  icon,
  trailing,
  selected,
  danger,
  disabled,
  disabledReason,
  description,
  onSelect,
  children,
}: MenuItemProps) {
  return (
    <button
      type="button"
      role={selected === undefined ? "menuitem" : "menuitemradio"}
      aria-checked={selected}
      aria-disabled={disabled || undefined}
      title={disabled ? disabledReason : undefined}
      onClick={() => {
        if (!disabled) onSelect?.();
      }}
      className={cn(
        "flex min-h-[34px] w-full items-center gap-2.5 rounded-lg py-1.5 pr-2 pl-2.5 text-left text-body outline-none",
        "hover:bg-surface-active focus-visible:bg-surface-active focus-visible:shadow-none",
        danger ? "text-danger" : "text-fg-default",
        disabled && "cursor-not-allowed opacity-50 hover:bg-transparent",
      )}
    >
      {icon && (
        <span className={cn("flex", danger ? "text-danger" : "text-fg-muted")}>
          {icon}
        </span>
      )}
      <span className="flex min-w-0 flex-1 flex-col">
        <span className="truncate">{children}</span>
        {description && (
          <span className="text-caption text-fg-muted">{description}</span>
        )}
      </span>
      {trailing && (
        <span className="flex text-caption text-fg-muted">{trailing}</span>
      )}
      {selected && <Icon name="check" className="text-fg-default" />}
    </button>
  );
}

export function MenuSeparator() {
  return <div role="separator" className="my-1 h-px bg-line-default" />;
}

export function MenuLabel({ children }: { children: ReactNode }) {
  return (
    <div className="px-2.5 pt-2 pb-1 text-caption text-fg-muted">
      {children}
    </div>
  );
}

/** 方向键在菜单项之间移动焦点。 */
export function handleMenuKeyDown(event: KeyboardEvent<HTMLElement>) {
  if (!["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) return;
  const items = Array.from(
    event.currentTarget.querySelectorAll<HTMLElement>(
      '[role^="menuitem"]:not([aria-disabled="true"]),[role="option"]:not([aria-disabled="true"])',
    ),
  );
  if (items.length === 0) return;
  event.preventDefault();
  event.stopPropagation();
  const index = items.indexOf(document.activeElement as HTMLElement);
  const next =
    event.key === "Home"
      ? 0
      : event.key === "End"
        ? items.length - 1
        : event.key === "ArrowDown"
          ? (index + 1) % items.length
          : (index - 1 + items.length) % items.length;
  items[next]?.focus();
}

export interface MenuProps {
  /** 触发按钮：接收 ref、展开状态与切换函数 */
  trigger: (props: {
    ref: (node: HTMLElement | null) => void;
    open: boolean;
    toggle: () => void;
  }) => ReactNode;
  placement?: Placement;
  label: string;
  className?: string;
  /** 菜单内容；函数形式可拿到 close */
  children: ReactNode | ((close: () => void) => ReactNode);
}

/** 下拉菜单：触发按钮 + 浮层，选中任意项后自动关闭。 */
export function Menu({
  trigger,
  placement = "bottom-start",
  label,
  className,
  children,
}: MenuProps) {
  const [open, setOpen] = useState(false);
  const anchor = useRef<HTMLElement | null>(null);
  const close = () => setOpen(false);
  return (
    <>
      {trigger({
        ref: (node) => {
          anchor.current = node;
        },
        open,
        toggle: () => setOpen((value) => !value),
      })}
      <Popover
        open={open}
        onOpenChange={setOpen}
        anchorRef={anchor}
        placement={placement}
        role="menu"
        aria-label={label}
        className={cn("min-w-[200px]", className)}
      >
        <div
          onKeyDown={handleMenuKeyDown}
          onClick={(event) => {
            if (
              (event.target as HTMLElement).closest(
                '[role^="menuitem"]:not([aria-disabled="true"])',
              )
            )
              close();
          }}
        >
          {typeof children === "function" ? children(close) : children}
        </div>
      </Popover>
    </>
  );
}
