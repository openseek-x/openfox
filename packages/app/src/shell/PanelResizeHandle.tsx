import { useEffect, useRef } from "react";

import { cn } from "@openfox/ui";

export function PanelResizeHandle({
  label,
  controls,
  width,
  min,
  max,
  side,
  onWidthChange,
  onWidthPreview,
  onCollapse,
  collapsed = false,
  onExpand,
  className,
}: {
  label: string;
  controls?: string;
  width: number;
  min: number;
  max: number;
  side: "left" | "right";
  onWidthChange: (width: number) => void;
  onWidthPreview: (width: number | null) => void;
  onCollapse: () => void;
  collapsed?: boolean;
  onExpand?: () => void;
  className: string;
}) {
  const start = useRef<{
    pointer: number;
    x: number;
    width: number;
    clientX: number;
    wasCollapsed: boolean;
    collapsed: boolean;
  } | null>(null);
  const frame = useRef<number | null>(null);
  const cancelFrame = () => {
    if (frame.current !== null) cancelAnimationFrame(frame.current);
    frame.current = null;
  };
  useEffect(() => cancelFrame, []);
  const direction = side === "left" ? 1 : -1;
  const change = (value: number) =>
    onWidthChange(Math.max(min, Math.min(max, value)));
  const dragWidth = (clientX: number) =>
    Math.max(
      onExpand ? 0 : min,
      Math.min(
        max,
        start.current!.width + direction * (clientX - start.current!.x),
      ),
    );
  const preview = (clientX: number) => {
    const drag = start.current;
    if (!drag) return;
    const next = dragWidth(clientX);
    onWidthPreview(next);
    if (drag.collapsed && next > 0) {
      onExpand?.();
      drag.collapsed = false;
    }
  };
  const cancelDrag = () => {
    const drag = start.current;
    if (!drag) return;
    cancelFrame();
    start.current = null;
    onWidthPreview(null);
    if (drag.wasCollapsed && !drag.collapsed) onCollapse();
  };
  return (
    <div
      role="separator"
      aria-label={label}
      aria-controls={controls}
      aria-orientation="vertical"
      aria-valuemin={onExpand ? 0 : min}
      aria-valuemax={max}
      aria-valuenow={collapsed ? 0 : width}
      tabIndex={0}
      className={cn(
        "z-30 cursor-col-resize touch-none select-none focus-visible:bg-surface-muted focus-visible:shadow-none",
        className,
      )}
      onPointerDown={(event) => {
        if (event.button !== 0 || start.current) return;
        event.preventDefault();
        event.currentTarget.focus();
        event.currentTarget.setPointerCapture(event.pointerId);
        const displayedWidth =
          (controls &&
            document.getElementById(controls)?.getBoundingClientRect().width) ||
          (collapsed ? 0 : width);
        start.current = {
          pointer: event.pointerId,
          x: event.clientX,
          width: displayedWidth,
          clientX: event.clientX,
          wasCollapsed: collapsed,
          collapsed: collapsed && displayedWidth === 0,
        };
        onWidthPreview(displayedWidth);
        if (collapsed && displayedWidth > 0) onExpand?.();
      }}
      onPointerMove={(event) => {
        if (start.current?.pointer !== event.pointerId) return;
        start.current.clientX = event.clientX;
        if (frame.current !== null) return;
        frame.current = requestAnimationFrame(() => {
          frame.current = null;
          if (start.current) preview(start.current.clientX);
        });
      }}
      onPointerUp={(event) => {
        if (start.current?.pointer !== event.pointerId) return;
        cancelFrame();
        const next = dragWidth(event.clientX);
        if (onExpand && next < min / 2) onCollapse();
        else {
          if (start.current.collapsed) onExpand?.();
          change(next);
        }
        start.current = null;
        onWidthPreview(null);
        event.currentTarget.releasePointerCapture(event.pointerId);
      }}
      onPointerCancel={cancelDrag}
      onLostPointerCapture={cancelDrag}
      onKeyDown={(event) => {
        if (event.key === "ArrowLeft" || event.key === "ArrowRight") {
          event.preventDefault();
          const delta = direction * (event.key === "ArrowRight" ? 16 : -16);
          if (onExpand && collapsed) {
            if (delta > 0) onExpand();
          } else if (onExpand && width + delta < min) {
            onCollapse();
          } else change(width + delta);
        } else if (event.key === "Home" || event.key === "End") {
          event.preventDefault();
          if (onExpand && event.key === "Home") onCollapse();
          else {
            if (collapsed) onExpand?.();
            change(event.key === "Home" ? min : max);
          }
        } else if (event.key === "Enter") {
          event.preventDefault();
          if (collapsed) onExpand?.();
          else onCollapse();
        }
      }}
    />
  );
}
