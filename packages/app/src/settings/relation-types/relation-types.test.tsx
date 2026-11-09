import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";

import type { RelationType } from "@openfox/api";

import {
  apiError,
  findCall,
  json,
  renderSettings,
  ts,
  type Route,
} from "../testing";

const builtin = (key: RelationType["key"] & string): RelationType => ({
  id: `builtin:${key}`,
  builtin: true,
  key,
  name: "",
  outward_label: "",
  inward_label: "",
  symmetric: key === "RELATES",
  version: null,
  created_at: null,
});

const types: RelationType[] = [
  builtin("BLOCKS"),
  builtin("CLONES"),
  builtin("DUPLICATES"),
  builtin("RELATES"),
  {
    id: "r1",
    builtin: false,
    key: null,
    name: "依赖",
    outward_label: "依赖于",
    inward_label: "被依赖",
    symmetric: false,
    version: 3,
    created_at: ts,
  },
];

const listRoute: Route = (call) =>
  call.method === "GET" &&
  call.path === "/v1/organizations/o1/task-relation-types"
    ? json(200, { relation_types: types })
    : undefined;

afterEach(() => localStorage.clear());

describe("任务关联类型", () => {
  it("内置类型按 key 多语言渲染并锁定，自定义类型排在后面", async () => {
    renderSettings("/o/o1/settings/relation-types", listRoute);
    const table = await screen.findByRole("table", { name: "任务关联类型" });
    const rows = within(table).getAllByRole("row").slice(1);
    expect(
      rows.map((row) => within(row).getAllByRole("cell")[0]!.textContent),
    ).toEqual(["阻塞内置", "克隆内置", "重复内置", "相关内置", "依赖"]);
    expect(rows[0]).toHaveTextContent("被阻塞");
    expect(
      within(rows[0]!).getByRole("img", { name: "内置关联类型不可编辑或删除" }),
    ).toHaveAttribute("title", "内置关联类型不可编辑或删除");
    expect(
      within(rows[4]!).getByRole("button", { name: "依赖 的操作" }),
    ).toBeInTheDocument();
  });

  it("创建：三项必填，带幂等键；失败时弹窗保留并显示错误", async () => {
    const user = userEvent.setup();
    let attempts = 0;
    const calls = renderSettings("/o/o1/settings/relation-types", (call) => {
      if (call.method === "POST") {
        attempts += 1;
        return attempts === 1
          ? apiError(409, "NAME_TAKEN")
          : json(201, { ...types[4], id: "r2", name: "实现" });
      }
      return listRoute(call);
    });
    await screen.findByText("依赖");
    await user.click(screen.getByRole("button", { name: "新建关联类型" }));
    const dialog = await screen.findByRole("dialog", { name: "新建关联类型" });
    expect(dialog).toHaveTextContent(
      "链出描述从源任务一侧读出；链入描述从被关联任务一侧读出。",
    );
    const save = within(dialog).getByRole("button", { name: "保存" });
    await user.type(within(dialog).getByLabelText(/^名称/), "实现");
    await user.type(within(dialog).getByLabelText(/^链出描述/), "实现了");
    expect(save).toBeDisabled();
    await user.type(within(dialog).getByLabelText(/^链入描述/), "由其实现");
    await user.click(save);
    expect(
      await within(dialog).findByText("名称已被使用，请换一个"),
    ).toBeInTheDocument();
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    await user.click(save);
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
    const posts = calls.filter((call) => call.method === "POST");
    expect(posts[0]!.body).toEqual({
      name: "实现",
      outward_label: "实现了",
      inward_label: "由其实现",
    });
    // 同一份输入重试时复用幂等键
    expect(posts[0]!.headers.get("Idempotency-Key")).toMatch(/^[a-f0-9]{32}$/);
    expect(posts[1]!.headers.get("Idempotency-Key")).toBe(
      posts[0]!.headers.get("Idempotency-Key"),
    );
  });

  it("删除自定义类型需要确认", async () => {
    const user = userEvent.setup();
    const calls = renderSettings("/o/o1/settings/relation-types", (call) => {
      if (call.method === "DELETE") return json(204);
      return listRoute(call);
    });
    await screen.findByText("依赖");
    await user.click(screen.getByRole("button", { name: "依赖 的操作" }));
    await user.click(screen.getByRole("menuitem", { name: "删除 依赖" }));
    const confirm = await screen.findByRole("dialog", { name: "删除关联类型" });
    expect(confirm).toHaveTextContent("确定删除“依赖”?此操作无法撤销。");
    await user.click(within(confirm).getByRole("button", { name: "删除" }));
    await waitFor(() =>
      expect(
        findCall(
          calls,
          "DELETE",
          "/v1/organizations/o1/task-relation-types/r1",
        ),
      ).toBeDefined(),
    );
  });

  it("非管理员只读：没有新建按钮和菜单", async () => {
    renderSettings("/o/o1/settings/relation-types", listRoute, "MEMBER");
    await screen.findByText("依赖");
    expect(
      screen.queryByRole("button", { name: "新建关联类型" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "依赖 的操作" }),
    ).not.toBeInTheDocument();
  });
});
