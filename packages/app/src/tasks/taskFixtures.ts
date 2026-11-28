import type { Task } from "@openfox/api";

export const ORG = "o1";
export const SPACE = "s1";

export const statuses = [
  status("st1", "待处理", "TODO", 0, true),
  status("st2", "进行中", "IN_PROGRESS", 0),
  status("st3", "已完成", "DONE", 0),
];

function status(
  id: string,
  name: string,
  category: string,
  position: number,
  isDefault = false,
) {
  return {
    id,
    name,
    description: "",
    category,
    position,
    version: 1,
    created_at: "2026-10-01T00:00:00Z",
    updated_at: "2026-10-01T00:00:00Z",
    is_default: isDefault,
    collapsed_by_default: false,
    task_count: 0,
    type_usage: [],
  };
}

export const taskType = {
  organization_id: ORG,
  space_id: SPACE,
  id: "t1",
  name: "任务",
  description: "",
  enabled: true,
  is_default: true,
  version: 1,
  created_at: "2026-10-01T00:00:00Z",
  statuses: statuses.map((s, i) => ({
    status_id: s.id,
    name: s.name,
    category: s.category,
    position: i,
    default: i === 0,
  })),
};

export function makeTask(
  seq: number,
  statusId: string,
  extra: Partial<Task> = {},
): Task {
  const s = statuses.find((x) => x.id === statusId)!;
  return {
    organization_id: ORG,
    space_id: SPACE,
    id: `task-${seq}`,
    display_id: `open-${seq}`,
    sequence: seq,
    type_id: "t1",
    type_name: "任务",
    status_id: s.id,
    status_name: s.name,
    status_category: s.category as Task["status_category"],
    title: `任务标题 ${seq}`,
    description: "",
    acceptance: "",
    priority: "NONE",
    version: 1,
    created_at: "2026-10-20T00:00:00Z",
    updated_at: "2026-10-20T00:00:00Z",
    labels: [],
    start_date: null,
    due_date: null,
    parent_task_id: null,
    parent: null,
    rank: `h${seq}`,
    assignees: [],
    label_items: [],
    subtask_count: 0,
    subtask_done_count: 0,
    created_by: "u1",
    type_icon: null,
    status_color: null,
    deleted: false,
    iteration_id: null,
    project_id: null,
    triage: null,
    duplicate_of: null,
    ...extra,
  };
}
