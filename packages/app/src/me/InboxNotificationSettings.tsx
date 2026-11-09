import { useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import type { InboxPreference, InboxPreferenceLevel } from "@openfox/api";
import { useT, type MessageKey } from "@openfox/i18n";
import {
  Button,
  Select,
  Icon,
  Switch,
  useToast,
  type IconName,
} from "@openfox/ui";
import { useApi } from "../context";
import { SettingsSection } from "../spaces/settings/parts";

const key = ["me", "inbox-preferences"] as const;
const groupIcons: Record<string, IconName> = {
  assignments: "user-plus",
  status_changes: "activity",
  comments: "message-circle",
  mentions: "at-sign",
  human_chat: "message-circle",
  agent_activity: "bot",
  automation_runs: "zap",
};
const levels = ["off", "important", "normal"] as const;
const dot = { off: "bg-fg-subtle", important: "bg-danger", normal: "bg-info" };
const text = (section: string, id: string) =>
  `me.notifications.inbox.${section}.${id}` as MessageKey;
export function useInboxPreferences() {
  const api = useApi();
  return useQuery({ queryKey: key, queryFn: api.me.inboxPreferences.list });
}
export function InboxNotificationSettings({ onBack }: { onBack: () => void }) {
  const t = useT(),
    api = useApi(),
    qc = useQueryClient(),
    toast = useToast();
  const q = useInboxPreferences();
  const [collapsed, setCollapsed] = useState<string[]>([]);
  const save = useMutation({
    mutationFn: (change: Pick<InboxPreference, "key" | "level"> | null) =>
      change
        ? api.me.inboxPreferences.update(change)
        : api.me.inboxPreferences.reset(),
    onSuccess: (rows, change) => {
      qc.setQueryData(key, rows);
      if (!change) toast(t("me.notifications.detail.resetDone"));
    },
    onError: (_, change) =>
      toast(
        t(
          change
            ? "me.notifications.detail.saveFailed"
            : "me.notifications.detail.resetFailed",
        ),
        { tone: "error" },
      ),
  });
  const opts = levels.map((value) => ({
    value,
    label: t(text("choices", value)),
    icon: <span aria-hidden className={`size-2 rounded-full ${dot[value]}`} />,
  }));
  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-col gap-2">
        <button
          type="button"
          className="inline-flex h-7 w-fit items-center gap-1 rounded-lg px-1.5 text-body-sm text-fg-secondary hover:bg-surface-hover"
          onClick={onBack}
        >
          <Icon name="chevron-left" size={14} />
          {t("me.notifications.back")}
        </button>
        <header className="flex items-center gap-4">
          <h1 className="flex-1 text-[24px] leading-8 font-medium">
            {t("me.notifications.inbox.title")}
          </h1>
          <Button
            variant="ghost"
            size="28"
            disabled={!q.data || save.isPending}
            onClick={() => save.mutate(null)}
          >
            {t("me.notifications.detail.reset")}
          </Button>
        </header>
      </div>
      <SettingsSection
        title={t("me.notifications.events.title")}
        description={t("me.notifications.inbox.description")}
      >
        {q.isError && (
          <button role="alert" onClick={() => void q.refetch()}>
            {t("common.retry")}
          </button>
        )}
        {q.data
          ?.filter((row) => !row.group)
          .map((group) => {
            const children = q.data.filter((row) => row.group === group.key),
              on = group.level !== "off",
              open = !collapsed.includes(group.key),
              label = t(text("groups", group.key));
            return (
              <div
                key={group.key}
                className="rounded-xl border border-line-default bg-surface-card"
              >
                <div className="flex min-h-16 items-center gap-4 pl-4 pr-8">
                  <button
                    type="button"
                    className="flex min-h-16 min-w-0 flex-1 items-center gap-3 text-left"
                    aria-expanded={open}
                    onClick={() =>
                      setCollapsed(
                        open
                          ? [...collapsed, group.key]
                          : collapsed.filter((id) => id !== group.key),
                      )
                    }
                  >
                    <span className="flex size-8 shrink-0 items-center justify-center rounded-lg bg-surface-muted text-fg-muted">
                      <Icon name={groupIcons[group.key] ?? "inbox"} size={16} />
                    </span>
                    <span className="min-w-0 flex-1">
                      <span className="block text-body font-medium">
                        {label}
                      </span>
                      {group.key === "agent_activity" && (
                        <span className="block text-caption text-fg-muted">
                          {t("me.notifications.inbox.agentHelp")}
                        </span>
                      )}
                    </span>
                    <span className="text-caption tabular-nums text-fg-muted">
                      {on
                        ? children.filter((e) => e.level !== "off").length
                        : 0}
                      /{children.length}
                    </span>
                    <Icon
                      name={open ? "chevron-down" : "chevron-right"}
                      size={14}
                      className="text-fg-muted"
                    />
                  </button>
                  <Switch
                    label={label}
                    checked={on}
                    disabled={save.isPending}
                    onCheckedChange={(value) =>
                      save.mutate({
                        key: group.key,
                        level: value ? "normal" : "off",
                      })
                    }
                  />
                </div>
                {open && (
                  <div className="border-t border-line-soft px-8 py-4">
                    <div className="divide-y divide-line-soft">
                      {children.map((event) => (
                        <div
                          key={event.key}
                          className="flex min-h-[60px] items-center justify-between gap-4 py-4"
                        >
                          <div className="min-w-0">
                            <span className="block text-body-sm font-medium">
                              {t(text("events", event.key))}
                            </span>
                            {group.key === "agent_activity" && (
                              <p className="text-caption text-fg-muted">
                                {t(text("help", event.key))}
                              </p>
                            )}
                          </div>
                          <Select<InboxPreferenceLevel>
                            role="combobox"
                            aria-label={t(text("events", event.key))}
                            value={on ? event.level : "off"}
                            options={opts}
                            className="h-7 w-auto shrink-0 text-caption"
                            disabled={!on || save.isPending}
                            onChange={(level) =>
                              save.mutate({ key: event.key, level })
                            }
                          />
                        </div>
                      ))}
                    </div>
                  </div>
                )}
              </div>
            );
          })}
      </SettingsSection>
    </div>
  );
}
