import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";

import { createApi, HttpClient, type ReconcilingRun } from "@openfox/api";
import { I18nProvider, type Locale } from "@openfox/i18n";
import { ToastProvider } from "@openfox/ui";

import { ApiProvider } from "../context";
import { json, type Call } from "../settings/testing";
import { ReconcilingRunsPanel } from "./ReconcilingRunsPanel";

// 阶段 12e：管理员在电脑页处理待核对的运行（列表与「标记为失败」的确认流程）。

const ORG = "/v1/organizations/o1";
const TS = "2026-11-12T08:00:00Z";

const pending = (extra: Partial<ReconcilingRun> = {}): ReconcilingRun => ({
  run_id: "r1",
  computer_id: "d1",
  computer_name: "build-server-01",
  agent_name: "熊孩子",
  actor_name: "周二",
  pending_since: TS,
  reason_code: "PID_REUSE",
  case_id: "case1",
  case_version: 4,
  ...extra,
});

interface Scenario {
  role?: "OWNER" | "ADMIN" | "MEMBER";
  runs?: ReconcilingRun[];
  deviceId?: string;
  /** mark-failed 的响应；缺省为成功 */
  markFailed?: () => Response;
  locale?: Locale;
}

function mount(scenario: Scenario) {
  const calls: Call[] = [];
  let runs = scenario.runs ?? [pending()];
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
    if (call.path === "/v1/me/organizations")
      return Promise.resolve(
        json(200, {
          organizations: [
            {
              id: "o1",
              slug: "lin",
              name: "林的团队",
              role: scenario.role ?? "ADMIN",
              created_at: TS,
            },
          ],
        }),
      );
    if (call.path === `${ORG}/reconciling-runs`)
      return Promise.resolve(json(200, { runs }));
    if (call.path.endsWith("/mark-failed")) {
      if (scenario.markFailed) return Promise.resolve(scenario.markFailed());
      // 处理成功后该运行不再待核对
      runs = runs.filter((run) => !call.path.includes(`/runs/${run.run_id}/`));
      return Promise.resolve(
        json(200, {
          case: { id: "case1", status: "RESOLVED" },
          already_resolved: false,
        }),
      );
    }
    return Promise.resolve(
      json(404, { error: { code: "NOT_FOUND", message: "x" } }),
    );
  });
  const api = createApi(
    new HttpClient("/api", transport as unknown as typeof fetch),
  );
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const view = render(
    <MemoryRouter>
      <I18nProvider initialLocale={scenario.locale ?? "zh-CN"}>
        <ApiProvider api={api}>
          <QueryClientProvider client={client}>
            <ToastProvider>
              <ReconcilingRunsPanel orgId="o1" deviceId={scenario.deviceId} />
            </ToastProvider>
          </QueryClientProvider>
        </ApiProvider>
      </I18nProvider>
    </MemoryRouter>,
  );
  return { calls, container: view.container };
}

const listCalls = (calls: Call[]) =>
  calls.filter((call) => call.path === `${ORG}/reconciling-runs`);
const markCalls = (calls: Call[]) =>
  calls.filter((call) => call.path.endsWith("/mark-failed"));

describe("电脑页：待核对的运行", () => {
  it("普通成员：不显示，也不请求列表", async () => {
    const { calls, container } = mount({ role: "MEMBER" });
    await waitFor(() =>
      expect(calls.some((call) => call.path === "/v1/me/organizations")).toBe(
        true,
      ),
    );
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(container.querySelector("section")).toBeNull();
    expect(listCalls(calls)).toHaveLength(0);
  });

  it("管理员：按电脑列出 Agent、发起人、时间与原因；没有待核对运行时不显示", async () => {
    const { calls } = mount({ deviceId: "d1" });
    const panel = await screen.findByRole("region", { name: "待核对的运行" });
    expect(listCalls(calls)[0]?.query.get("device_id")).toBe("d1");
    for (const header of ["Agent", "发起人", "进入待核对", "原因", "操作"])
      expect(
        within(panel).getByRole("columnheader", { name: header }),
      ).toBeInTheDocument();
    // 限定到一台电脑时不重复显示电脑列
    expect(
      within(panel).queryByRole("columnheader", { name: "电脑" }),
    ).not.toBeInTheDocument();
    const row = within(panel).getByText("熊孩子").closest("tr")!;
    expect(within(row).getByText("周二")).toBeInTheDocument();
    expect(
      within(row).getByText("无法确定原任务进程是否仍在，为避免误操作未做处理"),
    ).toBeInTheDocument();
    expect(row.querySelector("time")).toHaveAttribute("datetime", TS);

    const empty = mount({ runs: [] });
    await waitFor(() => expect(listCalls(empty.calls)).toHaveLength(1));
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(empty.container.querySelector("section")).toBeNull();
  });

  it("全组织列表：多一列电脑，不带 device_id；没有开放核对案的运行不给操作", async () => {
    const { calls } = mount({
      role: "OWNER",
      runs: [
        pending(),
        pending({
          run_id: "r2",
          computer_name: "laptop-02",
          agent_name: "小助手",
          reason_code: "",
          case_id: "",
          case_version: 0,
        }),
      ],
    });
    const panel = await screen.findByRole("region", { name: "待核对的运行" });
    expect(listCalls(calls)[0]?.query.get("device_id")).toBeNull();
    expect(
      within(panel).getByRole("columnheader", { name: "电脑" }),
    ).toBeInTheDocument();
    expect(within(panel).getByText("build-server-01")).toBeInTheDocument();
    const second = within(panel).getByText("laptop-02").closest("tr")!;
    expect(within(second).getByText("正在核对任务状态")).toBeInTheDocument();
    expect(within(second).getByText("暂时无法处理")).toBeInTheDocument();
    expect(within(second).queryByRole("button")).not.toBeInTheDocument();
    expect(
      within(panel).getAllByRole("button", { name: "标记为失败" }),
    ).toHaveLength(1);
  });

  it("标记为失败：二次确认说明后果，确认后带版本号调用并刷新列表", async () => {
    const { calls } = mount({ deviceId: "d1" });
    await userEvent.click(
      await screen.findByRole("button", { name: "标记为失败" }),
    );
    const dialog = await screen.findByRole("dialog");
    expect(
      within(dialog).getByRole("heading", { name: "把这次运行标记为失败？" }),
    ).toBeInTheDocument();
    const consequence =
      within(dialog).getByText(/熊孩子 由 周二 发起的这次运行/);
    expect(consequence).toHaveTextContent("立即结束并记为失败");
    expect(consequence).toHaveTextContent("对话恢复为可以继续输入");
    expect(consequence).toHaveTextContent("不会自动重试");
    expect(markCalls(calls)).toHaveLength(0);
    // 取消不调用
    await userEvent.click(within(dialog).getByRole("button", { name: "取消" }));
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
    expect(markCalls(calls)).toHaveLength(0);

    await userEvent.click(screen.getByRole("button", { name: "标记为失败" }));
    await userEvent.click(
      within(await screen.findByRole("dialog")).getByRole("button", {
        name: "确认标记为失败",
      }),
    );
    await waitFor(() => expect(markCalls(calls)).toHaveLength(1));
    expect(markCalls(calls)[0]?.path).toBe(
      `${ORG}/runs/r1/reconciliation/case1/mark-failed`,
    );
    expect(markCalls(calls)[0]?.body).toEqual({ version: 4 });
    expect(await screen.findByText("已标记为失败")).toBeInTheDocument();
    // 刷新后列表为空，面板消失
    await waitFor(() =>
      expect(
        screen.queryByRole("region", { name: "待核对的运行" }),
      ).not.toBeInTheDocument(),
    );
    expect(listCalls(calls).length).toBeGreaterThan(1);
  });

  it("被他人或电脑端先处理：提示「已处理」并刷新", async () => {
    const { calls } = mount({
      deviceId: "d1",
      markFailed: () =>
        json(200, {
          case: { id: "case1", status: "RESOLVED" },
          already_resolved: true,
        }),
    });
    await userEvent.click(
      await screen.findByRole("button", { name: "标记为失败" }),
    );
    const before = listCalls(calls).length;
    await userEvent.click(
      within(await screen.findByRole("dialog")).getByRole("button", {
        name: "确认标记为失败",
      }),
    );
    expect(await screen.findByText(/已处理：这次运行已由/)).toBeInTheDocument();
    expect(screen.queryByText("已标记为失败")).not.toBeInTheDocument();
    await waitFor(() =>
      expect(listCalls(calls).length).toBeGreaterThan(before),
    );
  });

  it("失败（例如核对案版本已变化）：提示错误并刷新", async () => {
    const { calls } = mount({
      deviceId: "d1",
      markFailed: () =>
        json(409, { error: { code: "RECONCILIATION_CONFLICT", message: "x" } }),
    });
    await userEvent.click(
      await screen.findByRole("button", { name: "标记为失败" }),
    );
    const before = listCalls(calls).length;
    await userEvent.click(
      within(await screen.findByRole("dialog")).getByRole("button", {
        name: "确认标记为失败",
      }),
    );
    expect(
      await screen.findByText("操作失败，请刷新后重试。"),
    ).toBeInTheDocument();
    await waitFor(() =>
      expect(listCalls(calls).length).toBeGreaterThan(before),
    );
  });

  it("英文文案", async () => {
    mount({ deviceId: "d1", locale: "en-US" });
    const panel = await screen.findByRole("region", {
      name: "Runs awaiting verification",
    });
    await userEvent.click(
      within(panel).getByRole("button", { name: "Mark as failed" }),
    );
    const dialog = await screen.findByRole("dialog");
    expect(
      within(dialog).getByText(/Nothing is retried automatically/),
    ).toBeInTheDocument();
    expect(
      within(dialog).getByText(/its conversation accepts input again/),
    ).toBeInTheDocument();
  });
});
