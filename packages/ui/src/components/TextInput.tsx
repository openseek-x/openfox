import { forwardRef, type InputHTMLAttributes, type ReactNode } from "react";

import { cn } from "../cn";
import { useFieldContext } from "./Field";

/** labeled：登录/表单弹窗（高 36，圆角 10）；form：自动化等表单（高 32，圆角 10）；field：设置页（高 32，圆角 8） */
export type ControlStyle = "labeled" | "form" | "field";

export const controlFrameClass: Record<ControlStyle, string> = {
  labeled: "h-9 rounded-lg px-3",
  form: "h-8 rounded-lg px-2.5",
  field: "h-8 rounded-md px-2.5",
};

/** 输入框外框的通用样式：描边、聚焦、错误、禁用。 */
export function controlShellClass(
  variant: ControlStyle,
  invalid: boolean,
): string {
  return cn(
    "of-control flex min-w-0 items-center gap-2 border bg-surface-card text-body text-fg-default transition-colors",
    controlFrameClass[variant],
    invalid ? "border-danger" : "border-line-default",
    "focus-within:border-line-focus",
    variant === "labeled" && "focus-within:shadow-focus",
    "has-[:disabled]:cursor-not-allowed has-[:disabled]:opacity-50",
  );
}

export interface TextInputProps extends Omit<
  InputHTMLAttributes<HTMLInputElement>,
  "size"
> {
  variant?: ControlStyle;
  leading?: ReactNode;
  trailing?: ReactNode;
  invalid?: boolean;
}

/** 单行输入框，支持标签、表单和独立字段样式；放在 Field 里时自动关联标签。 */
export const TextInput = forwardRef<HTMLInputElement, TextInputProps>(
  function TextInput(
    { variant = "field", leading, trailing, invalid, className, id, ...rest },
    ref,
  ) {
    const field = useFieldContext();
    const isInvalid = invalid ?? field?.invalid ?? false;
    return (
      <div className={cn(controlShellClass(variant, isInvalid), className)}>
        {leading && <span className="flex text-fg-muted">{leading}</span>}
        <input
          ref={ref}
          id={id ?? field?.inputId}
          aria-invalid={isInvalid || undefined}
          aria-describedby={field?.describedBy}
          className="min-w-0 flex-1 bg-transparent outline-none placeholder:text-fg-subtle disabled:cursor-not-allowed"
          {...rest}
        />
        {trailing && <span className="flex text-fg-muted">{trailing}</span>}
      </div>
    );
  },
);
