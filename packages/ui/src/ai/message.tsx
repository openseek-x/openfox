import type { ComponentProps } from "react";
import { cn } from "../cn";

export function Message({ className, ...props }: ComponentProps<"div">) {
  return (
    <div
      {...props}
      data-slot="message"
      className={cn("group/message flex min-w-0 flex-col gap-2", className)}
    />
  );
}

export function MessageContent({
  variant = "user",
  className,
  ...props
}: ComponentProps<"div"> & { variant?: "user" | "assistant" }) {
  return (
    <div
      {...props}
      data-slot="message-content"
      className={cn(
        "min-w-0 text-body leading-6 [overflow-wrap:anywhere]",
        variant === "user"
          ? "max-w-[85%] rounded-2xl bg-surface-muted px-4 py-3 whitespace-pre-wrap"
          : "w-full",
        className,
      )}
    />
  );
}

export function MessageActions({ className, ...props }: ComponentProps<"div">) {
  return (
    <div
      {...props}
      data-slot="message-actions"
      className={cn(
        "flex min-h-7 flex-wrap items-center gap-2 text-caption text-fg-muted",
        className,
      )}
    />
  );
}
