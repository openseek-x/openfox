import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { App, createQueryClient } from "../App";
import {
  backend,
  json,
  makeTask,
  ORG,
  SPACE,
  statuses,
  taskType,
  type Override,
} from "../tasks/test-backend";

// 分拣队列、四个动作、空间设置（责任与规则编辑器）、新建任务默认状态、任务详情操作条。

const o = `/v1/organizations/${ORG}`;
const sp = `${o}/spaces/${SPACE}`;

const status = (
  id: string,
  name: string,
  category: string,
  position: number,
) => ({
  ...statuses[0]!,
  id,
  name,
  category,
  position,
  is_default: false,
});
const allStatuses = [
  status("st0", "待分拣", "TRIAGE", 0),
  status("st9", "待规划", "BACKLOG", 1),
  ...statuses,
];
const type = {
  ...taskType,
  statuses: allStatuses.map((s, i) => ({
    status_id: s.id,
    name: s.name,
    category: s.category,
    position: i,
    default: s.id === "st1",
  })),
};

const space = (triage = true) => ({
  id: SPACE,
  prefix: "OPEN",
  name: "默认空间",
  visibility: "shared",
  version: 2,
  created_at: "2026-10-01T00:00:00Z",
  updated_at: "2026-10-01T00:00:00Z",
  icon: null,
  purpose: "software_development",
  features: { views: false, projects: false, iterations: false, triage },
  multi_assignee: false,
  estimation: {
    scale: "notUsed",
    extended: false,
    allow_zero: false,
    count_unestimated: false,
  },
  status: "active",
  retired_at: null,
  position: null,
  my_role: "OWNER",
  can_manage: true,
  member_count: 1,
  task_type_count: 1,
  label_count: 0,
  task_count: 0,
});

const triageTask = (
  seq: number,
  title: string,
  extra: Record<string, unknown> = {},
) =>
  makeTask(seq, "st1", {
    title,
    status_id: "st0",
    status_name: "待分拣",
    status_category: "TRIAGE",
    triage: {
      entered_at: "2026-10-29T08:00:00Z",
      source: "guest",
      snoozed_until: null,
      creator: { id: "u2", email: "li@acme.io" },
    },
    ...extra,
  } as never);

const rule = (id: string, extra: Record<string, unknown> = {}) => ({
  id,
  space_id: SPACE,
  name: `规则 ${id}`,
  position: 0,
  enabled: true,
  conditions: [
    { conditions: [{ field: "source", op: "is", values: ["guest"] }] },
  ],
  actions: [{ type: "set_priority", value: "HIGH" }],
  last_used_at: null,
  version: 3,
  ...extra,
});

const settings = (extra: Record<string, unknown> = {}) => ({
  enabled: true,
  require_priority: false,
  action: "none",
  members: [],
  rules: [],
  can_manage: true,
  version: 2,
  ...extra,
});

const error = (status: number, code: string) =>
  json(status, { error: { code, message: code, request_id: "r" } });

function renderAt(
  path: string,
  opts: {
    tasks?: object[];
    settings?: object;
    count?: number;
    triage?: boolean;
    override?: Override;
  } = {},
) {
  const queue = opts.tasks ?? [
    triageTask(1, "导出 CSV 乱码"),
    triageTask(2, "看板无法滚动"),
  ];
  const server = backend((call) => {
    const custom = opts.override?.(call);
    if (custom) return custom;
    const match = /\/tasks\/by-display-id\/(OPEN-\d+)$/i.exec(call.path);
    if (match) {
      const task = queue.find(
        (t) =>
          (t as { display_id: string }).display_id.toUpperCase() ===
          match[1]!.toUpperCase(),
      );
      if (task) return json(200, { ...task, description_reactions: [] });
    }
    if (call.path === sp) return json(200, space(opts.triage ?? true));
    if (call.path === `${o}/task-statuses`)
      return json(200, { statuses: allStatuses });
    if (call.path === `${sp}/task-types`)
      return json(200, { task_types: [type] });
    if (call.path === `${sp}/triage`)
      return json(200, { tasks: queue, next_cursor: "", total: queue.length });
    if (call.path === `${sp}/triage/count`)
      return json(200, { total: opts.count ?? queue.length, snoozed: 0 });
    if (call.path === `${sp}/triage-settings` && call.method === "GET")
      return json(200, opts.settings ?? settings());
    if (call.path === `${sp}/triage-settings` && call.method === "PATCH")
      return json(200, {
        ...(opts.settings ?? settings()),
        ...(call.body as object),
      });
    if (call.path === `${sp}/triage-members`) return json(200, { members: [] });
    if (call.path.startsWith(`${sp}/triage-rules`)) {
      if (call.method === "DELETE") return new Response(null, { status: 204 });
      if (call.path.endsWith("/reorder")) return json(200, { rules: [] });
      return json(
        call.method === "POST" ? 201 : 200,
        rule("new", { enabled: false, version: 1 }),
      );
    }
    if (
      /\/triage\/(accept|decline|duplicate)$/.test(call.path) ||
      call.path.endsWith("/triage-snooze")
    )
      return json(200, queue[0]);
    if (call.path.endsWith("/followers") && call.method !== "GET")
      return json(200, {});
    return undefined;
  });
  const queryClient = createQueryClient();
  queryClient.setDefaultOptions({ queries: { retry: false } });
  render(
    <MemoryRouter initialEntries={[path]}>
      <App api={server.api} queryClient={queryClient} locale="zh-CN" />
    </MemoryRouter>,
  );
  return server;
}

const queuePage = `/o/${ORG}/spaces/${SPACE}/triage`;
const settingsPage = `/o/${ORG}/settings/spaces/${SPACE}/triage`;

beforeEach(() => {
  localStorage.setItem(`openfox:onboarding:${ORG}`, JSON.stringify({}));
});
afterEach(() => localStorage.clear());

describe("分拣队列", () => {
  it("渲染任务，默认按进入分拣台时间倒序，勾选后请求带 include_snoozed", async () => {
    const user = userEvent.setup();
    const server = renderAt(queuePage);
    expect(
      await screen.findByRole("heading", { name: "2 个待分拣任务" }),
    ).toBeInTheDocument();
    const first = server.find("GET", "/triage")[0]!;
    expect(first.query.get("sort")).toBe("created");
    expect(first.query.get("order")).toBe("desc");
    await user.click(screen.getByRole("button", { name: "显示选项" }));
    await user.click(screen.getByRole("switch", { name: "显示稍后处理" }));
    await waitFor(() =>
      expect(
        server
          .find("GET", "/triage")
          .some((c) => c.query.get("include_snoozed") === "true"),
      ).toBe(true),
    );
  });

  it("上下键在列表里切换选中任务", async () => {
    const user = userEvent.setup();
    renderAt(queuePage);
    const list = (await screen.findAllByText("导出 CSV 乱码"))[0]!.closest(
      "ul",
    )!;
    await user.click(
      within(list).getByRole("button", { name: /导出 CSV 乱码/ }),
    );
    list.focus();
    await user.keyboard("{ArrowDown}");
    expect(
      within(list).getByText("看板无法滚动").closest("button"),
    ).toHaveClass("bg-surface-active");
    await user.keyboard("{ArrowUp}");
    expect(
      within(list).getByText("导出 CSV 乱码").closest("button"),
    ).toHaveClass("bg-surface-active");
  });

  it("未启用时显示去开启入口", async () => {
    renderAt(queuePage, { triage: false });
    expect(
      await screen.findByRole("button", { name: "去开启该功能" }),
    ).toBeInTheDocument();
  });
});

describe("分拣动作", () => {
  it("接受：待接受邀请处理人、下一状态、优先级、评论和订阅一次提交", async () => {
    const user = userEvent.setup();
    const server = renderAt(queuePage, {
      override: (call) =>
        call.path.endsWith("/invites")
          ? json(200, {
              invitations: [
                {
                  id: "iv1",
                  email: "triage@example.test",
                  role: "MEMBER",
                  status: "PENDING",
                  expires_at: "2099-01-01",
                  space_ids: [SPACE],
                },
              ],
            })
          : undefined,
    });
    await user.click(
      await screen.findByRole("button", { name: /导出 CSV 乱码/ }),
    );
    await user.click(await screen.findByRole("button", { name: "接受" }));
    const dialog = await screen.findByRole("dialog");
    await user.type(within(dialog).getByLabelText("评论"), "已确认");
    await user.click(within(dialog).getByRole("button", { name: /^处理人/ }));
    await user.click(
      await screen.findByRole("option", { name: /triage@example.test/ }),
    );
    await user.click(within(dialog).getByRole("button", { name: /^优先级/ }));
    await user.click(await screen.findByRole("option", { name: /高/ }));
    await user.click(within(dialog).getByRole("button", { name: "接受" }));
    await waitFor(() =>
      expect(server.find("POST", "/triage/accept")).toHaveLength(1),
    );
    expect(server.find("POST", "/triage/accept")[0]!.body).toMatchObject({
      version: 1,
      priority: "HIGH",
      comment: "已确认",
      status_id: "st9",
      subscribe: true,
      assignees: [{ kind: "INVITATION", id: "iv1" }],
    });
  });

  it("接受：要求优先级时显示错误提示", async () => {
    const user = userEvent.setup();
    renderAt(queuePage, {
      override: (call) =>
        call.path.endsWith("/triage/accept")
          ? error(400, "PRIORITY_REQUIRED")
          : undefined,
    });
    await user.click(
      await screen.findByRole("button", { name: /导出 CSV 乱码/ }),
    );
    await user.click(await screen.findByRole("button", { name: "接受" }));
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: "接受" }));
    expect(
      await within(dialog).findByText("接受前需要设置优先级。"),
    ).toBeInTheDocument();
  });

  it("要求优先级时先选值；退出接受表单不会修改任务", async () => {
    const user = userEvent.setup();
    const server = renderAt(queuePage, {
      settings: settings({ require_priority: true }),
      tasks: [triageTask(1, "导出 CSV 乱码", { priority: "NONE" })],
    });
    await user.click(
      await screen.findByRole("button", { name: /导出 CSV 乱码/ }),
    );
    await user.keyboard("1");
    const option = await screen.findByRole("option", { name: /高/ });
    expect(screen.queryByRole("option", { name: /无优先级/ })).toBeNull();
    await user.click(option);
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(server.find("POST", "/triage/accept")).toHaveLength(0);
  });

  it("拒绝：请求体带拒绝原因", async () => {
    const user = userEvent.setup();
    const server = renderAt(queuePage);
    await user.click(
      await screen.findByRole("button", { name: /导出 CSV 乱码/ }),
    );
    await user.click(await screen.findByRole("button", { name: "拒绝" }));
    const dialog = await screen.findByRole("dialog");
    await user.type(
      within(dialog).getByPlaceholderText("填写拒绝原因…"),
      "不在范围内",
    );
    await user.click(within(dialog).getByRole("switch", { name: "订阅更新" }));
    await user.click(within(dialog).getByRole("button", { name: "拒绝" }));
    await waitFor(() =>
      expect(server.find("POST", "/triage/decline")).toHaveLength(1),
    );
    expect(server.find("POST", "/triage/decline")[0]!.body).toEqual({
      version: 1,
      comment: "不在范围内",
      subscribe: false,
    });
  });

  it("标记重复：搜索选中目标后请求体带 duplicate_of_task_id", async () => {
    const user = userEvent.setup();
    const server = renderAt(queuePage);
    await user.click(
      await screen.findByRole("button", { name: /导出 CSV 乱码/ }),
    );
    await user.click(await screen.findByRole("button", { name: "标记重复" }));
    const search = await screen.findByRole("combobox", {
      name: "选择重复的目标任务",
    });
    await user.type(search, "OPEN-3");
    await screen.findByRole("button", { name: /OPEN-3/i });
    await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(1));
    await user.keyboard("{Enter}");
    await waitFor(() =>
      expect(server.find("POST", "/triage/duplicate")).toHaveLength(1),
    );
    expect(server.find("POST", "/triage/duplicate")[0]!.body).toEqual({
      version: 1,
      duplicate_of_task_id: "task-3",
      subscribe: true,
    });
  });

  it("稍后处理：选预设写入未来时间，已稍后处理时可取消", async () => {
    const user = userEvent.setup();
    const snoozed = triageTask(1, "导出 CSV 乱码", {
      triage: {
        entered_at: "2026-10-29T08:00:00Z",
        source: "guest",
        snoozed_until: "2099-01-01T00:00:00Z",
        creator: { id: "u2", email: "li@acme.io" },
      },
    });
    const server = renderAt(queuePage, { tasks: [snoozed] });
    await user.click(
      await screen.findByRole("button", { name: /导出 CSV 乱码/ }),
    );
    await user.click(
      (await screen.findAllByRole("button", { name: /稍后处理至/ })).at(-1)!,
    );
    await user.click(await screen.findByRole("option", { name: /一小时后/ }));
    await waitFor(() =>
      expect(server.find("PUT", "/triage-snooze")).toHaveLength(1),
    );
    const until = (
      server.find("PUT", "/triage-snooze")[0]!.body as { until: string }
    ).until;
    expect(new Date(until).getTime()).toBeGreaterThan(Date.now());
    await user.click(
      await screen.findByRole("button", { name: /导出 CSV 乱码/ }),
    );
    await user.click(
      (await screen.findAllByRole("button", { name: /稍后处理至/ })).at(-1)!,
    );
    await user.click(
      await screen.findByRole("button", { name: "取消稍后处理" }),
    );
    await waitFor(() =>
      expect(server.find("PUT", "/triage-snooze")).toHaveLength(2),
    );
    expect(server.find("PUT", "/triage-snooze")[1]!.body).toEqual({
      until: null,
    });
  });
});

describe("分拣设置", () => {
  it.each([
    [
      "开启发出 enabled:true",
      settings({ enabled: false }),
      0,
      { enabled: true, version: 2 },
      false,
    ],
    [
      "没有待分拣任务时直接关闭",
      settings(),
      0,
      { enabled: false, version: 2 },
      false,
    ],
    [
      "有待分拣任务时先确认再关闭",
      settings(),
      3,
      { enabled: false, version: 2 },
      true,
    ],
  ])("%s", async (_name, current, count, body, confirm) => {
    const user = userEvent.setup();
    const server = renderAt(settingsPage, { settings: current, count });
    await user.click(await screen.findByRole("switch", { name: "启用分拣台" }));
    if (confirm) {
      expect(
        await screen.findByText(/你有 3 个处于「待分拣」状态的任务/),
      ).toBeInTheDocument();
      expect(server.find("PATCH", "/triage-settings")).toHaveLength(0);
      await user.click(screen.getByRole("button", { name: "继续" }));
    }
    await waitFor(() =>
      expect(server.find("PATCH", "/triage-settings")).toHaveLength(1),
    );
    expect(server.find("PATCH", "/triage-settings")[0]!.body).toEqual(body);
  });

  it("缺少内置状态时提示检查工作流", async () => {
    const user = userEvent.setup();
    renderAt(settingsPage, {
      settings: settings({ enabled: false }),
      override: (call) =>
        call.path === `${sp}/triage-settings` && call.method === "PATCH"
          ? error(409, "TRIAGE_STATUS_MISSING")
          : undefined,
    });
    await user.click(await screen.findByRole("switch", { name: "启用分拣台" }));
    expect(
      await screen.findByText(/缺少内置的「待分拣」状态/),
    ).toBeInTheDocument();
  });

  it("责任动作与责任成员", async () => {
    const user = userEvent.setup();
    const server = renderAt(settingsPage, {
      settings: settings({ action: "assign" }),
    });
    await user.click(
      await screen.findByRole("switch", { name: "lin@openfox.dev" }),
    );
    await waitFor(() =>
      expect(server.find("PUT", "/triage-members")).toHaveLength(1),
    );
    expect(server.find("PUT", "/triage-members")[0]!.body).toEqual({
      user_ids: ["u1"],
    });
    await user.click(screen.getByRole("button", { name: "操作" }));
    await user.click(await screen.findByRole("option", { name: "不执行操作" }));
    await waitFor(() =>
      expect(server.find("PATCH", "/triage-settings")).toHaveLength(1),
    );
    expect(server.find("PATCH", "/triage-settings")[0]!.body).toEqual({
      action: "none",
      version: 2,
    });
  });

  it("新建规则先停用，编辑后保存且条件、或分组、候选值与动作", async () => {
    const user = userEvent.setup();
    const created = rule("new", {
      name: "新规则",
      enabled: false,
      conditions: [],
      version: 1,
    });
    const server = renderAt(settingsPage, {
      settings: settings({ rules: [created] }),
    });
    await user.click(await screen.findByRole("button", { name: "添加规则" }));
    await waitFor(() =>
      expect(server.find("POST", "/triage-rules")).toHaveLength(1),
    );
    expect(server.find("POST", "/triage-rules")[0]!.body).toMatchObject({
      enabled: false,
      conditions: [],
    });

    await user.click(screen.getByRole("button", { name: "添加或者条件组" }));
    await user.click(screen.getByRole("button", { name: "选择值" }));
    await user.click(await screen.findByRole("option", { name: "访客" }));
    await user.keyboard("{Escape}");
    await user.click(screen.getByRole("button", { name: "添加并且条件" }));
    const fields = screen.getAllByRole("button", { name: "字段" });
    await user.click(fields[1]!);
    await user.click(await screen.findByRole("option", { name: "优先级" }));
    await user.click(screen.getByRole("button", { name: "选择值" }));
    await user.click(await screen.findByRole("option", { name: /紧急/ }));
    await user.keyboard("{Escape}");
    await user.click(screen.getByRole("button", { name: "添加或者条件组" }));
    await user.click(screen.getByRole("button", { name: "保存规则" }));
    await waitFor(() =>
      expect(server.find("PUT", "/triage-rules/new")).toHaveLength(1),
    );
    expect(server.find("PUT", "/triage-rules/new")[0]!.body).toEqual({
      version: 1,
      name: "新规则",
      conditions: [
        {
          conditions: [
            { field: "source", op: "is", values: ["guest"] },
            { field: "priority", op: "is", values: ["URGENT"] },
          ],
        },
        { conditions: [{ field: "source", op: "is", values: [] }] },
      ],
      actions: [{ type: "set_priority", value: "HIGH" }],
    });
  });

  it("停用、删除、上移只带 rule_ids", async () => {
    const user = userEvent.setup();
    const server = renderAt(settingsPage, {
      settings: settings({ rules: [rule("a"), rule("b", { position: 1 })] }),
    });
    await user.click(await screen.findByRole("switch", { name: "规则 a" }));
    await waitFor(() =>
      expect(server.find("PUT", "/triage-rules/a/enabled")).toHaveLength(1),
    );
    expect(server.find("PUT", "/triage-rules/a/enabled")[0]!.body).toEqual({
      version: 3,
      enabled: false,
    });
    await user.click(screen.getAllByRole("button", { name: "上移" })[1]!);
    await waitFor(() =>
      expect(server.find("PUT", "/triage-rules/reorder")).toHaveLength(1),
    );
    expect(server.find("PUT", "/triage-rules/reorder")[0]!.body).toEqual({
      rule_ids: ["b", "a"],
    });
    await user.click(screen.getAllByRole("button", { name: "删除规则" })[0]!);
    await user.click(
      within(await screen.findByRole("dialog")).getByRole("button", {
        name: "删除规则",
      }),
    );
    await waitFor(() =>
      expect(server.find("DELETE", "/triage-rules/a")).toHaveLength(1),
    );
    expect(
      server.find("DELETE", "/triage-rules/a")[0]!.query.get("version"),
    ).toBe("3");
  });

  it.each([
    [
      "相同条件、同一动作不同的值时提示冲突",
      [
        rule("a"),
        rule("b", { actions: [{ type: "set_priority", value: "LOW" }] }),
      ],
      true,
    ],
    ["相同条件、相同动作值时不提示", [rule("a"), rule("b")], false],
  ])("%s", async (_name, rules, conflict) => {
    renderAt(settingsPage, { settings: settings({ rules }) });
    await screen.findByText("规则 a");
    expect(
      screen.queryAllByText("此规则与其他启用规则存在冲突。").length > 0,
    ).toBe(conflict);
  });

  it("没有管理权限时只读", async () => {
    renderAt(settingsPage, {
      settings: settings({ can_manage: false, rules: [rule("a")] }),
    });
    expect(await screen.findByText("你只能查看分拣设置。")).toBeInTheDocument();
    expect(screen.getByRole("switch", { name: "启用分拣台" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "添加规则" })).toBeDisabled();
  });
});

describe("新建任务与任务详情", () => {
  it("从分拣台创建时预选待分拣，可改选工作流状态", async () => {
    const user = userEvent.setup();
    renderAt(queuePage);
    await screen.findByRole("button", { name: /导出 CSV 乱码/ });
    await user.keyboard("c");
    const chip = await screen.findByRole("button", {
      name: "状态: 待分拣",
    });
    expect(chip).toHaveTextContent("待分拣");
    await user.click(chip);
    await user.click(await screen.findByRole("option", { name: "进行中" }));
    expect(chip).toHaveTextContent("进行中");
  });

  it("分拣中的任务详情显示操作条与「重复于」", async () => {
    const user = userEvent.setup();
    const detail = triageTask(1, "导出 CSV 乱码", {
      duplicate_of: { id: "task-3", display_id: "open-3", title: "原始问题" },
    });
    renderAt(`/o/${ORG}/spaces/${SPACE}/tasks?task=OPEN-1`, {
      override: (call) =>
        call.path.endsWith("/tasks/by-display-id/OPEN-1")
          ? json(200, { ...detail, description_reactions: [] })
          : undefined,
    });
    const drawer = await screen.findByRole("dialog");
    expect(
      await within(drawer).findByRole("button", { name: "接受" }),
    ).toBeInTheDocument();
    await user.click(within(drawer).getByRole("button", { name: "更多属性" }));
    const properties = await screen.findByRole("dialog", { name: "更多属性" });
    expect(within(properties).getByText("重复于")).toBeVisible();
    expect(
      within(properties).getByRole("button", { name: /OPEN-3 原始问题/ }),
    ).toBeVisible();
  });
});
