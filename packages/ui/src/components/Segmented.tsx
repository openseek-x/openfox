import type { ReactNode } from "react";

import { cn } from "../cn";

export interface SegmentedOption<T extends string> {
  value: T;
  label: ReactNode;
  icon?: ReactNode;
}

/** 分段切换，胶囊底 + 选中黑色块（Figma Segmented/AgentType）。 */
export function Segmented<T extends string>({
  value,
  onChange,
  options,
  label,
  className,
  equalWidth = false,
}: {
  value: T;
  onChange: (value: T) => void;
  options: ReadonlyArray<SegmentedOption<T>>;
  label: string;
  className?: string;
  /** 让各个选项保持相同宽度。 */
  equalWidth?: boolean;
}) {
  return (
    <div
      role="radiogroup"
      aria-label={label}
      className={cn(
        "h-9 items-center rounded-full bg-surface-muted p-[3px]",
        equalWidth
          ? "inline-grid auto-cols-fr grid-flow-col"
          : "inline-flex gap-0.5",
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
              "flex h-full flex-1 items-center justify-center gap-1.5 rounded-full px-3 text-body-sm font-medium whitespace-nowrap transition-colors motion-reduce:transition-none",
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
