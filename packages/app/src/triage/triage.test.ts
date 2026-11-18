import { describe, expect, it } from "vitest";

import { filtersToQuery } from "../tasks/model";
import { parseSnooze } from "../tasks/primitives/snooze";

describe("分拣台前端纯逻辑", () => {
  it.each([
    [{ field: "priority", op: "is", values: ["HIGH"] }, { priority: ["HIGH"] }],
    [{ field: "assignee", op: "is", values: ["u1"] }, { assignee: ["u1"] }],
    [{ field: "content", op: "is", values: ["客户"] }, { q: "客户" }],
  ])("队列筛选条件转查询参数：%o", (condition, expected) => {
    expect(filtersToQuery([condition as never])).toEqual(expected);
  });

  it("稍后处理支持相对小时与明天时刻", () => {
    const now = new Date(2026, 9, 30, 10, 0);
    expect(parseSnooze("2小时后", now)?.getHours()).toBe(12);
    expect(parseSnooze("明天 8:00", now)?.getDate()).toBe(31);
  });
});
