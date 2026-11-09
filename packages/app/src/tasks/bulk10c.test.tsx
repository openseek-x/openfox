import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import type { Task } from "@openfox/api";

import { App, createQueryClient } from "../App";
import { commonStatuses } from "./bulk/BulkActionBar";
import {
  backend,
  json,
  makeTask,
  ORG,
  SPACE,
  statuses,
  taskType,
  type Call,
  type Override,
} from "./test-backend";

// 任务批量操作：列表 / 表格多选 → 底部操作栏 → POST .../tasks/batch-update。

const o = `/v1/organizations/${ORG}`;
const sp = `${o}/spaces/${SPACE}`;
const batchPath = `${sp}/tasks/batch-update`;

function useLayout(layout: "list" | "table") {
  localStorage.setItem(
    `openfox:tasks-display:${ORG}:space:${SPACE}`,
    JSON.stringify({ layout }),
  );
}

/** 批量接口：按请求返回改过优先级的任务（version + 1）。 */
const batchOk: Override = (call) => {
  if (call.path !== batchPath || call.method !== "POST") return undefined;
  const body = call.body as {
    tasks: { id: string; version: number }[];
    patch: { priority?: Task["priority"] };
  };
  return json(200, {
    tasks: body.tasks.map((ref) => {
      const seq = Number(ref.id.split("-")[1]);
      return makeTask(seq, seq === 3 ? "st2" : "st1", {
        version: ref.version + 1,
        priority: body.patch.priority ?? "NONE",
      });
    }),
  });
};

function renderTasks(...overrides: Override[]) {
  const server = backend((call) => {
    for (const override of overrides) {
      const response = override(call);
      if (response) return response;
    }
    return undefined;
  });
  const queryClient = createQueryClient();
  queryClient.setDefaultOptions({ queries: { retry: false } });
  render(
    <MemoryRouter initialEntries={[`/o/${ORG}/spaces/${SPACE}/tasks`]}>
      <App api={server.api} queryClient={queryClient} locale="zh-CN" />
    </MemoryRouter>,
  );
  return server;
}

const batchCalls = (calls: Call[]) =>
  calls.filter((c) => c.method === "POST" && c.path === batchPath);

beforeEach(() => {
  localStorage.setItem(`openfox:onboarding:${ORG}`, JSON.stringify({}));
});
afterEach(() => localStorage.clear());

describe("任务批量操作", () => {
  it("列表：勾选两条 → 操作栏 → 改优先级 → 请求体正确、提示已更新、选择清空", async () => {
    useLayout("list");
    const user = userEvent.setup();
    const server = renderTasks(batchOk);
    const list = await screen.findByRole("region", { name: "任务列表" });
    expect(screen.queryByRole("toolbar", { name: "批量操作" })).toBeNull();
    await user.click(
      await within(list).findByRole("checkbox", { name: "选择 OPEN-1" }),
    );
    await user.click(
      within(list).getByRole("checkbox", { name: "选择 OPEN-2" }),
    );
    const bar = await screen.findByRole("toolbar", { name: "批量操作" });
    expect(within(bar).getByText("已选择 2 个任务")).toBeInTheDocument();

    await user.click(within(bar).getByRole("button", { name: "修改优先级" }));
    await user.click(await screen.findByRole("option", { name: "高" }));

    await waitFor(() => expect(batchCalls(server.calls)).toHaveLength(1));
    const call = batchCalls(server.calls)[0]!;
    expect(call.body).toEqual({
      tasks: [
        { id: "task-1", version: 1 },
        { id: "task-2", version: 1 },
      ],
      patch: { priority: "HIGH" },
    });
    expect(call.headers.get("Idempotency-Key")).toBeTruthy();
    expect(await screen.findByText("已更新 2 个任务")).toBeInTheDocument();
    await waitFor(() =>
      expect(screen.queryByRole("toolbar", { name: "批量操作" })).toBeNull(),
    );
    expect(
      within(list).getByRole("checkbox", { name: "选择 OPEN-1" }),
    ).not.toBeChecked();
    // 留在当前页面与布局
    expect(
      screen.getByRole("region", { name: "任务列表" }),
    ).toBeInTheDocument();
  });

  it("列表：Shift 连选同一分组内的任务；处理人清空发 id: null", async () => {
    useLayout("list");
    const user = userEvent.setup();
    const server = renderTasks(batchOk);
    const list = await screen.findByRole("region", { name: "任务列表" });
    await user.click(
      await within(list).findByRole("checkbox", { name: "选择 OPEN-1" }),
    );
    await user.keyboard("{Shift>}");
    await user.click(
      within(list).getByRole("checkbox", { name: "选择 OPEN-2" }),
    );
    await user.keyboard("{/Shift}");
    const bar = await screen.findByRole("toolbar", { name: "批量操作" });
    expect(within(bar).getByText("已选择 2 个任务")).toBeInTheDocument();
    await user.click(within(bar).getByRole("button", { name: "修改处理人" }));
    await user.click(await screen.findByRole("option", { name: "无处理人" }));
    await waitFor(() => expect(batchCalls(server.calls)).toHaveLength(1));
    expect(
      (batchCalls(server.calls)[0]!.body as { patch: unknown }).patch,
    ).toEqual({ assignee: { kind: "HUMAN", id: null } });
  });

  it("表格：状态选择器只列共同可用的状态", async () => {
    useLayout("table");
    const user = userEvent.setup();
    // task-3 是另一种类型 t2，工作流只有「待处理」「进行中」
    const types: Override = (call) => {
      if (call.path === `${sp}/task-types`)
        return json(200, {
          task_types: [
            taskType,
            {
              ...taskType,
              id: "t2",
              name: "缺陷",
              is_default: false,
              statuses: taskType.statuses.filter((s) => s.status_id !== "st3"),
            },
          ],
        });
      if (call.path === `${sp}/tasks` && call.method === "GET")
        return json(200, {
          tasks: [
            makeTask(1, "st1"),
            makeTask(2, "st1"),
            makeTask(3, "st2", { type_id: "t2", type_name: "缺陷" }),
          ],
          next_cursor: "",
        });
      return undefined;
    };
    renderTasks(types, batchOk);
    const table = await screen.findByRole("table", { name: "任务表格" });
    await user.click(
      await within(table).findByRole("checkbox", { name: "选择 OPEN-1" }),
    );
    await user.click(
      within(table).getByRole("checkbox", { name: "选择 OPEN-3" }),
    );
    const bar = await screen.findByRole("toolbar", { name: "批量操作" });
    await user.click(within(bar).getByRole("button", { name: "修改状态" }));
    const menu = await screen.findByRole("listbox", { name: "修改状态" });
    const names = within(menu)
      .getAllByRole("option")
      .map((option) => option.textContent);
    expect(names).toEqual(["待处理", "进行中"]);
  });

  it("409 版本冲突：提示需刷新、选择保留以便重试", async () => {
    useLayout("table");
    const user = userEvent.setup();
    const conflict: Override = (call) =>
      call.path === batchPath
        ? json(409, {
            error: {
              code: "VERSION_CONFLICT",
              message: "x",
              request_id: "r",
              details: { task_ids: ["task-2"] },
            },
          })
        : undefined;
    const server = renderTasks(conflict);
    const table = await screen.findByRole("table", { name: "任务表格" });
    await user.click(
      await within(table).findByRole("checkbox", { name: "全选" }),
    );
    const bar = await screen.findByRole("toolbar", { name: "批量操作" });
    expect(within(bar).getByText("已选择 3 个任务")).toBeInTheDocument();
    await user.click(within(bar).getByRole("button", { name: "修改优先级" }));
    await user.click(await screen.findByRole("option", { name: "紧急" }));
    expect(
      await screen.findByText("部分任务已被他人修改，请刷新后重试"),
    ).toBeInTheDocument();
    expect(batchCalls(server.calls)).toHaveLength(1);
    expect(
      within(screen.getByRole("toolbar", { name: "批量操作" })).getByText(
        "已选择 3 个任务",
      ),
    ).toBeInTheDocument();
    // 失败后刷新列表数据
    await waitFor(() =>
      expect(
        server.calls.filter(
          (c) => c.method === "GET" && c.path === `${sp}/tasks`,
        ).length,
      ).toBeGreaterThan(1),
    );
  });

  it("已停用空间：操作栏按钮禁用并提示原因，不发请求", async () => {
    useLayout("table");
    const user = userEvent.setup();
    const retired: Override = (call) => {
      if (call.path === `${o}/spaces`) return json(200, { spaces: [] });
      if (call.path === sp)
        return json(200, {
          id: SPACE,
          prefix: "OPEN",
          name: "默认空间",
          visibility: "shared",
          version: 2,
          created_at: "2026-10-01T00:00:00Z",
          updated_at: "2026-10-01T00:00:00Z",
          icon: null,
          purpose: "software_development",
          features: {
            views: true,
            projects: true,
            iterations: false,
            triage: false,
          },
          multi_assignee: false,
          estimation: {
            scale: "notUsed",
            extended: false,
            allow_zero: false,
            count_unestimated: false,
          },
          status: "retired",
          retired_at: "2026-10-28T00:00:00Z",
          position: null,
          my_role: "OWNER",
          can_manage: true,
          member_count: 1,
          task_type_count: 1,
          label_count: 0,
          task_count: 0,
        });
      return undefined;
    };
    const server = renderTasks(retired, batchOk);
    const table = await screen.findByRole("table", { name: "任务表格" });
    await user.click(
      await within(table).findByRole("checkbox", { name: "选择 OPEN-1" }),
    );
    const bar = await screen.findByRole("toolbar", { name: "批量操作" });
    await waitFor(() =>
      expect(
        within(bar).getByRole("button", { name: "修改优先级" }),
      ).toBeDisabled(),
    );
    expect(
      within(bar).getByRole("button", { name: "修改状态" }),
    ).toBeDisabled();
    expect(
      within(bar).getAllByLabelText("此空间已停用，只能查看。").length,
    ).toBeGreaterThan(0);
    expect(batchCalls(server.calls)).toHaveLength(0);
  });
});

describe("commonStatuses", () => {
  const all = statuses as unknown as Parameters<typeof commonStatuses>[2];
  const types = [
    { id: "t1", statuses: [{ status_id: "st1" }, { status_id: "st2" }] },
    { id: "t2", statuses: [{ status_id: "st2" }, { status_id: "st3" }] },
  ];
  it("取各类型工作流的交集，按全局顺序", () => {
    expect(
      commonStatuses([{ type_id: "t1" }, { type_id: "t2" }], types, all).map(
        (s) => s.id,
      ),
    ).toEqual(["st2"]);
  });
  it("未知类型或没有任务时为空", () => {
    expect(commonStatuses([{ type_id: "t9" }], types, all)).toEqual([]);
    expect(commonStatuses([], types, all)).toEqual([]);
  });
});
