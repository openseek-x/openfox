import {
  useEffect,
  useId,
  useRef,
  useState,
  type KeyboardEvent,
  type ReactNode,
} from "react";
import { createPortal } from "react-dom";

import { cn } from "../cn";
import { Icon } from "./Icon";
import { IconButton } from "./IconButton";
import { useModalBehavior } from "./Dialog";
import { useT } from "@openfox/i18n";

/** 正在输入或有菜单 / 弹窗打开时，Esc 属于它们，不关闭非模态抽屉。 */
function closeOnEscape(event: KeyboardEvent<HTMLElement>, onClose: () => void) {
  if (event.key !== "Escape" || event.defaultPrevented) return;
  const target = event.target as HTMLElement;
  if (
    target.isContentEditable ||
    ["INPUT", "TEXTAREA", "SELECT"].includes(target.tagName)
  )
    return;
  if (
    document.querySelector(
      '[role="menu"], [role="listbox"], [role="dialog"][aria-modal="true"]',
    )
  )
    return;
  event.stopPropagation();
  onClose();
}

export interface DrawerProps {
  open: boolean;
  onClose: () => void;
  title: ReactNode;
  /** 头部右侧操作（标题左侧为关闭按钮） */
  actions?: ReactNode;
  /** 宽度，默认 min(860, 100vw-378)（电脑详情抽屉） */
  width?: string;
  /**
   * 是否模态，默认 true：有遮罩、锁定焦点与滚动、点遮罩关闭。
   * false 时没有遮罩，页面其余部分仍可操作；焦点在抽屉内且不在输入 / 菜单 / 弹窗里时按 Esc 关闭。
   */
  modal?: boolean;
  children: ReactNode;
}

/**
 * 右侧抽屉：遮罩 #0000003F，从右滑入 300ms；头部固定、内容区滚动。
 * 外框距顶 44、右 9、底 9，避开标题栏。
 */
export function Drawer({
  open,
  onClose,
  title,
  actions,
  width = "min(860px, calc(100vw - 378px))",
  modal = true,
  children,
}: DrawerProps) {
  const panel = useRef<HTMLDivElement | null>(null);
  const t = useT();
  const titleId = useId();
  const [entered, setEntered] = useState(false);
  useModalBehavior(open && modal, panel, onClose);
  useEffect(() => {
    if (!open) {
      setEntered(false);
      return;
    }
    const frame = requestAnimationFrame(() => setEntered(true));
    return () => cancelAnimationFrame(frame);
  }, [open]);
  // 非模态：打开时把焦点放进抽屉（让 Esc 可用），关闭时若焦点还在抽屉里则还给之前的元素
  useEffect(() => {
    if (!open || modal) return;
    const node = panel.current;
    const previous = document.activeElement as HTMLElement | null;
    node?.focus({ preventScroll: true });
    return () => {
      const active = document.activeElement;
      if (active === document.body || (node && node.contains(active)))
        previous?.focus?.();
    };
  }, [open, modal]);
  if (!open) return null;
  return createPortal(
    <div
      className={cn(
        "fixed inset-0 z-40",
        // 非模态：外层不拦截指针，只有抽屉本身可点
        modal ? "bg-black/25" : "pointer-events-none",
      )}
      onPointerDown={
        modal
          ? (event) => {
              if (event.target === event.currentTarget) onClose();
            }
          : undefined
      }
    >
      <div
        ref={panel}
        role="dialog"
        aria-modal={modal ? "true" : undefined}
        aria-labelledby={titleId}
        tabIndex={-1}
        style={{ width }}
        onKeyDown={modal ? undefined : (event) => closeOnEscape(event, onClose)}
        className={cn(
          "absolute top-11 right-[9px] bottom-[9px] flex flex-col overflow-hidden rounded-xl border border-line-default bg-surface-card shadow-dialog outline-none",
          !modal && "pointer-events-auto",
          "transition-transform duration-300 ease-drawer",
          entered ? "translate-x-0" : "translate-x-[calc(100%+9px)]",
        )}
      >
        <div className="flex h-11 shrink-0 items-center gap-2 border-b border-line-default px-3">
          <IconButton
            label={t("common.close")}
            icon={<Icon name="x" />}
            size="28"
            onClick={onClose}
          />
          <h2
            id={titleId}
            className="min-w-0 flex-1 truncate text-body font-medium"
          >
            {title}
          </h2>
          {actions}
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto">{children}</div>
      </div>
    </div>,
    document.body,
  );
}
