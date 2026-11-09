import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, useLocation } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import type { DeletedTask } from "@openfox/api";

import { App, createQueryClient } from "../../App";
import {
  backend,
  json,
  makeTask,
  ORG,
  SPACE,
  type Override,
} from "../test-backend";

// 任务回收站「最近删除」：列表、空态、恢复确认与提示、409、普通成员说明、停用空间、删除提示里的「撤销」。

const o = `/v1/organizations/${ORG}`;
const trashPath = `/o/${ORG}/spaces/${SPACE}/trash`;
const RETIRED_TEXT = "此空间已停用，只能查看，不能恢复任务。";

function deleted(seq: number, extra: Partial<DeletedTask> = {}): DeletedTask {
  return {
    id: `task-${seq}`,
    space_id: SPACE,
    display_id: `open-${seq}`,
    title: `已删除任务 ${seq}`,
    type_id: "t1",
    type_name: "任务",
    type_icon: null,
    status_id: "st1",
    status_name: "待处理",
    status_category: "TODO",
    status_color: null,
    deleted_at: "2026-11-04T08:00:00Z",
    deleted_by: { id: "u2", name: "王五" },
    version: 3,
    ...extra,
  };
}

interface TrashOptions {
  scope?: "all" | "mine";
  pages?: DeletedTask[][];
  restore?: (taskId: string) => Response;
  retired?: boolean;
}

/** 回收站接口：按游标分页（cursor = 页码）；恢复默认成功。 */
function trashBackend(options: TrashOptions = {}): Override {
  const pages = options.pages ?? [[deleted(7), deleted(8)]];
  return (call) => {
    if (call.path === `${o}/spaces/${SPACE}/deleted-tasks`) {
      const index = Number(call.query.get("cursor") || "0");
      return json(200, {
        tasks: pages[index] ?? [],
        next_cursor: index + 1 < pages.length ? String(index + 1) : "",
        scope: options.scope ?? "all",
      });
    }
    const restore = /\/tasks\/(task-\d+)\/restore$/.exec(call.path);
    if (restore && call.method === "POST") {
      if (options.restore) return options.restore(restore[1]!);
      const seq = Number(restore[1]!.split("-")[1]);
      return json(200, makeTask(seq, "st1", { version: 4 }));
    }
    if (options.retired) {
      if (call.path === `${o}/spaces`) return json(200, { spaces: [] });
      if (call.path === `${o}/spaces/${SPACE}`)
        return json(200, {
          id: SPACE,
          prefix: "OPEN",
          name: "默认空间",
          visibility: "shared",
          version: 2,
          created_at: "2026-10-01T00:00:00Z",
          features: {
            views: false,
            projects: false,
            iterations: false,
            triage: false,
          },
          status: "retired",
          can_manage: true,
        });
    }
    return undefined;
  };
}

function Where() {
  const location = useLocation();
  return (
    <output aria-label="当前地址">{location.pathname + location.search}</output>
  );
}

function renderAt(path: string, override: Override) {
  const server = backend(override);
  const queryClient = createQueryClient();
  queryClient.setDefaultOptions({ queries: { retry: false } });
  render(
    <MemoryRouter initialEntries={[path]}>
      <App api={server.api} queryClient={queryClient} locale="zh-CN" />
      <Where />
    </MemoryRouter>,
  );
  return server;
}

const where = () => screen.getByRole("status", { name: "当前地址" });

beforeEach(() => {
  localStorage.setItem(`openfox:onboarding:${ORG}`, JSON.stringify({}));
});
afterEach(() => localStorage.clear());

describe("最近删除 · 列表", () => {
  it("管理员看到全部：编号、标题、类型、状态、删除人、删除时间与「恢复」", async () => {
    renderAt(trashPath, trashBackend());
    const table = await screen.findByRole("table", { name: "已删除的任务" });
    const rows = within(table).getAllByRole("row");
    expect(rows).toHaveLength(3);
    const first = rows[1]!;
    expect(first).toHaveTextContent("OPEN-7");
    expect(first).toHaveTextContent("已删除任务 7");
    expect(first).toHaveTextContent("任务");
    expect(first).toHaveTextContent("待处理");
    expect(first).toHaveTextContent("王五");
    expect(
      within(first).getByRole("button", { name: "恢复 OPEN-7" }),
    ).toBeEnabled();
    expect(
      screen.getByText("这里是此空间里已删除的任务。恢复后编号不变。"),
    ).toBeVisible();
    expect(screen.queryByText("你删除的任务")).toBeNull();
  });

  it("普通成员：标题为「你删除的任务」并有说明", async () => {
    renderAt(trashPath, trashBackend({ scope: "mine" }));
    expect(await screen.findByText("你删除的任务")).toBeVisible();
    expect(
      screen.getByText(
        "这里只列出你删除的任务。别人删除的任务只有删除人和空间管理员能看到并恢复。",
      ),
    ).toBeVisible();
  });

  it("空态", async () => {
    renderAt(trashPath, trashBackend({ pages: [[]] }));
    expect(await screen.findByText("没有最近删除的任务")).toBeVisible();
    expect(screen.queryByRole("table")).toBeNull();
  });

  it("加载更多：带上一页的游标", async () => {
    const user = userEvent.setup();
    const server = renderAt(
      trashPath,
      trashBackend({ pages: [[deleted(1)], [deleted(2)]] }),
    );
    const table = await screen.findByRole("table", { name: "已删除的任务" });
    expect(within(table).getAllByRole("row")).toHaveLength(2);
    await user.click(screen.getByRole("button", { name: "加载更多" }));
    await waitFor(() =>
      expect(within(table).getAllByRole("row")).toHaveLength(3),
    );
    expect(screen.queryByRole("button", { name: "加载更多" })).toBeNull();
    const calls = server.find("GET", "/deleted-tasks");
    expect(calls.at(-1)!.query.get("cursor")).toBe("1");
  });

  it("停用空间：恢复按钮禁用并提示原因，不发恢复请求", async () => {
    const user = userEvent.setup();
    const server = renderAt(trashPath, trashBackend({ retired: true }));
    expect(await screen.findByText(RETIRED_TEXT)).toBeVisible();
    const button = await screen.findByRole("button", { name: "恢复 OPEN-7" });
    await waitFor(() => expect(button).toBeDisabled());
    const tips = screen.getAllByLabelText(RETIRED_TEXT);
    await user.hover(tips[0]!);
    expect(await screen.findByRole("tooltip")).toHaveTextContent(RETIRED_TEXT);
    expect(server.find("POST", "/restore")).toHaveLength(0);
  });
});

describe("最近删除 · 恢复", () => {
  it("确认后带 version 与 Idempotency-Key 恢复，提示「已恢复」并可打开任务", async () => {
    const user = userEvent.setup();
    const server = renderAt(trashPath, trashBackend());
    await user.click(
      await screen.findByRole("button", { name: "恢复 OPEN-7" }),
    );
    const dialog = await screen.findByRole("dialog", { name: "恢复 OPEN-7？" });
    await user.click(within(dialog).getByRole("button", { name: "恢复" }));
    const toast = await screen.findByText("已恢复 OPEN-7");
    const call = server.find("POST", "/tasks/task-7/restore")[0]!;
    expect(call.body).toEqual({ version: 3 });
    expect(call.headers.get("Idempotency-Key")).toMatch(/^[0-9a-f]{32}$/);
    // 恢复后刷新列表
    await waitFor(() =>
      expect(server.find("GET", "/deleted-tasks").length).toBeGreaterThan(1),
    );
    await user.click(
      within(toast.closest<HTMLElement>("[role=status]")!).getByRole("button", {
        name: "打开",
      }),
    );
    expect(where()).toHaveTextContent(
      `/o/${ORG}/spaces/${SPACE}/tasks?task=OPEN-7`,
    );
  });

  it("取消确认不发请求", async () => {
    const user = userEvent.setup();
    const server = renderAt(trashPath, trashBackend());
    await user.click(
      await screen.findByRole("button", { name: "恢复 OPEN-8" }),
    );
    await user.click(await screen.findByRole("button", { name: "取消" }));
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(server.find("POST", "/restore")).toHaveLength(0);
  });

  it("409：提示已被恢复或修改并刷新列表", async () => {
    const user = userEvent.setup();
    const server = renderAt(
      trashPath,
      trashBackend({
        restore: () =>
          json(409, {
            error: {
              code: "VERSION_CONFLICT",
              message: "x",
              request_id: "r",
            },
          }),
      }),
    );
    await user.click(
      await screen.findByRole("button", { name: "恢复 OPEN-7" }),
    );
    await user.click(
      within(await screen.findByRole("dialog")).getByRole("button", {
        name: "恢复",
      }),
    );
    expect(
      await screen.findByText("这个任务已被恢复或修改，列表已刷新。"),
    ).toBeVisible();
    await waitFor(() =>
      expect(server.find("GET", "/deleted-tasks").length).toBeGreaterThan(1),
    );
    expect(screen.queryByRole("dialog")).toBeNull();
  });
});

describe("删除任务 · 撤销", () => {
  it("删除成功的提示带「撤销」，撤销用删除后的版本调恢复接口", async () => {
    const user = userEvent.setup();
    const server = renderAt(
      `/o/${ORG}/spaces/${SPACE}/tasks?task=OPEN-1`,
      (call) => {
        if (call.method === "DELETE" && call.path.endsWith("/tasks/task-1"))
          return new Response(null, { status: 204 });
        return trashBackend()(call);
      },
    );
    await user.click(await screen.findByRole("button", { name: "更多操作" }));
    await user.click(await screen.findByRole("menuitem", { name: "删除任务" }));
    const dialog = await screen.findByRole("dialog", { name: "删除任务？" });
    expect(dialog).toHaveTextContent("最近删除");
    await user.click(within(dialog).getByRole("button", { name: "删除任务" }));
    const toast = (await screen.findByText("任务已删除")).closest<HTMLElement>(
      "[role=status]",
    )!;
    const remove = server.find("DELETE", "/tasks/task-1")[0]!;
    expect(remove.query.get("version")).toBe("1");
    await user.click(within(toast).getByRole("button", { name: "撤销" }));
    expect(await screen.findByText("已恢复 OPEN-1")).toBeVisible();
    const restore = server.find("POST", "/tasks/task-1/restore")[0]!;
    expect(restore.body).toEqual({ version: 2 });
  });

  it("撤销时已被恢复：提示冲突", async () => {
    const user = userEvent.setup();
    renderAt(`/o/${ORG}/spaces/${SPACE}/tasks?task=OPEN-1`, (call) => {
      if (call.method === "DELETE" && call.path.endsWith("/tasks/task-1"))
        return new Response(null, { status: 204 });
      return trashBackend({
        restore: () =>
          json(409, {
            error: { code: "VERSION_CONFLICT", message: "x", request_id: "r" },
          }),
      })(call);
    });
    await user.click(await screen.findByRole("button", { name: "更多操作" }));
    await user.click(await screen.findByRole("menuitem", { name: "删除任务" }));
    await user.click(
      within(
        await screen.findByRole("dialog", { name: "删除任务？" }),
      ).getByRole("button", { name: "删除任务" }),
    );
    const toast = (await screen.findByText("任务已删除")).closest<HTMLElement>(
      "[role=status]",
    )!;
    await user.click(within(toast).getByRole("button", { name: "撤销" }));
    expect(
      await screen.findByText("这个任务已被恢复或修改，列表已刷新。"),
    ).toBeVisible();
  });
});

describe("最近删除 · 入口与动态", () => {
  it("命令面板「前往最近删除」跳到当前空间的回收站", async () => {
    const user = userEvent.setup();
    renderAt(`/o/${ORG}/spaces/${SPACE}/tasks`, trashBackend());
    await user.click(await screen.findByRole("button", { name: /^搜索/ }));
    await user.click(
      await screen.findByRole("option", { name: "前往最近删除" }),
    );
    expect(where()).toHaveTextContent(trashPath);
    expect(
      await screen.findByRole("table", { name: "已删除的任务" }),
    ).toBeVisible();
  });

  it("时间线显示 task.restored（原父任务已删除时注明）", async () => {
    const activity = (id: string, safe_diff: Record<string, unknown>) => ({
      type: "activity",
      created_at: "2026-11-04T09:00:00Z",
      activity: {
        id,
        task_id: "task-1",
        kind: "task.restored",
        safe_diff,
        actor_id: "u1",
        actor_name: "lin@openfox.dev",
        created_at: "2026-11-04T09:00:00Z",
      },
    });
    renderAt(`/o/${ORG}/spaces/${SPACE}/tasks?task=OPEN-1`, (call) => {
      if (call.path.endsWith("/tasks/task-1/timeline"))
        return json(200, {
          items: [
            activity("a1", {}),
            activity("a2", { detached_parent_id: "task-9" }),
          ],
          next_cursor: null,
        });
      return undefined;
    });
    expect(
      await screen.findByText(/lin@openfox\.dev 恢复了这个任务$/),
    ).toBeVisible();
    expect(
      screen.getByText(/恢复了这个任务（原父任务已删除，已改为顶层任务）/),
    ).toBeVisible();
  });
});
