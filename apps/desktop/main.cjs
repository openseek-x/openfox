const {
  app,
  BrowserWindow,
  dialog,
  ipcMain,
  net,
  powerSaveBlocker,
  shell,
  session,
  safeStorage,
  clipboard,
  Menu,
  Tray,
  nativeImage,
  powerMonitor,
} = require("electron");
const fs = require("node:fs/promises");
const os = require("node:os");
const path = require("node:path");
const { pathToFileURL } = require("node:url");
const { execFile, spawn } = require("node:child_process");
const {
  createWorkDirectoryCopier,
  requestJSON,
} = require("./work-directory.cjs");
const workDirectoryCopier = createWorkDirectoryCopier();
const { createLocalService } = require("./local-service.cjs");
const localDeviceService = createLocalService();
const { createBrowserHost } = require("./browser/host.cjs");
const { createUpstreamDial } = require("./browser/upstream.cjs");
const { addressKind } = require("./browser/policy.cjs");
const browserHosts = new WeakMap();

const { ensureOnPath, installBundledCli, installPaths } = require("./cli.cjs");
const {
  activeRunCount,
  buildCsp,
  createSleepGuard,
  injectCspMeta,
  isTrustedSender,
  isWorkbenchURL,
  allowWorkbenchPermission,
  isWorkDirectoryOrigin,
  isSafeWebLink,
  newWindowAction,
  isValidInstallToken,
  loadSettings,
  parseRegistrationStatus,
  parseWebURL,
  registerFailureReason,
  resolveLocalFile,
  saveSettings,
  trustedSources,
} = require("./security.cjs");

const { createLocalPrefs } = require("./local-prefs.cjs");
const { createLoginItem } = require("./login-item.cjs");
const { message, pickLocale } = require("./messages.cjs");
const { createQuitFlow } = require("./quit-flow.cjs");
const { decideRestart } = require("./restart-policy.cjs");
const { buildTrayMenu, serviceStatus } = require("./tray.cjs");
const { checkForUpdate, parseHostList } = require("./update-check.cjs");

const {
  createUpdateManager,
  downloadUpdate,
  removeInstalledPackages,
} = require("./update-install.cjs");

const MAX_OUTPUT_BYTES = 64 * 1024;
const COMMAND_TIMEOUT_MS = 10_000;
// computer register 需要联网签名、写配置并安装 launchd 服务
const REGISTER_TIMEOUT_MS = 90_000;
// 防止休眠开启时，每 30 秒检查一次本机是否还有活动 Run
const SLEEP_POLL_MS = 30_000;
const allowedActions = new Set(["start", "stop", "restart"]);
// 本地页面只有离线页（网页地址未配置或加载失败时显示），网页本身由服务端提供（与 HiFox 一致）
const rendererDir = path.resolve(__dirname, "renderer");
const offlineFile = path.join(rendererDir, "offline.html");
// Chromium 网络错误码：-3 = ERR_ABORTED（导航被新的导航替换），不算加载失败
const ERR_ABORTED = -3;

// 自带 CLI（参照 HiFox）：安装包里的 openfox 在启动时拷到 ~/.local/bin/openfox 并加进终端 PATH。
// 源码运行时由 pnpm --filter @openfox/desktop build:cli 编译到 resources/bin/openfox。
const bundledCli = app.isPackaged
  ? path.join(process.resourcesPath, "bin", "openfox")
  : path.join(__dirname, "resources", "bin", "openfox");
// 安装包的默认网页地址（打包时由 build-defaults.cjs 写入）；用户设置与环境变量优先。
const defaultsFile = app.isPackaged
  ? path.join(process.resourcesPath, "desktop-defaults.json")
  : path.join(__dirname, "resources", "desktop-defaults.json");
let defaultWebURL = null;
// state：pending / installed / unchanged / foreign（目标是别人的文件，直接用安装包里的）/ missing / failed
let cli = { state: "pending", path: null };
let cliReady = null;

async function bootstrapCli() {
  if (process.platform === "win32") {
    cli = { state: "missing", path: null };
    return;
  }
  try {
    cli = await installBundledCli({ bundled: bundledCli, home: os.homedir() });
  } catch (error) {
    cli = {
      state: error && error.code === "bundled_missing" ? "missing" : "failed",
      path: null,
    };
    return;
  }
  if (cli.state === "foreign") return;
  try {
    await ensureOnPath({
      home: os.homedir(),
      binDir: installPaths(os.homedir()).binDir,
    });
  } catch {
    // PATH 只影响终端里直接输入 openfox，失败不影响桌面端
  }
}

/** CLI 路径：环境变量 > 自带 CLI > PATH 里的 openfox（与 HiFox 的回退顺序一致）。 */
function resolveBinary() {
  const configured = String(
    process.env.OPENFOX_DESKTOP_OPENFOX_BINARY || "",
  ).trim();
  if (configured) {
    if (!path.isAbsolute(configured)) {
      throw new Error(
        "OPENFOX_DESKTOP_OPENFOX_BINARY must be an absolute path",
      );
    }
    return configured;
  }
  return cli.path || "openfox";
}

async function cliStatus() {
  if (cliReady) await cliReady;
  const override = Boolean(
    String(process.env.OPENFOX_DESKTOP_OPENFOX_BINARY || "").trim(),
  );
  return { state: override ? "override" : cli.state };
}

function runOpenFox(args, input, signal) {
  if (
    input !== undefined &&
    (typeof input !== "string" || Buffer.byteLength(input) > 16384)
  )
    return Promise.reject(Error("invalid local payload"));
  if (signal?.aborted) return Promise.reject(Error("local request cancelled"));
  const binary = resolveBinary();
  return new Promise((resolve, reject) => {
    const child = spawn(binary, args, {
      cwd: app.getPath("userData"),
      env: process.env,
      shell: false,
      windowsHide: true,
    });
    let stdout = "";
    let stderr = "";
    let settled = false;
    const append = (target, chunk) => {
      const next = target + chunk.toString();
      return next.length > MAX_OUTPUT_BYTES
        ? next.slice(0, MAX_OUTPUT_BYTES)
        : next;
    };
    const finish = (error, result) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      signal?.removeEventListener("abort", abort);
      if (error) reject(error);
      else resolve(result);
    };
    const timer = setTimeout(() => {
      child.kill("SIGTERM");
      finish(new Error("OpenFox command timed out"));
    }, COMMAND_TIMEOUT_MS);
    const abort = () => {
      child.kill("SIGTERM");
      finish(Error("local request cancelled"));
    };
    signal?.addEventListener("abort", abort, { once: true });
    child.stdin.on("error", () => {});
    child.stdin.end(input);
    child.stdout.on("data", (chunk) => {
      stdout = append(stdout, chunk);
    });
    child.stderr.on("data", (chunk) => {
      stderr = append(stderr, chunk);
    });
    child.on("error", () =>
      finish(new Error("OpenFox command is unavailable")),
    );
    child.on("close", (code, signal) => {
      if (code !== 0) {
        const error = new Error(
          signal ? "OpenFox command was terminated" : "OpenFox command failed",
        );
        if (
          [
            "machine_proxy_invalid",
            "machine_proxy_conflict",
            "machine_proxy_busy",
            "machine_proxy_registration_changed",
          ].includes(stderr.trim())
        )
          error.code = stderr.trim();
        finish(error);
        return;
      }
      finish(null, { stdout, stderr });
    });
  });
}

function unavailableDaemon() {
  return { state: "UNAVAILABLE", reason: "daemon status is unavailable" };
}

function unavailableRuntime() {
  return {
    runtime: "codex",
    runnable: false,
    login_status: "UNKNOWN",
    reason: "runtime capability probe is unavailable",
  };
}

async function daemonStatus() {
  try {
    const result = await runOpenFox(["daemon", "status"]);
    const parsed = JSON.parse(result.stdout);
    return {
      state: String(parsed.state || "UNAVAILABLE"),
      remote_state: parsed.remote_state,
      pid: Number.isFinite(parsed.pid) ? parsed.pid : undefined,
      center_epoch: Number.isFinite(parsed.center_epoch)
        ? parsed.center_epoch
        : undefined,
      active_runs: activeRunCount(parsed),
      reason: parsed.reason,
    };
  } catch {
    return unavailableDaemon();
  }
}

async function runtimeStatus() {
  try {
    const result = await runOpenFox(["runtime", "probe", "--json"]);
    const parsed = JSON.parse(result.stdout);
    const capabilities = parsed.capabilities || {};
    return {
      runtime: String(capabilities.runtime || "codex"),
      version: capabilities.version,
      executable: capabilities.executable,
      login_status: capabilities.login_status,
      runnable: capabilities.runnable === true,
      reason: capabilities.reason,
      error: parsed.error,
    };
  } catch {
    return unavailableRuntime();
  }
}

// 桌面设置（userData/desktop-settings.json）与防止休眠
const sleepGuard = createSleepGuard(powerSaveBlocker);
let settings = { prevent_sleep: false, web_url: null };
let sleepTimer = null;

function settingsFile() {
  return path.join(app.getPath("userData"), "desktop-settings.json");
}

function preventSleepState() {
  return { enabled: settings.prevent_sleep, active: sleepGuard.active };
}

/** 按设置与本机活动 Run 数更新 blocker；开启时定时复查，关闭时停止定时器。 */
function applySleepGuard(daemon) {
  sleepGuard.update({
    enabled: settings.prevent_sleep,
    activeRuns: daemon ? daemon.active_runs : 0,
  });
  if (settings.prevent_sleep && sleepTimer === null) {
    sleepTimer = setInterval(() => {
      void daemonStatus().then(applySleepGuard);
    }, SLEEP_POLL_MS);
  } else if (!settings.prevent_sleep && sleepTimer !== null) {
    clearInterval(sleepTimer);
    sleepTimer = null;
  }
}

async function setPreventSleep(enabled) {
  if (typeof enabled !== "boolean") throw new Error("invalid prevent-sleep");
  const next = { ...settings, prevent_sleep: enabled };
  try {
    await saveSettings(settingsFile(), next);
  } catch {
    throw new Error("failed to save desktop settings");
  }
  settings = next;
  applySleepGuard(enabled ? await daemonStatus() : null);
  return preventSleepState();
}

/** 本机注册状态（openfox computer status --json）：含是否注册、组织、设备 ID 与本机注册的中心地址。 */
async function registrationStatus() {
  try {
    const result = await runOpenFox(["computer", "status", "--json"]);
    return parseRegistrationStatus(result.stdout);
  } catch {
    return { registered: false, reason: "unavailable" };
  }
}

async function localStatus() {
  const cliInfo = await cliStatus();
  const [daemon, runtime, registration] = await Promise.all([
    daemonStatus(),
    runtimeStatus(),
    registrationStatus(),
  ]);
  applySleepGuard(daemon);
  return {
    daemon,
    runtime,
    registration,
    cli: cliInfo,
    // 自动注册时用作电脑名称（去掉 .local 后缀）
    machine_name: os.hostname().replace(/\.local$/i, ""),
    prevent_sleep: preventSleepState(),
  };
}

let registering = false;

/**
 * 在本机执行 openfox computer register：execFile 不经 shell，参数固定，令牌只出现在参数里，
 * 不写日志、不回传命令输出；失败只返回原因代码。
 */
async function registerComputer(input) {
  const token = input && typeof input === "object" ? input.installToken : null;
  // force：本机配置还在但服务端已删除这台电脑时，替换旧注册
  const force = Boolean(
    input && typeof input === "object" && input.force === true,
  );
  if (!isValidInstallToken(token))
    return { ok: false, reason: "invalid_token" };
  if (registering) return { ok: false, reason: "busy" };
  if (cliReady) await cliReady;
  let binary;
  try {
    binary = resolveBinary();
  } catch {
    return { ok: false, reason: "unavailable" };
  }
  registering = true;
  try {
    const reason = await new Promise((resolve) => {
      // --install-token - 让 CLI 从 stdin 读一行令牌：令牌不出现在进程参数里（ps 看不到）
      const child = execFile(
        binary,
        [
          "computer",
          "register",
          "--install-token",
          "-",
          ...(force ? ["--force"] : []),
        ],
        {
          cwd: app.getPath("userData"),
          env: process.env,
          shell: false,
          windowsHide: true,
          timeout: REGISTER_TIMEOUT_MS,
          maxBuffer: MAX_OUTPUT_BYTES,
        },
        // 只看退出码；命令输出不读取、不记录
        (error) => resolve(registerFailureReason(error)),
      );
      // 进程启动失败（ENOENT 等）时 stdin 可能已关闭，写入错误由上面的回调处理
      child.stdin.on("error", () => {});
      child.stdin.end(`${token}\n`);
    });
    return reason ? { ok: false, reason } : { ok: true };
  } finally {
    registering = false;
  }
}

// 系统受保护目录：不能直接作为 Agent 的工作目录（与 HiFox 的 inspect_protected_path 一致）
const protectedDirectories = new Set(
  process.platform === "win32"
    ? []
    : [
        "/",
        "/Applications",
        "/Library",
        "/System",
        "/Users",
        "/Volumes",
        "/bin",
        "/etc",
        "/home",
        "/opt",
        "/private",
        "/sbin",
        "/tmp",
        "/usr",
        "/var",
      ],
);

// 用系统原生对话框选择本机文件夹，返回前校验：存在、是文件夹、可读写、不是主目录或受保护目录
async function pickDirectory(event, defaultPath, allowCreate = true) {
  const owner = BrowserWindow.fromWebContents(event.sender);
  const options = {
    properties: allowCreate
      ? ["openDirectory", "createDirectory"]
      : ["openDirectory"],
    ...(typeof defaultPath === "string" && path.isAbsolute(defaultPath)
      ? { defaultPath }
      : {}),
  };
  let result;
  try {
    result = owner
      ? await dialog.showOpenDialog(owner, options)
      : await dialog.showOpenDialog(options);
  } catch {
    return { ok: false, reason: "failed" };
  }
  if (result.canceled || !result.filePaths[0]) {
    return { ok: false, reason: "cancelled" };
  }
  const selected = path.resolve(result.filePaths[0]);
  if (selected === path.resolve(os.homedir())) {
    return { ok: false, reason: "home_directory" };
  }
  if (
    protectedDirectories.has(selected) ||
    path.parse(selected).root === selected
  ) {
    return { ok: false, reason: "protected_path" };
  }
  try {
    const stat = await fs.stat(selected);
    if (!stat.isDirectory()) return { ok: false, reason: "not_found" };
    await fs.access(selected, fs.constants.R_OK | fs.constants.W_OK);
  } catch (error) {
    return {
      ok: false,
      reason: error && error.code === "ENOENT" ? "not_found" : "not_writable",
    };
  }
  return { ok: true, path: selected };
}

/**
 * 当前网页地址：环境变量 OPENFOX_DESKTOP_WEB_URL 优先，其次是设置文件的 web_url，最后是安装包的默认地址。
 * 返回 { url } 或 { url: null, reason }，reason = not_configured / invalid_url。
 */
function webTarget() {
  const raw =
    process.env.OPENFOX_DESKTOP_WEB_URL ||
    settings.web_url ||
    defaultWebURL ||
    "";
  try {
    const url = parseWebURL(raw);
    return url ? { url } : { url: null, reason: "not_configured" };
  } catch {
    return { url: null, reason: "invalid_url" };
  }
}

const localCsp = buildCsp();
let mainWindow = null;
let accounts = null;
let networkProxy;
const proxyFile = () =>
  path.join(app.getPath("userData"), "network-proxy.json");
let proxyLoadFailed = false;
const {
  createProxy,
  normalize: normalizeProxy,
  proxyRules,
} = require("./proxy.cjs");
const fileSessions = new WeakSet();
// 主窗口当前应加载的网页地址；IPC 来源与导航校验都以它为准
let currentWebURL = null;

function showOffline(window, reason, url) {
  void window.loadFile(offlineFile, {
    query: {
      reason,
      url: url || "",
      settings: reason === "invalid_proxy" ? proxyFile() : settingsFile(),
    },
  });
}

/** 加载网页；每次都重新读设置文件，用户改完地址点「重试」即可生效。 */
async function loadRemote(window) {
  const nextSettings = await loadSettings(settingsFile());
  if (window !== mainWindow || window.isDestroyed()) return;
  settings = nextSettings;
  const target = webTarget();
  if (target.url && new URL(target.url).origin !== accountOrigin) {
    const origin = new URL(target.url).origin;
    const registry = await require("./accounts.cjs").openAccounts(
      app.getPath("userData"),
      origin,
    );
    if (window !== mainWindow || window.isDestroyed()) return;
    accounts = registry;
    accountOrigin = origin;
    currentWebURL = target.url;
    replaceWindow(window);
    return;
  }
  currentWebURL = target.url;
  if (!target.url) {
    showOffline(window, target.reason, "");
    return;
  }
  // 加载失败由 did-fail-load 处理
  window.loadURL(target.url).catch(() => {});
}

function replaceWindow(previous) {
  const bounds = previous.getBounds();
  createWindow().setBounds(bounds);
  previous.destroy();
}

function createWindow() {
  const window = new BrowserWindow({
    width: 1440,
    height: 900,
    minWidth: 960,
    minHeight: 640,
    backgroundColor: "#f6f7f9",
    // macOS：隐藏系统标题栏，红黄绿按钮放在网页标题栏左侧（Figma App/TitleBar：x=18、y=14）
    ...(process.platform === "darwin"
      ? {
          titleBarStyle: "hiddenInset",
          trafficLightPosition: { x: 18, y: 14 },
        }
      : {}),
    webPreferences: {
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: true,
      partition: accounts?.partition(),
      preload: path.join(__dirname, "preload.cjs"),
    },
  });
  const browserSession = window.webContents.session;
  if (!fileSessions.has(browserSession)) {
    serveLocalFiles(browserSession.protocol);
    fileSessions.add(browserSession);
  }
  mainWindow = window;
  let browserIdentityRevision = 0;
  const protectedBrowserPorts = new Set();
  const host = createBrowserHost({
    window,
    async authorize(input) {
      const revision = browserIdentityRevision;
      const frameURL = window.webContents.getURL();
      const origin = new URL(currentWebURL).origin;
      if (new URL(frameURL).origin !== origin) throw Error("FORBIDDEN");
      const signal = AbortSignal.timeout(5000);
      const [me, conversation, registration] = await Promise.all([
        requestJSON(browserSession, origin, "/api/v1/me", { signal }),
        requestJSON(
          browserSession,
          origin,
          `/api/v1/organizations/${input.orgId}/conversations/${input.conversationId}`,
          { signal },
        ),
        registrationStatus(),
      ]);
      if (
        window.isDestroyed() ||
        revision !== browserIdentityRevision ||
        window.webContents.getURL() !== frameURL ||
        mainWindow !== window ||
        !me.id ||
        conversation.id !== input.conversationId ||
        conversation.organization_id !== input.orgId
      )
        throw Error("FORBIDDEN");
      const local =
        registration.registered &&
        registrationHasOrg(registration, input.orgId) &&
        isWorkDirectoryOrigin(
          frameURL,
          registration.center_url,
          !app.isPackaged && Boolean(process.env.OPENFOX_DESKTOP_WEB_URL),
        );
      return {
        center: origin,
        userId: me.id,
        deviceId: local ? registration.device_id : null,
      };
    },
    async connectControl(input, identity, control) {
      const revision = browserIdentityRevision;
      const frameURL = window.webContents.getURL();
      const origin = new URL(currentWebURL).origin;
      const ensureCurrent = () => {
        if (
          window.isDestroyed() ||
          mainWindow !== window ||
          revision !== browserIdentityRevision ||
          window.webContents.getURL() !== frameURL ||
          identity.center !== origin ||
          registering
        )
          throw Object.assign(Error("FORBIDDEN"), { code: "FORBIDDEN" });
      };
      ensureCurrent();
      const registration = await registrationStatus();
      if (
        !identity.deviceId ||
        !registration.registered ||
        registration.device_id !== identity.deviceId ||
        !registrationHasOrg(registration, input.orgId) ||
        !isWorkDirectoryOrigin(
          frameURL,
          registration.center_url,
          !app.isPackaged && Boolean(process.env.OPENFOX_DESKTOP_WEB_URL),
        )
      )
        throw Object.assign(Error("FORBIDDEN"), { code: "FORBIDDEN" });
      const signal = AbortSignal.timeout(5000);
      const [me, device, status] = await Promise.all([
        requestJSON(browserSession, origin, "/api/v1/me", { signal }),
        requestJSON(
          browserSession,
          origin,
          `/api/v1/organizations/${input.orgId}/devices/${identity.deviceId}`,
          { signal },
        ),
        runOpenFox(["daemon", "status"]).then((result) =>
          JSON.parse(result.stdout),
        ),
      ]);
      ensureCurrent();
      if (
        me.id !== identity.userId ||
        device.id !== identity.deviceId ||
        device.owner_user_id !== me.id ||
        status.state !== "RUNNING" ||
        typeof status.socket_path !== "string" ||
        !path.isAbsolute(status.socket_path)
      )
        throw Object.assign(Error("FORBIDDEN"), { code: "FORBIDDEN" });
      const root = path.join(path.dirname(status.socket_path), "browser");
      const localSocket = await fs.lstat(path.join(root, "browser.sock"));
      if (!localSocket.isSocket() || localSocket.mode & 0o077)
        throw Object.assign(Error("UNAVAILABLE"), { code: "UNAVAILABLE" });
      let binding;
      try {
        binding = await requestJSON(
          browserSession,
          origin,
          `/api/v1/organizations/${input.orgId}/conversations/${input.conversationId}/browser-bindings`,
          {
            signal: AbortSignal.timeout(5000),
            method: "POST",
            body: {
              device_id: identity.deviceId,
              host_instance_id: control.hostInstanceID,
              host_epoch: control.hostEpoch,
              replace: control.replace,
            },
          },
        );
      } catch (cause) {
        throw Object.assign(Error("UNAVAILABLE"), {
          code: cause.reason === "busy" ? "BINDING_CONFLICT" : "UNAVAILABLE",
        });
      }
      ensureCurrent();
      const current = await registrationStatus();
      ensureCurrent();
      if (
        current.device_id !== registration.device_id ||
        current.center_url !== registration.center_url ||
        !registrationHasOrg(current, input.orgId) ||
        binding.device_id !== identity.deviceId ||
        binding.owner_user_id !== me.id ||
        binding.conversation_id !== input.conversationId
      )
        throw Object.assign(Error("FORBIDDEN"), { code: "FORBIDDEN" });
      return {
        root,
        socketPath: path.join(root, "browser.sock"),
        claim: {
          organization_id: input.orgId,
          binding_id: binding.id,
          claim_secret: binding.claim_secret,
          host_instance_id: binding.host_instance_id,
          host_epoch: binding.host_epoch,
          revision: binding.revision,
        },
      };
    },
    networkOptions() {
      const url = new URL(currentWebURL);
      if (
        url.hostname === "localhost" ||
        addressKind(url.hostname.replace(/^\[|\]$/g, "")) === "private"
      )
        protectedBrowserPorts.add(
          Number(url.port || (url.protocol === "https:" ? 443 : 80)),
        );
      const proxy = networkProxy.view();
      if (proxy.mode === "manual") protectedBrowserPorts.add(proxy.port);
      return {
        blockedPorts: [...protectedBrowserPorts],
        dial: createUpstreamDial({
          resolveProxy: (url) => browserSession.resolveProxy(url),
          authenticate: (info) => networkProxy.authenticate(info),
          protectPort(port) {
            protectedBrowserPorts.add(port);
            host.blockPort(port);
          },
        }),
      };
    },
  });
  browserHosts.set(window, host);
  window.webContents.on("render-process-gone", () => {
    browserIdentityRevision++;
    void host.reset();
  });
  window.webContents.on(
    "did-start-navigation",
    (_event, _url, inPlace, isMainFrame) => {
      if (isMainFrame && !inPlace) {
        browserIdentityRevision++;
        void host.reset();
      }
    },
  );
  window.webContents.on("did-finish-load", () => {
    void syncMachineProxy(window.webContents);
  });
  const syncProxyOnIdentity = (_event, cookie) => {
    if (cookie.name === "openfox_session") {
      browserIdentityRevision++;
      void host.reset();
      void syncMachineProxy(window.webContents);
    }
  };
  browserSession.cookies.on("changed", syncProxyOnIdentity);
  // 「关闭窗口时保留在菜单栏」：只隐藏窗口；主动退出（quitting）与账号切换的 destroy 不受影响
  window.on("close", (event) => {
    if (!quitting && tray && localPrefs.get().keep_in_tray) {
      event.preventDefault();
      window.hide();
    }
  });
  window.on("closed", () => {
    void host.dispose();
    browserSession.cookies.removeListener("changed", syncProxyOnIdentity);
    if (mainWindow === window) mainWindow = null;
  });
  window.webContents.on(
    "did-fail-load",
    (_event, errorCode, _description, validatedURL, isMainFrame) => {
      if (!isMainFrame || errorCode === ERR_ABORTED || !currentWebURL) return;
      let origin = null;
      try {
        origin = new URL(validatedURL).origin;
      } catch {
        return;
      }
      if (origin === new URL(currentWebURL).origin)
        showOffline(window, "unreachable", currentWebURL);
    },
  );
  // 页面自己发起的导航只允许留在网页同源；离线页由主进程 loadFile 加载，不经过这里
  window.webContents.on("will-navigate", (event, target) => {
    if (isWorkbenchURL(target, currentWebURL)) return;
    event.preventDefault();
    if (
      newWindowAction(
        { url: target },
        window.webContents.getURL(),
        currentWebURL,
      ) === "external"
    )
      void shell.openExternal(target).catch(() => {});
  });
  window.webContents.on("will-redirect", (event) => {
    if (event.isMainFrame && !isWorkbenchURL(event.url, currentWebURL))
      event.preventDefault();
  });
  window.webContents.setWindowOpenHandler((details) => {
    const action = newWindowAction(
      details,
      window.webContents.getURL(),
      currentWebURL,
    );
    if (action === "external")
      void shell.openExternal(details.url).catch(() => {});
    if (action !== "oauth") return { action: "deny" };
    return {
      action: "allow",
      overrideBrowserWindowOptions: {
        parent: window,
        width: 640,
        height: 760,
        webPreferences: {
          preload: undefined,
          contextIsolation: true,
          nodeIntegration: false,
          sandbox: true,
          webSecurity: true,
          webviewTag: false,
        },
      },
    };
  });
  window.webContents.on("did-create-window", (child) => {
    child.webContents.setWindowOpenHandler(() => ({ action: "deny" }));
    const guardNavigation = (event, target) => {
      if (!isSafeWebLink(target)) event.preventDefault();
    };
    child.webContents.on("will-navigate", guardNavigation);
    child.webContents.on("will-redirect", guardNavigation);
  });
  // 授权窗口共享登录 cookie，但不能获取设备权限或调用主窗口 IPC。
  window.webContents.session.setPermissionRequestHandler(
    (contents, permission, callback, details) => {
      callback(
        allowWorkbenchPermission(
          contents,
          mainWindow?.webContents,
          permission,
          details.requestingUrl,
          details.isMainFrame,
          currentWebURL,
        ),
      );
    },
  );
  window.webContents.session.setPermissionCheckHandler(
    (contents, permission, origin, details) =>
      allowWorkbenchPermission(
        contents,
        mainWindow?.webContents,
        permission,
        origin,
        details.isMainFrame,
        currentWebURL,
      ),
  );
  if (proxyLoadFailed) showOffline(window, "invalid_proxy", "");
  else
    void browserSession
      .setProxy(networkProxy.config())
      .then(() => loadRemote(window))
      .catch(() => showOffline(window, "invalid_proxy", ""));
  return window;
}

/** 本地页面（file://）：只放行 renderer 目录内的文件；离线页注入 CSP（响应头 + meta 兜底）。 */
function serveLocalFiles(protocol) {
  protocol.handle("file", async (request) => {
    const target = resolveLocalFile(request.url, rendererDir);
    if (!target) return new Response("Not Found", { status: 404 });
    if (target === offlineFile) {
      try {
        const html = await fs.readFile(offlineFile, "utf8");
        return new Response(injectCspMeta(html, localCsp), {
          headers: {
            "Content-Type": "text/html; charset=utf-8",
            "Content-Security-Policy": localCsp,
          },
        });
      } catch {
        return new Response("Not Found", { status: 404 });
      }
    }
    return net.fetch(pathToFileURL(target).href, {
      bypassCustomProtocolHandlers: true,
    });
  });
}

/** 所有 IPC 统一校验来源：主窗口顶层 frame，URL 与主窗口加载的来源一致。 */
function handle(channel, handler) {
  ipcMain.handle(channel, (event, ...args) => {
    const expected = mainWindow && {
      webContentsId: mainWindow.webContents.id,
      sources: trustedSources(currentWebURL),
    };
    if (!isTrustedSender(event, expected)) {
      throw new Error("untrusted IPC sender");
    }
    return handler(event, ...args);
  });
}

app.on("login", (event, contents, _details, info, callback) => {
  if (!contents || contents.session !== mainWindow?.webContents.session) return;
  const auth = networkProxy?.authenticate(info);
  if (auth) {
    event.preventDefault();
    callback(auth.username, auth.password);
  }
});
handle("openfox:get-proxy", () => networkProxy.view());
for (const action of ["mount", "command"]) {
  handle(`openfox:browser-${action}`, async (_event, input) => {
    const host = browserHosts.get(mainWindow);
    if (process.platform !== "darwin" || !host) throw Error("UNAVAILABLE");
    try {
      return await host[action](input);
    } catch (error) {
      const code = error.code || error.message;
      throw Error(
        [
          "FORBIDDEN",
          "INVALID_REQUEST",
          "INVALID_URL",
          "INVALID_BOUNDS",
          "STALE_TARGET",
          "TARGET_CLOSED",
          "LIMIT_EXCEEDED",
          "UNAVAILABLE",
          "CONTROL_REVOKED",
          "ORIGIN_NOT_ALLOWED",
          "BINDING_CONFLICT",
        ].includes(code)
          ? code
          : "UNAVAILABLE",
      );
    }
  });
}
handle("openfox:save-proxy", async (event, config) => {
  await networkProxy.save(config);
  await event.sender.session.setProxy(networkProxy.config());
  await event.sender.session.closeAllConnections();
  await browserHosts.get(mainWindow)?.reset();
  return { machine_proxy: await syncMachineProxy(event.sender) };
});
handle("openfox:test-proxy", async (_event, input) => {
  const config = normalizeProxy(input);
  const probe = session.fromPartition("openfox-proxy-probe");
  if (testingProxy) return false;
  testingProxy = true;
  try {
    await probe.closeAllConnections();
    await probe.clearAuthCache();
    // The explicit connection test must traverse the selected server even when
    // the app itself is on the bypass list (including a loopback QA server).
    const rules = proxyRules(config);
    if (config.mode === "manual") rules.proxyBypassRules = "<-loopback>";
    await probe.setProxy(rules);
    const url = new URL(currentWebURL);
    return await new Promise((resolve) => {
      const request = net.request({
        url: url.origin,
        method: "HEAD",
        session: probe,
        redirect: "manual",
        useSessionCookies: false,
      });
      const timer = setTimeout(() => {
        request.abort();
        resolve(false);
      }, 5000);
      const finish = (ok) => {
        clearTimeout(timer);
        resolve(ok);
      };
      let attemptedAuth = false;
      request.on("login", (info, callback) => {
        if (
          !attemptedAuth &&
          input.authenticated &&
          info.isProxy &&
          info.host.toLowerCase() === config.host?.toLowerCase() &&
          Number(info.port) === config.port
        ) {
          const saved = networkProxy.view();
          const auth = input.password
            ? input
            : saved.protocol === config.protocol &&
                saved.username === input.username
              ? networkProxy.authenticate(info)
              : null;
          if (auth) {
            attemptedAuth = true;
            callback(auth.username, auth.password);
            return;
          }
        }
        callback();
      });
      request.on("response", (response) => {
        response.resume();
        finish(response.statusCode < 500 && response.statusCode !== 407);
      });
      request.on("redirect", () => {
        finish(true);
        request.abort();
      });
      request.on("error", () => finish(false));
      request.end();
    });
  } catch {
    return false;
  } finally {
    testingProxy = false;
  }
});
let testingProxy = false;
let switchingAccount = false;
function accountRegistry(event) {
  const url = new URL(event.senderFrame.url);
  if (
    !accounts ||
    !currentWebURL ||
    url.origin !== new URL(currentWebURL).origin ||
    url.origin !== accountOrigin
  )
    throw Error("account registry unavailable");
  return accounts;
}
let accountOrigin;
handle("openfox:accounts", (event) => accountRegistry(event).list());
handle("openfox:account-name", (event, name) =>
  switchingAccount ? Promise.resolve() : accountRegistry(event).name(name),
);
handle("openfox:select-account", async (event, id) => {
  const registry = accountRegistry(event);
  if (switchingAccount) throw Error("account switch pending");
  switchingAccount = true;
  try {
    await registry.select(id);
    replaceWindow(mainWindow);
  } finally {
    switchingAccount = false;
  }
});
handle("openfox:local-status", () => localStatus());
async function localServiceRequest(operation, event, input) {
  const contents = event.sender;
  const frameURL = event.senderFrame.url;
  const controller = new AbortController();
  const abort = () => controller.abort();
  const cookieChanged = (_event, cookie) => {
    if (cookie.name === "openfox_session") abort();
  };
  contents.on("did-start-navigation", abort);
  contents.once("destroyed", abort);
  contents.session.cookies.on("changed", cookieChanged);
  try {
    if (registering) throw Error("busy");
    return await localDeviceService(operation, input, {
      platform: process.platform,
      registration: registrationStatus,
      run: (args, payload) => runOpenFox(args, payload, controller.signal),
      desktopProxy: () => networkProxy.view(),
      status: localStatus,
      ensureCurrent() {
        controller.signal.throwIfAborted();
        if (registering) throw Error("registration_changed");
        if (contents.isDestroyed() || contents.getURL() !== frameURL)
          throw Error("navigation_changed");
      },
      request: (pathname) =>
        requestJSON(contents.session, new URL(frameURL).origin, pathname, {
          signal: controller.signal,
        }),
    });
  } catch (error) {
    // Do not expose CLI stderr, file paths or session/network error details.
    if (
      operation.startsWith("proxy-") &&
      [
        "machine_proxy_invalid",
        "machine_proxy_conflict",
        "machine_proxy_busy",
        "machine_proxy_registration_changed",
      ].includes(error.code || error.message)
    )
      throw Error(error.code || error.message);
    throw Error("local service request failed");
  } finally {
    contents.removeListener("did-start-navigation", abort);
    contents.removeListener("destroyed", abort);
    contents.session.cookies.removeListener("changed", cookieChanged);
  }
}
for (const operation of [
  "service-status",
  "startup",
  "logs",
  "action",
  "proxy-status",
  "proxy-save",
]) {
  handle(`openfox:local-service-${operation}`, (event, input) =>
    localServiceRequest(operation, event, input),
  );
}

// Every lifecycle sync uses the current desktop session's owner checks.
async function syncMachineProxy(contents) {
  try {
    if (
      process.platform !== "darwin" ||
      !contents ||
      contents.isDestroyed() ||
      mainWindow?.webContents !== contents ||
      !currentWebURL ||
      new URL(contents.getURL()).origin !== new URL(currentWebURL).origin
    )
      return "unverified";
    const registration = await registrationStatus();
    if (!registration.registered) return "unverified";
    const result = await localServiceRequest(
      "proxy-status",
      { sender: contents, senderFrame: contents.mainFrame },
      {
        orgId: registration.organization_id,
        deviceId: registration.device_id,
        centerUrl: registration.center_url,
      },
    );
    return result.mode !== "FOLLOW_DESKTOP"
      ? "not_following"
      : result.source_state === "synced" && !result.sync_error
        ? "synced"
        : "not_synced";
  } catch {
    return "unverified";
  }
}
handle("openfox:daemon-action", async (_event, action) => {
  if (typeof action !== "string" || !allowedActions.has(action)) {
    throw new Error("unsupported daemon action");
  }
  try {
    await runOpenFox(["daemon", action]);
  } catch {
    throw new Error("daemon action failed");
  }
  return localStatus();
});

handle("openfox:pick-directory", (event, defaultPath) =>
  pickDirectory(event, defaultPath),
);

handle("openfox:cancel-work-directory-copy", (_event, requestId) =>
  workDirectoryCopier.cancel(requestId),
);
handle("openfox:copy-work-directory", async (event, input) => {
  const contents = event.sender;
  const origin = currentWebURL && new URL(currentWebURL).origin;
  if (
    !origin ||
    new URL(event.senderFrame.url).origin !== origin ||
    switchingAccount
  )
    return { ok: false, reason: "unavailable" };
  return workDirectoryCopier.copy(input, {
    isCurrent: () =>
      !contents.isDestroyed() &&
      mainWindow?.webContents === contents &&
      !switchingAccount &&
      new URL(contents.getURL()).origin === origin,
    onInvalidate(cancel) {
      const navigate = (_event, _url, _inPlace, main) => {
        if (main) cancel();
      };
      const cookieChanged = (_event, cookie) => {
        if (cookie.name === "openfox_session") cancel();
      };
      contents.on("did-start-navigation", navigate);
      contents.once("destroyed", cancel);
      contents.session.cookies.on("changed", cookieChanged);
      return () => {
        contents.removeListener("did-start-navigation", navigate);
        contents.removeListener("destroyed", cancel);
        contents.session.cookies.removeListener("changed", cookieChanged);
      };
    },
    request: (pathname, options) =>
      requestJSON(contents.session, origin, pathname, options),
    writeText: (value) => clipboard.writeText(value),
    async local(scope, signal) {
      const registration = await registrationStatus();
      signal.throwIfAborted();
      if (
        !registration.registered ||
        registration.device_id !== scope.device_id ||
        !registrationHasOrg(registration, scope.organization_id) ||
        !isWorkDirectoryOrigin(
          origin,
          registration.center_url,
          !app.isPackaged && Boolean(process.env.OPENFOX_DESKTOP_WEB_URL),
        )
      )
        return null;
      const args = ["computer", "workspace-path"];
      for (const [flag, key] of Object.entries({
        "organization-id": "organization_id",
        "device-id": "device_id",
        "space-id": "space_id",
        "conversation-id": "conversation_id",
        "agent-id": "agent_id",
        "run-id": "run_id",
        generation: "generation",
        "user-id": "user_id",
      }))
        args.push("--" + flag, String(scope[key]));
      try {
        return JSON.parse((await runOpenFox(args)).stdout);
      } catch {
        return null;
      }
    },
  });
});

// 阶段 12e：一台电脑可加入多个组织；旧版 CLI 只输出 organization_id。
function registrationHasOrg(registration, orgId) {
  return (
    registration.organization_id === orgId ||
    (Array.isArray(registration.organization_ids) &&
      registration.organization_ids.includes(orgId))
  );
}

// The renderer supplies the intended Agent and folder, never the authoritative allowlist.
// Read the signed-in user's choices from the center that registered this computer.
handle("openfox:resolve-work-directory", async (event, input) => {
  const uuid = /^[0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12}$/i;
  if (
    !input ||
    ![input.orgId, input.agentId, input.deviceId].every(
      (id) => typeof id === "string" && uuid.test(id),
    )
  ) {
    return { ok: false, reason: "unavailable" };
  }
  const registration = await registrationStatus();
  if (
    !registration.registered ||
    !registration.center_url ||
    !registrationHasOrg(registration, input.orgId) ||
    registration.device_id !== input.deviceId
  ) {
    return { ok: false, reason: "wrong_device" };
  }
  try {
    if (
      !isWorkDirectoryOrigin(
        event.senderFrame.url,
        registration.center_url,
        !app.isPackaged && Boolean(process.env.OPENFOX_DESKTOP_WEB_URL),
      )
    )
      return { ok: false, reason: "wrong_device" };
    const base = new URL(registration.center_url);
    const url = new URL(
      base.href.replace(/\/$/, "") +
        "/api/v1/organizations/" +
        input.orgId +
        "/conversation-workspace-options",
    );
    url.searchParams.set("agent_id", input.agentId);
    const response = await event.sender.session.fetch(url.href, {
      credentials: "include",
      redirect: "error",
      signal: AbortSignal.timeout(5000),
    });
    if (!response.ok) return { ok: false, reason: "unavailable" };
    const options = await response.json();
    if (
      options.device_id !== input.deviceId ||
      options.supported !== true ||
      !Array.isArray(options.directories) ||
      !options.directories.length ||
      options.directories.length > 32
    ) {
      return { ok: false, reason: "outside_allowed_roots" };
    }
    let selected = input.path;
    if (input.browse === true) {
      const choice = await pickDirectory(event, undefined, false);
      if (!choice.ok) return choice;
      selected = choice.path;
    }
    if (
      typeof selected !== "string" ||
      !selected ||
      selected.length > 4096 ||
      /[\x00-\x1f\x7f]/.test(selected)
    )
      return { ok: false, reason: "outside_allowed_roots" };
    const args = [
      "computer",
      "resolve-directory",
      "--organization-id",
      input.orgId,
      "--device-id",
      input.deviceId,
      "--path",
      selected,
    ];
    for (const root of options.directories) args.push("--allow-root", root);
    const checked = JSON.parse((await runOpenFox(args)).stdout);
    if (checked.ok !== true)
      // 阶段 12e：命中本机受保护目录名单时给出明确原因（与目录选择器的 protected_path 一致）
      return {
        ok: false,
        reason:
          checked.reason === "protected_directory"
            ? "protected_path"
            : "outside_allowed_roots",
      };
    return {
      ok: true,
      directory: checked.directory,
      path: checked.path,
      git: checked.is_git === true,
      worktree: checked.is_git === true,
    };
  } catch {
    return { ok: false, reason: "unavailable" };
  }
});

handle("openfox:register-computer", async (event, input) => {
  const result = await registerComputer(input);
  void syncMachineProxy(event.sender);
  return result;
});

handle("openfox:prevent-sleep", (_event, input) =>
  setPreventSleep(input && typeof input === "object" ? input.enabled : null),
);

// 离线页的「重试」：重新读设置并加载网页
handle("openfox:retry-remote", async () => {
  if (!mainWindow) return;
  try {
    await networkProxy.load();
    await mainWindow.webContents.session.setProxy(networkProxy.config());
    proxyLoadFailed = false;
  } catch {
    proxyLoadFailed = true;
    showOffline(mainWindow, "invalid_proxy", "");
    return;
  }
  await loadRemote(mainWindow);
});

// ---------- 阶段 12h：托盘、开机自启、更新检查、升级后重启本地服务 ----------

const localPrefs = createLocalPrefs(
  path.join(app.getPath("userData"), "desktop-local-prefs.json"),
);
const loginItem = createLoginItem({
  app,
  platform: process.platform,
  packaged: app.isPackaged,
});
const locale = pickLocale(app.getLocale());
const tr = (key, params) => message(locale, key, params);
// 主动退出标记：为 true 时关闭窗口不再隐藏到菜单栏
let quitting = false;
let confirmingQuit = false;
// 系统关机/注销触发的退出：不弹确认、不停止服务
let shuttingDown = false;
let tray = null;
let trayService = "unknown";

const promptedVersions = new Set();
let updateTimer = null;
let restartTimer = null;
const UPDATE_STARTUP_DELAY_MS = 30_000;
const UPDATE_INTERVAL_MS = 24 * 60 * 60 * 1000;
const UPDATE_TIMEOUT_MS = 10_000;
const UPDATE_MAX_BYTES = 16 * 1024;
const launchAgentFile = path.join(
  os.homedir(),
  "Library",
  "LaunchAgents",
  "io.openfox.daemon.plist",
);

function showMainWindow() {
  if (!mainWindow) {
    createWindow();
    return;
  }
  if (mainWindow.isMinimized()) mainWindow.restore();
  mainWindow.show();
  mainWindow.focus();
}

async function refreshTray() {
  if (!tray) return;
  let installed = false;
  try {
    await fs.access(launchAgentFile);
    installed = true;
  } catch {
    // 未安装本地服务
  }
  const daemon = installed ? await daemonStatus() : null;
  trayService = serviceStatus({
    installed,
    daemonState: daemon ? daemon.state : "UNAVAILABLE",
  });
  rebuildTrayMenu();
}

function rebuildTrayMenu() {
  if (!tray) return;
  tray.setContextMenu(
    Menu.buildFromTemplate(
      buildTrayMenu({
        locale,
        service: trayService,
        updateVersion: updates.update?.manifest.version ?? null,
        handlers: {
          open: showMainWindow,
          checkUpdate: () =>
            void runUpdateCheck({ manual: true }).catch(() => {}),
          quit: () => app.quit(),
        },
      }),
    ),
  );
}

function createTray() {
  if (process.platform !== "darwin" || tray) return;
  const image = nativeImage.createFromPath(
    path.join(__dirname, "renderer", "openfoxTemplate.png"),
  );
  // 模板图：由系统按明暗菜单栏着色
  image.setTemplateImage(true);
  tray = new Tray(image);
  tray.setToolTip(tr("tray.tooltip"));
  rebuildTrayMenu();
  tray.on("mouse-enter", () => void refreshTray());
  void refreshTray();
}

/** 通过主窗口会话（沿用代理设置）取更新清单；不跟随重定向、不带 cookie，限时限大小。 */
async function fetchUpdateText(url, { timeoutMs, maxBytes }) {
  if (!mainWindow) throw new Error("no window");
  const response = await mainWindow.webContents.session.fetch(url, {
    credentials: "omit",
    redirect: "error",
    cache: "no-store",
    signal: AbortSignal.timeout(timeoutMs),
  });
  if (response.status !== 200) {
    await response.body?.cancel();
    return { status: response.status, text: "" };
  }
  const declared = Number(response.headers.get("content-length"));
  if (Number.isFinite(declared) && declared > maxBytes) {
    await response.body?.cancel();
    throw new Error("manifest too large");
  }
  const reader = response.body.getReader();
  const chunks = [];
  let total = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    total += value.byteLength;
    if (total > maxBytes) {
      await reader.cancel();
      throw new Error("manifest too large");
    }
    chunks.push(value);
  }
  return { status: 200, text: Buffer.concat(chunks).toString("utf8") };
}

const automaticUpdates =
  app.isPackaged &&
  process.platform === "darwin" &&
  require("./package.json").openfoxAutoUpdate === true;
const updates = createUpdateManager({
  automatic: automaticUpdates,
  check: () =>
    checkForUpdate({
      centerURL: automaticUpdates ? defaultWebURL : currentWebURL,
      current: app.getVersion(),
      skippedVersion: localPrefs.get().skipped_version,
      allowedHosts: parseHostList(process.env.OPENFOX_DESKTOP_UPDATE_HOSTS),
      fetchText: fetchUpdateText,
    }),
  download: (manifest) => {
    const updateSession = mainWindow.webContents.session;
    return downloadUpdate(manifest, (url, options) =>
      updateSession.fetch(url, options),
    );
  },
  install: async (archive) => {
    const cache = path.join(app.getPath("userData"), "desktop-installers");
    await archive.openInstaller(cache, (file) => shell.openPath(file));
  },
  record: async (result) => {
    await localPrefs
      .update({
        last_check: {
          at: new Date().toISOString(),
          status: result.status,
          version: result.manifest?.version ?? result.version ?? null,
        },
      })
      .catch(() => {});
  },
  showResult: async (result, update, manual) => {
    rebuildTrayMenu();
    if (quitting || shuttingDown) return;
    if (
      !manual &&
      (result.status !== "update_available" ||
        !update ||
        promptedVersions.has(update.manifest.version))
    )
      return;
    return showUpdateResult(result, update);
  },
});

function runUpdateCheck({ manual }) {
  return updates.check(manual);
}

async function showUpdateResult(result, update) {
  const owner = mainWindow && !mainWindow.isDestroyed() ? mainWindow : null;
  const box = (options) =>
    owner
      ? dialog.showMessageBox(owner, options)
      : dialog.showMessageBox(options);
  if (result.status === "update_available") {
    const m = update.manifest;
    promptedVersions.add(m.version);
    if (update.phase === "ready") {
      const { response } = await box({
        type: "info",
        message: tr("update.ready.message", { version: m.version }),
        detail: [tr("update.ready.detail"), m.notes]
          .filter(Boolean)
          .join("\n\n"),
        buttons: [tr("update.restart"), tr("update.later")],
        defaultId: 1,
        cancelId: 1,
      });
      return response === 0 && !confirmingQuit && !quitting && !shuttingDown;
    }
    const detail = [
      tr("update.available.detail", {
        current: app.getVersion(),
        notes: m.notes || "-",
        sha256: m.sha256,
      }),
      update.mandatory
        ? "\n" + tr("update.available.mandatory", { min: m.min_version })
        : "",
    ].join("");
    const buttons = [tr("update.download"), tr("update.later")];
    if (!update.mandatory) buttons.push(tr("update.skip"));
    const { response } = await box({
      type: "info",
      message: tr("update.available.message", { version: m.version }),
      detail,
      buttons,
      defaultId: 0,
      cancelId: 1,
    });
    if (response === 0) await openUpdateDownload();
    else if (response === 2) await skipUpdate();
    return;
  }
  const keys = {
    up_to_date: "upToDate",
    no_update_info: "noInfo",
    unsupported_origin: "unsupported",
    invalid_manifest: "rejected",
    unsafe_manifest: "rejected",
    failed: "failed",
  };
  const key =
    update?.phase === "failed"
      ? "installFailed"
      : keys[result.status] || "failed";
  await box({
    type: "info",
    message: tr(`update.${key}.message`),
    detail: tr(`update.${key}.detail`, { current: app.getVersion() }),
    buttons: [tr("update.ok")],
  });
}

/** 只打开主进程里已校验过的下载地址（渲染进程不能传 URL）。 */
async function openUpdateDownload() {
  if (updates.update?.phase !== "manual") return { ok: false };
  try {
    await shell.openExternal(updates.update.manifest.download_url);
    return { ok: true };
  } catch {
    return { ok: false };
  }
}

async function skipUpdate() {
  const ok = await updates
    .skip((version) => localPrefs.update({ skipped_version: version }))
    .catch(() => false);
  rebuildTrayMenu();
  return { ok };
}

function scheduleUpdateChecks() {
  if (updateTimer) return;
  const run = () => void runUpdateCheck({ manual: false }).catch(() => {});
  setTimeout(run, UPDATE_STARTUP_DELAY_MS).unref();
  updateTimer = setInterval(run, UPDATE_INTERVAL_MS);
  updateTimer.unref();
}

function aboutInfo() {
  const prefs = localPrefs.get();
  return {
    version: app.getVersion(),
    last_check: prefs.last_check,
    update: updates.update
      ? {
          version: updates.update.manifest.version,
          sha256: updates.update.manifest.sha256,
          phase: updates.update.phase,
          mandatory: updates.update.mandatory,
        }
      : null,
    skipped_version: prefs.skipped_version,
  };
}

function desktopPrefsView() {
  return {
    keep_in_tray: localPrefs.get().keep_in_tray,
    tray_supported: process.platform === "darwin",
    login_item: loginItem.get(),
  };
}

/** 退出确认：对齐 HiFox，主动退出都询问；是否停止本地服务见 quit-flow.cjs。 */
const confirmQuit = createQuitFlow({
  locale,
  isShuttingDown: () => shuttingDown,
  getKeepService: () => localPrefs.get().keep_service_running,
  setKeepService: (value) => localPrefs.update({ keep_service_running: value }),
  getService: async () => {
    let installed = false;
    try {
      await fs.access(launchAgentFile);
      installed = true;
    } catch {
      // 未安装本地服务
    }
    return { installed, daemon: installed ? await daemonStatus() : null };
  },
  showDialog: (options) => {
    const owner = mainWindow && !mainWindow.isDestroyed() ? mainWindow : null;
    return owner
      ? dialog.showMessageBox(owner, options)
      : dialog.showMessageBox(options);
  },
  stopService: () => runOpenFox(["daemon", "stop"]),
  showError: (title, detail) =>
    dialog.showMessageBox({
      type: "warning",
      message: title,
      detail,
      buttons: [tr("update.ok")],
    }),
});

app.on("before-quit", (event) => {
  if (quitting) return;
  event.preventDefault();
  if (confirmingQuit) return;
  confirmingQuit = true;
  void confirmQuit()
    .catch(() => true)
    .then((ok) => {
      confirmingQuit = false;
      if (ok) {
        quitting = true;
        app.quit();
      }
    });
});

/** 升级覆盖自带 CLI 后，空闲时重启本地服务让新版本生效；拿不到可靠状态就提示手动重启。 */
async function rawDaemonStatus() {
  try {
    const parsed = JSON.parse((await runOpenFox(["daemon", "status"])).stdout);
    return {
      state: String(parsed.state || "UNAVAILABLE"),
      // 保留原始值：缺失时 decideRestart 按「无法确认」处理，不能当成 0
      active_runs: parsed.active_runs,
    };
  } catch {
    return null;
  }
}

async function applyRestartPolicy() {
  if (process.env.OPENFOX_DESKTOP_OPENFOX_BINARY) return;
  const decision = decideRestart({
    cliState: cli.state,
    daemon: await rawDaemonStatus(),
    pending: localPrefs.get().pending_service_restart,
  });
  if (decision === "restart") {
    stopRestartTimer();
    try {
      await localPrefs.update({ pending_service_restart: false });
      await runOpenFox(["daemon", "restart"]);
    } catch {
      await notifyManualRestart();
    }
  } else if (decision === "defer") {
    await localPrefs.update({ pending_service_restart: true }).catch(() => {});
    if (!restartTimer) {
      restartTimer = setInterval(() => {
        void applyRestartPolicy().catch(() => {});
      }, SLEEP_POLL_MS);
    }
  } else if (decision === "manual") {
    stopRestartTimer();
    await notifyManualRestart();
  } else {
    stopRestartTimer();
    if (localPrefs.get().pending_service_restart) {
      await localPrefs
        .update({ pending_service_restart: false })
        .catch(() => {});
    }
  }
}

function stopRestartTimer() {
  if (restartTimer) {
    clearInterval(restartTimer);
    restartTimer = null;
  }
}

async function notifyManualRestart() {
  await localPrefs.update({ pending_service_restart: false }).catch(() => {});
  await dialog
    .showMessageBox({
      type: "info",
      message: tr("restart.manual.title"),
      detail: tr("restart.manual.body"),
      buttons: [tr("update.ok")],
    })
    .catch(() => {});
}

handle("openfox:desktop-prefs", () => desktopPrefsView());
handle("openfox:set-keep-in-tray", async (_event, input) => {
  const enabled = input && typeof input === "object" ? input.enabled : null;
  if (typeof enabled !== "boolean" || process.platform !== "darwin") {
    throw new Error("invalid keep-in-tray");
  }
  try {
    await localPrefs.update({ keep_in_tray: enabled });
  } catch {
    throw new Error("failed to save desktop preferences");
  }
  return desktopPrefsView();
});
handle("openfox:set-login-item", (_event, input) => {
  const enabled = input && typeof input === "object" ? input.enabled : null;
  return loginItem.set(enabled);
});
handle("openfox:about", () => aboutInfo());
handle("openfox:check-update", async () => {
  const result = await runUpdateCheck({ manual: true });
  return { status: result.status, about: aboutInfo() };
});
handle("openfox:open-update-download", () => openUpdateDownload());
handle("openfox:skip-update", async () => {
  const result = await skipUpdate();
  return { ...result, about: aboutInfo() };
});

app.whenReady().then(async () => {
  if (process.platform === "darwin" && !app.isPackaged) {
    app.dock.setIcon(path.join(__dirname, "build", "icon.png"));
  }
  // 不阻塞窗口：本机状态与注册请求会等它完成
  networkProxy = createProxy(proxyFile(), safeStorage);
  try {
    await networkProxy.load();
  } catch {
    proxyLoadFailed = true;
  }
  await localPrefs.load();
  cliReady = bootstrapCli();
  settings = await loadSettings(settingsFile());
  // 默认地址文件与设置文件同格式，只取 web_url；不存在时没有默认地址
  defaultWebURL = (await loadSettings(defaultsFile)).web_url;
  const target = webTarget();
  if (target.url) {
    accountOrigin = new URL(target.url).origin;
    accounts = await require("./accounts.cjs").openAccounts(
      app.getPath("userData"),
      accountOrigin,
    );
  }
  if (settings.prevent_sleep) applySleepGuard(await daemonStatus());
  createWindow();
  createTray();
  if (automaticUpdates)
    void removeInstalledPackages(
      path.join(app.getPath("userData"), "desktop-installers"),
      app.getVersion(),
    ).catch(() => {});
  scheduleUpdateChecks();
  // 系统关机/注销时不能卡在退出确认上
  const suspendBrowser = () => browserHosts.get(mainWindow)?.suspend();
  const resumeBrowser = () => browserHosts.get(mainWindow)?.resume();
  powerMonitor.on("suspend", suspendBrowser);
  powerMonitor.on("lock-screen", suspendBrowser);
  powerMonitor.on("resume", resumeBrowser);
  powerMonitor.on("unlock-screen", resumeBrowser);
  powerMonitor.on("shutdown", () => {
    shuttingDown = true;
    quitting = true;
  });
  // 自带 CLI 覆盖后按空闲策略重启本地服务（等 CLI 安装完成再判断）
  void cliReady.then(() => applyRestartPolicy()).catch(() => {});
  app.on("activate", () => {
    if (BrowserWindow.getAllWindows().length === 0) createWindow();
    else showMainWindow();
  });
});

app.on("window-all-closed", () => {
  if (process.platform !== "darwin") app.quit();
});
