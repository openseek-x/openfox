import type { SVGProps } from "react";

import { cn } from "../cn";
import { iconData, multicolorIcons, type IconName } from "../icons/data";

export type { IconName };

export interface IconProps extends Omit<SVGProps<SVGSVGElement>, "name"> {
  name: IconName;
  /** 边长，默认 16px。 */
  size?: number;
  /** 提供时作为无障碍名称；不提供则视为装饰图标 */
  label?: string;
}

/**
 * 单色图标随文字颜色（currentColor），品牌、运行时和系统图标保留原色。
 * 图形数据是静态 SVG 片段。
 */
export function Icon({
  name,
  size = 16,
  label,
  className,
  ...rest
}: IconProps) {
  const icon = iconData[name];
  return (
    <svg
      viewBox={icon.viewBox}
      width={size}
      height={size}
      fill="none"
      role={label ? "img" : undefined}
      aria-label={label}
      aria-hidden={label ? undefined : true}
      focusable="false"
      className={cn(
        "shrink-0",
        multicolorIcons.has(name) ? undefined : "text-current",
        className,
      )}
      dangerouslySetInnerHTML={{ __html: icon.body }}
      {...rest}
    />
  );
}
