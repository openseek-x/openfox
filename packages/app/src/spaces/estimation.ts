// 任务估时的刻度、显示与转换规则（纯函数，与后端校验保持一致）。

export type EstimationScale =
  | "notUsed"
  | "storyPoints"
  | "time"
  | "exponential"
  | "fibonacci"
  | "linear"
  | "tShirt";

/** 下拉顺序：未启用 | 故事点、时间 | 指数、斐波那契、线性、T 恤（null 表示分隔线）。 */
export const SCALE_MENU: Array<EstimationScale | null> = [
  "notUsed",
  null,
  "storyPoints",
  "time",
  null,
  "exponential",
  "fibonacci",
  "linear",
  "tShirt",
];

export type FixedScale = "exponential" | "fibonacci" | "linear" | "tShirt";

export interface EstimationOptions {
  extended?: boolean;
  allowZero?: boolean;
}

export interface EstimationSettings {
  scale: EstimationScale;
  extended: boolean;
  allow_zero: boolean;
  count_unestimated: boolean;
}

export interface ScalePoint {
  value: number;
  label: string;
}

const numeric = (values: number[]): ScalePoint[] =>
  values.map((value) => ({ value, label: String(value) }));

const BASE_POINTS: Record<FixedScale, ScalePoint[]> = {
  exponential: numeric([1, 2, 4, 8, 16]),
  fibonacci: numeric([1, 2, 3, 5, 8]),
  linear: numeric([1, 2, 3, 4, 5]),
  tShirt: [
    { value: 1, label: "XS" },
    { value: 2, label: "S" },
    { value: 3, label: "M" },
    { value: 5, label: "L" },
    { value: 8, label: "XL" },
  ],
};

const EXTENDED_POINTS: Record<FixedScale, ScalePoint[]> = {
  exponential: numeric([32, 64]),
  fibonacci: numeric([13, 21]),
  linear: numeric([6, 7]),
  tShirt: [
    { value: 13, label: "XXL" },
    { value: 21, label: "XXXL" },
  ],
};

export function isFixedScale(scale: EstimationScale): scale is FixedScale {
  return scale in BASE_POINTS;
}

export function estimationEnabled(scale: EstimationScale | undefined | null) {
  return !!scale && scale !== "notUsed";
}

/** 固定档位刻度的可选值（含 0 与扩展档）；故事点、时间、未启用返回空数组。 */
export function scalePoints(
  scale: EstimationScale,
  { extended = false, allowZero = false }: EstimationOptions = {},
): ScalePoint[] {
  if (!isFixedScale(scale)) return [];
  return [
    ...(allowZero ? [{ value: 0, label: scale === "tShirt" ? "-" : "0" }] : []),
    ...BASE_POINTS[scale],
    ...(extended ? EXTENDED_POINTS[scale] : []),
  ];
}

/** 下拉里括号内的序列，例如「1, 2, 3, 5, 8」。 */
export function scaleSequence(
  scale: EstimationScale,
  options: EstimationOptions = {},
): string {
  return scalePoints(scale, options)
    .map((point) => point.label)
    .join(", ");
}

// ---- 时间刻度：每天 8 小时、每周 5 天 ----

const HOURS_PER_DAY = 8;
const DAYS_PER_WEEK = 5;
const UNIT_SECONDS: Record<string, number> = {
  w: DAYS_PER_WEEK * HOURS_PER_DAY * 3600,
  d: HOURS_PER_DAY * 3600,
  h: 3600,
  m: 60,
};

/** 解析「2w 3d 4h 30m」为秒数；空串、非法格式返回 null。单位不区分大小写，可带小数。 */
export function parseDuration(input: string): number | null {
  const text = input.trim();
  if (!text) return null;
  let total = 0;
  let cursor = 0;
  let matched = false;
  for (const match of text.matchAll(/(\d+(?:\.\d+)?)\s*([wdhm])/gi)) {
    const index = match.index ?? 0;
    // 两段之间只能是空白
    if (text.slice(cursor, index).trim() !== "") return null;
    const amount = Number(match[1]);
    const unit = UNIT_SECONDS[match[2]!.toLowerCase()];
    if (!Number.isFinite(amount) || amount < 0 || unit === undefined)
      return null;
    total += amount * unit;
    cursor = index + match[0].length;
    matched = true;
  }
  if (!matched || text.slice(cursor).trim() !== "") return null;
  return Math.round(total);
}

/** 秒数显示为「2w 3d 4h 30m」；不足 1 分钟显示 0m。 */
export function formatDuration(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) return "";
  let rest = Math.round(seconds);
  const parts: string[] = [];
  for (const unit of ["w", "d", "h", "m"]) {
    const size = UNIT_SECONDS[unit]!;
    const count = Math.floor(rest / size);
    if (count > 0) {
      parts.push(`${count}${unit}`);
      rest %= size;
    }
  }
  return parts.length > 0 ? parts.join(" ") : "0m";
}

/** 任务上的估时值按空间刻度显示；未启用或无值返回空串。 */
export function formatEstimate(
  value: number | null | undefined,
  settings: Pick<EstimationSettings, "scale" | "extended" | "allow_zero">,
): string {
  if (value === null || value === undefined) return "";
  if (!estimationEnabled(settings.scale)) return "";
  if (settings.scale === "time") return formatDuration(value);
  const point = scalePoints(settings.scale, {
    extended: settings.extended,
    allowZero: settings.allow_zero,
  }).find((item) => item.value === value);
  return point?.label ?? String(value);
}

/** 校验某个值是否可以写入当前刻度（与后端 INVALID_ESTIMATE 规则一致）。 */
export function isValidEstimate(
  value: number,
  settings: Pick<EstimationSettings, "scale" | "extended" | "allow_zero">,
): boolean {
  if (!Number.isFinite(value) || value < 0 || value > 1e9) return false;
  if (!estimationEnabled(settings.scale)) return false;
  if (value === 0 && !settings.allow_zero && settings.scale !== "time")
    return false;
  if (settings.scale === "storyPoints")
    return Math.round(value * 100) === value * 100;
  if (settings.scale === "time") return Number.isInteger(value);
  return scalePoints(settings.scale, {
    extended: settings.extended,
    allowZero: settings.allow_zero,
  }).some((point) => point.value === value);
}

// ---- 切换刻度时的转换 ----

type Category = "time" | "free" | "fixed";
const category = (scale: EstimationScale): Category | null =>
  scale === "notUsed"
    ? null
    : scale === "time"
      ? "time"
      : scale === "storyPoints"
        ? "free"
        : "fixed";

/** 需要用户补充的换算方式：取整（自由点数 → 固定档位）或「1 点 = 时长」（点数 ↔ 时间）。 */
export type ManualMethod = "round" | "pointsToTime" | "timeToPoints";

export interface ConversionPlan {
  /** noop：值都能原样保留；convert：需要提交 convert；disable：切到未启用，不改任务 */
  kind: "noop" | "convert" | "disable";
  /** 能自动确定的映射（to = null 表示清空） */
  auto: Array<{ from: number; to: number | null }>;
  /** 需要用户选择换算方式的值 */
  manual: number[];
  method: ManualMethod | null;
}

/** 固定档位（不含 0）里的序号（从 1 开始），落在两档之间时取较低档。 */
function tierIndex(
  scale: FixedScale,
  value: number,
  options: EstimationOptions,
): number {
  const values = scalePoints(scale, { ...options, allowZero: false }).map(
    (point) => point.value,
  );
  const index = values.indexOf(value);
  if (index >= 0) return index + 1;
  return Math.max(1, values.filter((item) => value >= item).length);
}

function tierValue(
  scale: FixedScale,
  index: number,
  options: EstimationOptions,
): number {
  const values = scalePoints(scale, { ...options, allowZero: false }).map(
    (point) => point.value,
  );
  return values[Math.min(Math.max(index, 1), values.length) - 1] ?? 0;
}

const zeroTarget = (to: EstimationScale, options: EstimationOptions) =>
  to === "time" || options.allowZero ? 0 : null;

/**
 * 计算从一个刻度切到另一个刻度时已有估时值的去向：
 * - 固定档位 ↔ 固定档位：按档位序号对齐（第 n 档 → 第 n 档）。
 * - 固定 / 自由 → 自由点数：原值保留。
 * - 自由点数 → 固定档位：需要选择取整方式。
 * - 点数 ↔ 时间：需要填写「1 点 =」的时长。
 * - 0 在目标不允许零时清空。
 */
export function planConversion(input: {
  from: EstimationSettings;
  to: EstimationSettings;
  values: number[];
}): ConversionPlan {
  const { from, to } = input;
  const values = [...new Set(input.values)]
    .filter((value) => Number.isFinite(value))
    .sort((a, b) => a - b);
  if (to.scale === "notUsed")
    return { kind: "disable", auto: [], manual: [], method: null };
  // 原来未启用时，存量值（例如以前设置过）按目标刻度解释
  const source = estimationEnabled(from.scale) ? from.scale : to.scale;
  const fromOptions = { extended: from.extended, allowZero: from.allow_zero };
  const toOptions = { extended: to.extended, allowZero: to.allow_zero };
  const fromCategory = category(source)!;
  const toCategory = category(to.scale)!;
  const auto: ConversionPlan["auto"] = [];
  const manual: number[] = [];
  let method: ManualMethod | null = null;
  for (const value of values) {
    if (fromCategory === toCategory && toCategory !== "fixed") {
      auto.push({ from: value, to: value });
    } else if (fromCategory === "fixed" && toCategory === "fixed") {
      auto.push({
        from: value,
        to:
          value <= 0
            ? zeroTarget(to.scale, toOptions)
            : tierValue(
                to.scale as FixedScale,
                tierIndex(source as FixedScale, value, fromOptions),
                toOptions,
              ),
      });
    } else if (toCategory === "free" && fromCategory === "fixed") {
      auto.push({
        from: value,
        to: value <= 0 ? zeroTarget(to.scale, toOptions) : value,
      });
    } else {
      manual.push(value);
      method =
        toCategory === "time"
          ? "pointsToTime"
          : fromCategory === "time"
            ? "timeToPoints"
            : "round";
    }
  }
  const changed =
    manual.length > 0 || auto.some((item) => item.to !== item.from);
  return { kind: changed ? "convert" : "noop", auto, manual, method };
}

export type RoundingMode = "nearest" | "ceil" | "floor";

/** 把任意数贴到固定档位（不含 0）：超出两端取端点。 */
export function snapToScale(
  scale: FixedScale,
  value: number,
  mode: RoundingMode,
  options: EstimationOptions = {},
): number {
  const values = scalePoints(scale, { ...options, allowZero: false }).map(
    (point) => point.value,
  );
  const first = values[0]!;
  const last = values[values.length - 1]!;
  if (value <= first) return first;
  if (value >= last) return last;
  if (mode === "ceil") return values.find((item) => item >= value) ?? last;
  if (mode === "floor") return values.filter((item) => item <= value).pop()!;
  let best = first;
  let distance = Infinity;
  for (const item of values) {
    const gap = Math.abs(item - value);
    // 距离相同时取较大档。
    if (gap <= distance) {
      distance = gap;
      best = item;
    }
  }
  return best;
}

/**
 * 按用户选择的换算方式补齐 manual 部分，得到完整的 convert 数组。
 * secondsPerPoint：点数 ↔ 时间时「1 点 = n 秒」。
 */
export function resolveConversion(
  plan: ConversionPlan,
  target: EstimationSettings,
  choice: { rounding?: RoundingMode; secondsPerPoint?: number },
): Array<{ from: number; to: number | null }> {
  const toOptions = {
    extended: target.extended,
    allowZero: target.allow_zero,
  };
  const manual = plan.manual.map((value) => {
    if (value <= 0)
      return { from: value, to: zeroTarget(target.scale, toOptions) };
    if (plan.method === "pointsToTime") {
      const seconds = choice.secondsPerPoint ?? 0;
      return {
        from: value,
        to: seconds > 0 ? Math.round(value * seconds) : null,
      };
    }
    let points = value;
    if (plan.method === "timeToPoints") {
      const seconds = choice.secondsPerPoint ?? 0;
      if (seconds <= 0) return { from: value, to: null };
      points = Math.round((value / seconds) * 100) / 100;
    }
    if (isFixedScale(target.scale))
      return {
        from: value,
        to: snapToScale(
          target.scale,
          points,
          choice.rounding ?? "nearest",
          toOptions,
        ),
      };
    return { from: value, to: points };
  });
  return [...plan.auto, ...manual]
    .filter((item) => item.to !== item.from)
    .sort((a, b) => a.from - b.from);
}
