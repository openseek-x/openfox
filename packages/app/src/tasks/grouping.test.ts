import { describe, expect, it } from "vitest";

import type { TaskStatus } from "@openfox/api";

import {
  NONE_KEY,
  OTHER_ITERATION_KEY,
  boardCells,
  canDropCell,
  cellKey,
  groupTasks,
  orderedGroups,
  withoutIterationGrouping,
} from "./grouping";
import { defaultDisplay, loadDisplay, normalizeDisplay } from "./model";
import { makeTask, statuses } from "./test-backend";

// 分组显示：显示设置回落、分组函数、看板单元格与拖动规则。

const human = (id: string, name = id) => ({ kind: "HUMAN" as const, id, name });
const agent = (id: string, name = id) => ({ kind: "AGENT" as const, id, name });
const crew = (id: string, name = id) => ({ kind: "CREW" as const, id, name });

const tasks = [
  makeTask(1, "st1", { assignees: [human("u1"), agent("a1"), crew("c1")] }),
  makeTask(2, "st1", { assignees: [], priority: "HIGH" }),
  makeTask(3, "st2", {
    assignees: [human("u1")],
    iteration_id: "it1",
    priority: "HIGH",
  }),
  makeTask(4, "st2", { iteration_id: "it-old" }),
];
const ctx = {
  columns: statuses as unknown as TaskStatus[],
  people: [
    { id: "u1", email: "lin@openfox.dev", name: "林" },
    { id: "u2", email: "wang@openfox.dev" },
  ],
  agents: [{ id: "a1", name: "产品大王" }],
  refNames: new Map([
    ["it1", "迭代 1"],
    ["p1", "专项 1"],
  ]),
};

describe("显示设置的分组字段", () => {
  it("旧数据没有分组字段时回落默认", () => {
    expect(
      normalizeDisplay({ layout: "list" }, defaultDisplay("space")),
    ).toMatchObject({
      layout: "list",
      boardColumns: "status",
      boardRows: "none",
      showEmptyRows: false,
      listSubgroup: "none",
    });
  });

  it("非法值回落默认，行与列相同时行回落不分组", () => {
    const fallback = defaultDisplay("space");
    expect(
      normalizeDisplay(
        {
          boardColumns: "priority" as never,
          boardRows: "label" as never,
          showEmptyRows: "yes" as never,
          listSubgroup: "status" as never,
        },
        fallback,
      ),
    ).toMatchObject({
      boardColumns: "status",
      boardRows: "none",
      showEmptyRows: false,
      listSubgroup: "none",
    });
    expect(
      normalizeDisplay(
        { boardColumns: "assignee", boardRows: "assignee" },
        fallback,
      ),
    ).toMatchObject({ boardColumns: "assignee", boardRows: "none" });
    expect(
      normalizeDisplay(
        {
          boardColumns: "crew",
          boardRows: "priority",
          showEmptyRows: true,
          listSubgroup: "iteration",
        },
        fallback,
      ),
    ).toMatchObject({
      boardColumns: "crew",
      boardRows: "priority",
      showEmptyRows: true,
      listSubgroup: "iteration",
    });
  });

  it("本机保存的无分组字段设置仍能读出", () => {
    localStorage.setItem(
      "openfox:tasks-display:o1:space:s1",
      JSON.stringify({ layout: "table", sort: "created", properties: ["id"] }),
    );
    expect(loadDisplay("o1", "space:s1", "space")).toMatchObject({
      layout: "table",
      sort: "created",
      boardColumns: "status",
      boardRows: "none",
    });
    localStorage.clear();
  });

  it("未启用迭代时迭代分组回落：列回落状态，行与二级分组回落不分组", () => {
    const display = {
      ...defaultDisplay("space"),
      boardColumns: "iteration" as const,
      boardRows: "status" as const,
      listSubgroup: "iteration" as const,
    };
    expect(withoutIterationGrouping(display, true)).toBe(display);
    expect(withoutIterationGrouping(display, false)).toMatchObject({
      boardColumns: "status",
      // 行原来是状态，与回落后的列相同，也回落不分组
      boardRows: "none",
      listSubgroup: "none",
    });
  });
});

describe("分组函数", () => {
  it("处理人：多处理人出现在每个人的组里，小队不算处理人，没有处理人的进「未指派」", () => {
    const map = groupTasks(tasks, "assignee", ctx);
    expect(map.get("HUMAN:u1")?.map((t) => t.id)).toEqual(["task-1", "task-3"]);
    expect(map.get("AGENT:a1")?.map((t) => t.id)).toEqual(["task-1"]);
    expect(map.get(NONE_KEY)?.map((t) => t.id)).toEqual(["task-2", "task-4"]);
    const order = orderedGroups("assignee", tasks, ctx).map((g) => g.key);
    // 人员 → Agent → 未指派；候选里没有任务的人也给出，由界面按「显示空白列」过滤
    expect(order).toEqual(["HUMAN:u1", "HUMAN:u2", "AGENT:a1", NONE_KEY]);
  });

  it("小队：只按小队处理人分组，空值进「无小队」", () => {
    const map = groupTasks(tasks, "crew", ctx);
    expect(map.get("c1")?.map((t) => t.id)).toEqual(["task-1"]);
    expect(map.get(NONE_KEY)).toHaveLength(3);
    expect(orderedGroups("crew", tasks, ctx).map((g) => g.key)).toEqual([
      "c1",
      NONE_KEY,
    ]);
  });

  it("迭代：名单外的迭代合成「其他迭代」，没有迭代进「无迭代」", () => {
    const map = groupTasks(tasks, "iteration", ctx);
    expect(map.get("it1")?.map((t) => t.id)).toEqual(["task-3"]);
    expect(map.get(OTHER_ITERATION_KEY)?.map((t) => t.id)).toEqual(["task-4"]);
    expect(map.get(NONE_KEY)).toHaveLength(2);
    // 专项 p1 虽在 refNames 里，但没有任务属于它，不会成为迭代分组
    expect(orderedGroups("iteration", tasks, ctx).map((g) => g.key)).toEqual([
      "it1",
      OTHER_ITERATION_KEY,
      NONE_KEY,
    ]);
  });

  it("优先级与状态：给出全部组，顺序固定", () => {
    expect(orderedGroups("priority", tasks, ctx).map((g) => g.key)).toEqual([
      "URGENT",
      "HIGH",
      "NORMAL",
      "LOW",
      "LOWEST",
      "NONE",
    ]);
    expect(orderedGroups("status", tasks, ctx).map((g) => g.key)).toEqual([
      "st1",
      "st2",
      "st3",
    ]);
  });

  it("看板单元格：行 × 列，多处理人的任务在每个单元格各出现一次", () => {
    const cells = boardCells(tasks, "assignee", "priority", ctx);
    expect(cells.get(cellKey("NONE", "HUMAN:u1"))?.map((t) => t.id)).toEqual([
      "task-1",
    ]);
    expect(cells.get(cellKey("HIGH", "HUMAN:u1"))?.map((t) => t.id)).toEqual([
      "task-3",
    ]);
    expect(cells.get(cellKey("HIGH", NONE_KEY))?.map((t) => t.id)).toEqual([
      "task-2",
    ]);
  });

  it("拖动规则：不能跨行；跨列只在按状态分列时允许；列内排序要求手动排序", () => {
    const rules = { crossColumns: false, allowReorder: true };
    expect(canDropCell("all|HUMAN:u1", "all|HUMAN:u1", rules)).toBe(true);
    expect(canDropCell("all|HUMAN:u1", "all|AGENT:a1", rules)).toBe(false);
    expect(canDropCell("HIGH|st1", "NONE|st1", rules)).toBe(false);
    const status = { crossColumns: true, allowReorder: false };
    expect(canDropCell("HIGH|st1", "HIGH|st2", status)).toBe(true);
    expect(canDropCell("HIGH|st1", "HIGH|st1", status)).toBe(false);
    expect(canDropCell("HIGH|st1", "NONE|st2", status)).toBe(false);
  });
});
