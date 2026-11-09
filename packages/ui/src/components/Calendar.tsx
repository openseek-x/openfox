import { useEffect, useRef, useState, type KeyboardEvent } from "react";

import { useI18n } from "@openfox/i18n";

import { cn } from "../cn";
import {
  addDays,
  addMonths,
  formatFullDate,
  formatMonthTitle,
  parseYmd,
  startOfMonth,
  todayYmd,
  weekdayIndex,
  weekdayLabels,
} from "../date";
import { Icon } from "./Icon";

export interface CalendarProps {
  /** 选中的日期 'YYYY-MM-DD' */
  value?: string | null;
  onChange: (value: string) => void;
  /** 「今天」，默认取本机日期；测试时可固定 */
  today?: string;
  min?: string;
  /** Inclusive start/end to highlight while choosing an interval. */
  range?: readonly [string, string];
  className?: string;
}

/** 单月日历：周一起始，上下月切换，方向键 / PageUp / PageDown 移动焦点。 */
export function Calendar({
  value,
  onChange,
  today,
  min,
  range,
  className,
}: CalendarProps) {
  const { t, locale } = useI18n();
  const todayValue = today ?? todayYmd();
  const selected = value && parseYmd(value) ? value : null;
  const initial = selected ?? (min && todayValue < min ? min : todayValue);
  const [focused, setFocused] = useState(initial);
  const [month, setMonth] = useState(startOfMonth(initial));
  const moved = useRef(false);
  const grid = useRef<HTMLDivElement | null>(null);

  // 键盘移动后把真实焦点交给新的日期按钮
  useEffect(() => {
    if (!moved.current) return;
    moved.current = false;
    grid.current
      ?.querySelector<HTMLElement>(`[data-date="${focused}"]`)
      ?.focus();
  }, [focused, month]);

  const moveFocus = (next: string) => {
    if (min && next < min) next = min;
    moved.current = true;
    setFocused(next);
    setMonth(startOfMonth(next));
  };

  const shiftMonth = (delta: number) => {
    const nextMonth = addMonths(month, delta);
    setMonth(nextMonth);
    // 保持日号，超出当月天数时取月末
    const day = Number(focused.slice(8));
    const last = Number(addDays(addMonths(nextMonth, 1), -1).slice(8));
    setFocused(
      `${nextMonth.slice(0, 8)}${String(Math.min(day, last)).padStart(2, "0")}`,
    );
  };

  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    const step: Record<string, number> = {
      ArrowLeft: -1,
      ArrowRight: 1,
      ArrowUp: -7,
      ArrowDown: 7,
    };
    if (event.key in step) {
      event.preventDefault();
      moveFocus(addDays(focused, step[event.key]!));
    } else if (event.key === "PageUp" || event.key === "PageDown") {
      event.preventDefault();
      const delta = event.key === "PageUp" ? -1 : 1;
      const target = addMonths(focused, delta);
      const day = Number(focused.slice(8));
      const last = Number(addDays(addMonths(target, 1), -1).slice(8));
      moveFocus(
        `${target.slice(0, 8)}${String(Math.min(day, last)).padStart(2, "0")}`,
      );
    }
  };

  // 6 行 × 7 列，从包含当月 1 号的那一周的周一开始
  const first = addDays(month, -weekdayIndex(month));
  const days = Array.from({ length: 42 }, (_, i) => addDays(first, i));
  const weekdays = weekdayLabels(locale);

  return (
    <div className={cn("w-[252px] p-2", className)}>
      <div className="mb-1 flex items-center justify-between">
        <button
          type="button"
          aria-label={t("ui.calendar.prevMonth")}
          onClick={() => shiftMonth(-1)}
          className="flex size-7 items-center justify-center rounded-md text-fg-muted hover:bg-surface-active"
        >
          <Icon name="chevron-left" />
        </button>
        <div aria-live="polite" className="text-body font-medium">
          {formatMonthTitle(month, locale)}
        </div>
        <button
          type="button"
          aria-label={t("ui.calendar.nextMonth")}
          onClick={() => shiftMonth(1)}
          className="flex size-7 items-center justify-center rounded-md text-fg-muted hover:bg-surface-active"
        >
          <Icon name="chevron-right" />
        </button>
      </div>
      <div
        ref={grid}
        role="grid"
        aria-label={formatMonthTitle(month, locale)}
        onKeyDown={onKeyDown}
      >
        <div role="row" className="grid grid-cols-7">
          {weekdays.map((name, i) => (
            <div
              key={i}
              role="columnheader"
              className="flex h-7 items-center justify-center text-caption text-fg-muted"
            >
              {name}
            </div>
          ))}
        </div>
        {Array.from({ length: 6 }, (_, row) => (
          <div key={row} role="row" className="grid grid-cols-7">
            {days.slice(row * 7, row * 7 + 7).map((day) => {
              const inMonth = day.slice(0, 7) === month.slice(0, 7);
              const inRange = !!range && day >= range[0] && day <= range[1];
              const isSelected =
                day === selected ||
                (inRange && (day === range[0] || day === range[1]));
              const isToday = day === todayValue;
              return (
                <div
                  key={day}
                  role="gridcell"
                  aria-selected={isSelected || inRange}
                >
                  <button
                    type="button"
                    data-date={day}
                    disabled={Boolean(min && day < min)}
                    tabIndex={day === focused ? 0 : -1}
                    aria-label={formatFullDate(day, locale)}
                    aria-current={isToday ? "date" : undefined}
                    onClick={() => onChange(day)}
                    className={cn(
                      "disabled:pointer-events-none disabled:opacity-35 mx-auto flex size-8 items-center justify-center rounded-lg text-body-sm",
                      isSelected
                        ? "bg-primary font-medium text-fg-inverse"
                        : cn(
                            "hover:bg-surface-active",
                            inMonth ? "text-fg-default" : "text-fg-subtle",
                            inRange && "rounded-none bg-surface-active",
                            isToday && "font-medium text-accent",
                          ),
                    )}
                  >
                    {Number(day.slice(8))}
                  </button>
                </div>
              );
            })}
          </div>
        ))}
      </div>
    </div>
  );
}
