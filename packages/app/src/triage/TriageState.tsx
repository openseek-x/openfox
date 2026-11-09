import type { ReactNode } from "react";
import { cn } from "@openfox/ui";

export function TriageIcon({ size = 16 }: { size?: number }) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.35"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <circle cx="8" cy="8" r="6.5" />
      <path d="M4 8h8M6 6 4 8l2 2m4-4 2 2-2 2" />
    </svg>
  );
}
export function QueueIllustration() {
  return (
    <svg
      aria-hidden="true"
      width="132"
      height="84"
      viewBox="0 0 132 84"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.4"
      strokeLinejoin="round"
    >
      {[
        [0, 12, 0.65],
        [66, 2, 1],
        [33, 44, 0.25],
      ].map(([x, y, a]) => (
        <g key={x} transform={`translate(${x} ${y})`} opacity={a}>
          <path d="m3 15 3 1 1 12 22 4q6 1 4 4l-2 1-27-4q-2 0-2-3L1 17q0-2 2-2Zm26-13 29 4q3 1 3 3l1 14q-1 3-5 1l-2-1-1-12-24-4q-4-1-3-4ZM22 22l13-6q3-1 6 1l1 2-15 7q-3 1-6-1Z" />
        </g>
      ))}
    </svg>
  );
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
