import { useT, type MessageKey } from "@openfox/i18n";
import { Field, Select, TextInput, cn } from "@openfox/ui";

import { COMMON_TIMEZONES, parseCron } from "./cron";
import { scheduleText } from "./parts";
import {
  isValidTime,
  type ScheduleDraft,
  type ScheduleFrequency,
} from "./model";

// 定时触发器编辑：频率、时间与时区、预览。

const FREQUENCIES: ScheduleFrequency[] = [
  "hourly",
  "daily",
  "weekdays",
  "weekly",
  "custom",
];

const FREQUENCY_LABEL: Record<ScheduleFrequency, MessageKey> = {
  hourly: "automations.schedule.hourly",
  daily: "automations.schedule.daily",
  weekdays: "automations.schedule.weekdays",
  weekly: "automations.schedule.weekly",
  custom: "automations.schedule.custom",
};

/** 时区下拉：18 个常用时区；浏览器时区不在列表里时追加进去。 */
export function timezoneOptions(browserZone: string): string[] {
  const zones: string[] = [...COMMON_TIMEZONES];
  if (browserZone && !zones.includes(browserZone)) zones.push(browserZone);
  return zones;
}

export function browserTimezone(): string {
  return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
}

export function ScheduleEditor({
  value,
  onChange,
}: {
  value: ScheduleDraft;
  onChange: (next: ScheduleDraft) => void;
}) {
  const t = useT();
  const parsed = parseCron(value.cron);
  const cronError =
    value.frequency === "custom" && !parsed.ok
      ? t(
          parsed.error === "minute"
            ? "automations.schedule.cronInvalid"
            : "automations.schedule.cronFormat",
        )
      : undefined;
  // 时间不合法时（输入中途）不计算预览，避免生成错误的 cron
  const timeInvalid =
    value.frequency !== "custom" &&
    value.frequency !== "hourly" &&
    !isValidTime(value.time);
  const zones = timezoneOptions(browserTimezone());

  return (
    <div className="flex flex-col gap-3">
      <Field label={t("automations.schedule.frequency")}>
        <Select<ScheduleFrequency>
          value={value.frequency}
          aria-label={t("automations.schedule.frequency")}
          onChange={(frequency) => onChange({ ...value, frequency })}
          options={FREQUENCIES.map((frequency) => ({
            value: frequency,
            label: t(FREQUENCY_LABEL[frequency]),
          }))}
        />
      </Field>

      {value.frequency === "hourly" && (
        <Field label={t("automations.schedule.minute")}>
          <TextInput
            type="number"
            min={0}
            max={59}
            value={value.minute}
            onChange={(event) =>
              onChange({ ...value, minute: Number(event.target.value) })
            }
          />
        </Field>
      )}

      {(value.frequency === "daily" ||
        value.frequency === "weekdays" ||
        value.frequency === "weekly") && (
        <div className="grid grid-cols-2 gap-3">
          <Field label={t("automations.schedule.time")}>
            <TextInput
              type="time"
              value={value.time}
              invalid={!isValidTime(value.time)}
              onChange={(event) =>
                onChange({ ...value, time: event.target.value })
              }
            />
          </Field>
          <Field label={t("automations.schedule.timezone")}>
            <Select
              value={value.timezone}
              aria-label={t("automations.schedule.timezone")}
              onChange={(timezone) => onChange({ ...value, timezone })}
              options={zones.map((zone) => ({ value: zone, label: zone }))}
            />
          </Field>
        </div>
      )}

      {value.frequency === "weekly" && (
        <Field label={t("automations.schedule.weekdaysLabel")}>
          <div className="flex gap-1.5">
            {[0, 1, 2, 3, 4, 5, 6].map((day) => {
              const active = value.weekdays.includes(day);
              return (
                <button
                  key={day}
                  type="button"
                  aria-pressed={active}
                  onClick={() => {
                    // 至少保留一天，避免生成空的星期字段
                    const next = active
                      ? value.weekdays.filter((item) => item !== day)
                      : [...value.weekdays, day];
                    if (next.length > 0) onChange({ ...value, weekdays: next });
                  }}
                  className={cn(
                    "flex h-8 flex-1 items-center justify-center rounded-md border text-body",
                    active
                      ? "border-accent bg-accent/10 text-accent"
                      : "border-line-default text-fg-muted hover:bg-surface-hover",
                  )}
                >
                  {t(`automations.schedule.dayShort.${day}` as MessageKey)}
                </button>
              );
            })}
          </div>
        </Field>
      )}

      {value.frequency === "custom" && (
        <div className="grid grid-cols-2 gap-3">
          <Field
            label={t("automations.schedule.cronLabel")}
            helper={cronError ? undefined : t("automations.schedule.cronHint")}
            error={cronError}
          >
            <TextInput
              value={value.cron}
              placeholder={t("automations.schedule.cronPlaceholder")}
              className="font-mono"
              onChange={(event) =>
                onChange({ ...value, cron: event.target.value })
              }
            />
          </Field>
          <Field label={t("automations.schedule.timezone")}>
            <Select
              value={value.timezone}
              aria-label={t("automations.schedule.timezone")}
              onChange={(timezone) => onChange({ ...value, timezone })}
              options={zones.map((zone) => ({ value: zone, label: zone }))}
            />
          </Field>
        </div>
      )}

      <p className="text-body-sm text-fg-muted" aria-live="polite">
        {cronError || timeInvalid
          ? t("automations.schedule.previewEmpty")
          : scheduleText(t, value)}
      </p>
    </div>
  );
}
