import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";

import {
  createApi,
  HttpClient,
  type ReconciliationCase,
  type ReconciliationEvidence,
  type Run,
} from "@openfox/api";
import { I18nProvider, type Locale } from "@openfox/i18n";
import { ToastProvider } from "@openfox/ui";

import { ApiProvider } from "../../context";
import { json, type Call } from "../../settings/testing";
import { RunTimeline } from "./RunTimeline";

// 阶段 12e：待核对面板——管理员看到证据列表与「接受已有结果」，非管理员显示保持不变。

const ORG = "/v1/organizations/o1";
const TS = "2026-11-12T08:00:00Z";

const run = (extra: Partial<Run> = {}): Run => ({
  id: "r1",
  organization_id: "o1",
  space_id: "s1",
  conversation_id: "c1",
  agent_id: "a1",
  computer_id: "d1",
  grant_id: "g1",
  config_version: 1,
  grant_version: 1,
  actor_id: "u1",
  state: "RUNNING",
  generation: 1,
  reconciliation_required: true,
  reason: "",
  version: 3,
  created_at: TS,
  updated_at: TS,
  failure_code: "",
  started_at: TS,
  usage: null,
  duration_ms: null,
  ...extra,
});

const openCase: ReconciliationCase = {
  organization_id: "o1",
  id: "case1",
  run_id: "r1",
  generation: 1,
  reason_code: "PID_REUSE",
  failure_code: "UNKNOWN_PROCESS",
  status: "OPEN",
  safe_detail: "",
  opened_at: TS,
  version: 2,
};

const evidence = (
  extra: Partial<ReconciliationEvidence> = {},
): ReconciliationEvidence => ({
  id: "ev1",
  case_id: "case1",
  kind: "PROCESS_NOT_FOUND",
  source: "DAEMON",
  process_state: "UNKNOWN",
  accepted_state: "ACCEPTED",
  detail: "进程号可能已被复用",
  observed_at: TS,
  created_at: TS,
  can_accept_result: false,
  ...extra,
});

interface Scenario {
  role: "OWNER" | "ADMIN" | "MEMBER";
  evidence?: ReconciliationEvidence[];
  /** resolve 接口的响应；缺省为成功裁决 */
  resolve?: () => Response;
  locale?: Locale;
}

function mount(scenario: Scenario) {
  const calls: Call[] = [];
  let resolved = false;
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
    const reply = (status: number, body: unknown) =>
      Promise.resolve(json(status, body));
    if (call.path === "/v1/me/organizations")
      return reply(200, {
        organizations: [
          {
            id: "o1",
            slug: "lin",
            name: "林的团队",
            role: scenario.role,
            created_at: TS,
          },
        ],
      });
    if (call.path === `${ORG}/runs/r1`)
      return reply(200, run({ reconciliation_required: !resolved }));
    if (call.path === `${ORG}/runs/r1/events`)
      return reply(200, { events: [] });
    if (call.path === `${ORG}/runs/r1/reconciliation`)
      return reply(200, { cases: [openCase] });
    if (call.path === `${ORG}/runs/r1/reconciliation/evidence`)
      return scenario.role === "MEMBER"
        ? reply(403, { error: { code: "FORBIDDEN", message: "x" } })
        : reply(200, { evidence: scenario.evidence ?? [] });
    if (call.path === `${ORG}/runs/r1/reconciliation/case1/resolve`) {
      if (scenario.resolve) return Promise.resolve(scenario.resolve());
      resolved = true;
      return reply(200, {
        ...openCase,
        status: "RESOLVED",
        resolution: "RESULT_ACCEPTED",
        version: 3,
        already_resolved: false,
      });
    }
    return reply(404, { error: { code: "NOT_FOUND", message: "x" } });
  });
  const api = createApi(
    new HttpClient("/api", transport as unknown as typeof fetch),
  );
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  render(
    <MemoryRouter>
      <I18nProvider initialLocale={scenario.locale ?? "zh-CN"}>
        <ApiProvider api={api}>
          <QueryClientProvider client={client}>
            <ToastProvider>
              <RunTimeline orgId="o1" runId="r1" />
            </ToastProvider>
          </QueryClientProvider>
        </ApiProvider>
      </I18nProvider>
    </MemoryRouter>,
  );
  return calls;
}

const evidenceCalls = (calls: Call[]) =>
  calls.filter((call) => call.path.endsWith("/reconciliation/evidence"));
const resolveCalls = (calls: Call[]) =>
  calls.filter((call) => call.path.endsWith("/case1/resolve"));

describe("待核对面板（管理员部分）", () => {
  it("非管理员：只见状态与「请联系管理员」，不请求证据，没有任何处理入口", async () => {
    const calls = mount({
      role: "MEMBER",
      evidence: [
        evidence({ kind: "RESULT_CONFIRMED", can_accept_result: true }),
      ],
    });
    expect(await screen.findByText(/待核对/)).toBeInTheDocument();
    expect(await screen.findByText("请联系管理员处理。")).toBeInTheDocument();
    expect(screen.queryByText("核对证据")).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "接受已有结果" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "标记为失败" }),
    ).not.toBeInTheDocument();
    expect(evidenceCalls(calls)).toHaveLength(0);
  });

  it("管理员：显示证据的来源、类型、时间与说明；没有结果确认类证据时不显示「接受已有结果」", async () => {
    mount({
      role: "ADMIN",
      evidence: [
        evidence(),
        evidence({
          id: "ev2",
          kind: "HEARTBEAT",
          source: "CENTER",
          accepted_state: "STALE",
          detail: "",
        }),
      ],
    });
    expect(await screen.findByText("核对证据")).toBeInTheDocument();
    const table = await screen.findByRole("table");
    for (const header of ["来源", "类型", "时间", "说明"])
      expect(
        within(table).getByRole("columnheader", { name: header }),
      ).toBeInTheDocument();
    const first = within(table).getByText("进程号可能已被复用").closest("tr")!;
    expect(within(first).getByText("电脑")).toBeInTheDocument();
    expect(within(first).getByText("找不到进程")).toBeInTheDocument();
    expect(first.querySelector("time")).toHaveAttribute("datetime", TS);
    const second = within(table).getByText("中心").closest("tr")!;
    expect(within(second).getByText(/心跳/)).toBeInTheDocument();
    expect(within(second).getByText(/未受理/)).toBeInTheDocument();
    expect(within(second).getByText("无")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "标记为失败" }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "接受已有结果" }),
    ).not.toBeInTheDocument();
  });

  it("管理员：没有证据时显示空提示", async () => {
    mount({ role: "OWNER", evidence: [] });
    expect(await screen.findByText("还没有收到证据。")).toBeInTheDocument();
  });

  it("接受已有结果：二次确认后才调用，带版本号、证据 ID 与裁决类型", async () => {
    const calls = mount({
      role: "OWNER",
      evidence: [
        evidence({
          id: "ev9",
          kind: "RESULT_CONFIRMED",
          detail: "结果已上传",
          can_accept_result: true,
        }),
        evidence(),
      ],
    });
    await userEvent.click(
      await screen.findByRole("button", { name: "接受已有结果" }),
    );
    expect(resolveCalls(calls)).toHaveLength(0);
    expect(screen.getByText(/确认接受？/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "取消" }));
    expect(resolveCalls(calls)).toHaveLength(0);

    await userEvent.click(screen.getByRole("button", { name: "接受已有结果" }));
    await userEvent.click(screen.getByRole("button", { name: "确认接受" }));
    await waitFor(() => expect(resolveCalls(calls)).toHaveLength(1));
    expect(resolveCalls(calls)[0]?.body).toEqual({
      version: 2,
      evidence_id: "ev9",
      resolution: "RESULT_ACCEPTED",
    });
    // 成功后刷新：待核对解除，面板消失
    await waitFor(() =>
      expect(screen.queryByText("核对证据")).not.toBeInTheDocument(),
    );
  });

  it("被他人先处理：提示「已处理」并刷新", async () => {
    const calls = mount({
      role: "ADMIN",
      evidence: [
        evidence({ kind: "RESULT_CONFIRMED", can_accept_result: true }),
      ],
      resolve: () =>
        json(200, {
          ...openCase,
          status: "RESOLVED",
          resolution: "PROCESS_EXITED_FAILED",
          resolved_by: "u9",
          version: 3,
          already_resolved: true,
        }),
    });
    await userEvent.click(
      await screen.findByRole("button", { name: "接受已有结果" }),
    );
    const before = calls.filter(
      (call) => call.path === `${ORG}/runs/r1`,
    ).length;
    await userEvent.click(screen.getByRole("button", { name: "确认接受" }));
    expect(await screen.findByText(/已处理：这次核对已由/)).toBeInTheDocument();
    await waitFor(() =>
      expect(
        calls.filter((call) => call.path === `${ORG}/runs/r1`).length,
      ).toBeGreaterThan(before),
    );
  });

  it("裁决失败（例如证据已不满足要求）：显示错误并刷新证据", async () => {
    const calls = mount({
      role: "ADMIN",
      evidence: [
        evidence({ kind: "RESULT_CONFIRMED", can_accept_result: true }),
      ],
      resolve: () =>
        json(409, { error: { code: "RECONCILIATION_CONFLICT", message: "x" } }),
    });
    await userEvent.click(
      await screen.findByRole("button", { name: "接受已有结果" }),
    );
    const before = evidenceCalls(calls).length;
    await userEvent.click(screen.getByRole("button", { name: "确认接受" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "操作失败，请刷新后重试。",
    );
    await waitFor(() =>
      expect(evidenceCalls(calls).length).toBeGreaterThan(before),
    );
  });

  it("英文文案", async () => {
    mount({
      role: "OWNER",
      locale: "en-US",
      evidence: [
        evidence({ kind: "RESULT_CONFIRMED", can_accept_result: true }),
      ],
    });
    expect(
      await screen.findByText("Verification evidence"),
    ).toBeInTheDocument();
    expect(await screen.findByText("Result confirmed")).toBeInTheDocument();
    expect(screen.getByText("Computer")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Accept existing result" }),
    ).toBeInTheDocument();
  });
});
