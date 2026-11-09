import type { ComponentProps } from "react";
import { cn } from "../cn";

export function CodeBlock({ className, ...props }: ComponentProps<"div">) {
  return (
    <div
      {...props}
      data-slot="code-block"
      className={cn(
        "flex w-full min-w-0 flex-col overflow-hidden rounded-xl border border-line-default bg-surface-muted text-fg-default",
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
        "flex min-w-0 items-center justify-between gap-2 border-b border-line-default px-3 py-1 text-caption text-fg-muted",
        className,
      )}
    />
  );
}
