import { useState } from "react";

import { cn } from "@openfox/ui";

import { initialOf } from "./model";

/** 市场图标 / 头像：直接用 https 地址；没有地址或加载失败时显示首字母块。 */
export function MarketIcon({
  src,
  name,
  size = 40,
  round = false,
  className,
}: {
  src?: string | null;
  name: string;
  size?: number;
  round?: boolean;
  className?: string;
}) {
  const [failed, setFailed] = useState(false);
  const shape = round ? "rounded-full" : "rounded-lg";
  if (!src || failed)
    return (
      <span
        aria-hidden
        style={{ width: size, height: size }}
        className={cn(
          "flex shrink-0 items-center justify-center bg-surface-muted font-medium text-fg-secondary",
          shape,
          className,
        )}
      >
        {initialOf(name)}
      </span>
    );
  return (
    <img
      src={src}
      alt=""
      referrerPolicy="no-referrer"
      loading="lazy"
      width={size}
      height={size}
      style={{ width: size, height: size }}
      onError={() => setFailed(true)}
      className={cn("shrink-0 bg-surface-muted object-cover", shape, className)}
    />
  );
}
