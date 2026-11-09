import type { MessageKey } from "@openfox/i18n";
import type {
  CreateJiraApiImportInput,
  ImportAnalysis,
  ImportColumn,
  ImportColumnMapping,
  ImportConnectionErrorReason,
  ImportField,
  ImportJob,
  ImportNewSpace,
  ImportStatus,
  ImportStatusMapping,
  ImportUserMapping,
  TaskStatus,
} from "@openfox/api";

// 导入的前端常量与纯函数。来源名称是品牌名，不进 i18n。

/** 当前可用的导入来源。 */
export const AVAILABLE_SOURCES = [
  "Jira",
  "TAPD",
  "禅道",
  "Teambition",
  "PingCode",
  "Shortcut",
] as const;

/** 即将推出的来源，按固定顺序展示。 */
export const COMING_SOON_SOURCES = [
  "云效",
  "Linear",
  "ONES",
  "Asana",
  "ClickUp",
] as const;

/** 记录列表默认条数与每次「加载更多」的条数。 */
export const PAGE_SIZE = 5;
export const MAX_FILE_BYTES = 10 * 1024 * 1024;

/** 有执行中的导入时 4 秒刷新，否则 30 秒。 */
export const pollInterval = (hasActive: boolean | undefined) =>
  hasActive ? 4000 : 30000;

/** 列映射下拉里的任务字段（顺序即下拉顺序）。 */
export const FIELD_ORDER: ImportField[] = [
  "ignore",
  "title",
  "source_id",
  "source_key",
  "type",
  "status",
  "priority",
  "assignee",
  "reporter",
  "creator",
  "description",
  "parent",
  "labels",
  "comment",
  "created_at",
  "updated_at",
  "start_date",
  "due_date",
  "source_container",
  "story_points",
  "source_url",
  "relations",
  "attachment",
  "sprint",
  "resolution",
  "watchers",
  "original_estimate",
  "custom",
];

/** 不支持导入的字段，下拉里标「暂不导入」。 */
export const UNSUPPORTED_FIELDS = new Set<ImportField>([
  "relations",
  "attachment",
  "sprint",
  "resolution",
  "watchers",
  "original_estimate",
]);

/** 可以映射到多列的字段；其余字段最多一列。 */
const MULTI_FIELDS = new Set<ImportField>([
  "ignore",
  "labels",
  "comment",
  "custom",
  ...UNSUPPORTED_FIELDS,
]);

export type ColumnIssue =
  | {
      code:
        | "title_column_missing"
        | "custom_name_invalid"
        | "custom_name_duplicate";
    }
  | { code: "source_id_column_missing" }
  | { code: "duplicate_column_field"; field: ImportField };

/** 列映射校验：标题列、源 ID 列各有且只有一列，单值字段不能重复映射。 */
export function validateColumns(
  columns: readonly ImportColumnMapping[],
): ColumnIssue[] {
  const count = new Map<ImportField, number>();
  for (const column of columns)
    count.set(column.field, (count.get(column.field) ?? 0) + 1);
  const issues: ColumnIssue[] = [];
  const names = columns
    .filter((c) => c.field === "custom")
    .map((c) => c.custom_name?.trim().toLowerCase() ?? "");
  if (names.some((name) => !name || Array.from(name).length > 64))
    issues.push({ code: "custom_name_invalid" });
  if (new Set(names.filter(Boolean)).size < names.filter(Boolean).length)
    issues.push({ code: "custom_name_duplicate" });
  if (!count.get("title")) issues.push({ code: "title_column_missing" });
  if (!count.get("source_id"))
    issues.push({ code: "source_id_column_missing" });
  for (const [field, n] of count)
    if (n > 1 && !MULTI_FIELDS.has(field))
      issues.push({ code: "duplicate_column_field", field });
  return issues;
}

/** 用文件列（含建议字段）生成初始列映射。 */
export function defaultColumns(
  columns: readonly ImportColumn[],
): ImportColumnMapping[] {
  return columns.map((column) => ({
    index: column.index,
    field: column.suggested_field,
    ...(column.suggested_field === "custom"
      ? { custom_name: column.header }
      : {}),
  }));
}

/** 来源状态名是否像「已取消」。 */
const CANCEL_PATTERN = /cancel|won't|wont|取消/i;

/** 新建状态的默认类别：done 为 DONE，含 cancel 字样为 CANCELLED，否则 TODO。 */
export function defaultCategory(source: string, done: boolean) {
  if (CANCEL_PATTERN.test(source)) return "CANCELLED" as const;
  return done ? ("DONE" as const) : ("TODO" as const);
}

/** 可新建的状态类别（不含分拣与重复）。 */
export const NEW_CATEGORIES = [
  "BACKLOG",
  "TODO",
  "IN_PROGRESS",
  "DONE",
  "CANCELLED",
] as const;

/** 按分析结果补齐状态映射：保留已有映射，缺的用建议状态或新建状态。 */
export function reconcileStatuses(
  analysis: ImportAnalysis,
  current: readonly ImportStatusMapping[],
): ImportStatusMapping[] {
  const byName = new Map(current.map((item) => [item.source, item]));
  return analysis.statuses.map((item) => {
    const kept = byName.get(item.source);
    if (kept) return kept;
    if (item.suggested_status_id)
      return { source: item.source, status_id: item.suggested_status_id };
    return {
      source: item.source,
      new_name: item.source,
      new_category: defaultCategory(item.source, item.done),
    };
  });
}

/** 按分析结果补齐用户映射：默认匹配到成员，否则创建外部账号。 */
export function reconcileUsers(
  analysis: ImportAnalysis,
  current: readonly ImportUserMapping[],
): ImportUserMapping[] {
  const byId = new Map(current.map((item) => [item.source_id, item]));
  return analysis.users.map((item) => {
    const kept = byId.get(item.source_id);
    if (kept) return kept;
    if (item.suggested_user_id)
      return {
        source_id: item.source_id,
        mode: "member" as const,
        user_id: item.suggested_user_id,
      };
    return { source_id: item.source_id, mode: "external" as const };
  });
}

/** 状态映射是否完整（新建状态必须有名称）。 */
export function statusMappingsValid(items: readonly ImportStatusMapping[]) {
  return items.every((item) =>
    item.status_id ? true : Boolean(item.new_name?.trim() && item.new_category),
  );
}

const EMAIL = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;

/** 用户映射是否完整：member 要选成员，invite 要有合法邮箱。 */
export function userMappingsValid(items: readonly ImportUserMapping[]) {
  return items.every((item) =>
    item.mode === "member"
      ? Boolean(item.user_id)
      : item.mode === "invite"
        ? EMAIL.test((item.email ?? "").trim())
        : true,
  );
}

/** 确认页「迁移 / 跳过」两列的任务数。 */
export function migrationCounts(
  analysis: ImportAnalysis,
  options: { import_open: boolean; import_done: boolean },
) {
  return {
    migrate: {
      open: options.import_open ? analysis.counts.open : 0,
      done: options.import_done ? analysis.counts.done : 0,
    },
    skip: {
      open: options.import_open ? 0 : analysis.counts.open,
      done: options.import_done ? 0 : analysis.counts.done,
    },
  };
}

/** 新建状态的个数。 */
export const newStatusCount = (items: readonly ImportStatusMapping[]) =>
  items.filter((item) => !item.status_id).length;

export const ACTIVE_STATUSES: readonly ImportStatus[] = [
  "QUEUED",
  "RUNNING",
  "CANCEL_REQUESTED",
];

/** 进入终态后不再轮询。 */
export const isTerminal = (status: ImportStatus) =>
  !ACTIVE_STATUSES.includes(status) && status !== "DRAFT";

/** 记录行状态徽标文案。 */
export function statusLabelKey(status: ImportStatus): MessageKey {
  switch (status) {
    case "SUCCEEDED":
      return "imports.status.succeeded";
    case "FAILED":
      return "imports.status.failed";
    case "CANCELLED":
      return "imports.status.cancelled";
    case "ROLLED_BACK":
      return "imports.status.rolledBack";
    case "DRAFT":
    case "DISCARDED":
      return "imports.status.draft";
    default:
      return "imports.status.running";
  }
}

export function statusTone(status: ImportStatus) {
  switch (status) {
    case "SUCCEEDED":
      return "success" as const;
    case "FAILED":
      return "danger" as const;
    case "QUEUED":
    case "RUNNING":
    case "CANCEL_REQUESTED":
      return "info" as const;
    default:
      return "muted" as const;
  }
}

export function rollbackReasonKey(
  reason: ImportJob["rollback"]["reason"],
): MessageKey | null {
  switch (reason) {
    case "not_finished":
      return "imports.rollbackReason.not_finished";
    case "expired":
      return "imports.rollbackReason.expired";
    case "rolled_back":
      return "imports.rollbackReason.rolled_back";
    case "nothing_created":
      return "imports.rollbackReason.nothing_created";
    default:
      return null;
  }
}

/** 进行中页面的步骤顺序。 */
export const RUN_STEPS = [
  "queued",
  "validating",
  "importing",
  "linking",
  "finishing",
] as const;

/** 当前所在步骤的序号（0 起）：排队 → 验证 → 导入任务 → 关联父子 → 收尾。 */
export function runStepIndex(job: Pick<ImportJob, "status" | "phase">) {
  if (job.status === "QUEUED") return 0;
  switch (job.phase) {
    case "validating":
    case "fetching":
      return 1;
    case "importing":
      return 2;
    case "linking":
      return 3;
    case "finishing":
      return 4;
    default:
      return job.status === "SUCCEEDED" ? 5 : 1;
  }
}

/** 已有状态按类别分组排序，供下拉使用。 */
export function sortedStatuses(statuses: readonly TaskStatus[]) {
  const order = [
    "TRIAGE",
    "BACKLOG",
    "TODO",
    "IN_PROGRESS",
    "DONE",
    "CANCELLED",
    "DUPLICATE",
  ];
  return statuses
    .filter((status) => !status.archived_at)
    .slice()
    .sort(
      (a, b) =>
        order.indexOf(a.category) - order.indexOf(b.category) ||
        a.position - b.position,
    );
}

// ---------------- Jira API 导入 ----------------

/** 目标空间下拉里「新建空间…」的占位值。 */
export const NEW_SPACE = "__new_space__";

export type ImportMode = "csv" | "api";

export const CSV_STEPS = [
  "welcome",
  "upload",
  "preview",
  "columns",
  "statuses",
  "users",
  "options",
  "confirm",
] as const;

export const API_STEPS = [
  "welcome",
  "config",
  "check",
  "select",
  "statuses",
  "users",
  "options",
  "confirm",
] as const;

export type WizardStep =
  (typeof CSV_STEPS)[number] | (typeof API_STEPS)[number] | "running";

export interface NewSpaceDraft {
  name: string;
  prefix: string;
  visibility: ImportNewSpace["visibility"];
}

export const EMPTY_NEW_SPACE: NewSpaceDraft = {
  name: "",
  prefix: "",
  visibility: "PRIVATE",
};

/** 目标空间是否填写完整：选了已有空间，或新建空间有名称。 */
export function targetValid(spaceId: string, newSpace: NewSpaceDraft) {
  return spaceId === NEW_SPACE
    ? Boolean(newSpace.name.trim())
    : Boolean(spaceId);
}

/** 新建空间的请求字段；前缀为空时不带（由服务端取默认值）。 */
export function newSpacePayload(
  newSpace: NewSpaceDraft,
  fallbackPrefix = "",
): ImportNewSpace {
  const prefix = (newSpace.prefix || fallbackPrefix).trim();
  return {
    name: newSpace.name.trim(),
    ...(prefix ? { prefix } : {}),
    visibility: newSpace.visibility,
  };
}

export type JiraDeployment = "cloud" | "server";
export type JiraAuthKind = "api_token" | "pat" | "password";

export interface ApiForm {
  deployment: JiraDeployment;
  siteUrl: string;
  /** server 的认证方式；cloud 固定 api_token。 */
  serverAuth: "pat" | "password";
  email: string;
  username: string;
  /** API 令牌 / 个人访问令牌 / 密码。只在内存里，提交后立即清空。 */
  secret: string;
  projectKey: string;
  jql: string;
}

export const EMPTY_API_FORM: ApiForm = {
  deployment: "cloud",
  siteUrl: "",
  serverAuth: "pat",
  email: "",
  username: "",
  secret: "",
  projectKey: "",
  jql: "",
};

/** 项目 Key 的大写形式，作为新空间前缀的默认值。 */
export const defaultPrefix = (projectKey: string) =>
  projectKey.trim().toUpperCase();

/** 站点地址只接受 https，不带用户名密码。 */
export function siteUrlValid(raw: string) {
  const value = raw.trim();
  if (!/^https:\/\//i.test(value)) return false;
  try {
    const url = new URL(value);
    return Boolean(url.hostname) && !url.username && !url.password;
  } catch {
    return false;
  }
}

/** 配置表单是否可以提交。 */
export function apiFormValid(form: ApiForm) {
  if (!siteUrlValid(form.siteUrl) || !form.projectKey.trim()) return false;
  if (!form.secret) return false;
  if (form.deployment === "cloud") return Boolean(form.email.trim());
  return form.serverAuth === "pat" || Boolean(form.username.trim());
}

/** 组装 createJiraApiImport 请求体（凭证只出现在这里）。 */
export function buildApiRequest(
  form: ApiForm,
  target: { spaceId: string; newSpace: NewSpaceDraft },
): CreateJiraApiImportInput {
  const kind: JiraAuthKind =
    form.deployment === "cloud" ? "api_token" : form.serverAuth;
  const auth: CreateJiraApiImportInput["auth"] = { kind, secret: form.secret };
  if (kind === "api_token") auth.email = form.email.trim();
  if (kind === "password") auth.username = form.username.trim();
  const jql = form.jql.trim();
  return {
    deployment: form.deployment,
    site_url: form.siteUrl.trim(),
    auth,
    project_key: form.projectKey.trim(),
    ...(jql ? { jql } : {}),
    ...(target.spaceId === NEW_SPACE
      ? {
          new_space: newSpacePayload(
            target.newSpace,
            defaultPrefix(form.projectKey),
          ),
        }
      : { space_id: target.spaceId }),
  };
}

export const CONNECTION_REASONS: readonly ImportConnectionErrorReason[] = [
  "auth_failed",
  "site_not_found",
  "project_not_found",
  "jql_invalid",
  "unreachable",
  "dns_failed",
  "tls_failed",
  "blocked_address",
  "invalid_url",
  "rate_limited",
  "too_many_issues",
];

export const isConnectionReason = (
  value: unknown,
): value is ImportConnectionErrorReason =>
  CONNECTION_REASONS.includes(value as ImportConnectionErrorReason);

/** 重新导入 API 记录时的预填（不含凭证）。 */
export interface ApiPrefill {
  deployment: JiraDeployment;
  site: string;
  projectKey: string;
  jql: string;
  authKind: JiraAuthKind;
  /** 认证用的邮箱或用户名，不是凭证 */
  user: string;
}

/** 优先用记录里的连接设置；旧记录没有时，由 source 和 source_label 推出站点与部署方式。 */
export function prefillFromJob(job: ImportJob): ApiPrefill {
  const api = job.api;
  const deployment: JiraDeployment =
    api?.deployment ?? (job.source === "jira_server" ? "server" : "cloud");
  return {
    deployment,
    site:
      api?.site_url ?? (job.source_label ? `https://${job.source_label}` : ""),
    projectKey: api?.project_key ?? "",
    jql: api?.jql ?? "",
    authKind: api?.auth_kind ?? (deployment === "cloud" ? "api_token" : "pat"),
    user: api?.auth_user ?? "",
  };
}

/** 预填值转成表单初值（密文始终为空）。 */
export function formFromPrefill(prefill: ApiPrefill): ApiForm {
  return {
    ...EMPTY_API_FORM,
    deployment: prefill.deployment,
    siteUrl: prefill.site,
    projectKey: prefill.projectKey,
    jql: prefill.jql,
    serverAuth: prefill.authKind === "password" ? "password" : "pat",
    email: prefill.authKind === "api_token" ? prefill.user : "",
    username: prefill.authKind === "password" ? prefill.user : "",
  };
}

/** 草稿还在拉取中。 */
export const isFetching = (job: Pick<ImportJob, "status" | "phase">) =>
  job.status === "DRAFT" && job.phase === "fetching";

/** API 拉取失败：被丢弃且带错误码。 */
export const isFetchFailed = (job: Pick<ImportJob, "status" | "error_code">) =>
  job.status === "DISCARDED" && Boolean(job.error_code);
