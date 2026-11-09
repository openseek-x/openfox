import { render, screen, waitFor, within } from "@testing-library/react";
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
} from "../tasks/test-backend";
import { displayStorageKey } from "../tasks/model";

// 专项：列表、新建、删除确认、详情（进度分布、加移空间、设主空间）与任务的专项字段。

const o = `/v1/organizations/${ORG}`;
const sp = `${o}/spaces/${SPACE}`;

const space = (id: string, projects = true) => ({
  id,
  prefix: id === SPACE ? "OPEN" : "SEC",
  name: id === SPACE ? "默认空间" : "第二空间",
  visibility: "shared",
  version: 2,
  created_at: "2026-10-01T00:00:00Z",
  updated_at: "2026-10-01T00:00:00Z",
  icon: null,
  purpose: "software_development",
  features: { views: true, projects, iterations: false, triage: false },
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

const project = (extra: Record<string, unknown> = {}) => ({
  id: "p1",
  name: "官网改版",
  icon: "🚀",
  description: "",
  status: "in_progress",
  priority: "HIGH",
  lead: { id: "u1", email: "lin@openfox.dev" },
  progress: { done: 3, total: 5 },
  spaces: [{ id: SPACE, name: "默认空间", prefix: "OPEN", is_primary: true }],
  labels: [],
  created_at: "2026-10-20T00:00:00Z",
  updated_at: "2026-10-20T00:00:00Z",
  version: 4,
  ...extra,
});

const distribution = {
  assignees: [
    { key: "u1", label: "lin@openfox.dev", done: 2, total: 3 },
    { key: "", label: "", done: 1, total: 2 },
  ],
  iterations: [{ key: "i1", label: "迭代 1", done: 3, total: 5 }],
};

function renderAt(
  path: string,
  opts: { projects?: object[]; projectsOff?: boolean; extra?: Override } = {},
) {
  const items = opts.projects ?? [project()];
  const override: Override = (call) => {
    const custom = opts.extra?.(call);
    if (custom) return custom;
    if (call.path === sp) return json(200, space(SPACE, !opts.projectsOff));
    if (call.path === `${o}/spaces`)
      return json(200, {
        spaces: [space(SPACE, !opts.projectsOff), space("s2")],
      });
    if (call.path === `${o}/projects` && call.method === "GET")
      return json(200, { projects: items, next_cursor: "" });
    if (call.path === `${o}/projects` && call.method === "POST")
      return json(201, project({ id: "p9", name: "新专项" }));
    if (call.path === `${o}/projects/p1/delete-impact`)
      return json(200, { task_count: 7 });
    if (call.path === `${o}/projects/p1` && call.method === "GET")
      return json(200, { ...items[0], distribution });
    if (call.path === `${o}/projects/p1` && call.method === "PATCH")
      return json(200, project());
    if (call.path.startsWith(`${o}/projects/p1`) && call.method !== "GET")
      return new Response(null, { status: 204 });
    if (call.path === `${o}/projects/p9`)
      return json(200, project({ id: "p9", name: "新专项", distribution }));
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

const list = `/o/${ORG}/spaces/${SPACE}/projects`;

describe("专项列表", () => {
  it("空态显示新建入口；未启用专项时提示启用", async () => {
    renderAt(list, { projects: [] });
    expect(await screen.findByText("还没有专项")).toBeTruthy();
  });

  it("未启用专项的空间不显示列表", async () => {
    renderAt(list, { projectsOff: true });
    expect(await screen.findByText("此空间未启用专项")).toBeTruthy();
  });

  it("行显示状态、进度与负责人；新建带名称、表情与默认状态后进入详情", async () => {
    const user = userEvent.setup();
    const server = renderAt(list);
    expect(await screen.findByText("官网改版")).toBeTruthy();
    expect(screen.getByText("3/5")).toBeTruthy();
    expect(screen.getByText("进行中")).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "新建专项" }));
    const dialog = await screen.findByRole("dialog", { name: "新建专项" });
    await user.type(
      within(dialog).getByRole("textbox", { name: "专项名称" }),
      "新专项",
    );
    await user.click(within(dialog).getByRole("button", { name: "表情图标" }));
    await user.click(await screen.findByRole("button", { name: "🚀" }));
    await user.click(within(dialog).getByRole("button", { name: "保存" }));
    await waitFor(() =>
      expect(server.find("POST", "/projects")).toHaveLength(1),
    );
    expect(server.find("POST", "/projects")[0]!.body).toEqual({
      space_id: SPACE,
      name: "新专项",
      status: "planned",
      priority: "NONE",
      icon: "🚀",
    });
    // 创建后进入详情
    expect(await screen.findByRole("tab", { name: "概览" })).toBeTruthy();
  });

  it("删除确认显示受影响任务数，确认后带 version 删除", async () => {
    const user = userEvent.setup();
    const server = renderAt(list);
    await user.click(
      await screen.findByRole("button", { name: "官网改版 更多操作" }),
    );
    await user.click(await screen.findByRole("menuitem", { name: "删除专项" }));
    const dialog = await screen.findByRole("dialog");
    expect(await within(dialog).findByText(/7 个任务/)).toBeTruthy();
    await user.click(within(dialog).getByRole("button", { name: "删除" }));
    await waitFor(() =>
      expect(server.find("DELETE", "/projects/p1")).toHaveLength(1),
    );
    expect(server.find("DELETE", "/projects/p1")[0]!.query.get("version")).toBe(
      "4",
    );
  });
});

describe("专项详情", () => {
  const detail = `${list}/p1`;

  it("概览显示进度与分布，切换到迭代分布；修改状态带 version", async () => {
    const user = userEvent.setup();
    const server = renderAt(detail);
    expect(await screen.findByText("3/5")).toBeTruthy();
    expect(screen.getAllByText("lin@openfox.dev").length).toBeGreaterThan(0);
    expect(screen.getByText("未分配")).toBeTruthy();
    await user.click(screen.getByRole("radio", { name: "迭代" }));
    expect(await screen.findByText("迭代 1")).toBeTruthy();

    await user.click(screen.getByRole("button", { name: /^状态/ }));
    await user.click(await screen.findByRole("option", { name: "已完成" }));
    await waitFor(() =>
      expect(server.find("PATCH", "/projects/p1")).toHaveLength(1),
    );
    expect(server.find("PATCH", "/projects/p1")[0]!.body).toEqual({
      version: 4,
      status: "completed",
    });
  });

  it("添加空间、设为主空间、从专项移除", async () => {
    const user = userEvent.setup();
    const two = project({
      spaces: [
        { id: SPACE, name: "默认空间", prefix: "OPEN", is_primary: true },
        { id: "s2", name: "第二空间", prefix: "SEC", is_primary: false },
      ],
    });
    const server = renderAt(detail, { projects: [two] });
    await user.click(
      await screen.findByRole("button", { name: "第二空间 更多操作" }),
    );
    await user.click(
      await screen.findByRole("menuitem", { name: "设为主空间" }),
    );
    await waitFor(() =>
      expect(server.find("PUT", "/projects/p1/spaces/s2/primary")).toHaveLength(
        1,
      ),
    );
    await user.click(screen.getByRole("button", { name: "第二空间 更多操作" }));
    await user.click(
      await screen.findByRole("menuitem", { name: "从专项移除" }),
    );
    await waitFor(() =>
      expect(server.find("DELETE", "/projects/p1/spaces/s2")).toHaveLength(1),
    );
  });

  it("可添加的空间只列出未关联的空间", async () => {
    const user = userEvent.setup();
    const server = renderAt(detail);
    await user.click(await screen.findByRole("button", { name: "添加空间…" }));
    const dialog = await screen.findByRole("dialog", { name: "添加空间" });
    await user.click(await within(dialog).findByRole("radio"));
    await user.click(within(dialog).getByRole("button", { name: "添加空间" }));
    await waitFor(() =>
      expect(server.find("POST", "/projects/p1/spaces")).toHaveLength(1),
    );
    expect(server.find("POST", "/projects/p1/spaces")[0]!.body).toEqual({
      space_id: "s2",
    });
  });

  it("任务页签固定 project_id，人员页签按处理人类型筛选", async () => {
    const user = userEvent.setup();
    const server = renderAt(detail);
    await user.click(await screen.findByRole("tab", { name: "任务" }));
    await waitFor(() =>
      expect(
        server.calls.some(
          (c) =>
            c.path === `${sp}/tasks/counts` &&
            c.query.getAll("project_id").includes("p1"),
        ),
      ).toBe(true),
    );
    await user.click(await screen.findByRole("radio", { name: "人员" }));
    await waitFor(() =>
      expect(
        server.calls.some(
          (c) =>
            c.path === `${sp}/tasks/counts` &&
            c.query.getAll("assignee").some((v) => v.startsWith("user:")),
        ),
      ).toBe(true),
    );
  });
});

describe("任务的专项字段", () => {
  const tasks = `/o/${ORG}/spaces/${SPACE}/tasks`;

  it.each([
    { enabled: true, shown: true },
    { enabled: false, shown: false },
  ])(
    "新建弹窗与筛选：空间启用专项=$enabled 时字段显示=$shown",
    async ({ enabled, shown }) => {
      const user = userEvent.setup();
      const server = renderAt(tasks, { projectsOff: !enabled });
      await screen.findByRole("region", { name: "待处理 2" });
      await user.click(screen.getByRole("button", { name: "筛选" }));
      expect(
        (await screen.findAllByRole("menuitem")).some(
          (item) => item.textContent === "专项",
        ),
      ).toBe(shown);
      await user.keyboard("{Escape}");

      await user.keyboard("c");
      const dialog = await screen.findByRole("dialog", { name: "新建任务" });
      await user.type(
        within(dialog).getByRole("textbox", { name: "标题" }),
        "改版首页",
      );
      const chip = within(dialog).queryByRole("button", { name: /专项/ });
      expect(chip !== null).toBe(shown);
      if (chip) {
        await user.click(chip);
        await user.click(
          await screen.findByRole("option", { name: "官网改版" }),
        );
      }
      await user.click(within(dialog).getByRole("button", { name: "创 建" }));
      await waitFor(() =>
        expect(server.find("POST", `/spaces/${SPACE}/tasks`)).toHaveLength(1),
      );
      const body = server.find("POST", `/spaces/${SPACE}/tasks`)[0]!
        .body as Record<string, unknown>;
      expect(body.project_id).toBe(shown ? "p1" : undefined);
    },
  );

  it("列表显示专项名称；筛选选「无专项」写到 project_id=none", async () => {
    const user = userEvent.setup();
    localStorage.setItem(
      displayStorageKey(ORG, `space:${SPACE}`),
      JSON.stringify({ layout: "list", properties: ["project"] }),
    );
    const server = renderAt(tasks, {
      extra: (call) =>
        call.path === `${sp}/tasks` &&
        call.query.getAll("status_id").includes("st1")
          ? json(200, {
              tasks: [
                makeTask(1, "st1", { title: "改版首页", project_id: "p1" }),
              ],
              next_cursor: "",
            })
          : undefined,
    });
    expect((await screen.findAllByText("官网改版")).length).toBeGreaterThan(0);

    await user.click(await screen.findByRole("button", { name: "筛选" }));
    await user.click(await screen.findByRole("menuitem", { name: "专项" }));
    await user.click(await screen.findByRole("option", { name: "无专项" }));
    await user.keyboard("{Escape}");
    await waitFor(() =>
      expect(
        server.calls.some(
          (c) =>
            c.path === `${sp}/tasks/counts` &&
            c.query.getAll("project_id").includes("none"),
        ),
      ).toBe(true),
    );
  });
});
