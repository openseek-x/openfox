import { forwardRef, type ReactNode, type TextareaHTMLAttributes } from "react";

import { cn } from "../cn";
import { useFieldContext } from "./Field";

export interface TextareaProps extends TextareaHTMLAttributes<HTMLTextAreaElement> {
  invalid?: boolean;
  /** 右上角操作，比如「展开编辑」 */
  action?: ReactNode;
}

/** 多行输入：圆角 10，内边距 10/12。 */
export const Textarea = forwardRef<HTMLTextAreaElement, TextareaProps>(
  function Textarea(
    { invalid, action, className, id, rows = 3, ...rest },
    ref,
  ) {
    const field = useFieldContext();
    const isInvalid = invalid ?? field?.invalid ?? false;
    return (
      <div
        className={cn(
          "of-control flex min-w-0 gap-2 rounded-lg border bg-surface-card py-2.5 pr-2.5 pl-3 text-body transition-colors focus-within:border-line-focus",
          isInvalid ? "border-danger" : "border-line-default",
          "has-[:disabled]:opacity-50",
          className,
        )}
      >
        <textarea
          ref={ref}
          id={id ?? field?.inputId}
          rows={rows}
          aria-invalid={isInvalid || undefined}
          aria-describedby={field?.describedBy}
          className="min-h-0 min-w-0 flex-1 resize-none bg-transparent outline-none placeholder:text-fg-subtle"
          {...rest}
        />
        {action && (
          <span className="flex self-start text-fg-muted">{action}</span>
        )}
      </div>
    );
  },
);
