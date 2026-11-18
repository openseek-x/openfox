import type { Iteration } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Badge, type BadgeTone } from "@openfox/ui";

const TONE: Record<Iteration["status"], BadgeTone> = {
  planned: "neutral",
  active: "success",
  completed: "info",
  canceled: "muted",
};

const STATUS_KEY = {
  planned: "spaces.iteration.statusPlanned",
  active: "spaces.iteration.statusActive",
  completed: "spaces.iteration.statusCompleted",
  canceled: "spaces.iteration.statusCanceled",
} as const;

export function IterationStatusBadge({
  status,
}: {
  status: Iteration["status"];
}) {
  const t = useT();
  return <Badge tone={TONE[status]}>{t(STATUS_KEY[status])}</Badge>;
}

/** 空间时区的今天（YYYY-MM-DD）；en-CA 的日期格式恰好是 ISO。 */
export function todayIn(timeZone: string): string {
  return new Intl.DateTimeFormat("en-CA", { timeZone }).format(new Date());
}

/** 已完成 / 取消的迭代用定格计数，其余用实时计数。 */
export function iterationCounts(iteration: Iteration) {
  const frozen =
    iteration.status === "completed" || iteration.status === "canceled";
  return {
    scope:
      (frozen ? iteration.scope_count : null) ?? iteration.live.scope_count,
    done:
      (frozen ? iteration.completed_count : null) ??
      iteration.live.completed_count,
    points:
      (frozen ? iteration.scope_points : null) ?? iteration.live.scope_points,
  };
}
