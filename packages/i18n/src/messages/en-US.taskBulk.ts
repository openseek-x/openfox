import type { Messages } from "../translate";

/** Stage 10c · Task bulk actions (English). Mounted under tasks.bulk by en-US.ts. */
export const taskBulkEn: Messages["tasks"]["bulk"] = {
  bar: "Bulk actions",
  selected: "{count} task(s) selected",
  select: "Select {task}",
  status: "Change status",
  priority: "Change priority",
  assignee: "Change assignee",
  executor: "Change executor Agent",
  addLabel: "Add label",
  removeLabel: "Remove label",
  clear: "Clear selection",
  updated: "Updated {count} task(s)",
  conflict: "Some tasks were changed by someone else. Refresh and try again.",
  noCommonStatus: "The selected task types have no status in common",
  multipleSpaces:
    "The selected tasks are in different spaces. Bulk edit within one space.",
  tooMany: "You can change at most {max} tasks at a time",
  noLabels: "The selected tasks have no labels",
};
