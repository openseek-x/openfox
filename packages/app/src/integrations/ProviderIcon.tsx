import { cn } from "@openfox/ui";

import { initialOf } from "./model";

/** 服务商图标：图标集里没有第三方品牌图标，统一用首字母方块。 */
export function ProviderIcon({
  name,
  size = 32,
  className,
}: {
  name: string;
  size?: number;
  className?: string;
}) {
  return (
    <span
      aria-hidden
      style={{ width: size, height: size }}
      className={cn(
        "flex shrink-0 items-center justify-center rounded-lg bg-surface-muted font-medium text-fg-secondary",
        className,
      )}
    >
      {initialOf(name)}
    </span>
  );
}
