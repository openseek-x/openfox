import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { App, createQueryClient } from "../App";
import { displayStorageKey } from "../tasks/model";
import {
  backend,
  json,
  makeTask,
  ORG,
  SPACE,
  type Override,
} from "../tasks/test-backend";

// 迭代列表、行菜单、手动模式开始 / 完成 / 删除、详情规划与日期显示。

const o = `/v1/organizations/${ORG}`;
const sp = `${o}/spaces/${SPACE}`;

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
  features: { views: true, projects: true, iterations: true, triage: false },
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
};

const live = {
  scope_count: 3,
  completed_count: 1,
  scope_points: 5,
  completed_points: 2,
};
const iteration = (
  id: string,
  status: string,
  extra: Record<string, unknown> = {},
) => ({
  id,
  space_id: SPACE,
  number: id === "i1" ? 1 : 2,
  name: "",
  display_name: id === "i1" ? "迭代 1" : "迭代 2",
  description: "",
  status,
  starts_at: "2026-10-12T00:01:00+08:00",
  ends_at: "2026-10-26T00:01:00+08:00",
  start_date: "2026-10-12",
  end_date: "2026-10-25",
  started_at: null,
  completed_at: null,
  scope_count: null,
  completed_count: null,
  scope_points: null,
  completed_points: null,
  live,
  capacity_percent: null,
  links: [],
  version: 3,
  ...extra,
});

const settings = (extra: Record<string, unknown> = {}) => ({
  enabled: true,
  mode: "manual",
  duration_weeks: 2,
  cooldown_weeks: 0,
  start_weekday: 1,
  timezone: "Asia/Shanghai",
  upcoming_count: 2,
  auto_add_started: false,
  auto_add_completed: false,
  next_start_date: null,
  current: null,
  version: 2,
  ...extra,
});

function renderAt(
  path: string,
  opts: {
    settings?: object;
    items?: object[];
    iterationsOff?: boolean;
    tasks?: Override;
  } = {},
) {
  const items = opts.items ?? [
    iteration("i1", "planned"),
    iteration("i2", "active"),
  ];
  const override: Override = (call) => {
    const custom = opts.tasks?.(call);
    if (custom) return custom;
    if (call.path === sp)
      return json(
        200,
        opts.iterationsOff
          ? { ...space, features: { ...space.features, iterations: false } }
          : space,
      );
    if (call.path === `${sp}/iteration-settings`)
      return json(200, opts.settings ?? settings());
    if (call.path === `${sp}/iterations` && call.method === "GET")
      return json(200, { iterations: items, next_cursor: "" });
    const one = items.find(
      (item) => call.path === `${sp}/iterations/${(item as { id: string }).id}`,
    );
    if (one && call.method === "GET") return json(200, one);
    if (call.path.startsWith(`${sp}/iterations/`) && call.method !== "GET") {
      return call.method === "DELETE"
        ? new Response(null, { status: 204 })
        : json(200, items[0]);
    }
    if (
      call.path === `${sp}/tasks` &&
      call.query.get("iteration_id") === "none"
    )
      return json(200, {
        tasks: [makeTask(9, "st1", { title: "待规划任务" })],
        next_cursor: "",
      });
    return undefined;
  };
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

const list = `/o/${ORG}/spaces/${SPACE}/iterations`;

beforeEach(() => {
  localStorage.setItem(`openfox:onboarding:${ORG}`, JSON.stringify({}));
});
afterEach(() => localStorage.clear());

describe("迭代列表", () => {
  it("未启用时显示启用入口", async () => {
    renderAt(list, { settings: settings({ enabled: false }) });
    expect(
      await screen.findByRole("link", { name: "启用迭代" }),
    ).toBeInTheDocument();
  });

  it("按空间时区显示日期范围、状态与范围计数", async () => {
    renderAt(list);
    const row = (await screen.findByText("迭代 1")).closest("li")!;
    expect(within(row).getByText("10月12日 – 10月25日")).toBeInTheDocument();
    expect(within(row).getByText("计划中")).toBeInTheDocument();
    expect(within(row).getByText(/1\/3/)).toBeInTheDocument();
  });

  it("自动模式不显示新建迭代，也没有开始 / 完成 / 删除", async () => {
    const user = userEvent.setup();
    renderAt(list, { settings: settings({ mode: "automatic" }) });
    await screen.findByText("迭代 1");
    expect(screen.queryByRole("button", { name: "新建迭代" })).toBeNull();
    await user.click(
      within((await screen.findByText("迭代 1")).closest("li")!).getByRole(
        "button",
        { name: "迭代" },
      ),
    );
    expect(
      await screen.findByRole("menuitem", { name: "更改迭代日期" }),
    ).toBeInTheDocument();
    expect(screen.queryByRole("menuitem", { name: "开始迭代" })).toBeNull();
    expect(screen.queryByRole("menuitem", { name: "删除迭代" })).toBeNull();
  });

  it("手动模式：开始迭代需确认并带上 version", async () => {
    const user = userEvent.setup();
    const server = renderAt(list);
    await user.click(
      within((await screen.findByText("迭代 1")).closest("li")!).getByRole(
        "button",
        { name: "迭代" },
      ),
    );
    await user.click(await screen.findByRole("menuitem", { name: "开始迭代" }));
    await user.click(await screen.findByRole("button", { name: "确认" }));
    await waitFor(() =>
      expect(server.find("POST", "/iterations/i1/start")).toHaveLength(1),
    );
    expect(server.find("POST", "/iterations/i1/start")[0]!.body).toEqual({
      version: 3,
    });
  });

  it("手动模式：完成迭代选择去向，删除迭代用 ?version=", async () => {
    const user = userEvent.setup();
    const server = renderAt(list);
    await user.click(
      within((await screen.findByText("迭代 2")).closest("li")!).getByRole(
        "button",
        { name: "迭代" },
      ),
    );
    await user.click(await screen.findByRole("menuitem", { name: "完成迭代" }));
    await user.click(await screen.findByRole("radio", { name: "移出迭代" }));
    await user.click(await screen.findByRole("button", { name: "确认" }));
    await waitFor(() =>
      expect(server.find("POST", "/iterations/i2/complete")).toHaveLength(1),
    );
    expect(server.find("POST", "/iterations/i2/complete")[0]!.body).toEqual({
      version: 3,
      move_unfinished_to: "backlog",
    });

    await user.click(
      within((await screen.findByText("迭代 1")).closest("li")!).getByRole(
        "button",
        { name: "迭代" },
      ),
    );
    await user.click(await screen.findByRole("menuitem", { name: "删除迭代" }));
    await user.click(await screen.findByRole("button", { name: "确认" }));
    await waitFor(() =>
      expect(server.find("DELETE", "/iterations/i1")).toHaveLength(1),
    );
    expect(
      server.find("DELETE", "/iterations/i1")[0]!.query.get("version"),
    ).toBe("3");
  });
});

describe("迭代详情", () => {
  it("规划：勾选待规划任务加入迭代", async () => {
    const user = userEvent.setup();
    const server = renderAt(`${list}/i1`);
    await user.click(await screen.findByRole("button", { name: "规划" }));
    await user.click(
      await screen.findByRole("checkbox", { name: /待规划任务/ }),
    );
    await user.click(screen.getByRole("button", { name: "加入迭代" }));
    await waitFor(() =>
      expect(server.find("POST", "/iterations/i1/tasks")).toHaveLength(1),
    );
    expect(server.find("POST", "/iterations/i1/tasks")[0]!.body).toEqual({
      task_ids: ["task-9"],
      action: "add",
    });
  });

  it("任务列表按迭代过滤", async () => {
    const server = renderAt(`${list}/i1`);
    await screen.findByText("迭代 1");
    await waitFor(() =>
      expect(
        server.calls.some(
          (c) =>
            c.path === `${sp}/tasks` && c.query.get("iteration_id") === "i1",
        ),
      ).toBe(true),
    );
  });
});

describe("迭代列表 · 自动模式", () => {
  const automatic = settings({ mode: "automatic" });

  it("无进行中且有计划中的迭代时显示冷却提示", async () => {
    renderAt(list, {
      settings: automatic,
      items: [iteration("i1", "planned")],
    });
    expect(await screen.findByText(/冷却期，下个迭代/)).toBeInTheDocument();
  });

  it("有进行中的迭代时没有提示", async () => {
    renderAt(list, { settings: automatic });
    await screen.findByText("迭代 2");
    expect(screen.queryByText(/冷却期/)).toBeNull();
    expect(screen.queryByText("当前没有进行中的迭代")).toBeNull();
  });
});

describe("迭代详情面板", () => {
  it("⌘I 打开详情面板，添加文档链接", async () => {
    const user = userEvent.setup();
    const server = renderAt(`${list}/i1`);
    await screen.findByText("迭代 1");
    await user.keyboard("{Meta>}i{/Meta}");
    const panel = await screen.findByRole("dialog");
    await user.type(within(panel).getByLabelText("标题"), "设计稿");
    await user.type(
      within(panel).getByLabelText("链接地址"),
      "https://example.com/doc",
    );
    await user.click(within(panel).getByRole("button", { name: "添加链接" }));
    await waitFor(() =>
      expect(server.find("POST", "/iterations/i1/links")).toHaveLength(1),
    );
    expect(server.find("POST", "/iterations/i1/links")[0]!.body).toEqual({
      title: "设计稿",
      url: "https://example.com/doc",
    });
  });
});

describe("迭代设置页", () => {
  const page = `/o/${ORG}/settings/spaces/${SPACE}/iterations`;

  it("未启用时开关弹出模式选择，确认后带 version 启用", async () => {
    const user = userEvent.setup();
    const server = renderAt(page, { settings: settings({ enabled: false }) });
    await user.click(await screen.findByRole("switch", { name: "启用迭代" }));
    await user.click(await screen.findByRole("button", { name: "确认" }));
    await waitFor(() =>
      expect(server.find("PATCH", "/iteration-settings")).toHaveLength(1),
    );
    expect(server.find("PATCH", "/iteration-settings")[0]!.body).toEqual({
      enabled: true,
      mode: "automatic",
      version: 2,
    });
  });

  it("禁用需要确认，确认后带 version 保存", async () => {
    const user = userEvent.setup();
    const server = renderAt(page, {
      settings: settings({ mode: "automatic" }),
    });
    await user.click(await screen.findByRole("switch", { name: "启用迭代" }));
    await user.click(await screen.findByRole("button", { name: "确认" }));
    await waitFor(() =>
      expect(server.find("PATCH", "/iteration-settings")).toHaveLength(1),
    );
    expect(server.find("PATCH", "/iteration-settings")[0]!.body).toEqual({
      enabled: false,
      version: 2,
    });
  });
});

describe("设置页 · 模式切换", () => {
  it("自动 → 手动需确认，确认后带 version 保存 mode", async () => {
    const user = userEvent.setup();
    const server = renderAt(`/o/${ORG}/settings/spaces/${SPACE}/iterations`, {
      settings: settings({ mode: "automatic" }),
    });
    await user.click(await screen.findByRole("button", { name: "迭代模式" }));
    await user.click(await screen.findByRole("option", { name: "手动模式" }));
    expect(server.find("PATCH", "/iteration-settings")).toHaveLength(0);
    await user.click(await screen.findByRole("button", { name: "确认" }));
    await waitFor(() =>
      expect(server.find("PATCH", "/iteration-settings")).toHaveLength(1),
    );
    expect(server.find("PATCH", "/iteration-settings")[0]!.body).toEqual({
      mode: "manual",
      version: 2,
    });
  });
});

describe("任务的迭代字段", () => {
  const tasks = `/o/${ORG}/spaces/${SPACE}/tasks`;

  it.each([
    { enabled: true, shown: true },
    { enabled: false, shown: false },
  ])(
    "新建任务弹窗与详情属性：空间启用迭代=$enabled 时字段显示=$shown",
    async ({ enabled, shown }) => {
      const user = userEvent.setup();
      const server = renderAt(`${tasks}?task=OPEN-1`, {
        iterationsOff: !enabled,
        settings: settings({ enabled }),
      });
      // 详情属性
      const drawer = await screen.findByRole("dialog");
      await within(drawer).findByRole("textbox", { name: "任务标题" });
      await user.click(
        within(drawer).getByRole("button", { name: "更多属性" }),
      );
      const properties = await screen.findByRole("dialog", {
        name: "更多属性",
      });
      if (shown) await within(properties).findByText("迭代");
      expect(within(properties).queryByText("迭代") !== null).toBe(shown);
      await user.keyboard("{Escape}");
      await user.keyboard("{Escape}");

      // 新建任务弹窗
      await user.keyboard("c");
      const dialog = await screen.findByRole("dialog", { name: "新建任务" });
      await user.type(
        within(dialog).getByRole("textbox", { name: "标题" }),
        "排期任务",
      );
      const chip = within(dialog).queryByRole("button", { name: /迭代/ });
      expect(chip !== null).toBe(shown);
      if (chip) {
        await user.click(chip);
        await user.click(await screen.findByRole("option", { name: "迭代 1" }));
      }
      await user.click(within(dialog).getByRole("button", { name: "创 建" }));
      await waitFor(() =>
        expect(server.find("POST", `/spaces/${SPACE}/tasks`)).toHaveLength(1),
      );
      const body = server.find("POST", `/spaces/${SPACE}/tasks`)[0]!
        .body as Record<string, unknown>;
      expect(body.iteration_id).toBe(shown ? "i1" : undefined);
    },
  );
});

describe("任务列表的迭代名称与筛选项", () => {
  const tasks = `/o/${ORG}/spaces/${SPACE}/tasks`;

  it("列表显示迭代名称而不是 id，筛选项带当前 / 无迭代与迭代名称", async () => {
    const user = userEvent.setup();
    localStorage.setItem(
      displayStorageKey(ORG, `space:${SPACE}`),
      JSON.stringify({ layout: "list", properties: ["iteration"] }),
    );
    renderAt(tasks, {
      tasks: (call) =>
        call.path === `${sp}/tasks` &&
        call.query.getAll("status_id").includes("st1")
          ? json(200, {
              tasks: [
                makeTask(1, "st1", { title: "排期任务", iteration_id: "i1" }),
              ],
              next_cursor: "",
            })
          : undefined,
    });
    expect((await screen.findAllByText("迭代 1")).length).toBeGreaterThan(0);
    expect(screen.queryByText("i1")).toBeNull();

    await user.click(await screen.findByRole("button", { name: "筛选" }));
    await user.click(await screen.findByRole("menuitem", { name: "迭代" }));
    for (const name of ["当前迭代", "无迭代", "迭代 1", "迭代 2"])
      expect(await screen.findByRole("option", { name })).toBeTruthy();
  });
});
