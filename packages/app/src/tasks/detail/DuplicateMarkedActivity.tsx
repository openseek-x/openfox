import { Link } from "react-router-dom";
import type { TimelineActivity } from "@openfox/api";
import { useT, type Translate } from "@openfox/i18n";
import { Avatar } from "@openfox/ui";
import { formatRelativeTime } from "../primitives/dates";
import { taskPagePath } from "../navigation";

type SourceActivity = Pick<TimelineActivity, "safe_diff" | "actor_name">;

// 来源任务编号形如 OPE-12，空间标识允许字母、数字、下划线和连字符（如 SP_7E0C31CB87FF-2）；
// 不合规（缺失、非字符串、带 `:` `.` `/` 等其它字符）时不渲染链接，走通用兜底文案。
const DISPLAY_ID = /^[A-Za-z][A-Za-z0-9_-]*-[0-9]+$/;
// 文案里来源编号的占位：先插入占位再切分，链接放回原位，语序交给各语言文案决定。
const SLOT = "\u0001";

/** task.duplicate_marked 写在目标任务上，safe_diff.display_id 是被标记为重复的来源任务。 */
export function duplicateMarkedSource(activity: SourceActivity): string {
  const id = activity.safe_diff?.display_id;
  return typeof id === "string" && DISPLAY_ID.test(id) ? id.toUpperCase() : "";
}

export function duplicateMarkedText(
  t: Translate,
  activity: SourceActivity,
): string {
  const actor = activity.actor_name || t("tasks.activity.someone");
  const source = duplicateMarkedSource(activity);
  return source
    ? t("tasks.activity.duplicateMarked", { actor, task: source })
    : t("tasks.activity.unknown", { actor });
}

/** 目标任务动态里的「某人将 OPE-12 标记为本任务的重复」，来源编号可点击跳到来源任务。 */
export function DuplicateMarkedActivity({
  activity,
  orgId,
  locale,
}: {
  activity: TimelineActivity;
  orgId: string;
  locale: string;
}) {
  const t = useT();
  const actor = activity.actor_name || t("tasks.activity.someone");
  const source = duplicateMarkedSource(activity);
  const [before, after = ""] = source
    ? t("tasks.activity.duplicateMarked", { actor, task: SLOT }).split(SLOT)
    : [t("tasks.activity.unknown", { actor })];
  return (
    <li className="flex items-center gap-2 text-body-sm text-fg-muted">
      <Avatar name={activity.actor_name || "?"} size={16} />
      <span className="min-w-0 flex-1">
        {before}
        {source && (
          <Link
            to={taskPagePath(orgId, source)}
            className="text-fg-secondary underline-offset-2 hover:underline"
          >
            {source}
          </Link>
        )}
        {after}
      </span>
      <time dateTime={activity.created_at} className="shrink-0 text-caption">
        {formatRelativeTime(activity.created_at, locale)}
      </time>
    </li>
  );
}
