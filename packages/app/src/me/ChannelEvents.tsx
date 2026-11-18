import { useState } from "react";
import type {
  InboxPreference,
  NotificationChannel,
  NotificationChannelPatch,
  NotificationEvent,
} from "@openfox/api";
import { useT, type MessageKey } from "@openfox/i18n";
import { Icon, Switch } from "@openfox/ui";
import {
  SettingsSection,
  SettingsCard,
  SettingRow,
} from "../spaces/settings/parts";
import { useInboxPreferences } from "./InboxNotificationSettings";

const suppressed = new Set([
  "chat_replied_participant",
  "task_failed_participant",
  "task_needs_human_input_participant",
  "task_review_requested_participant",
]);
/** Display groups differ from the historical kind-based fallback in these five cases. */
export function legacyEventForLeaf(
  event: InboxPreference,
): NotificationEvent | undefined {
  if (
    [
      "task_unassigned_assignee",
      "task_assignee_updated_assignee",
      "task_assignee_updated_follower",
      "task_subscribed_follower",
    ].includes(event.key)
  )
    return "status";
  if (event.key === "chat_replied_participant") return "comment";
  return (
    {
      assignments: "assign",
      status_changes: "status",
      comments: "comment",
      mentions: "mention",
      agent_activity: "agent",
      automation_runs: "automation",
    } as Record<string, NotificationEvent>
  )[event.group];
}
/** Stored child choice remains visible when a channel/group/consent gate is off. */
export function eventChoice(
  channel: NotificationChannel,
  event: InboxPreference,
  catalog: InboxPreference[],
): boolean {
  const explicit = channel.event_overrides?.[event.key];
  if (explicit !== undefined) return explicit;
  const legacy = legacyEventForLeaf(event);
  if (legacy && channel.disabled_events.includes(legacy)) return false;
  return (
    !suppressed.has(event.key) ||
    (event.level !== "off" &&
      catalog.find((row) => row.key === event.group)?.level !== "off")
  );
}
export function effectiveEventEnabled(
  channel: NotificationChannel,
  event: InboxPreference,
  catalog: InboxPreference[],
): boolean {
  return (
    channel.enabled &&
    channel.available !== false &&
    !channel.muted_groups?.includes(
      event.group as NonNullable<NotificationChannel["muted_groups"]>[number],
    ) &&
    (event.group !== "human_chat" ||
      (channel.direct_messages ??
        ["desktop", "browser"].includes(channel.channel))) &&
    eventChoice(channel, event, catalog)
  );
}
export function ChannelEvents({
  channel,
  saving,
  onChange,
}: {
  channel: NotificationChannel;
  saving: boolean;
  onChange: (patch: Omit<NotificationChannelPatch, "revision">) => void;
}) {
  const t = useT();
  const prefs = useInboxPreferences();
  const [collapsed, setCollapsed] = useState<string[]>([]);
  const disabled = saving || !channel.enabled || channel.available === false;
  const direct =
    channel.direct_messages ?? ["desktop", "browser"].includes(channel.channel);
  return (
    <SettingsSection
      title={t("me.notifications.events.title")}
      description={t("me.notifications.detail.inheritHelp")}
    >
      <SettingsCard>
        <SettingRow
          title={t("me.notifications.detail.directConsent")}
          description={t("me.notifications.detail.directConsentHelp")}
          control={
            <Switch
              label={t("me.notifications.detail.directConsent")}
              checked={direct}
              disabled={disabled}
              onCheckedChange={(direct_messages) =>
                onChange({ direct_messages })
              }
            />
          }
        />
      </SettingsCard>
      {prefs.isError && (
        <button role="alert" onClick={() => void prefs.refetch()}>
          {t("common.retry")}
        </button>
      )}
      {!prefs.data && !prefs.isError && <p>{t("common.loading")}</p>}
      {prefs.data
        ?.filter((row) => !row.group)
        .map((group) => {
          const catalog = prefs.data;
          const children = catalog.filter((row) => row.group === group.key);
          const key = group.key as NonNullable<
            NotificationChannel["muted_groups"]
          >[number];
          const on = !channel.muted_groups?.includes(key);
          const open = !collapsed.includes(key);
          const title = t(`me.notifications.inbox.groups.${key}` as MessageKey);
          return (
            <div
              key={key}
              className="rounded-xl border border-line-default bg-surface-card"
            >
              <div className="flex min-h-16 items-center gap-4 px-4">
                <button
                  type="button"
                  className="flex min-h-16 min-w-0 flex-1 items-center gap-3 text-left"
                  aria-expanded={open}
                  onClick={() =>
                    setCollapsed(
                      open
                        ? [...collapsed, key]
                        : collapsed.filter((value) => value !== key),
                    )
                  }
                >
                  <span className="flex-1 text-body font-medium">{title}</span>
                  <span className="text-caption text-fg-muted">
                    {
                      children.filter((event) =>
                        effectiveEventEnabled(channel, event, catalog),
                      ).length
                    }
                    /{children.length}
                  </span>
                  <Icon
                    name={open ? "chevron-down" : "chevron-right"}
                    size={14}
                  />
                </button>
                <Switch
                  label={title}
                  checked={on}
                  disabled={disabled}
                  onCheckedChange={(value) =>
                    onChange({
                      muted_groups: value
                        ? (channel.muted_groups ?? []).filter(
                            (item) => item !== key,
                          )
                        : [...(channel.muted_groups ?? []), key],
                    })
                  }
                />
              </div>
              {open && (
                <div className="divide-y divide-line-soft border-t border-line-soft px-8 py-2">
                  {children.map((event) => (
                    <SettingRow
                      key={event.key}
                      title={t(
                        `me.notifications.inbox.events.${event.key}` as MessageKey,
                      )}
                      control={
                        <Switch
                          label={t(
                            `me.notifications.inbox.events.${event.key}` as MessageKey,
                          )}
                          checked={eventChoice(channel, event, catalog)}
                          disabled={
                            disabled || !on || (key === "human_chat" && !direct)
                          }
                          onCheckedChange={(value) =>
                            onChange({
                              event_overrides: { [event.key]: value },
                            })
                          }
                        />
                      }
                    />
                  ))}
                </div>
              )}
            </div>
          );
        })}
    </SettingsSection>
  );
}
