// 日期工具：统一使用 'YYYY-MM-DD' 字符串，按日历日计算，不受时区和夏令时影响。

const YMD = /^(\d{4})-(\d{2})-(\d{2})$/;

export interface YmdParts {
  year: number;
  month: number; // 1-12
  day: number;
}

/** 解析 'YYYY-MM-DD'；格式或日历日不合法时返回 null。 */
export function parseYmd(value: string): YmdParts | null {
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

function utc(value: string): Date {
  const parts = parseYmd(value);
  if (!parts) throw new RangeError(`非法日期：${value}`);
  return new Date(Date.UTC(parts.year, parts.month - 1, parts.day));
}

function pad(n: number, width = 2): string {
  return String(n).padStart(width, "0");
}

function fromUtc(date: Date): string {
  return `${pad(date.getUTCFullYear(), 4)}-${pad(date.getUTCMonth() + 1)}-${pad(date.getUTCDate())}`;
}

/** 本地时区的「今天」。 */
export function todayYmd(now: Date = new Date()): string {
  return `${pad(now.getFullYear(), 4)}-${pad(now.getMonth() + 1)}-${pad(now.getDate())}`;
}

export function addDays(value: string, days: number): string {
  const date = utc(value);
  date.setUTCDate(date.getUTCDate() + days);
  return fromUtc(date);
}

/** 月份加减，返回该月 1 号。 */
export function addMonths(value: string, months: number): string {
  const date = utc(value);
  return fromUtc(
    new Date(Date.UTC(date.getUTCFullYear(), date.getUTCMonth() + months, 1)),
  );
}

export function startOfMonth(value: string): string {
  return `${value.slice(0, 7)}-01`;
}

/** 周一为 0，周日为 6。 */
export function weekdayIndex(value: string): number {
  return (utc(value).getUTCDay() + 6) % 7;
}

export interface QuickDates {
  today: string;
  tomorrow: string;
  /** 本周六；今天已是周六或周日时为下周六 */
  thisWeekend: string;
  /** 今天 + 7 天 */
  nextWeek: string;
  /** 本周末 + 7 天 */
  nextWeekend: string;
}

/** 日期选择器的快捷日期。周一是一周的第一天。 */
export function quickDates(today: string): QuickDates {
  const weekday = weekdayIndex(today);
  const thisWeekend = addDays(today, weekday < 5 ? 5 - weekday : 12 - weekday);
  return {
    today,
    tomorrow: addDays(today, 1),
    thisWeekend,
    nextWeek: addDays(today, 7),
    nextWeekend: addDays(thisWeekend, 7),
  };
}

/** 「周三, 10月28日」「Wed, Oct 28」。 */
export function formatDateLabel(value: string, locale: string): string {
  const date = utc(value);
  const weekday = new Intl.DateTimeFormat(locale, {
    weekday: "short",
    timeZone: "UTC",
  }).format(date);
  const monthDay = new Intl.DateTimeFormat(locale, {
    month: "short",
    day: "numeric",
    timeZone: "UTC",
  }).format(date);
  return `${weekday}, ${monthDay}`;
}

/** 月历标题，如「2026年10月」。 */
export function formatMonthTitle(value: string, locale: string): string {
  return new Intl.DateTimeFormat(locale, {
    year: "numeric",
    month: "long",
    timeZone: "UTC",
  }).format(utc(value));
}

/** 完整日期，用作无障碍名称，如「2026年10月28日星期三」。 */
export function formatFullDate(value: string, locale: string): string {
  return new Intl.DateTimeFormat(locale, {
    dateStyle: "full",
    timeZone: "UTC",
  }).format(utc(value));
}

/** 周一起始的 7 个星期名称（窄格式）。 */
export function weekdayLabels(locale: string): string[] {
  // 2024-01-01 是周一
  const format = new Intl.DateTimeFormat(locale, {
    weekday: "narrow",
    timeZone: "UTC",
  });
  return Array.from({ length: 7 }, (_, i) =>
    format.format(utc(addDays("2024-01-01", i))),
  );
}
