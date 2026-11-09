import { cn } from "../cn";

/** 骨架占位块。 */
export function Skeleton({ className }: { className?: string }) {
  return (
    <div
      aria-hidden
      className={cn("animate-pulse rounded-md bg-surface-muted", className)}
    />
  );
}
