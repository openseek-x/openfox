import type {
  Automation,
  AutomationScheduleConfig,
  AutomationTaskFilter,
  AutomationTrigger,
  AutomationTriggerInput,
  AutomationTriggerKind,
  AutomationWebhook,
} from "@openfox/api";

import { parseCron } from "./cron";

// 编辑弹窗的草稿模型：表单状态与接口请求体之间的转换、校验。纯函数，不依赖 React。

/** 合同限制：每个自动化最多 10 个触发器，任务状态触发器最多 10 个筛选条件，webhook 白名单最多 20 条。 */
export const MAX_TRIGGERS = 10;
export const MAX_FILTERS = 10;
export const MAX_IP_ENTRIES = 20;
export const MAX_COMMENT_LENGTH = 200;

export type ScheduleFrequency = AutomationScheduleConfig["frequency"];

export interface ScheduleDraft {
  frequency: ScheduleFrequency;
  /** hourly 的分钟 */
  minute: number;
  /** daily / weekdays / weekly 的 HH:mm */
  time: string;
  /** weekly 的星期，0 = 周日 */
  weekdays: number[];
  /** custom 的 cron 表达式 */
  cron: string;
  timezone: string;
}

export interface FilterDraft {
  field: AutomationTaskFilter["field"];
  op: AutomationTaskFilter["op"];
  value: string;
}

export interface TriggerDraft {
  /** 客户端临时 key，用于列表渲染；已有触发器用服务端 id */
  key: string;
  /** 已有触发器保留服务端 id，编辑时据此保留 webhook 的 URL 与密钥 */
  id?: string;
  kind: AutomationTriggerKind;
  enabled: boolean;
  schedule: ScheduleDraft;
  statusIds: string[];
  filters: FilterDraft[];
  contains: string;
  signatureRequired: boolean;
  /** IP 白名单的原始多行文本，提交前按行拆分 */
  ipAllowlist: string;
  webhook: AutomationWebhook | null;
}

export const DEFAULT_CRON = "0 9 * * *";

/** 新建定时触发器的默认值：每天 09:00，时区为浏览器时区。 */
export function defaultSchedule(timezone: string): ScheduleDraft {
  return {
    frequency: "daily",
    minute: 0,
    time: "09:00",
    weekdays: [1],
    cron: DEFAULT_CRON,
    timezone,
  };
}

export function newTriggerDraft(
  kind: AutomationTriggerKind,
  key: string,
  timezone: string,
): TriggerDraft {
  return {
    key,
    kind,
    enabled: true,
    schedule: defaultSchedule(timezone),
    statusIds: [],
    filters: [],
    contains: "",
    signatureRequired: false,
    ipAllowlist: "",
    webhook: null,
  };
}

/** 把已有触发器转换成草稿（编辑弹窗打开时使用）。 */
export function draftFromTrigger(
  trigger: AutomationTrigger,
  key: string,
  timezone: string,
): TriggerDraft {
  const schedule = trigger.config.schedule;
  return {
    key,
    id: trigger.id,
    kind: trigger.kind,
    enabled: trigger.enabled,
    schedule: schedule
      ? {
          frequency: schedule.frequency,
          minute: schedule.minute ?? 0,
          time: schedule.time ?? "09:00",
          weekdays: schedule.weekdays ?? [1],
          cron: schedule.cron ?? DEFAULT_CRON,
          timezone: schedule.timezone || timezone,
        }
      : defaultSchedule(timezone),
    statusIds: trigger.config.status_ids ?? [],
    filters: (trigger.config.filters ?? []).map((filter) => ({ ...filter })),
    contains: trigger.config.contains ?? "",
    signatureRequired: trigger.webhook?.signature_required ?? false,
    ipAllowlist: (trigger.webhook?.ip_allowlist ?? []).join("\n"),
    webhook: trigger.webhook,
  };
}

/** 草稿按频率只保留相关字段，避免把无关字段一并提交。 */
export function scheduleConfig(
  schedule: ScheduleDraft,
): AutomationScheduleConfig {
  const base = { frequency: schedule.frequency, timezone: schedule.timezone };
  switch (schedule.frequency) {
    case "hourly":
      return { ...base, minute: schedule.minute };
    case "daily":
    case "weekdays":
      return { ...base, time: schedule.time };
    case "weekly":
      return { ...base, time: schedule.time, weekdays: schedule.weekdays };
    case "custom":
      return { ...base, cron: schedule.cron };
  }
}

/** 是否合法的 HH:mm。 */
export function isValidTime(value: string): boolean {
  return /^([01]\d|2[0-3]):[0-5]\d$/.test(value);
}

/** 按行拆分 IP 白名单，去掉空行与首尾空白。 */
export function ipLines(text: string): string[] {
  return text
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line !== "");
}

function isIPv4(address: string): boolean {
  const octets = address.split(".");
  return (
    octets.length === 4 &&
    octets.every((octet) => /^\d{1,3}$/.test(octet) && Number(octet) <= 255)
  );
}

function isIPv6(address: string): boolean {
  if (!/^[0-9a-fA-F:]+$/.test(address) || address.split("::").length > 2) {
    return false;
  }
  const groups = address.split(":");
  if (!address.includes("::") && groups.length !== 8) return false;
  if (address.includes("::") && groups.length > 8) return false;
  return groups.every(
    (group) => group === "" || /^[0-9a-fA-F]{1,4}$/.test(group),
  );
}

/** 单行 IPv4 / IPv6 地址或 CIDR 网段。 */
export function isValidIpEntry(line: string): boolean {
  const [address = "", prefix, ...rest] = line.split("/");
  if (rest.length > 0) return false;
  const isV6 = address.includes(":");
  const valid = isV6 ? isIPv6(address) : isIPv4(address);
  if (!valid) return false;
  if (prefix === undefined) return true;
  if (!/^\d{1,3}$/.test(prefix)) return false;
  return Number(prefix) <= (isV6 ? 128 : 32);
}

/** 校验白名单：返回第一条格式错误的行与条数是否超限。 */
export function validateIpAllowlist(text: string): {
  lines: string[];
  invalid: { line: number; value: string } | null;
  tooMany: boolean;
} {
  const lines = ipLines(text);
  const bad = lines.findIndex((line) => !isValidIpEntry(line));
  return {
    lines,
    invalid: bad < 0 ? null : { line: bad + 1, value: lines[bad]! },
    tooMany: lines.length > MAX_IP_ENTRIES,
  };
}

/** 触发器是否可以提交：定时需要合法的时间或 cron，状态变更需要至少一个状态，评论需要内容。 */
export function isTriggerValid(draft: TriggerDraft): boolean {
  switch (draft.kind) {
    case "schedule": {
      const { schedule } = draft;
      if (schedule.frequency === "custom") return parseCron(schedule.cron).ok;
      if (schedule.frequency === "hourly")
        return (
          Number.isInteger(schedule.minute) &&
          schedule.minute >= 0 &&
          schedule.minute <= 59
        );
      if (schedule.frequency === "weekly" && schedule.weekdays.length === 0)
        return false;
      return isValidTime(schedule.time);
    }
    case "task_status":
      return (
        draft.statusIds.length > 0 &&
        draft.filters.length <= MAX_FILTERS &&
        draft.filters.every((filter) => filter.value !== "")
      );
    case "comment": {
      const text = draft.contains.trim();
      return text.length > 0 && text.length <= MAX_COMMENT_LENGTH;
    }
    case "webhook": {
      const allowlist = validateIpAllowlist(draft.ipAllowlist);
      return allowlist.invalid === null && !allowlist.tooMany;
    }
  }
}

/** 草稿转为接口的触发器输入。已有触发器带上 id，编辑时保留它。 */
export function triggerInput(draft: TriggerDraft): AutomationTriggerInput {
  const base = {
    ...(draft.id ? { id: draft.id } : {}),
    kind: draft.kind,
    enabled: draft.enabled,
  };
  switch (draft.kind) {
    case "schedule":
      return {
        ...base,
        config: { schedule: scheduleConfig(draft.schedule) },
      };
    case "task_status":
      return {
        ...base,
        config: {
          status_ids: draft.statusIds,
          filters: draft.filters.map((filter) => ({ ...filter })),
        },
      };
    case "comment":
      return {
        ...base,
        config: { contains: draft.contains.trim() },
      };
    case "webhook":
      return {
        ...base,
        config: {},
        // 只对已有 webhook 触发器有效；新建时不带签名与白名单
        ...(draft.id
          ? {
              signature_required: draft.signatureRequired,
              ip_allowlist: ipLines(draft.ipAllowlist),
            }
          : {}),
      };
  }
}

/** 两种表单共用：标题、指派、触发器都满足时才能提交。 */
export function isAutomationReady(input: {
  title: string;
  assigneeId: string | null;
  triggers: TriggerDraft[];
}): boolean {
  return (
    input.title.trim() !== "" &&
    input.assigneeId !== null &&
    input.triggers.length > 0 &&
    input.triggers.length <= MAX_TRIGGERS &&
    input.triggers.every(isTriggerValid)
  );
}

/** 已有自动化的草稿触发器（编辑弹窗）。 */
export function draftsFromAutomation(
  automation: Automation,
  timezone: string,
): TriggerDraft[] {
  return automation.triggers.map((trigger) =>
    draftFromTrigger(trigger, trigger.id, timezone),
  );
}
