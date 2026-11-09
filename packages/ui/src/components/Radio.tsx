import { forwardRef, type InputHTMLAttributes } from "react";

import { cn } from "../cn";

/** 16px 单选按钮。 */
export const Radio = forwardRef<
  HTMLInputElement,
  Omit<InputHTMLAttributes<HTMLInputElement>, "type">
>(function Radio({ className, ...rest }, ref) {
  return (
    <input
      ref={ref}
      type="radio"
      className={cn(
        "size-4 shrink-0 cursor-pointer appearance-none rounded-full border border-line-default bg-surface-card",
        "checked:border-[5px] checked:border-primary",
        "disabled:cursor-not-allowed disabled:opacity-50",
        className,
      )}
      {...rest}
    />
  );
});
