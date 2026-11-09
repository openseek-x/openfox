import type { ReactNode } from "react";

import { cn } from "../cn";
import { Icon, type IconName } from "./Icon";

export type CalloutTone = "info" | "warning" | "danger" | "success";

const toneClass: Record<
  CalloutTone,
  { box: string; icon: IconName; iconClass: string }
> = {
  info: { box: "bg-info-soft", icon: "info", iconClass: "text-info" },
  warning: {
    box: "bg-warning-soft",
    icon: "alert-triangle",
    iconClass: "text-warning",
  },
  danger: { box: "bg-danger-soft", icon: "x-circle", iconClass: "text-danger" },
  success: {
    box: "bg-accent-soft",
    icon: "check-circle",
    iconClass: "text-accent",
  },
};

/** 圆角 14 的提示条。 */
export function Callout({
  tone = "info",
  title,
  children,
  action,
  className,
}: {
  tone?: CalloutTone;
  title?: ReactNode;
  children?: ReactNode;
  action?: ReactNode;
  className?: string;
}) {
  const t = toneClass[tone];
  return (
    <div
      role={tone === "danger" ? "alert" : "status"}
      className={cn(
        "flex items-start gap-2.5 rounded-xl px-3.5 py-3 text-body",
        t.box,
        className,
      )}
    >
      <Icon name={t.icon} className={cn("mt-0.5", t.iconClass)} />
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        {title && <div className="font-medium text-fg-default">{title}</div>}
        {children && <div className="text-fg-secondary">{children}</div>}
      </div>
      {action}
    </div>
  );
}
