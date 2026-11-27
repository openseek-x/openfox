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
import { dismissOnBackdrop, useDrawerOutsideClick } from "./outsideClick";
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
  side?: "left" | "right";
  /**
   * 是否模态，默认 true：有遮罩、锁定焦点与滚动、点遮罩关闭。
   * false 时页面其余部分仍可操作，点击外部关闭当前抽屉。
   */
  modal?: boolean;
  /** 原地切换详情时标识当前条目，避免外部点击同时关闭刚打开的详情。 */
  itemKey?: string;
  children: ReactNode;
}

/**
 * 抽屉默认从右侧滑入，导航可从左侧打开。头部固定、内容区滚动。
 * 外框距顶 44、两侧对应边及底部 9，避开标题栏。
 */
export function Drawer({
  open,
  onClose,
  title,
  actions,
  width = "min(860px, calc(100vw - 378px))",
  side = "right",
  modal = true,
  itemKey,
  children,
}: DrawerProps) {
  const panel = useRef<HTMLDivElement | null>(null);
  const t = useT();
  const titleId = useId();
  const [entered, setEntered] = useState(false);
  useModalBehavior(open && modal, panel, onClose);
  useDrawerOutsideClick(panel, open && !modal, onClose, itemKey);
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
        modal ? "bg-scrim/30 backdrop-blur-[2px]" : "pointer-events-none",
      )}
      onPointerDown={modal ? dismissOnBackdrop(onClose) : undefined}
    >
      <div
        ref={panel}
        data-openfox-drawer=""
        role="dialog"
        aria-modal={modal ? "true" : undefined}
        aria-labelledby={titleId}
        tabIndex={-1}
        style={{ width }}
        onKeyDown={modal ? undefined : (event) => closeOnEscape(event, onClose)}
        className={cn(
          "absolute top-11 bottom-[9px] flex flex-col overflow-hidden rounded-xl border border-line-floating bg-surface-card shadow-dialog outline-none",
          !modal && "pointer-events-auto",
          "transition-transform duration-300 ease-drawer",
          side === "left" ? "left-[9px]" : "right-[9px]",
          !entered &&
            (side === "left"
              ? "-translate-x-[calc(100%+9px)]"
              : "translate-x-[calc(100%+9px)]"),
          "motion-reduce:transition-none",
        )}
      >
        <div className="flex h-11 shrink-0 items-center gap-2 border-b border-line-subtle px-3.5">
          <IconButton
            label={t("common.close")}
            icon={<Icon name="x" />}
            size="28"
            onClick={onClose}
          />
          <h2
            id={titleId}
            className="min-w-0 flex-1 truncate text-body font-semibold"
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
