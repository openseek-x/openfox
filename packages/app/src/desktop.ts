import type { BrowserComment } from "@openfox/api";
/**
 * 桌面端通过 preload 注入的桥接接口（apps/desktop/preload.cjs → main.cjs）。浏览器中不存在。
 * 状态探测失败返回 UNAVAILABLE / runnable=false；控制及只读日志失败会 reject。
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
  started_at?: string;
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
      /** 本机已知的全部组织（注册组织在前）；一台电脑可加入多个组织（阶段 12e），旧版 CLI 没有 */
      organization_ids?: string[];
      device_id: string;
      /** 本机注册的中心地址（http(s) 绝对地址）；旧版桌面端没有 */
      center_url?: string;
    }
  | {
      registered: false;
      reason: "not_registered" | "invalid_config" | "unavailable";
    };

/** 本机注册是否属于该组织：认 organization_ids（多组织），旧版 CLI 只看 organization_id */
export function registrationHasOrg(
  registration: { organization_id: string; organization_ids?: string[] },
  orgId: string,
): boolean {
  return (
    registration.organization_id === orgId ||
    Boolean(registration.organization_ids?.includes(orgId))
  );
}

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
/** 阶段 12h：开机自启的系统实际状态；supported=false 时界面隐藏开关 */
export interface DesktopLoginItemState {
  supported: boolean;
  enabled: boolean;
  /** not_packaged / unsupported_platform / requires_approval / read_failed */
  reason?: string;
}

export interface DesktopPrefs {
  /** 关闭窗口时保留在菜单栏 */
  keep_in_tray: boolean;
  tray_supported: boolean;
  login_item: DesktopLoginItemState;
}

export type DesktopUpdateStatus =
  | "up_to_date"
  | "update_available"
  | "no_update_info"
  | "invalid_manifest"
  | "unsafe_manifest"
  | "unsupported_origin"
  | "failed";

export interface DesktopAbout {
  version: string;
  last_check: {
    /** ISO 时间 */
    at: string;
    status: DesktopUpdateStatus;
    version: string | null;
  } | null;
  /** 有可用新版本时给出版本号与 sha256（下载地址只在主进程里，不暴露给页面） */
  update: {
    version: string;
    sha256: string;
    phase:
      | "manual"
      | "downloading"
      | "ready"
      | "installing"
      | "installer_opened"
      | "failed";
    mandatory: boolean;
  } | null;
  skipped_version: string | null;
}

export interface DesktopBridge {
  browserMount?(input: {
    orgId: string;
    conversationId: string;
  }): Promise<DesktopBrowserState>;
  browserCommand?(input: DesktopBrowserCommand): Promise<DesktopBrowserState>;
  onBrowserState?(callback: (state: DesktopBrowserState) => void): () => void;
  onBrowserFocusAddress?(
    callback: (event: { lease: string }) => void,
  ): () => void;
  onBrowserToggleSidebar?(
    callback: (event: { lease: string; side: "left" | "right" }) => void,
  ): () => void;
  getMachineProxy?(target: DesktopLocalTarget): Promise<DesktopMachineProxy>;
  saveMachineProxy?(
    input: DesktopLocalTarget & {
      expected_revision: number;
      scope_token: string;
      mode: DesktopMachineProxy["mode"];
      effective?: DesktopMachineProxy["effective"];
    },
  ): Promise<DesktopMachineProxy>;
  getLocalService?(target: DesktopLocalTarget): Promise<DesktopServiceSettings>;
  setLocalKeepRunning?(
    input: DesktopLocalTarget & { enabled: boolean },
  ): Promise<DesktopServiceSettings>;
  setLocalAwake?(
    input: DesktopLocalTarget & { minutes: number },
  ): Promise<DesktopServiceSettings>;
  moveLocalStorage?(
    target: DesktopLocalTarget,
  ): Promise<DesktopServiceSettings | null>;
  openLocalStorage?(target: DesktopLocalTarget): Promise<void>;
  setLocalStartup?(
    input: DesktopLocalTarget & { mode: "login" | "manual" },
  ): Promise<DesktopServiceSettings>;
  readLocalLogs?(
    input: DesktopLocalTarget & { cursor?: string },
  ): Promise<DesktopLogPage>;
  localDaemonAction?(
    input: DesktopLocalTarget & { action: "start" | "stop" | "restart" },
  ): Promise<DesktopLocalStatus>;
  /** 阶段 12h：以下方法旧版桌面端没有，调用前先判断 */
  getDesktopPrefs?(): Promise<DesktopPrefs>;
  setKeepInTray?(enabled: boolean): Promise<DesktopPrefs>;
  setLoginItem?(enabled: boolean): Promise<{
    ok: boolean;
    state: DesktopLoginItemState;
    reason?: string;
  }>;
  getAbout?(): Promise<DesktopAbout>;
  checkForUpdate?(): Promise<{
    status: DesktopUpdateStatus;
    about: DesktopAbout;
  }>;
  openUpdateDownload?(): Promise<{ ok: boolean }>;
  skipUpdate?(): Promise<{ ok: boolean; about: DesktopAbout }>;
  copyWorkDirectory?(input: {
    orgId: string;
    conversationId: string;
    requestId: string;
  }): Promise<{ ok: true } | { ok: false; reason: string }>;
  cancelWorkDirectoryCopy?(requestId: string): Promise<void>;
  getProxy?(): Promise<DesktopProxy>;
  saveProxy?(config: DesktopProxy): Promise<void | {
    machine_proxy: "synced" | "not_following" | "not_synced" | "unverified";
  }>;
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
  /** 阶段 2c：旧版桌面端没有这两个方法，调用前先判断 */
  /** options.force：本机配置还在但服务端已删除这台电脑时替换旧注册（旧版桌面端忽略） */
  registerComputer?(
    installToken: string,
    options?: { force?: boolean },
  ): Promise<DesktopRegisterResult>;
  setPreventSleep?(enabled: boolean): Promise<DesktopPreventSleep>;
  /** 主进程的 process.platform（darwin / win32 / linux）；旧版桌面端没有 */
  platform?: string;
}

export interface DesktopBrowserTab {
  id: string;
  title: string;
  url: string;
  loading: boolean;
  error: string | null;
  canBack: boolean;
  canAllowLocal?: boolean;
  canForward: boolean;
}
export type BrowserAnnotationMode = "element" | "text" | "region";

export interface DesktopBrowserState {
  annotation?: {
    enabled: boolean;
    mode: BrowserAnnotationMode;
    frozen: boolean;
    error: string | null;
    selection: BrowserSelection | null;
  };
  lease: string;
  activeTab: string | null;
  tabs: DesktopBrowserTab[];
  reset?: boolean;
  control?: {
    connected: boolean;
    pending: boolean;
    active: boolean;
    epoch: number;
    runId: string | null;
    generation: number | null;
    clientMessageId: string | null;
    origins: string[];
    error: string | null;
  };
  downloads: {
    id: string;
    filename: string;
    state: string;
    received: number;
    total: number;
  }[];
}
export interface BrowserSelection {
  id: string;
  tabId: string;
  source_url: string;
  title: string;
  captured_at: string;
  anchor: BrowserComment["anchor"];
  image?: string;
  screenshot?: string;
}
export interface DesktopBrowserCommand {
  enabled?: boolean;
  mode?: BrowserAnnotationMode;
  selectionId?: string;
  live?: BrowserComment["anchor"]["live"];
  lease: string;
  action:
    | "layout"
    | "hide"
    | "open"
    | "focus"
    | "close"
    | "navigate"
    | "back"
    | "forward"
    | "reload"
    | "stop"
    | "clear"
    | "allow-origin"
    | "reveal-download"
    | "open-external"
    | "allow-control"
    | "take-over"
    | "annotate"
    | "annotation-ready"
    | "annotation-cancel"
    | "annotation-locate";
  tabId?: string;
  url?: string;
  visible?: boolean;
  /** 原生视图因瞬时菜单或窗口被其他应用遮挡而暂时隐藏，保留当前标签上下文与 Agent 共用。 */
  occluded?: boolean;
  bounds?: { x: number; y: number; width: number; height: number };
  downloadId?: string;
  runId?: string;
  generation?: number;
  clientMessageId?: string;
  origins?: string[];
  replace?: boolean;
}

export interface DesktopLocalTarget {
  orgId: string;
  deviceId: string;
  centerUrl: string;
}

export interface DesktopMachineProxy {
  revision: number;
  scope_token: string;
  mode: "DISABLED" | "FOLLOW_DESKTOP" | "CUSTOM";
  effective?:
    | { mode: "direct" }
    | { mode: "proxy"; protocol: "http" | "https"; host: string; port: number };
  source_fingerprint?: string;
  updated_at?: string;
  problem?: string;
  source_state: "not_following" | "synced" | "outdated" | "unsupported";
  desktop_supported: boolean;
  sync_error?: boolean;
  runtime?: {
    loaded_revision?: number;
    connection_revision?: number;
    problem?: string;
  };
}

export interface DesktopServiceSettings {
  supported: boolean;
  installed: boolean;
  startup: "login" | "manual" | "";
  keep_alive: boolean;
  keep_service_running?: boolean;
  storage_path?: string;
  log_path?: string;
  awake_until?: number;
}

export interface DesktopLogPage {
  lines: string[];
  next_cursor: string | null;
  reset: boolean;
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
