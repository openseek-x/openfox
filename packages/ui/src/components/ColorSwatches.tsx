import { cn } from "../cn";
import { Icon } from "./Icon";

/** 标签颜色选项。 */
export const LABEL_COLORS = [
  "#16A34A",
  "#7C3AED",
  "#0284C7",
  "#C026D3",
  "#EA580C",
  "#CA8A04",
  "#DB2777",
  "#DC2626",
  "#0D9488",
  "#2563EB",
  "#92400E",
] as const;

/** 自定义字段选项颜色 */
export const OPTION_COLORS = [
  "#00dd44",
  "#8066f0",
  "#00baed",
  "#e94ff8",
  "#ff9800",
  "#ffd600",
  "#ff3881",
  "#ff7567",
  "#00ddb3",
  "#33bbff",
  "#bd9576",
] as const;

/** 状态生命周期类别的默认颜色；TRIAGE 使用橙色。 */
export const STATUS_LIFECYCLE_COLORS = {
  TRIAGE: "#e5703a",
  BACKLOG: "#bec2c8",
  TODO: "#e2e2e2",
  IN_PROGRESS: "#f0bf00",
  DONE: "#4cb782",
  CANCELLED: "#5e6ad2",
  DUPLICATE: "#95a2b3",
} as const;

export interface ColorSwatchesProps {
  colors: readonly string[];
  value?: string | null;
  onChange: (color: string) => void;
  /** 无障碍名称，如「标签颜色」 */
  label: string;
  className?: string;
}

/** 色板：单选，选中项显示白色对勾。 */
export function ColorSwatches({
  colors,
  value,
  onChange,
  label,
  className,
}: ColorSwatchesProps) {
  const current = value?.toLowerCase();
  return (
    <div
      role="radiogroup"
      aria-label={label}
      className={cn("flex flex-wrap gap-2", className)}
    >
      {colors.map((color) => {
        const checked = color.toLowerCase() === current;
        return (
          <button
            key={color}
            type="button"
            role="radio"
            aria-checked={checked}
            aria-label={color}
            title={color}
            onClick={() => onChange(color)}
            style={{ backgroundColor: color }}
            className="flex size-6 items-center justify-center rounded-full text-white"
          >
            {checked && <Icon name="check" size={14} />}
          </button>
        );
      })}
    </div>
  );
}
