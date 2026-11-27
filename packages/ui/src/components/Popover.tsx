import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type ReactNode,
  type RefObject,
} from "react";
import { createPortal } from "react-dom";

import { cn } from "../cn";
import { isOutsidePress } from "./outsideClick";

export type Placement =
  | "bottom-start"
  | "bottom-end"
  | "top-start"
  | "top-end"
  | "right-start"
  | "right-end";

export interface PopoverProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Escape can discard a draft while clicking outside applies it. */
  onEscape?: () => void;
  /** 定位参照元素 */
  anchorRef: RefObject<HTMLElement | null>;
  placement?: Placement;
  /** 与参照元素的间距 */
  offset?: number;
  /** 宽度与参照元素一致（下拉框） */
  matchWidth?: boolean;
  /** Keep long vertical menus scrollable without covering their trigger. */
  fitHeight?: boolean;
  className?: string;
  role?: string;
  "aria-label"?: string;
  children: ReactNode;
}

interface Position {
  top: number;
  left: number;
  minWidth?: number;
  maxHeight?: number;
}

const VIEWPORT_MARGIN = 8;

function computePosition(
  anchor: DOMRect,
  panel: DOMRect,
  placement: Placement,
  offset: number,
  fitHeight: boolean,
): Position {
  let top: number;
  let left: number;
  let maxHeight: number | undefined;
  if (placement.startsWith("right")) {
    left = anchor.right + offset;
    top =
      placement === "right-start" ? anchor.top : anchor.bottom - panel.height;
  } else {
    let below = placement.startsWith("bottom");
    if (fitHeight) {
      const aboveRoom = Math.max(0, anchor.top - offset - VIEWPORT_MARGIN);
      const belowRoom = Math.max(
        0,
        window.innerHeight - anchor.bottom - offset - VIEWPORT_MARGIN,
      );
      if (panel.height > (below ? belowRoom : aboveRoom))
        below = belowRoom >= aboveRoom;
      maxHeight = below ? belowRoom : aboveRoom;
    }
    const height = Math.min(panel.height, maxHeight ?? panel.height);
    top = below ? anchor.bottom + offset : anchor.top - height - offset;
    // 下方放不下时翻到上方，反之亦然
    if (
      below &&
      top + panel.height > window.innerHeight - VIEWPORT_MARGIN &&
      anchor.top - panel.height - offset > VIEWPORT_MARGIN
    ) {
      top = anchor.top - panel.height - offset;
    } else if (!below && top < VIEWPORT_MARGIN) {
      top = anchor.bottom + offset;
    }
    left = placement.endsWith("start")
      ? anchor.left
      : anchor.right - panel.width;
  }
  left = Math.min(
    Math.max(left, VIEWPORT_MARGIN),
    window.innerWidth - panel.width - VIEWPORT_MARGIN,
  );
  top = Math.min(
    Math.max(top, VIEWPORT_MARGIN),
    window.innerHeight -
      Math.min(panel.height, maxHeight ?? panel.height) -
      VIEWPORT_MARGIN,
  );
  return { top, left, maxHeight };
}

/**
 * 浮层：渲染到所属模态窗口（否则 body），保留无障碍树与焦点范围；按参照元素定位。
 * 对应 Figma 的 Popover/* 与 Menu/* 外壳（圆角 14、浮层阴影）。
 */
export function Popover({
  open,
  onOpenChange,
  onEscape,
  anchorRef,
  placement = "bottom-start",
  offset = 4,
  matchWidth = false,
  fitHeight = false,
  className,
  role = "dialog",
  children,
  ...aria
}: PopoverProps) {
  const panelRef = useRef<HTMLDivElement | null>(null);
  const [position, setPosition] = useState<Position | null>(null);

  const update = useCallback(() => {
    const anchor = anchorRef.current;
    const panel = panelRef.current;
    if (!anchor || !panel) return;
    const a = anchor.getBoundingClientRect();
    const next = computePosition(
      a,
      panel.getBoundingClientRect(),
      placement,
      offset,
      fitHeight,
    );
    setPosition(matchWidth ? { ...next, minWidth: a.width } : next);
  }, [anchorRef, placement, offset, matchWidth, fitHeight]);

  useLayoutEffect(() => {
    if (!open) {
      setPosition(null);
      return;
    }
    update();
  }, [open, update]);

  useEffect(() => {
    if (!open) return;
    const onPointer = (event: PointerEvent) => {
      if (
        isOutsidePress(event, panelRef.current) &&
        !anchorRef.current?.contains(event.target as Node)
      )
        onOpenChange(false);
    };
    const onKey = (event: KeyboardEvent) => {
      if (event.key !== "Escape") return;
      if (panelRef.current?.querySelector("[data-openfox-popover]")) return;
      event.stopPropagation();
      if (onEscape) onEscape();
      else onOpenChange(false);
      anchorRef.current?.focus();
    };
    document.addEventListener("pointerdown", onPointer, true);
    document.addEventListener("keydown", onKey, true);
    window.addEventListener("resize", update);
    window.addEventListener("scroll", update, true);
    const observer =
      typeof ResizeObserver === "undefined" ? null : new ResizeObserver(update);
    if (panelRef.current) observer?.observe(panelRef.current);
    if (anchorRef.current) observer?.observe(anchorRef.current);
    return () => {
      observer?.disconnect();
      document.removeEventListener("pointerdown", onPointer, true);
      document.removeEventListener("keydown", onKey, true);
      window.removeEventListener("resize", update);
      window.removeEventListener("scroll", update, true);
    };
  }, [open, onOpenChange, onEscape, anchorRef, update]);

  if (!open) return null;
  return createPortal(
    <div
      ref={panelRef}
      data-openfox-popover=""
      role={role}
      {...aria}
      style={{
        top: position?.top ?? -9999,
        left: position?.left ?? -9999,
        minWidth: position?.minWidth,
        maxHeight: position?.maxHeight,
      }}
      className={cn(
        "fixed z-50 rounded-xl border border-line-floating bg-surface-card p-1.5 shadow-popover outline-none",
        position ? "visible" : "invisible",
        className,
      )}
    >
      {children}
    </div>,
    anchorRef.current?.closest('[data-openfox-popover], [aria-modal="true"]') ??
      document.body,
  );
}
