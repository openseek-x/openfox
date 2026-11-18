import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";

import type {
  SpaceTaskType,
  TaskStatus,
  TaskStatusCategory,
} from "@openfox/api";

import {
  apiError,
  findCall,
  json,
  renderSettings,
  ts,
} from "../settings/testing";
import { spaceColumns, workflowStatuses } from "../tasks/model";

const base = "/v1/organizations/o1/spaces/s1";
const path = `${base}/task-types/type1`;

function status(
  id: string,
  category: TaskStatusCategory,
  position: number,
): TaskStatus {
  return {
    id,
    name: id,
    description: "",
    category,
    position,
    version: 1,
    created_at: ts,
    updated_at: ts,
    is_default: id === "First",
    collapsed_by_default: false,
    task_count: 12,
    type_usage: [],
  };
}

function sample() {
  const statuses = [
    status("First", "TODO", 0),
    status("Second", "TODO", 1),
    status("Done", "DONE", 0),
    status("Triage", "TRIAGE", 0),
  ];
  const current: SpaceTaskType = {
    id: "type1",
    organization_id: "o1",
    space_id: "s1",
    name: "Task",
    description: "",
    enabled: true,
    is_default: true,
    version: 3,
    created_at: ts,
    statuses: [statuses[3]!, statuses[0]!, statuses[1]!, statuses[2]!].map(
      (s, i) => ({
        status_id: s.id,
        name: s.name,
        category: s.category,
        position: i,
        default: s.id === "Second",
        task_count: i,
      }),
    ),
  };
  return { statuses, current };
}

function fixture(
  options: { readOnly?: boolean; conflict?: boolean; oldServer?: boolean } = {},
) {
  const { statuses, current } = sample();
  if (options.oldServer)
    current.statuses.forEach((item) => delete item.task_count);
  const calls = renderSettings(
    "/o/o1/settings/spaces/s1/task-types/type1?tab=workflow",
    (call) => {
      if (call.path === base)
        return json(200, {
          id: "s1",
          prefix: "OPEN",
          name: "Space",
          visibility: "shared",
          version: 1,
          created_at: ts,
          updated_at: ts,
          icon: null,
          purpose: "software_development",
          features: {
            views: true,
            projects: true,
            iterations: false,
            triage: true,
          },
          multi_assignee: false,
          estimation: {
            scale: "notUsed",
            extended: false,
            allow_zero: false,
            count_unestimated: false,
          },
          status: "active",
          retired_at: null,
          position: 0,
          can_manage: !options.readOnly,
          my_role: options.readOnly ? "MEMBER" : "OWNER",
          member_count: 1,
          task_type_count: 1,
          label_count: 0,
          task_count: 6,
        });
      if (call.path === `${base}/task-types`)
        return json(200, { task_types: [current] });
      if (call.path.endsWith("/task-statuses")) return json(200, { statuses });
      if (call.path.endsWith("/fields"))
        return json(200, { custom_fields: [] });
      if (call.path === path && call.method === "PATCH") {
        if (options.conflict) return apiError(409, "VERSION_CONFLICT");
        const patch = call.body as { status_ids: string[] };
        current.statuses = patch.status_ids.map((id, position) => ({
          ...current.statuses.find((item) => item.status_id === id)!,
          position,
        }));
        current.version += 1;
        return json(200, current);
      }
      return undefined;
    },
    options.readOnly ? "MEMBER" : "OWNER",
  );
  return { calls, current };
}

describe("任务类型工作流排序与计数", () => {
  it("同类别上移写入完整顺序及版本，回读计数和默认标记，TRIAGE保持首类别", async () => {
    const user = userEvent.setup();
    const { calls, current } = fixture();
    const todo = await screen.findByRole("region", { name: "未开始" });
    expect(
      within(todo).getByRole("button", { name: "上移 First" }),
    ).toBeDisabled();
    expect(
      within(todo).getByRole("button", { name: "下移 Second" }),
    ).toBeDisabled();
    expect(within(todo).getByText("1 个任务")).toBeInTheDocument();
    expect(within(todo).getByText("2 个任务")).toBeInTheDocument();
    const triage = screen.getByRole("region", { name: "待分拣" });
    expect(within(triage).getByText("0 个任务")).toBeInTheDocument();
    expect(within(triage).queryByRole("button")).not.toBeInTheDocument();
    await user.click(within(todo).getByRole("button", { name: "上移 Second" }));
    await waitFor(() =>
      expect(findCall(calls, "PATCH", path)?.body).toEqual({
        status_ids: ["Triage", "Second", "First", "Done"],
        version: 3,
      }),
    );
    await waitFor(() =>
      expect(within(todo).getAllByRole("listitem")[0]).toHaveTextContent(
        "Second",
      ),
    );
    expect(current.statuses.find((item) => item.default)?.status_id).toBe(
      "Second",
    );
    expect(
      within(todo).getByRole("button", { name: "上移 Second" }),
    ).toBeDisabled();
  });

  it("409回读服务端顺序并提示冲突，不留下假的成功顺序", async () => {
    const user = userEvent.setup();
    const { current } = fixture({ conflict: true });
    await user.click(
      await screen.findByRole("button", { name: "上移 Second" }),
    );
    await screen.findByRole("status");
    expect(current.version).toBe(3);
    expect(
      within(screen.getByRole("region", { name: "未开始" })).getAllByRole(
        "listitem",
      )[0],
    ).toHaveTextContent("First");
  });

  it("只读成员看见计数，无排序和移除入口；旧服务端缺失计数显示未知", async () => {
    fixture({ readOnly: true, oldServer: true });
    const todo = await screen.findByRole("region", { name: "未开始" });
    expect(within(todo).queryByRole("button")).not.toBeInTheDocument();
    expect(within(todo).getAllByText("—")).toHaveLength(2);
    expect(within(todo).queryByText("0 个任务")).not.toBeInTheDocument();
  });
});

describe("任务列表、看板和选择器工作流消费者", () => {
  it("类型局部顺序优先于全局position，类别固定，其他类型和全部视图保留原顺序", () => {
    const { current, statuses } = sample();
    const other = { ...current, id: "type2" };
    current.statuses = current.statuses.map((item) => ({
      ...item,
      position:
        item.status_id === "Second" ? 0 : item.status_id === "First" ? 1 : 9,
    }));
    const ids = (items: TaskStatus[]) => items.map((item) => item.id);
    expect(ids(spaceColumns(statuses, [current, other], current.id))).toEqual([
      "Triage",
      "Second",
      "First",
      "Done",
    ]);
    expect(ids(workflowStatuses(statuses, current))).toEqual([
      "Triage",
      "Second",
      "First",
      "Done",
    ]);
    expect(ids(spaceColumns(statuses, [current, other], other.id))).toEqual([
      "Triage",
      "First",
      "Second",
      "Done",
    ]);
    expect(ids(spaceColumns(statuses, [current, other], null))).toEqual([
      "Triage",
      "First",
      "Second",
      "Done",
    ]);
    expect(statuses[0]!.position).toBe(0);
  });

  it("排除归档及工作流外状态，类型缺失和空工作流不产生列", () => {
    const { current, statuses } = sample();
    statuses[1]!.archived_at = ts;
    statuses.push(status("Outside", "TODO", 2));
    expect(workflowStatuses(statuses, current).map((item) => item.id)).toEqual([
      "Triage",
      "First",
      "Done",
    ]);
    expect(spaceColumns(statuses, [current], "missing")).toEqual([]);
    expect(workflowStatuses(statuses, { ...current, statuses: [] })).toEqual(
      [],
    );
  });
});
