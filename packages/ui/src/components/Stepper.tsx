import { cn } from "../cn";
import { Icon } from "./Icon";
import { useT } from "@openfox/i18n";

/** 首登向导步骤条，current 从 1 开始。 */
export function Stepper({
  steps,
  current,
  className,
}: {
  steps: string[];
  current: number;
  className?: string;
}) {
  const t = useT();
  return (
    <ol
      className={cn("flex items-center gap-3", className)}
      aria-label={t("ui.stepper")}
    >
      {steps.map((label, index) => {
        const n = index + 1;
        const state = n < current ? "done" : n === current ? "current" : "todo";
        return (
          <li
            key={label}
            className="flex flex-1 items-center gap-3"
            aria-current={state === "current" ? "step" : undefined}
          >
            <span
              className={cn(
                "flex size-6 shrink-0 items-center justify-center rounded-full text-caption",
                state === "current" && "bg-primary text-fg-inverse",
                state === "done" && "bg-accent-soft text-accent",
                state === "todo" && "border border-line-default text-fg-muted",
              )}
            >
              {state === "done" ? <Icon name="check" size={14} /> : n}
            </span>
            <span
              className={cn(
                "text-caption whitespace-nowrap",
                state === "todo" ? "text-fg-muted" : "text-fg-default",
              )}
            >
              {label}
            </span>
            {n < steps.length && (
              <span aria-hidden className="h-px flex-1 bg-line-default" />
            )}
          </li>
        );
      })}
    </ol>
  );
}
