// 任务分组的纯逻辑：按状态 / 处理人 / 小队 / 迭代 / 优先级把已加载的任务分组。不依赖 React，便于单测。

import type { Task, TaskStatus } from "@openfox/api";

import { PRIORITY_ORDER, type DisplaySettings, type GroupField } from "./model";
import type { Priority } from "./primitives/PriorityIcon";

/** 空值组（未指派 / 无小队 / 无迭代）的键 */
export const NONE_KEY = "none";
/** 已加载任务里出现、但不在当前迭代名单里的迭代（例如已完成的迭代）合成一组 */
export const OTHER_ITERATION_KEY = "other";

export interface GroupContext {
  /** 按全局顺序排好的状态 */
  columns: TaskStatus[];
  people: { id: string; email: string; name?: string }[];
  agents: { id: string; name: string }[];
  /** 迭代 / 专项 id → 名称；迭代分组只认其中出现的迭代 id */
  refNames?: ReadonlyMap<string, string>;
}

export interface TaskGroup {
  key: string;
  field: GroupField;
  /** 显示名；空值组、「其他迭代」、优先级为 null，由界面翻译 */
  name: string | null;
  status?: TaskStatus;
  assigneeKind?: Task["assignees"][number]["kind"];
  priority?: Priority;
}

/** 任务所属的分组键：多处理人 / 多小队时返回多个，空值返回 [NONE_KEY]。 */
export function taskGroupKeys(
  task: Task,
  field: GroupField,
  ctx: Pick<GroupContext, "refNames">,
): string[] {
  switch (field) {
    case "status":
      return [task.status_id];
    case "priority":
      return [task.priority];
    case "iteration":
      if (!task.iteration_id) return [NONE_KEY];
      return [
        ctx.refNames?.has(task.iteration_id)
          ? task.iteration_id
          : OTHER_ITERATION_KEY,
      ];
    case "assignee": {
      const keys = task.assignees
        .filter((a) => a.kind !== "CREW")
        .map((a) => `${a.kind}:${a.id}`);
      return keys.length ? [...new Set(keys)] : [NONE_KEY];
    }
    case "crew": {
      const keys = task.assignees
        .filter((a) => a.kind === "CREW")
        .map((a) => a.id);
      return keys.length ? [...new Set(keys)] : [NONE_KEY];
    }
  }
}

/** 任务按字段分组：键 → 任务（保持传入顺序，即服务端排序）；同一任务可出现在多个组里。 */
export function groupTasks(
  tasks: Task[],
  field: GroupField,
  ctx: Pick<GroupContext, "refNames">,
): Map<string, Task[]> {
  const map = new Map<string, Task[]>();
  for (const task of tasks)
    for (const key of taskGroupKeys(task, field, ctx)) {
      const list = map.get(key);
      if (list) list.push(task);
      else map.set(key, [task]);
    }
  return map;
}

/**
 * 分组的显示顺序。全量可知的组（状态、优先级、处理人候选）总是给出，界面再按「显示空白列 / 空行」过滤；
 * 小队与迭代只给出已加载任务里出现过的组。空值组排最后。
 */
export function orderedGroups(
  field: GroupField,
  tasks: Task[],
  ctx: GroupContext,
): TaskGroup[] {
  const seen = groupTasks(tasks, field, ctx);
  const withNone = (groups: TaskGroup[]): TaskGroup[] => [
    ...groups,
    { key: NONE_KEY, field, name: null },
  ];
  switch (field) {
    case "status": {
      const groups: TaskGroup[] = ctx.columns.map((s) => ({
        key: s.id,
        field,
        name: s.name,
        status: s,
      }));
      // 不在列里的状态（例如已归档）仍要显示出来，不能让任务凭空消失
      for (const task of tasks)
        if (!groups.some((g) => g.key === task.status_id))
          groups.push({ key: task.status_id, field, name: task.status_name });
      return groups;
    }
    case "priority":
      return PRIORITY_ORDER.map((p) => ({
        key: p,
        field,
        name: null,
        priority: p,
      }));
    case "assignee": {
      const groups: TaskGroup[] = [
        ...ctx.people.map((p) => ({
          key: `HUMAN:${p.id}`,
          field,
          name: p.name || p.email,
          assigneeKind: "HUMAN" as const,
        })),
        ...ctx.agents.map((a) => ({
          key: `AGENT:${a.id}`,
          field,
          name: a.name,
          assigneeKind: "AGENT" as const,
        })),
      ];
      // 候选名单之外的处理人（例如已离开的成员）按出现顺序补上
      for (const task of tasks)
        for (const a of task.assignees) {
          const key = `${a.kind}:${a.id}`;
          if (a.kind === "CREW" || groups.some((g) => g.key === key)) continue;
          groups.push({ key, field, name: a.name, assigneeKind: a.kind });
        }
      return withNone(groups);
    }
    case "crew": {
      const crews = new Map<string, string>();
      for (const task of tasks)
        for (const a of task.assignees)
          if (a.kind === "CREW" && !crews.has(a.id)) crews.set(a.id, a.name);
      const groups = [...crews]
        .sort(([, a], [, b]) => a.localeCompare(b))
        .map(([key, name]) => ({
          key,
          field,
          name,
          assigneeKind: "CREW" as const,
        }));
      return withNone(groups);
    }
    case "iteration": {
      const groups: TaskGroup[] = [];
      for (const [id, name] of ctx.refNames ?? [])
        if (seen.has(id)) groups.push({ key: id, field, name });
      if (seen.has(OTHER_ITERATION_KEY))
        groups.push({ key: OTHER_ITERATION_KEY, field, name: null });
      return withNone(groups);
    }
  }
}

/** 行不分组时唯一一行的键 */
export const ALL_ROWS_KEY = "all";

export function cellKey(rowKey: string, columnKey: string): string {
  return `${rowKey}|${columnKey}`;
}

export function splitCellKey(key: string): { row: string; column: string } {
  const at = key.indexOf("|");
  return { row: key.slice(0, at), column: key.slice(at + 1) };
}

/**
 * 分组看板的拖动规则：不能跨行；跨列只在按状态分列时允许（改状态）；
 * 同一单元格内排序要求手动排序。不实现跨列改处理人 / 小队 / 迭代。
 */
export function canDropCell(
  from: string,
  to: string,
  rules: { crossColumns: boolean; allowReorder: boolean },
): boolean {
  const a = splitCellKey(from);
  const b = splitCellKey(to);
  if (a.row !== b.row) return false;
  if (a.column !== b.column) return rules.crossColumns;
  return rules.allowReorder;
}

/** 看板单元格：cellKey(行, 列) → 任务（保持传入顺序）。行不分组时行键恒为 ALL_ROWS_KEY。 */
export function boardCells(
  tasks: Task[],
  columns: GroupField,
  rows: GroupField | "none",
  ctx: Pick<GroupContext, "refNames">,
): Map<string, Task[]> {
  const cells = new Map<string, Task[]>();
  for (const task of tasks) {
    const rowKeys =
      rows === "none" ? [ALL_ROWS_KEY] : taskGroupKeys(task, rows, ctx);
    for (const row of rowKeys)
      for (const column of taskGroupKeys(task, columns, ctx)) {
        const key = cellKey(row, column);
        const list = cells.get(key);
        if (list) list.push(task);
        else cells.set(key, [task]);
      }
  }
  return cells;
}

/** 按状态分列、行不分组：沿用原来按状态分别分页的看板。 */
export function isStatusBoard(display: DisplaySettings): boolean {
  return display.boardColumns === "status" && display.boardRows === "none";
}

/** 空间没启用迭代（或我的任务）时，迭代分组回落默认：列回落状态，行与二级分组回落不分组。 */
export function withoutIterationGrouping(
  display: DisplaySettings,
  enabled: boolean,
): DisplaySettings {
  if (enabled) return display;
  const columns =
    display.boardColumns === "iteration" ? "status" : display.boardColumns;
  const rows =
    display.boardRows === "iteration" || display.boardRows === columns
      ? "none"
      : display.boardRows;
  const subgroup =
    display.listSubgroup === "iteration" ? "none" : display.listSubgroup;
  if (
    columns === display.boardColumns &&
    rows === display.boardRows &&
    subgroup === display.listSubgroup
  )
    return display;
  return {
    ...display,
    boardColumns: columns,
    boardRows: rows,
    listSubgroup: subgroup,
  };
}
