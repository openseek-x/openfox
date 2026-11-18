import { describe, expect, it } from "vitest";

import {
  applyDrag,
  barRange,
  dragPatch,
  fromDay,
  monthCells,
  sortByStart,
  tickCells,
  timelineRange,
  toDay,
  weekStart,
} from "./model";

const d = (ymd: string) => toDay(ymd)!;

describe("甘特图日期换算", () => {
  it("YYYY-MM-DD 与天数互转，跨月跨年不受夏令时影响", () => {
    expect(fromDay(d("2026-11-01"))).toBe("2026-11-01");
    expect(d("2026-03-09") - d("2026-03-08")).toBe(1);
    expect(d("2027-01-01") - d("2026-12-31")).toBe(1);
    expect(toDay(null)).toBeNull();
    expect(toDay("abc")).toBeNull();
  });

  it("周一对齐", () => {
    // 2026-11-02 是周一
    expect(fromDay(weekStart(d("2026-11-04")))).toBe("2026-11-02");
    expect(fromDay(weekStart(d("2026-11-02")))).toBe("2026-11-02");
    expect(fromDay(weekStart(d("2026-11-08")))).toBe("2026-11-02");
  });
});

describe("barRange", () => {
  it("两端都有、只有一端、都没有、反序", () => {
    expect(
      barRange({ start_date: "2026-11-02", due_date: "2026-11-05" }),
    ).toEqual({ start: d("2026-11-02"), end: d("2026-11-05") });
    expect(barRange({ start_date: null, due_date: "2026-11-05" })).toEqual({
      start: d("2026-11-05"),
      end: d("2026-11-05"),
    });
    expect(barRange({ start_date: "2026-11-05", due_date: null })).toEqual({
      start: d("2026-11-05"),
      end: d("2026-11-05"),
    });
    expect(barRange({ start_date: null, due_date: null })).toBeNull();
    expect(
      barRange({ start_date: "2026-11-09", due_date: "2026-11-05" }),
    ).toEqual({ start: d("2026-11-05"), end: d("2026-11-09") });
  });
});

describe("拖动", () => {
  const range = { start: d("2026-11-02"), end: d("2026-11-05") };
  it("整体平移保持跨度；拉伸一端不越过另一端", () => {
    expect(applyDrag(range, "move", 3)).toEqual({
      start: d("2026-11-05"),
      end: d("2026-11-08"),
    });
    expect(applyDrag(range, "start", 10).start).toBe(range.end);
    expect(applyDrag(range, "end", -10).end).toBe(range.start);
    expect(applyDrag(range, "end", 2).end).toBe(d("2026-11-07"));
  });

  it("只提交变化的字段；单端日期的任务平移只改那一端", () => {
    const both = { start_date: "2026-11-02", due_date: "2026-11-05" };
    expect(dragPatch(both, applyDrag(range, "move", 1), "move")).toEqual({
      start_date: "2026-11-03",
      due_date: "2026-11-06",
    });
    expect(dragPatch(both, applyDrag(range, "end", 2), "end")).toEqual({
      due_date: "2026-11-07",
    });
    expect(dragPatch(both, applyDrag(range, "start", -1), "start")).toEqual({
      start_date: "2026-11-01",
    });
    expect(dragPatch(both, range, "move")).toBeNull();

    const dueOnly = { start_date: null, due_date: "2026-11-05" };
    const dueRange = barRange(dueOnly)!;
    expect(dragPatch(dueOnly, applyDrag(dueRange, "move", 2), "move")).toEqual({
      due_date: "2026-11-07",
    });
    // 拉伸开始端：为只有截止日的任务补上开始日期
    expect(
      dragPatch(dueOnly, applyDrag(dueRange, "start", -2), "start"),
    ).toEqual({ start_date: "2026-11-03" });
    const startOnly = { start_date: "2026-11-05", due_date: null };
    const startRange = barRange(startOnly)!;
    expect(
      dragPatch(startOnly, applyDrag(startRange, "move", -1), "move"),
    ).toEqual({ start_date: "2026-11-04" });
    expect(
      dragPatch({ start_date: null, due_date: null }, range, "move"),
    ).toBeNull();
  });
});

describe("时间轴", () => {
  it("范围覆盖所有条与今天，周 / 月刻度对齐到周一", () => {
    const today = d("2026-11-04");
    const r = timelineRange(
      [{ start: d("2026-11-10"), end: d("2026-11-20") }],
      today,
      "week",
    );
    expect(r.start).toBeLessThanOrEqual(today);
    expect(r.end).toBeGreaterThanOrEqual(d("2026-11-20"));
    expect(fromDay(r.start)).toBe(fromDay(weekStart(r.start)));
    expect(timelineRange([], today, "day").start).toBeLessThan(today);
  });

  it("月分段首尾相接、天数之和等于范围", () => {
    const range = { start: d("2026-10-28"), end: d("2026-12-03") };
    const cells = monthCells(range);
    expect(cells.map((c) => c.span)).toEqual([4, 30, 3]);
    expect(cells.reduce((sum, c) => sum + c.span, 0)).toBe(
      range.end - range.start + 1,
    );
    const ticks = tickCells(range, "week");
    expect(ticks[0]!.offset).toBe(0);
    expect(tickCells(range, "day")).toHaveLength(range.end - range.start + 1);
  });
});

describe("按开始日期排序", () => {
  it("升序、无日期靠后、相同日期保持原顺序", () => {
    const tasks = [
      { id: "none", start_date: null, due_date: null },
      { id: "late", start_date: "2026-11-09", due_date: null },
      { id: "dueOnly", start_date: null, due_date: "2026-11-03" },
      { id: "early", start_date: "2026-11-01", due_date: "2026-11-02" },
      { id: "sameA", start_date: "2026-11-03", due_date: null },
    ];
    expect(sortByStart(tasks).map((t) => t.id)).toEqual([
      "early",
      "dueOnly",
      "sameA",
      "late",
      "none",
    ]);
  });
});
