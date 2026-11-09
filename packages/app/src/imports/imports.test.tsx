import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { App, createQueryClient } from "../App";
import {
  backend,
  json,
  makeTask,
  ORG,
  SPACE,
  type Call,
  type Override,
} from "../tasks/test-backend";
import { pollInterval, validateColumns } from "./model";

// 导入：来源卡片、记录列表、回滚确认框、向导、外部账号处理人。

const o = `/v1/organizations/${ORG}`;
const TS = "2026-10-20T08:00:00Z";

const job = (id: string, extra: Record<string, unknown> = {}) => ({
  id,
  space_id: SPACE,
  space_name: "默认空间",
  source: "jira_csv",
  source_label: "Jira CSV",
  file_name: `${id}.csv`,
  status: "SUCCEEDED",
  phase: "",
  operation_id: `op_${id}`,
  total_count: 10,
  processed_count: 10,
  created_count: 8,
  updated_count: 0,
  skipped_count: 2,
  failed_count: 0,
  warnings: [],
  error_code: "",
  error_message: "",
  created_by: "u1",
  created_by_email: "lin@openfox.dev",
  created_at: TS,
  started_at: TS,
  finished_at: TS,
  rolled_back_at: null,
  retry_at: null,
  version: 3,
  config_version: 2,
  rollback: { eligible: true, reason: "", deadline: TS },
  ...extra,
});

const space = {
  id: SPACE,
  prefix: "OPEN",
  name: "默认空间",
  visibility: "shared",
  version: 1,
  created_at: TS,
  features: { views: false, projects: false, iterations: false, triage: false },
  status: "active",
  my_role: "OWNER",
  can_manage: true,
};

const list =
  (imports: unknown[], extra: Record<string, unknown> = {}): Override =>
  (call) => {
    if (call.method === "GET" && call.path === `${o}/imports`)
      return json(200, {
        imports,
        next_cursor: "",
        has_active: false,
        can_manage: true,
        total: imports.length,
        ...extra,
      });
    if (call.path === `${o}/spaces`) return json(200, { spaces: [space] });
    return undefined;
  };

function compose(...routes: Override[]): Override {
  return (call) => {
    for (const route of routes) {
      const response = route(call);
      if (response) return response;
    }
    return undefined;
  };
}

function renderAt(path: string, override: Override) {
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

const page = `/o/${ORG}/settings/import-export`;

beforeEach(() => {
  localStorage.setItem(`openfox:onboarding:${ORG}`, JSON.stringify({}));
});
afterEach(() => localStorage.clear());

describe("纯函数", () => {
  it("轮询间隔：有执行中的导入 4 秒，否则 30 秒", () => {
    expect(pollInterval(true)).toBe(4000);
    expect(pollInterval(false)).toBe(30000);
    expect(pollInterval(undefined)).toBe(30000);
  });

  it("列映射校验：标题与源 ID 各有且只有一列，单值字段不能重复", () => {
    expect(
      validateColumns([{ index: 0, field: "ignore" }]).map((i) => i.code),
    ).toEqual(["title_column_missing", "source_id_column_missing"]);
    expect(
      validateColumns([
        { index: 0, field: "title" },
        { index: 1, field: "title" },
        { index: 2, field: "source_id" },
        { index: 3, field: "labels" },
        { index: 4, field: "labels" },
        { index: 5, field: "sprint" },
        { index: 6, field: "sprint" },
      ]).map((i) => i.code),
    ).toEqual(["duplicate_column_field"]);
    expect(
      validateColumns([
        { index: 0, field: "title" },
        { index: 1, field: "source_id" },
      ]),
    ).toEqual([]);
  });
});

describe("导入与导出页", () => {
  it("来源卡片：已接入的来源可点，即将推出的来源禁用；导出的任务数据可点；没有记录时不显示历史", async () => {
    renderAt(page, list([]));
    const jira = await screen.findByTestId("source-Jira");
    expect(jira.tagName).toBe("BUTTON");
    for (const name of ["TAPD", "禅道", "Teambition", "PingCode", "Shortcut"])
      expect(screen.getByTestId(`source-${name}`)).toBeEnabled();
    expect(screen.getByTestId("source-GitHub")).toBeEnabled();
    expect(screen.getByText("导入已有 Issue 并持续同步")).toBeVisible();
    const linear = screen.getByTestId("source-Linear");
    expect(linear).toHaveAttribute("aria-disabled", "true");
    expect(within(linear).getByText("即将推出")).toBeVisible();
    await waitFor(() =>
      expect(screen.getByTestId("export-tasks")).toBeEnabled(),
    );
    expect(screen.queryByText("导出记录")).toBeNull();
    await waitFor(() => expect(screen.queryByText(/历史记录/)).toBeNull());
  });

  it("点开 Jira：上传 CSV 与 API 凭证都可用", async () => {
    const user = userEvent.setup();
    renderAt(page, list([]));
    await user.click(await screen.findByTestId("source-Jira"));
    expect(screen.getByRole("button", { name: /上传 CSV/ })).toBeEnabled();
    expect(screen.getByRole("button", { name: /提供 API 凭证/ })).toBeEnabled();
  });

  it("GitHub 卡片打开已有 Issue 同步入口", async () => {
    const user = userEvent.setup();
    renderAt(
      page,
      compose(list([]), (call) => {
        if (call.method === "GET" && call.path === `${o}/integrations/github`)
          return json(200, {
            provider: {
              id: "github",
              name: "GitHub",
              category: "git",
              availability: "available",
              template: "B",
              capabilities: ["sync", "pr_link", "personal_account"],
              configured: true,
              bindable: true,
              summary: { connections: 1, status: "ok" },
            },
            can_manage: true,
            connections: [
              {
                id: "c1",
                provider: "github",
                scope: "ORGANIZATION",
                external_id: "openfox-dev",
                display_name: "openfox-dev",
                account_kind: "organization",
                status: "ACTIVE",
                created_by_email: "lin@openfox.dev",
                created_at: TS,
              },
            ],
            personal_connections: [],
            sync_links: [],
          });
        return undefined;
      }),
    );
    await user.click(await screen.findByTestId("source-GitHub"));
    const dialog = await screen.findByRole("dialog");
    expect(
      within(dialog).getByRole("checkbox", {
        name: "导入已有的未关闭 Issue",
      }),
    ).toBeChecked();
    expect(
      within(dialog).getByText(/最多回填 5,000 条未关闭的 Issue/),
    ).toBeVisible();
  });

  it("记录列表：状态文案、失败摘要与操作编号、加载更多带 cursor", async () => {
    const user = userEvent.setup();
    const clipboard = vi.fn(() => Promise.resolve());
    Object.defineProperty(navigator, "clipboard", {
      value: { writeText: clipboard },
      configurable: true,
    });
    const calls: Call[] = [];
    renderAt(
      page,
      compose((call) => {
        if (call.method === "GET" && call.path === `${o}/imports`) {
          calls.push(call);
          const cursor = call.query.get("cursor");
          return json(200, {
            imports: cursor
              ? [job("j6", { status: "CANCELLED" })]
              : [
                  job("j1"),
                  job("j2", { status: "RUNNING", processed_count: 3 }),
                  job("j3", {
                    status: "FAILED",
                    error_message: "文件损坏",
                    rollback: {
                      eligible: false,
                      reason: "nothing_created",
                      deadline: null,
                    },
                  }),
                  job("j4", {
                    status: "ROLLED_BACK",
                    rollback: {
                      eligible: false,
                      reason: "rolled_back",
                      deadline: null,
                    },
                  }),
                  job("j5", { status: "QUEUED" }),
                ],
            next_cursor: cursor ? "" : "c1",
            has_active: true,
            can_manage: true,
            total: 6,
          });
        }
        return undefined;
      }, list([])),
    );
    expect(await screen.findByText("历史记录 · 6 次")).toBeVisible();
    const row = (id: string) => screen.getByTestId(`import-${id}`);
    expect(within(row("j1")).getByText("已完成")).toBeVisible();
    expect(
      within(row("j1")).getByText(/j1\.csv · .* · 10\/10 项（新建 8 个）/),
    ).toBeVisible();
    expect(within(row("j2")).getByText("导入中")).toBeVisible();
    expect(within(row("j5")).getByText("导入中")).toBeVisible();
    expect(within(row("j4")).getByText("已回滚")).toBeVisible();
    expect(within(row("j3")).getByText("失败")).toBeVisible();
    expect(within(row("j3")).getByText(/文件损坏/)).toBeVisible();
    await user.click(
      within(row("j3")).getByRole("button", { name: /复制操作编号/ }),
    );
    expect(clipboard).toHaveBeenCalledWith("op_j3");

    expect(calls[0]!.query.get("limit")).toBe("5");
    await user.click(screen.getByRole("button", { name: "加载更多" }));
    await waitFor(() => expect(screen.getByTestId("import-j6")).toBeVisible());
    expect(calls.some((c) => c.query.get("cursor") === "c1")).toBe(true);
    expect(
      within(screen.getByTestId("import-j6")).getByText("已取消"),
    ).toBeVisible();
    expect(screen.queryByRole("button", { name: "加载更多" })).toBeNull();
  });

  it("行菜单：不可回滚时禁用并在底部显示原因", async () => {
    const user = userEvent.setup();
    renderAt(
      page,
      list([
        job("j1", {
          rollback: { eligible: false, reason: "expired", deadline: null },
        }),
      ]),
    );
    const row = await screen.findByTestId("import-j1");
    await user.click(within(row).getByRole("button", { name: "更多操作" }));
    expect(
      screen.getByRole("menuitem", { name: "删除导入内容" }),
    ).toHaveAttribute("aria-disabled", "true");
    expect(screen.getByText("已超过 7 天，不能再删除导入内容")).toBeVisible();
  });

  it("非管理员：能看记录，没有行菜单，不能开始导入并提示", async () => {
    renderAt(page, list([job("j1")], { can_manage: false }));
    const row = await screen.findByTestId("import-j1");
    expect(within(row).queryByRole("button", { name: "更多操作" })).toBeNull();
    expect(screen.getByTestId("source-Jira")).toBeDisabled();
    expect(screen.getByTestId("source-GitHub")).toBeDisabled();
    expect(
      screen.getByText("只有组织所有者和管理员可以管理连接、导入和同步操作"),
    ).toBeVisible();
  });
});

describe("回滚确认框", () => {
  it("展示预览、被改过的任务与清理说明，确认后调用回滚并刷新", async () => {
    const user = userEvent.setup();
    const rollbacks: Call[] = [];
    renderAt(
      page,
      compose(
        (call) => {
          if (call.path === `${o}/imports/j1/rollback`) {
            if (call.method === "POST") {
              rollbacks.push(call);
              return json(200, job("j1", { status: "ROLLED_BACK" }));
            }
            return json(200, {
              eligible: true,
              reason: "",
              deadline: TS,
              created_count: 8,
              modified_total: 52,
              modified: Array.from({ length: 50 }, (_, i) => ({
                task_id: `t${i}`,
                display_id: `OPEN-${i + 1}`,
                title: `被改的任务 ${i + 1}`,
                updated_at: TS,
              })),
              cleanup: { statuses: 1, labels: 2, iterations: 0 },
              space_created: false,
            });
          }
          return undefined;
        },
        list([job("j1")]),
      ),
    );
    const row = await screen.findByTestId("import-j1");
    await user.click(within(row).getByRole("button", { name: "更多操作" }));
    await user.click(screen.getByRole("menuitem", { name: "删除导入内容" }));
    const dialog = await screen.findByRole("dialog");
    expect(
      await within(dialog).findByText(
        "这会永久删除从 Jira CSV 导入到 默认空间 的 8 个条目（本次导入创建的全部内容）。导入后的手动编辑会丢失。不可撤销。",
      ),
    ).toBeVisible();
    expect(within(dialog).getByText("被改的任务 1")).toBeVisible();
    expect(within(dialog).getByText("等 2 条")).toBeVisible();
    expect(
      within(dialog).getByText(
        "将同时归档本次新建且不再使用的 1 个状态、2 个标签。",
      ),
    ).toBeVisible();
    await user.click(within(dialog).getByRole("button", { name: "回滚" }));
    await waitFor(() => expect(rollbacks).toHaveLength(1));
    expect(rollbacks[0]!.body).toEqual({ version: 3 });
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("RUN_ACTIVE 显示专用提示", async () => {
    const user = userEvent.setup();
    renderAt(
      page,
      compose(
        (call) => {
          if (call.path === `${o}/imports/j1/rollback`) {
            if (call.method === "POST")
              return json(409, {
                error: { code: "RUN_ACTIVE", message: "x", request_id: "r" },
              });
            return json(200, {
              eligible: true,
              reason: "",
              deadline: TS,
              created_count: 1,
              modified_total: 0,
              modified: [],
              cleanup: { statuses: 0, labels: 0, iterations: 0 },
              space_created: false,
            });
          }
          return undefined;
        },
        list([job("j1")]),
      ),
    );
    const row = await screen.findByTestId("import-j1");
    await user.click(within(row).getByRole("button", { name: "更多操作" }));
    await user.click(screen.getByRole("menuitem", { name: "删除导入内容" }));
    const dialog = await screen.findByRole("dialog");
    await user.click(
      await within(dialog).findByRole("button", { name: "回滚" }),
    );
    expect(
      await within(dialog).findByText(
        "回滚失败：有 Agent 正在执行其中的任务，请先停止",
      ),
    ).toBeVisible();
  });
});

const draft = (extra: Record<string, unknown> = {}) =>
  job("d1", {
    status: "DRAFT",
    config_version: 1,
    preview: {
      columns: [
        {
          index: 0,
          header: "Summary",
          samples: ["修复登录", "新增导出"],
          suggested_field: "title",
        },
        {
          index: 1,
          header: "Issue id",
          samples: ["10001"],
          suggested_field: "source_id",
        },
        {
          index: 2,
          header: "Status",
          samples: ["Done"],
          suggested_field: "status",
        },
      ],
      total_rows: 3,
      importable_rows: 3,
      skipped_rows: 0,
      failed_rows: 0,
      row_errors: [],
    },
    config: {
      columns: [
        { index: 0, field: "title" },
        { index: 1, field: "source_id" },
        { index: 2, field: "status" },
      ],
      statuses: [],
      users: [],
      options: {
        import_open: true,
        import_done: true,
        migrated_label: false,
        overwrite: false,
      },
    },
    ...extra,
  });

const analysis = (extra: Record<string, unknown> = {}) => ({
  capabilities: { sprints: false, keep_numbers: false, sprint_count: 0 },
  statuses: [
    { source: "Done", count: 2, done: true, suggested_status_id: null },
    {
      source: "In Progress",
      count: 1,
      done: false,
      suggested_status_id: "st2",
    },
  ],
  users: [
    { source_id: "acc-1", name: "张三", count: 3, suggested_user_id: "u2" },
    { source_id: "acc-2", name: "李四", count: 1, suggested_user_id: null },
  ],
  types: [],
  counts: { total: 3, open: 1, done: 2, invalid: 0 },
  overwrite: { existing: 0, modified: 0 },
  members: { member: 1, invite: 0, external: 1, skip: 0 },
  issues: [],
  ...extra,
});

/** 上传、配置、开始的后端；puts 记录配置请求。 */
function wizardBackend(
  puts: Call[],
  starts: Call[],
  analysisExtra: Record<string, unknown> = {},
): Override {
  return compose((call) => {
    if (call.path === `${o}/imports/jira-csv` && call.method === "POST")
      return json(201, draft());
    if (call.path === `${o}/imports/d1/config` && call.method === "PUT") {
      puts.push(call);
      return json(
        200,
        draft({
          config_version: 1 + puts.length,
          analysis: analysis(analysisExtra),
        }),
      );
    }
    if (call.path === `${o}/imports/d1/start` && call.method === "POST") {
      starts.push(call);
      return json(
        200,
        job("d1", { status: "QUEUED", version: 5, finished_at: null }),
      );
    }
    if (call.path === `${o}/imports/d1` && call.method === "GET")
      return json(
        200,
        job("d1", { status: "QUEUED", version: 5, finished_at: null }),
      );
    return undefined;
  }, list([]));
}

async function pickOption(
  user: ReturnType<typeof userEvent.setup>,
  name: string,
  option: string,
) {
  await user.click(screen.getByRole("button", { name }));
  await user.click(await screen.findByRole("option", { name: option }));
}

async function toColumns(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByRole("button", { name: "下一步" }));
  await pickOption(user, "目标空间", "默认空间");
  await user.upload(
    screen.getByLabelText("选择 CSV 文件"),
    new File(["a,b"], "jira.csv", { type: "text/csv" }),
  );
  await user.click(screen.getByRole("button", { name: "上传并预览" }));
  expect(await screen.findByRole("table", { name: "样例值" })).toBeVisible();
  await user.click(screen.getByRole("button", { name: "下一步" }));
}

describe("CSV 向导", () => {
  it("导入完成后查看空间进入真实任务列表", async () => {
    const user = userEvent.setup();
    const server = renderAt(
      `${page}?source=jira`,
      compose(
        (call) => {
          if (call.path === `${o}/imports/d1` && call.method === "GET")
            return json(200, job("d1", { version: 6 }));
          return undefined;
        },
        wizardBackend([], [], { users: [] }),
      ),
    );
    await toColumns(user);
    for (let i = 0; i < 4; i++)
      await user.click(await screen.findByRole("button", { name: "下一步" }));
    await user.click(await screen.findByRole("button", { name: "开始导入" }));
    await user.click(await screen.findByRole("button", { name: "查看空间" }));
    await waitFor(() =>
      expect(
        server.calls.some(
          (call) =>
            call.method === "GET" && call.path === `${o}/spaces/${SPACE}/tasks`,
        ),
      ).toBe(true),
    );
    expect(screen.queryByText("该功能即将开放")).toBeNull();
  });

  it("映射列：标题列缺失时下一步禁用并说明原因", async () => {
    const user = userEvent.setup();
    renderAt(`${page}?source=jira`, wizardBackend([], []));
    await toColumns(user);
    expect(
      await screen.findByRole("heading", { name: "映射列/字段" }),
    ).toBeVisible();
    const next = screen.getByRole("button", { name: "下一步" });
    expect(next).toBeEnabled();
    await pickOption(user, "Summary 导入为", "不导入");
    expect(next).toBeDisabled();
    expect(screen.getByText("需要有且只有一列映射为「标题」")).toBeVisible();
    // 暂不导入的字段有标注
    await user.click(screen.getByRole("button", { name: "Status 导入为" }));
    expect(
      await screen.findByRole("option", { name: "迭代（暂不导入）" }),
    ).toBeVisible();
  });

  it("状态与用户映射写入 config，覆盖导入需要勾选「我已了解」才能开始", async () => {
    const user = userEvent.setup();
    const puts: Call[] = [];
    const starts: Call[] = [];
    renderAt(
      `${page}?source=jira`,
      wizardBackend(puts, starts, {
        overwrite: { existing: 5, modified: 2 },
      }),
    );
    await toColumns(user);
    await user.click(await screen.findByRole("button", { name: "下一步" }));
    // 映射状态：In Progress 默认用建议状态，Done 默认新建
    expect(
      await screen.findByText("映射状态", { selector: "h2" }),
    ).toBeVisible();
    expect(screen.getByLabelText("Done 的新状态名称")).toHaveValue("Done");
    await user.click(screen.getByRole("button", { name: "下一步" }));
    // 映射用户：张三默认映射到成员，李四默认外部账号；改为邀请
    expect(
      await screen.findByText("映射用户", { selector: "h2" }),
    ).toBeVisible();
    await pickOption(user, "李四 的导入方式", "通过邮箱邀请");
    const next = screen.getByRole("button", { name: "下一步" });
    expect(next).toBeDisabled();
    await user.type(screen.getByLabelText("李四 的邀请邮箱"), "li@example.com");
    expect(next).toBeEnabled();
    await user.click(next);
    // 选项：开启覆盖导入
    expect(
      await screen.findByText("导入选项", { selector: "h2" }),
    ).toBeVisible();
    await user.click(screen.getByRole("switch", { name: "覆盖导入" }));
    await user.click(screen.getByRole("button", { name: "下一步" }));
    // 确认
    expect(
      await screen.findByText("确认导入", { selector: "h2" }),
    ).toBeVisible();
    const config = (puts.at(-1)!.body as { config: Record<string, unknown> })
      .config as {
      statuses: unknown[];
      users: unknown[];
      options: { overwrite: boolean };
    };
    expect(config.statuses).toEqual([
      { source: "Done", new_name: "Done", new_category: "DONE" },
      { source: "In Progress", status_id: "st2" },
    ]);
    expect(config.users).toEqual([
      { source_id: "acc-1", mode: "member", user_id: "u2" },
      { source_id: "acc-2", mode: "invite", email: "li@example.com" },
    ]);
    expect(config.options.overwrite).toBe(true);
    expect(screen.getByText("将覆盖 2 条本地修改")).toBeVisible();
    const start = screen.getByRole("button", { name: "开始导入" });
    expect(start).toBeDisabled();
    await user.click(screen.getByRole("checkbox", { name: "我已了解" }));
    expect(start).toBeEnabled();
    await user.click(start);
    await waitFor(() => expect(starts).toHaveLength(1));
    expect(starts[0]!.body).toEqual({
      config_version: expect.any(Number) as number,
      confirm_overwrite: true,
    });
    // 进入进行中
    expect(
      await screen.findByRole("button", { name: "取消导入" }),
    ).toBeVisible();
  });

  it("analysis.issues 非空时列出问题且不能开始", async () => {
    const user = userEvent.setup();
    renderAt(
      `${page}?source=jira`,
      wizardBackend([], [], {
        issues: [
          {
            code: "status_unmapped",
            message: "状态 Blocked 没有映射",
            value: "Blocked",
          },
        ],
      }),
    );
    await toColumns(user);
    for (let i = 0; i < 3; i++)
      await user.click(await screen.findByRole("button", { name: "下一步" }));
    await user.click(await screen.findByRole("button", { name: "下一步" }));
    expect(await screen.findByText("状态 Blocked 没有映射")).toBeVisible();
    expect(screen.getByRole("button", { name: "开始导入" })).toBeDisabled();
  });

  it("草稿状态下关闭向导会取消（丢弃）草稿", async () => {
    const user = userEvent.setup();
    const cancels: Call[] = [];
    renderAt(
      `${page}?source=jira`,
      compose(
        (call) => {
          if (call.path === `${o}/imports/d1/cancel`) {
            cancels.push(call);
            return json(200, draft({ status: "DISCARDED" }));
          }
          return undefined;
        },
        wizardBackend([], []),
      ),
    );
    await toColumns(user);
    await user.click(screen.getByRole("button", { name: "关闭导入向导" }));
    await waitFor(() => expect(cancels).toHaveLength(1));
    expect(cancels[0]!.body).toEqual({ version: 3 });
    await waitFor(() =>
      expect(
        screen.queryByRole("region", { name: "导入 Jira 数据" }),
      ).toBeNull(),
    );
  });
});

describe("任务详情 · 外部账号处理人", () => {
  const task = (extra: Record<string, unknown>) => (call: Call) => {
    if (call.path.endsWith("/tasks/by-display-id/OPEN-1")) {
      return json(200, {
        ...makeTask(1, "st1", extra),
        description_reactions: [],
      });
    }
    return undefined;
  };

  it("显示只读标签，已邀请时加「· 已邀请」", async () => {
    renderAt(
      `/o/${ORG}/spaces/${SPACE}/tasks?task=OPEN-1`,
      task({
        external_assignee: {
          id: "e1",
          provider: "jira",
          name: "张三",
          email: "z@example.com",
          pending_invitation: true,
        },
      }),
    );
    expect(await screen.findByTestId("external-assignee")).toHaveTextContent(
      "张三（外部） · 已邀请",
    );
  });

  it("没有外部账号时不显示", async () => {
    renderAt(`/o/${ORG}/spaces/${SPACE}/tasks?task=OPEN-1`, task({}));
    await screen.findByRole("textbox", { name: "任务标题" });
    expect(screen.queryByTestId("external-assignee")).toBeNull();
  });
});
