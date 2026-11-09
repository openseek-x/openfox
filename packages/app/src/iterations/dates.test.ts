import { describe, expect, it } from "vitest";

import {
  addCalendarDays,
  formatIterationRange,
  isInIteration,
  iterationWindow,
  nextWeekday,
  parseCalendarDate,
  weekday,
} from "./dates";

describe("迭代日期与排期", () => {
  it("校验日历日期并按日历日加减", () => {
    expect(parseCalendarDate("2026-10-28")).toEqual({
      year: 2026,
      month: 10,
      day: 28,
    });
    expect(parseCalendarDate("2026-02-30")).toBeNull();
    expect(addCalendarDays("2026-10-28", 7)).toBe("2026-11-04");
    expect(addCalendarDays("2026-01-01", -1)).toBe("2025-12-31");
  });

  it("按后端星期编号找今天或之后最近的开始日", () => {
    expect(weekday("2026-10-25")).toBe(0);
    expect(nextWeekday("2026-10-25", 1)).toBe("2026-10-26");
    expect(nextWeekday("2026-10-26", 1)).toBe("2026-10-26");
    expect(nextWeekday("2026-10-27", 1)).toBe("2026-11-02");
  });

  it("计算持续区间、冷却区间和下一迭代起点", () => {
    expect(iterationWindow("2026-10-12", 2, 1)).toEqual({
      startDate: "2026-10-12",
      endDate: "2026-10-26",
      nextStartDate: "2026-11-02",
    });
    expect(isInIteration("2026-10-25", "2026-10-12", "2026-10-26")).toBe(true);
    expect(isInIteration("2026-10-26", "2026-10-12", "2026-10-26")).toBe(false);
  });

  it("格式化日期时使用空间时区", () => {
    expect(
      formatIterationRange(
        "2026-03-08",
        "2026-03-22",
        "en-US",
        "America/New_York",
      ),
    ).toBe("Mar 8 – Mar 22");
    expect(
      formatIterationRange(
        "2026-10-12",
        "2026-10-26",
        "zh-CN",
        "Asia/Shanghai",
      ),
    ).toBe("10月12日 – 10月26日");
  });
});
