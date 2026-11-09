import { forwardRef, type ButtonHTMLAttributes, type ReactNode } from "react";

import { cn } from "../cn";

export type IconButtonStyle =
  "round" | "ghost-soft" | "outline" | "secondary" | "ghost";
export type IconButtonSize = "36" | "32" | "28";

export interface IconButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  /** 必填：图标按钮没有可见文字，需要无障碍名称 */
  label: string;
  icon: ReactNode;
  variant?: IconButtonStyle;
  size?: IconButtonSize;
  /** 按下或展开状态。 */
  active?: boolean;
}

const sizeClass: Record<IconButtonSize, string> = {
  "36": "size-9",
  "32": "size-8",
  "28": "size-7",
};

const variantClass: Record<IconButtonStyle, string> = {
  round:
    "rounded-full bg-surface-card border border-line-default hover:bg-surface-muted",
  "ghost-soft": "rounded-lg hover:bg-surface-muted",
  outline: "bg-surface-card border border-line-default hover:bg-surface-hover",
  secondary: "rounded-lg bg-surface-muted hover:bg-surface-active",
  ghost: "hover:bg-surface-muted",
};

/** 支持不同样式和尺寸的图标按钮。 */
export const IconButton = forwardRef<HTMLButtonElement, IconButtonProps>(
  function IconButton(
    {
      label,
      icon,
      variant = "ghost",
      size = "32",
      active = false,
      className,
      type = "button",
      ...rest
    },
    ref,
  ) {
    const radius =
      variant === "round"
        ? ""
        : size === "36" || variant === "ghost-soft" || variant === "secondary"
          ? "rounded-lg"
          : "rounded-md";
    return (
      <button
        ref={ref}
        type={type}
        aria-label={label}
        title={label}
        aria-pressed={active || undefined}
        className={cn(
          "inline-flex shrink-0 items-center justify-center text-fg-secondary transition-colors",
          "disabled:cursor-not-allowed disabled:opacity-50",
          sizeClass[size],
          radius,
          variantClass[variant],
          active &&
            (variant === "round"
              ? "bg-surface-muted border-line-strong"
              : "bg-surface-muted"),
          className,
        )}
        {...rest}
      >
        {icon}
      </button>
    );
  },
);
