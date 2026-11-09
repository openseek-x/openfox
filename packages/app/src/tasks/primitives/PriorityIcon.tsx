import { useT } from "@openfox/i18n";
import { cn } from "@openfox/ui";

export type Priority = "NONE" | "LOWEST" | "LOW" | "NORMAL" | "HIGH" | "URGENT";

export const PRIORITIES: Priority[] = [
  "NONE",
  "LOWEST",
  "LOW",
  "NORMAL",
  "HIGH",
  "URGENT",
];

/** 信号条的数量：最低 0（三格都是灰色）、低 1、中 2、高 3。 */
const BARS: Partial<Record<Priority, number>> = {
  LOWEST: 0,
  LOW: 1,
  NORMAL: 2,
  HIGH: 3,
};

export interface PriorityIconProps {
  priority: Priority;
  size?: number;
  /** 为 true 时作为图片暴露优先级名称，否则视为装饰 */
  labelled?: boolean;
  className?: string;
}

/**
 * 优先级图标：紧急为红底感叹号，
 * 低 / 中 / 高为 1 / 2 / 3 格信号条，无为三个点。NORMAL 使用中优先级图标。
 */
export function PriorityIcon({
  priority,
  size = 16,
  labelled = false,
  className,
}: PriorityIconProps) {
  const t = useT();
  const label = t(`tasks.priority.${priority}`);
  const bars = BARS[priority];
  return (
    <svg
      viewBox="0 0 16 16"
      width={size}
      height={size}
      role={labelled ? "img" : undefined}
      aria-label={labelled ? label : undefined}
      aria-hidden={labelled ? undefined : true}
      data-priority={priority}
      focusable="false"
      className={cn("shrink-0", className)}
    >
      {priority === "URGENT" && (
        <>
          <rect
            x="1"
            y="1"
            width="14"
            height="14"
            rx="3"
            className="fill-danger"
          />
          <rect
            x="7.25"
            y="3.75"
            width="1.5"
            height="5.5"
            rx="0.75"
            fill="#fff"
          />
          <circle cx="8" cy="11.5" r="0.9" fill="#fff" />
        </>
      )}
      {priority === "NONE" && (
        <g className="fill-fg-subtle">
          <circle cx="3.5" cy="8" r="1.1" />
          <circle cx="8" cy="8" r="1.1" />
          <circle cx="12.5" cy="8" r="1.1" />
        </g>
      )}
      {bars !== undefined &&
        [0, 1, 2].map((i) => (
          <rect
            key={i}
            x={2 + i * 4.5}
            y={10 - i * 3}
            width="3"
            height={4 + i * 3}
            rx="0.8"
            className={i < bars ? "fill-info" : "fill-line-strong"}
          />
        ))}
    </svg>
  );
}
