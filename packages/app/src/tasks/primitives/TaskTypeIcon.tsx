import { Icon, iconData, type IconName } from "@openfox/ui";

export const DEFAULT_TASK_TYPE_ICON: IconName = "square-check";

/** task_types.icon 的名字不在图标库里时回退到默认图标。 */
export function resolveTaskTypeIcon(icon: string | null | undefined): IconName {
  return icon && Object.hasOwn(iconData, icon)
    ? (icon as IconName)
    : DEFAULT_TASK_TYPE_ICON;
}

export function TaskTypeIcon({
  icon,
  color,
  size = 16,
  label,
  className,
}: {
  icon?: string | null;
  color?: string | null;
  size?: number;
  /** 提供时作为无障碍名称，否则视为装饰 */
  label?: string;
  className?: string;
}) {
  return (
    <Icon
      name={resolveTaskTypeIcon(icon)}
      size={size}
      label={label}
      className={className}
      style={color ? { color } : undefined}
    />
  );
}
