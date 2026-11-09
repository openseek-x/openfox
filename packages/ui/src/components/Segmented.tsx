import type { ReactNode } from "react";

import { cn } from "../cn";

export interface SegmentedOption<T extends string> {
  value: T;
  label: ReactNode;
  icon?: ReactNode;
}

/** 分段切换：胶囊背景与选中态。 */
export function Segmented<T extends string>({
  value,
  onChange,
  options,
  label,
  className,
}: {
  value: T;
  onChange: (value: T) => void;
  options: ReadonlyArray<SegmentedOption<T>>;
  label: string;
  className?: string;
}) {
  return (
    <div
      role="radiogroup"
      aria-label={label}
      className={cn(
        "inline-flex h-9 items-center gap-0.5 rounded-full bg-surface-muted p-[3px]",
        className,
      )}
    >
      {options.map((option) => {
        const selected = option.value === value;
        return (
          <button
            key={option.value}
            type="button"
            role="radio"
            aria-checked={selected}
            onClick={() => onChange(option.value)}
            className={cn(
              "flex h-full flex-1 items-center justify-center gap-1.5 rounded-full px-3 text-body-sm font-medium whitespace-nowrap transition-colors",
              selected
                ? "bg-primary text-fg-inverse"
                : "text-fg-secondary hover:text-fg-default",
            )}
          >
            {option.icon}
            {option.label}
          </button>
        );
      })}
    </div>
  );
}
