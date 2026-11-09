import { useT } from "@openfox/i18n";

import { cn } from "../cn";

/** 加载中转圈，尺寸与图标一致。 */
export function Spinner({
  size = 16,
  className,
  label,
}: {
  size?: number;
  className?: string;
  label?: string;
}) {
  const t = useT();
  return (
    <svg
      viewBox="0 0 16 16"
      width={size}
      height={size}
      role="status"
      aria-label={label ?? t("common.loading")}
      className={cn("animate-spin shrink-0", className)}
    >
      <circle
        cx="8"
        cy="8"
        r="6"
        fill="none"
        stroke="currentColor"
        strokeOpacity="0.25"
        strokeWidth="1.5"
      />
      <path
        d="M14 8a6 6 0 0 0-6-6"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.5"
        strokeLinecap="round"
      />
    </svg>
  );
}
