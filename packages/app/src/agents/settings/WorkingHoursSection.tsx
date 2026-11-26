import { useEffect, useState } from "react";
import type { AgentConfigInput } from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Button,
  Callout,
  Checkbox,
  Icon,
  Switch,
  TextInput,
} from "@openfox/ui";
import type { SectionProps } from "./BasicSections";

type Schedule = NonNullable<AgentConfigInput["working_hours"]>;
const weekdays = ["mon", "tue", "wed", "thu", "fri", "sat", "sun"] as const;
export function defaultWorkingHours(): Schedule {
  return {
    enabled: false,
    timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC",
    days: Array.from({ length: 7 }, (_, i) =>
      i < 5
        ? [
            { start: "00:00", end: "08:00" },
            { start: "20:00", end: "24:00" },
          ]
        : [{ start: "00:00", end: "24:00" }],
    ),
    triggers: { assignment: true, comment_mention: true, direct_chat: false },
  };
}
export function validWorkingHours(value: Schedule): boolean {
  try {
    if (!value.timezone || value.timezone === "Local") return false;
    new Intl.DateTimeFormat("en", { timeZone: value.timezone });
  } catch {
    return false;
  }
  return (
    value.days.length === 7 &&
    value.days.every(
      (day) =>
        day.length <= 2 &&
        day.every(
          (p, i) =>
            /^(?:[01]\d|2[0-3]):[0-5]\d$/.test(p.start) &&
            /^(?:(?:[01]\d|2[0-3]):[0-5]\d|24:00)$/.test(p.end) &&
            p.start < p.end &&
            day.every(
              (q, j) => i === j || p.end <= q.start || q.end <= p.start,
            ),
        ),
    )
  );
}
export function WorkingHoursSection({ agent, writes, setDirty }: SectionProps) {
  const t = useT();
  const saved = agent.config?.working_hours;
  const [initial, setInitial] = useState(() => saved ?? defaultWorkingHours());
  const [draft, setDraft] = useState(initial);
  useEffect(() => {
    const next = saved ?? defaultWorkingHours();
    setInitial(next);
    setDraft(next);
  }, [saved]);
  const dirty = JSON.stringify(draft) !== JSON.stringify(initial);
  useEffect(() => {
    setDirty("working-hours", dirty);
    return () => setDirty("working-hours", false);
  }, [dirty, setDirty]);
  const editDay = (index: number, periods: Schedule["days"][number]) =>
    setDraft((prev) => ({
      ...prev,
      days: prev.days.map((d, i) => (i === index ? periods : d)),
    }));
  if (!agent.config) return null;
  return (
    <div className="flex flex-col gap-4">
      <label className="flex items-center gap-2">
        <Switch
          checked={draft.enabled}
          onCheckedChange={(enabled) => setDraft({ ...draft, enabled })}
        />
        {t("agents.workingHours.enabled")}
      </label>
      <p className="text-body-sm text-fg-muted">
        {t("agents.workingHours.hint")}
      </p>
      <label className="flex flex-col gap-1">
        {t("agents.workingHours.timezone")}
        <TextInput
          value={draft.timezone}
          onChange={(e) => setDraft({ ...draft, timezone: e.target.value })}
        />
      </label>
      <div className="flex flex-col gap-2">
        {weekdays.map((day, index) => (
          <div
            key={day}
            className="flex flex-wrap items-center gap-2"
            role="group"
            aria-label={t(`agents.workingHours.${day}`)}
          >
            <span className="w-16">{t(`agents.workingHours.${day}`)}</span>
            {!draft.days[index]!.length && (
              <span className="text-fg-muted">
                {t("agents.workingHours.closed")}
              </span>
            )}
            {draft.days[index]!.map((period, p) => (
              <div key={p} className="flex items-center gap-1">
                <TextInput
                  className="w-20"
                  aria-label={t("agents.workingHours.start", {
                    day: t(`agents.workingHours.${day}`),
                    n: p + 1,
                  })}
                  value={period.start}
                  onChange={(e) =>
                    editDay(
                      index,
                      draft.days[index]!.map((v, j) =>
                        j === p ? { ...v, start: e.target.value } : v,
                      ),
                    )
                  }
                />
                <span>–</span>
                <TextInput
                  className="w-20"
                  aria-label={t("agents.workingHours.end", {
                    day: t(`agents.workingHours.${day}`),
                    n: p + 1,
                  })}
                  value={period.end}
                  onChange={(e) =>
                    editDay(
                      index,
                      draft.days[index]!.map((v, j) =>
                        j === p ? { ...v, end: e.target.value } : v,
                      ),
                    )
                  }
                />
                <Button
                  variant="ghost"
                  size="28"
                  aria-label={t("agents.workingHours.remove", {
                    day: t(`agents.workingHours.${day}`),
                    n: p + 1,
                  })}
                  onClick={() =>
                    editDay(
                      index,
                      draft.days[index]!.filter((_, j) => j !== p),
                    )
                  }
                >
                  <Icon name="x" size={12} />
                </Button>
              </div>
            ))}
            {draft.days[index]!.length < 2 && (
              <Button
                variant="outline"
                size="28"
                onClick={() =>
                  editDay(index, [
                    ...draft.days[index]!,
                    {
                      start: draft.days[index]!.length ? "20:00" : "00:00",
                      end: draft.days[index]!.length ? "24:00" : "08:00",
                    },
                  ])
                }
              >
                {t("agents.workingHours.add")}
              </Button>
            )}
          </div>
        ))}
      </div>
      <fieldset className="flex flex-col gap-2">
        <legend>{t("agents.workingHours.triggers")}</legend>
        {(["assignment", "comment_mention", "direct_chat"] as const).map(
          (key) => (
            <label key={key} className="flex items-center gap-2">
              <Checkbox
                checked={draft.triggers[key]}
                onChange={(e) =>
                  setDraft({
                    ...draft,
                    triggers: { ...draft.triggers, [key]: e.target.checked },
                  })
                }
              />
              {t(`agents.workingHours.${key}`)}
            </label>
          ),
        )}
      </fieldset>
      {!validWorkingHours(draft) && (
        <Callout tone="danger">{t("agents.workingHours.invalid")}</Callout>
      )}
      {draft.enabled && draft.days.every((d) => !d.length) && (
        <Callout>{t("agents.workingHours.allClosed")}</Callout>
      )}
      <Button
        disabled={!dirty || !validWorkingHours(draft) || writes.configPending}
        onClick={() => void writes.saveConfig({ working_hours: draft })}
      >
        {t("common.save")}
      </Button>
    </div>
  );
}
