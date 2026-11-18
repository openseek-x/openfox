import { useId, useRef, useEffect, useState, type RefObject } from "react";
import { useI18n, useT } from "@openfox/i18n";
import { Calendar, Icon, Popover, todayYmd } from "@openfox/ui";
import { triageSnoozePresets, parseSnooze } from "../tasks/primitives/snooze";

interface Props {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  anchorRef: RefObject<HTMLElement | null>;
  onSnooze: (date: Date) => void;
  onUnsnooze?: () => void;
}
export function TriageSnoozeMenu({
  open,
  onOpenChange,
  anchorRef,
  ...actions
}: Props) {
  return (
    <Popover
      open={open}
      onOpenChange={onOpenChange}
      anchorRef={anchorRef}
      placement="bottom-end"
      className="w-72 p-0!"
    >
      {open && <TimeChoices {...actions} close={() => onOpenChange(false)} />}
    </Popover>
  );
}
function TimeChoices({
  onSnooze,
  onUnsnooze,
  close,
}: Pick<Props, "onSnooze" | "onUnsnooze"> & { close: () => void }) {
  const t = useT(),
    { locale } = useI18n(),
    list = useId();
  const [now] = useState(() => new Date()),
    [text, setText] = useState(""),
    [active, setActive] = useState(-1),
    [calendar, setCalendar] = useState(false);
  const cal = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (calendar)
      cal.current
        ?.querySelector<HTMLButtonElement>("[data-date]:not(:disabled)")
        ?.focus();
  }, [calendar]);
  const input = useRef<HTMLInputElement>(null);
  useEffect(() => {
    const frame = requestAnimationFrame(() => input.current?.focus());
    return () => cancelAnimationFrame(frame);
  }, []);
  const presets = triageSnoozePresets(now);
  const tomorrow = new Date(now);
  tomorrow.setDate(now.getDate() + 1);
  const describe = (d: Date) => {
    const day = todayYmd(d);
    const label =
      day === todayYmd(now)
        ? t("spaces.triage.today")
        : day === todayYmd(tomorrow)
          ? t("spaces.triage.tomorrow")
          : d.getTime() - now.getTime() < 7 * 86400000
            ? d.toLocaleDateString(locale, { weekday: "long" })
            : d.toLocaleDateString(locale, { weekday: "short" }) +
              ", " +
              d.toLocaleDateString(locale, { month: "short", day: "numeric" });
    const clock = d
      .toLocaleTimeString(locale, {
        hour: "numeric",
        minute: "2-digit",
        hour12: true,
      })
      .replace(/^(上午|下午)(?=\d)/, "$1 ");
    return label + " · " + clock;
  };
  const parsed = text.trim() ? parseSnooze(text, now) : null;
  const choose = (d: Date) => {
    onSnooze(d);
    close();
  };
  const row =
    "flex min-h-8 w-full items-center gap-2.5 rounded-md px-3 py-1.5 text-left text-body hover:bg-surface-hover aria-selected:bg-surface-active";
  return (
    <>
      <div className="p-2">
        <input
          ref={input}
          value={text}
          maxLength={256}
          aria-label={t("spaces.triage.chooseTime")}
          placeholder={t("spaces.triage.timePlaceholder")}
          role="combobox"
          aria-expanded
          aria-autocomplete="list"
          aria-controls={list}
          aria-activedescendant={active >= 0 ? list + active : undefined}
          className="h-9 w-full rounded-md border border-line-default px-3 text-body outline-none"
          onChange={(e) => {
            setText(e.target.value);
            setActive(-1);
            setCalendar(false);
          }}
          onKeyDown={(e) => {
            const delta =
                e.key === "ArrowDown" ? 1 : e.key === "ArrowUp" ? -1 : 0,
              count = presets.length + 1;
            if (!text.trim() && delta) {
              e.preventDefault();
              setActive((n) =>
                n < 0
                  ? delta === 1
                    ? 0
                    : count - 1
                  : (n + delta + count) % count,
              );
            } else if (e.key === "Enter" && !e.nativeEvent.isComposing) {
              e.preventDefault();
              if (parsed) choose(parsed);
              else if (!text.trim() && presets[active])
                choose(presets[active]!.at);
              else if (!text.trim() && active === presets.length)
                setCalendar(true);
            }
          }}
        />
      </div>
      <div
        id={list}
        role="listbox"
        className="border-t border-line-default px-1 py-1"
      >
        {text.trim() ? (
          parsed ? (
            <button
              role="option"
              aria-selected={false}
              className={row}
              onClick={() => choose(parsed)}
            >
              {describe(parsed)}
              <kbd className="ml-auto">↵</kbd>
            </button>
          ) : (
            <p className="px-3 py-2 text-caption text-fg-muted">
              {t("spaces.triage.noTime")}
            </p>
          )
        ) : (
          <>
            {presets.map((item, i) => (
              <button
                key={item.id}
                id={list + i}
                role="option"
                aria-selected={active === i}
                className={row}
                onClick={() => choose(item.at)}
              >
                <Icon name={i === 0 ? "clock" : "calendar"} size={14} />
                {item.id === "oneHour"
                  ? t("tasks.snooze.oneHour")
                  : t(
                      item.id === "nextMonday"
                        ? "spaces.triage.nextWeek"
                        : item.id === "nextMonth"
                          ? "spaces.triage.nextMonth"
                          : "spaces.triage.tomorrow",
                    )}
                <span className="ml-auto text-caption text-fg-muted">
                  {describe(item.at)}
                </span>
              </button>
            ))}
            <button
              role="option"
              aria-selected={active === presets.length}
              aria-expanded={calendar}
              id={list + presets.length}
              className={row}
              onClick={() => setCalendar((v) => !v)}
            >
              <Icon name="calendar" size={14} />
              {t("spaces.triage.customTime")}
              <Icon
                name={calendar ? "chevron-down" : "chevron-right"}
                size={14}
                className="ml-auto"
              />
            </button>
          </>
        )}
      </div>
      {calendar && (
        <div ref={cal} className="border-t border-line-default p-2">
          <Calendar
            min={todayYmd(tomorrow)}
            onChange={(ymd) => choose(new Date(ymd + "T08:00:00"))}
          />
        </div>
      )}
      {!text.trim() && onUnsnooze && (
        <button
          type="button"
          className={row + " border-t border-line-default"}
          onClick={() => {
            onUnsnooze();
            close();
          }}
        >
          {t("spaces.triage.unsnooze")}
        </button>
      )}
    </>
  );
}
