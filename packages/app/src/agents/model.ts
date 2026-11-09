import type {
  AgentAvatarValue,
  AgentConfigInput,
  AgentConfigVersion,
  AgentDirectoryEntry,
  AgentDirectoryMode,
  CollaborationMode,
  Device,
} from "@openfox/api";

import { collaborationModeOf } from "./collaboration";
import { isUploadedIconURL } from "../icons/uploaded";

// 全部 Agent、设置抽屉、成员页签共用的纯函数：目录筛选与分组、Runtime 可用性、单位换算、邮箱解析。

export type DirectoryFilter = "all" | "agent" | "crew" | "mine";

/**
 * Agent 的编辑权限（对话输入框「更换电脑」用，判断与设置抽屉技能区块一致）：未归档，且是所有者或组织所有者 / 管理员。
 * 服务端的 Agent 编辑权限判断是最终依据。
 */
export function canEditAgent(
  agent: { archived_at?: string | null; owner_user_id: string },
  meId: string | undefined,
  orgRole: string | undefined,
): boolean {
  return (
    !agent.archived_at &&
    (agent.owner_user_id === meId || orgRole === "OWNER" || orgRole === "ADMIN")
  );
}

/** 筛选胶囊 + 搜索：名称、职位、小队成员名（不区分大小写）。 */
export function filterDirectory(
  entries: AgentDirectoryEntry[],
  filter: DirectoryFilter,
  query: string,
): AgentDirectoryEntry[] {
  const needle = query.trim().toLowerCase();
  return entries.filter((entry) => {
    if (filter === "agent" && entry.type !== "agent") return false;
    if (filter === "crew" && entry.type !== "crew") return false;
    if (filter === "mine" && !entry.mine) return false;
    if (!needle) return true;
    const haystack = [
      entry.name,
      entry.title,
      ...(entry.members ?? []).map((member) => member.name),
    ];
    return haystack.some((text) => text?.toLowerCase().includes(needle));
  });
}

export interface DirectoryGroup {
  /** pinned / unassigned / space:<id> */
  id: string;
  kind: "pinned" | "space" | "unassigned";
  /** 空间名称（kind = space 时） */
  name: string;
  entries: AgentDirectoryEntry[];
}

/**
 * 分组：置顶在最前，其余按空间分组（按空间列表顺序，列表外的空间按名称），「未归属空间」放最后。
 * 组内保持接口返回的顺序；空组不输出。
 */
export function groupDirectory(
  entries: AgentDirectoryEntry[],
  spaceOrder: ReadonlyArray<{ id: string }>,
): DirectoryGroup[] {
  const pinned = entries.filter((entry) => entry.pinned);
  const bySpace = new Map<string, DirectoryGroup>();
  const unassigned: AgentDirectoryEntry[] = [];
  for (const entry of entries) {
    if (entry.pinned) continue;
    if (!entry.space_id) {
      unassigned.push(entry);
      continue;
    }
    const group = bySpace.get(entry.space_id) ?? {
      id: `space:${entry.space_id}`,
      kind: "space" as const,
      name: entry.space_name,
      entries: [],
    };
    group.entries.push(entry);
    bySpace.set(entry.space_id, group);
  }
  const rank = new Map(spaceOrder.map((space, index) => [space.id, index]));
  const spaces = [...bySpace.entries()]
    .sort(([a, ga], [b, gb]) => {
      const ra = rank.get(a) ?? Number.MAX_SAFE_INTEGER;
      const rb = rank.get(b) ?? Number.MAX_SAFE_INTEGER;
      return ra !== rb ? ra - rb : ga.name.localeCompare(gb.name);
    })
    .map(([, group]) => group);
  const groups: DirectoryGroup[] = [];
  if (pinned.length)
    groups.push({ id: "pinned", kind: "pinned", name: "", entries: pinned });
  groups.push(...spaces);
  if (unassigned.length)
    groups.push({
      id: "unassigned",
      kind: "unassigned",
      name: "",
      entries: unassigned,
    });
  return groups;
}

/** 支持的 Runtime（Codex、Claude Code、OpenCode、pi），按展示顺序。 */
export const RUNTIME_PROVIDERS = [
  "codex",
  "claude-code",
  "opencode",
  "pi",
] as const;

/** 默认 Runtime：小助手等没有显式选择的地方优先使用。 */
export const SUPPORTED_RUNTIME = "codex";

export function isSupportedRuntime(provider: string): boolean {
  return (RUNTIME_PROVIDERS as readonly string[]).includes(
    provider.toLowerCase(),
  );
}

export type RuntimeBlock =
  "offline" | "space" | "personal" | "missing" | "login" | "unavailable";

export interface RuntimeChoice {
  /** <deviceId>:<provider> */
  value: string;
  deviceId: string;
  grantId: string;
  deviceName: string;
  provider: string;
  models: Device["runtimes"][number]["models"];
  /** 不可用原因；null 表示可选 */
  block: RuntimeBlock | null;
  /** Runtime 自带的不可用原因（如登录失效） */
  reason: string | null;
  maxSlots: number;
}

/** 电脑授权是否允许该空间与所有者（与后端 DEVICE_SCOPE_MISMATCH 规则一致）。 */
export function deviceScopeBlock(
  device: Pick<Device, "grant_scope" | "grant_space_id" | "grant_user_id">,
  spaceId: string,
  ownerId: string,
): RuntimeBlock | null {
  if (device.grant_scope === "SPACE" && device.grant_space_id !== spaceId)
    return "space";
  if (device.grant_scope === "PERSONAL" && device.grant_user_id !== ownerId)
    return "personal";
  return null;
}

/**
 * 创建 Agent / 电脑 & Runtime 的可选项：每台电脑上报的每个 Runtime 一项（Codex 总是有一项，
 * 未检测到时置灰为「缺失」；其他 Runtime 只有电脑上报了才出现）。
 * 只有在线、授权允许该空间与所有者、Runtime 可运行的项可选，其余置灰并带原因。
 */
export function runtimeChoices(
  devices: Device[],
  spaceId: string,
  ownerId: string,
): RuntimeChoice[] {
  return devices.flatMap((device) =>
    RUNTIME_PROVIDERS.flatMap((provider) => {
      const runtime = device.runtimes.find(
        (item) => item.provider === provider,
      );
      if (!runtime && provider !== SUPPORTED_RUNTIME) return [];
      const block: RuntimeBlock | null = !device.online
        ? "offline"
        : (deviceScopeBlock(device, spaceId, ownerId) ??
          (!runtime
            ? "missing"
            : runtime.runnable
              ? null
              : runtime.status === "NEEDS_LOGIN"
                ? "login"
                : "unavailable"));
      return [
        {
          value: `${device.id}:${provider}`,
          deviceId: device.id,
          grantId: device.grant_id,
          deviceName: device.name,
          provider,
          models: runtime?.models ?? [],
          block,
          reason: runtime?.reason ?? null,
          maxSlots: device.max_slots,
        },
      ];
    }),
  );
}

const runtimeLabels: Record<string, string> = {
  codex: "Codex",
  "claude-code": "Claude Code",
  opencode: "OpenCode",
  pi: "pi",
};

/** Runtime 显示名 */
export function runtimeLabel(provider: string | null | undefined): string {
  if (!provider) return "";
  return (
    runtimeLabels[provider.toLowerCase()] ??
    provider.charAt(0).toUpperCase() + provider.slice(1)
  );
}

// ---------- 工作方式 ----------

export type WorkMode = "git" | "explicit" | "none";

export const workModeToDirectory: Record<WorkMode, AgentDirectoryMode> = {
  git: "WORKSPACE",
  explicit: "EXPLICIT",
  none: "NONE",
};

/** 手动指定目录：后端只接受相对路径（不能以 / 或 ~ 开头，不能含 \、. 或 ..）。 */
export function validDirectoryRoot(root: string): boolean {
  const value = root.trim();
  if (!value || value.length > 256) return false;
  if (value.startsWith("/") || value.startsWith("~") || value.includes("\\"))
    return false;
  return value
    .split("/")
    .every((part) => part && part !== "." && part !== "..");
}

export function parseDirectoryRoots(text: string): string[] {
  return text
    .split("\n")
    .map((line) => line.trim())
    .filter(Boolean);
}

// ---------- 单位换算（运行设置） ----------

export const RUN_TIMEOUT_MINUTES = { min: 1, max: 480 } as const;
export const QUEUE_TIMEOUT_HOURS = { min: 0.25, max: 24 } as const;

export const minutesToSeconds = (minutes: number) => Math.round(minutes * 60);
export const secondsToMinutes = (seconds: number) =>
  Math.round((seconds / 60) * 100) / 100;
export const hoursToSeconds = (hours: number) => Math.round(hours * 3600);
export const secondsToHours = (seconds: number) =>
  Math.round((seconds / 3600) * 100) / 100;

/** 数字输入：在范围内返回数值，否则 null。 */
export function parseInRange(
  text: string,
  range: { min: number; max: number },
): number | null {
  if (!text.trim()) return null;
  const value = Number(text);
  if (!Number.isFinite(value) || value < range.min || value > range.max)
    return null;
  return value;
}

// ---------- 自定义参数 ----------

/** 按空白切分命令行参数，单 / 双引号内的空格保留。 */
export function splitArgs(text: string): string[] {
  const out: string[] = [];
  let current = "";
  let quote: '"' | "'" | null = null;
  let started = false;
  for (const ch of text) {
    if (quote) {
      if (ch === quote) quote = null;
      else current += ch;
      continue;
    }
    if (ch === '"' || ch === "'") {
      quote = ch;
      started = true;
    } else if (/\s/.test(ch)) {
      if (started) out.push(current);
      current = "";
      started = false;
    } else {
      current += ch;
      started = true;
    }
  }
  if (started) out.push(current);
  return out;
}

export interface ArgRow {
  key: string;
  value: string;
}

/** custom_args 数组 → 参数表：以 - 开头的项与紧随的非 - 项配成一行。 */
export function argsToRows(args: string[]): ArgRow[] {
  const rows: ArgRow[] = [];
  for (let i = 0; i < args.length; i++) {
    const key = args[i]!;
    const next = args[i + 1];
    if (key.startsWith("-") && next !== undefined && !next.startsWith("-")) {
      rows.push({ key, value: next });
      i += 1;
    } else rows.push({ key, value: "" });
  }
  return rows;
}

export function rowsToArgs(rows: ArgRow[]): string[] {
  return rows.flatMap((row) => {
    const key = row.key.trim();
    if (!key) return [];
    const value = row.value.trim();
    return value ? [key, value] : [key];
  });
}

// ---------- 邀请邮箱 ----------

const EMAIL = /^[^\s@,]+@[^\s@,]+\.[^\s@,]+$/;

export function isValidEmail(value: string): boolean {
  return EMAIL.test(value.trim());
}

/** 输入框文字按逗号、空格切分为邮箱；去重（不区分大小写）。 */
export function splitEmails(text: string): string[] {
  return text
    .split(/[\s,，;；]+/)
    .map((item) => item.trim())
    .filter(Boolean);
}

export function mergeEmails(current: string[], added: string[]): string[] {
  const seen = new Set(current.map((item) => item.toLowerCase()));
  const next = [...current];
  for (const item of added) {
    const key = item.toLowerCase();
    if (seen.has(key)) continue;
    seen.add(key);
    next.push(item);
  }
  return next;
}

// ---------- 成员页签 ?contact= ----------

export type ContactKind = "agent" | "crew" | "user";

export function parseContact(
  value: string | null,
): { kind: ContactKind; id: string } | null {
  if (!value) return null;
  const [kind, id] = value.split(":");
  if (!id || (kind !== "agent" && kind !== "crew" && kind !== "user"))
    return null;
  return { kind, id };
}

// ---------- 头像 ----------

export interface AvatarSpec {
  kind: "icon" | "mood" | "upload";
  value: string;
  color: string;
}

export const AVATAR_COLORS = [
  "#22C55E",
  "#F97316",
  "#EAB308",
  "#3B82F6",
  "#8B5CF6",
  "#EF4444",
  "#06B6D4",
  "#EC4899",
  "#64748B",
] as const;

/** Agent 头像可选的 Moods 表情。 */
export const AVATAR_MOODS = [
  "ᵔᴗᵔ",
  "°o°",
  "•ᴗ•",
  "¬‿¬",
  "˘ᵕ˘",
  "•̀ᴗ•́",
  "^o^",
  "·_·",
  ">ᴗ<",
  "ʘ‿ʘ",
  "-ᴗ-",
] as const;

/** 解析接口里的头像 JSON；格式不认识时返回 null（显示默认头像）。 */
export function readAvatar(
  value: AgentAvatarValue | undefined,
): AvatarSpec | null {
  if (!value || typeof value !== "object") return null;
  const record = value as Record<string, unknown>;
  const kind = record.kind === "mood" ? "mood" : "icon";
  const color =
    typeof record.color === "string" && /^#[0-9a-fA-F]{6}$/.test(record.color)
      ? record.color
      : AVATAR_COLORS[0];
  const text = typeof record.value === "string" ? record.value : "";
  if (record.kind === "upload")
    return isUploadedIconURL(text)
      ? { kind: "upload", value: text, color }
      : null;
  return { kind, value: kind === "mood" ? text.slice(0, 8) : "bot", color };
}

export function writeAvatar(spec: AvatarSpec): AgentAvatarValue {
  return { kind: spec.kind, value: spec.value, color: spec.color };
}

// ---------- 配置版本 ----------

/** 配置字段的默认值（与后端缺省一致；生成类型把带默认值的字段标为必填）。 */
export const CONFIG_DEFAULTS = {
  max_concurrency: 1,
  queue_timeout_seconds: 3600,
  run_timeout_seconds: 3600,
  chat_concurrency: "DEFAULT",
  workdir_isolation: "WORKTREE",
  approval_mode: "FOLLOW_RUNTIME",
  collaboration_mode: "FOLLOW_RUNTIME",
  git_author_name_mode: "INHERIT",
  git_author_email_mode: "INHERIT",
  git_co_author_enabled: null,
} as const satisfies Partial<AgentConfigInput>;

/** 当前配置版本 → 新版本的完整输入（新建版本必须传完整配置）。 */
export function configInputFrom(config: AgentConfigVersion): AgentConfigInput {
  return {
    instructions: config.instructions,
    computer_id: config.computer_id,
    grant_id: config.grant_id,
    runtime: config.runtime,
    model: config.model,
    max_concurrency: config.max_concurrency,
    queue_timeout_seconds: config.queue_timeout_seconds,
    run_timeout_seconds: config.run_timeout_seconds,
    directory_mode: config.directory_mode,
    directory_roots: config.directory_roots,
    chat_concurrency: config.chat_concurrency,
    chat_concurrency_value:
      config.chat_concurrency === "MANUAL"
        ? config.chat_concurrency_value
        : null,
    workdir_isolation: config.workdir_isolation,
    approval_mode: config.approval_mode,
    // 历史值 FOLLOW_AGENT 按 FOLLOW_RUNTIME 提交；无法识别的值原样提交（服务端返回 400，由用户在选择器里重新选择）
    collaboration_mode: (collaborationModeOf(config.collaboration_mode) ??
      config.collaboration_mode) as CollaborationMode,
    custom_args: config.custom_args,
    git_author_name_mode: config.git_author_name_mode,
    git_author_name: config.git_author_name,
    git_author_email_mode: config.git_author_email_mode,
    git_author_email: config.git_author_email,
    git_co_author_enabled: config.git_co_author_enabled ?? null,
  };
}

/** daemon 丢弃的环境变量（只注入 Runtime 子进程，这些名字会被忽略；大小写按原样匹配） */
const RESERVED_ENV_NAMES = new Set(["CODEX_HOME", "HOME", "PATH"]);
const RESERVED_ENV_PREFIXES = ["OPENFOX_", "CODEX_", "DYLD_", "LD_"];

export function isReservedEnvName(name: string): boolean {
  return (
    RESERVED_ENV_NAMES.has(name) ||
    RESERVED_ENV_PREFIXES.some((prefix) => name.startsWith(prefix))
  );
}
