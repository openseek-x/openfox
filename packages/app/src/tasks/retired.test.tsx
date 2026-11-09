import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { App, createQueryClient } from "../App";
import { keys } from "../context";
import { backend, json, ORG, SPACE, type Override } from "./test-backend";

// 已停用空间的任务只读：看板拖动、卡片菜单、表格单元格、详情行内编辑、属性、评论、顶栏菜单都禁用并提示原因。

const RETIRED_TEXT = "此空间已停用，只能查看。";

/** s1 已停用：不在活跃空间列表里，详情返回 retired。 */
const retired: Override = (call) => {
  const o = `/v1/organizations/${ORG}`;
  if (call.path === `${o}/spaces`) return json(200, { spaces: [] });
  if (call.path === `${o}/spaces/${SPACE}`)
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

function renderAt(path: string, override: Override = retired) {
  const server = backend(override);
  const queryClient = createQueryClient();
  queryClient.setDefaultOptions({ queries: { retry: false } });
  render(
    <MemoryRouter initialEntries={[path]}>
      <App api={server.api} queryClient={queryClient} locale="zh-CN" />
    </MemoryRouter>,
  );
  return { ...server, queryClient };
}

const spaceTasks = `/o/${ORG}/spaces/${SPACE}/tasks`;

beforeEach(() => {
  localStorage.setItem(`openfox:onboarding:${ORG}`, JSON.stringify({}));
});
afterEach(() => localStorage.clear());

describe("已停用空间 · 任务视图只读", () => {
  it("看板：横幅、卡片不可拖动、菜单里「移动到」禁用", async () => {
    const user = userEvent.setup();
    renderAt(spaceTasks);
    expect(
      await screen.findByText("此空间已停用，任务只读。"),
    ).toBeInTheDocument();
    const todo = await screen.findByRole("region", { name: "待处理 2" });
    const card = await within(todo).findByRole("button", {
      name: /OPEN-1 任务标题 1/,
    });
    await waitFor(() => expect(card).toHaveAttribute("data-readonly", "true"));
    expect(card).toHaveAttribute("title", RETIRED_TEXT);
    // 拖动被禁用：dnd-kit 把禁用的拖拽源标成 aria-disabled
    expect(card).toHaveAttribute("aria-disabled", "true");
    await user.click(within(card).getByRole("button", { name: "任务操作" }));
    const move = await screen.findByRole("menuitem", { name: "移动到" });
    expect(move).toHaveAttribute("aria-disabled", "true");
    expect(move).toHaveAttribute("title", RETIRED_TEXT);
    await user.click(move);
    // 不进入状态列表
    expect(screen.queryByRole("menuitemradio", { name: "进行中" })).toBeNull();
  });

  it("表格：状态、优先级、处理人、截止日期单元格禁用", async () => {
    localStorage.setItem(
      `openfox:tasks-display:${ORG}:space:${SPACE}`,
      JSON.stringify({ layout: "table" }),
    );
    renderAt(spaceTasks);
    const table = await screen.findByRole("table", { name: "任务表格" });
    await waitFor(() =>
      expect(within(table).getAllByRole("row").length).toBeGreaterThan(1),
    );
    const row = within(table).getAllByRole("row")[1]!;
    await waitFor(() =>
      expect(within(row).getByRole("button", { name: /^状态/ })).toBeDisabled(),
    );
    expect(within(row).getByRole("button", { name: /^优先级/ })).toBeDisabled();
    expect(within(row).getByRole("button", { name: /^处理人/ })).toBeDisabled();
    expect(
      within(row).getByRole("button", { name: /^截止日期/ }),
    ).toBeDisabled();
  });

  it("详情：标题只读、类型 / 属性 / 评论 / 关注禁用，顶栏写操作禁用，悬停显示原因，且不发写请求", async () => {
    const user = userEvent.setup();
    const server = renderAt(`${spaceTasks}?task=OPEN-1`);
    const title = await screen.findByRole("textbox", { name: "任务标题" });
    await waitFor(() => expect(title).toHaveAttribute("readonly"));
    await user.type(title, "改不了{Enter}");
    expect(title).toHaveValue("任务标题 1");
    expect(screen.getByRole("textbox", { name: "任务描述" })).toHaveAttribute(
      "readonly",
    );
    expect(
      screen.getByRole("button", { name: /^修改任务类型/ }),
    ).toBeDisabled();
    expect(screen.getByRole("textbox", { name: "评论..." })).toBeDisabled();
    expect(screen.getByRole("button", { name: "发送评论" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "关注" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "更改追踪者" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "添加子任务" })).toBeDisabled();
    const properties = screen.getByRole("group", { name: "属性" });
    expect(properties).toBeDisabled();
    // 悬停禁用控件的外层，显示停用原因
    const tips = screen.getAllByLabelText(RETIRED_TEXT);
    await user.hover(tips[0]!);
    expect(await screen.findByRole("tooltip")).toHaveTextContent(RETIRED_TEXT);
    // 顶栏「更多」：删除、添加链接等写操作禁用
    await user.click(screen.getByRole("button", { name: "更多操作" }));
    expect(
      await screen.findByRole("menuitem", { name: "删除任务" }),
    ).toHaveAttribute("aria-disabled", "true");
    expect(screen.getByRole("menuitem", { name: "添加链接" })).toHaveAttribute(
      "aria-disabled",
      "true",
    );
    expect(
      screen.getByRole("menuitem", { name: "复制链接" }),
    ).not.toHaveAttribute("aria-disabled");
    expect(server.calls.filter((c) => c.method !== "GET")).toEqual([]);
  });

  it("活跃空间不受影响：标题可编辑、评论可用", async () => {
    renderAt(`${spaceTasks}?task=OPEN-1`, () => undefined);
    const title = await screen.findByRole("textbox", { name: "任务标题" });
    expect(title).not.toHaveAttribute("readonly");
    expect(screen.getByRole("textbox", { name: "评论..." })).toBeEnabled();
    expect(screen.queryByText("此空间已停用，任务只读。")).toBeNull();
  });
});

const VIEWER_TEXT = "你在此空间只有查看权限，不能修改任务。";
function membership(role: string): Override {
  return (call) => {
    if (call.path === `/v1/organizations/${ORG}/spaces`)
      return json(200, {
        spaces: [
          {
            id: SPACE,
            prefix: "open",
            name: "默认空间",
            visibility: "shared",
            status: "active",
            version: 1,
            my_role: role,
            features: {
              views: false,
              projects: false,
              iterations: false,
              triage: false,
            },
          },
        ],
      });
    return undefined;
  };
}

describe("活跃空间只读成员", () => {
  it("详情禁止写入，权限恢复后同一详情可以编辑", async () => {
    const user = userEvent.setup();
    let role = "VIEWER";
    const server = renderAt(`${spaceTasks}?task=OPEN-1`, (call) =>
      membership(role)(call),
    );
    const title = await screen.findByRole("textbox", { name: "任务标题" });
    await waitFor(() => expect(title).toHaveAttribute("readonly"));
    await user.type(title, "不能写入");
    expect(title).toHaveValue("任务标题 1");
    expect(screen.getByRole("textbox", { name: "任务描述" })).toHaveAttribute(
      "readonly",
    );
    expect(screen.getByRole("group", { name: "属性" })).toBeDisabled();
    expect(screen.getByRole("textbox", { name: "评论..." })).toBeDisabled();
    expect(screen.getByRole("button", { name: "添加子任务" })).toBeDisabled();
    expect(screen.getAllByText(VIEWER_TEXT).length).toBeGreaterThan(0);
    expect(server.calls.filter((c) => c.method !== "GET")).toEqual([]);
    role = "MEMBER";
    await act(async () => {
      await server.queryClient.invalidateQueries({
        queryKey: keys.spaces(ORG),
      });
    });
    await waitFor(() =>
      expect(
        screen.getByRole("textbox", { name: "任务标题" }),
      ).not.toHaveAttribute("readonly"),
    );
    expect(screen.getByRole("textbox", { name: "评论..." })).toBeEnabled();
  });

  it("看板卡片禁拖动和更改状态，表格属性禁用", async () => {
    const user = userEvent.setup();
    renderAt(spaceTasks, membership("VIEWER"));
    const card = await screen.findByRole("button", {
      name: /OPEN-1 任务标题 1/,
    });
    await waitFor(() => expect(card).toHaveAttribute("aria-disabled", "true"));
    expect(card).toHaveAttribute("title", VIEWER_TEXT);
    await user.click(within(card).getByRole("button", { name: "任务操作" }));
    expect(
      await screen.findByRole("menuitem", { name: "移动到" }),
    ).toHaveAttribute("aria-disabled", "true");
  });

  it("表格的所有任务属性只读", async () => {
    localStorage.setItem(
      `openfox:tasks-display:${ORG}:space:${SPACE}`,
      JSON.stringify({ layout: "table" }),
    );
    renderAt(spaceTasks, membership("VIEWER"));
    const table = await screen.findByRole("table", { name: "任务表格" });
    const status = await within(table).findAllByRole("button", {
      name: /^状态/,
    });
    await waitFor(() => expect(status[0]).toBeDisabled());
    for (const label of [/^优先级/, /^处理人/, /^截止日期/])
      expect(
        within(table).getAllByRole("button", { name: label })[0],
      ).toBeDisabled();
  });

  it("权限在提交前被撤销时，保存拒绝后恢复原始标题", async () => {
    const user = userEvent.setup();
    const server = renderAt(`${spaceTasks}?task=OPEN-1`, (call) =>
      call.method === "PATCH"
        ? json(403, { error: { code: "FORBIDDEN", message: "not allowed" } })
        : undefined,
    );
    const title = await screen.findByRole("textbox", { name: "任务标题" });
    await user.clear(title);
    await user.type(title, "未保存的内容");
    await user.tab();
    await waitFor(() =>
      expect(server.calls.some((c) => c.method === "PATCH")).toBe(true),
    );
    await waitFor(() => expect(title).toHaveValue("任务标题 1"));
  });
});
