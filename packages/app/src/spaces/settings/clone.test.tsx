import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import {
  createApi,
  HttpClient,
  type CloneJob,
  type ClonePreview,
  type SpaceDetail,
} from "@openfox/api";
import { I18nProvider } from "@openfox/i18n";
import { ToastProvider } from "@openfox/ui";

import { ApiProvider } from "../../context";
import {
  apiError,
  findCall,
  json,
  renderSettings,
  ts,
  type Call,
} from "../../settings/testing";
import { CloneSpaceDialog } from "./CloneSpaceDialog";

// 克隆空间对话框：目标组织、预览、确认副本名称、开始条件、任务详情与终态刷新。

const SRC = "/v1/organizations/o1";
const DST = "/v1/organizations/o2";

const space = {
  id: "s1",
  prefix: "OPEN",
  name: "默认空间",
  visibility: "shared",
  version: 3,
  created_at: ts,
  updated_at: ts,
  icon: null,
  purpose: "software_development",
  features: { views: true, projects: true, iterations: false, triage: false },
  multi_assignee: false,
  estimation: {
    scale: "notUsed",
    extended: false,
    allow_zero: false,
    count_unestimated: false,
  },
  status: "active",
  retired_at: null,
  position: 0,
  my_role: "OWNER",
  can_manage: true,
  member_count: 3,
  task_type_count: 2,
  label_count: 0,
  task_count: 0,
} as SpaceDetail;

const targets = {
  targets: [
    { id: "o2", name: "OpenSeek Labs" },
    { id: "o3", name: "Design Studio" },
  ],
};

function previewFor(overrides: Partial<ClonePreview> = {}): ClonePreview {
  return {
    source_name: "默认空间",
    source_prefix: "OPEN",
    target_organization_id: "o2",
    target_organization_name: "OpenSeek Labs",
    target_name: "默认空间",
    target_prefix: "OPEN",
    name_conflict: false,
    counts: {
      spaces: 1,
      tasks: 128,
      projects: 6,
      comments: 342,
      activities: 1204,
      task_types: 4,
      task_statuses: 7,
      labels: 9,
      agents: 2,
      custom_fields: 0,
    },
    blockers: [],
    warnings: [
      {
        code: "agents_need_reconfigure",
        message: "需要重新配置 Agent。",
        count: 2,
      },
      {
        code: "actor_history_kept",
        message: "历史原始操作者标识可能会作为只读历史记录保留。",
        count: 0,
      },
    ],
    accepted_risks: [
      {
        code: "actor_history_kept",
        message: "动态保留原操作者名称。",
        count: 0,
      },
    ],
    skipped: [
      { code: "attachments", message: "附件不会复制。", count: 3 },
      { code: "automations", message: "自动化不会复制。", count: 1 },
    ],
    can_start: true,
    ...overrides,
  };
}

function jobFor(overrides: Partial<CloneJob> = {}): CloneJob {
  return {
    id: "j1",
    status: "QUEUED",
    phase: "",
    source_organization_id: "o1",
    source_space_id: "s1",
    source_space_name: "默认空间",
    target_organization_id: "o2",
    target_space_id: null,
    target_space_name: "默认空间",
    total_count: 128,
    created_count: 0,
    skipped_count: 0,
    failed_count: 0,
    failures: [],
    warnings: [],
    error_code: "",
    error_message: "",
    cancellable: true,
    operation_id: "op_abcdefgh",
    created_at: ts,
    started_at: null,
    finished_at: null,
    version: 1,
    ...overrides,
  };
}

interface Fixture {
  targets?: typeof targets;
  preview?: (body: Record<string, unknown>) => Response;
  start?: (call: Call) => Response;
  /** 开始后的任务；轮询默认也返回它 */
  started?: CloneJob;
  job?: () => CloneJob;
  cancel?: () => Response;
}

function mount(fixture: Fixture = {}) {
  const calls: Call[] = [];
  const transport = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), "http://localhost");
    const raw = init?.body;
    const call: Call = {
      method: init?.method ?? "GET",
      path: url.pathname.replace(/^\/api/, ""),
      query: url.searchParams,
      body: typeof raw === "string" ? (JSON.parse(raw) as unknown) : undefined,
      headers: new Headers(init?.headers),
    };
    calls.push(call);
    if (call.path === `${SRC}/spaces/s1/clone/targets`)
      return Promise.resolve(json(200, fixture.targets ?? targets));
    if (call.path === `${SRC}/spaces/s1/clone/preview`)
      return Promise.resolve(
        fixture.preview
          ? fixture.preview(call.body as Record<string, unknown>)
          : json(200, previewFor()),
      );
    if (call.path === `${SRC}/spaces/s1/clone`)
      return Promise.resolve(
        fixture.start
          ? fixture.start(call)
          : json(201, fixture.started ?? jobFor()),
      );
    if (call.path === `${DST}/clone-jobs/j1` && call.method === "GET")
      return Promise.resolve(
        json(200, fixture.job ? fixture.job() : (fixture.started ?? jobFor())),
      );
    if (call.path === `${DST}/clone-jobs/j1/cancel`)
      return Promise.resolve(
        fixture.cancel
          ? fixture.cancel()
          : json(200, jobFor({ status: "CLEANING", phase: "cleaning" })),
      );
    return Promise.resolve(apiError(404, "NOT_FOUND"));
  });
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const invalidate = vi.spyOn(queryClient, "invalidateQueries");
  const onClose = vi.fn();
  render(
    <I18nProvider initialLocale="zh-CN">
      <QueryClientProvider client={queryClient}>
        <ApiProvider
          api={createApi(
            new HttpClient("/api", transport as unknown as typeof fetch),
          )}
        >
          <ToastProvider>
            <CloneSpaceDialog orgId="o1" space={space} onClose={onClose} />
          </ToastProvider>
        </ApiProvider>
      </QueryClientProvider>
    </I18nProvider>,
  );
  return { calls, invalidate, onClose };
}

afterEach(() => {
  vi.useRealTimers();
});

const previewButton = () => screen.getByRole("button", { name: "预览" });
const startButton = () => screen.getByRole("button", { name: "开始" });

describe("目标组织", () => {
  it("下拉默认选第一个可选组织，可切换", async () => {
    const user = userEvent.setup();
    mount();
    const select = await screen.findByRole("button", { name: "目标组织" });
    expect(select).toHaveTextContent("OpenSeek Labs");
    await user.click(select);
    await user.click(
      await screen.findByRole("option", { name: "Design Studio" }),
    );
    expect(screen.getByRole("button", { name: "目标组织" })).toHaveTextContent(
      "Design Studio",
    );
  });

  it("没有可选组织时显示空态并禁用预览", async () => {
    mount({ targets: { targets: [] } });
    expect(await screen.findByText("没有可克隆到的组织")).toBeInTheDocument();
    expect(previewButton()).toBeDisabled();
    expect(startButton()).toBeDisabled();
  });

  it("切换组织会清空预览", async () => {
    const user = userEvent.setup();
    mount();
    await screen.findByRole("button", { name: "目标组织" });
    await user.click(previewButton());
    expect(
      await screen.findByRole("region", { name: "预览" }),
    ).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "目标组织" }));
    await user.click(
      await screen.findByRole("option", { name: "Design Studio" }),
    );
    expect(
      screen.queryByRole("region", { name: "预览" }),
    ).not.toBeInTheDocument();
    expect(startButton()).toBeDisabled();
  });
});

describe("预览", () => {
  it("显示流向、计数卡、警告，并隐藏值为 0 的计数", async () => {
    const user = userEvent.setup();
    mount();
    await screen.findByRole("button", { name: "目标组织" });
    await user.click(previewButton());
    const region = await screen.findByRole("region", { name: "预览" });
    expect(within(region).getByText("OpenSeek Labs")).toBeInTheDocument();
    expect(within(region).getByText("OPEN")).toBeInTheDocument();
    expect(within(region).getByText("128")).toBeInTheDocument();
    expect(within(region).getByText("任务状态")).toBeInTheDocument();
    expect(within(region).queryByText("自定义字段")).not.toBeInTheDocument();
    expect(
      within(region).getByText("需要重新配置 Agent。"),
    ).toBeInTheDocument();
    expect(
      within(region).getByText(
        "历史原始操作者标识可能会作为只读历史记录保留。",
      ),
    ).toBeInTheDocument();
    expect(within(region).queryByText("阻塞项")).not.toBeInTheDocument();
  });

  it("「已接受风险」「已跳过」默认折叠，右侧显示数量，点击展开", async () => {
    const user = userEvent.setup();
    mount();
    await screen.findByRole("button", { name: "目标组织" });
    await user.click(previewButton());
    const skipped = await screen.findByRole("button", { name: /已跳过/ });
    expect(skipped).toHaveAttribute("aria-expanded", "false");
    expect(skipped).toHaveTextContent("2");
    expect(screen.queryByText("附件不会复制。")).not.toBeInTheDocument();
    await user.click(skipped);
    expect(skipped).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByText("附件不会复制。")).toBeInTheDocument();
    const risks = screen.getByRole("button", { name: /已接受风险/ });
    expect(risks).toHaveAttribute("aria-expanded", "false");
    expect(risks).toHaveTextContent("1");
  });

  it("阻塞项显示在预览里，开始不可用", async () => {
    const user = userEvent.setup();
    mount({
      preview: () =>
        json(
          200,
          previewFor({
            can_start: false,
            blockers: [
              {
                code: "task_limit",
                message: "任务数超过 20,000 的上限。",
                count: 1,
              },
            ],
          }),
        ),
    });
    await screen.findByRole("button", { name: "目标组织" });
    await user.click(previewButton());
    expect(await screen.findByText("阻塞项")).toBeInTheDocument();
    expect(screen.getByText("任务数超过 20,000 的上限。")).toBeInTheDocument();
    expect(startButton()).toBeDisabled();
  });

  it("预览请求带目标组织，请求中按钮显示加载状态", async () => {
    const user = userEvent.setup();
    const { calls } = mount();
    await screen.findByRole("button", { name: "目标组织" });
    await user.click(previewButton());
    await screen.findByRole("region", { name: "预览" });
    expect(
      findCall(calls, "POST", `${SRC}/spaces/s1/clone/preview`)?.body,
    ).toEqual({
      target_organization_id: "o2",
    });
  });
});

describe("确认副本名称", () => {
  it("冲突时出现名称与标识符输入，修改后 400ms 去抖重新预览", async () => {
    const user = userEvent.setup();
    const { calls } = mount({
      preview: (body) =>
        body.name === "副本" && body.prefix === "CPY"
          ? json(200, previewFor({ target_name: "副本", target_prefix: "CPY" }))
          : json(
              200,
              previewFor({
                name_conflict: true,
                can_start: false,
                target_name: "默认空间",
                target_prefix: "OPEN",
              }),
            ),
    });
    await screen.findByRole("button", { name: "目标组织" });
    await user.click(previewButton());
    const name = await screen.findByRole("textbox", { name: "名称" });
    const prefix = screen.getByRole("textbox", { name: "标识符" });
    expect(name).toHaveValue("默认空间");
    expect(prefix).toHaveValue("OPEN");
    expect(startButton()).toBeDisabled();
    const previews = () =>
      calls.filter((call) => call.path === `${SRC}/spaces/s1/clone/preview`);
    expect(previews()).toHaveLength(1);

    await user.clear(name);
    await user.type(name, "副本");
    await user.clear(prefix);
    await user.type(prefix, "cpy");
    // 输入期间不会逐字发请求
    expect(previews()).toHaveLength(1);
    await waitFor(() => expect(previews()).toHaveLength(2), { timeout: 2000 });
    expect(previews()[1]?.body).toEqual({
      target_organization_id: "o2",
      name: "副本",
      prefix: "CPY",
    });
    await waitFor(() => expect(startButton()).toBeEnabled());
  });

  it("命名不合法时不重新预览，开始保持禁用", async () => {
    const user = userEvent.setup();
    const { calls } = mount({
      preview: () =>
        json(200, previewFor({ name_conflict: true, can_start: false })),
    });
    await screen.findByRole("button", { name: "目标组织" });
    await user.click(previewButton());
    const prefix = await screen.findByRole("textbox", { name: "标识符" });
    await user.clear(prefix);
    await user.type(prefix, "A");
    expect(
      await screen.findByText("标识符需要 2–7 位大写字母或数字。"),
    ).toBeInTheDocument();
    await act(() => new Promise((resolve) => setTimeout(resolve, 600)));
    expect(
      calls.filter((call) => call.path === `${SRC}/spaces/s1/clone/preview`),
    ).toHaveLength(1);
    expect(startButton()).toBeDisabled();
  });
});

describe("开始", () => {
  it("预览通过后可用；请求带幂等键与目标组织，并切换到任务详情", async () => {
    const user = userEvent.setup();
    const { calls } = mount();
    await screen.findByRole("button", { name: "目标组织" });
    expect(startButton()).toBeDisabled();
    await user.click(previewButton());
    await screen.findByRole("region", { name: "预览" });
    await waitFor(() => expect(startButton()).toBeEnabled());
    await user.click(startButton());
    const start = await waitFor(() => {
      const call = findCall(calls, "POST", `${SRC}/spaces/s1/clone`);
      expect(call).toBeDefined();
      return call!;
    });
    expect(start.body).toEqual({ target_organization_id: "o2" });
    expect(start.headers.get("Idempotency-Key")).toMatch(
      /^[A-Za-z0-9_-]{8,128}$/,
    );
    expect(await screen.findByText("等待中")).toBeInTheDocument();
    expect(
      screen.queryByRole("region", { name: "预览" }),
    ).not.toBeInTheDocument();
  });

  it("命名冲突（409）时提示并重新预览", async () => {
    const user = userEvent.setup();
    const { calls } = mount({
      start: () => apiError(409, "IDENTIFIER_TAKEN"),
    });
    await screen.findByRole("button", { name: "目标组织" });
    await user.click(previewButton());
    await screen.findByRole("region", { name: "预览" });
    await waitFor(() => expect(startButton()).toBeEnabled());
    await user.click(startButton());
    expect(
      await screen.findByText(
        "目标组织已有同名或同标识符的空间，请修改后重新预览。",
      ),
    ).toBeInTheDocument();
    await waitFor(() =>
      expect(
        calls.filter((call) => call.path === `${SRC}/spaces/s1/clone/preview`),
      ).toHaveLength(2),
    );
  });
});

describe("任务详情", () => {
  async function startJob(fixture: Fixture) {
    const user = userEvent.setup();
    const mounted = mount(fixture);
    await screen.findByRole("button", { name: "目标组织" });
    await user.click(previewButton());
    await screen.findByRole("region", { name: "预览" });
    await waitFor(() => expect(startButton()).toBeEnabled());
    await user.click(startButton());
    await screen.findByRole("button", { name: /克隆空间/ });
    return { user, ...mounted };
  }

  it.each([
    ["QUEUED", "等待中", true],
    ["RUNNING", "运行中", true],
    ["CLEANING", "清理中", true],
    ["SUCCEEDED", "已完成", false],
    ["CLEANED", "已清理", false],
    ["FAILED", "失败", false],
    ["CLEANUP_FAILED", "清理失败", false],
    ["CANCELLED", "已取消", false],
  ] as const)(
    "状态 %s 显示「%s」，取消按钮 %s",
    async (status, label, cancellable) => {
      await startJob({
        started: jobFor({
          status,
          phase: cancellable ? "importing" : "",
          finished_at: cancellable ? null : ts,
        }),
      });
      expect(await screen.findByText(label)).toBeInTheDocument();
      if (cancellable) {
        expect(screen.getByText("阶段: 导入任务")).toBeInTheDocument();
        expect(
          screen.getByRole("button", { name: "取消" }),
        ).toBeInTheDocument();
      } else {
        expect(
          screen.queryByRole("button", { name: "取消" }),
        ).not.toBeInTheDocument();
      }
    },
  );

  it("取消请求发往目标组织，状态变为清理中", async () => {
    const { user, calls } = await startJob({
      started: jobFor({ status: "RUNNING", phase: "importing" }),
    });
    await user.click(await screen.findByRole("button", { name: "取消" }));
    await waitFor(() =>
      expect(
        findCall(calls, "POST", `${DST}/clone-jobs/j1/cancel`),
      ).toBeDefined(),
    );
    expect(await screen.findByText("清理中")).toBeInTheDocument();
  });

  it("显示失败项目与红字明细", async () => {
    await startJob({
      started: jobFor({
        status: "CLEANED",
        finished_at: ts,
        failed_count: 2,
        failures: [
          { ref: "OPEN-12", error: "状态不在工作流内" },
          { ref: "OPEN-30", error: "标题为空" },
        ],
        error_message: "导入任务失败",
      }),
    });
    expect(await screen.findByText("失败项目")).toBeInTheDocument();
    const item = screen.getByText(/OPEN-12/);
    expect(item).toHaveTextContent("OPEN-12 — 状态不在工作流内");
    expect(item).toHaveClass("text-danger");
    expect(screen.getByText("导入任务失败")).toBeInTheDocument();
  });

  it("运行中不显示失败原因，也不暴露内部错误文本", async () => {
    await startJob({
      started: jobFor({
        status: "RUNNING",
        phase: "importing",
        error_message: "no rows in result set",
      }),
    });
    expect(await screen.findByText("运行中")).toBeInTheDocument();
    expect(screen.queryByText("失败原因")).not.toBeInTheDocument();
    expect(screen.queryByText(/no rows in result set/)).not.toBeInTheDocument();
  });

  it("失败原因按错误码显示可读文案，取消不显示失败原因", async () => {
    await startJob({
      started: jobFor({
        status: "CLEANED",
        finished_at: ts,
        error_code: "CLONE_FAILED",
        error_message: "internal: no rows in result set",
      }),
    });
    expect(await screen.findByText("失败原因")).toBeInTheDocument();
    expect(
      screen.getByText(/克隆过程中出现内部错误，已停止并清理/),
    ).toBeInTheDocument();
    expect(screen.queryByText(/no rows in result set/)).not.toBeInTheDocument();
  });

  it("用户取消后清理完成时不显示失败原因", async () => {
    await startJob({
      started: jobFor({
        status: "CLEANED",
        finished_at: ts,
        error_code: "CANCELLED",
        error_message: "克隆已取消",
      }),
    });
    expect(await screen.findByText("已清理")).toBeInTheDocument();
    expect(screen.queryByText("失败原因")).not.toBeInTheDocument();
  });

  it("每 2 秒轮询，到终态后停止并刷新源、目标两个组织的空间列表", async () => {
    let status: CloneJob["status"] = "RUNNING";
    const user = userEvent.setup();
    const { calls, invalidate } = mount({
      start: () => json(201, jobFor({ status: "RUNNING", phase: "importing" })),
      job: () =>
        jobFor({
          status,
          phase: status === "RUNNING" ? "importing" : "",
          finished_at: status === "RUNNING" ? null : ts,
        }),
    });
    await screen.findByRole("button", { name: "目标组织" });
    await user.click(previewButton());
    await screen.findByRole("region", { name: "预览" });
    await waitFor(() => expect(startButton()).toBeEnabled());
    await user.click(startButton());
    expect(await screen.findByText("运行中")).toBeInTheDocument();
    const polls = () =>
      calls.filter(
        (call) => call.method === "GET" && call.path === `${DST}/clone-jobs/j1`,
      );
    await waitFor(() => expect(polls().length).toBeGreaterThanOrEqual(2), {
      timeout: 5000,
    });
    expect(invalidate).not.toHaveBeenCalledWith({
      queryKey: ["org", "o2", "spaces"],
    });
    status = "SUCCEEDED";
    expect(
      await screen.findByText("已完成", {}, { timeout: 5000 }),
    ).toBeInTheDocument();
    await waitFor(() => {
      expect(invalidate).toHaveBeenCalledWith({
        queryKey: ["org", "o1", "spaces"],
      });
      expect(invalidate).toHaveBeenCalledWith({
        queryKey: ["org", "o2", "spaces"],
      });
    });
    const after = polls().length;
    await act(() => new Promise((resolve) => setTimeout(resolve, 2600)));
    expect(polls().length).toBe(after);
    // 没有完成 toast
    expect(
      screen.queryByRole("status", { name: /克隆/ }),
    ).not.toBeInTheDocument();
  }, 20_000);

  it("「‹ 克隆空间」返回预览视图", async () => {
    const { user } = await startJob({});
    await user.click(screen.getByRole("button", { name: /克隆空间/ }));
    expect(
      await screen.findByRole("button", { name: "目标组织" }),
    ).toBeInTheDocument();
  });
});

describe("危险操作入口", () => {
  it("克隆按钮可用并打开对话框", async () => {
    const user = userEvent.setup();
    renderSettings("/o/o1/settings/spaces/s1/general", (call) => {
      if (call.path === `${SRC}/spaces` && call.method === "GET")
        return json(200, { spaces: [space] });
      if (call.path === `${SRC}/spaces/s1` && call.method === "GET")
        return json(200, space);
      if (call.path === `${SRC}/spaces/s1/repos`)
        return json(200, { repos: [] });
      if (call.path === `${SRC}/spaces/s1/clone/targets`)
        return json(200, targets);
      return undefined;
    });
    const button = await screen.findByRole("button", { name: "克隆空间" });
    expect(button).toBeEnabled();
    await user.click(button);
    const dialog = await screen.findByRole("dialog", { name: "克隆空间" });
    expect(
      within(dialog).getByText(
        "在另一个组织中创建此空间的副本。源空间保持不变。",
      ),
    ).toBeInTheDocument();
    expect(
      await within(dialog).findByRole("button", { name: "目标组织" }),
    ).toHaveTextContent("OpenSeek Labs");
    expect(
      within(dialog).getByText("预览会在服务器端再次校验所有者/管理员权限。"),
    ).toBeInTheDocument();
  });
});
