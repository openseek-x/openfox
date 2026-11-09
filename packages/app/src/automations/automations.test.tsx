import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { createApi, HttpClient } from "@openfox/api";
import { createTranslator } from "@openfox/i18n";

import { App, createQueryClient } from "../App";
import { eventForKind } from "../me/NotificationWatcher";
import {
  backend,
  json,
  ORG,
  SPACE,
  type Call,
  type Override,
} from "../tasks/test-backend";
import { cronFor, nextRuns, parseCron } from "./cron";
import {
  isAutomationReady,
  isValidIpEntry,
  newTriggerDraft,
  triggerInput,
  validateIpAllowlist,
} from "./model";
import { triggerSummary } from "./parts";

// Agent 自动化：纯函数（cron、草稿与校验）、列表与空态、创建 / 编辑、启停、删除、运行、
// 运行详情、Webhook 安全区、投递详情与重放、拖拽排序的请求体、收件箱映射。

const o = `/v1/organizations/${ORG}`;
const sp = `${o}/spaces/${SPACE}`;
const listPath = `/o/${ORG}/spaces/${SPACE}/automations`;
const detailPath = (id: string) => `${listPath}/${id}`;

const space = {
  id: SPACE,
  prefix: "OPEN",
  name: "默认空间",
  visibility: "shared",
  version: 2,
  created_at: "2026-10-01T00:00:00Z",
  updated_at: "2026-10-01T00:00:00Z",
  icon: null,
  purpose: "software_development",
  features: { views: false, projects: false, iterations: false, triage: false },
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
};

const agents = [
  {
    id: "ag1",
    name: "小助手",
    title: "",
    description: "",
    allow_assign: true,
    allow_direct_chat: true,
    kind: "CODE",
    visibility: "SPACE",
    avatar: null,
    owner_user_id: "u1",
    space_id: SPACE,
    organization_id: ORG,
    trigger_crew: false,
    trigger_mention: true,
    trigger_workflow: true,
    profile_version: 1,
    online: true,
    computer_name: null,
    favorite: false,
    created_at: "2026-10-01T00:00:00Z",
    current_config_version: 1,
    version: 1,
  },
  {
    id: "ag2",
    name: "只读 Agent",
    title: "",
    description: "",
    allow_assign: false,
    allow_direct_chat: true,
    kind: "CODE",
    visibility: "SPACE",
    avatar: null,
    owner_user_id: "u1",
    space_id: SPACE,
    organization_id: ORG,
    trigger_crew: false,
    trigger_mention: true,
    trigger_workflow: true,
    profile_version: 1,
    online: true,
    computer_name: null,
    favorite: false,
    created_at: "2026-10-01T00:00:00Z",
    current_config_version: 1,
    version: 1,
  },
  {
    id: "ag3",
    name: "无直聊 Agent",
    title: "",
    description: "",
    allow_assign: true,
    allow_direct_chat: false,
    kind: "CODE",
    visibility: "SPACE",
    avatar: null,
    owner_user_id: "u1",
    space_id: SPACE,
    organization_id: ORG,
    trigger_crew: false,
    trigger_mention: true,
    trigger_workflow: true,
    profile_version: 1,
    online: true,
    computer_name: null,
    favorite: false,
    created_at: "2026-10-01T00:00:00Z",
    current_config_version: 1,
    version: 1,
  },
];

const schedule = (id: string, extra: Record<string, unknown> = {}) => ({
  id,
  kind: "schedule",
  enabled: true,
  config: {
    schedule: { frequency: "daily", time: "09:00", timezone: "Asia/Shanghai" },
  },
  next_run_at: "2026-10-31T01:00:00Z",
  webhook: null,
  ...extra,
});

const automation = (id: string, extra: Record<string, unknown> = {}) => ({
  id,
  space_id: SPACE,
  title: `自动化 ${id}`,
  prompt: "每天整理待办",
  assignee: { kind: "AGENT", id: "ag1", name: "小助手" },
  notify_user_ids: ["u1"],
  enabled: true,
  position: 0,
  triggers: [schedule(`${id}-tr`)],
  last_run: null,
  created_by: "u1",
  created_at: "2026-10-01T00:00:00Z",
  updated_at: "2026-10-01T00:00:00Z",
  version: 3,
  can_edit: true,
  ...extra,
});

const run = (id: string, extra: Record<string, unknown> = {}) => ({
  id,
  automation_id: "a1",
  trigger_id: "a1-tr",
  trigger_kind: "schedule",
  status: "completed",
  reason: "",
  trigger_title: "计划运行",
  created_at: "2026-10-30T01:00:00Z",
  finished_at: "2026-10-30T01:02:00Z",
  conversation_id: null,
  run_id: null,
  ...extra,
});

const runDetail = (id: string, extra: Record<string, unknown> = {}) => ({
  ...run(id),
  trigger_payload: { source: "schedule" },
  result: "已整理 12 条待办",
  failure_code: "",
  failure_message: "",
  ...extra,
});

const webhookTrigger = (id: string, extra: Record<string, unknown> = {}) => ({
  id,
  kind: "webhook",
  enabled: true,
  config: {},
  next_run_at: null,
  webhook: {
    signature_required: false,
    has_secret: true,
    ip_allowlist: [],
    last_source_ip: null,
  },
  ...extra,
});

const delivery = (id: string, extra: Record<string, unknown> = {}) => ({
  id,
  trigger_id: "a1-hook",
  status: "dispatched",
  signature_status: "valid",
  provider: "github",
  event: "push",
  received_at: "2026-10-30T02:00:00Z",
  attempts: 1,
  replay_of: null,
  automation_run_id: null,
  ...extra,
});

const deliveryDetail = (id: string, extra: Record<string, unknown> = {}) => ({
  ...delivery(id),
  last_attempt_at: "2026-10-30T02:00:01Z",
  response_status: 202,
  response_body: "accepted",
  source_ip: "203.0.113.9",
  dedupe_key: "abc123",
  dedupe_source: "header",
  content_type: "application/json",
  headers: { "X-GitHub-Event": "push" },
  body: '{"ref":"refs/heads/main"}',
  body_truncated: false,
  error: "",
  replayable: true,
  not_replayable_reason: null,
  ...extra,
});

function renderAt(
  path: string,
  opts: {
    automations?: ReturnType<typeof automation>[];
    override?: Override;
  } = {},
) {
  const list = opts.automations ?? [automation("a1")];
  const server = backend((call) => {
    const custom = opts.override?.(call);
    if (custom) return custom;
    if (call.path === sp) return json(200, space);
    if (call.path === `${sp}/agents`) return json(200, { agents });
    if (call.path === `${o}/agent-directory`)
      return json(200, { entries: [], archived_count: 0, truncated: false });
    if (call.path === `${sp}/automations` && call.method === "GET")
      return json(200, { automations: list });
    if (call.path === `${sp}/automations` && call.method === "POST")
      return json(201, {
        automation: automation("new"),
        webhook_urls: [],
      });
    if (call.path === `${sp}/automations/reorder`)
      return new Response(null, { status: 204 });
    if (/\/automations\/a1$/.test(call.path) && call.method === "GET")
      return json(
        200,
        list.find((item) => item.id === "a1") ?? automation("a1"),
      );
    if (/\/automations\/a1$/.test(call.path) && call.method === "PATCH")
      return json(200, { automation: automation("a1"), webhook_urls: [] });
    if (/\/automations\/a1$/.test(call.path) && call.method === "DELETE")
      return new Response(null, { status: 204 });
    if (/\/automations\/a1\/run$/.test(call.path))
      return json(201, run("r-new", { status: "queued" }));
    if (/\/automations\/a1\/runs$/.test(call.path))
      return json(200, { runs: [run("r1")], next_cursor: "" });
    if (/\/automations\/a1\/runs\/r1$/.test(call.path))
      return json(200, runDetail("r1"));
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

const callsTo = (calls: Call[], method: string, suffix: RegExp) =>
  calls.filter((call) => call.method === method && suffix.test(call.path));

beforeEach(() => {
  localStorage.setItem(`openfox:onboarding:${ORG}`, JSON.stringify({}));
});
afterEach(() => localStorage.clear());

describe("自动化纯函数", () => {
  it("cron 分钟必须是单个数字", () => {
    expect(parseCron("*/5 * * * *")).toEqual({ ok: false, error: "minute" });
    expect(parseCron("0-30 9 * * *")).toEqual({ ok: false, error: "minute" });
    expect(parseCron("0 9 * *")).toEqual({ ok: false, error: "format" });
    expect(parseCron("30 9 * * 1-5").ok).toBe(true);
  });

  it("每天 09:00（上海）下一次运行是 UTC 01:00", () => {
    const [first, second] = nextRuns(
      "0 9 * * *",
      "Asia/Shanghai",
      new Date("2026-10-30T00:00:00Z"),
      2,
    );
    expect(first?.toISOString()).toBe("2026-10-30T01:00:00.000Z");
    expect(second?.toISOString()).toBe("2026-10-31T01:00:00.000Z");
  });

  it("工作日跳过周末：周六之后的第一次是周一", () => {
    // 2026-10-31 是周六
    const [first] = nextRuns(
      "0 9 * * 1-5",
      "Asia/Shanghai",
      new Date("2026-10-31T02:00:00Z"),
      1,
    );
    expect(first?.toISOString()).toBe("2026-11-02T01:00:00.000Z");
  });

  it("每小时的分钟按时区计算，非法表达式没有预览", () => {
    const runs = nextRuns(
      "15 * * * *",
      "UTC",
      new Date("2026-10-30T10:20:00Z"),
      2,
    );
    expect(runs.map((date) => date.toISOString())).toEqual([
      "2026-10-30T11:15:00.000Z",
      "2026-10-30T12:15:00.000Z",
    ]);
    expect(nextRuns("*/5 * * * *", "UTC", new Date(0))).toEqual([]);
  });

  it("按频率生成 cron", () => {
    const base = { minute: 20, time: "10:30", weekdays: [3, 1], cron: "" };
    expect(cronFor({ ...base, frequency: "hourly" })).toBe("20 * * * *");
    expect(cronFor({ ...base, frequency: "daily" })).toBe("30 10 * * *");
    expect(cronFor({ ...base, frequency: "weekdays" })).toBe("30 10 * * 1-5");
    expect(cronFor({ ...base, frequency: "weekly" })).toBe("30 10 * * 1,3");
  });

  it("四种触发器的请求体", () => {
    const tz = "Asia/Shanghai";
    const schedule = triggerInput(newTriggerDraft("schedule", "k1", tz));
    expect(schedule.config.schedule).toEqual({
      frequency: "daily",
      time: "09:00",
      timezone: tz,
    });
    const status = triggerInput({
      ...newTriggerDraft("task_status", "k2", tz),
      statusIds: ["st2"],
      filters: [{ field: "priority", op: "is", value: "HIGH" }],
    });
    expect(status.config).toEqual({
      status_ids: ["st2"],
      filters: [{ field: "priority", op: "is", value: "HIGH" }],
    });
    const comment = triggerInput({
      ...newTriggerDraft("comment", "k3", tz),
      contains: " /triage ",
    });
    expect(comment.config).toEqual({ contains: "/triage" });
    const hook = triggerInput(newTriggerDraft("webhook", "k4", tz));
    expect(hook).toEqual({ kind: "webhook", enabled: true, config: {} });
    // 编辑已有 webhook 时才带签名与白名单
    const kept = triggerInput({
      ...newTriggerDraft("webhook", "k5", tz),
      id: "h1",
      signatureRequired: true,
      ipAllowlist: "203.0.113.10\n\n198.51.100.0/24",
    });
    expect(kept).toEqual({
      id: "h1",
      kind: "webhook",
      enabled: true,
      config: {},
      signature_required: true,
      ip_allowlist: ["203.0.113.10", "198.51.100.0/24"],
    });
  });

  it("触发器与标题、指派共同决定能否创建", () => {
    const tz = "Asia/Shanghai";
    const valid = newTriggerDraft("schedule", "k1", tz);
    expect(
      isAutomationReady({ title: "", assigneeId: "ag1", triggers: [valid] }),
    ).toBe(false);
    expect(
      isAutomationReady({ title: "整理", assigneeId: null, triggers: [valid] }),
    ).toBe(false);
    expect(
      isAutomationReady({ title: "整理", assigneeId: "ag1", triggers: [] }),
    ).toBe(false);
    expect(
      isAutomationReady({
        title: "整理",
        assigneeId: "ag1",
        triggers: [newTriggerDraft("comment", "k2", tz)],
      }),
    ).toBe(false);
    expect(
      isAutomationReady({
        title: "整理",
        assigneeId: "ag1",
        triggers: [valid],
      }),
    ).toBe(true);
  });

  it("IP 白名单：IPv4、IPv6、CIDR 与条数上限", () => {
    expect(isValidIpEntry("203.0.113.10")).toBe(true);
    expect(isValidIpEntry("198.51.100.0/24")).toBe(true);
    expect(isValidIpEntry("2001:db8::/32")).toBe(true);
    expect(isValidIpEntry("999.1.1.1")).toBe(false);
    expect(isValidIpEntry("198.51.100.0/33")).toBe(false);
    expect(validateIpAllowlist("203.0.113.10\nnope")).toMatchObject({
      invalid: { line: 2, value: "nope" },
      tooMany: false,
    });
    const many = Array.from({ length: 21 }, (_, i) => `10.0.0.${i}`).join("\n");
    expect(validateIpAllowlist(many).tooMany).toBe(true);
  });
});

describe("自动化列表", () => {
  it("空态显示说明，顶栏不显示新建按钮", async () => {
    renderAt(listPath, { automations: [] });
    expect(
      await screen.findByText(/自动化可由任务事件触发、对任务执行操作/),
    ).toBeInTheDocument();
    // 只剩空态正文里的一个「新建自动化」按钮
    expect(screen.getAllByRole("button", { name: "新建自动化" })).toHaveLength(
      1,
    );
  });

  it("卡片展示标题、提示词与触发器摘要", async () => {
    renderAt(listPath);
    expect(await screen.findByText("自动化 a1")).toBeInTheDocument();
    expect(screen.getByText("每天整理待办")).toBeInTheDocument();
    expect(screen.getByText("每天 09:00")).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: "新建自动化" })).toHaveLength(
      1,
    );
  });

  it("启停需要先确认，确认后带 version 请求", async () => {
    const user = userEvent.setup();
    const server = renderAt(listPath);
    await user.click(await screen.findByRole("switch", { name: "自动化 a1" }));
    expect(await screen.findByText("暂停自动化？")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "暂停" }));
    await waitFor(() =>
      expect(
        callsTo(server.calls, "PATCH", /automations\/a1$/)[0]?.body,
      ).toEqual({
        version: 3,
        enabled: false,
      }),
    );
  });

  it("删除需要确认，请求带 version 参数", async () => {
    const user = userEvent.setup();
    const server = renderAt(listPath);
    await user.click(
      await screen.findByRole("button", { name: "自动化 a1 更多操作" }),
    );
    await user.click(await screen.findByRole("menuitem", { name: "删除" }));
    await user.click(await screen.findByRole("button", { name: "删除" }));
    await waitFor(() => {
      const deletes = callsTo(server.calls, "DELETE", /automations\/a1$/);
      expect(deletes[0]?.query.get("version")).toBe("3");
    });
    expect(await screen.findByText("已删除自动化")).toBeInTheDocument();
  });

  it("立即运行：跳过时提示已跳过，AUTOMATION_DISABLED 时提示暂停", async () => {
    const user = userEvent.setup();
    const skipped = renderAt(listPath, {
      override: (call) =>
        /\/automations\/a1\/run$/.test(call.path)
          ? json(201, run("r2", { status: "skipped" }))
          : undefined,
    });
    await user.click(
      await screen.findByRole("button", { name: "自动化 a1 更多操作" }),
    );
    await user.click(await screen.findByRole("menuitem", { name: "立即运行" }));
    expect(
      await screen.findByText("上一次运行尚未结束，本次已跳过"),
    ).toBeInTheDocument();
    expect(callsTo(skipped.calls, "POST", /\/run$/)).toHaveLength(1);
  });

  it("立即运行被拒绝（AUTOMATION_DISABLED）显示暂停提示", async () => {
    const user = userEvent.setup();
    renderAt(listPath, {
      override: (call) =>
        /\/automations\/a1\/run$/.test(call.path)
          ? json(409, {
              error: {
                code: "AUTOMATION_DISABLED",
                message: "x",
                request_id: "r",
              },
            })
          : undefined,
    });
    await user.click(
      await screen.findByRole("button", { name: "自动化 a1 更多操作" }),
    );
    await user.click(await screen.findByRole("menuitem", { name: "立即运行" }));
    expect(
      await screen.findByText("自动化已暂停，请先恢复后再运行。"),
    ).toBeInTheDocument();
  });
});

describe("创建与编辑", () => {
  it("未开启直接对话的 Agent 禁用并说明原因", async () => {
    const user = userEvent.setup();
    renderAt(listPath);
    await user.click(await screen.findByRole("button", { name: "新建自动化" }));
    await user.click(screen.getByRole("button", { name: "选择 Agent 或小队" }));
    const option = await screen.findByRole("option", { name: /无直聊 Agent/ });
    expect(option).toHaveAttribute("aria-disabled", "true");
    expect(
      within(option).getByText("该 Agent 未开启直接对话，不能用于自动化"),
    ).toBeInTheDocument();
  });

  it("保存被拒绝（AGENT_DIRECT_CHAT_DISABLED）显示专门提示", async () => {
    const user = userEvent.setup();
    renderAt(listPath, {
      override: (call) =>
        call.method === "POST" && /\/automations$/.test(call.path)
          ? json(400, {
              error: {
                code: "AGENT_DIRECT_CHAT_DISABLED",
                message: "x",
                request_id: "r",
              },
            })
          : undefined,
    });
    await user.click(await screen.findByRole("button", { name: "新建自动化" }));
    await user.type(screen.getByLabelText(/标题/), "整理");
    await user.click(screen.getByRole("button", { name: "选择 Agent 或小队" }));
    await user.click(await screen.findByRole("option", { name: /小助手/ }));
    await user.click(screen.getByRole("button", { name: "创建自动化" }));
    expect(
      await screen.findByText("该 Agent 未开启直接对话，不能用于自动化。"),
    ).toBeInTheDocument();
  });

  it("创建请求包含定时触发器与通知人员，标题与指派满足后才可提交", async () => {
    const user = userEvent.setup();
    const server = renderAt(listPath);
    await user.click(await screen.findByRole("button", { name: "新建自动化" }));
    const submit = screen.getByRole("button", { name: "创建自动化" });
    expect(submit).toBeDisabled();
    await user.type(screen.getByLabelText(/标题/), "每日整理");
    expect(submit).toBeDisabled();
    await user.click(screen.getByRole("button", { name: "选择 Agent 或小队" }));
    await user.click(await screen.findByRole("option", { name: /小助手/ }));
    await waitFor(() => expect(submit).toBeEnabled());
    await user.click(submit);
    await waitFor(() => {
      const body = callsTo(server.calls, "POST", /\/automations$/)[0]?.body as {
        title: string;
        assignee: { kind: string; id: string };
        notify_user_ids: string[];
        triggers: {
          kind: string;
          config: { schedule?: { frequency: string } };
        }[];
      };
      expect(body.title).toBe("每日整理");
      expect(body.assignee).toEqual({ kind: "AGENT", id: "ag1" });
      expect(body.notify_user_ids).toEqual(["u1"]);
      expect(body.triggers[0]?.kind).toBe("schedule");
      expect(body.triggers[0]?.config.schedule?.frequency).toBe("daily");
    });
  });

  it("只能指派允许指派的 Agent：不可指派的选项禁用并说明原因", async () => {
    const user = userEvent.setup();
    renderAt(listPath);
    await user.click(await screen.findByRole("button", { name: "新建自动化" }));
    await user.click(screen.getByRole("button", { name: "选择 Agent 或小队" }));
    const option = await screen.findByRole("option", { name: /只读 Agent/ });
    expect(option).toHaveAttribute("aria-disabled", "true");
    expect(within(option).getByText("不可指派")).toBeInTheDocument();
  });

  it("创建返回 webhook_urls 时切换为小窗，一次性显示完整 URL", async () => {
    const user = userEvent.setup();
    const url = "https://openfox.test/v1/hooks/tok123";
    renderAt(listPath, {
      override: (call) =>
        call.method === "POST" && /\/automations$/.test(call.path)
          ? json(201, {
              automation: automation("new"),
              webhook_urls: [{ trigger_id: "h1", url }],
            })
          : undefined,
    });
    await user.click(await screen.findByRole("button", { name: "新建自动化" }));
    await user.type(screen.getByLabelText(/标题/), "接收 push");
    await user.click(screen.getByRole("button", { name: "选择 Agent 或小队" }));
    await user.click(await screen.findByRole("option", { name: /小助手/ }));
    await user.click(screen.getByRole("button", { name: "触发方式" }));
    await user.click(await screen.findByRole("option", { name: /Webhook/ }));
    await user.click(screen.getByRole("button", { name: "创建自动化" }));
    expect(await screen.findByText(url)).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "复制 URL" }),
    ).toBeInTheDocument();
  });

  it("编辑保存时保留触发器 id 与 version", async () => {
    const user = userEvent.setup();
    const server = renderAt(detailPath("a1"));
    await user.click(await screen.findByRole("button", { name: "编辑" }));
    await user.click(await screen.findByRole("button", { name: "保存" }));
    await waitFor(() => {
      const body = callsTo(server.calls, "PATCH", /automations\/a1$/)[0]
        ?.body as {
        version: number;
        triggers: { id: string }[];
      };
      expect(body.version).toBe(3);
      expect(body.triggers[0]?.id).toBe("a1-tr");
    });
  });
});

describe("详情、运行与投递", () => {
  it("运行记录点击后打开运行详情抽屉", async () => {
    const user = userEvent.setup();
    renderAt(detailPath("a1"));
    await user.click(await screen.findByRole("button", { name: /计划运行/ }));
    expect(await screen.findByText("已整理 12 条待办")).toBeInTheDocument();
    expect(screen.getByText("运行详情")).toBeInTheDocument();
  });

  it("投递：可重放时发起重放，不可重放时显示原因", async () => {
    const user = userEvent.setup();
    const hookAutomation = automation("a1", {
      triggers: [schedule("a1-tr"), webhookTrigger("a1-hook")],
    });
    const server = renderAt(detailPath("a1"), {
      automations: [hookAutomation],
      override: (call) => {
        if (/\/deliveries$/.test(call.path))
          return json(200, {
            deliveries: [
              delivery("d1"),
              delivery("d2", { status: "queued", signature_status: "missing" }),
            ],
            next_cursor: "",
          });
        if (/\/deliveries\/d1$/.test(call.path))
          return json(200, deliveryDetail("d1"));
        if (/\/deliveries\/d2$/.test(call.path))
          return json(
            200,
            deliveryDetail("d2", {
              status: "queued",
              replayable: false,
              not_replayable_reason: "queued",
            }),
          );
        if (/\/deliveries\/d1\/replay$/.test(call.path))
          return json(201, delivery("d3", { replay_of: "d1" }));
        return undefined;
      },
    });
    await user.click(
      (await screen.findAllByRole("button", { name: /github/ }))[0]!,
    );
    const replay = await screen.findByRole("button", { name: "重放" });
    await user.click(replay);
    await waitFor(() =>
      expect(
        callsTo(server.calls, "POST", /deliveries\/d1\/replay$/),
      ).toHaveLength(1),
    );
  });

  it("不可重放的投递禁用重放按钮并说明原因", async () => {
    const user = userEvent.setup();
    renderAt(detailPath("a1"), {
      automations: [
        automation("a1", {
          triggers: [schedule("a1-tr"), webhookTrigger("a1-hook")],
        }),
      ],
      override: (call) => {
        if (/\/deliveries$/.test(call.path))
          return json(200, {
            deliveries: [
              delivery("d2", { status: "queued", signature_status: "missing" }),
            ],
            next_cursor: "",
          });
        if (/\/deliveries\/d2$/.test(call.path))
          return json(
            200,
            deliveryDetail("d2", {
              status: "queued",
              replayable: false,
              not_replayable_reason: "queued",
            }),
          );
        return undefined;
      },
    });
    await user.click(await screen.findByRole("button", { name: /github/ }));
    expect(
      await screen.findByText("排队中的投递不能重放。"),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "重放" })).toBeDisabled();
  });
});

describe("Webhook 安全设置", () => {
  const hookEdit = (override?: Override) =>
    renderAt(detailPath("a1"), {
      automations: [
        automation("a1", {
          triggers: [
            schedule("a1-tr"),
            webhookTrigger("a1-hook", {
              webhook: {
                signature_required: false,
                has_secret: false,
                ip_allowlist: [],
                last_source_ip: null,
              },
            }),
          ],
        }),
      ],
      override: (call) => {
        if (/\/webhook-url$/.test(call.path))
          return json(200, { url: "https://openfox.test/v1/hooks/new-token" });
        if (/\/signing-secret$/.test(call.path) && call.method === "POST")
          return json(200, { secret: "whsec_test_123" });
        return override?.(call);
      },
    });

  it("重新生成 URL 需要先确认，新 URL 只显示一次", async () => {
    const user = userEvent.setup();
    const server = hookEdit();
    await user.click(await screen.findByRole("button", { name: "编辑" }));
    await user.click(
      await screen.findByRole("button", { name: "重新生成 URL" }),
    );
    expect(
      await screen.findByText("重新生成 Webhook URL？"),
    ).toBeInTheDocument();
    expect(callsTo(server.calls, "POST", /webhook-url$/)).toHaveLength(0);
    await user.click(
      within(screen.getAllByRole("dialog").at(-1)!).getByRole("button", {
        name: "重新生成",
      }),
    );
    expect(
      await screen.findByText("https://openfox.test/v1/hooks/new-token"),
    ).toBeInTheDocument();
    expect(callsTo(server.calls, "POST", /webhook-url$/)).toHaveLength(1);
  });

  it("开启签名校验前没有密钥时先生成密钥", async () => {
    const user = userEvent.setup();
    const server = hookEdit();
    await user.click(await screen.findByRole("button", { name: "编辑" }));
    await user.click(await screen.findByRole("switch", { name: "签名校验" }));
    await waitFor(() =>
      expect(callsTo(server.calls, "POST", /signing-secret$/)).toHaveLength(1),
    );
    expect(await screen.findByText("whsec_test_123")).toBeInTheDocument();
  });

  it("IP 白名单格式错误时在输入框下方显示红字", async () => {
    const user = userEvent.setup();
    hookEdit();
    await user.click(await screen.findByRole("button", { name: "编辑" }));
    await user.click(await screen.findByRole("switch", { name: "IP 白名单" }));
    await user.type(
      screen.getByRole("textbox", { name: "IP 白名单" }),
      "999.1.1.1",
    );
    expect(
      await screen.findByText(/第 1 行不是有效的 IP 地址或网段：999\.1\.1\.1/),
    ).toBeInTheDocument();
  });
});

describe("收件箱与通知映射", () => {
  it("AUTOMATION_RESULT 映射为自动化通知，并有中文文案", () => {
    expect(eventForKind("AUTOMATION_RESULT")).toBe("automation");
    const zh = createTranslator("zh-CN");
    expect(zh("inbox.kinds.AUTOMATION_RESULT")).toBe("自动化运行结束");
  });
});

describe("拖拽排序请求体", () => {
  it("reorder 发送空间全部自动化的新顺序", async () => {
    const calls: Call[] = [];
    const transport = (async (url: string, init: RequestInit) => {
      const parsed = new URL(url, "http://localhost");
      calls.push({
        method: init.method ?? "GET",
        path: parsed.pathname.replace(/^\/api/, ""),
        query: parsed.searchParams,
        body: JSON.parse(String(init.body)),
        headers: new Headers(init.headers),
      });
      return new Response(null, { status: 204 });
    }) as typeof fetch;
    const api = createApi(new HttpClient("/api", transport));
    await api.automations.reorder(ORG, SPACE, ["a2", "a1"]);
    const put = calls.find((call) => call.method === "PUT");
    expect(put?.path).toBe(`${sp}/automations/reorder`);
    expect(put?.body).toEqual({ automation_ids: ["a2", "a1"] });
  });
});

// 验收台账 R37：Webhook 详情白屏与状态触发摘要。
describe("投递列表兜底与状态触发摘要", () => {
  const hookAutomation = () =>
    automation("a1", {
      triggers: [schedule("a1-tr"), webhookTrigger("a1-hook")],
    });

  it.each([
    ["null（旧服务端）", null],
    ["空数组", []],
  ])("投递列表为 %s 时详情页正常渲染投递空态", async (_label, deliveries) => {
    renderAt(detailPath("a1"), {
      automations: [hookAutomation()],
      override: (call) => {
        if (/\/deliveries$/.test(call.path))
          return json(200, { deliveries, next_cursor: "" });
        return undefined;
      },
    });
    expect(await screen.findByText(/还没有 webhook 投递/)).toBeInTheDocument();
    expect(screen.getByText("WEBHOOK 投递")).toBeInTheDocument();
  });

  it("运行记录为 null 时显示运行空态，不抛错", async () => {
    renderAt(detailPath("a1"), {
      override: (call) => {
        if (/\/automations\/a1\/runs$/.test(call.path))
          return json(200, { runs: null, next_cursor: "" });
        return undefined;
      },
    });
    expect(
      await screen.findByText("自动化 a1", { selector: "h1" }),
    ).toBeInTheDocument();
    expect(await screen.findByText(/暂无运行记录/)).toBeInTheDocument();
  });

  const statusTrigger = (config: Record<string, unknown>) =>
    ({
      id: "a1-st",
      kind: "task_status",
      enabled: true,
      config,
      next_run_at: null,
      webhook: null,
    }) as const;
  const known = [
    { id: "st1", name: "待处理" },
    { id: "st2", name: "进行中" },
    { id: "st3", name: "已完成" },
    { id: "st4", name: "已验收" },
  ];
  const zh = createTranslator("zh-CN");
  const en = createTranslator("en-US");

  it("单个状态显示状态名", () => {
    expect(
      triggerSummary(zh, statusTrigger({ status_ids: ["st2"] }), known),
    ).toBe("状态变为进行中");
    expect(
      triggerSummary(en, statusTrigger({ status_ids: ["st2"] }), known),
    ).toBe("Status changes to 进行中");
  });

  it("多个状态用分隔符连接；超过 3 个显示前 3 个加「等 N 个」", () => {
    expect(
      triggerSummary(zh, statusTrigger({ status_ids: ["st1", "st3"] }), known),
    ).toBe("状态变为待处理、已完成");
    expect(
      triggerSummary(
        zh,
        statusTrigger({ status_ids: ["st1", "st2", "st3"] }),
        known,
      ),
    ).toBe("状态变为待处理、进行中、已完成");
    expect(
      triggerSummary(
        zh,
        statusTrigger({ status_ids: ["st1", "st2", "st3", "st4"] }),
        known,
      ),
    ).toBe("状态变为待处理、进行中、已完成 等 4 个");
    expect(
      triggerSummary(
        en,
        statusTrigger({ status_ids: ["st1", "st2", "st3", "st4"] }),
        known,
      ),
    ).toBe("Status changes to 待处理, 进行中, 已完成 and more (4 total)");
  });

  it("状态被删除时显示「未知状态」而不是 UUID；状态列表未加载时显示省略号", () => {
    const gone = "8f0c9d8e-0000-4000-8000-000000000001";
    const text = triggerSummary(
      zh,
      statusTrigger({ status_ids: ["st2", gone] }),
      known,
    );
    expect(text).toBe("状态变为进行中、未知状态");
    expect(text).not.toContain(gone);
    expect(
      triggerSummary(zh, statusTrigger({ status_ids: ["st2"] }), undefined),
    ).toBe("状态变为…");
    // status_ids 为空只是防御：才显示「任意状态」
    expect(triggerSummary(zh, statusTrigger({ status_ids: [] }), known)).toBe(
      "状态变为任意状态",
    );
    expect(triggerSummary(zh, statusTrigger({}), known)).toBe(
      "状态变为任意状态",
    );
  });

  it("配置了 filters 时摘要末尾说明条件个数", () => {
    const trigger = statusTrigger({
      status_ids: ["st2"],
      filters: [
        { field: "priority", op: "is", value: "HIGH" },
        { field: "label", op: "includes", value: "lb1" },
      ],
    });
    expect(triggerSummary(zh, trigger, known)).toBe(
      "状态变为进行中 · 另有 2 个条件",
    );
    expect(triggerSummary(en, trigger, known)).toBe(
      "Status changes to 进行中 · 2 more conditions",
    );
  });

  it("列表卡片与详情触发器行都显示配置的状态名", async () => {
    const statusAutomation = automation("a1", {
      triggers: [statusTrigger({ status_ids: ["st2"] })],
    });
    renderAt(listPath, { automations: [statusAutomation] });
    expect(await screen.findByText("状态变为进行中")).toBeInTheDocument();
    expect(screen.queryByText("状态变为任意状态")).not.toBeInTheDocument();
    cleanup();

    renderAt(detailPath("a1"), { automations: [statusAutomation] });
    expect(await screen.findByText("状态变为进行中")).toBeInTheDocument();
    expect(screen.queryByText("状态变为任意状态")).not.toBeInTheDocument();
  });
});

describe("停用的规则也能立即运行", () => {
  const paused = automation("a1", { enabled: false });

  it("列表：停用时「立即运行」可点并说明只运行这一次，请求照常发出", async () => {
    const user = userEvent.setup();
    const server = renderAt(listPath, { automations: [paused] });
    await user.click(
      await screen.findByRole("button", { name: "自动化 a1 更多操作" }),
    );
    const item = await screen.findByRole("menuitem", { name: /立即运行/ });
    expect(item).not.toHaveAttribute("aria-disabled");
    expect(
      within(item).getByText("规则已暂停，只运行这一次"),
    ).toBeInTheDocument();
    await user.click(item);
    await waitFor(() =>
      expect(callsTo(server.calls, "POST", /\/run$/)).toHaveLength(1),
    );
    expect(await screen.findByText("已开始运行")).toBeInTheDocument();
  });

  it("详情：停用时按钮可点，悬停提示只运行这一次，请求照常发出", async () => {
    const user = userEvent.setup();
    const server = renderAt(detailPath("a1"), { automations: [paused] });
    const button = await screen.findByRole("button", { name: "立即运行" });
    expect(button).toBeEnabled();
    await user.hover(button);
    expect(
      await screen.findByText("规则已暂停，只运行这一次"),
    ).toBeInTheDocument();
    await user.click(button);
    await waitFor(() =>
      expect(callsTo(server.calls, "POST", /\/run$/)).toHaveLength(1),
    );
  });

  it("启用的规则不显示这条说明", async () => {
    const user = userEvent.setup();
    renderAt(listPath);
    await user.click(
      await screen.findByRole("button", { name: "自动化 a1 更多操作" }),
    );
    const item = await screen.findByRole("menuitem", { name: "立即运行" });
    expect(
      within(item).queryByText("规则已暂停，只运行这一次"),
    ).not.toBeInTheDocument();
  });
});
