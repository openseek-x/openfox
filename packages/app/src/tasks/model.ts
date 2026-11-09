// 任务视图的纯逻辑：筛选条件 → 查询参数、显示设置、看板移动计算。不依赖 React，便于单测。

import type {
  Task,
  TaskListQuery,
  TaskStatus,
  SpaceTaskType,
  UpdateTaskInput,
} from "@openfox/api";

import { readLocal, writeLocal } from "../storage";
import type { Priority } from "./primitives/PriorityIcon";

// ---------- 筛选 ----------

export type CustomFilterField = `cf_${string}`;
export type FilterField =
  | CustomFilterField
  | "subscriber"
  | "relation"
  | "status"
  | "priority"
  | "type"
  | "assignee"
  | "crew"
  | "label"
  | "content"
  | "date"
  | "creator"
  // 估时（只在启用估时的空间出现）
  | "estimate"
  // 迭代归属
  | "iteration"
  // 专项归属
  | "project";

export const FILTER_FIELDS: FilterField[] = [
  "status",
  "priority",
  "type",
  "assignee",
  "crew",
  "label",
  "content",
  "date",
  "creator",
  "iteration",
  "project",
];

export type FilterOp = "is" | "is_not";

/**
 * 一个筛选条件。values 的含义按字段：
 * - assignee：user:<id> / agent:<id> / none；crew：小队 id
 * - content：[关键字]
 * - date：[start|due, from, to]（from / to 可为空串）
 * - estimate：[min, max]（可为空串；time 刻度为秒）或 ["none"]（未估时）
 * - 其他：对应实体 id 或枚举值
 */
export interface FilterCondition {
  field: FilterField;
  op: FilterOp;
  values: string[];
}

/** 只支持「是」的字段。内容与日期已有 q_not / start_not / due_not；估时接口没有取反参数。 */
export const POSITIVE_ONLY: ReadonlySet<FilterField> = new Set([]);

type ArrayParam =
  | "subscriber"
  | "relation"
  | "status_id"
  | "type_id"
  | "priority"
  | "assignee"
  | "label_id"
  | "creator_id"
  | "iteration_id"
  | "project_id";

const PARAM: Partial<Record<FilterField, ArrayParam>> = {
  subscriber: "subscriber",
  relation: "relation",
  status: "status_id",
  type: "type_id",
  priority: "priority",
  assignee: "assignee",
  crew: "assignee",
  label: "label_id",
  creator: "creator_id",
  iteration: "iteration_id",
  project: "project_id",
};

export type FilterQuery = Record<string, string | string[]>;

/**
 * 把筛选条件转成接口查询参数：同字段多值为「任一」，is_not 写到 *_not；
 * 处理人与小队共用 assignee 参数（小队值加 crew: 前缀）。空条件忽略。
 */
export function filtersToQuery(conditions: FilterCondition[]): FilterQuery {
  const query: FilterQuery = {};
  const push = (key: string, values: string[]) => {
    const list = (query[key] as string[] | undefined) ?? [];
    for (const value of values) if (!list.includes(value)) list.push(value);
    if (list.length) query[key] = list;
  };
  for (const condition of conditions) {
    const values = condition.values.filter(Boolean);
    if (condition.field === "content") {
      const q = condition.values[0]?.trim();
      if (q) query[condition.op === "is_not" ? "q_not" : "q"] = q;
      continue;
    }
    if (condition.field === "date") {
      const [which, from, to] = condition.values;
      if (which !== "start" && which !== "due") continue;
      if (from) query[`${which}_from`] = from;
      if (to) query[`${which}_to`] = to;
      // 「不是」：日期为空或落在区间外
      if ((from || to) && condition.op === "is_not")
        query[`${which}_not`] = "true";
      continue;
    }
    if (condition.field === "estimate") {
      const [min, max] = condition.values;
      if (min === "none") {
        query.estimate_none = "true";
        continue;
      }
      if (min) query.estimate_min = min;
      if (max) query.estimate_max = max;
      continue;
    }
    const param = condition.field.startsWith("cf_")
      ? condition.field
      : PARAM[condition.field];
    if (!param || values.length === 0) continue;
    const mapped =
      condition.field === "crew" ? values.map((id) => `crew:${id}`) : values;
    push(condition.op === "is_not" ? `${param}_not` : param, mapped);
  }
  return query;
}

/** filtersToQuery 的逆运算：视图保存的筛选参数还原成筛选条件（编辑视图时用）。 */
export function queryToConditions(query: FilterQuery): FilterCondition[] {
  const list = (value: string | string[] | undefined) =>
    value === undefined ? [] : Array.isArray(value) ? value : [value];
  const conditions: FilterCondition[] = [];
  const add = (field: FilterField, op: FilterOp, values: string[]) => {
    if (values.length) conditions.push({ field, op, values });
  };
  for (const [field, param] of Object.entries(PARAM) as [
    FilterField,
    ArrayParam,
  ][]) {
    // 小队与处理人共用 assignee 参数（由 assignee 这一项一并还原）
    if (field === "crew") continue;
    for (const op of ["is", "is_not"] as const) {
      const values = list(query[op === "is_not" ? `${param}_not` : param]);
      if (field !== "assignee") {
        add(field, op, values);
        continue;
      }
      add(
        "crew",
        op,
        values.filter((v) => v.startsWith("crew:")).map((v) => v.slice(5)),
      );
      add(
        "assignee",
        op,
        values.filter((v) => !v.startsWith("crew:")),
      );
    }
  }
  for (const op of ["is", "is_not"] as const) {
    const q = list(query[op === "is_not" ? "q_not" : "q"])[0];
    if (q) add("content", op, [q]);
  }
  for (const [key, values] of Object.entries(query)) {
    const m = /^(cf_[a-f0-9-]{36})(_not)?$/i.exec(key);
    if (m) add(m[1] as CustomFilterField, m[2] ? "is_not" : "is", list(values));
  }
  for (const which of ["start", "due"] as const) {
    const from = list(query[`${which}_from`])[0] ?? "";
    const to = list(query[`${which}_to`])[0] ?? "";
    if (from || to)
      add("date", query[`${which}_not`] ? "is_not" : "is", [which, from, to]);
  }
  if (query.estimate_none) add("estimate", "is", ["none"]);
  else {
    const min = list(query.estimate_min)[0] ?? "";
    const max = list(query.estimate_max)[0] ?? "";
    if (min || max) add("estimate", "is", [min, max]);
  }
  return conditions;
}

/** 条件是否已经填了值（chip 只显示有值的条件） */
export function conditionActive(condition: FilterCondition): boolean {
  if (condition.field === "content")
    return Boolean(condition.values[0]?.trim());
  if (condition.field === "date")
    return Boolean(condition.values[1] || condition.values[2]);
  if (condition.field === "estimate")
    return Boolean(condition.values[0] || condition.values[1]);
  return condition.values.length > 0;
}

// ---------- 显示设置 ----------

export type Layout = "board" | "list" | "table" | "gantt";
export type SortKey =
  | "manual"
  | "created"
  | "updated"
  | "priority"
  | "due"
  // 只在启用估时的空间任务页出现
  | "estimate"
  // 按迭代编号排序
  | "iteration"
  // 按专项排序
  | "project";
export type TaskProperty =
  | "id"
  | "status"
  | "assignee"
  | "priority"
  | "due"
  | "labels"
  | "created"
  | "updated"
  | "start"
  | "subtasks"
  | "estimate"
  | "iteration"
  | "project";

export const TASK_PROPERTIES: TaskProperty[] = [
  "id",
  "status",
  "assignee",
  "priority",
  "due",
  "labels",
  "created",
  "updated",
  "start",
  "subtasks",
  "estimate",
  "iteration",
  "project",
];

export const SORT_KEYS: SortKey[] = [
  "manual",
  "created",
  "updated",
  "priority",
  "due",
  "estimate",
  "iteration",
  "project",
];

// 分组字段：看板的列 / 行、列表的二级分组
export type GroupField =
  "status" | "assignee" | "crew" | "iteration" | "priority";
export type BoardColumnField = "status" | "assignee" | "crew" | "iteration";
export type BoardRowField = "none" | GroupField;
export type ListSubgroupField =
  "none" | "assignee" | "crew" | "iteration" | "priority";

export const BOARD_COLUMN_FIELDS: BoardColumnField[] = [
  "status",
  "assignee",
  "crew",
  "iteration",
];
export const BOARD_ROW_FIELDS: BoardRowField[] = [
  "none",
  "status",
  "assignee",
  "crew",
  "iteration",
  "priority",
];
export const LIST_SUBGROUP_FIELDS: ListSubgroupField[] = [
  "none",
  "assignee",
  "crew",
  "iteration",
  "priority",
];

export interface DisplaySettings {
  layout: Layout;
  sort: SortKey;
  completed: "all" | "hide";
  showSubtasks: boolean;
  showEmptyColumns: boolean;
  properties: TaskProperty[];
  /** 看板的列（默认状态） */
  boardColumns: BoardColumnField;
  /** 看板的行（默认不分组，不能与列相同） */
  boardRows: BoardRowField;
  /** 看板显示空行（行不分组时无效） */
  showEmptyRows: boolean;
  /** 列表在状态分组下的二级分组（默认不分组） */
  listSubgroup: ListSubgroupField;
}

export function defaultDisplay(page: "space" | "my"): DisplaySettings {
  return {
    layout: "board",
    // 我的任务跨空间，手动排序键没有意义，默认按更新时间
    sort: page === "my" ? "updated" : "manual",
    completed: "all",
    showSubtasks: true,
    showEmptyColumns: false,
    properties: [
      "id",
      "status",
      "assignee",
      "priority",
      "due",
      "labels",
      "created",
      "subtasks",
    ],
    boardColumns: "status",
    boardRows: "none",
    showEmptyRows: false,
    listSubgroup: "none",
  };
}

export function displayStorageKey(orgId: string, page: string): string {
  return `openfox:tasks-display:${orgId}:${page}`;
}

/**
 * 把任意对象（本机存储、视图的 display）规整成显示设置；字段非法时按默认值补齐。
 * 旧数据没有分组字段时回落默认；行与列相同时行回落「不分组」。
 */
export function normalizeDisplay(
  value: Partial<DisplaySettings>,
  fallback: DisplaySettings,
): DisplaySettings {
  const boardColumns = BOARD_COLUMN_FIELDS.includes(
    value.boardColumns as BoardColumnField,
  )
    ? (value.boardColumns as BoardColumnField)
    : fallback.boardColumns;
  const rows = BOARD_ROW_FIELDS.includes(value.boardRows as BoardRowField)
    ? (value.boardRows as BoardRowField)
    : fallback.boardRows;
  return {
    boardColumns,
    boardRows: rows === boardColumns ? "none" : rows,
    showEmptyRows: value.showEmptyRows === true,
    listSubgroup: LIST_SUBGROUP_FIELDS.includes(
      value.listSubgroup as ListSubgroupField,
    )
      ? (value.listSubgroup as ListSubgroupField)
      : fallback.listSubgroup,
    layout: ["board", "list", "table", "gantt"].includes(value.layout as string)
      ? (value.layout as Layout)
      : fallback.layout,
    sort: SORT_KEYS.includes(value.sort as SortKey)
      ? (value.sort as SortKey)
      : fallback.sort,
    completed: value.completed === "hide" ? "hide" : "all",
    showSubtasks:
      typeof value.showSubtasks === "boolean"
        ? value.showSubtasks
        : fallback.showSubtasks,
    showEmptyColumns: value.showEmptyColumns === true,
    properties: Array.isArray(value.properties)
      ? value.properties.filter((p): p is TaskProperty =>
          TASK_PROPERTIES.includes(p),
        )
      : fallback.properties,
  };
}

/** 读本机保存的显示设置。 */
export function loadDisplay(
  orgId: string,
  page: string,
  kind: "space" | "my",
): DisplaySettings {
  const fallback = defaultDisplay(kind);
  const raw = readLocal(displayStorageKey(orgId, page));
  if (!raw) return fallback;
  try {
    return normalizeDisplay(
      JSON.parse(raw) as Partial<DisplaySettings>,
      fallback,
    );
  } catch {
    return fallback;
  }
}

export function saveDisplay(
  orgId: string,
  page: string,
  value: DisplaySettings | null,
): void {
  writeLocal(
    displayStorageKey(orgId, page),
    value ? JSON.stringify(value) : null,
  );
}

/** 排序方向：手动与截止日期升序，其他降序（优先级降序 = 紧急在前）。 */
export function sortQuery(sort: SortKey): {
  sort: SortKey;
  order: "asc" | "desc";
} {
  return { sort, order: sort === "manual" || sort === "due" ? "asc" : "desc" };
}

/** 显示设置里影响查询的部分 */
export function displayQuery(display: DisplaySettings): Partial<TaskListQuery> {
  return {
    ...sortQuery(display.sort),
    completed: display.completed === "hide" ? "hide" : undefined,
    include_subtasks: display.showSubtasks ? undefined : false,
  };
}

// ---------- 看板列 ----------

const CATEGORY_ORDER = [
  "TRIAGE",
  "BACKLOG",
  "TODO",
  "IN_PROGRESS",
  "DONE",
  "CANCELLED",
  "DUPLICATE",
];

/** 按全局状态顺序（类别 → position）排序。 */
export function sortStatuses(statuses: TaskStatus[]): TaskStatus[] {
  return [...statuses].sort(
    (a, b) =>
      CATEGORY_ORDER.indexOf(a.category) - CATEGORY_ORDER.indexOf(b.category) ||
      a.position - b.position,
  );
}

/**
 * 空间看板的列：所选类型（或全部类型）工作流状态的并集，按全局顺序排列，排除已归档状态。
 * 类型还没加载出来时返回空数组。
 */
export function spaceColumns(
  statuses: TaskStatus[],
  types: SpaceTaskType[],
  typeId: string | null,
): TaskStatus[] {
  const ids = new Set<string>();
  for (const type of types) {
    if (typeId && type.id !== typeId) continue;
    for (const s of type.statuses) ids.add(s.status_id);
  }
  return sortStatuses(statuses.filter((s) => ids.has(s.id) && !s.archived_at));
}

// ---------- 看板移动 ----------

export interface BoardMove {
  taskId: string;
  fromStatusId: string;
  toStatusId: string;
  /** 落点在目标列（不含被拖动卡片）中的下标 */
  toIndex: number;
}

/**
 * 计算拖动后的 PATCH 请求体（不含 version）。返回 null 表示不需要请求。
 * - 手动排序且允许重排时带 position：before = 落点下一张，after = 落点上一张
 * - 其他排序（或我的任务）只允许跨列，只带 status_id
 */
export function planBoardMove(
  move: BoardMove,
  targetColumn: Pick<Task, "id">[],
  allowReorder: boolean,
): Omit<UpdateTaskInput, "version"> | null {
  const crossing = move.fromStatusId !== move.toStatusId;
  if (!crossing && !allowReorder) return null;
  const body: Omit<UpdateTaskInput, "version"> = {};
  if (crossing) body.status_id = move.toStatusId;
  if (allowReorder) {
    const rest = targetColumn.filter((t) => t.id !== move.taskId);
    const index = Math.max(0, Math.min(move.toIndex, rest.length));
    const after = rest[index - 1]?.id ?? null;
    const before = rest[index]?.id ?? null;
    if (!crossing) {
      // 原位放下：邻居没变就不发请求
      const original = targetColumn.findIndex((t) => t.id === move.taskId);
      if (original === index) return null;
    }
    body.position = { before_task_id: before, after_task_id: after };
  }
  return body;
}

// ---------- 其他 ----------

export type BranchFormat =
  | "username/identifier-title"
  | "identifier-title"
  | "username/identifier"
  | "identifier";

/** 分支名里的用户名：邮箱 @ 前的部分，小写，只保留 [a-z0-9-]。 */
export function branchUsername(email: string | null | undefined): string {
  const local = (email ?? "").split("@")[0] ?? "";
  return local.toLowerCase().replace(/[^a-z0-9-]/g, "");
}

/**
 * git 分支名：按格式拼 username / identifier / title。
 * identifier 取展示编号小写；title 取 ASCII slug（最长 40）；缺少 username 或 title 时对应段落省略。
 * 不传 options 时等价于 identifier-title（保持旧行为）。
 */
export function gitBranchName(
  displayId: string,
  title: string,
  options: { format?: BranchFormat; username?: string } = {},
): string {
  const slug = title
    .toLowerCase()
    .normalize("NFKD")
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 40)
    .replace(/-+$/g, "");
  const id = displayId.toLowerCase();
  const format = options.format ?? "identifier-title";
  const withTitle = format.endsWith("-title");
  const name = withTitle && slug ? `${id}-${slug}` : id;
  const username = format.startsWith("username/") ? options.username : "";
  return username ? `${username}/${name}` : name;
}

export const PRIORITY_ORDER: Priority[] = [
  "URGENT",
  "HIGH",
  "NORMAL",
  "LOW",
  "LOWEST",
  "NONE",
];
