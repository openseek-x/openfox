import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, useLocation } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { App, createQueryClient } from "../App";
import { displayStorageKey } from "./model";
import {
  backend,
  json,
  makeTask,
  ORG,
  SPACE,
  type Override,
} from "./test-backend";

// 任务分组显示：显示设置选项与互斥、看板按处理人分列 / 按优先级分行、拖动限制、列表二级分组、?view= 读写、视图里的分组。

const o = `/v1/organizations/${ORG}`;
const sp = `${o}/spaces/${SPACE}`;
const spaceTasks = `/o/${ORG}/spaces/${SPACE}/tasks`;

// task-1：林 + 产品大王（多处理人），task-2：未指派、高优先级，task-3：林、高优先级
const grouped = [
  makeTask(1, "st1", {
    assignees: [
      { kind: "HUMAN", id: "u1", name: "lin@openfox.dev" },
      { kind: "AGENT", id: "a1", name: "产品大王" },
    ],
  }),
  makeTask(2, "st1", { priority: "HIGH" }),
  makeTask(3, "st2", {
    priority: "HIGH",
    assignees: [{ kind: "HUMAN", id: "u1", name: "lin@openfox.dev" }],
  }),
];

const withTasks: Override = (call) => {
  if (call.path === `${sp}/tasks` && call.method === "GET") {
    const ids = call.query.getAll("status_id");
    return json(200, {
      tasks: grouped.filter((t) => !ids.length || ids.includes(t.status_id)),
      next_cursor: "",
    });
  }
  return undefined;
};

function Location() {
  const location = useLocation();
  return <output data-testid="location">{location.search}</output>;
}

function renderAt(path: string, extra?: Override) {
  const server = backend((call) => extra?.(call) ?? withTasks(call));
  const queryClient = createQueryClient();
  queryClient.setDefaultOptions({ queries: { retry: false } });
  const view = render(
    <MemoryRouter initialEntries={[path]}>
      <App api={server.api} queryClient={queryClient} locale="zh-CN" />
      <Location />
    </MemoryRouter>,
  );
  return { server, unmount: view.unmount };
}

/** 本机显示设置（空间任务页） */
function seed(display: Record<string, unknown>) {
  localStorage.setItem(
    displayStorageKey(ORG, `space:${SPACE}`),
    JSON.stringify(display),
  );
}

async function openDisplay(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByRole("button", { name: "显示设置" }));
  return screen.findByRole("dialog", { name: "显示设置" });
}

const optionNames = (select: HTMLElement) =>
  within(select)
    .getAllByRole("option")
    .map((option) => option.textContent);

beforeEach(() => {
  localStorage.setItem(`openfox:onboarding:${ORG}`, JSON.stringify({}));
});
afterEach(() => localStorage.clear());

describe("显示设置的分组选项", () => {
  it("看板有列 / 行，行不能与列相同；行不分组时「显示空行」禁用；未启用迭代时没有迭代", async () => {
    const user = userEvent.setup();
    renderAt(spaceTasks);
    const menu = await openDisplay(user);
    const columns = within(menu).getByRole("combobox", { name: "列" });
    const rows = within(menu).getByRole("combobox", { name: "行" });
    expect(optionNames(columns)).toEqual(["状态", "处理人", "小队"]);
    expect(optionNames(rows)).toEqual(["不分组", "处理人", "小队", "优先级"]);
    expect(
      within(menu).getByRole("switch", { name: "显示空行" }),
    ).toBeDisabled();
    expect(
      within(menu).queryByRole("combobox", { name: "二级分组" }),
    ).not.toBeInTheDocument();

    await user.selectOptions(rows, "assignee");
    expect(
      within(menu).getByRole("switch", { name: "显示空行" }),
    ).toBeEnabled();
    // 列改成与行相同的字段：行回落不分组
    await user.selectOptions(columns, "assignee");
    expect(rows).toHaveValue("none");
    expect(optionNames(rows)).toEqual(["不分组", "状态", "小队", "优先级"]);
    const saved = JSON.parse(
      localStorage.getItem(displayStorageKey(ORG, `space:${SPACE}`))!,
    ) as Record<string, unknown>;
    expect(saved).toMatchObject({
      boardColumns: "assignee",
      boardRows: "none",
    });
  });

  it("列表只有二级分组，表格没有分组选项", async () => {
    const user = userEvent.setup();
    renderAt(spaceTasks);
    const menu = await openDisplay(user);
    await user.click(within(menu).getByRole("radio", { name: "列表" }));
    const subgroup = within(menu).getByRole("combobox", { name: "二级分组" });
    expect(optionNames(subgroup)).toEqual([
      "不分组",
      "处理人",
      "小队",
      "优先级",
    ]);
    expect(within(menu).queryByRole("combobox", { name: "列" })).toBeNull();
    await user.click(within(menu).getByRole("radio", { name: "表格" }));
    expect(
      within(menu).queryByRole("combobox", { name: "二级分组" }),
    ).toBeNull();
    expect(within(menu).queryByRole("combobox", { name: "列" })).toBeNull();
  });
});

describe("分组看板", () => {
  it("按处理人分列：多处理人的卡片在每个处理人列里各出现一次，没有处理人的进「未指派」", async () => {
    seed({ boardColumns: "assignee" });
    const { server } = renderAt(spaceTasks);
    const lin = await screen.findByRole("region", {
      name: "lin@openfox.dev 2",
    });
    expect(await within(lin).findByText("任务标题 1")).toBeInTheDocument();
    expect(within(lin).getByText("任务标题 3")).toBeInTheDocument();
    const agent = screen.getByRole("region", { name: "产品大王 1" });
    expect(within(agent).getByText("任务标题 1")).toBeInTheDocument();
    const none = screen.getByRole("region", { name: "未指派 1" });
    expect(within(none).getByText("任务标题 2")).toBeInTheDocument();
    // 没有任务的成员默认不显示（「显示空白列」关闭）
    expect(
      screen.queryByRole("region", { name: /^wang@openfox.dev/ }),
    ).toBeNull();
    // 一次分页：任务列表请求不带 status_id
    const lists = server.find("GET", `${sp}/tasks`);
    expect(lists.length).toBeGreaterThan(0);
    expect(lists.every((c) => c.query.getAll("status_id").length === 0)).toBe(
      true,
    );
  });

  it("按处理人分列、按优先级分行：单元格 = 列 · 行；开启显示空白列后出现没有任务的成员", async () => {
    const user = userEvent.setup();
    seed({ boardColumns: "assignee", boardRows: "priority" });
    renderAt(spaceTasks);
    const high = await screen.findByRole("region", { name: "高 2" });
    const cell = within(high).getByRole("region", {
      name: "lin@openfox.dev · 高",
    });
    expect(await within(cell).findByText("任务标题 3")).toBeInTheDocument();
    expect(
      within(high).getByRole("region", { name: "未指派 · 高" }),
    ).toHaveTextContent("任务标题 2");
    expect(
      screen.getByRole("region", { name: "无优先级 1" }),
    ).toBeInTheDocument();
    // 没有任务的优先级行默认不显示
    expect(screen.queryByRole("region", { name: /^紧急/ })).toBeNull();
    const menu = await openDisplay(user);
    await user.click(within(menu).getByRole("switch", { name: "显示空行" }));
    await user.click(within(menu).getByRole("switch", { name: "显示空白列" }));
    expect(
      await screen.findByRole("region", { name: "紧急 0" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("region", { name: "wang@openfox.dev · 高" }),
    ).toBeInTheDocument();
  });

  it("非状态分列时不能跨列拖动：非手动排序时卡片不可拖并给出提示，手动排序时可在列内拖", async () => {
    seed({ boardColumns: "assignee", sort: "created" });
    const { unmount } = renderAt(spaceTasks);
    const lin = await screen.findByRole("region", {
      name: "lin@openfox.dev 2",
    });
    const card = await within(lin).findByRole("button", {
      name: /OPEN-1 任务标题 1/,
    });
    expect(card).toHaveAttribute("aria-disabled", "true");
    expect(
      screen.getByText("按处理人分列且非手动排序时，卡片不能拖动"),
    ).toBeInTheDocument();
    unmount();
    seed({ boardColumns: "assignee", sort: "manual" });
    renderAt(spaceTasks);
    const again = await screen.findByRole("region", {
      name: "lin@openfox.dev 2",
    });
    const movable = await within(again).findByRole("button", {
      name: /OPEN-1 任务标题 1/,
    });
    expect(movable).toHaveAttribute("aria-disabled", "false");
  });
});

describe("列表二级分组", () => {
  it("状态分组下按处理人二级分组，多处理人的任务在每个子组各出现一次", async () => {
    seed({ layout: "list", listSubgroup: "assignee" });
    renderAt(spaceTasks);
    const todo = await screen.findByRole("region", { name: "待处理 2" });
    const lin = await within(todo).findByRole("group", {
      name: "lin@openfox.dev 1",
    });
    expect(within(lin).getByText("任务标题 1")).toBeInTheDocument();
    expect(
      within(todo).getByRole("group", { name: "产品大王 1" }),
    ).toHaveTextContent("任务标题 1");
    expect(
      within(todo).getByRole("group", { name: "未指派 1" }),
    ).toHaveTextContent("任务标题 2");
  });
});

describe("地址里的布局 ?view=", () => {
  it("地址优先于本机设置；切换布局用 replace 写回地址，刷新后保留", async () => {
    const user = userEvent.setup();
    seed({ layout: "board" });
    const { unmount } = renderAt(`${spaceTasks}?view=list`);
    expect(
      await screen.findByRole("region", { name: "任务列表" }),
    ).toBeInTheDocument();
    const menu = await openDisplay(user);
    await user.click(within(menu).getByRole("radio", { name: "表格" }));
    await waitFor(() =>
      expect(screen.getByTestId("location")).toHaveTextContent("?view=table"),
    );
    unmount();
    // 「刷新」：用写回后的地址重新打开
    renderAt(`${spaceTasks}?view=table`);
    expect(await screen.findByRole("table")).toBeInTheDocument();
  });

  it("非法的 ?view= 被忽略，回落本机设置", async () => {
    seed({ layout: "list" });
    renderAt(`${spaceTasks}?view=kanban`);
    expect(
      await screen.findByRole("region", { name: "任务列表" }),
    ).toBeInTheDocument();
  });
});

describe("已保存视图里的分组", () => {
  const space = {
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
      projects: false,
      iterations: false,
      triage: false,
    },
    multi_assignee: true,
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
  };
  const view = {
    id: "v1",
    space_id: SPACE,
    name: "按人看",
    description: "",
    icon_kind: null,
    icon: null,
    icon_url: null,
    visibility: "space",
    filters: {},
    display: {
      layout: "board",
      boardColumns: "assignee",
      boardRows: "priority",
    },
    created_by: { id: "u1", email: "lin@openfox.dev" },
    created_at: "2026-10-20T00:00:00Z",
    updated_at: "2026-10-20T00:00:00Z",
    pinned: false,
    position: null,
    member_view_mode: null,
    can_edit: true,
    version: 1,
  };
  const views: Override = (call) => {
    if (call.path === sp) return json(200, space);
    if (call.path === `${o}/spaces`) return json(200, { spaces: [space] });
    if (call.path === `${o}/views/v1` && call.method === "GET")
      return json(200, view);
    if (call.path.endsWith("/view-mode-mine"))
      return new Response(null, { status: 204 });
    return undefined;
  };

  it("视图 display 带分组字段时按它分组；打开带 ?view= 的链接不改写本人展示方式", async () => {
    const { server, unmount } = renderAt(
      `/o/${ORG}/spaces/${SPACE}/views/v1`,
      views,
    );
    const high = await screen.findByRole("region", { name: "高 2" });
    expect(
      await within(high).findByRole("region", { name: "lin@openfox.dev · 高" }),
    ).toHaveTextContent("任务标题 3");
    unmount();

    const again = renderAt(
      `/o/${ORG}/spaces/${SPACE}/views/v1?view=list`,
      views,
    );
    expect(
      await screen.findByRole("region", { name: "任务列表" }),
    ).toBeInTheDocument();
    // 等到任务加载完，确认没有发出 view-mode-mine
    await screen.findByRole("region", { name: "待处理 2" });
    expect(again.server.find("PUT", "/view-mode-mine")).toHaveLength(0);
    expect(server.find("PUT", "/view-mode-mine")).toHaveLength(0);
  });
});
