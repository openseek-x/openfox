import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { App, createQueryClient } from "../App";
import {
  backend,
  json,
  ORG,
  SPACE,
  type Override,
} from "../tasks/test-backend";

// 视图：列表分组、新建（筛选与显示设置写入请求体）、可见性、编辑删除、置顶与排序、图标三种来源、打开视图。

const o = `/v1/organizations/${ORG}`;
const sp = `${o}/spaces/${SPACE}`;

const space = (views = true) => ({
  id: SPACE,
  prefix: "OPEN",
  name: "默认空间",
  visibility: "shared",
  version: 2,
  created_at: "2026-10-01T00:00:00Z",
  updated_at: "2026-10-01T00:00:00Z",
  icon: null,
  purpose: "software_development",
  features: { views, projects: false, iterations: false, triage: false },
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
});

const view = (id: string, extra: Record<string, unknown> = {}) => ({
  id,
  space_id: SPACE,
  name: id === "v1" ? "我的待办" : "团队看板",
  description: "",
  icon_kind: null,
  icon: null,
  icon_url: null,
  visibility: id === "v1" ? "personal" : "space",
  filters: id === "v1" ? { priority: ["HIGH"], status_id: ["st1"] } : {},
  display: { layout: "list" },
  created_by: { id: "u1", email: "lin@openfox.dev" },
  created_at: "2026-10-20T00:00:00Z",
  updated_at: "2026-10-20T00:00:00Z",
  pinned: false,
  position: null,
  member_view_mode: null,
  can_edit: true,
  version: 3,
  ...extra,
});

function renderAt(
  path: string,
  opts: { views?: object[]; viewsOff?: boolean; extra?: Override } = {},
) {
  const items = opts.views ?? [view("v1"), view("v2")];
  const override: Override = (call) => {
    const custom = opts.extra?.(call);
    if (custom) return custom;
    if (call.path === sp) return json(200, space(!opts.viewsOff));
    if (call.path === `${o}/spaces`)
      return json(200, { spaces: [space(!opts.viewsOff)] });
    if (call.path === `${sp}/views` && call.method === "GET")
      return json(200, { views: items });
    if (call.path === `${sp}/views` && call.method === "POST")
      return json(201, view("v9"));
    const one = items.find(
      (item) => call.path === `${o}/views/${(item as { id: string }).id}`,
    );
    if (one && call.method === "GET") return json(200, one);
    if (call.path === `${o}/icons` && call.method === "POST")
      return json(201, {
        id: "ic1",
        url: `/api${o}/icons/ic1`,
        content_type: "image/png",
        size_bytes: 4,
      });
    if (call.path.startsWith(`${o}/views/`) && call.method === "PATCH")
      return json(200, items[0]);
    if (
      call.path.startsWith(`${o}/views/`) ||
      call.path.endsWith("/reorder-mine")
    )
      return new Response(null, { status: 204 });
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

beforeEach(() => {
  localStorage.setItem(`openfox:onboarding:${ORG}`, JSON.stringify({}));
});
afterEach(() => localStorage.clear());

const list = `/o/${ORG}/spaces/${SPACE}/views`;

describe("视图列表", () => {
  it("空态与未启用", async () => {
    renderAt(list, { views: [] });
    expect(await screen.findByText("还没有视图")).toBeTruthy();
  });

  it("未启用视图的空间不显示列表", async () => {
    renderAt(list, { viewsOff: true });
    expect(await screen.findByText("此空间未启用视图")).toBeTruthy();
  });

  it("按私人 / 共享分组，行显示筛选摘要与创建者", async () => {
    renderAt(list);
    expect(await screen.findByText("我的待办")).toBeTruthy();
    expect(screen.getByRole("heading", { name: "私人" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "共享" })).toBeTruthy();
    expect(screen.getByText("2 个筛选条件")).toBeTruthy();
    expect(screen.getByText("无筛选条件")).toBeTruthy();
    expect(screen.getAllByText("lin@openfox.dev").length).toBe(2);
  });

  it("置顶写 pin-mine；删除确认后带 version 删除", async () => {
    const user = userEvent.setup();
    const server = renderAt(list);
    await user.click(
      await screen.findByRole("button", { name: "我的待办 更多操作" }),
    );
    await user.click(await screen.findByRole("menuitem", { name: "置顶" }));
    await waitFor(() =>
      expect(server.find("PUT", "/views/v1/pin-mine")).toHaveLength(1),
    );
    expect(server.find("PUT", "/views/v1/pin-mine")[0]!.body).toEqual({
      pinned: true,
    });

    await user.click(screen.getByRole("button", { name: "团队看板 更多操作" }));
    await user.click(await screen.findByRole("menuitem", { name: "删除" }));
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: "删除" }));
    await waitFor(() =>
      expect(server.find("DELETE", "/views/v2")).toHaveLength(1),
    );
    expect(server.find("DELETE", "/views/v2")[0]!.query.get("version")).toBe(
      "3",
    );
  });

  it("不能编辑的视图菜单里编辑与删除禁用", async () => {
    const user = userEvent.setup();
    renderAt(list, { views: [view("v2", { can_edit: false })] });
    await user.click(
      await screen.findByRole("button", { name: "团队看板 更多操作" }),
    );
    expect(
      (await screen.findByRole("menuitem", { name: "编辑" })).getAttribute(
        "aria-disabled",
      ),
    ).toBe("true");
  });
});

describe("新建与编辑视图", () => {
  it("筛选与显示设置写进请求体，可见性选共享", async () => {
    const user = userEvent.setup();
    const server = renderAt(list, { views: [] });
    await user.click(
      (await screen.findAllByRole("button", { name: "新建视图" }))[0]!,
    );
    await user.type(screen.getByRole("textbox", { name: "名称" }), "高优先级");
    await user.click(screen.getByRole("button", { name: "可见性" }));
    await user.click(await screen.findByRole("option", { name: "共享" }));
    await user.click(await screen.findByRole("button", { name: "筛选" }));
    await user.click(await screen.findByRole("menuitem", { name: "优先级" }));
    await user.click(await screen.findByRole("option", { name: "高" }));
    await user.keyboard("{Escape}");
    await user.click(screen.getByRole("button", { name: "显示设置" }));
    await user.click(await screen.findByRole("radio", { name: "列表" }));
    await user.keyboard("{Escape}");
    await user.click(screen.getByRole("button", { name: "创建视图" }));
    await waitFor(() => expect(server.find("POST", "/views")).toHaveLength(1));
    const body = server.find("POST", "/views")[0]!.body as Record<
      string,
      unknown
    >;
    expect(body).toMatchObject({
      name: "高优先级",
      visibility: "space",
      filters: { priority: ["HIGH"] },
      display: { layout: "list" },
    });
  });

  it("编辑带 version，可见性没改就不带；改了图标整体替换", async () => {
    const user = userEvent.setup();
    const server = renderAt(list);
    await user.click(
      await screen.findByRole("button", { name: "我的待办 更多操作" }),
    );
    await user.click(await screen.findByRole("menuitem", { name: "编辑" }));
    // 已有筛选还原成了胶囊
    expect(await screen.findByRole("group", { name: "优先级" })).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "选择图标" }));
    await user.click(await screen.findByRole("tab", { name: "表情" }));
    await user.click(await screen.findByRole("button", { name: "🚀" }));
    await user.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() =>
      expect(server.find("PATCH", "/views/v1")).toHaveLength(1),
    );
    const body = server.find("PATCH", "/views/v1")[0]!.body as Record<
      string,
      unknown
    >;
    expect(body).toMatchObject({
      version: 3,
      icon_kind: "emoji",
      icon: "🚀",
      filters: { priority: ["HIGH"], status_id: ["st1"] },
    });
    expect("visibility" in body).toBe(false);
  });
});

describe("图标来源", () => {
  const openPicker = async () => {
    const user = userEvent.setup();
    const server = renderAt(list, { views: [] });
    await user.click(
      (await screen.findAllByRole("button", { name: "新建视图" }))[0]!,
    );
    await user.type(screen.getByRole("textbox", { name: "名称" }), "带图标");
    await user.click(screen.getByRole("button", { name: "选择图标" }));
    return { user, server };
  };
  const created = async (server: ReturnType<typeof backend>) => {
    await waitFor(() => expect(server.find("POST", "/views")).toHaveLength(1));
    return server.find("POST", "/views")[0]!.body as Record<string, unknown>;
  };

  it("内置图标、表情", async () => {
    const { user, server } = await openPicker();
    await user.click(await screen.findByRole("button", { name: "rocket" }));
    await user.click(screen.getByRole("button", { name: "创建视图" }));
    expect(await created(server)).toMatchObject({
      icon_kind: "icon",
      icon: "rocket",
    });
  });

  it("上传：成功后带 upload 与图标 id", async () => {
    const { user, server } = await openPicker();
    await user.click(await screen.findByRole("tab", { name: "上传" }));
    const file = new File([new Uint8Array([137, 80, 78, 71])], "a.png", {
      type: "image/png",
    });
    await user.upload(await screen.findByLabelText("上传"), file);
    await waitFor(() => expect(server.find("POST", "/icons")).toHaveLength(1));
    await user.click(screen.getByRole("button", { name: "创建视图" }));
    expect(await created(server)).toMatchObject({
      icon_kind: "upload",
      icon: "ic1",
    });
  });
});

describe("打开视图", () => {
  it("任务查询带 view_id；切换布局写 view-mode-mine", async () => {
    const user = userEvent.setup();
    const server = renderAt(`${list}/v1`);
    await screen.findByRole("heading", { name: "我的待办" });
    await waitFor(() =>
      expect(
        server.calls.some(
          (c) =>
            c.path === `${sp}/tasks/counts` && c.query.get("view_id") === "v1",
        ),
      ).toBe(true),
    );
    // 本人展示方式覆盖视图自带的布局
    await user.click(await screen.findByRole("button", { name: "显示设置" }));
    await user.click(await screen.findByRole("radio", { name: "表格" }));
    await waitFor(() =>
      expect(server.find("PUT", "/views/v1/view-mode-mine")).toHaveLength(1),
    );
    expect(server.find("PUT", "/views/v1/view-mode-mine")[0]!.body).toEqual({
      view_mode: "table",
    });
  });

  it("他人的私人视图 404", async () => {
    renderAt(`${list}/zz`, {
      extra: (call) =>
        call.path === `${o}/views/zz`
          ? json(404, {
              error: { code: "NOT_FOUND", message: "x", request_id: "r" },
            })
          : undefined,
    });
    expect(await screen.findByText("没有找到这个视图")).toBeTruthy();
  });
});

describe("侧栏置顶视图", () => {
  it("只列出本人置顶的视图，按 position 顺序", async () => {
    renderAt(`/o/${ORG}/spaces/${SPACE}/tasks`, {
      // 服务端已按本人 position 排好序
      views: [
        view("v2", { pinned: true, position: 0 }),
        view("v1", { pinned: true, position: 1 }),
        view("v3", { name: "没置顶" }),
      ],
    });
    const links = await screen.findAllByRole("link", {
      name: /我的待办|团队看板|没置顶/,
    });
    expect(links.map((a) => a.textContent)).toEqual(["团队看板", "我的待办"]);
  });
});
