import { afterEach, describe, expect, it } from "vitest";

import { HttpClient } from "@openfox/api";

import {
  defaultDisplay,
  filtersToQuery,
  branchUsername,
  gitBranchName,
  type BranchFormat,
  loadDisplay,
  normalizeDisplay,
  planBoardMove,
  queryToConditions,
  saveDisplay,
  sortQuery,
} from "./model";
import { replaceAssignee, toggleLabel } from "./pickers";

afterEach(() => localStorage.clear());

describe("专项筛选与视图筛选还原", () => {
  it("专项：none / 指定值写到 project_id，「不是」写到 project_id_not", () => {
    expect(
      filtersToQuery([{ field: "project", op: "is", values: ["none", "p1"] }]),
    ).toEqual({ project_id: ["none", "p1"] });
    expect(
      filtersToQuery([{ field: "project", op: "is_not", values: ["p1"] }]),
    ).toEqual({ project_id_not: ["p1"] });
  });

  it("filtersToQuery 的结果可以还原成同样的条件（编辑视图用）", () => {
    const conditions = [
      { field: "status", op: "is", values: ["s1"] },
      { field: "priority", op: "is_not", values: ["LOW"] },
      { field: "assignee", op: "is", values: ["user:u1", "none"] },
      { field: "crew", op: "is", values: ["c1"] },
      { field: "label", op: "is_not", values: ["l1"] },
      { field: "iteration", op: "is", values: ["current"] },
      { field: "project", op: "is_not", values: ["none"] },
      { field: "content", op: "is_not", values: ["草稿"] },
      { field: "date", op: "is", values: ["due", "2026-10-01", "2026-10-31"] },
      { field: "estimate", op: "is", values: ["3600", ""] },
    ] as const;
    const query = filtersToQuery(
      conditions.map((c) => ({ ...c, values: [...c.values] })),
    );
    const key = (c: { field: string; op: string }) => `${c.field}:${c.op}`;
    expect(
      queryToConditions(query).sort((a, b) => key(a).localeCompare(key(b))),
    ).toEqual([...conditions].sort((a, b) => key(a).localeCompare(key(b))));
    expect(queryToConditions({})).toEqual([]);
  });

  it("视图的显示设置：非法字段按默认值补齐", () => {
    expect(
      normalizeDisplay(
        {
          layout: "kanban" as never,
          sort: "project",
          properties: ["project", "x" as never],
        },
        defaultDisplay("space"),
      ),
    ).toMatchObject({
      layout: "board",
      sort: "project",
      properties: ["project"],
    });
  });
});

describe("筛选条件 → 查询参数", () => {
  it("同字段多值为数组，不是写到 _not，小队并入 assignee", () => {
    expect(
      filtersToQuery([
        { field: "status", op: "is", values: ["s1", "s2"] },
        { field: "priority", op: "is_not", values: ["LOW"] },
        { field: "assignee", op: "is", values: ["user:u1", "none"] },
        { field: "crew", op: "is", values: ["c1"] },
        { field: "label", op: "is_not", values: ["l1"] },
        { field: "creator", op: "is", values: ["u2"] },
        { field: "type", op: "is_not", values: [] },
        { field: "content", op: "is", values: ["  登录  "] },
        { field: "date", op: "is", values: ["due", "2026-10-01", ""] },
      ]),
    ).toEqual({
      status_id: ["s1", "s2"],
      priority_not: ["LOW"],
      assignee: ["user:u1", "none", "crew:c1"],
      label_id_not: ["l1"],
      creator_id: ["u2"],
      q: "登录",
      due_from: "2026-10-01",
    });
  });

  it("内容与日期的「不是」写到 q_not 与 *_not", () => {
    expect(
      filtersToQuery([
        { field: "content", op: "is_not", values: ["草稿"] },
        { field: "date", op: "is_not", values: ["start", "", "2026-10-31"] },
        { field: "date", op: "is_not", values: ["due", "", ""] },
      ]),
    ).toEqual({ q_not: "草稿", start_to: "2026-10-31", start_not: "true" });
  });

  it("迭代筛选：none / current / 指定值写到 iteration_id，「不是」写到 iteration_id_not", () => {
    expect(
      filtersToQuery([
        { field: "iteration", op: "is", values: ["none", "current", "i1"] },
      ]),
    ).toEqual({ iteration_id: ["none", "current", "i1"] });
    expect(
      filtersToQuery([{ field: "iteration", op: "is_not", values: ["none"] }]),
    ).toEqual({ iteration_id_not: ["none"] });
  });

  it("数组参数展开为重复的查询参数", () => {
    const url = new HttpClient().url("/v1/x", {
      status_id: ["a", "b"],
      priority_not: ["HIGH"],
      q: "",
    });
    expect(url).toBe("/api/v1/x?status_id=a&status_id=b&priority_not=HIGH");
  });
});

describe("看板移动", () => {
  const column = [{ id: "a" }, { id: "b" }, { id: "c" }];

  it("跨列 + 手动排序：带 status_id 与前后邻居", () => {
    expect(
      planBoardMove(
        { taskId: "x", fromStatusId: "s1", toStatusId: "s2", toIndex: 1 },
        column,
        true,
      ),
    ).toEqual({
      status_id: "s2",
      position: { before_task_id: "b", after_task_id: "a" },
    });
  });

  it("列内重排：移到末尾时 before 为空", () => {
    expect(
      planBoardMove(
        { taskId: "a", fromStatusId: "s1", toStatusId: "s1", toIndex: 2 },
        column,
        true,
      ),
    ).toEqual({ position: { before_task_id: null, after_task_id: "c" } });
  });

  it("原位放下不发请求；非手动排序只允许跨列改状态", () => {
    expect(
      planBoardMove(
        { taskId: "b", fromStatusId: "s1", toStatusId: "s1", toIndex: 1 },
        column,
        true,
      ),
    ).toBeNull();
    expect(
      planBoardMove(
        { taskId: "a", fromStatusId: "s1", toStatusId: "s1", toIndex: 2 },
        column,
        false,
      ),
    ).toBeNull();
    expect(
      planBoardMove(
        { taskId: "x", fromStatusId: "s1", toStatusId: "s2", toIndex: 0 },
        column,
        false,
      ),
    ).toEqual({ status_id: "s2" });
  });
});

describe("显示设置", () => {
  it("按组织和页面保存，非法字段回退默认", () => {
    saveDisplay("o1", "space:s1", {
      ...defaultDisplay("space"),
      layout: "table",
    });
    expect(loadDisplay("o1", "space:s1", "space").layout).toBe("table");
    expect(loadDisplay("o1", "space:s2", "space").layout).toBe("board");
    localStorage.setItem(
      "openfox:tasks-display:o1:my-tasks",
      JSON.stringify({
        layout: "kanban",
        sort: "nope",
        properties: ["id", "x"],
      }),
    );
    const my = loadDisplay("o1", "my-tasks", "my");
    expect(my.layout).toBe("board");
    expect(my.sort).toBe("updated");
    expect(my.properties).toEqual(["id"]);
  });

  it("甘特图是合法布局，可以保存并还原", () => {
    expect(
      normalizeDisplay({ layout: "gantt" }, defaultDisplay("space")).layout,
    ).toBe("gantt");
    saveDisplay("o1", "space:s1", {
      ...defaultDisplay("space"),
      layout: "gantt",
    });
    expect(loadDisplay("o1", "space:s1", "space").layout).toBe("gantt");
  });

  it("排序方向：手动与截止日期升序，其余降序", () => {
    expect(sortQuery("manual")).toEqual({ sort: "manual", order: "asc" });
    expect(sortQuery("priority")).toEqual({ sort: "priority", order: "desc" });
    expect(sortQuery("due")).toEqual({ sort: "due", order: "asc" });
  });
});

describe("选择器辅助", () => {
  it("单选分组内选择时替换同组标签", () => {
    const labels = [
      { id: "p1", group_id: "g" },
      { id: "p2", group_id: "g" },
      { id: "x", group_id: null },
    ];
    expect(toggleLabel(["p1", "x"], "p2", labels)).toEqual(["x", "p2"]);
    expect(toggleLabel(["p1", "x"], "x", labels)).toEqual(["p1"]);
  });

  it("单选处理人：人员替换人员，保留 Agent；none 移除人员", () => {
    const current = [
      { kind: "HUMAN" as const, id: "u1", name: "a@x" },
      { kind: "AGENT" as const, id: "a1", name: "Bot" },
    ];
    const names = { people: [{ id: "u2", email: "b@x" }], agents: [] };
    expect(replaceAssignee(current, "user:u2", names)).toEqual([
      { kind: "HUMAN", id: "u2", name: "b@x" },
      { kind: "AGENT", id: "a1", name: "Bot" },
    ]);
    expect(replaceAssignee(current, "none", names)).toEqual([current[1]]);
  });

  it("git 分支名：小写编号 + ASCII slug", () => {
    expect(gitBranchName("OPEN-12", "Fix Safari 登录 bug!")).toBe(
      "open-12-fix-safari-bug",
    );
    expect(gitBranchName("OPEN-3", "修复登录")).toBe("open-3");
  });

  it("git 分支名：四种格式与用户名清洗", () => {
    const username = branchUsername("Lin.Wei+x@openfox.dev");
    expect(username).toBe("linweix");
    const title = "Fix Safari bug";
    const f = (format: BranchFormat, name = username) =>
      gitBranchName("OPEN-12", title, { format, username: name });
    expect(f("username/identifier-title")).toBe(
      "linweix/open-12-fix-safari-bug",
    );
    expect(f("identifier-title")).toBe("open-12-fix-safari-bug");
    expect(f("username/identifier")).toBe("linweix/open-12");
    expect(f("identifier")).toBe("open-12");
    // 拿不到用户名时省略前缀
    expect(f("username/identifier-title", "")).toBe("open-12-fix-safari-bug");
    expect(branchUsername(undefined)).toBe("");
  });
});
