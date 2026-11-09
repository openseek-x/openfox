// 定时触发器的 cron 计算（纯函数，便于测试）。
// 契约规则：cron 为 5 段（分 时 日 月 周），分钟必须是单个数字，即最短 1 小时一次。
// 时区按 IANA 名称处理，运行时间用 Intl 按目标时区拆分本地时间，不依赖第三方库。

/** 时区下拉的 18 个常用时区；不在列表里的浏览器时区会被追加进去。 */
export const COMMON_TIMEZONES = [
  "Asia/Shanghai",
  "Asia/Hong_Kong",
  "Asia/Taipei",
  "Asia/Tokyo",
  "Asia/Seoul",
  "Asia/Singapore",
  "Asia/Kolkata",
  "Asia/Dubai",
  "Europe/London",
  "Europe/Paris",
  "Europe/Berlin",
  "Europe/Moscow",
  "America/New_York",
  "America/Chicago",
  "America/Denver",
  "America/Los_Angeles",
  "Australia/Sydney",
  "Pacific/Auckland",
] as const;

/** 预览的运行次数。 */
export const PREVIEW_COUNT = 5;

/** 向后搜索的上限（小时）。足够覆盖每周、每月类 cron 的下一次运行。 */
const SEARCH_HOURS = 24 * 62;

interface FieldRange {
  min: number;
  max: number;
}

// 周字段允许 7 表示周日，解析后统一映射为 0
const RANGES: Record<"hour" | "day" | "month" | "weekday", FieldRange> = {
  hour: { min: 0, max: 23 },
  day: { min: 1, max: 31 },
  month: { min: 1, max: 12 },
  weekday: { min: 0, max: 7 },
};

/** 解析单个 cron 字段（支持 *、*\/n、a-b、a-b/n、列表与单个数字）。非法时返回 null。 */
function parseField(field: string, range: FieldRange): Set<number> | null {
  const values = new Set<number>();
  for (const part of field.split(",")) {
    const match = /^(\*|\d+(?:-\d+)?)(?:\/(\d+))?$/.exec(part);
    if (!match) return null;
    const [, base = "", stepText] = match;
    const step = stepText === undefined ? 1 : Number(stepText);
    if (step < 1) return null;
    let from = range.min;
    let to = range.max;
    if (base !== "*") {
      const [startText, endText] = base.split("-");
      from = Number(startText);
      to =
        endText === undefined
          ? stepText === undefined
            ? from
            : range.max
          : Number(endText);
    }
    if (from < range.min || to > range.max || from > to) return null;
    for (let value = from; value <= to; value += step) values.add(value);
  }
  return values;
}

export interface ParsedCron {
  minute: number;
  hours: Set<number>;
  days: Set<number>;
  months: Set<number>;
  weekdays: Set<number>;
  /** 日与周都受限时按 cron 惯例取或关系 */
  dayRestricted: boolean;
  weekdayRestricted: boolean;
}

/** 校验并解析 cron。返回的错误键由调用方映射为文案。 */
export function parseCron(
  expression: string,
): { ok: true; value: ParsedCron } | { ok: false; error: "format" | "minute" } {
  const fields = expression.trim().split(/\s+/);
  if (fields.length !== 5) return { ok: false, error: "format" };
  const [
    minuteField = "",
    hourField = "",
    dayField = "",
    monthField = "",
    weekdayField = "",
  ] = fields;
  // 分钟必须是单个数字：最短 1 小时一次
  if (!/^\d+$/.test(minuteField)) return { ok: false, error: "minute" };
  const minute = Number(minuteField);
  if (minute > 59) return { ok: false, error: "minute" };
  const hours = parseField(hourField, RANGES.hour);
  const days = parseField(dayField, RANGES.day);
  const months = parseField(monthField, RANGES.month);
  const weekdayValues = parseField(weekdayField, RANGES.weekday);
  if (!hours || !days || !months || !weekdayValues)
    return { ok: false, error: "format" };
  const weekdays = new Set([...weekdayValues].map((day) => day % 7));
  return {
    ok: true,
    value: {
      minute,
      hours,
      days,
      months,
      weekdays,
      dayRestricted: dayField !== "*",
      weekdayRestricted: weekdayField !== "*",
    },
  };
}

const formatters = new Map<string, Intl.DateTimeFormat>();

/** 按时区拆分本地时间；时区不存在时 Intl 会抛错，由调用方处理。 */
function localParts(date: Date, timezone: string) {
  let formatter = formatters.get(timezone);
  if (!formatter) {
    formatter = new Intl.DateTimeFormat("en-US", {
      timeZone: timezone,
      hourCycle: "h23",
      year: "numeric",
      month: "numeric",
      day: "numeric",
      hour: "numeric",
      minute: "numeric",
      weekday: "short",
    });
    formatters.set(timezone, formatter);
  }
  const parts = Object.fromEntries(
    formatter.formatToParts(date).map((part) => [part.type, part.value]),
  );
  const weekday = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"].indexOf(
    parts.weekday ?? "",
  );
  return {
    month: Number(parts.month),
    day: Number(parts.day),
    hour: Number(parts.hour),
    minute: Number(parts.minute),
    weekday,
  };
}

/** 判断某个本地时间是否满足 cron（日与周按或关系合并）。 */
function matches(
  cron: ParsedCron,
  local: ReturnType<typeof localParts>,
): boolean {
  if (local.minute !== cron.minute) return false;
  if (!cron.hours.has(local.hour) || !cron.months.has(local.month))
    return false;
  const dayOk = cron.days.has(local.day);
  const weekdayOk = cron.weekdays.has(local.weekday);
  if (cron.dayRestricted && cron.weekdayRestricted) return dayOk || weekdayOk;
  return dayOk && weekdayOk;
}

/**
 * 计算 from 之后（不含 from 本身）的下 count 次运行时间。
 * 运行时间只落在 cron 的分钟上；逐小时向后搜索，时区偏移为整点或半点时仍成立。
 * 表达式非法或时区无效时返回空数组。
 */
export function nextRuns(
  expression: string,
  timezone: string,
  from: Date,
  count = PREVIEW_COUNT,
): Date[] {
  const parsed = parseCron(expression);
  if (!parsed.ok) return [];
  const cron = parsed.value;
  let cursor: number;
  try {
    // 从 from 之后的第一个 cron 分钟开始
    cursor = Math.floor(from.getTime() / 60_000) * 60_000 + 60_000;
    const runs: Date[] = [];
    // 先在一小时内对齐到 cron 的分钟（只看分钟，小时与日期交给后面的匹配），之后每次前进一小时
    for (let step = 0; step < 60; step++) {
      if (localParts(new Date(cursor), timezone).minute === cron.minute) break;
      cursor += 60_000;
    }
    for (let step = 0; step < SEARCH_HOURS && runs.length < count; step++) {
      const at = new Date(cursor);
      if (matches(cron, localParts(at, timezone))) runs.push(at);
      cursor += 60 * 60_000;
    }
    return runs;
  } catch {
    return [];
  }
}

/** 把频率选项转换成 cron；自定义时直接用输入的表达式。 */
export function cronFor(input: {
  frequency: "hourly" | "daily" | "weekdays" | "weekly" | "custom";
  minute: number;
  time: string;
  weekdays: number[];
  cron: string;
}): string {
  // 时间为 HH:mm，去掉前导零后写入 cron
  const [hour = "0", minute = "0"] = input.time
    .split(":")
    .map((part) => String(Number(part)));
  switch (input.frequency) {
    case "hourly":
      return `${input.minute} * * * *`;
    case "daily":
      return `${minute} ${hour} * * *`;
    case "weekdays":
      return `${minute} ${hour} * * 1-5`;
    case "weekly": {
      const days = [...input.weekdays].sort((a, b) => a - b).join(",");
      return `${minute} ${hour} * * ${days}`;
    }
    case "custom":
      return input.cron;
  }
}
