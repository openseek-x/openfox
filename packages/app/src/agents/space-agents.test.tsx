import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";

import {
  apiError,
  json,
  renderSettings,
  type Route,
} from "../settings/testing";
import { device, entry, TS } from "./fixtures";

// 空间 Agents 页（复测 D21）：只列当前空间、四个分类与搜索、新建默认当前空间、
// 已归档只列当前空间、无权访问的空间显示「页面不存在」、侧栏高亮；全局页不受影响。

const ORG = "/v1/organizations/o1";
const space = (id: string, name: string, prefix: string) => ({
  id,
  name,
  prefix,
  icon: null,
  visibility: "shared",
  status: "active",
  my_role: "MEMBER",
  can_manage: false,
  features: {},
  created_at: TS,
});
const spaces = [space("s1", "默认空间", "DEF"), space("s2", "OpenFox", "OPF")];

const directory = {
  entries: [
    entry("a1", { name: "熊孩子" }),
    entry("a2", { name: "架构师", space_id: "s2", space_name: "OpenFox" }),
    entry("a3", {
      name: "测试员",
      space_id: "s2",
      space_name: "OpenFox",
      mine: false,
      owner_email: "fox@openfox.dev",
    }),
    entry("c1", {
      type: "crew",
      name: "前端小分队",
      space_id: "s2",
      space_name: "OpenFox",
      member_count: 2,
    }),
    entry("c2", { type: "crew", name: "默认小队", member_count: 1 }),
  ],
  archived_count: 3,
  truncated: false,
};

/** 空间列表、目录（含已归档）、电脑；s9 是无权访问 / 不存在的空间。 */
const routes: Route = (call) => {
  if (call.method !== "GET") return undefined;
  if (call.path === `${ORG}/spaces`) return json(200, { spaces });
  if (call.path === `${ORG}/spaces/s9`) return apiError(404, "NOT_FOUND");
  if (call.path === `${ORG}/agent-directory`)
    return json(
      200,
      call.query.get("archived") === "true" ? archived : directory,
    );
  if (call.path === `${ORG}/devices`)
    return json(200, { devices: [device("d1")], next_cursor: null });
  if (call.path === `${ORG}/members`)
    return json(200, { members: [], next_cursor: "" });
  return undefined;
};

const page = "/o/o1/spaces/s2/agents";

afterEach(() => localStorage.clear());

const archived = {
  entries: [
    entry("x1", { name: "旧 Agent 甲", archived_at: TS }),
    entry("x2", {
      name: "旧 Agent 乙",
      space_id: "s2",
      space_name: "OpenFox",
      archived_at: TS,
    }),
    entry("x3", { name: "旧 Agent 丙", archived_at: TS }),
  ],
  archived_count: 3,
  truncated: false,
};

describe("空间 Agents 页", () => {
  it("只列当前空间；页头是空间面包屑；侧栏「Agents」高亮", async () => {
    renderSettings(page, routes);
    expect(await screen.findByText("架构师")).toBeInTheDocument();
    expect(screen.getByText("测试员")).toBeInTheDocument();
    expect(screen.getByText("前端小分队")).toBeInTheDocument();
    expect(screen.queryByText("熊孩子")).not.toBeInTheDocument();
    expect(screen.queryByText("默认小队")).not.toBeInTheDocument();
    const crumb = screen.getByRole("navigation", { name: "面包屑" });
    expect(within(crumb).getByText("OpenFox")).toBeInTheDocument();
    expect(
      within(crumb).getByRole("heading", { name: "Agents" }),
    ).toBeInTheDocument();
    // 不再是「即将开放」占位
    expect(screen.queryByText("该功能即将开放")).not.toBeInTheDocument();
    const nav = screen
      .getAllByRole("link", { name: /Agents/ })
      .find((link) => link.getAttribute("href") === page);
    expect(nav).toHaveAttribute("aria-current", "page");
    // 已归档数只数本空间
    expect(await screen.findByText("显示已归档（1）→")).toBeInTheDocument();
  });

  it("四个分类与搜索只在当前空间里生效", async () => {
    const user = userEvent.setup();
    renderSettings(page, routes);
    await screen.findByText("架构师");
    await user.click(screen.getByRole("radio", { name: "小队" }));
    expect(screen.getByText("前端小分队")).toBeInTheDocument();
    expect(screen.queryByText("架构师")).not.toBeInTheDocument();
    expect(screen.queryByText("默认小队")).not.toBeInTheDocument();
    await user.click(screen.getByRole("radio", { name: "我的" }));
    expect(screen.getByText("架构师")).toBeInTheDocument();
    expect(screen.queryByText("测试员")).not.toBeInTheDocument();
    await user.click(screen.getByRole("radio", { name: "Agent" }));
    expect(screen.getByText("测试员")).toBeInTheDocument();
    expect(screen.queryByText("前端小分队")).not.toBeInTheDocument();
    await user.click(screen.getByRole("radio", { name: "全部" }));
    await user.type(
      screen.getByRole("searchbox", { name: "搜索 Agent 或小队" }),
      "熊孩子",
    );
    expect(
      screen.getByText("当前筛选下没有匹配的 Agent 或小队。"),
    ).toBeInTheDocument();
  });
  it("新建 Agent / 小队默认所属空间为当前空间", async () => {
    const user = userEvent.setup();
    renderSettings(page, routes);
    await screen.findByText("架构师");
    await user.click(screen.getByRole("button", { name: "创建 Agent" }));
    const agentDialog = await screen.findByRole("dialog", {
      name: "创建 Agent",
    });
    expect(within(agentDialog).getByText("OpenFox")).toBeInTheDocument();
    expect(within(agentDialog).queryByText("默认空间")).toBeNull();
    await user.click(within(agentDialog).getByRole("button", { name: "取消" }));
    await user.click(screen.getByRole("button", { name: "创建小队" }));
    const crewDialog = await screen.findByRole("dialog", { name: "创建小队" });
    expect(within(crewDialog).getByText("OpenFox")).toBeInTheDocument();
    expect(within(crewDialog).queryByText("默认空间")).toBeNull();
  });

  it("已归档弹窗只列当前空间", async () => {
    const user = userEvent.setup();
    renderSettings(page, routes);
    await user.click(await screen.findByText("显示已归档（1）→"));
    const dialog = await screen.findByRole("dialog", { name: "已归档 Agent" });
    expect(await within(dialog).findByText("旧 Agent 乙")).toBeInTheDocument();
    expect(within(dialog).queryByText("旧 Agent 甲")).toBeNull();
    expect(within(dialog).queryByText("旧 Agent 丙")).toBeNull();
  });

  it("没有权限访问的空间与其他空间页一样显示「页面不存在」", async () => {
    renderSettings("/o/o1/spaces/s9/agents", routes);
    expect(await screen.findByText("页面不存在")).toBeInTheDocument();
  });

  it("全局 Agent 页不受影响：列出全部空间，已归档弹窗列全部", async () => {
    const user = userEvent.setup();
    renderSettings("/o/o1/agents", routes);
    expect(await screen.findByText("熊孩子")).toBeInTheDocument();
    expect(screen.getByText("架构师")).toBeInTheDocument();
    expect(
      screen.getByRole("heading", { name: "全部 Agent 和小队" }),
    ).toBeInTheDocument();
    await user.click(screen.getByText("显示已归档（3）→"));
    const dialog = await screen.findByRole("dialog", { name: "已归档 Agent" });
    expect(await within(dialog).findByText("旧 Agent 甲")).toBeInTheDocument();
    expect(within(dialog).getByText("旧 Agent 乙")).toBeInTheDocument();
    await waitFor(() =>
      expect(within(dialog).getByText("旧 Agent 丙")).toBeInTheDocument(),
    );
  });
});
