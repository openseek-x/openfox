import type { CustomFieldKind } from "@openfox/api";
import { parseYmd, todayYmd } from "@openfox/ui";

export const searchTaskFields = [
  "status",
  "priority",
  "type",
  "assignee",
  "crew",
  "label",
  "content",
  "relation",
  "dueDate",
  "creator",
  "project",
  "sprint",
] as const;
export type SearchTaskField =
  (typeof searchTaskFields)[number] | "createdAt" | `cf_${string}`;
export const createdPresets = [
  "past_1_day",
  "past_3_days",
  "past_1_week",
  "past_1_month",
  "past_3_months",
  "past_6_months",
  "past_1_year",
] as const;
export const duePresets = [
  "overdue",
  "next_1_day",
  "next_3_days",
  "next_1_week",
  "next_1_month",
  "next_3_months",
  "no_due_date",
] as const;
export const sprintPresets = [
  "anyPast",
  "previous",
  "current",
  "cooldown",
  "upcoming",
  "anyFuture",
] as const;
const basic = ["is", "is_not"] as const;
const textOps = [...basic, "contains", "not_contains", "is_empty"] as const;
const numeric = [
  ...basic,
  "lt",
  "not_lt",
  "lte",
  "not_lte",
  "gt",
  "not_gt",
  "gte",
  "not_gte",
  "between",
  "not_between",
  "is_empty",
] as const;
const dateOps = [
  ...basic,
  "before",
  "not_before",
  "before_or_on",
  "not_before_or_on",
  "after",
  "not_after",
  "after_or_on",
  "not_after_or_on",
  "between",
  "not_between",
  "is_empty",
] as const;
export type SearchTaskOperator =
  | (typeof textOps | typeof numeric | typeof dateOps)[number]
  | "is_true"
  | "is_false";
export type TaskFilterParams = Record<string, string | string[]>;
export interface SearchTaskCondition {
  field: SearchTaskField;
  op: SearchTaskOperator;
  values: string[];
}

export function taskFilterOperators(
  field: SearchTaskField,
  kind?: CustomFieldKind,
): readonly SearchTaskOperator[] {
  if (field === "crew") return ["is"];
  if (!kind) return basic;
  if (kind === "BOOLEAN") return ["is_true", "is_false"];
  if (kind === "NUMBER") return numeric;
  if (kind === "DATE") return dateOps;
  if (kind === "SELECT" || kind === "MULTI_SELECT" || kind === "MEMBER")
    return [...basic, "is_empty"];
  return textOps;
}
export const noOperand = (op: SearchTaskOperator) =>
  op === "is_empty" || op === "is_true" || op === "is_false";
export const twoOperands = (op: SearchTaskOperator) =>
  op === "between" || op === "not_between";

function localDate(ymd: string) {
  const p = parseYmd(ymd);
  return p ? new Date(p.year, p.month - 1, p.day) : null;
}
function days(date: Date, amount: number) {
  const next = new Date(date);
  next.setDate(next.getDate() + amount);
  return next;
}
// Calendar-month arithmetic clamps the last day (March 31 minus one month is February 28/29).
function months(date: Date, amount: number) {
  const next = new Date(date);
  next.setDate(1);
  next.setMonth(next.getMonth() + amount + 1, 0);
  next.setDate(Math.min(next.getDate(), date.getDate()));
  return next;
}
function dateRange(field: "dueDate" | "createdAt", value: string, now: Date) {
  if (field === "dueDate") {
    const today = todayYmd(now);
    if (value === "no_due_date") return "none";
    if (value === "overdue") return "/" + today;
    const offsets: Record<string, Date> = {
      next_1_day: days(now, 1),
      next_3_days: days(now, 3),
      next_1_week: days(now, 7),
      next_1_month: months(now, 1),
      next_3_months: months(now, 3),
    };
    const end = offsets[value];
    return end ? today + "/" + todayYmd(days(end, 1)) : "";
  }
  const offsets: Record<string, Date> = {
    past_1_day: days(now, -1),
    past_3_days: days(now, -3),
    past_1_week: days(now, -7),
    past_1_month: months(now, -1),
    past_3_months: months(now, -3),
    past_6_months: months(now, -6),
    past_1_year: months(now, -12),
  };
  if (Object.hasOwn(offsets, value)) return offsets[value]!.toISOString() + "/";
  const [kind, payload] = value.split(":");
  const [start, end] = (payload ?? "").split("|");
  const from = localDate(start ?? "");
  if (!from) return "";
  if (kind === "after") return from.toISOString() + "/";
  if (kind === "before") return "/" + from.toISOString();
  const to = localDate(end ?? "");
  return kind === "between" && to && from <= to
    ? from.toISOString() + "/" + days(to, 1).toISOString()
    : "";
}

const params: Record<(typeof searchTaskFields)[number] | "createdAt", string> =
  {
    status: "status_id",
    priority: "priority",
    type: "type_id",
    assignee: "assignee",
    crew: "crew_id",
    label: "label_id",
    content: "task_q",
    relation: "relation",
    dueDate: "due_range",
    createdAt: "created_range",
    creator: "creator_id",
    project: "project_id",
    sprint: "iteration_id",
  };
const relations: Record<string, string> = {
  parentTasks: "subtask",
  subtasks: "parent",
  blockedTasks: "blocked",
  blockingTasks: "blocking",
  duplicates: "duplicate",
};
export function searchTaskFilterParams(
  conditions: SearchTaskCondition[],
  now = new Date(),
): TaskFilterParams {
  const out: TaskFilterParams = {};
  for (const { field, op, values } of conditions) {
    if (field.startsWith("cf_")) {
      if (!noOperand(op) && !values.length) continue;
      if (op !== "is" && op !== "is_not") out[field + "_op"] = op;
      if (!noOperand(op)) out[field + (op === "is_not" ? "_not" : "")] = values;
      continue;
    }
    const key = params[field as keyof typeof params];
    if (!key || !values.length) continue;
    const param = key + (op === "is_not" ? "_not" : "");
    if (field === "content") {
      if (values[0]?.trim()) out[param] = values[0].trim();
    } else if (field === "dueDate" || field === "createdAt") {
      const ranges = values
        .map((v) => dateRange(field, v, now))
        .filter(Boolean);
      if (ranges.length) out[param] = ranges;
    } else if (field === "creator") {
      for (const kind of ["user", "agent"]) {
        const ids = values
          .filter((v) => v.startsWith(kind + ":"))
          .map((v) => v.slice(kind.length + 1));
        if (ids.length)
          out[
            (kind === "agent" ? "creator_agent_id" : "creator_id") +
              (op === "is_not" ? "_not" : "")
          ] = ids;
      }
    } else if (field === "relation") {
      out[param] = values.map((v) => relations[v]!).filter(Boolean);
    } else out[param] = values;
  }
  return out;
}
