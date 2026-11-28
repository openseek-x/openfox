import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, useLocation } from "react-router-dom";
import { afterEach, describe, expect, it } from "vitest";

import { App, createQueryClient } from "../../App";
import { defaultDisplay, saveDisplay } from "../model";
import { backend, json, makeTask, ORG, SPACE } from "../test-backend";

afterEach(() => localStorage.clear());

function LocationProbe() {
  const loc = useLocation();
  return <output aria-label="路由查询">{loc.search}</output>;
}

function mountAt(path: string, tasks = [makeTask(1, "st1")]) {
  const server = backend((call) => {
    const base = `/v1/organizations/${ORG}`;
    if (call.path === `${base}/spaces/${SPACE}/tasks/counts`)
      return json(200, {
        total: tasks.length,
        by_status: [{ status_id: "st1", count: tasks.length }],
      });
    if (call.path === `${base}/spaces/${SPACE}/tasks` && call.method === "GET")
      return json(200, { tasks, next_cursor: null });
    if (call.path === `${base}/my-tasks/counts`)
      return json(200, { total: 0, by_status: [] });
    if (call.path === `${base}/my-tasks` && call.method === "GET")
      return json(200, { tasks: [], next_cursor: null });
    if (call.path === `${base}/tasks/OPENFOX-1234` && call.method === "GET") {
      return json(200, {
        ...tasks[0],
        reactions: [],
        subtasks: [],
        attachments: [],
        linked_conversations: [],
      });
    }
    return undefined;
  });
  const queryClient = createQueryClient();
  queryClient.setDefaultOptions({ queries: { retry: false } });
  localStorage.setItem(`openfox:onboarding:${ORG}`, "{}");
  return render(
    <MemoryRouter initialEntries={[path]}>
      <LocationProbe />
      <App api={server.api} queryClient={queryClient} locale="zh-CN" />
    </MemoryRouter>,
  );
}

describe("OPE-24 任务列表排版与我的任务页头", () => {
  it("列表页显示日期列头、无优先级对齐占位、长 ID title 与可透传点击的日期提示", async () => {
    saveDisplay(ORG, `space:${SPACE}`, {
      ...defaultDisplay("space"),
      layout: "list",
      properties: ["priority", "id", "start", "due", "created", "updated"],
    });
    const tasks = [
      makeTask(1234, "st1", {
        display_id: "OPENFOX-1234",
        title: "无优先级且带日期的任务",
        priority: "NONE",
        start_date: "2026-11-20",
        due_date: "2026-12-01",
        created_at: "2026-11-28T00:00:00Z",
        updated_at: "2026-11-28T08:00:00Z",
      }),
      makeTask(1235, "st1", {
        display_id: "OPENFOX-1235",
        title: "高优先级任务",
        priority: "HIGH",
      }),
    ];
    mountAt(`/o/${ORG}/spaces/${SPACE}/tasks`, tasks);
    const listRegion = await screen.findByRole("region", { name: "任务列表" });
    expect(
      await within(listRegion).findByText("无优先级且带日期的任务"),
    ).toBeVisible();

    // 顶部日期列头
    expect(within(listRegion).getByText("开始日期")).toBeInTheDocument();
    expect(within(listRegion).getByText("截止日期")).toBeInTheDocument();
    expect(within(listRegion).getByText("创建日期")).toBeInTheDocument();
    expect(within(listRegion).getByText("更新时间")).toBeInTheDocument();

    // 无优先级行仍占固定图标位，与高优先级行对齐
    expect(
      within(listRegion).getByRole("img", { name: "无优先级" }),
    ).toBeInTheDocument();
    expect(
      within(listRegion).getByRole("img", { name: "高" }),
    ).toBeInTheDocument();

    // 长前缀 ID 提供完整 title 悬停
    const idSpan = within(listRegion).getByText("OPENFOX-1234");
    expect(idSpan).toHaveAttribute("title", "OPENFOX-1234");

    // 日期芯片带明确字段语义 title/aria-label，且点击透传打开任务详情
    const dueChip = within(listRegion).getByLabelText(/^截止日期：/);
    expect(dueChip).toHaveAttribute(
      "title",
      expect.stringMatching(/^截止日期：/),
    );
    expect(
      within(listRegion).getAllByLabelText(/^创建日期：/)[0],
    ).toHaveAttribute("title", expect.stringMatching(/^创建日期：/));

    await userEvent.setup().click(dueChip);
    expect(screen.getByRole("status", { name: "路由查询" })).toHaveTextContent(
      "task=OPENFOX-1234",
    );
  });

  it("我的任务页显示统一可见页头与新建任务按钮", async () => {
    mountAt(`/o/${ORG}/my-tasks`, []);
    expect(
      await screen.findByRole("heading", { level: 1, name: "我的任务" }),
    ).toBeVisible();
    const header = screen
      .getByRole("heading", { level: 1, name: "我的任务" })
      .closest("header")!;
    expect(
      within(header).getByRole("button", { name: "新建任务" }),
    ).toBeVisible();
  });
});
