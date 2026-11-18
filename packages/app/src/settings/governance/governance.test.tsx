import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";

import {
  createApi,
  HttpClient,
  type AuditEvent,
  type RuntimeConfig,
  type RuntimeUsage,
} from "@openfox/api";
import { I18nProvider, createTranslator, type Locale } from "@openfox/i18n";
import { ToastProvider } from "@openfox/ui";

import { ReconcilingBadge } from "../../computers/ReconcilingBadge";
import {
  SharedTrustConfirm,
  needsSharedTrustConfirm,
} from "../../computers/SharedTrustConfirm";
import { ApiProvider } from "../../context";
import { QueueFullNotice } from "../../conversations/QueueFullNotice";
import { apiError, json, renderSettings, ts, type Call } from "../testing";
import { validateAuditFilters } from "./AuditLogPage";
import { parseLimit, QUEUE_MAX_RANGE } from "./QuotaPage";

// 阶段 12e · 审计日志页、资源配额页、队列已满提示、共享电脑信任弹窗、核对中徽标。

const ORG = "/v1/organizations/o1";
const en = createTranslator("en-US");
const zh = createTranslator("zh-CN");

function auditEvent(overrides: Partial<AuditEvent> = {}): AuditEvent {
  return {
    id: "e1",
    actor_id: "u1",
    actor_name: "林一",
    actor_email: "lin@openfox.dev",
    action: "device.registered",
    decision: "allow",
    reason: "allowed",
    resource_kind: "device",
    resource_id: "d1",
    metadata: { status: "ACTIVE" },
    created_at: ts,
    ...overrides,
  };
}

const members = (call: Call): Response | undefined =>
  call.path === `${ORG}/members`
    ? json(200, {
        members: [
          {
            user_id: "u2",
            email: "zhou@openfox.dev",
            full_name: "周二",
            status: "ACTIVE",
            role: "ADMIN",
            avatar_url: null,
          },
        ],
        next_cursor: "",
      })
    : undefined;

const auditCalls = (calls: Call[]) =>
  calls.filter((call) => call.path === `${ORG}/audit-events`);
/** 第 index 次审计请求的查询参数 */
const auditQuery = (calls: Call[], index: number) =>
  auditCalls(calls)[index]?.query ?? new URLSearchParams("missing=1");

describe("审计日志页", () => {
  it("所有者：导航可见，按时间倒序列出，加载更多带游标，到底后提示", async () => {
    const calls = renderSettings("/o/o1/settings/audit-log", (call) => {
      if (call.path === `${ORG}/audit-events`)
        return call.query.get("cursor") === "c2"
          ? json(200, {
              events: [auditEvent({ id: "e2", action: "label.created" })],
              next_cursor: null,
            })
          : json(200, { events: [auditEvent()], next_cursor: "c2" });
      return members(call);
    });
    expect(
      await screen.findByRole("heading", { name: "审计日志" }),
    ).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "审计日志" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "资源配额" })).toBeInTheDocument();
    const row = (await screen.findByText("device.registered")).closest("tr")!;
    expect(within(row).getByText("林一")).toBeInTheDocument();
    expect(within(row).getByText("status=ACTIVE")).toBeInTheDocument();
    expect(auditQuery(calls, 0).get("limit")).toBe("50");
    expect(auditQuery(calls, 0).get("cursor")).toBeNull();

    await userEvent.click(screen.getByRole("button", { name: "加载更多" }));
    expect(await screen.findByText("label.created")).toBeInTheDocument();
    expect(screen.getByText("device.registered")).toBeInTheDocument();
    expect(auditQuery(calls, 1).get("cursor")).toBe("c2");
    expect(screen.getByText("已显示全部记录")).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "加载更多" }),
    ).not.toBeInTheDocument();
  });

  it("筛选：动作前缀与资源类型随请求发送；非法输入不发请求；重置恢复", async () => {
    const calls = renderSettings("/o/o1/settings/audit-log", (call) => {
      if (call.path === `${ORG}/audit-events`)
        return json(200, {
          events: call.query.get("action") ? [] : [auditEvent()],
          next_cursor: null,
        });
      return members(call);
    });
    await screen.findByText("device.registered");
    const action = screen.getByLabelText("动作");
    await userEvent.type(action, "bad action");
    await userEvent.click(screen.getByRole("button", { name: "查询" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "动作只能包含字母",
    );
    expect(auditCalls(calls)).toHaveLength(1);

    await userEvent.clear(action);
    await userEvent.type(action, "runtime_config.*");
    await userEvent.type(screen.getByLabelText("资源类型"), "runtime_config");
    await userEvent.click(screen.getByRole("button", { name: "查询" }));
    expect(
      await screen.findByText("没有符合条件的审计记录"),
    ).toBeInTheDocument();
    const filtered = { query: auditQuery(calls, 1) };
    expect(filtered.query.get("action")).toBe("runtime_config.*");
    expect(filtered.query.get("resource_kind")).toBe("runtime_config");
    expect(filtered.query.get("actor_id")).toBeNull();

    await userEvent.click(screen.getByRole("button", { name: "重置" }));
    expect(await screen.findByText("device.registered")).toBeInTheDocument();
    expect(action).toHaveValue("");
  });

  it("空状态与错误状态（可重试）", async () => {
    let fail = true;
    renderSettings("/o/o1/settings/audit-log", (call) => {
      if (call.path === `${ORG}/audit-events`)
        return fail
          ? apiError(500, "INTERNAL_ERROR")
          : json(200, { events: [], next_cursor: null });
      return members(call);
    });
    expect(await screen.findByText("加载审计日志失败")).toBeInTheDocument();
    fail = false;
    await userEvent.click(screen.getByRole("button", { name: "重试" }));
    expect(await screen.findByText("还没有审计记录")).toBeInTheDocument();
  });

  it("普通成员：导航不显示，直接访问看到无权限页且不请求审计数据", async () => {
    const calls = renderSettings(
      "/o/o1/settings/audit-log",
      (call) =>
        call.path === `${ORG}/audit-events`
          ? json(200, { events: [auditEvent()], next_cursor: null })
          : members(call),
      "MEMBER",
    );
    expect(await screen.findByText("没有权限")).toBeInTheDocument();
    expect(
      screen.getByText("只有组织所有者和管理员可以查看此页面。"),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("link", { name: "审计日志" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("link", { name: "资源配额" }),
    ).not.toBeInTheDocument();
    expect(screen.queryByText("device.registered")).not.toBeInTheDocument();
    expect(auditCalls(calls)).toHaveLength(0);
  });

  it("筛选校验：时间顺序、最大跨度、动作与资源类型格式", () => {
    const base = {
      actorId: "",
      action: "",
      resourceKind: "",
      from: "",
      to: "",
    };
    expect(validateAuditFilters(base)).toBeNull();
    expect(validateAuditFilters({ ...base, action: "device.*" })).toBeNull();
    expect(validateAuditFilters({ ...base, action: "*" })).toBe(
      "governance.audit.invalidAction",
    );
    expect(validateAuditFilters({ ...base, action: "dev%" })).toBe(
      "governance.audit.invalidAction",
    );
    expect(validateAuditFilters({ ...base, resourceKind: "a b" })).toBe(
      "governance.audit.invalidResourceKind",
    );
    expect(
      validateAuditFilters({
        ...base,
        from: "2026-10-02T00:00",
        to: "2026-10-01T00:00",
      }),
    ).toBe("governance.audit.invalidRange");
    expect(
      validateAuditFilters({
        ...base,
        from: "2026-10-01T00:00",
        to: "2026-10-01T00:00",
      }),
    ).toBe("governance.audit.invalidRange");
    expect(
      validateAuditFilters({
        ...base,
        from: "2025-01-01T00:00",
        to: "2026-06-01T00:00",
      }),
    ).toBe("governance.audit.rangeTooLong");
    expect(
      validateAuditFilters({
        ...base,
        from: "2026-10-01T00:00",
        to: "2026-10-08T00:00",
      }),
    ).toBeNull();
  });
});

function runtimeConfig(overrides: Partial<RuntimeConfig> = {}): RuntimeConfig {
  return {
    organization_id: "o1",
    config_version: 3,
    applied_version: 3,
    restart_required: false,
    policy: {
      queue_max: 1000,
      active_max: 10,
      queue_timeout_hours: 24,
      spool_max_bytes: 2147483648,
      disk_min_free_bytes: 2147483648,
      run_max_bytes: 20971520,
      detailed_retention_days: 14,
      artifact_file_max_bytes: 20971520,
      artifact_run_max_bytes: 104857600,
      artifact_retention_days: 90,
      audit_retention_days: 180,
      idempotency_retention_days: 30,
    },
    updated_at: ts,
    ...overrides,
  };
}

function runtimeUsage(overrides: Partial<RuntimeUsage> = {}): RuntimeUsage {
  return {
    organization_id: "o1",
    config_version: 3,
    queued: 12,
    queue_max: 1000,
    active: 3,
    active_max: 10,
    capacity_max: 10,
    effective_active_max: 10,
    queue_full: false,
    ...overrides,
  };
}

describe("资源配额页", () => {
  it("显示用量与上限；保存时带版本号与整份策略，只改两个上限", async () => {
    let current = runtimeConfig();
    const calls = renderSettings("/o/o1/settings/quotas", (call) => {
      if (call.path === `${ORG}/runtime-config/usage`)
        return json(200, runtimeUsage());
      if (call.path === `${ORG}/runtime-config` && call.method === "PATCH") {
        const body = call.body as { policy: RuntimeConfig["policy"] };
        current = runtimeConfig({ config_version: 4, policy: body.policy });
        return json(200, current);
      }
      if (call.path === `${ORG}/runtime-config`) return json(200, current);
      return undefined;
    });
    expect(
      await screen.findByRole("heading", { name: "资源配额" }),
    ).toBeInTheDocument();
    expect(await screen.findByText("12 / 1000")).toBeInTheDocument();
    expect(screen.getByText("3 / 10")).toBeInTheDocument();
    const save = screen.getByRole("button", { name: "保存" });
    expect(save).toBeDisabled();
    const queue = screen.getByLabelText("队列上限");
    expect(queue).toHaveValue("1000");
    await userEvent.clear(queue);
    await userEvent.type(queue, "200");
    const active = screen.getByLabelText("活动运行上限");
    await userEvent.clear(active);
    await userEvent.type(active, "5");
    expect(
      screen.getByText(/调小上限不会影响已经在排队或运行的任务/),
    ).toBeInTheDocument();
    await userEvent.click(save);
    await waitFor(() =>
      expect(calls.find((call) => call.method === "PATCH")?.body).toEqual({
        expected_version: 3,
        policy: { ...runtimeConfig().policy, queue_max: 200, active_max: 5 },
      }),
    );
    expect(await screen.findByText("资源配额已更新")).toBeInTheDocument();
    await waitFor(() => expect(save).toBeDisabled());
  });

  it("越界或非整数：提示范围并禁止保存，不发请求", async () => {
    const calls = renderSettings("/o/o1/settings/quotas", (call) => {
      if (call.path === `${ORG}/runtime-config/usage`)
        return json(200, runtimeUsage());
      if (call.path === `${ORG}/runtime-config`)
        return json(200, runtimeConfig());
      return undefined;
    });
    const queue = await screen.findByLabelText("队列上限");
    for (const value of ["0", "10001", "1.5", "abc", ""]) {
      await userEvent.clear(queue);
      if (value) await userEvent.type(queue, value);
      expect(
        screen.getByText("请输入 1–10000 之间的整数。"),
      ).toBeInTheDocument();
      expect(screen.getByRole("button", { name: "保存" })).toBeDisabled();
    }
    const active = screen.getByLabelText("活动运行上限");
    await userEvent.clear(active);
    await userEvent.type(active, "101");
    expect(screen.getByText("请输入 1–100 之间的整数。")).toBeInTheDocument();
    expect(calls.some((call) => call.method === "PATCH")).toBe(false);
    expect(parseLimit("10000", QUEUE_MAX_RANGE)).toBe(10000);
    expect(parseLimit("1", QUEUE_MAX_RANGE)).toBe(1);
    expect(parseLimit("-1", QUEUE_MAX_RANGE)).toBeNull();
    expect(parseLimit("1e3", QUEUE_MAX_RANGE)).toBeNull();
  });

  it("并发修改：409 时提示并刷新为他人的最新值，不覆盖", async () => {
    let current = runtimeConfig();
    const calls = renderSettings("/o/o1/settings/quotas", (call) => {
      if (call.path === `${ORG}/runtime-config/usage`)
        return json(200, runtimeUsage());
      if (call.path === `${ORG}/runtime-config` && call.method === "PATCH") {
        // 另一位管理员已经改成 300（版本 4）
        current = runtimeConfig({
          config_version: 4,
          policy: { ...runtimeConfig().policy, queue_max: 300 },
        });
        return apiError(409, "VERSION_CONFLICT");
      }
      if (call.path === `${ORG}/runtime-config`) return json(200, current);
      return undefined;
    });
    const queue = await screen.findByLabelText("队列上限");
    await userEvent.clear(queue);
    await userEvent.type(queue, "200");
    await userEvent.click(screen.getByRole("button", { name: "保存" }));
    expect(
      await screen.findByText(/配额刚被其他管理员修改/),
    ).toBeInTheDocument();
    await waitFor(() => expect(queue).toHaveValue("300"));
    expect(calls.filter((call) => call.method === "PATCH")).toHaveLength(1);
  });

  it("活动上限超过平台槽位：提示超出部分不生效；队列已满时提示", async () => {
    renderSettings("/o/o1/settings/quotas", (call) => {
      if (call.path === `${ORG}/runtime-config/usage`)
        return json(
          200,
          runtimeUsage({
            queued: 1000,
            queue_full: true,
            capacity_max: 4,
            effective_active_max: 4,
          }),
        );
      if (call.path === `${ORG}/runtime-config`)
        return json(200, runtimeConfig());
      return undefined;
    });
    expect(
      await screen.findByText("当前生效的活动运行上限：4"),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/平台为本组织提供的运行槽位为 4/),
    ).toBeInTheDocument();
    expect(screen.getByText(/队列已满，新消息会等待/)).toBeInTheDocument();
    expect(screen.getByText("3 / 4")).toBeInTheDocument();
    // 已保存的旧值（10）本来就超过平台槽位：只提示，不拦截对队列上限的修改
    const save = screen.getByRole("button", { name: "保存" });
    const queue = screen.getByLabelText("队列上限");
    await userEvent.clear(queue);
    await userEvent.type(queue, "900");
    expect(save).toBeEnabled();
    // 把活动上限改成另一个超过平台槽位的值：不允许保存，并说明原因
    const active = screen.getByLabelText("活动运行上限");
    await userEvent.clear(active);
    await userEvent.type(active, "20");
    expect(
      screen.getByText(
        "平台为本组织提供的运行槽位为 4，活动运行上限不能超过它。",
      ),
    ).toBeInTheDocument();
    expect(save).toBeDisabled();
    expect(active).toHaveAttribute("aria-invalid", "true");
    // 不超过平台槽位的值可以保存
    await userEvent.clear(active);
    await userEvent.type(active, "4");
    expect(save).toBeEnabled();
    expect(
      screen.queryByText(/活动运行上限不能超过它/),
    ).not.toBeInTheDocument();
  });

  it("普通成员：无权限页，不请求配置", async () => {
    const calls = renderSettings(
      "/o/o1/settings/quotas",
      (call) =>
        call.path === `${ORG}/runtime-config`
          ? json(200, runtimeConfig())
          : undefined,
      "MEMBER",
    );
    expect(await screen.findByText("没有权限")).toBeInTheDocument();
    expect(calls.some((call) => call.path === `${ORG}/runtime-config`)).toBe(
      false,
    );
  });
});

function Providers({
  children,
  role = "MEMBER",
  usage,
  locale = "zh-CN",
}: {
  children: ReactNode;
  role?: "OWNER" | "ADMIN" | "MEMBER";
  usage?: RuntimeUsage | "error";
  locale?: Locale;
}) {
  const transport = vi.fn((input: RequestInfo | URL) => {
    const path = new URL(String(input), "http://localhost").pathname;
    if (path.endsWith("/v1/me/organizations"))
      return Promise.resolve(
        json(200, {
          organizations: [
            { id: "o1", slug: "lin", name: "林的团队", role, created_at: ts },
          ],
        }),
      );
    if (path.endsWith("/runtime-config/usage") && usage && usage !== "error")
      return Promise.resolve(json(200, usage));
    return Promise.resolve(apiError(500, "INTERNAL_ERROR"));
  });
  const api = createApi(
    new HttpClient("/api", transport as unknown as typeof fetch),
  );
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return (
    <MemoryRouter>
      <I18nProvider initialLocale={locale}>
        <ApiProvider api={api}>
          <QueryClientProvider client={client}>
            <ToastProvider>{children}</ToastProvider>
          </QueryClientProvider>
        </ApiProvider>
      </I18nProvider>
    </MemoryRouter>
  );
}

describe("对话里的队列已满提示", () => {
  const full = runtimeUsage({ queued: 5, queue_max: 5, queue_full: true });
  // 非管理员从接口只得到排队数与是否已满
  const summary: RuntimeUsage = {
    organization_id: "o1",
    queued: 5,
    queue_full: true,
  };

  it("成员：说明是组织队列已满并提示联系管理员，没有调整入口", async () => {
    render(
      <Providers usage={summary}>
        <QueueFullNotice orgId="o1" />
      </Providers>,
    );
    expect(await screen.findByText("组织队列已满")).toBeInTheDocument();
    expect(
      screen.getByText(/当前排队的运行数已达到组织上限。你的消息已保存/),
    ).toBeInTheDocument();
    // 具体上限不给非管理员
    expect(screen.queryByText(/上限 5/)).not.toBeInTheDocument();
    expect(
      screen.getByText(/请联系组织所有者或管理员调整资源配额/),
    ).toBeInTheDocument();
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });

  it("管理员：给出资源配额页入口", async () => {
    render(
      <Providers usage={full} role="ADMIN">
        <QueueFullNotice orgId="o1" />
      </Providers>,
    );
    expect(
      await screen.findByRole("link", { name: "调整资源配额" }),
    ).toHaveAttribute("href", "/o/o1/settings/quotas");
    expect(screen.queryByText(/请联系组织所有者/)).not.toBeInTheDocument();
    expect(screen.getByText(/当前排队 5，上限 5。/)).toBeInTheDocument();
  });

  it("英文文案", async () => {
    render(
      <Providers usage={full} locale="en-US">
        <QueueFullNotice orgId="o1" />
      </Providers>,
    );
    expect(
      await screen.findByText("The organization queue is full"),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/ask an organization owner or admin/),
    ).toBeInTheDocument();
  });

  it("队列未满或用量接口失败：不显示", async () => {
    const { container, unmount } = render(
      <Providers usage={runtimeUsage()}>
        <QueueFullNotice orgId="o1" />
      </Providers>,
    );
    await waitFor(() => expect(container).toBeEmptyDOMElement());
    unmount();
    const failed = render(
      <Providers usage="error">
        <QueueFullNotice orgId="o1" />
      </Providers>,
    );
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(failed.container).toBeEmptyDOMElement();
  });
});

describe("共享电脑信任说明", () => {
  it("扩大共享范围：说明不是安全沙箱，勾选「我已了解」后才能确认", async () => {
    const onConfirm = vi.fn();
    const onCancel = vi.fn();
    render(
      <Providers>
        <SharedTrustConfirm
          open
          variant="widen"
          onConfirm={onConfirm}
          onCancel={onCancel}
        />
      </Providers>,
    );
    expect(
      screen.getByRole("heading", { name: "扩大这台电脑的共享范围？" }),
    ).toBeInTheDocument();
    expect(screen.getByText("工作目录隔离不是安全沙箱。")).toBeInTheDocument();
    expect(
      screen.getByText(
        "共享对象触发的运行以本机用户身份执行，可读取该用户可读的文件。",
      ),
    ).toBeInTheDocument();
    const confirm = screen.getByRole("button", { name: "确认共享" });
    expect(confirm).toBeDisabled();
    await userEvent.click(confirm);
    expect(onConfirm).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("checkbox", { name: "我已了解" }));
    expect(confirm).toBeEnabled();
    await userEvent.click(confirm);
    expect(onConfirm).toHaveBeenCalledTimes(1);
    await userEvent.click(screen.getByRole("button", { name: "取消" }));
    expect(onCancel).toHaveBeenCalledTimes(1);
  });

  it("添加组织变体与英文文案；重新打开需要重新勾选", async () => {
    const view = (open: boolean) => (
      <Providers locale="en-US">
        <SharedTrustConfirm
          open={open}
          variant="addOrganization"
          onConfirm={() => undefined}
          onCancel={() => undefined}
        />
      </Providers>
    );
    const { rerender } = render(view(true));
    expect(
      screen.getByRole("heading", {
        name: "Add this computer to another organization?",
      }),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        "Working-directory isolation is not a security sandbox.",
      ),
    ).toBeInTheDocument();
    expect(screen.getByText(/execute as the local user/)).toBeInTheDocument();
    await userEvent.click(
      screen.getByRole("checkbox", { name: "I understand" }),
    );
    expect(screen.getByRole("button", { name: "Add" })).toBeEnabled();
    rerender(view(false));
    rerender(view(true));
    expect(screen.getByRole("button", { name: "Add" })).toBeDisabled();
  });

  it("只在范围扩大时需要确认", () => {
    expect(needsSharedTrustConfirm("PERSONAL", "ORGANIZATION")).toBe(true);
    expect(needsSharedTrustConfirm("PERSONAL", "SPACE")).toBe(true);
    expect(needsSharedTrustConfirm("SPACE", "ORGANIZATION")).toBe(true);
    // 换到另一个空间：新的一批人可以使用，同样需要确认
    expect(needsSharedTrustConfirm("SPACE", "SPACE")).toBe(true);
    expect(needsSharedTrustConfirm("ORGANIZATION", "SPACE")).toBe(false);
    expect(needsSharedTrustConfirm("ORGANIZATION", "PERSONAL")).toBe(false);
    expect(needsSharedTrustConfirm("SPACE", "PERSONAL")).toBe(false);
  });
});

describe("电脑「核对中」徽标", () => {
  it("有待核对运行时显示，没有或旧服务端未返回字段时不显示", () => {
    const { container, rerender } = render(
      <Providers>
        <ReconcilingBadge device={{ reconciling_runs: 2 }} />
      </Providers>,
    );
    expect(screen.getByText("核对中")).toBeInTheDocument();
    expect(
      screen.getByTitle("这台电脑上有 2 个运行正在核对状态"),
    ).toBeInTheDocument();
    rerender(
      <Providers>
        <ReconcilingBadge device={{ reconciling_runs: 0 }} />
      </Providers>,
    );
    expect(container).toBeEmptyDOMElement();
    rerender(
      <Providers>
        <ReconcilingBadge device={{}} />
      </Providers>,
    );
    expect(container).toBeEmptyDOMElement();
  });

  it("中英文文案齐全", () => {
    expect(zh("governance.computer.reconciling")).toBe("核对中");
    expect(en("governance.computer.reconciling")).toBe("Verifying");
    expect(en("governance.audit.nav")).toBe("Audit log");
    expect(en("governance.quota.nav")).toBe("Resource quotas");
    expect(en("governance.reconcile.acceptResult")).toBe(
      "Accept existing result",
    );
    expect(zh("governance.reconcile.acceptResult")).toBe("接受已有结果");
  });
});
