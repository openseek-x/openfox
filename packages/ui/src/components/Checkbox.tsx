import { forwardRef, useEffect, useRef, type InputHTMLAttributes } from "react";

import { cn } from "../cn";

export interface CheckboxProps extends Omit<
  InputHTMLAttributes<HTMLInputElement>,
  "type" | "size"
> {
  indeterminate?: boolean;
}

/** 复选框，18px、圆角 5，选中时使用强调绿。 */
export const Checkbox = forwardRef<HTMLInputElement, CheckboxProps>(
  function Checkbox({ indeterminate = false, className, ...rest }, forwarded) {
    const local = useRef<HTMLInputElement | null>(null);
    useEffect(() => {
      if (local.current) local.current.indeterminate = indeterminate;
    }, [indeterminate]);
    return (
      <input
        ref={(node) => {
          local.current = node;
          if (typeof forwarded === "function") forwarded(node);
          else if (forwarded) forwarded.current = node;
        }}
        type="checkbox"
        className={cn(
          "size-[18px] shrink-0 cursor-pointer appearance-none rounded-[5px] border-[1.5px] border-line-strong bg-surface-card",
          "bg-center bg-no-repeat checked:border-accent checked:bg-accent indeterminate:border-accent indeterminate:bg-accent",
          "checked:bg-[url('data:image/svg+xml;utf8,<svg xmlns=%22http://www.w3.org/2000/svg%22 viewBox=%220 0 12 12%22><path d=%22M2.5 6.2 5 8.5l4.5-5%22 fill=%22none%22 stroke=%22white%22 stroke-width=%221.6%22 stroke-linecap=%22round%22 stroke-linejoin=%22round%22/></svg>')]",
          "indeterminate:bg-[url('data:image/svg+xml;utf8,<svg xmlns=%22http://www.w3.org/2000/svg%22 viewBox=%220 0 12 12%22><path d=%22M3 6h6%22 stroke=%22white%22 stroke-width=%221.6%22 stroke-linecap=%22round%22/></svg>')]",
          "disabled:cursor-not-allowed disabled:opacity-50",
          className,
        )}
        {...rest}
      />
    );
  },
);
