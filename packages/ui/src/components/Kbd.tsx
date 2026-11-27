import type { ReactNode } from "react";

import { cn } from "../cn";

/** 键盘快捷键提示，如 ⌘ K。 */
export function Kbd({
  children,
  className,
}: {
  children: ReactNode;
  className?: string;
}) {
  return (
    <kbd
      className={cn(
        "inline-flex h-5 min-w-5 items-center justify-center rounded-sm border border-line-soft bg-surface-card/90 px-1.5 font-sans text-caption text-fg-muted shadow-card",
        className,
      )}
    >
      {children}
    </kbd>
  );
}
