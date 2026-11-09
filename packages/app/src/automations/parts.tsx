import type {
  AutomationRunStatus,
  AutomationScheduleConfig,
  AutomationTrigger,
  AutomationTriggerKind,
  TaskStatus,
  WebhookDelivery,
} from "@openfox/api";
import type { MessageKey, Translate } from "@openfox/i18n";
import { Icon, type IconName, cn } from "@openfox/ui";

// 自动化页面共用的展示部件与格式化函数。

/** 触发方式下拉的固定顺序。 */
export const TRIGGER_KIND_ORDER: AutomationTriggerKind[] = [
  "schedule",
  "task_status",
  "comment",
  "webhook",
];

const RUN_STATUS_VIEW: Record<
  AutomationRunStatus,
  { icon: IconName; tone: string; label: MessageKey }
> = {
  queued: {
    icon: "loader-circle",
    tone: "text-info",
    label: "automations.runStatus.queued",
  },
  running: {
    icon: "loader-circle",
    tone: "text-info",
    label: "automations.runStatus.running",
  },
  completed: {
    icon: "circle-check",
    tone: "text-success",
    label: "automations.runStatus.completed",
  },
  failed: {
    icon: "circle-x",
    tone: "text-danger",
    label: "automations.runStatus.failed",
  },
  canceled: {
    icon: "circle-slash",
    tone: "text-fg-muted",
    label: "automations.runStatus.canceled",
  },
  skipped: {
    icon: "skip-forward",
    tone: "text-warning",
    label: "automations.runStatus.skipped",
  },
};

export function RunStatusIcon({
  status,
  className,
}: {
  status: AutomationRunStatus;
  className?: string;
}) {
  const view = RUN_STATUS_VIEW[status];
  return (
    <Icon
      name={view.icon}
      size={14}
      className={cn(
        view.tone,
        status === "running" && "animate-spin",
        className,
      )}
    />
  );
}

export function runStatusLabel(
  t: Translate,
  status: AutomationRunStatus,
): string {
  return t(RUN_STATUS_VIEW[status].label);
}

const DELIVERY_STATUS_VIEW: Record<
  WebhookDelivery["status"],
  { icon: IconName; tone: string; label: MessageKey }
> = {
  queued: {
    icon: "loader-circle",
    tone: "text-info",
    label: "automations.deliveryStatus.queued",
  },
  dispatched: {
    icon: "circle-check",
    tone: "text-success",
    label: "automations.deliveryStatus.dispatched",
  },
  rejected: {
    icon: "circle-x",
    tone: "text-danger",
    label: "automations.deliveryStatus.rejected",
  },
  ignored: {
    icon: "circle-slash",
    tone: "text-fg-muted",
    label: "automations.deliveryStatus.ignored",
  },
  failed: {
    icon: "alert-triangle",
    tone: "text-danger",
    label: "automations.deliveryStatus.failed",
  },
};

export function DeliveryStatusIcon({
  status,
}: {
  status: WebhookDelivery["status"];
}) {
  const view = DELIVERY_STATUS_VIEW[status];
  return <Icon name={view.icon} size={14} className={view.tone} />;
}

export function deliveryStatusLabel(
  t: Translate,
  status: WebhookDelivery["status"],
): string {
  return t(DELIVERY_STATUS_VIEW[status].label);
}

const TRIGGER_ICON: Record<AutomationTriggerKind, IconName> = {
  schedule: "clock",
  task_status: "circle-dot",
  comment: "message-circle",
  webhook: "webhook",
};

export function triggerIcon(kind: AutomationTriggerKind): IconName {
  return TRIGGER_ICON[kind];
}

/** 时间 HH:mm 按 24 小时制展示；星期用于「按星期」摘要。 */
export function scheduleText(
  t: Translate,
  schedule: AutomationScheduleConfig,
): string {
  switch (schedule.frequency) {
    case "hourly":
      return t("automations.schedule.summaryHourly", {
        minute: schedule.minute ?? 0,
      });
    case "daily":
      return t("automations.schedule.summaryDaily", {
        time: schedule.time ?? "",
      });
    case "weekdays":
      return t("automations.schedule.summaryWeekdays", {
        time: schedule.time ?? "",
      });
    case "weekly":
      return t("automations.schedule.summaryWeekly", {
        days: (schedule.weekdays ?? [])
          .map((day) => t(`automations.schedule.dayShort.${day}` as MessageKey))
          .join("、"),
        time: schedule.time ?? "",
      });
    case "custom":
      return t("automations.schedule.summaryCustom", {
        cron: schedule.cron ?? "",
      });
  }
}

/** 状态摘要最多列出的状态名个数，超出时显示「等 N 个」。 */
export const STATUS_SUMMARY_LIMIT = 3;

/** 摘要用到的状态字段；与编辑弹窗同一个组织状态列表（useStatuses）。 */
export type SummaryStatus = Pick<TaskStatus, "id" | "name">;

/**
 * 任务状态触发器的状态部分：按配置的 status_ids 显示状态名。
 * 状态列表还没加载完（statuses 为 undefined）时显示省略号，不能先显示「任意状态」或 UUID；
 * 状态已被删除时显示「未知状态」；status_ids 为空（契约要求 ≥ 1，仅防御）才显示「任意状态」。
 */
function statusSummary(
  t: Translate,
  statusIds: readonly string[],
  statuses: readonly SummaryStatus[] | undefined,
): string {
  if (statusIds.length === 0) return t("automations.trigger.statusAll");
  if (!statuses) return "…";
  const byId = new Map(statuses.map((status) => [status.id, status.name]));
  const names = statusIds.map(
    (id) => byId.get(id) ?? t("automations.trigger.statusUnknown"),
  );
  const separator = t("automations.trigger.statusSeparator");
  if (names.length <= STATUS_SUMMARY_LIMIT) return names.join(separator);
  return t("automations.trigger.statusMore", {
    names: names.slice(0, STATUS_SUMMARY_LIMIT).join(separator),
    count: names.length,
  });
}

/**
 * 卡片与详情里的触发器摘要。
 * statuses 是组织的任务状态列表，只有任务状态触发器用到；加载中传 undefined。
 */
export function triggerSummary(
  t: Translate,
  trigger: AutomationTrigger,
  statuses: readonly SummaryStatus[] | undefined,
): string {
  switch (trigger.kind) {
    case "schedule":
      return trigger.config.schedule
        ? scheduleText(t, trigger.config.schedule)
        : t("automations.trigger.kind.schedule");
    case "task_status": {
      const text = t("automations.trigger.summary.task_status", {
        statuses: statusSummary(t, trigger.config.status_ids ?? [], statuses),
      });
      const filters = trigger.config.filters?.length ?? 0;
      return filters > 0
        ? `${text}${t("automations.trigger.filtersMore", { count: filters })}`
        : text;
    }
    case "comment":
      return t("automations.trigger.summary.comment", {
        value: trigger.config.contains ?? "",
      });
    case "webhook":
      return t("automations.trigger.summary.webhook");
  }
}

/** 提示词去掉常见的 markdown 标记，用于卡片的两行描述。 */
export function plainPrompt(prompt: string): string {
  return prompt
    .replace(/```[\s\S]*?```/g, " ")
    .replace(/[#>*_`~-]+/g, " ")
    .replace(/\[([^\]]*)\]\([^)]*\)/g, "$1")
    .replace(/\s+/g, " ")
    .trim();
}

/** 按时区格式化日期时间（月/日 时:分），缺失时返回空串。 */
export function formatDateTime(
  locale: string,
  value: string | null | undefined,
  timeZone?: string,
): string {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  return new Intl.DateTimeFormat(locale, {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
    timeZone,
  }).format(date);
}

/** 触发原因（运行记录与详情）：手动启动 / 计划运行 / 任务状态变更 / 任务评论 / Webhook。 */
export function triggerReason(t: Translate, kind: string): string {
  const key = `automations.detail.triggerReasons.${kind}` as MessageKey;
  return t(key);
}
