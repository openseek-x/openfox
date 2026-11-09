import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";

import type { CustomField } from "@openfox/api";

import { findCall, json, renderSettings, ts, type Route } from "../testing";
import { buildDefault, draftFromValue } from "./kinds";

function field(
  id: string,
  name: string,
  kind: CustomField["kind"],
  extra: Partial<CustomField> = {},
): CustomField {
  return {
    id,
    name,
    kind,
    description: "",
    default_value: null,
    member_scope: null,
    options: [],
    used_by_type_count: 0,
    created_by: null,
    archived_at: null,
    version: 1,
    created_at: ts,
    updated_at: ts,
    ...extra,
  };
}

const severity = field("f1", "严重程度", "SELECT", {
  description: "线上问题的影响级别",
  used_by_type_count: 3,
  default_value: "o2",
  created_by: { user_id: "u1", email: "lin@openfox.dev" },
  options: [
    {
      id: "o1",
      label: "致命",
      color: "#ff3881",
      position: 0,
      archived_at: null,
      version: 1,
    },
    {
      id: "o2",
      label: "一般",
      color: "#ffd600",
      position: 1,
      archived_at: null,
      version: 1,
    },
  ],
});
const points = field("f2", "估算点数", "NUMBER");

const listRoute: Route = (call) => {
  if (call.method !== "GET") return undefined;
  if (call.path === "/v1/organizations/o1/custom-fields")
    return json(200, { custom_fields: [severity, points] });
  if (call.path === "/v1/organizations/o1/task-types")
    return json(200, {
      spaces: [
        {
          space_id: "sp1",
          space_name: "研发",
          space_prefix: "DEV",
          task_types: [
            { id: "t1", name: "缺陷", icon: "bug", field_ids: [] },
            { id: "t2", name: "需求", icon: null, field_ids: [] },
          ],
        },
        {
          space_id: "sp2",
          space_name: "市场",
          space_prefix: "MKT",
          task_types: [{ id: "t3", name: "活动", icon: null, field_ids: [] }],
        },
      ],
    });
  return undefined;
};

afterEach(() => localStorage.clear());

describe("默认值转换（纯函数）", () => {
  const rows = [
    { key: "a", label: "", color: "#000000" },
    { key: "b", label: "高", color: "#000000" },
    { key: "c", label: "低", color: "#000000" },
  ];
  it("创建时单选 / 多选用本次有名称选项的下标", () => {
    expect(
      buildDefault("SELECT", { scalar: "", keys: ["c"] }, rows, "create"),
    ).toEqual({ ok: true, value: { option_index: 1 } });
    expect(
      buildDefault(
        "MULTI_SELECT",
        { scalar: "", keys: ["b", "c"] },
        rows,
        "create",
      ),
    ).toEqual({ ok: true, value: { option_indexes: [0, 1] } });
  });
  it("数字、URL 校验与成员值", () => {
    expect(
      buildDefault("NUMBER", { scalar: "abc", keys: [] }, [], "create").ok,
    ).toBe(false);
    expect(
      buildDefault("NUMBER", { scalar: "3.5", keys: [] }, [], "create"),
    ).toEqual({ ok: true, value: 3.5 });
    expect(
      buildDefault("URL", { scalar: "ftp://x", keys: [] }, [], "create").ok,
    ).toBe(false);
    expect(buildDefault("URL", { scalar: "", keys: [] }, [], "create")).toEqual(
      { ok: true, value: null },
    );
    expect(
      buildDefault("MEMBER", { scalar: "AGENT:a1", keys: [] }, [], "create"),
    ).toEqual({
      ok: true,
      value: { kind: "AGENT", id: "a1" },
    });
    expect(draftFromValue("BOOLEAN", false)).toEqual({
      scalar: "false",
      keys: [],
    });
  });
});

describe("自定义字段页", () => {
  it("列表与展开详情：使用数、类型徽标、默认值、选项、创建者", async () => {
    const user = userEvent.setup();
    renderSettings("/o/o1/settings/custom-fields", listRoute);
    const list = await screen.findByRole("list", { name: "自定义字段" });
    const items = within(list).getAllByRole("listitem");
    expect(items[0]).toHaveTextContent("严重程度/ 被 3 个类型使用单选");
    expect(items[1]).toHaveTextContent("估算点数数字");
    expect(items[1]).not.toHaveTextContent("被 0 个");
    await user.click(screen.getByRole("button", { name: "展开 严重程度" }));
    await user.click(screen.getByRole("button", { name: "展开 估算点数" }));
    // 可同时展开多张
    expect(items[0]).toHaveTextContent("默认值一般");
    expect(items[0]).toHaveTextContent("选项致命一般");
    expect(items[0]).toHaveTextContent(/创建者.?lin@openfox\.dev/);
    expect(items[1]).toHaveTextContent("描述无描述");
    expect(items[1]).toHaveTextContent("创建者系统");
    expect(screen.getByRole("link", { name: "前往任务类型" })).toHaveAttribute(
      "href",
      "/o/o1/settings/spaces",
    );
  });

  it("已绑定类型的字段不弹确认，直接提示无法删除", async () => {
    const user = userEvent.setup();
    const calls = renderSettings("/o/o1/settings/custom-fields", listRoute);
    await screen.findByText("严重程度");
    await user.click(screen.getByRole("button", { name: "严重程度 的操作" }));
    await user.click(screen.getByRole("menuitem", { name: "删除" }));
    expect(
      await screen.findByText("无法删除已绑定到任务类型的字段"),
    ).toBeInTheDocument();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(calls.some((call) => call.method === "DELETE")).toBe(false);
  });

  it("创建单选字段：选项全空时禁用保存；提交带幂等键与 option_index；之后添加到任务类型", async () => {
    const user = userEvent.setup();
    const created = field("f9", "优先级", "SELECT", {
      options: [
        {
          id: "x1",
          label: "高",
          color: "#00dd44",
          position: 0,
          archived_at: null,
          version: 1,
        },
        {
          id: "x2",
          label: "低",
          color: "#8066f0",
          position: 1,
          archived_at: null,
          version: 1,
        },
      ],
    });
    const calls = renderSettings("/o/o1/settings/custom-fields", (call) => {
      if (call.method === "POST" && call.path.endsWith("/custom-fields"))
        return json(201, created);
      if (call.method === "PUT") return json(204);
      return listRoute(call);
    });
    await screen.findByText("严重程度");
    await user.click(screen.getByRole("button", { name: "添加自定义字段" }));
    const dialog = await screen.findByRole("dialog", {
      name: "创建自定义字段",
    });
    await user.type(within(dialog).getByLabelText("名称"), "优先级");
    await user.click(within(dialog).getByRole("button", { name: "类型" }));
    await user.click(screen.getByRole("option", { name: /单选/ }));
    const save = within(dialog).getByRole("button", { name: "保存" });
    // 初始两行空选项
    expect(within(dialog).getAllByPlaceholderText("选项名称")).toHaveLength(2);
    expect(save).toBeDisabled();
    await user.type(within(dialog).getByLabelText("选项 1 名称"), "高");
    await user.type(within(dialog).getByLabelText("选项 2 名称"), "低");
    await user.click(
      within(dialog).getByRole("button", { name: "默认值（可选）" }),
    );
    await user.click(screen.getByRole("option", { name: /^低/ }));
    await user.click(save);

    const add = await screen.findByRole("dialog", {
      name: "将“优先级”添加到任务类型",
    });
    const post = findCall(calls, "POST", "/v1/organizations/o1/custom-fields")!;
    expect(post.body).toEqual({
      name: "优先级",
      kind: "SELECT",
      default_value: { option_index: 1 },
      options: [
        { label: "高", color: "#00dd44" },
        { label: "低", color: "#8066f0" },
      ],
    });
    expect(post.headers.get("Idempotency-Key")).toMatch(/^[a-f0-9]{32}$/);

    // 空间复选框：全选该空间的类型；搜索同时匹配空间名和类型名
    const dev = within(add).getByRole("group", { name: "研发" });
    await user.click(within(dev).getAllByRole("checkbox")[0]!);
    expect(within(add).getByText("已选择 2 个")).toBeInTheDocument();
    await user.click(within(dev).getByRole("checkbox", { name: /需求/ }));
    expect(within(dev).getAllByRole("checkbox")[0]).toHaveProperty(
      "indeterminate",
      true,
    );
    await user.type(within(add).getByLabelText("搜索空间或任务类型"), "活动");
    expect(within(add).queryByRole("group", { name: "研发" })).toBeNull();
    expect(
      within(add).getByRole("group", { name: "市场" }),
    ).toBeInTheDocument();
    await user.click(within(add).getByRole("button", { name: "添加 1 个" }));
    expect(await screen.findByText("已添加到 研发 / 缺陷")).toBeInTheDocument();
    await waitFor(() =>
      expect(
        findCall(
          calls,
          "PUT",
          "/v1/organizations/o1/spaces/sp1/task-types/t1/fields/f9",
        ),
      ).toBeDefined(),
    );
  });

  it("只读成员：没有添加按钮和菜单", async () => {
    renderSettings("/o/o1/settings/custom-fields", listRoute, "MEMBER");
    await screen.findByText("严重程度");
    expect(
      screen.queryByRole("button", { name: "添加自定义字段" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "严重程度 的操作" }),
    ).not.toBeInTheDocument();
  });
});
