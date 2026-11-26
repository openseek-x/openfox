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
  statuses,
  taskType,
  type Override,
} from "./test-backend";

// OPE-17 · D1 第一期：普通任务也能「标记为重复任务」（必须选目标）；
// D2：宽布局的属性侧栏可整栏收起，偏好记在本机。

const o = `/v1/organizations/${ORG}`;
const sp = `${o}/spaces/${SPACE}`;
const detailPath = `/o/${ORG}/spaces/${SPACE}/tasks?task=OPEN-1`;

/** 在默认工作流后追加一个重复类别的状态 st4。 */
const dupStatus = {
  ...statuses[2]!,
  id: "st4",
  name: "重复",
  category: "DUPLICATE",
};
const withDuplicate: Override = (call) => {
  if (call.path === `${o}/task-statuses`)
    return json(200, { statuses: [...statuses, dupStatus] });
  if (call.path === `${sp}/task-types`)
    return json(200, {
      task_types: [
        {
          ...taskType,
          statuses: [
            ...taskType.statuses,
            {
              status_id: "st4",
              name: "重复",
              category: "DUPLICATE",
              position: 3,
              default: false,
            },
          ],
        },
      ],
    });
  const mark = /\/tasks\/(task-\d+)\/duplicate$/.exec(call.path);
  if (mark && call.method === "POST") {
    const body = call.body as { version: number; duplicate_of_task_id: string };
    return json(200, {
      ...makeTask(Number(mark[1]!.split("-")[1]), "st1", {
        version: body.version + 1,
        status_id: "st4",
        status_name: "重复",
        status_category: "DUPLICATE",
        duplicate_of: {
          id: body.duplicate_of_task_id,
          display_id: "open-3",
          title: "任务标题 3",
        },
      }),
      description_reactions: [],
    });
  }
  return undefined;
};

function renderAt(path: string, ...overrides: Override[]) {
  const server = backend((call) => {
    for (const override of overrides) {
      const response = override(call);
      if (response) return response;
    }
    return undefined;
  });
  const queryClient = createQueryClient();
  queryClient.setDefaultOptions({ queries: { retry: false } });
  render(
    <MemoryRouter initialEntries={[path]}>
      <App api={server.api} queryClient={queryClient} locale="zh-CN" />
    </MemoryRouter>,
  );
  return server;
}

/** 在目标选择器里按编号选中 OPEN-3。 */
async function pickOpen3(user: ReturnType<typeof userEvent.setup>) {
  const search = await screen.findByRole("combobox", {
    name: "选择重复的目标任务",
  });
  await user.type(search, "OPEN-3");
  await screen.findByRole("button", { name: /OPEN-3/i });
  await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(1));
  await user.keyboard("{Enter}");
}

beforeEach(() => {
  localStorage.setItem(`openfox:onboarding:${ORG}`, JSON.stringify({}));
});
afterEach(() => localStorage.clear());

describe("OPE-17 · D1 标记为重复任务", () => {
  it("详情更多菜单：选目标后 POST /duplicate，不带 status_id", async () => {
    const user = userEvent.setup();
    const server = renderAt(detailPath, withDuplicate);
    await screen.findByRole("textbox", { name: "任务标题" });
    await user.click(screen.getByRole("button", { name: "更多操作" }));
    const menu = await screen.findByRole("menu", { name: "更多操作" });
    await user.click(
      within(menu).getByRole("menuitem", { name: "标记为重复任务" }),
    );
    await pickOpen3(user);
    await waitFor(() =>
      expect(server.find("POST", "/tasks/task-1/duplicate")).toHaveLength(1),
    );
    expect(server.find("POST", "/tasks/task-1/duplicate")[0]!.body).toEqual({
      version: 1,
      duplicate_of_task_id: "task-3",
    });
    expect(await screen.findByText("已标记为重复任务")).toBeInTheDocument();
    // 不经 PATCH 直接改到重复状态
    expect(
      server.calls.filter(
        (c) => c.method === "PATCH" && c.path.endsWith("/tasks/task-1"),
      ),
    ).toHaveLength(0);
  });

  it("工作流里没有重复状态时不显示该菜单项", async () => {
    const user = userEvent.setup();
    renderAt(detailPath);
    await screen.findByRole("textbox", { name: "任务标题" });
    await user.click(screen.getByRole("button", { name: "更多操作" }));
    const menu = await screen.findByRole("menu", { name: "更多操作" });
    expect(
      within(menu).queryByRole("menuitem", { name: "标记为重复任务" }),
    ).toBeNull();
  });

  it("属性栏状态选择器选「重复」：弹出目标选择器、不发 PATCH，请求带 status_id", async () => {
    const user = userEvent.setup();
    const server = renderAt(detailPath, withDuplicate);
    const aside = await screen.findByRole("complementary", { name: "属性" });
    const statusGroup = await within(aside).findByRole("group", {
      name: "状态",
    });
    await user.click(within(statusGroup).getByRole("button"));
    await user.click(await screen.findByRole("option", { name: "重复" }));
    await pickOpen3(user);
    await waitFor(() =>
      expect(server.find("POST", "/tasks/task-1/duplicate")).toHaveLength(1),
    );
    expect(server.find("POST", "/tasks/task-1/duplicate")[0]!.body).toEqual({
      version: 1,
      duplicate_of_task_id: "task-3",
      status_id: "st4",
    });
    expect(
      server.calls.filter(
        (c) => c.method === "PATCH" && c.path.endsWith("/tasks/task-1"),
      ),
    ).toHaveLength(0);
  });

  it("取消目标选择器：任务状态不变，不发任何写请求", async () => {
    const user = userEvent.setup();
    const server = renderAt(detailPath, withDuplicate);
    const aside = await screen.findByRole("complementary", { name: "属性" });
    const statusGroup = await within(aside).findByRole("group", {
      name: "状态",
    });
    await user.click(within(statusGroup).getByRole("button"));
    await user.click(await screen.findByRole("option", { name: "重复" }));
    await screen.findByRole("combobox", { name: "选择重复的目标任务" });
    await user.keyboard("{Escape}");
    await waitFor(() =>
      expect(
        screen.queryByRole("combobox", { name: "选择重复的目标任务" }),
      ).toBeNull(),
    );
    expect(
      server.calls.filter((c) => c.method === "PATCH" || c.method === "POST"),
    ).toHaveLength(0);
  });

  it("批量操作栏的状态候选里没有重复类别", async () => {
    localStorage.setItem(
      `openfox:tasks-display:${ORG}:space:${SPACE}`,
      JSON.stringify({ layout: "table" }),
    );
    const user = userEvent.setup();
    renderAt(`/o/${ORG}/spaces/${SPACE}/tasks`, withDuplicate);
    const table = await screen.findByRole("table", { name: "任务表格" });
    await user.click(
      await within(table).findByRole("checkbox", { name: "选择 OPEN-1" }),
    );
    const bar = await screen.findByRole("toolbar", { name: "批量操作" });
    await user.click(within(bar).getByRole("button", { name: "修改状态" }));
    const menu = await screen.findByRole("listbox", { name: "修改状态" });
    const names = within(menu)
      .getAllByRole("option")
      .map((option) => option.textContent);
    expect(names).toEqual(["待处理", "进行中", "已完成"]);
  });
});

describe("OPE-17 · D2 属性侧栏收起", () => {
  it("收起后属性移到标题下方并记到本机；展开后恢复侧栏", async () => {
    const user = userEvent.setup();
    renderAt(detailPath);
    const aside = await screen.findByRole("complementary", { name: "属性" });
    await user.click(
      within(aside).getByRole("button", { name: "收起属性侧栏" }),
    );
    await waitFor(() =>
      expect(screen.queryByRole("complementary", { name: "属性" })).toBeNull(),
    );
    // 窄布局同款属性胶囊行：带「状态: 当前值」按钮
    expect(
      screen.getByRole("button", { name: "状态: 待处理" }),
    ).toBeInTheDocument();
    expect(localStorage.getItem("openfox:task-properties-collapsed")).toBe("1");
    await user.click(screen.getByRole("button", { name: "展开属性侧栏" }));
    expect(
      await screen.findByRole("complementary", { name: "属性" }),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "状态: 待处理" })).toBeNull();
    expect(
      localStorage.getItem("openfox:task-properties-collapsed"),
    ).toBeNull();
  });

  it("本机已记为收起时，打开详情直接是收起状态", async () => {
    localStorage.setItem("openfox:task-properties-collapsed", "1");
    renderAt(detailPath);
    expect(
      await screen.findByRole("button", { name: "展开属性侧栏" }),
    ).toBeInTheDocument();
    expect(screen.queryByRole("complementary", { name: "属性" })).toBeNull();
  });
});
