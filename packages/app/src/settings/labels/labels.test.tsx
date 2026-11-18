import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";

import type { Label, LabelGroup } from "@openfox/api";

import { findCall, json, renderSettings, ts, type Route } from "../testing";

const group: LabelGroup = {
  id: "g1",
  name: "严重程度",
  description: "按影响范围分级",
  color: "#DC2626",
  label_count: 2,
  version: 1,
  created_at: ts,
  updated_at: ts,
};

function label(id: string, name: string, extra: Partial<Label> = {}): Label {
  return {
    id,
    space_id: null,
    name,
    description: "",
    color: "#16A34A",
    group_id: null,
    archived_at: null,
    task_count: 0,
    last_applied_at: null,
    version: 1,
    created_at: ts,
    updated_at: ts,
    ...extra,
  };
}

const labels = [
  label("l1", "P0 致命", {
    group_id: "g1",
    task_count: 4,
    description: "线上不可用",
  }),
  label("l2", "P1 严重", { group_id: "g1" }),
  label("l3", "Bug", { description: "缺陷与回归问题", task_count: 12 }),
];

const listRoutes: Route = (call) => {
  if (call.method !== "GET") return undefined;
  if (call.path === "/v1/organizations/o1/label-groups")
    return json(200, { label_groups: [group] });
  if (call.path === "/v1/organizations/o1/labels")
    return json(200, {
      labels: call.query.get("scope") === "archived" ? [] : labels,
    });
  return undefined;
};

afterEach(() => localStorage.clear());

describe("任务标签", () => {
  it("分组在前、子标签缩进、未分组在后；任务为 0 留空，分组统计列显示 -", async () => {
    renderSettings("/o/o1/settings/labels", listRoutes);
    const table = await screen.findByRole("table", { name: "任务标签" });
    const rows = within(table).getAllByRole("row").slice(1);
    expect(rows.map((row) => row.textContent)).toEqual([
      expect.stringContaining("严重程度"),
      expect.stringContaining("P0 致命"),
      expect.stringContaining("P1 严重"),
      expect.stringContaining("Bug"),
    ]);
    expect(rows[0]).toHaveTextContent("---");
    const cells = (row: HTMLElement) => within(row).getAllByRole("cell");
    // 列：选择 / 名称 / 描述 / 任务 / 上次应用 / 创建时间 / 操作
    expect(cells(rows[1]!)[3]).toHaveTextContent("4");
    expect(cells(rows[2]!)[3]).toHaveTextContent(/^$/);
    expect(cells(rows[3]!)[3]).toHaveTextContent("12");
  });

  it("搜索匹配名称和描述，没有命中子标签的分组隐藏", async () => {
    const user = userEvent.setup();
    renderSettings("/o/o1/settings/labels", listRoutes);
    await screen.findByText("P0 致命");
    await user.type(screen.getByLabelText("按名称筛选..."), "回归");
    expect(screen.getByText("Bug")).toBeInTheDocument();
    expect(screen.queryByText("严重程度")).not.toBeInTheDocument();
    await user.clear(screen.getByLabelText("按名称筛选..."));
    await user.type(screen.getByLabelText("按名称筛选..."), "zzz");
    expect(screen.getByText("未找到匹配项")).toBeInTheDocument();
  });

  it("创建标签：随机初始色，带幂等键提交，新标签置顶", async () => {
    const user = userEvent.setup();
    const calls = renderSettings("/o/o1/settings/labels", (call) => {
      if (call.method === "POST" && call.path === "/v1/organizations/o1/labels")
        return json(201, label("l9", "需求", { color: "#2563EB" }));
      return listRoutes(call);
    });
    await screen.findByText("Bug");
    await user.click(screen.getByRole("button", { name: "新建标签" }));
    const dialog = await screen.findByRole("dialog", { name: "创建标签" });
    const name = within(dialog).getByLabelText("名称");
    expect(name).toHaveFocus();
    await user.type(name, "  需求 ");
    await user.click(within(dialog).getByRole("button", { name: "创建" }));
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
    const post = findCall(calls, "POST", "/v1/organizations/o1/labels")!;
    expect(post.body).toEqual({
      name: "需求",
      color: expect.stringMatching(/^#[0-9A-F]{6}$/),
    });
    expect(post.headers.get("Idempotency-Key")).toMatch(/^[a-f0-9]{32}$/);
    expect(await screen.findByText("已创建标签“需求”")).toBeInTheDocument();
  });

  it("删除分组不二次确认，toast 撤销时用 label_ids 重建分组", async () => {
    const user = userEvent.setup();
    const calls = renderSettings("/o/o1/settings/labels", (call) => {
      if (call.method === "DELETE" && call.path.endsWith("/label-groups/g1"))
        return json(200, { removed_label_ids: ["l1", "l2"] });
      if (call.method === "POST" && call.path.endsWith("/label-groups"))
        return json(201, { ...group, id: "g2" });
      return listRoutes(call);
    });
    await screen.findByText("严重程度");
    await user.click(
      screen.getByRole("button", { name: "分组 严重程度 的操作" }),
    );
    await user.click(screen.getByRole("menuitem", { name: "删除分组" }));
    await user.click(await screen.findByRole("button", { name: "撤销" }));
    await waitFor(() =>
      expect(
        findCall(calls, "POST", "/v1/organizations/o1/label-groups"),
      ).toBeDefined(),
    );
    const post = findCall(calls, "POST", "/v1/organizations/o1/label-groups")!;
    expect(post.body).toEqual({
      name: "严重程度",
      description: "按影响范围分级",
      color: "#DC2626",
      label_ids: ["l1", "l2"],
    });
  });

  it("删除已使用的标签：提示受影响任务数，并提供归档作为替代", async () => {
    const user = userEvent.setup();
    const calls = renderSettings("/o/o1/settings/labels", (call) => {
      if (call.method === "PATCH" && call.path.endsWith("/labels/l3"))
        return json(200, { ...labels[2], archived_at: ts, version: 2 });
      return listRoutes(call);
    });
    await screen.findByText("Bug");
    await user.click(screen.getByRole("button", { name: "Bug 的操作" }));
    await user.click(screen.getByRole("menuitem", { name: "删除" }));
    const dialog = await screen.findByRole("dialog", { name: "删除“Bug”？" });
    expect(dialog).toHaveTextContent("此标签将从 12 个任务中移除");
    await user.click(within(dialog).getByRole("button", { name: "归档" }));
    await waitFor(() =>
      expect(
        findCall(calls, "PATCH", "/v1/organizations/o1/labels/l3")?.body,
      ).toEqual({ archived: true, version: 1 }),
    );
    expect(findCall(calls, "DELETE", "/v1/organizations/o1/labels/l3")).toBe(
      undefined,
    );
  });

  it("只读成员：没有复选框和编辑入口，菜单只保留查看已标记任务", async () => {
    const user = userEvent.setup();
    renderSettings("/o/o1/settings/labels", listRoutes, "MEMBER");
    await screen.findByText("Bug");
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "新建标签" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "编辑 Bug 的名称" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "分组 严重程度 的操作" }),
    ).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Bug 的操作" }));
    const menu = screen.getByRole("menu");
    expect(
      within(menu)
        .getAllByRole("menuitem")
        .map((i) => i.textContent),
    ).toEqual(["查看已标记任务"]);
  });

  it("行内编辑：Enter 提交，Esc 取消，未改动不提交", async () => {
    const user = userEvent.setup();
    const calls = renderSettings("/o/o1/settings/labels", (call) => {
      if (call.method === "PATCH" && call.path.endsWith("/labels/l3"))
        return json(200, { ...labels[2], name: "缺陷", version: 2 });
      return listRoutes(call);
    });
    await screen.findByText("Bug");
    await user.click(screen.getByRole("button", { name: "编辑 Bug 的名称" }));
    await user.keyboard("{Escape}");
    await user.click(screen.getByRole("button", { name: "编辑 Bug 的名称" }));
    await user.keyboard("{Enter}");
    expect(calls.filter((c) => c.method === "PATCH")).toHaveLength(0);
    await user.click(screen.getByRole("button", { name: "编辑 Bug 的名称" }));
    await user.keyboard("缺陷{Enter}");
    await waitFor(() =>
      expect(
        findCall(calls, "PATCH", "/v1/organizations/o1/labels/l3")?.body,
      ).toEqual({ name: "缺陷", version: 1 }),
    );
  });
});
