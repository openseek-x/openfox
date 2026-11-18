import type { SVGProps } from "react";

import { cn } from "../cn";
import { iconData, type IconName } from "../icons/data";

export type { IconName };

export interface IconProps extends Omit<SVGProps<SVGSVGElement>, "name"> {
  name: IconName;
  /** 边长，默认 16（设计稿图标统一 16px） */
  size?: number;
  /** 提供时作为无障碍名称；不提供则视为装饰图标 */
  label?: string;
}

/** Static Lucide glyphs and plain-text service labels. */
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
      className={cn("shrink-0 text-current", className)}
      dangerouslySetInnerHTML={{ __html: icon.body }}
      {...rest}
    />
  );
}
