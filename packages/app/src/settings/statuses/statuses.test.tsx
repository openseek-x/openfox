import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";

import type { TaskStatus } from "@openfox/api";

import {
  apiError,
  findCall,
  json,
  renderSettings,
  ts,
  type Route,
} from "../testing";
import { applyMove, buildOrder, checkMove, resolveDrop } from "./order";

function status(
  id: string,
  name: string,
  category: TaskStatus["category"],
  extra: Partial<TaskStatus> = {},
): TaskStatus {
  return {
    id,
    name,
    description: "",
    category,
    position: 0,
    version: 1,
    created_at: ts,
    updated_at: ts,
    is_default: false,
    collapsed_by_default: false,
    task_count: 0,
    type_usage: [],
    ...extra,
  };
}

const statuses: TaskStatus[] = [
  status("s0", "分拣", "TRIAGE"),
  status("s1", "待规划", "BACKLOG"),
  status("s2", "待开始", "TODO", {
    is_default: true,
    task_count: 31,
    type_usage: [
      {
        space_id: "sp1",
        space_name: "默认空间",
        type_ids: ["t1", "t2"],
        type_names: ["需求", "缺陷"],
      },
    ],
  }),
  status("s3", "进行中", "IN_PROGRESS", { task_count: 5 }),
  status("s4", "测试中", "IN_PROGRESS", { position: 1 }),
  status("s5", "已完成", "DONE"),
  status("s6", "已取消", "CANCELLED"),
];

const listRoute: Route = (call) =>
  call.method === "GET" && call.path === "/v1/organizations/o1/task-statuses"
    ? json(200, { statuses })
    : undefined;

afterEach(() => localStorage.clear());

describe("状态顺序（纯函数）", () => {
  const order = buildOrder(statuses);

  it("按分类分组，不含 TRIAGE", () => {
    expect(order.IN_PROGRESS).toEqual(["s3", "s4"]);
    expect(order.DUPLICATE).toEqual([]);
    expect(Object.keys(order)).not.toContain("TRIAGE");
  });

  it("默认状态只能留在待规划 / 未开始", () => {
    expect(checkMove(statuses[2]!, "BACKLOG")).toBe("ok");
    expect(checkMove(statuses[2]!, "IN_PROGRESS")).toBe("default-category");
    expect(checkMove(statuses[3]!, "DONE")).toBe("ok");
  });

  it("落点：同位置不发请求；跨分类到末尾时 before 为 null", () => {
    expect(resolveDrop(order, order, "s3")).toBeNull();
    expect(
      resolveDrop(order, { ...order, IN_PROGRESS: ["s4", "s3"] }, "s3"),
    ).toEqual({ category: "IN_PROGRESS", beforeId: null });
    expect(
      resolveDrop(
        order,
        { ...order, IN_PROGRESS: ["s4"], DUPLICATE: ["s3"] },
        "s3",
      ),
    ).toEqual({ category: "DUPLICATE", beforeId: null });
    expect(
      resolveDrop(
        order,
        { ...order, IN_PROGRESS: ["s4"], DONE: ["s3", "s5"] },
        "s3",
      ),
    ).toEqual({ category: "DONE", beforeId: "s5" });
  });

  it("乐观更新把状态插到目标之前或分类末尾", () => {
    const moved = applyMove(statuses, "s4", "IN_PROGRESS", "s3");
    expect(buildOrder(moved).IN_PROGRESS).toEqual(["s4", "s3"]);
    const toDone = applyMove(statuses, "s3", "DONE", null);
    expect(buildOrder(toDone).DONE).toEqual(["s5", "s3"]);
    expect(toDone.find((s) => s.id === "s3")!.category).toBe("DONE");
  });
});

describe("任务状态页", () => {
  it("6 张分类卡按固定顺序，默认标记、任务数与类型数，空分类提示", async () => {
    renderSettings("/o/o1/settings/statuses", listRoute);
    await screen.findByText("测试中");
    const regions = screen.getAllByRole("region");
    expect(regions.map((r) => r.getAttribute("aria-labelledby"))).toHaveLength(
      6,
    );
    expect(
      regions.map((r) => within(r).getByRole("heading").textContent),
    ).toEqual(["待规划", "未开始", "进行中", "已完成", "已取消", "重复"]);
    expect(screen.queryByText("分拣")).not.toBeInTheDocument();
    const todo = screen.getByRole("list", { name: "未开始" });
    expect(todo).toHaveTextContent("待开始· 默认");
    expect(todo).toHaveTextContent("31 个任务");
    expect(todo).toHaveTextContent("2 个任务类型");
    expect(screen.getByRole("list", { name: "重复" })).toHaveTextContent(
      "此分类中暂无状态——拖动一个状态到这里即可归入。",
    );
  });

  it("移除禁用时写明原因；有任务时弹出重新分配对话框", async () => {
    const user = userEvent.setup();
    const calls = renderSettings("/o/o1/settings/statuses", (call) => {
      if (call.method === "DELETE" && call.path.endsWith("/task-statuses/s3"))
        return json(204);
      return listRoute(call);
    });
    await screen.findByText("测试中");
    await user.click(screen.getByRole("button", { name: "待开始 的操作" }));
    expect(screen.getByRole("menuitem", { name: "移除" })).toHaveAttribute(
      "title",
      "默认状态不能移除",
    );
    await user.keyboard("{Escape}");
    await user.click(screen.getByRole("button", { name: "已完成 的操作" }));
    expect(screen.getByRole("menuitem", { name: "移除" })).toHaveAttribute(
      "title",
      "分类内唯一的状态不能移除",
    );
    await user.keyboard("{Escape}");

    await user.click(screen.getByRole("button", { name: "进行中 的操作" }));
    await user.click(screen.getByRole("menuitem", { name: "移除" }));
    const dialog = await screen.findByRole("dialog", {
      name: "删除“进行中”？",
    });
    const confirm = within(dialog).getByRole("button", {
      name: "删除并重新分配",
    });
    expect(confirm).toBeDisabled();
    await user.click(
      within(dialog).getByRole("button", { name: "将任务移动到" }),
    );
    // 排除自身，允许跨分类
    expect(
      screen.queryByRole("option", { name: /^进行中/ }),
    ).not.toBeInTheDocument();
    expect(screen.getByRole("option", { name: /^已完成/ })).toBeInTheDocument();
    await user.click(screen.getByRole("option", { name: /测试中/ }));
    await user.click(confirm);
    await waitFor(() => {
      const call = findCall(
        calls,
        "DELETE",
        "/v1/organizations/o1/task-statuses/s3",
      );
      expect(call?.query.get("replacement_status_id")).toBe("s4");
    });
  });

  it("创建状态：带幂等键，颜色无效时禁用保存", async () => {
    const user = userEvent.setup();
    const calls = renderSettings("/o/o1/settings/statuses", (call) => {
      if (call.method === "POST" && call.path.endsWith("/task-statuses"))
        return json(201, status("s9", "开发中", "IN_PROGRESS"));
      return listRoute(call);
    });
    await screen.findByText("测试中");
    await user.click(
      screen.getByRole("button", { name: "向「进行中」添加状态" }),
    );
    const dialog = await screen.findByRole("dialog", { name: "添加状态" });
    await user.type(within(dialog).getByLabelText("名称"), "开发中");
    const color = within(dialog).getByLabelText("颜色");
    await user.clear(color);
    await user.type(color, "#zzz");
    const save = within(dialog).getByRole("button", { name: "保存" });
    expect(save).toBeDisabled();
    await user.clear(color);
    await user.type(color, "#123456");
    await user.click(save);
    await waitFor(() =>
      expect(
        findCall(calls, "POST", "/v1/organizations/o1/task-statuses"),
      ).toBeDefined(),
    );
    const post = findCall(calls, "POST", "/v1/organizations/o1/task-statuses")!;
    expect(post.body).toEqual({
      name: "开发中",
      category: "IN_PROGRESS",
      color: "#123456",
    });
    expect(post.headers.get("Idempotency-Key")).toMatch(/^[a-f0-9]{32}$/);
  });

  it("默认状态改到进行中：前端拒绝并提示，不发请求", async () => {
    const user = userEvent.setup();
    const calls = renderSettings("/o/o1/settings/statuses", listRoute);
    await screen.findByText("测试中");
    await user.click(screen.getByRole("button", { name: "待开始 的操作" }));
    await user.click(screen.getByRole("menuitem", { name: "编辑" }));
    const dialog = await screen.findByRole("dialog", { name: "编辑状态" });
    await user.click(within(dialog).getByRole("button", { name: "状态分组" }));
    await user.click(screen.getByRole("option", { name: /进行中/ }));
    await user.click(within(dialog).getByRole("button", { name: "保存" }));
    expect(
      await screen.findByText("默认状态只能位于「待规划」或「未开始」分组。"),
    ).toBeInTheDocument();
    expect(calls.filter((c) => c.method !== "GET")).toHaveLength(0);
  });

  it("设为默认失败（服务端拒绝）时回滚并提示", async () => {
    const user = userEvent.setup();
    renderSettings("/o/o1/settings/statuses", (call) => {
      if (call.method === "POST" && call.path.endsWith("/s1/default"))
        return apiError(409, "STATUS_DEFAULT_CATEGORY");
      return listRoute(call);
    });
    await screen.findByText("测试中");
    await user.click(screen.getByRole("button", { name: "待规划 的操作" }));
    await user.click(screen.getByRole("menuitem", { name: "设为默认" }));
    expect(
      await screen.findByText("默认状态只能位于「待规划」或「未开始」分组。"),
    ).toBeInTheDocument();
    await waitFor(() =>
      expect(screen.getByRole("list", { name: "未开始" })).toHaveTextContent(
        "· 默认",
      ),
    );
    expect(screen.getByRole("list", { name: "待规划" })).not.toHaveTextContent(
      "· 默认",
    );
  });

  it("只读：没有拖拽手柄和菜单，添加状态禁用", async () => {
    renderSettings("/o/o1/settings/statuses", listRoute, "MEMBER");
    await screen.findByText("测试中");
    expect(
      screen.queryByRole("button", { name: /^拖动/ }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /的操作$/ }),
    ).not.toBeInTheDocument();
    for (const button of screen.getAllByRole("button", { name: /添加状态/ }))
      expect(button).toBeDisabled();
  });
});
