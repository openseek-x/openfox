import type { ComponentProps } from "react";
import { cn } from "../cn";

// 代码块外框：使用独立的 surface-code / line-code 令牌，和行内代码、普通卡片区分开。
export function CodeBlock({ className, ...props }: ComponentProps<"div">) {
  return (
    <div
      {...props}
      data-slot="code-block"
      className={cn(
        "relative flex w-full min-w-0 flex-col overflow-hidden rounded-lg border border-line-code bg-surface-code text-fg-default",
        className,
      )}
    />
  );
}

export function CodeBlockGroup({ className, ...props }: ComponentProps<"div">) {
  return (
    <div
      {...props}
      className={cn(
        "flex h-9 min-w-0 items-center justify-between gap-2 border-b border-line-code pr-1.5 pl-3 text-caption text-fg-muted",
        className,
      )}
    />
  );
}
