import type { Device } from "@openfox/api";
import type { MessageKey, Translate } from "@openfox/i18n";
import type { IconName } from "@openfox/ui";

/** 列表与侧栏展示的电脑状态：在线且有可运行 Runtime / 在线但 Runtime 不可用 / 离线 */
export type ComputerStatus = "online" | "noRuntime" | "offline";

export function computerStatus(
  device: Pick<Device, "online" | "runtimes">,
): ComputerStatus {
  if (!device.online) return "offline";
  return device.runtimes.some((runtime) => runtime.runnable)
    ? "online"
    : "noRuntime";
}

export const statusLabel: Record<ComputerStatus, MessageKey> = {
  online: "computers.status.online",
  noRuntime: "computers.status.noRuntime",
  offline: "computers.status.offline",
};

/** 状态点按绿 / 黄 / 灰显示连接状态。 */
export const statusDotClass: Record<ComputerStatus, string> = {
  online: "bg-success",
  noRuntime: "bg-warning",
  offline: "bg-fg-subtle",
};

/** 侧栏「电脑」入口的状态点：至少一台可用为绿；有在线电脑但 Runtime 不可用为黄；否则为灰。 */
export function railStatus(
  devices: ReadonlyArray<Pick<Device, "online" | "runtimes">>,
): ComputerStatus {
  let online = false;
  for (const device of devices) {
    const status = computerStatus(device);
    if (status === "online") return "online";
    if (status === "noRuntime") online = true;
  }
  return online ? "noRuntime" : "offline";
}

/** 分区：个人授权 = 私有；组织 / 空间授权 = 共享 */
export function computerPartition(
  device: Pick<Device, "grant_scope">,
): "shared" | "private" {
  return device.grant_scope === "PERSONAL" ? "private" : "shared";
}

const statusRank: Record<ComputerStatus, number> = {
  online: 0,
  noRuntime: 1,
  offline: 2,
};

/** 排序：自己的在前 → 所有者 → 状态 → 名称。 */
export function sortComputers<T extends Device>(
  devices: readonly T[],
  meId: string | undefined,
): T[] {
  const collator = new Intl.Collator(undefined, {
    numeric: true,
    sensitivity: "base",
  });
  return [...devices].sort((a, b) => {
    const mineA = meId !== undefined && a.owner_user_id === meId ? 0 : 1;
    const mineB = meId !== undefined && b.owner_user_id === meId ? 0 : 1;
    if (mineA !== mineB) return mineA - mineB;
    const owner = collator.compare(a.owner_email ?? "", b.owner_email ?? "");
    if (owner !== 0) return owner;
    const status =
      statusRank[computerStatus(a)] - statusRank[computerStatus(b)];
    if (status !== 0) return status;
    return collator.compare(a.name, b.name) || a.id.localeCompare(b.id);
  });
}

/** 可见性文案：私有 / 组织 / 空间名 */
export function visibilityText(
  device: Pick<Device, "grant_scope" | "grant_space_name">,
  t: Translate,
): string {
  if (device.grant_scope === "PERSONAL")
    return t("computers.visibility.private");
  if (device.grant_scope === "ORGANIZATION")
    return t("computers.visibility.organization");
  return device.grant_space_name || t("computers.visibility.space");
}

/** 操作系统名称：darwin → macOS 等；未知取值原样显示 */
export function osName(os: string): string {
  const value = os.toLowerCase();
  if (value === "darwin" || value === "macos" || value.startsWith("mac"))
    return "macOS";
  if (value.startsWith("win")) return "Windows";
  if (value === "linux") return "Linux";
  return os;
}

/** 系统(架构)，例如 macOS (arm64) */
export function systemText(
  device: Pick<Device, "operating_system" | "architecture">,
): string {
  const os = osName(device.operating_system || "");
  if (!os) return device.architecture || "";
  return device.architecture ? `${os} (${device.architecture})` : os;
}

export function osIcon(os: string): IconName {
  const name = osName(os);
  if (name === "macOS") return "os-macos";
  if (name === "Windows") return "os-windows";
  if (name === "Linux") return "os-linux";
  return "monitor";
}

/** 已有图标的 Runtime；其他 Runtime 用通用终端图标 */
const runtimeIcons: Record<string, IconName> = {
  codex: "runtime-codex",
  "claude-code": "runtime-claude-code",
  claude: "runtime-claude-code",
  "gemini-cli": "runtime-gemini-cli",
  gemini: "runtime-gemini-cli",
  opencode: "runtime-opencode",
  kiro: "runtime-kiro",
  codebuddy: "runtime-codebuddy",
  auggie: "runtime-auggie",
  pi: "runtime-pi",
};

export function runtimeIcon(provider: string): IconName {
  return runtimeIcons[provider.toLowerCase()] ?? "terminal";
}

const runtimeNames: Record<string, string> = {
  codex: "Codex",
  "claude-code": "Claude Code",
  "gemini-cli": "Gemini CLI",
  opencode: "OpenCode",
  pi: "pi",
};

export function runtimeName(provider: string): string {
  return runtimeNames[provider.toLowerCase()] ?? provider;
}

/** 支持的四种 Runtime，按展示顺序；未检测到时展示各自的安装指南 */
export const RUNTIME_PROVIDERS = [
  "codex",
  "claude-code",
  "opencode",
  "pi",
] as const;
export const SUPPORTED_RUNTIME = "codex";
export const CODEX_INSTALL_COMMAND = "npm install -g @openai/codex";
export const CODEX_INSTALL_DOCS = "https://github.com/openai/codex";

/** 各 Runtime 的 npm 安装命令与官方文档（出处：各项目官方 README / 文档） */
export const RUNTIME_INSTALL: Record<
  string,
  { command: string; docs: string }
> = {
  codex: { command: CODEX_INSTALL_COMMAND, docs: CODEX_INSTALL_DOCS },
  "claude-code": {
    command: "npm install -g @anthropic-ai/claude-code",
    docs: "https://code.claude.com/docs/en/setup",
  },
  opencode: {
    command: "npm install -g opencode-ai",
    docs: "https://opencode.ai/docs/",
  },
  pi: {
    command: "npm install -g @mariozechner/pi-coding-agent",
    docs: "https://github.com/badlogic/pi-mono",
  },
};

const BYTE_UNITS = ["B", "KB", "MB", "GB", "TB"] as const;

/** 字节数：1024 进制，保留至多 1 位小数，例如 1.2 GB */
export function formatBytes(bytes: number, locale: string): string {
  let value = Math.max(0, bytes);
  let unit = 0;
  while (value >= 1024 && unit < BYTE_UNITS.length - 1) {
    value /= 1024;
    unit++;
  }
  const number = new Intl.NumberFormat(locale, {
    maximumFractionDigits: unit === 0 ? 0 : 1,
  }).format(value);
  return `${number} ${BYTE_UNITS[unit]}`;
}

/** 倒计时 m:ss（负数按 0） */
export function formatCountdown(ms: number): string {
  const total = Math.max(0, Math.ceil(ms / 1000));
  const minutes = Math.floor(total / 60);
  const seconds = total % 60;
  return `${minutes}:${String(seconds).padStart(2, "0")}`;
}
