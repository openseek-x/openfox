import type { ReactNode } from "react";
import { cn, Icon } from "@openfox/ui";

export function TriageIcon({ size = 16 }: { size?: number }) {
  return <Icon name="hifox-arrow-left-right" size={size} />;
}
export function QueueIllustration() {
  return <Icon name="layers" size={96} />;
}
export function TriageState({
  media,
  title,
  description,
  notice,
  action,
  center = false,
}: {
  media: ReactNode;
  title: string;
  description: string;
  notice?: string;
  action?: ReactNode;
  center?: boolean;
}) {
  return (
    <div className="flex h-full min-h-0 flex-1 items-center justify-center overflow-y-auto px-8 py-6">
      <div
        className={cn(
          "flex w-full max-w-md flex-col items-start",
          center && "items-center text-center",
        )}
      >
        <span className="mb-5 text-fg-muted">{media}</span>
        <h1 className="text-base font-semibold">{title}</h1>
        <p className="mt-2 text-body leading-relaxed text-fg-muted">
          {description}
        </p>
        {notice && <p className="mt-3 text-body font-medium">{notice}</p>}
        {action && <div className="mt-4">{action}</div>}
      </div>
    </div>
  );
}
