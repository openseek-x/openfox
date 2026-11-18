import type { TimelineActivity } from "@openfox/api";
import type { Translate } from "@openfox/i18n";
const text = (v: unknown) => (typeof v === "string" ? v : "");
export function zohoImportText(
  t: Translate,
  activity: Pick<TimelineActivity, "safe_diff" | "actor_name">,
): string {
  const d = activity.safe_diff ?? {},
    id = text(d.source_reporter_id),
    name = text(d.source_reporter_name) || id;
  const actor = activity.actor_name || t("tasks.activity.someone");
  if (
    d.source !== "zoho_bugtracker" ||
    d.source_reporter_region !== "US" ||
    !/^[1-9][0-9]{0,63}$/.test(id)
  )
    return t("tasks.activity.unknown", { actor });
  const mapping = text(d.mapped_user_id)
    ? t("integrations.zoho.activityMember", {
        name: text(d.mapped_user_name) || text(d.mapped_user_id),
      })
    : text(d.mapped_external_account_id)
      ? t("integrations.zoho.activityExternal")
      : t("integrations.zoho.activityUnmapped");
  return `${t("integrations.zoho.activity", { actor, name, id })}；${mapping}`;
}
