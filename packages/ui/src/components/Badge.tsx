import type { HTMLAttributes } from "react";

import { cn } from "../cn";

export type BadgeTone =
  "neutral" | "success" | "warning" | "danger" | "info" | "dark" | "muted";
export type BadgeStyle = "soft" | "outline" | "status";

export interface BadgeProps extends HTMLAttributes<HTMLSpanElement> {
  tone?: BadgeTone;
  variant?: BadgeStyle;
  /** status 样式的小号（高 18，带描边） */
  small?: boolean;
  dot?: boolean;
}

const softTone: Record<BadgeTone, string> = {
  neutral: "bg-surface-muted text-fg-secondary",
  success: "bg-accent-soft text-accent",
  warning: "bg-warning-soft text-warning",
  danger: "bg-danger-soft text-danger",
  info: "bg-info-soft text-info",
  dark: "bg-primary text-fg-inverse",
  muted: "bg-surface-muted text-fg-muted",
};

const statusTone: Partial<Record<BadgeTone, string>> = {
  success: "bg-success-soft text-accent border-pill-success-border",
  warning: "bg-warning-soft text-warning border-pill-warning-border",
  muted: "bg-surface-muted text-fg-muted border-pill-muted-border",
};

/** 徽标与状态胶囊，支持柔和、描边和状态样式。 */
export function Badge({
  tone = "neutral",
  variant = "soft",
  small = false,
  dot = false,
  className,
  children,
  ...rest
}: BadgeProps) {
  return (
    <span
      className={cn(
        "inline-flex shrink-0 items-center rounded-full whitespace-nowrap text-caption",
        variant === "soft" && cn("h-5 gap-1 px-2 font-medium", softTone[tone]),
        variant === "outline" &&
          cn(
            "h-5 gap-1.5 border border-line-default px-2",
            tone === "neutral"
              ? "font-medium text-fg-default"
              : "text-fg-muted",
          ),
        variant === "status" &&
          cn(
            "gap-1 px-1.5 font-medium",
            small ? "h-[18px] border" : "h-5 border border-transparent",
            statusTone[tone] ?? softTone[tone],
          ),
        className,
      )}
      {...rest}
    >
      {dot && <span aria-hidden className="size-1.5 rounded-full bg-current" />}
      {children}
    </span>
  );
}
