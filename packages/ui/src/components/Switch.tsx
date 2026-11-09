import { forwardRef, type ButtonHTMLAttributes } from "react";

import { cn } from "../cn";

export interface SwitchProps extends Omit<
  ButtonHTMLAttributes<HTMLButtonElement>,
  "onChange"
> {
  checked: boolean;
  onCheckedChange?: (checked: boolean) => void;
  /** success：绿色（任务会话等）；default：黑色（设置页） */
  tone?: "default" | "success";
  /** 无可见文字时必填 */
  label?: string;
}

/** 32×18 开关，支持选中、禁用和色调状态。 */
export const Switch = forwardRef<HTMLButtonElement, SwitchProps>(
  function Switch(
    {
      checked,
      onCheckedChange,
      tone = "default",
      label,
      disabled,
      className,
      ...rest
    },
    ref,
  ) {
    return (
      <button
        ref={ref}
        type="button"
        role="switch"
        aria-checked={checked}
        aria-label={label}
        disabled={disabled}
        onClick={() => onCheckedChange?.(!checked)}
        className={cn(
          "relative inline-flex h-[18px] w-8 shrink-0 cursor-pointer rounded-[9px] transition-colors",
          checked
            ? tone === "success"
              ? "bg-success"
              : "bg-primary"
            : tone === "success"
              ? "bg-line-strong"
              : "bg-line-default",
          "disabled:cursor-not-allowed disabled:opacity-50",
          className,
        )}
        {...rest}
      >
        <span
          aria-hidden
          className={cn(
            "absolute top-0.5 size-3.5 rounded-full bg-white shadow-card transition-[left]",
            checked ? "left-4" : "left-0.5",
          )}
        />
      </button>
    );
  },
);
