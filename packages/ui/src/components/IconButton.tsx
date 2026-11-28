import { forwardRef, type ButtonHTMLAttributes, type ReactNode } from "react";

import { cn } from "../cn";
import { Tooltip } from "./Tooltip";

export type IconButtonStyle =
  "round" | "ghost-soft" | "outline" | "secondary" | "ghost";
export type IconButtonSize = "36" | "32" | "28";

export interface IconButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  /** 必填：图标按钮没有可见文字，需要无障碍名称 */
  label: string;
  icon: ReactNode;
  variant?: IconButtonStyle;
  size?: IconButtonSize;
  /** 按下/展开态（Figma Active/Pressed） */
  active?: boolean;
  /** 开启自定义 Tooltip（悬停和键盘聚焦均显示），避免与原生 title 重复弹出 */
  tooltip?: boolean | ReactNode;
}

const sizeClass: Record<IconButtonSize, string> = {
  "36": "size-9",
  "32": "size-8",
  "28": "size-7",
};

const idleClass: Record<IconButtonStyle, string> = {
  round:
    "bg-surface-card shadow-card enabled:not-aria-disabled:hover:border-line-strong enabled:not-aria-disabled:hover:bg-surface-hover",
  outline:
    "bg-surface-card shadow-card enabled:not-aria-disabled:hover:border-line-strong enabled:not-aria-disabled:hover:bg-surface-hover",
  secondary:
    "bg-surface-muted enabled:not-aria-disabled:hover:bg-secondary-hover",
  "ghost-soft":
    "enabled:not-aria-disabled:hover:bg-control-hover enabled:not-aria-disabled:hover:text-fg-default",
  ghost:
    "enabled:not-aria-disabled:hover:bg-control-hover enabled:not-aria-disabled:hover:text-fg-default",
};

/** 图标按钮，对应 Figma IconButton 的 Style × Size 变体。 */
export const IconButton = forwardRef<HTMLButtonElement, IconButtonProps>(
  function IconButton(
    {
      label,
      icon,
      variant = "ghost",
      size = "32",
      active = false,
      tooltip = false,
      className,
      type = "button",
      ...rest
    },
    ref,
  ) {
    const radius =
      variant === "round"
        ? "rounded-full"
        : size === "36" || variant === "ghost-soft" || variant === "secondary"
          ? "rounded-lg"
          : "rounded-md";
    const highlighted = [
      active,
      rest["aria-expanded"],
      rest["aria-pressed"],
    ].some((state) => state === true || state === "true");
    const bordered = variant === "outline" || variant === "round";
    const button = (
      <button
        ref={ref}
        type={type}
        aria-label={label}
        title={tooltip ? undefined : label}
        aria-pressed={
          (rest["aria-expanded"] === undefined && active) || undefined
        }
        className={cn(
          "inline-flex shrink-0 items-center justify-center transition-colors duration-100 motion-reduce:transition-none",
          "disabled:cursor-not-allowed disabled:opacity-50",
          sizeClass[size],
          radius,
          bordered && "border",
          bordered &&
            (highlighted && variant === "round"
              ? "border-line-strong"
              : "border-line-default"),
          highlighted
            ? "bg-surface-active text-fg-default"
            : "text-fg-secondary",
          !highlighted && idleClass[variant],
          className,
        )}
        {...rest}
      >
        {icon}
      </button>
    );
    if (!tooltip) return button;
    return (
      <Tooltip content={tooltip === true ? label : tooltip}>{button}</Tooltip>
    );
  },
);
