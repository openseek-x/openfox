import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { App, createQueryClient } from "../App";
import {
  backend,
  json,
  ORG,
  type Call,
  type Override,
} from "../tasks/test-backend";
import {
  exportErrorMessage,
  exportPollInterval,
  selectionSummary,
} from "./export-model";

// 导出任务数据：选择空间与格式、发起、进度、下载、错误。

const o = `/v1/organizations/${ORG}`;
const TS = "2026-11-01T08:00:00Z";

const spaces = [
  { id: "s1", name: "产品研发", prefix: "PRD", task_count: 120 },
  { id: "s2", name: "运营", prefix: "OPS", task_count: 30 },
];

const exportJob = (id: string, extra: Record<string, unknown> = {}) => ({
  id,
  format: "csv",
  status: "SUCCEEDED",
  space_ids: ["s1"],
  space_count: 1,
  task_count: 120,
  file_name: "PRD-tasks-20261101.csv",
  size_bytes: 2048,
  error_code: "",
  error_message: "",
  operation_id: `op_${id}`,
  created_by: "u1",
  created_at: TS,
  finished_at: TS,
  expires_at: "2026-11-08T08:00:00Z",
  downloadable: true,
  ...extra,
});

interface Options {
  jobs?: unknown[];
  spaces?: unknown[];
  role?: string;
  onStart?: (call: Call) => Response | undefined;
}

function route(options: Options = {}): Override {
  return (call) => {
    if (call.path === "/v1/me/organizations" && options.role)
      return json(200, {
        organizations: [
          {
            id: ORG,
            slug: "lin",
            name: "林的团队",
            role: options.role,
            created_at: TS,
          },
        ],
      });
    if (call.path === `${o}/imports`)
      return json(200, {
        imports: [],
        next_cursor: "",
        has_active: false,
        can_manage: options.role !== "MEMBER",
        total: 0,
      });
    if (call.path === `${o}/export-spaces`)
      return json(200, { spaces: options.spaces ?? spaces });
    if (call.path === `${o}/exports` && call.method === "GET")
      return json(200, { exports: options.jobs ?? [] });
    if (call.path === `${o}/exports` && call.method === "POST")
      return (
        options.onStart?.(call) ??
        json(201, exportJob("new", { status: "QUEUED" }))
      );
    return undefined;
  };
}

function renderPage(override: Override) {
  const server = backend(override);
  const queryClient = createQueryClient();
  queryClient.setDefaultOptions({ queries: { retry: false } });
  render(
    <MemoryRouter initialEntries={[`/o/${ORG}/settings/import-export`]}>
      <App api={server.api} queryClient={queryClient} locale="zh-CN" />
    </MemoryRouter>,
  );
  return server;
}

beforeEach(() => {
  localStorage.setItem(`openfox:onboarding:${ORG}`, JSON.stringify({}));
});
afterEach(() => localStorage.clear());

async function openDialog() {
  const user = userEvent.setup();
  const card = await screen.findByTestId("export-tasks");
  await waitFor(() => expect(card).toBeEnabled());
  await user.click(card);
  const dialog = await screen.findByRole("dialog");
  return { user, dialog };
}

describe("纯函数", () => {
  it("已选合计与上限：超过 20 个空间或 100,000 个任务为超限", () => {
    const list = Array.from({ length: 21 }, (_, i) => ({
      id: `s${i}`,
      name: `S${i}`,
      prefix: `P${i}`,
      task_count: 1,
    }));
    const all = new Set(list.map((s) => s.id));
    expect(selectionSummary(list, all)).toEqual({
      spaces: 21,
      tasks: 21,
      over: true,
    });
    expect(selectionSummary(list, new Set(["s1", "s2"])).over).toBe(false);
    const big = [{ id: "b", name: "B", prefix: "B", task_count: 100_001 }];
    expect(selectionSummary(big, new Set(["b"])).over).toBe(true);
    expect(selectionSummary(big, new Set()).spaces).toBe(0);
  });

  it("轮询：有等待中或生成中的导出时 2 秒，否则不轮询", () => {
    expect(exportPollInterval(undefined)).toBe(false);
    expect(exportPollInterval([exportJob("a") as never])).toBe(false);
    expect(
      exportPollInterval([exportJob("a", { status: "RUNNING" }) as never]),
    ).toBe(2000);
  });

  it("发起失败的提示：未知错误用通用文案", () => {
    expect(exportErrorMessage(new Error("x"))).toEqual({
      key: "imports.export.errors.generic",
    });
  });
});

describe("导出任务数据对话框", () => {
  it("所有者默认全选，显示合计；选 JSON 后发起，带幂等键并关闭对话框", async () => {
    let started: Call | undefined;
    renderPage(
      route({
        onStart: (call) => {
          started = call;
          return undefined;
        },
      }),
    );
    const { user, dialog } = await openDialog();
    await waitFor(() =>
      expect(
        within(dialog).getByText("已选 2 个空间 · 150 个任务"),
      ).toBeVisible(),
    );
    expect(
      within(dialog).getByRole("checkbox", { name: /全选/ }),
    ).toBeChecked();
    await user.click(within(dialog).getByRole("radio", { name: /JSON/ }));
    await user.click(within(dialog).getByRole("button", { name: "开始导出" }));
    await waitFor(() => expect(started).toBeDefined());
    expect(started?.body).toEqual({ space_ids: ["s1", "s2"], format: "json" });
    expect(started?.headers.get("Idempotency-Key")).toMatch(/^export-/);
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("取消勾选一个空间后只导出另一个；全不选时不能开始", async () => {
    let started: Call | undefined;
    renderPage(
      route({
        onStart: (call) => {
          started = call;
          return undefined;
        },
      }),
    );
    const { user, dialog } = await openDialog();
    const start = within(dialog).getByRole("button", { name: "开始导出" });
    await waitFor(() => expect(start).toBeEnabled());
    await user.click(
      within(dialog).getByRole("checkbox", { name: /产品研发/ }),
    );
    expect(within(dialog).getByText("已选 1 个空间 · 30 个任务")).toBeVisible();
    await user.click(start);
    await waitFor(() => expect(started).toBeDefined());
    expect(started?.body).toEqual({ space_ids: ["s2"], format: "csv" });
  });

  it("成员默认不选任何空间，开始按钮禁用；勾选后可用", async () => {
    renderPage(route({ role: "MEMBER" }));
    const { user, dialog } = await openDialog();
    await within(dialog).findByText("产品研发");
    const start = within(dialog).getByRole("button", { name: "开始导出" });
    expect(start).toBeDisabled();
    expect(within(dialog).getByText("已选 0 个空间 · 0 个任务")).toBeVisible();
    await user.click(within(dialog).getByRole("checkbox", { name: /运营/ }));
    expect(start).toBeEnabled();
  });

  it("超出单次上限：提示缩小范围且不能开始", async () => {
    renderPage(
      route({
        spaces: [
          { id: "big", name: "大空间", prefix: "BIG", task_count: 100_001 },
        ],
      }),
    );
    const { dialog } = await openDialog();
    expect(await within(dialog).findByRole("alert")).toHaveTextContent(
      "超出单次上限（最多 20 个空间、100,000 个任务），请缩小范围。",
    );
    expect(
      within(dialog).getByRole("button", { name: "开始导出" }),
    ).toBeDisabled();
  });

  it("后端 422 超限：显示后端文案，对话框保持打开", async () => {
    renderPage(
      route({
        onStart: () =>
          json(422, {
            error: {
              code: "EXPORT_TOO_LARGE",
              message: "export exceeds the limits; narrow the selection",
              request_id: "r",
            },
          }),
      }),
    );
    const { user, dialog } = await openDialog();
    const start = within(dialog).getByRole("button", { name: "开始导出" });
    await waitFor(() => expect(start).toBeEnabled());
    await user.click(start);
    expect(await within(dialog).findByRole("alert")).toHaveTextContent(
      "export exceeds the limits",
    );
    expect(screen.getByRole("dialog")).toBeVisible();
  });

  it("403：显示没有导出权限", async () => {
    renderPage(
      route({
        onStart: () =>
          json(403, {
            error: { code: "FORBIDDEN", message: "x", request_id: "r" },
          }),
      }),
    );
    const { user, dialog } = await openDialog();
    const start = within(dialog).getByRole("button", { name: "开始导出" });
    await waitFor(() => expect(start).toBeEnabled());
    await user.click(start);
    expect(await within(dialog).findByText("你没有导出权限")).toBeVisible();
  });

  it("没有可导出的空间时给出说明", async () => {
    renderPage(route({ spaces: [] }));
    const { dialog } = await openDialog();
    expect(await within(dialog).findByText("没有可导出的空间")).toBeVisible();
  });

  it("访客不能打开导出", async () => {
    renderPage(route({ role: "GUEST" }));
    const card = await screen.findByTestId("export-tasks");
    await waitFor(() => expect(card).toBeDisabled());
  });
});

describe("导出记录", () => {
  it("已完成的有下载链接，生成中显示进度状态，失败显示原因与操作编号，过期没有下载", async () => {
    renderPage(
      route({
        jobs: [
          exportJob("done"),
          exportJob("run", {
            status: "RUNNING",
            downloadable: false,
            file_name: "",
            finished_at: null,
            expires_at: null,
          }),
          exportJob("bad", {
            status: "FAILED",
            downloadable: false,
            file_name: "",
            error_code: "EXPORT_TOO_LARGE",
            error_message: "导出文件超过大小上限，请缩小范围后重试。",
          }),
          exportJob("old", { status: "EXPIRED", downloadable: false }),
        ],
      }),
    );
    expect(await screen.findByText("导出记录")).toBeVisible();
    const done = screen.getByTestId("export-done");
    expect(within(done).getByText("已完成")).toBeVisible();
    const link = within(done).getByRole("link", {
      name: "下载 PRD-tasks-20261101.csv",
    });
    expect(link.getAttribute("href")).toContain(`${o}/exports/done/download`);
    expect(link).toHaveAttribute("download");
    expect(
      within(screen.getByTestId("export-run")).getByText("生成中"),
    ).toBeVisible();
    expect(
      within(screen.getByTestId("export-run")).queryByRole("link"),
    ).toBeNull();
    const bad = screen.getByTestId("export-bad");
    expect(within(bad).getByText("失败")).toBeVisible();
    expect(
      within(bad).getByText(/导出失败：导出文件超过大小上限/),
    ).toBeVisible();
    expect(within(bad).getByText("op_bad")).toBeVisible();
    const old = screen.getByTestId("export-old");
    expect(within(old).getByText("已过期")).toBeVisible();
    expect(within(old).queryByRole("link")).toBeNull();
  });

  it("没有记录时不显示导出记录", async () => {
    renderPage(route());
    await screen.findByTestId("export-tasks");
    expect(screen.queryByText("导出记录")).toBeNull();
  });
});
