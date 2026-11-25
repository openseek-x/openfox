import { forwardRef, type ButtonHTMLAttributes, type ReactNode } from "react";

import { cn } from "../cn";
import { Spinner } from "./Spinner";

export type ButtonVariant =
  | "primary"
  | "secondary"
  | "outline"
  | "ghost"
  | "ghost-muted"
  | "danger"
  | "danger-outline";
/** lg/md：登录与首登页（圆角 10）；32/28：应用内（圆角 8），对应 Figma Button 的 Size 变体 */
export type ButtonSize = "lg" | "md" | "32" | "28";

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant;
  size?: ButtonSize;
  leading?: ReactNode;
  trailing?: ReactNode;
  loading?: boolean;
}

const sizeClass: Record<ButtonSize, string> = {
  lg: "h-10 px-4 rounded-lg text-body font-medium",
  md: "h-9 px-3 rounded-lg text-body font-medium",
  "32": "h-8 px-2.5 rounded-md text-body font-medium",
  "28": "h-7 px-2.5 rounded-md text-body-sm font-medium",
};

const variantClass: Record<ButtonVariant, string> = {
  primary:
    "bg-primary text-fg-inverse enabled:not-aria-disabled:hover:bg-primary/90",
  secondary:
    "bg-surface-muted text-fg-default enabled:not-aria-disabled:hover:bg-secondary-hover",
  outline:
    "bg-surface-card text-fg-default border border-line-default enabled:not-aria-disabled:hover:bg-surface-hover",
  ghost: "text-fg-secondary enabled:not-aria-disabled:hover:bg-control-hover",
  "ghost-muted":
    "text-fg-muted enabled:not-aria-disabled:hover:bg-control-hover",
  danger:
    "bg-danger text-fg-inverse enabled:not-aria-disabled:hover:bg-danger/90",
  "danger-outline":
    "bg-surface-card text-danger border border-danger-border enabled:not-aria-disabled:hover:bg-danger-soft",
};

/** 按钮。loading 时禁用并在文字前显示转圈。 */
export const Button = forwardRef<HTMLButtonElement, ButtonProps>(
  function Button(
    {
      variant = "primary",
      size = "32",
      leading,
      trailing,
      loading = false,
      disabled,
      className,
      children,
      type = "button",
      ...rest
    },
    ref,
  ) {
    const isDisabled = disabled || loading;
    return (
      <button
        ref={ref}
        type={type}
        disabled={isDisabled}
        aria-busy={loading || undefined}
        className={cn(
          "inline-flex shrink-0 items-center justify-center gap-1.5 whitespace-nowrap transition-colors duration-100 select-none motion-reduce:transition-none",
          "disabled:cursor-not-allowed disabled:opacity-50",
          variant === "primary" &&
            isDisabled &&
            size !== "32" &&
            size !== "28" &&
            "disabled:bg-primary-disabled disabled:opacity-100",
          sizeClass[size],
          variantClass[variant],
          className,
        )}
        {...rest}
      >
        {loading ? <Spinner /> : leading}
        {children}
        {trailing}
      </button>
    );
  },
);
