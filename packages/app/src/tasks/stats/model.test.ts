import { describe, expect, it } from "vitest";

import {
  areaPath,
  barRatio,
  completionRate,
  donutArcs,
  niceTicks,
  trendPoints,
} from "./model";

describe("统计计算", () => {
  it("完成率取整，没有任务为 0", () => {
    expect(completionRate(1, 5)).toBe(20);
    expect(completionRate(2, 3)).toBe(67);
    expect(completionRate(0, 0)).toBe(0);
  });

  it("y 轴刻度是整数且覆盖最大值", () => {
    for (const max of [0, 1, 2, 5, 9, 31]) {
      const ticks = niceTicks(max);
      expect(ticks[0]).toBe(0);
      expect(ticks[ticks.length - 1]).toBeGreaterThanOrEqual(max);
      expect(ticks.every(Number.isInteger)).toBe(true);
      expect(ticks.length).toBeLessThanOrEqual(5);
    }
  });

  it("趋势坐标：首尾贴边、0 在底部、最大值在顶部、单点居中", () => {
    const points = trendPoints([0, 2, 1], 100, 50, 2);
    expect(points[0]).toEqual({ x: 0, y: 50 });
    expect(points[1]).toEqual({ x: 50, y: 0 });
    expect(points[2]).toEqual({ x: 100, y: 25 });
    expect(trendPoints([3], 100, 50, 3)[0]).toEqual({ x: 50, y: 0 });
    expect(areaPath([], 50)).toBe("");
    expect(areaPath(points, 50).endsWith("Z")).toBe(true);
  });

  it("圆环与条形比例", () => {
    const { circumference, completedLength } = donutArcs(1, 4, 10);
    expect(completedLength / circumference).toBeCloseTo(0.25);
    expect(donutArcs(0, 0, 10).completedLength).toBe(0);
    expect(barRatio(2, 4)).toBe(0.5);
    expect(barRatio(0, 4)).toBe(0);
    expect(barRatio(1, 1000)).toBe(0.02);
  });
});
