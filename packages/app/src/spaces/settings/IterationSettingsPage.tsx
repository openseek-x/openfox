import { useRef, useState, type MutableRefObject } from "react";

import type {
  IterationMode,
  IterationSettings,
  IterationSettingsInput,
} from "@openfox/api";
import { useI18n, useT } from "@openfox/i18n";
import {
  Button,
  ConfirmDialog,
  DateFieldPopover,
  Dialog,
  Radio,
  Select,
  Switch,
  cn,
} from "@openfox/ui";

import { formatIterationRange } from "../../iterations/dates";
import {
  useIterationSettings,
  useUpdateIterationSettings,
} from "../../iterations/queries";
import type { SpacePageProps } from "./GeneralPage";
import {
  PageHeading,
  RetiredBanner,
  SettingRow,
  SettingsCard,
  SettingsColumn,
  SettingsSection,
} from "./parts";

const WEEKDAYS = [
  "daySunday",
  "dayMonday",
  "dayTuesday",
  "dayWednesday",
  "dayThursday",
  "dayFriday",
  "daySaturday",
] as const;
const TIMEZONES = [
  "Asia/Shanghai",
  "America/New_York",
  "Europe/London",
  "UTC",
] as const;
const UPCOMING_COUNTS = [1, 2, 3, 4, 6, 8, 10, 12, 15] as const;

export function IterationSettingsPage({
  orgId,
  space,
  canManage,
  retired,
}: SpacePageProps) {
  const t = useT();
  const settings = useIterationSettings(orgId, space.id);
  const update = useUpdateIterationSettings(orgId, space.id);
  const [modeDialog, setModeDialog] = useState(false);
  const [pendingMode, setPendingMode] = useState<IterationMode | null>(null);
  const [confirmDisable, setConfirmDisable] = useState(false);
  const [calendarOpen, setCalendarOpen] = useState(false);
  const calendarAnchor = useRef<HTMLButtonElement | null>(null);
  const editable = canManage && !retired;
  const value = settings.data;

  if (settings.isLoading || !value) return null;
  const save = (patch: Omit<IterationSettingsInput, "version">) =>
    update.mutate({ ...patch, version: value.version });
  const chooseMode = (mode: IterationMode) => {
    if (mode === value.mode || !value.enabled) {
      setModeDialog(false);
      setPendingMode(null);
      if (!value.enabled) save({ enabled: true, mode });
      return;
    }
    setModeDialog(false);
    setPendingMode(mode);
  };
  const enabledChange = (enabled: boolean) => {
    if (!editable) return;
    if (enabled) {
      setModeDialog(true);
      return;
    }
    setConfirmDisable(true);
  };
  const confirmModeSwitch = () => {
    if (pendingMode) save({ mode: pendingMode });
    setPendingMode(null);
  };
  const current = value.current;
  const currentRange = current
    ? formatIterationRange(
        current.start_date,
        current.end_date,
        "zh-CN",
        value.timezone,
      )
    : null;

  return (
    <SettingsColumn>
      {retired && <RetiredBanner />}
      <PageHeading
        title={t("spaces.iteration.title")}
        description={t("spaces.iteration.description")}
      />
      <SettingsSection title={t("spaces.iteration.title")}>
        <SettingsCard>
          <SettingRow
            title={t("spaces.iteration.enable")}
            description={
              value.enabled
                ? currentRange
                  ? t("spaces.iteration.current", { range: currentRange })
                  : value.next_start_date
                    ? t("spaces.iteration.next", {
                        date: formatIterationDate(
                          value.next_start_date,
                          value.timezone,
                        ),
                      })
                    : t("spaces.iteration.enabledDescription")
                : t("spaces.iteration.disabledDescription")
            }
            control={
              <Switch
                checked={value.enabled}
                disabled={!editable || update.isPending}
                label={t("spaces.iteration.enable")}
                onCheckedChange={enabledChange}
              />
            }
          />
        </SettingsCard>
      </SettingsSection>

      {value.enabled && (
        <>
          <SettingsSection title={t("spaces.iteration.mode")}>
            <SettingsCard>
              <SettingRow
                title={t("spaces.iteration.mode")}
                description={t("spaces.iteration.modeDescription")}
                control={
                  <Select
                    aria-label={t("spaces.iteration.mode")}
                    value={value.mode}
                    disabled={!editable || update.isPending}
                    options={[
                      {
                        value: "manual" as const,
                        label: t("spaces.iteration.manual"),
                      },
                      {
                        value: "automatic" as const,
                        label: t("spaces.iteration.automatic"),
                      },
                    ]}
                    onChange={(mode) => {
                      if (mode !== value.mode) setPendingMode(mode);
                    }}
                  />
                }
              />
            </SettingsCard>
          </SettingsSection>

          {value.mode === "automatic" ? (
            <AutomaticSettings
              value={value}
              editable={editable}
              saving={update.isPending}
              onChange={save}
              calendarOpen={calendarOpen}
              setCalendarOpen={setCalendarOpen}
              calendarAnchor={calendarAnchor}
            />
          ) : (
            <SettingsSection title={t("spaces.iteration.mode")}>
              <SettingsCard>
                <SettingRow
                  title={t("spaces.iteration.manual")}
                  description={t(
                    "spaces.create.iterationMode.manualDescription",
                  )}
                />
              </SettingsCard>
            </SettingsSection>
          )}
        </>
      )}

      <ModeDialog
        open={modeDialog}
        onCancel={() => setModeDialog(false)}
        onConfirm={chooseMode}
      />
      <ConfirmDialog
        open={pendingMode !== null}
        title={
          pendingMode === "manual"
            ? t("spaces.iteration.switchManualTitle")
            : t("spaces.iteration.chooseModeTitle")
        }
        description={
          pendingMode === "manual"
            ? t("spaces.iteration.switchManualDescription")
            : t("spaces.iteration.enabledDescription")
        }
        onCancel={() => setPendingMode(null)}
        onConfirm={confirmModeSwitch}
        loading={update.isPending}
      />
      <ConfirmDialog
        open={confirmDisable}
        title={t("spaces.iteration.disableTitle")}
        description={t("spaces.iteration.disableDescription")}
        onCancel={() => setConfirmDisable(false)}
        onConfirm={() => {
          setConfirmDisable(false);
          save({ enabled: false });
        }}
        loading={update.isPending}
      />
    </SettingsColumn>
  );
}

function AutomaticSettings({
  value,
  editable,
  saving,
  onChange,
  calendarOpen,
  setCalendarOpen,
  calendarAnchor,
}: {
  value: IterationSettings;
  editable: boolean;
  saving: boolean;
  onChange: (patch: Omit<IterationSettingsInput, "version">) => void;
  calendarOpen: boolean;
  setCalendarOpen: (open: boolean) => void;
  calendarAnchor: MutableRefObject<HTMLButtonElement | null>;
}) {
  const t = useT();
  const { locale } = useI18n();
  const selectOptions = (from: number, to: number) =>
    Array.from({ length: to - from + 1 }, (_, i) => {
      const count = from + i;
      return {
        value: String(count),
        label: t("spaces.iteration.weeks", { count }),
      };
    });
  return (
    <>
      <SettingsSection title={t("spaces.iteration.automation")}>
        <SettingsCard>
          <SettingRow
            title={t("spaces.iteration.duration")}
            control={
              <Select
                aria-label={t("spaces.iteration.duration")}
                value={String(value.duration_weeks)}
                disabled={!editable || saving}
                options={selectOptions(1, 8)}
                onChange={(next) => onChange({ duration_weeks: Number(next) })}
              />
            }
          />
          <SettingRow
            title={t("spaces.iteration.cooldown")}
            control={
              <Select
                aria-label={t("spaces.iteration.cooldown")}
                value={String(value.cooldown_weeks)}
                disabled={!editable || saving}
                options={[
                  {
                    value: "0",
                    label: t("spaces.iteration.weeks", { count: 0 }),
                  },
                  ...selectOptions(1, 3),
                ]}
                onChange={(next) => onChange({ cooldown_weeks: Number(next) })}
              />
            }
          />
          <SettingRow
            title={t("spaces.iteration.startWeekday")}
            control={
              <Select
                aria-label={t("spaces.iteration.startWeekday")}
                value={String(value.start_weekday)}
                disabled={!editable || saving}
                options={WEEKDAYS.map((key, index) => ({
                  value: String(index),
                  label: t(`spaces.iteration.${key}`),
                }))}
                onChange={(next) => onChange({ start_weekday: Number(next) })}
              />
            }
          />
          <SettingRow
            title={t("spaces.iteration.timezone")}
            control={
              <Select
                aria-label={t("spaces.iteration.timezone")}
                value={value.timezone}
                disabled={!editable || saving}
                options={TIMEZONES.map((zone) => ({
                  value: zone,
                  label: zone,
                }))}
                onChange={(timezone) => onChange({ timezone })}
              />
            }
          />
          <SettingRow
            title={t("spaces.iteration.upcomingCount")}
            control={
              <Select
                aria-label={t("spaces.iteration.upcomingCount")}
                value={String(value.upcoming_count)}
                disabled={!editable || saving}
                options={UPCOMING_COUNTS.map((count) => ({
                  value: String(count),
                  label: String(count),
                }))}
                onChange={(next) =>
                  onChange({
                    upcoming_count: Number(
                      next,
                    ) as IterationSettings["upcoming_count"],
                  })
                }
              />
            }
          />
          <SettingRow
            title={t("spaces.iteration.nextStartDate")}
            description={
              value.next_start_date
                ? formatIterationDate(
                    value.next_start_date,
                    value.timezone,
                    locale,
                  )
                : undefined
            }
            control={
              <>
                <button
                  ref={calendarAnchor}
                  type="button"
                  disabled={!editable || saving}
                  className={cn(
                    "h-8 rounded-lg border border-line-default px-2.5 text-body-sm hover:bg-surface-hover",
                    !value.next_start_date && "text-fg-muted",
                  )}
                  onClick={() => setCalendarOpen(true)}
                >
                  {value.next_start_date ?? t("ui.date.pick")}
                </button>
                <DateFieldPopover
                  open={calendarOpen}
                  onOpenChange={setCalendarOpen}
                  anchorRef={calendarAnchor}
                  value={value.next_start_date}
                  onChange={(next) => onChange({ next_start_date: next })}
                />
              </>
            }
          />
        </SettingsCard>
      </SettingsSection>
      <SettingsSection title={t("spaces.iteration.automation")}>
        <SettingsCard>
          <SettingRow
            title={t("spaces.iteration.autoAddStarted")}
            control={
              <Switch
                checked={value.auto_add_started}
                disabled={!editable || saving}
                label={t("spaces.iteration.autoAddStarted")}
                onCheckedChange={(auto_add_started) =>
                  onChange({ auto_add_started })
                }
              />
            }
          />
          <SettingRow
            title={t("spaces.iteration.autoAddCompleted")}
            control={
              <Switch
                checked={value.auto_add_completed}
                disabled={!editable || saving}
                label={t("spaces.iteration.autoAddCompleted")}
                onCheckedChange={(auto_add_completed) =>
                  onChange({ auto_add_completed })
                }
              />
            }
          />
        </SettingsCard>
      </SettingsSection>
    </>
  );
}

function ModeDialog({
  open,
  onCancel,
  onConfirm,
}: {
  open: boolean;
  onCancel: () => void;
  onConfirm: (mode: IterationMode) => void;
}) {
  const t = useT();
  const [mode, setMode] = useState<IterationMode>("automatic");
  return (
    <Dialog
      open={open}
      onClose={onCancel}
      title={t("spaces.iteration.chooseModeTitle")}
      description={t("spaces.iteration.chooseModeDescription")}
      footer={
        <>
          <Button variant="outline" onClick={onCancel}>
            {t("spaces.iteration.cancel")}
          </Button>
          <Button onClick={() => onConfirm(mode)}>
            {t("spaces.iteration.confirm")}
          </Button>
        </>
      }
    >
      <div role="radiogroup" className="flex flex-col gap-2">
        {(["manual", "automatic"] as const).map((item) => (
          <label
            key={item}
            className={cn(
              "flex cursor-pointer items-start gap-2 rounded-xl border border-line-default p-3",
              mode === item && "border-line-focus bg-surface-active",
            )}
          >
            <Radio
              name="iteration-mode-settings"
              checked={mode === item}
              onChange={() => setMode(item)}
            />
            <span className="flex flex-col gap-0.5">
              <span className="text-body font-medium">
                {t(`spaces.iteration.${item}`)}
              </span>
              <span className="text-caption text-fg-muted">
                {t(
                  item === "manual"
                    ? "spaces.create.iterationMode.manualDescription"
                    : "spaces.create.iterationMode.automaticDescription",
                )}
              </span>
            </span>
          </label>
        ))}
      </div>
    </Dialog>
  );
}

function formatIterationDate(
  value: string,
  timeZone: string,
  locale = "zh-CN",
): string {
  const [year, month, day] = value.split("-").map(Number);
  return new Intl.DateTimeFormat(locale, {
    year: "numeric",
    month: "short",
    day: "numeric",
    timeZone,
  }).format(new Date(Date.UTC(year!, month! - 1, day!, 12)));
}
