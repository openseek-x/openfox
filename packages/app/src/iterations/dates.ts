// 迭代日期按空间时区的日历日计算，避免用本机时区参与周起始与冷却判断。

const YMD = /^(\d{4})-(\d{2})-(\d{2})$/;

export interface CalendarDate {
  year: number;
  month: number;
  day: number;
}

export function parseCalendarDate(value: string): CalendarDate | null {
  const match = YMD.exec(value);
  if (!match) return null;
  const year = Number(match[1]);
  const month = Number(match[2]);
  const day = Number(match[3]);
  const date = new Date(Date.UTC(year, month - 1, day));
  if (
    date.getUTCFullYear() !== year ||
    date.getUTCMonth() !== month - 1 ||
    date.getUTCDate() !== day
  )
    return null;
  return { year, month, day };
}

function toUtc(value: string): Date {
  const parsed = parseCalendarDate(value);
  if (!parsed) throw new RangeError(`非法日期：${value}`);
  return new Date(Date.UTC(parsed.year, parsed.month - 1, parsed.day));
}

function fromUtc(value: Date): string {
  const pad = (part: number, width = 2) => String(part).padStart(width, "0");
  return `${pad(value.getUTCFullYear(), 4)}-${pad(value.getUTCMonth() + 1)}-${pad(value.getUTCDate())}`;
}

export function addCalendarDays(value: string, days: number): string {
  const date = toUtc(value);
  date.setUTCDate(date.getUTCDate() + days);
  return fromUtc(date);
}

/** 0=周日，与后端 iteration_start_weekday 保持一致。 */
export function weekday(value: string): number {
  return toUtc(value).getUTCDay();
}

/** 返回今天或之后最近一个指定星期的日期。 */
export function nextWeekday(value: string, target: number): string {
  if (!Number.isInteger(target) || target < 0 || target > 6)
    throw new RangeError(`非法星期：${target}`);
  return addCalendarDays(value, (target - weekday(value) + 7) % 7);
}

export interface IterationWindow {
  startDate: string;
  endDate: string;
  nextStartDate: string;
}

/** 以 start_date 为起点计算迭代结束日与冷却后的下一迭代起点。 */
export function iterationWindow(
  startDate: string,
  durationWeeks: number,
  cooldownWeeks: number,
): IterationWindow {
  if (
    !Number.isInteger(durationWeeks) ||
    durationWeeks < 1 ||
    durationWeeks > 8
  )
    throw new RangeError(`非法迭代时长：${durationWeeks}`);
  if (
    !Number.isInteger(cooldownWeeks) ||
    cooldownWeeks < 0 ||
    cooldownWeeks > 3
  )
    throw new RangeError(`非法冷却时长：${cooldownWeeks}`);
  const endDate = addCalendarDays(startDate, durationWeeks * 7);
  return {
    startDate,
    endDate,
    nextStartDate: addCalendarDays(endDate, cooldownWeeks * 7),
  };
}

export function formatIterationRange(
  startDate: string,
  endDate: string,
  locale: string,
  timeZone: string,
): string {
  const start = parseCalendarDate(startDate);
  const end = parseCalendarDate(endDate);
  if (!start || !end) return `${startDate} – ${endDate}`;
  const formatter = new Intl.DateTimeFormat(locale, {
    month: "short",
    day: "numeric",
    timeZone,
  });
  // noon UTC avoids a date changing at the common negative offsets.
  const asNoon = (date: CalendarDate) =>
    new Date(Date.UTC(date.year, date.month - 1, date.day, 12));
  return `${formatter.format(asNoon(start))} – ${formatter.format(asNoon(end))}`;
}

export function isInIteration(
  date: string,
  startDate: string,
  endDate: string,
): boolean {
  return date >= startDate && date < endDate;
}
