/**
 * 桌面端通过 preload 注入的桥接接口（apps/desktop/preload.cjs → main.cjs）。浏览器中不存在。
 * 类型与 main.cjs 的实际返回值一致：命令失败时不抛错，而是返回 UNAVAILABLE / runnable=false 的兜底值。
 */

/** openfox daemon status 的服务状态（internal/daemon/control.go），命令不可用时为 UNAVAILABLE */
export type DesktopDaemonState =
  | "STARTING"
  | "RUNNING"
  | "STOPPING"
  | "STOPPED"
  | "RECONCILE_REQUIRED"
  | "UNAVAILABLE";

export interface DesktopDaemonStatus {
  /** 未知取值原样透传，界面按「不可用」处理 */
  state: DesktopDaemonState | (string & {});
  /** 与中心的连接状态：CONNECTING / CONNECTED / RECONNECTING / DISCONNECTED / RECONCILE_REQUIRED */
  remote_state?: string;
  pid?: number;
  center_epoch?: number;
  /** 本机未终态的 Run 数（daemon 不上报时为 0），用于防止休眠 */
  active_runs?: number;
  reason?: string;
}

export interface DesktopRuntimeStatus {
  runtime: string;
  version?: string;
  executable?: string;
  /** LOGGED_IN / LOGGED_OUT / UNKNOWN */
  login_status?: string;
  runnable: boolean;
  reason?: string;
  error?: unknown;
}

/** 防止休眠：enabled = 用户设置（持久化在 userData）；active = 当前是否正在阻止休眠（有活动 Run） */
export interface DesktopPreventSleep {
  enabled: boolean;
  active: boolean;
}

/** openfox computer status --json：本机是否已注册；unavailable = CLI 不可用 */
export type DesktopRegistration =
  | {
      registered: true;
      organization_id: string;
      device_id: string;
      /** 本机注册的中心地址（http(s) 绝对地址）；旧版桌面端没有 */
      center_url?: string;
    }
  | {
      registered: false;
      reason: "not_registered" | "invalid_config" | "unavailable";
    };

/** 自带 CLI 的安装结果（apps/desktop/cli.cjs）；override = 用环境变量指定了 CLI */
export type DesktopCliState =
  | "pending"
  | "installed"
  | "unchanged"
  | "foreign"
  | "missing"
  | "failed"
  | "override";

export interface DesktopLocalStatus {
  daemon: DesktopDaemonStatus;
  runtime: DesktopRuntimeStatus;
  /** 旧版桌面端没有这个字段 */
  prevent_sleep?: DesktopPreventSleep;
  /** 以下三项旧版桌面端没有 */
  registration?: DesktopRegistration;
  cli?: { state: DesktopCliState };
  /** 本机名称（hostname 去掉 .local），自动注册时用作电脑名称 */
  machine_name?: string;
}

/** 在本机执行 openfox computer register 的结果；失败只有原因代码，不含命令输出 */
export type DesktopRegisterResult =
  | { ok: true }
  | {
      ok: false;
      reason:
        | "invalid_token"
        | "already_registered"
        | "token_rejected"
        | "center_unavailable"
        | "busy"
        | "timeout"
        | "unavailable"
        | "failed";
    };

export type DesktopPickDirectoryResult =
  | { ok: true; path: string }
  | {
      ok: false;
      reason:
        | "cancelled"
        | "failed"
        | "home_directory"
        | "protected_path"
        | "not_found"
        | "not_writable";
    };

export type DesktopWorkDirectoryInput = {
  orgId: string;
  agentId: string;
  deviceId: string;
  roots?: readonly string[];
  path?: string;
  browse?: boolean;
};
export type DesktopWorkDirectoryResult =
  | {
      ok: true;
      directory: string;
      path: string;
      git: boolean;
      worktree: boolean;
    }
  | { ok: false; reason: string };

export interface DesktopAccount {
  id: string;
  name: string;
  current: boolean;
}

export type DesktopProxy = {
  mode: "system" | "direct" | "manual";
  protocol?: "http" | "https" | "socks5";
  host?: string;
  port?: number;
  bypass?: string;
  authenticated?: boolean;
  username?: string;
  password?: string;
};
export interface DesktopBridge {
  copyWorkDirectory?(input: {
    orgId: string;
    conversationId: string;
    requestId: string;
  }): Promise<{ ok: true } | { ok: false; reason: string }>;
  cancelWorkDirectoryCopy?(requestId: string): Promise<void>;
  getProxy?(): Promise<DesktopProxy>;
  saveProxy?(config: DesktopProxy): Promise<void>;
  testProxy?(config: DesktopProxy): Promise<boolean>;
  accounts?(): Promise<DesktopAccount[]>;
  nameAccount?(name: string): Promise<void>;
  selectAccount?(id: string | null): Promise<void>;
  resolveWorkDirectory?(
    input: DesktopWorkDirectoryInput,
  ): Promise<DesktopWorkDirectoryResult>;
  getLocalStatus(): Promise<DesktopLocalStatus>;
  /** 执行后返回最新的本机状态；命令失败时 reject（"daemon action failed"） */
  daemonAction(
    action: "start" | "stop" | "restart",
  ): Promise<DesktopLocalStatus>;
  pickDirectory(defaultPath?: string): Promise<DesktopPickDirectoryResult>;
  /** 旧版桌面端没有这两个方法，调用前先判断 */
  /** options.force：本机配置还在但服务端已删除这台电脑时替换旧注册（旧版桌面端忽略） */
  registerComputer?(
    installToken: string,
    options?: { force?: boolean },
  ): Promise<DesktopRegisterResult>;
  setPreventSleep?(enabled: boolean): Promise<DesktopPreventSleep>;
  /** 主进程的 process.platform（darwin / win32 / linux）；旧版桌面端没有 */
  platform?: string;
}

export function desktopBridge(): DesktopBridge | undefined {
  return (globalThis as { openfoxDesktop?: DesktopBridge }).openfoxDesktop;
}

export const isDesktop = (): boolean => desktopBridge() !== undefined;

/** macOS 桌面端：窗口标题栏隐藏（hiddenInset），左上角留给系统红黄绿按钮，标题栏可拖动窗口。 */
export const isMacDesktop = (): boolean =>
  desktopBridge()?.platform === "darwin";

/** 本机状态查询键（首登向导与电脑页共用） */
export const desktopLocalStatusKey = ["desktop", "local-status"] as const;
