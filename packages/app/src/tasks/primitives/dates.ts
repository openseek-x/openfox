import { parseYmd, todayYmd } from "@openfox/ui";

export {
  formatDateLabel,
  quickDates,
  todayYmd,
  type QuickDates,
} from "@openfox/ui";

type Loc = string;

/** 「10月28日」「Oct 28」；传入 'YYYY-MM-DD' 或 Date（按本地日期）。 */
export function formatMonthDay(value: string | Date, locale: Loc): string {
  const ymd = typeof value === "string" ? value.slice(0, 10) : todayYmd(value);
  const parts = parseYmd(ymd);
  if (!parts) return "";
  return new Intl.DateTimeFormat(locale, {
    month: "short",
    day: "numeric",
    timeZone: "UTC",
  }).format(new Date(Date.UTC(parts.year, parts.month - 1, parts.day)));
}

const UNITS: Array<[Intl.RelativeTimeFormatUnit, number]> = [
  ["year", 365 * 24 * 3600],
  ["month", 30 * 24 * 3600],
  ["week", 7 * 24 * 3600],
  ["day", 24 * 3600],
  ["hour", 3600],
  ["minute", 60],
  ["second", 1],
];

/** 相对时间：「3 分钟前」「in 2 days」「明天」。 */
export function formatRelativeTime(
  value: string | Date,
  locale: Loc,
  now: Date = new Date(),
): string {
  const formatter = new Intl.RelativeTimeFormat(locale, { numeric: "auto" });
  const diff = Math.round((new Date(value).getTime() - now.getTime()) / 1000);
  for (const [unit, seconds] of UNITS) {
    if (Math.abs(diff) >= seconds || unit === "second")
      return formatter.format(Math.trunc(diff / seconds), unit);
  }
  return formatter.format(0, "second");
}

/** 截止日是否已过：早于今天（按 'YYYY-MM-DD' 比较）。 */
export function isOverdue(due: string, today: string = todayYmd()): boolean {
  return due < today;
}
