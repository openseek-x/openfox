// 「稍后处理」：预设时间 + 有限格式的自然语言解析（只在前端解析，后端只收绝对时间）。

const DEFAULT_HOUR = 8;
const LATER_TODAY_HOUR = 18;
const MINUTE = 60_000;

function at(base: Date, days: number, hour: number, minute = 0): Date {
  return new Date(
    base.getFullYear(),
    base.getMonth(),
    base.getDate() + days,
    hour,
    minute,
  );
}

export type SnoozePresetId =
  "oneHour" | "laterToday" | "tomorrow" | "nextMonday" | "nextMonth";

export interface SnoozePreset {
  id: SnoozePresetId;
  at: Date;
}

/** 一小时后、明天 08:00、下周一 08:00、一个月后 08:00；laterToday 时在一小时后之后加「今天 18:00」（已过则不出现）。 */
export function snoozePresets(
  now: Date = new Date(),
  laterToday = false,
): SnoozePreset[] {
  const daysToMonday = (8 - now.getDay()) % 7 || 7;
  const month = new Date(
    now.getFullYear(),
    now.getMonth() + 1,
    now.getDate(),
    DEFAULT_HOUR,
  );
  // 31 号加一个月会溢出到下下个月，回退到下个月月底
  if (month.getDate() !== now.getDate()) month.setDate(0);
  const evening = at(now, 0, LATER_TODAY_HOUR);
  return [
    { id: "oneHour", at: new Date(now.getTime() + 60 * MINUTE) },
    ...(laterToday && evening.getTime() > now.getTime() + 60 * MINUTE
      ? [{ id: "laterToday" as const, at: evening }]
      : []),
    { id: "tomorrow", at: at(now, 1, DEFAULT_HOUR) },
    { id: "nextMonday", at: at(now, daysToMonday, DEFAULT_HOUR) },
    { id: "nextMonth", at: month },
  ];
}

/** 分拣台的「下周」从下一个周日开始的一周算起；「一个月后」按本地日历进位。 */
export function triageSnoozePresets(now: Date): SnoozePreset[] {
  const month = new Date(now);
  month.setMonth(month.getMonth() + 1);
  month.setHours(DEFAULT_HOUR, 0, 0, 0);
  return [
    { id: "oneHour", at: new Date(now.getTime() + 60 * MINUTE) },
    { id: "tomorrow", at: at(now, 1, DEFAULT_HOUR) },
    { id: "nextMonday", at: at(now, 8 - now.getDay(), DEFAULT_HOUR) },
    { id: "nextMonth", at: month },
  ];
}

function validTime(hour: number, minute: number): boolean {
  return hour >= 0 && hour <= 23 && minute >= 0 && minute <= 59;
}

/** 按「上午/下午/晚上」「am/pm」折算成 24 小时制。 */
function to24(hour: number, period: string | undefined): number {
  switch (period) {
    case "下午":
    case "晚上":
    case "pm":
      return hour < 12 ? hour + 12 : hour;
    case "中午":
      return hour < 11 ? hour + 12 : hour;
    case "上午":
    case "早上":
    case "am":
      return hour === 12 ? 0 : hour;
    default:
      return hour;
  }
}

function addAmount(now: Date, amount: number, unit: "m" | "h" | "d" | "w") {
  if (unit === "m") return new Date(now.getTime() + amount * MINUTE);
  if (unit === "h") return new Date(now.getTime() + amount * 60 * MINUTE);
  const days = unit === "d" ? amount : amount * 7;
  return new Date(
    now.getFullYear(),
    now.getMonth(),
    now.getDate() + days,
    now.getHours(),
    now.getMinutes(),
  );
}

const ZH_UNITS: Record<string, "m" | "h" | "d" | "w"> = {
  分钟: "m",
  小时: "h",
  天: "d",
  周: "w",
};
const EN_UNITS: Record<string, "m" | "h" | "d" | "w"> = {
  m: "m",
  min: "m",
  mins: "m",
  minute: "m",
  minutes: "m",
  h: "h",
  hr: "h",
  hrs: "h",
  hour: "h",
  hours: "h",
  d: "d",
  day: "d",
  days: "d",
  w: "w",
  week: "w",
  weeks: "w",
};

/**
 * 解析自然语言，无法识别或结果不在未来时返回 null。支持：
 * - 「N 分钟/小时/天/周后」、in N minutes/hours/days/weeks
 * - 「今天/明天/后天 [上午|下午|晚上] H[点|:mm|点mm分]」，缺省时间按 08:00
 * - tomorrow [at] [h[:mm] am/pm]，缺省时间按 08:00
 * - YYYY-MM-DD [HH:mm]，缺省时间按 08:00
 */
export function parseSnooze(
  input: string,
  now: Date = new Date(),
): Date | null {
  const text = input.trim().toLowerCase().replace(/\s+/g, " ");
  if (!text) return null;
  const result = parse(text, now);
  return result && result.getTime() > now.getTime() ? result : null;
}

function parseClock(text: string) {
  const zhClock =
    /^(上午|下午|晚上|中午)?\s*(\d{1,2})\s*(?:点|时|[:：])\s*(\d{1,2})?分?$/.exec(
      text,
    );
  const enClock = /^(\d{1,2})(?::(\d{2}))?\s*(am|pm)$/.exec(text);
  if (zhClock || enClock) {
    const period = zhClock?.[1] ?? enClock?.[3];
    const rawHour = Number(zhClock?.[2] ?? enClock?.[1]);
    const hour = to24(rawHour, period);
    const minute = Number(zhClock?.[3] ?? enClock?.[2] ?? 0);
    if (!validTime(hour, minute) || (period && (rawHour < 1 || rawHour > 12)))
      return null;
    return { hour, minute };
  }
  return null;
}

function parse(text: string, now: Date): Date | null {
  const clock = parseClock(text);
  if (clock) {
    const time = at(now, 0, clock.hour, clock.minute);
    if (time <= now) time.setDate(time.getDate() + 1);
    return time;
  }
  if (text === "下周" || text === "next week")
    return at(now, (8 - now.getDay()) % 7 || 7, DEFAULT_HOUR);
  let m = /^(?:在\s*)?(\d+) ?(分钟|小时|天|周)(?:后|之后)?$/.exec(
    text.replace("个", ""),
  );
  if (m) return addAmount(now, Number(m[1]), ZH_UNITS[m[2]!]!);

  const duration = text.replace(/^in /, "");
  const token =
    /(\d+)\s*(weeks?|w|days?|d|hours?|hrs?|h|minutes?|mins?|m|seconds?|secs?|s)/g;
  const parts = [...duration.matchAll(token)];
  if (parts.length && !duration.replace(token, "").replace(/and|[\s,]/g, "")) {
    const ms = parts.reduce((sum, part) => {
      const unit = part[2]!;
      const multiplier = unit.startsWith("s")
        ? 1000
        : ({ m: 1, h: 60, d: 1440, w: 10080 }[EN_UNITS[unit]!] ?? 0) * MINUTE;
      return sum + Number(part[1]) * multiplier;
    }, 0);
    return new Date(now.getTime() + ms);
  }

  const weekday =
    /^(?:next )?(sun(?:day)?|mon(?:day)?|tue(?:s|sday)?|wed(?:nesday)?|thu(?:rs?|rsday)?|fri(?:day)?|sat(?:urday)?)(?: at (.+))?$/.exec(
      text,
    );
  if (weekday) {
    const index = ["sun", "mon", "tue", "wed", "thu", "fri", "sat"].indexOf(
      weekday[1]!.slice(0, 3),
    );
    const target = at(now, (index - now.getDay() + 7) % 7 || 7, DEFAULT_HOUR);
    if (!weekday[2]) return target;
    const time = parseClock(weekday[2]);
    if (!time) return null;
    target.setHours(time.hour, time.minute);
    return target;
  }
  if (text === "tonight" || text === "今晚") return at(now, 0, 20);
  m =
    /^(今天|明天|后天) ?(上午|下午|晚上|早上|中午)? ?(?:(\d{1,2})(?:点|时|:(\d{2}))?(?:(\d{1,2})分?)?)?$/.exec(
      text.replace(/点半$/, "点30分"),
    );
  if (m) {
    const days = m[1] === "今天" ? 0 : m[1] === "明天" ? 1 : 2;
    if (m[3] === undefined) {
      // 只写了时段（如「明天下午」）不够明确
      return m[2] ? null : at(now, days, DEFAULT_HOUR);
    }
    const hour = to24(Number(m[3]), m[2]);
    const minute = Number(m[4] ?? m[5] ?? 0);
    return validTime(hour, minute) ? at(now, days, hour, minute) : null;
  }

  m = /^tomorrow(?: (?:at )?(\d{1,2})(?::(\d{2}))? ?(am|pm)?)?$/.exec(text);
  if (m) {
    if (m[1] === undefined) return at(now, 1, DEFAULT_HOUR);
    const hour = to24(Number(m[1]), m[3]);
    const minute = Number(m[2] ?? 0);
    if (m[3] && (Number(m[1]) < 1 || Number(m[1]) > 12)) return null;
    return validTime(hour, minute) ? at(now, 1, hour, minute) : null;
  }

  m = /^(\d{4})-(\d{2})-(\d{2})(?:[ t](\d{1,2}):(\d{2}))?$/.exec(text);
  if (m) {
    const [year, month, day] = [Number(m[1]), Number(m[2]), Number(m[3])];
    const hour = m[4] === undefined ? DEFAULT_HOUR : Number(m[4]);
    const minute = Number(m[5] ?? 0);
    if (!validTime(hour, minute)) return null;
    const date = new Date(year, month - 1, day, hour, minute);
    // 拒绝 2 月 30 日这类会被自动进位的日期
    if (date.getMonth() !== month - 1 || date.getDate() !== day) return null;
    return date;
  }
  return null;
}
