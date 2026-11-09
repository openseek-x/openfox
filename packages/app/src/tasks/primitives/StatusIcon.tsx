import { useT } from "@openfox/i18n";
import { STATUS_LIFECYCLE_COLORS, cn } from "@openfox/ui";

export type StatusCategory = keyof typeof STATUS_LIFECYCLE_COLORS;

export const STATUS_CATEGORIES = Object.keys(
  STATUS_LIFECYCLE_COLORS,
) as StatusCategory[];

export interface StatusIconProps {
  category: StatusCategory;
  /** 状态自带颜色；缺省用类别默认色 */
  color?: string | null;
  size?: number;
  /** 为 true 时作为图片暴露类别名称，否则视为装饰 */
  labelled?: boolean;
  className?: string;
}

/** 状态图标按类别画 7 种形状。 */
export function StatusIcon({
  category,
  color,
  size = 16,
  labelled = false,
  className,
}: StatusIconProps) {
  const t = useT();
  const c = color || STATUS_LIFECYCLE_COLORS[category];
  const label = t(`tasks.statusCategory.${category}`);
  return (
    <svg
      viewBox="0 0 16 16"
      width={size}
      height={size}
      fill="none"
      role={labelled ? "img" : undefined}
      aria-label={labelled ? label : undefined}
      aria-hidden={labelled ? undefined : true}
      data-category={category}
      focusable="false"
      className={cn("shrink-0", className)}
    >
      {shape(category, c)}
    </svg>
  );
}

function shape(category: StatusCategory, c: string) {
  switch (category) {
    case "TRIAGE":
      // 实心圆 + 上下两个箭头
      return (
        <>
          <circle cx="8" cy="8" r="7" fill={c} />
          <path
            d="M5.2 6.6h5.6M9.2 5l1.6 1.6-1.6 1.6M10.8 9.4H5.2M6.8 7.8 5.2 9.4l1.6 1.6"
            stroke="#fff"
            strokeWidth="1.2"
            strokeLinecap="round"
            strokeLinejoin="round"
          />
        </>
      );
    case "BACKLOG":
      return (
        <circle
          cx="8"
          cy="8"
          r="6.25"
          stroke={c}
          strokeWidth="1.5"
          strokeDasharray="1.9 1.9"
        />
      );
    case "TODO":
      return <circle cx="8" cy="8" r="6.25" stroke={c} strokeWidth="1.5" />;
    case "IN_PROGRESS":
      // 圆环 + 右半圆填充
      return (
        <>
          <circle cx="8" cy="8" r="6.25" stroke={c} strokeWidth="1.5" />
          <path d="M8 4.5a3.5 3.5 0 0 1 0 7z" fill={c} />
        </>
      );
    case "DONE":
      return (
        <>
          <circle cx="8" cy="8" r="7" fill={c} />
          <path
            d="m5 8.2 2 2L11 6"
            stroke="#fff"
            strokeWidth="1.5"
            strokeLinecap="round"
            strokeLinejoin="round"
          />
        </>
      );
    case "CANCELLED":
      return (
        <>
          <circle cx="8" cy="8" r="7" fill={c} />
          <path
            d="m5.6 5.6 4.8 4.8M10.4 5.6l-4.8 4.8"
            stroke="#fff"
            strokeWidth="1.5"
            strokeLinecap="round"
          />
        </>
      );
    case "DUPLICATE":
      return (
        <>
          <circle cx="8" cy="8" r="7" fill={c} />
          <rect
            x="5.2"
            y="5.2"
            width="4"
            height="4"
            rx="0.8"
            stroke="#fff"
            strokeWidth="1.2"
          />
          <path
            d="M7.2 10.8h2.6a1 1 0 0 0 1-1V7.2"
            stroke="#fff"
            strokeWidth="1.2"
            strokeLinecap="round"
          />
        </>
      );
  }
}
