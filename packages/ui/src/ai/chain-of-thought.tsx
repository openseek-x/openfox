import type { ComponentProps, ReactNode } from "react";
import { cn } from "../cn";
import { Icon } from "../components/Icon";

export function ChainOfThought({ className, ...props }: ComponentProps<"div">) {
  return (
    <div
      {...props}
      data-slot="chain-of-thought"
      className={cn("flex min-w-0 flex-col gap-2", className)}
    />
  );
}

export function ChainOfThoughtContent({
  className,
  ...props
}: ComponentProps<"div">) {
  return (
    <div
      {...props}
      data-slot="chain-of-thought-content"
      className={cn(
        "ml-2 flex min-w-0 flex-col gap-2 border-l border-line-default py-1 pl-3",
        className,
      )}
    />
  );
}

export function ChainOfThoughtTrigger({
  expanded,
  icon,
  className,
  children,
  ...props
}: ComponentProps<"button"> & { expanded: boolean; icon?: ReactNode }) {
  return (
    <button
      {...props}
      type="button"
      aria-expanded={expanded}
      className={cn(
        "flex min-h-8 max-w-full items-center gap-2 self-start rounded-md px-1 text-left text-body-sm transition-colors hover:bg-surface-hover focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent",
        className,
      )}
    >
      {icon}
      <span className="min-w-0">{children}</span>
      <Icon
        name={expanded ? "chevron-down" : "chevron-right"}
        size={14}
        className="shrink-0"
      />
    </button>
  );
}
