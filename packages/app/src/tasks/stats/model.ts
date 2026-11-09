// 统计视图的纯计算：完成率、趋势图坐标、圆环弧长。不依赖 React，便于单测。

/** 完成率（0-100 的整数百分比）；没有任务时为 0。 */
export function completionRate(completed: number, total: number): number {
  if (total <= 0) return 0;
  return Math.round((completed / total) * 100);
}

/** y 轴刻度：0 到不小于 max 的整数，最多 4 档，全部为整数（任务数没有小数）。 */
export function niceTicks(max: number): number[] {
  const top = Math.max(1, Math.ceil(max));
  const step = Math.max(1, Math.ceil(top / 3));
  const ticks: number[] = [];
  for (let value = 0; value < top + step; value += step) {
    ticks.push(value);
    if (value >= top) break;
  }
  return ticks;
}

export interface Point {
  x: number;
  y: number;
}

/** 把逐日数据换成 SVG 坐标（y 向下，0 在底部）。单点时放在中间。 */
export function trendPoints(
  values: number[],
  width: number,
  height: number,
  yMax: number,
): Point[] {
  const top = Math.max(1, yMax);
  return values.map((value, index) => ({
    x: values.length === 1 ? width / 2 : (index / (values.length - 1)) * width,
    y: height - (Math.min(value, top) / top) * height,
  }));
}

export function linePath(points: Point[]): string {
  return points
    .map((p, i) => `${i === 0 ? "M" : "L"}${p.x.toFixed(1)} ${p.y.toFixed(1)}`)
    .join(" ");
}

export function areaPath(points: Point[], height: number): string {
  if (points.length === 0) return "";
  const first = points[0]!;
  const last = points[points.length - 1]!;
  return `${linePath(points)} L${last.x.toFixed(1)} ${height} L${first.x.toFixed(1)} ${height} Z`;
}

/** 圆环：已完成占的弧长与圆周长（stroke-dasharray 用）。 */
export function donutArcs(
  completed: number,
  total: number,
  radius: number,
): { circumference: number; completedLength: number } {
  const circumference = 2 * Math.PI * radius;
  const ratio = total > 0 ? Math.min(1, completed / total) : 0;
  return { circumference, completedLength: circumference * ratio };
}

/** 横向条形图中每条的宽度比例（相对最大值，最小可见 2%）。 */
export function barRatio(count: number, max: number): number {
  if (max <= 0 || count <= 0) return 0;
  return Math.max(0.02, count / max);
}
