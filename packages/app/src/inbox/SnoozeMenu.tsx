import { useState, type RefObject } from "react";

import { useI18n, useT } from "@openfox/i18n";
import {
  Calendar,
  Icon,
  MenuItem,
  MenuSeparator,
  Popover,
  TextInput,
  handleMenuKeyDown,
  todayYmd,
} from "@openfox/ui";

import { parseSnooze, snoozePresets } from "../tasks/primitives/snooze";

/** 稍后处理浮层：自然语言输入、4 个预设、自定义日期和时间。 */
export function SnoozeMenu({
  open,
  onOpenChange,
  anchorRef,
  onSnooze,
  onUnsnooze,
  now = new Date(),
  laterToday = false,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  anchorRef: RefObject<HTMLElement | null>;
  onUnsnooze?: () => void;
  onSnooze: (at: Date) => void;
  now?: Date;
  /** 分拣台的稍后处理多一个「今天稍晚」预设 */
  laterToday?: boolean;
}) {
  const t = useT();
  const { locale } = useI18n();
  const [text, setText] = useState("");
  const [error, setError] = useState(false);
  const [custom, setCustom] = useState(false);
  const tomorrow = new Date(now);
  tomorrow.setDate(now.getDate() + 1);
  const fmt = new Intl.DateTimeFormat(locale, {
    month: "short",
    day: "numeric",
    weekday: "short",
    hour: "2-digit",
    minute: "2-digit",
  });
  const choose = (at: Date) => {
    onSnooze(at);
    onOpenChange(false);
    setText("");
    setCustom(false);
  };
  return (
    <Popover
      open={open}
      onOpenChange={(next) => {
        onOpenChange(next);
        if (!next) {
          setCustom(false);
          setError(false);
        }
      }}
      anchorRef={anchorRef}
      placement="bottom-end"
      aria-label={t("tasks.snooze.title")}
      className="w-[300px]"
    >
      <form
        className="p-1"
        onSubmit={(event) => {
          event.preventDefault();
          const at = parseSnooze(text, new Date());
          if (at) choose(at);
          else setError(true);
        }}
      >
        <TextInput
          autoFocus
          aria-label={t("tasks.snooze.title")}
          placeholder={t("tasks.snooze.placeholder")}
          leading={<Icon name="clock" />}
          value={text}
          invalid={error}
          aria-describedby={error ? "snooze-error" : undefined}
          onChange={(event) => {
            setText(event.target.value);
            setError(false);
          }}
        />
        {error && (
          <p
            id="snooze-error"
            role="alert"
            className="px-1 pt-1 text-caption text-danger"
          >
            {t("tasks.snooze.invalid")}
          </p>
        )}
      </form>
      <MenuSeparator />
      <div
        role="menu"
        aria-label={t("tasks.snooze.title")}
        onKeyDown={handleMenuKeyDown}
      >
        {onUnsnooze && (
          <MenuItem
            onSelect={() => {
              onUnsnooze();
              onOpenChange(false);
            }}
          >
            {t("spaces.triage.unsnooze")}
          </MenuItem>
        )}
        {snoozePresets(now, laterToday).map((preset) => (
          <MenuItem
            key={preset.id}
            trailing={fmt.format(preset.at)}
            onSelect={() => choose(preset.at)}
          >
            {t(`tasks.snooze.${preset.id}`)}
          </MenuItem>
        ))}
        <MenuSeparator />
        <button
          type="button"
          role="menuitem"
          aria-expanded={custom}
          onClick={() => setCustom(!custom)}
          className="flex min-h-[34px] w-full items-center gap-2.5 rounded-lg py-1.5 pr-2 pl-2.5 text-left text-body outline-none hover:bg-surface-active focus-visible:bg-surface-active"
        >
          <span className="flex-1">{t("tasks.snooze.custom")}</span>
          <Icon
            name={custom ? "chevron-down" : "chevron-right"}
            className="text-fg-muted"
          />
        </button>
      </div>
      {custom && (
        <Calendar
          min={todayYmd(tomorrow)}
          today={todayYmd(now)}
          onChange={(ymd) => choose(new Date(ymd + "T08:00:00"))}
        />
      )}
    </Popover>
  );
}
