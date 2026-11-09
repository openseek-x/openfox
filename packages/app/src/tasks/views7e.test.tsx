import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
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
  type Override,
} from "./test-backend";

// 收藏、统计视图、甘特图。

function renderAt(path: string, override?: Override) {
  const server = backend(override);
  const queryClient = createQueryClient();
  queryClient.setDefaultOptions({ queries: { retry: false } });
  render(
    <MemoryRouter initialEntries={[path]}>
      <App api={server.api} queryClient={queryClient} locale="zh-CN" />
    </MemoryRouter>,
  );
  return server;
}

const spaceTasks = `/o/${ORG}/spaces/${SPACE}/tasks`;
const sp = `/v1/organizations/${ORG}/spaces/${SPACE}`;
const errorBody = (code: string) => ({
  error: { code, message: "x", request_id: "r" },
});

beforeEach(() => {
  localStorage.setItem(`openfox:onboarding:${ORG}`, JSON.stringify({}));
});
afterEach(() => localStorage.clear());

const stats = {
  total: 5,
  completed: 1,
  open: 4,
  overdue: 1,
  by_status: [
    { key: "st1", count: 2 },
    { key: "st2", count: 2 },
    { key: "st3", count: 1 },
  ],
  by_type: [{ key: "t1", count: 5 }],
  by_priority: [
    { key: "HIGH", count: 1 },
    { key: "NONE", count: 4 },
  ],
  by_assignee: [
    { kind: "HUMAN", id: "u1", count: 1 },
    { kind: "NONE", id: "", count: 4 },
  ],
  trend: Array.from({ length: 30 }, (_, i) => ({
    date: `2026-10-${String(i + 1).padStart(2, "0")}`,
    completed: i === 21 ? 1 : 0,
  })),
};

describe("统计视图", () => {
  it("工具栏统计按钮切换：指标卡、完成率，请求带筛选与调用方时区", async () => {
    const user = userEvent.setup();
    const server = renderAt(spaceTasks, (call) =>
      call.path === `${sp}/tasks/stats` ? json(200, stats) : undefined,
    );
    await screen.findByRole("region", { name: "待处理 2" });
    await user.click(screen.getByRole("button", { name: "统计" }));

    const view = await screen.findByTestId("task-stats");
    expect(
      within(screen.getByTestId("stat-total")).getByText("5"),
    ).toBeInTheDocument();
    expect(
      within(screen.getByTestId("stat-completed")).getByText("1"),
    ).toBeInTheDocument();
    expect(
      within(screen.getByTestId("stat-open")).getByText("4"),
    ).toBeInTheDocument();
    expect(
      within(screen.getByTestId("stat-overdue")).getByText("1"),
    ).toBeInTheDocument();
    expect(
      within(screen.getByTestId("stat-rate")).getByText("20%"),
    ).toBeInTheDocument();
    expect(within(view).getByText("完成趋势")).toBeInTheDocument();
    expect(
      within(screen.getByTestId("stats-priority")).getByText("高"),
    ).toBeInTheDocument();
    expect(
      within(screen.getByTestId("stats-assignee")).getByText("未指派"),
    ).toBeInTheDocument();
    // 人员显示全名或邮箱，而不是 ID 前缀
    expect(
      await within(screen.getByTestId("stats-assignee")).findByText(
        "lin@openfox.dev",
      ),
    ).toBeInTheDocument();

    const request = server.find("GET", "/tasks/stats")[0]!;
    expect(request.query.get("tz")).toBeTruthy();

    // 「仅未完成」默认开：不含已完成类别的状态；关闭后全部状态都在
    const list = screen.getByTestId("stats-status");
    expect(within(list).queryByText("已完成")).not.toBeInTheDocument();
    expect(within(list).getByText("待处理")).toBeInTheDocument();
    await user.click(screen.getByRole("switch", { name: "仅未完成" }));
    expect(
      within(screen.getByTestId("stats-status")).getByText("已完成"),
    ).toBeInTheDocument();

    // 再点一次回到看板
    await user.click(screen.getByRole("button", { name: "统计" }));
    expect(
      await screen.findByRole("region", { name: "待处理 2" }),
    ).toBeInTheDocument();
  });

  it("加载失败给出重试", async () => {
    const user = userEvent.setup();
    let fail = true;
    renderAt(spaceTasks, (call) =>
      call.path === `${sp}/tasks/stats`
        ? fail
          ? json(500, errorBody("INTERNAL_ERROR"))
          : json(200, stats)
        : undefined,
    );
    await screen.findByRole("region", { name: "待处理 2" });
    await user.click(screen.getByRole("button", { name: "统计" }));
    expect(await screen.findByText("统计加载失败")).toBeInTheDocument();
    fail = false;
    await user.click(screen.getByRole("button", { name: "重试" }));
    expect(await screen.findByTestId("stat-total")).toBeInTheDocument();
  });
});

describe("甘特图", () => {
  // 有状态的任务数据：PATCH 真正改日期并递增 version，刷新后的列表与服务端一致
  let dated = [] as ReturnType<typeof makeTask>[];
  beforeEach(() => {
    dated = [
      makeTask(1, "st1", { start_date: "2026-11-02", due_date: "2026-11-05" }),
      makeTask(2, "st2", { start_date: null, due_date: "2026-11-09" }),
      makeTask(3, "st1"),
    ];
  });
  const withDated: Override = (call) => {
    if (call.method === "GET" && call.path === `${sp}/tasks`)
      return json(200, { tasks: dated, next_cursor: "" });
    const patch = /\/tasks\/(task-\d+)$/.exec(call.path);
    if (call.method === "PATCH" && patch) {
      const body = call.body as { start_date?: string; due_date?: string };
      dated = dated.map((task) =>
        task.id === patch[1]
          ? { ...task, ...body, version: task.version + 1 }
          : task,
      );
      return json(
        200,
        dated.find((task) => task.id === patch[1]),
      );
    }
    return undefined;
  };

  async function openGantt(
    user: ReturnType<typeof userEvent.setup>,
    override: Override = withDated,
  ) {
    const server = renderAt(spaceTasks, override);
    await screen.findByRole("region", { name: "待处理 2" });
    await user.click(screen.getByRole("button", { name: "显示设置" }));
    const gantt = screen.getByRole("radio", { name: "甘特图" });
    expect(gantt).toBeEnabled();
    await user.click(gantt);
    await screen.findByTestId("gantt-scroller");
    return server;
  }

  it("显示菜单里的甘特图可用：画出有日期的条，未排期的任务标注「未排期」", async () => {
    const user = userEvent.setup();
    await openGantt(user);
    expect(await screen.findByTestId("gantt-bar-task-1")).toBeInTheDocument();
    expect(screen.getByTestId("gantt-bar-task-2")).toBeInTheDocument();
    expect(screen.queryByTestId("gantt-bar-task-3")).not.toBeInTheDocument();
    expect(
      within(screen.getByTestId("gantt-row-task-3")).getByText("未排期"),
    ).toBeInTheDocument();
    expect(screen.getByTestId("gantt-today")).toBeInTheDocument();
    expect(screen.getByTestId("gantt-bar-task-1")).toHaveAccessibleName(
      /任务标题 1，11月2日 至 11月5日/,
    );
    // 按开始日期排序：任务 1（2 日）在任务 2（9 日）之前，未排期在最后
    const rows = screen
      .getAllByRole("row")
      .map((row) => row.getAttribute("data-testid"));
    expect(rows).toEqual([
      "gantt-row-task-1",
      "gantt-row-task-2",
      "gantt-row-task-3",
    ]);
  });

  it("键盘方向键移动一天：PATCH 带新的开始 / 截止日期与 version；Shift+方向键只改截止日期", async () => {
    const user = userEvent.setup();
    const server = await openGantt(user);
    const bar = await screen.findByTestId("gantt-bar-task-1");
    bar.focus();
    await user.keyboard("{ArrowRight}");
    await waitFor(() =>
      expect(server.find("PATCH", "/tasks/task-1")).toHaveLength(1),
    );
    expect(server.find("PATCH", "/tasks/task-1")[0]!.body).toEqual({
      start_date: "2026-11-03",
      due_date: "2026-11-06",
      version: 1,
    });
    // 只有截止日的任务平移只改截止日期
    screen.getByTestId("gantt-bar-task-2").focus();
    await user.keyboard("{ArrowLeft}");
    await waitFor(() =>
      expect(server.find("PATCH", "/tasks/task-2")).toHaveLength(1),
    );
    expect(server.find("PATCH", "/tasks/task-2")[0]!.body).toEqual({
      due_date: "2026-11-08",
      version: 1,
    });
    screen.getByTestId("gantt-bar-task-1").focus();
    await user.keyboard("{Shift>}{ArrowRight}{/Shift}");
    await waitFor(() =>
      expect(server.find("PATCH", "/tasks/task-1")).toHaveLength(2),
    );
    const second = server.find("PATCH", "/tasks/task-1")[1]!.body;
    expect(second).toMatchObject({ due_date: "2026-11-07" });
    expect(second).not.toHaveProperty("start_date");
  });

  it("版本冲突时提示已刷新并回滚；没有编辑权限（403）同样回滚", async () => {
    const user = userEvent.setup();
    let code: "FORBIDDEN" | "VERSION_CONFLICT" = "VERSION_CONFLICT";
    const server = await openGantt(user, (call) => {
      if (call.method === "PATCH")
        return json(code === "FORBIDDEN" ? 403 : 409, errorBody(code));
      return withDated(call);
    });
    const bar = await screen.findByTestId("gantt-bar-task-1");
    bar.focus();
    await user.keyboard("{ArrowRight}");
    expect(
      await screen.findByText("任务已被其他人修改，已刷新为最新内容"),
    ).toBeInTheDocument();
    await waitFor(() =>
      expect(screen.getByTestId("gantt-bar-task-1")).toHaveAccessibleName(
        /11月2日 至 11月5日/,
      ),
    );
    code = "FORBIDDEN";
    screen.getByTestId("gantt-bar-task-1").focus();
    await user.keyboard("{ArrowRight}");
    await waitFor(() =>
      expect(
        server.find("PATCH", "/tasks/task-1").length,
      ).toBeGreaterThanOrEqual(2),
    );
    await waitFor(() =>
      expect(screen.getByTestId("gantt-bar-task-1")).toHaveAccessibleName(
        /11月2日 至 11月5日/,
      ),
    );
  });

  it("拖动条改日期：指针按下、移动、抬起后提交", async () => {
    const user = userEvent.setup();
    const server = await openGantt(user);
    const bar = await screen.findByTestId("gantt-bar-task-1");
    // 周刻度每天 14px：向右拖 28px = 2 天（jsdom 的 getBoundingClientRect 全 0，点在中间之外时按边缘判断）
    fireEvent.pointerDown(bar, { clientX: 100, button: 0, pointerId: 1 });
    fireEvent.pointerMove(bar, { clientX: 128, pointerId: 1 });
    fireEvent.pointerUp(bar, { clientX: 128, pointerId: 1 });
    await waitFor(() =>
      expect(server.find("PATCH", "/tasks/task-1")).toHaveLength(1),
    );
  });

  it("最后一次移动尚未渲染就松手时仍提交最终日期", async () => {
    const server = await openGantt(userEvent.setup());
    const bar = await screen.findByTestId("gantt-bar-task-1");
    fireEvent.pointerDown(bar, { clientX: 100, button: 0, pointerId: 1 });
    act(() => {
      bar.dispatchEvent(
        new PointerEvent("pointermove", {
          bubbles: true,
          clientX: 128,
          buttons: 1,
          pointerId: 1,
        }),
      );
      bar.dispatchEvent(
        new PointerEvent("pointerup", {
          bubbles: true,
          clientX: 128,
          pointerId: 1,
        }),
      );
    });
    await waitFor(() =>
      expect(server.find("PATCH", "/tasks/task-1")).toHaveLength(1),
    );
    expect(server.find("PATCH", "/tasks/task-1")[0]!.body).toEqual({
      due_date: "2026-11-07",
      version: 1,
    });
  });

  // 没有送达条形的松手不能让预览一直挂着。
  // jsdom 的 getBoundingClientRect 全 0：按在 x=100 处判定为拖右端（只改截止日期），周刻度每天 14px。
  const startDrag = (bar: HTMLElement) => {
    fireEvent.pointerDown(bar, { clientX: 100, button: 0, pointerId: 1 });
    fireEvent.pointerMove(bar, { clientX: 128, pointerId: 1, buttons: 1 });
  };
  const original = /11月2日 至 11月5日/;
  const preview = /11月2日 至 11月7日/;
  const barOf = () => screen.getByTestId("gantt-bar-task-1");

  it("丢失指针捕获（没有 pointerup）：取消拖拽，条形回到原日期且不发请求", async () => {
    const user = userEvent.setup();
    const server = await openGantt(user);
    startDrag(await screen.findByTestId("gantt-bar-task-1"));
    expect(barOf()).toHaveAccessibleName(preview);
    fireEvent.lostPointerCapture(barOf(), { pointerId: 1 });
    expect(barOf()).toHaveAccessibleName(original);
    // 之后迟到的 pointerup 也不会再提交
    fireEvent.pointerUp(barOf(), { clientX: 128, pointerId: 1 });
    expect(barOf()).toHaveAccessibleName(original);
    expect(server.find("PATCH", "/tasks/task-1")).toHaveLength(0);
  });

  it("正常松手：pointerup 提交 PATCH，随后的 lostpointercapture 不会撤销", async () => {
    const user = userEvent.setup();
    const server = await openGantt(user);
    startDrag(await screen.findByTestId("gantt-bar-task-1"));
    fireEvent.pointerUp(barOf(), { clientX: 128, pointerId: 1 });
    fireEvent.lostPointerCapture(barOf(), { pointerId: 1 });
    await waitFor(() =>
      expect(server.find("PATCH", "/tasks/task-1")).toHaveLength(1),
    );
    expect(server.find("PATCH", "/tasks/task-1")[0]!.body).toEqual({
      due_date: "2026-11-07",
      version: 1,
    });
    await waitFor(() => expect(barOf()).toHaveAccessibleName(preview));
  });

  it("拖动中按 Esc、窗口失焦、松手落在条形之外：都取消拖拽且不发请求", async () => {
    const user = userEvent.setup();
    const server = await openGantt(user);
    await screen.findByTestId("gantt-bar-task-1");

    startDrag(barOf());
    expect(barOf()).toHaveAccessibleName(preview);
    fireEvent.keyDown(window, { key: "Escape" });
    expect(barOf()).toHaveAccessibleName(original);

    startDrag(barOf());
    expect(barOf()).toHaveAccessibleName(preview);
    fireEvent.blur(window);
    expect(barOf()).toHaveAccessibleName(original);

    startDrag(barOf());
    expect(barOf()).toHaveAccessibleName(preview);
    fireEvent.pointerUp(document.body, { clientX: 128, pointerId: 1 });
    expect(barOf()).toHaveAccessibleName(original);

    // 鼠标已无按键却仍收到移动：松手丢失，同样取消
    startDrag(barOf());
    fireEvent.pointerMove(barOf(), {
      clientX: 142,
      pointerId: 1,
      pointerType: "mouse",
      buttons: 0,
    });
    expect(barOf()).toHaveAccessibleName(original);

    expect(server.find("PATCH", "/tasks/task-1")).toHaveLength(0);
  });

  it("拖拽提交失败：条形回到原日期并提示错误", async () => {
    const user = userEvent.setup();
    const server = await openGantt(user, (call) => {
      if (call.method === "PATCH")
        return json(400, errorBody("INVALID_DATE_RANGE"));
      return withDated(call);
    });
    startDrag(await screen.findByTestId("gantt-bar-task-1"));
    fireEvent.pointerUp(barOf(), { clientX: 128, pointerId: 1 });
    await waitFor(() =>
      expect(server.find("PATCH", "/tasks/task-1")).toHaveLength(1),
    );
    expect(
      await screen.findByText("截止日期不能早于开始日期"),
    ).toBeInTheDocument();
    await waitFor(() => expect(barOf()).toHaveAccessibleName(original));
  });
});

describe("收藏", () => {
  const detail = `/o/${ORG}/spaces/${SPACE}/tasks?task=OPEN-1`;

  it("详情顶栏星标：收藏 → PUT，取消 → DELETE；失败回滚并提示", async () => {
    const user = userEvent.setup();
    let fail = false;
    let favorited = false;
    const server = renderAt(detail, (call) => {
      if (call.path.endsWith("/tasks/task-1/favorite")) {
        if (fail) return json(500, errorBody("INTERNAL_ERROR"));
        favorited = call.method === "PUT";
        return new Response(null, { status: 204 });
      }
      // 详情接口回显服务端保存的收藏状态
      if (call.path.endsWith("/tasks/by-display-id/OPEN-1"))
        return json(200, {
          ...makeTask(1, "st1"),
          favorited,
          description_reactions: [],
        });
      return undefined;
    });
    const star = await screen.findByRole("button", { name: "收藏" });
    expect(star).toHaveAttribute("aria-pressed", "false");
    await user.click(star);
    await waitFor(() =>
      expect(server.find("PUT", "/tasks/task-1/favorite")).toHaveLength(1),
    );
    const unstar = await screen.findByRole("button", { name: "取消收藏" });
    expect(unstar).toHaveAttribute("aria-pressed", "true");
    await user.click(unstar);
    await waitFor(() =>
      expect(server.find("DELETE", "/tasks/task-1/favorite")).toHaveLength(1),
    );
    expect(
      await screen.findByRole("button", { name: "收藏" }),
    ).toBeInTheDocument();

    fail = true;
    await user.click(screen.getByRole("button", { name: "收藏" }));
    expect(await screen.findByText("无法更新收藏")).toBeInTheDocument();
    expect(await screen.findByRole("button", { name: "收藏" })).toHaveAttribute(
      "aria-pressed",
      "false",
    );
  });

  it("我的任务保留全部、我负责的、我创建的三个范围，并发送对应查询", async () => {
    const user = userEvent.setup();
    const server = renderAt(`/o/${ORG}/my-tasks`, (call) =>
      call.path.startsWith(`/v1/organizations/${ORG}/my-tasks`) &&
      call.query.get("scope") === "created"
        ? call.path.endsWith("/counts")
          ? json(200, { total: 0, by_status: [] })
          : json(200, { tasks: [], next_cursor: "" })
        : undefined,
    );
    await user.click(await screen.findByRole("radio", { name: "我创建的" }));
    expect(
      screen.queryByRole("radio", { name: "我收藏的" }),
    ).not.toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "我创建的" })).toHaveAttribute(
      "aria-checked",
      "true",
    );
    await waitFor(() =>
      expect(
        server.calls.some(
          (c) =>
            c.path.endsWith("/my-tasks/counts") &&
            c.query.get("scope") === "created",
        ),
      ).toBe(true),
    );
  });
});
