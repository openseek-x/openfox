// 甘特图的纯逻辑：日期换算、时间轴范围、拖动计算。不依赖 React，便于单测。
// 日期一律用 'YYYY-MM-DD' 字符串，内部换成「自 1970-01-01 起的天数」做加减，避开夏令时和时区。

import type { Task } from "@openfox/api";

export type GanttScale = "day" | "week" | "month";

/** 每个刻度下一天占的像素宽度 */
export const DAY_WIDTH: Record<GanttScale, number> = {
  day: 40,
  week: 14,
  month: 4,
};

/** 甘特图最多加载的任务数；超出时提示缩小筛选范围 */
export const MAX_GANTT_TASKS = 200;

const DAY_MS = 86_400_000;

/** 'YYYY-MM-DD' → 天数；格式不对返回 null。 */
export function toDay(ymd: string | null | undefined): number | null {
  if (!ymd) return null;
  const match = /^(\d{4})-(\d{2})-(\d{2})/.exec(ymd);
  if (!match) return null;
  const time = Date.UTC(
    Number(match[1]),
    Number(match[2]) - 1,
    Number(match[3]),
  );
  return Number.isNaN(time) ? null : Math.round(time / DAY_MS);
}

export function fromDay(day: number): string {
  return new Date(day * DAY_MS).toISOString().slice(0, 10);
}

/** 一个任务在时间轴上的区间（含首尾两天）。 */
export interface BarRange {
  start: number;
  end: number;
}

/**
 * 任务的条形区间：开始、截止都有 → 整段；只有一个 → 单日；都没有 → null（未排期）。
 * 数据上 start <= due 由服务端保证；这里仍对反序数据取小大，避免画出负宽度。
 */
export function barRange(
  task: Pick<Task, "start_date" | "due_date">,
): BarRange | null {
  const start = toDay(task.start_date);
  const due = toDay(task.due_date);
  if (start === null && due === null) return null;
  const a = start ?? due!;
  const b = due ?? start!;
  return { start: Math.min(a, b), end: Math.max(a, b) };
}

export type DragMode = "move" | "start" | "end";

/** 按拖动的天数偏移计算新区间：move 整体平移；start / end 只动一端且不越过另一端。 */
export function applyDrag(
  range: BarRange,
  mode: DragMode,
  delta: number,
): BarRange {
  if (mode === "move")
    return { start: range.start + delta, end: range.end + delta };
  if (mode === "start")
    return { start: Math.min(range.start + delta, range.end), end: range.end };
  return { start: range.start, end: Math.max(range.end + delta, range.start) };
}

/**
 * 把拖动后的区间换成要提交的日期字段。只提交真正变化的字段：
 * - 原本只有截止日（或只有开始日）的任务整体平移时，只改那一个日期，不凭空生成另一端；
 * - 拉伸某一端时只改该端。
 * 没有变化返回 null。
 */
export function dragPatch(
  task: Pick<Task, "start_date" | "due_date">,
  next: BarRange,
  mode: DragMode,
): { start_date?: string; due_date?: string } | null {
  const hadStart = toDay(task.start_date) !== null;
  const hadDue = toDay(task.due_date) !== null;
  const patch: { start_date?: string; due_date?: string } = {};
  const range = barRange(task);
  if (!range) return null;
  const startChanged = next.start !== range.start;
  const endChanged = next.end !== range.end;
  if (mode === "start") {
    if (startChanged) patch.start_date = fromDay(next.start);
  } else if (mode === "end") {
    if (endChanged) patch.due_date = fromDay(next.end);
  } else {
    if (startChanged && hadStart) patch.start_date = fromDay(next.start);
    if (endChanged && hadDue) patch.due_date = fromDay(next.end);
  }
  return Object.keys(patch).length ? patch : null;
}

/** 时间轴的显示范围：覆盖所有条与今天，前后各留出余量，并按刻度对齐到周一 / 月初。 */
export function timelineRange(
  ranges: BarRange[],
  today: number,
  scale: GanttScale,
): BarRange {
  let min = today;
  let max = today;
  for (const range of ranges) {
    min = Math.min(min, range.start);
    max = Math.max(max, range.end);
  }
  const pad = scale === "day" ? 7 : scale === "week" ? 14 : 45;
  min -= pad;
  max += pad;
  if (scale !== "day") min = weekStart(min);
  return { start: min, end: max };
}

/** 该天所在周的周一（1970-01-01 是周四）。 */
export function weekStart(day: number): number {
  const weekday = (((day + 3) % 7) + 7) % 7; // 周一 = 0
  return day - weekday;
}

export interface HeaderCell {
  /** 起点相对时间轴起点的天数 */
  offset: number;
  /** 占的天数 */
  span: number;
  day: number;
}

/** 上排：按月分段。 */
export function monthCells(range: BarRange): HeaderCell[] {
  const cells: HeaderCell[] = [];
  let day = range.start;
  while (day <= range.end) {
    const date = new Date(day * DAY_MS);
    const next =
      Date.UTC(date.getUTCFullYear(), date.getUTCMonth() + 1, 1) / DAY_MS;
    const end = Math.min(next - 1, range.end);
    cells.push({ offset: day - range.start, span: end - day + 1, day });
    day = end + 1;
  }
  return cells;
}

/** 下排：日刻度每天一格；周刻度每周一格（周一起）；月刻度每周一格但标签较稀疏。 */
export function tickCells(range: BarRange, scale: GanttScale): HeaderCell[] {
  const cells: HeaderCell[] = [];
  if (scale === "day") {
    for (let day = range.start; day <= range.end; day++)
      cells.push({ offset: day - range.start, span: 1, day });
    return cells;
  }
  for (let day = weekStart(range.start); day <= range.end; day += 7) {
    const offset = Math.max(day, range.start) - range.start;
    cells.push({ offset, span: Math.min(7, range.end - day + 1), day });
  }
  return cells;
}

/** 'YYYY-MM-DD' 中的日（1-31） */
export function dayOfMonth(day: number): number {
  return new Date(day * DAY_MS).getUTCDate();
}

/**
 * 排序：按开始日期（没有开始日期的用截止日）升序，未排期的放最后；同日期保持原顺序（稳定）。
 */
export function sortByStart<T extends Pick<Task, "start_date" | "due_date">>(
  tasks: T[],
): T[] {
  const key = (task: T) => {
    const range = barRange(task);
    return range ? range.start : Number.POSITIVE_INFINITY;
  };
  return tasks
    .map((task, index) => ({ task, index, key: key(task) }))
    .sort((a, b) =>
      a.key === b.key ? a.index - b.index : a.key < b.key ? -1 : 1,
    )
    .map((item) => item.task);
}
