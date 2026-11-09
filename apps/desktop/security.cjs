// 桌面主进程的安全相关纯函数：IPC 来源校验、网页地址校验、安装令牌校验、CSP、本地文件范围、设置持久化、防休眠状态。
// 不依赖 electron，便于用 node:test 单测（security.test.cjs）。

const fs = require("node:fs/promises");
const path = require("node:path");
const { fileURLToPath } = require("node:url");

const INSTALL_TOKEN_PREFIX = "ofx_v2_";
const INSTALL_TOKEN_MAX_LENGTH = 512;
// 前缀之后是 base64url（无填充）
const INSTALL_TOKEN_PATTERN = /^ofx_v2_[A-Za-z0-9_-]+$/;

/**
 * 主窗口可信来源：网页地址的 origin（远程网页），以及本地 file://（加载失败时的离线页）。
 * 本地文件范围由 resolveLocalFile 限制在 renderer 目录内，所以 file 只校验协议与空主机。
 */
function trustedSources(webURL) {
  const sources = [{ kind: "file" }];
  if (webURL)
    sources.unshift({ kind: "origin", origin: new URL(webURL).origin });
  return sources;
}

function isTrustedURL(raw, sources) {
  if (typeof raw !== "string" || !raw) return false;
  let url;
  try {
    url = new URL(raw);
  } catch {
    return false;
  }
  return sources.some((source) =>
    source.kind === "origin"
      ? url.origin === source.origin
      : url.protocol === "file:" && url.host === "",
  );
}

/**
 * IPC 来源校验：必须来自主窗口的顶层 frame，且 frame 当前 URL 与主窗口加载的来源一致。
 * senderFrame 为空（frame 已销毁或导航中）一律拒绝。
 */
function isTrustedSender(event, expected) {
  if (!event || !expected) return false;
  const sender = event.sender;
  if (!sender || sender.id !== expected.webContentsId) return false;
  const frame = event.senderFrame;
  if (!frame || frame.parent) return false;
  return isTrustedURL(frame.url, expected.sources);
}

/** 安装令牌只做格式校验（前缀、字符集、长度），内容由 CLI 解析。 */
function isValidInstallToken(value) {
  return (
    typeof value === "string" &&
    value.length > INSTALL_TOKEN_PREFIX.length &&
    value.length <= INSTALL_TOKEN_MAX_LENGTH &&
    INSTALL_TOKEN_PATTERN.test(value)
  );
}

/** 只接受 http(s) 绝对地址，其余一律当作没有。 */
function httpURLOrNull(value) {
  if (typeof value !== "string") return null;
  try {
    const parsed = new URL(value.trim());
    return parsed.protocol === "https:" || parsed.protocol === "http:"
      ? value.trim()
      : null;
  } catch {
    return null;
  }
}

/** 外链和 OAuth 导航不接受文件、自定义协议或 URL 中的账号密码。 */
function isSafeWebLink(value) {
  if (!httpURLOrNull(value)) return false;
  const url = new URL(value);
  return !url.username && !url.password;
}

/** 只接受主页面发起的链接；OAuth 使用现有固定窗口名，普通链接交系统浏览器。 */
function newWindowAction({ url, frameName }, sourceURL, webURL) {
  if (!webURL || !isSafeWebLink(url)) return "deny";
  if (
    !isTrustedURL(sourceURL, [
      { kind: "origin", origin: new URL(webURL).origin },
    ])
  )
    return "deny";
  return /^openfox-(feishu|lark|dingtalk|github|gitlab|gitlab_cn|gitee|mcp)-oauth$/.test(
    frameName,
  )
    ? "oauth"
    : "external";
}

/**
 * 解析 openfox computer status --json 的输出。已注册时带上本机注册的中心地址 center_url
 * （只接受 http(s) 绝对地址；旧版 CLI 没有该字段则不传）。
 */
function parseRegistrationStatus(stdout) {
  const parsed = JSON.parse(stdout);
  if (parsed.registered !== true) {
    return {
      registered: false,
      reason:
        parsed.reason === "invalid_config"
          ? "invalid_config"
          : "not_registered",
    };
  }
  const result = {
    registered: true,
    organization_id: String(parsed.organization_id || ""),
    device_id: String(parsed.device_id || ""),
  };
  const centerURL = httpURLOrNull(parsed.center_url);
  if (centerURL) result.center_url = centerURL;
  return result;
}

/**
 * 网页地址：桌面端加载服务端提供的网页，接口走网页同源的 /api。
 * 必须是 HTTP(S)，不带账号密码、查询串和片段；未配置返回 null，格式不对抛错。
 */
function parseWebURL(raw) {
  const value = String(raw || "").trim();
  if (!value) return null;
  const url = new URL(value);
  if (
    !["http:", "https:"].includes(url.protocol) ||
    url.username ||
    url.password ||
    url.search ||
    url.hash
  ) {
    throw new Error(
      "web URL must be an HTTP(S) URL without credentials, query or fragment",
    );
  }
  return url.href;
}

/**
 * 本地离线页的 CSP：只允许本地脚本，不允许 unsafe-eval，不能联网。
 * style-src 允许 'unsafe-inline'，与网页端一致。
 */
function buildCsp() {
  return [
    "default-src 'self'",
    "script-src 'self'",
    "style-src 'self' 'unsafe-inline'",
    "img-src 'self' data: blob:",
    "font-src 'self' data:",
    "connect-src 'self'",
    "object-src 'none'",
    "base-uri 'none'",
    "form-action 'none'",
    "frame-src 'none'",
  ].join("; ");
}

/** 在 <head> 开头插入 CSP meta（file:// 响应头不一定生效，meta 兜底）。 */
function injectCspMeta(html, csp) {
  const escaped = csp.replace(/&/g, "&amp;").replace(/"/g, "&quot;");
  const meta = `<meta http-equiv="Content-Security-Policy" content="${escaped}" />`;
  const match = /<head[^>]*>/i.exec(html);
  if (!match) return meta + html;
  const at = match.index + match[0].length;
  return html.slice(0, at) + meta + html.slice(at);
}

/**
 * file:// 请求映射到 distDir 内的文件；目录外（含 ../ 与其他盘符）返回 null。
 * BrowserRouter 刷新后的路径（如 file:///o/xxx）不在 dist 内，同样返回 null，由调用方回退到 index.html。
 */
function resolveLocalFile(requestURL, distDir) {
  let filePath;
  try {
    filePath = fileURLToPath(requestURL);
  } catch {
    return null;
  }
  const root = path.resolve(distDir);
  const resolved = path.resolve(filePath);
  const relative = path.relative(root, resolved);
  // relative 为空 = 请求的是 dist 目录本身
  if (
    !relative ||
    relative === ".." ||
    relative.startsWith(`..${path.sep}`) ||
    path.isAbsolute(relative)
  )
    return null;
  return resolved;
}

const DEFAULT_SETTINGS = Object.freeze({ prevent_sleep: false, web_url: null });

/** 规范化设置：只保留已知字段；web_url 原样保留字符串，使用前再用 parseWebURL 校验。 */
function normalizeSettings(value) {
  const web =
    value && typeof value.web_url === "string" && value.web_url.trim()
      ? value.web_url.trim()
      : null;
  return {
    prevent_sleep: Boolean(value && value.prevent_sleep === true),
    web_url: web,
  };
}

/** 读取桌面设置；文件不存在或损坏时返回默认值。 */
async function loadSettings(file) {
  try {
    return normalizeSettings(JSON.parse(await fs.readFile(file, "utf8")));
  } catch {
    return { ...DEFAULT_SETTINGS };
  }
}

/** 原子写入（临时文件 + rename），权限 0600。 */
async function saveSettings(file, settings) {
  // web_url 一并写回：切换防止休眠时不能丢掉用户手工配置的网页地址
  const body = JSON.stringify(normalizeSettings(settings));
  const temp = `${file}.${process.pid}.${Date.now()}.tmp`;
  await fs.mkdir(path.dirname(file), { recursive: true });
  await fs.writeFile(temp, body, { mode: 0o600 });
  try {
    await fs.rename(temp, file);
  } catch (error) {
    await fs.rm(temp, { force: true });
    throw error;
  }
}

/**
 * 防止休眠：启用且本机有活动 Run 时持有 blocker，否则释放。
 * blocker 由调用方注入（electron powerSaveBlocker），重复 update 不会重复 start。
 */
function createSleepGuard(blocker) {
  let id = null;
  return {
    update({ enabled, activeRuns }) {
      const want = enabled === true && Number(activeRuns) > 0;
      if (want && id === null) id = blocker.start("prevent-app-suspension");
      if (!want && id !== null) {
        if (blocker.isStarted(id)) blocker.stop(id);
        id = null;
      }
      return id !== null;
    },
    get active() {
      return id !== null;
    },
  };
}

/** daemon status 的活动 Run 数；字段缺失或非法按 0。 */
function activeRunCount(status) {
  const value = status && status.active_runs;
  return Number.isInteger(value) && value > 0 ? value : 0;
}

/**
 * register 命令失败原因：只看退出码，不读取也不回传命令输出（可能含路径等信息）。
 */
// openfox computer register 的退出码（cmd/openfox/main.go）
const REGISTER_EXIT_REASONS = Object.freeze({
  2: "invalid_token",
  3: "already_registered",
  4: "token_rejected",
  5: "center_unavailable",
});

function registerFailureReason(error) {
  if (!error) return null;
  if (error.code === "ERR_CHILD_PROCESS_STDIO_MAXBUFFER") return "failed";
  if (error.killed || error.signal) return "timeout";
  if (error.code === "ENOENT" || error.code === "EACCES") return "unavailable";
  if (Number.isInteger(error.code) && REGISTER_EXIT_REASONS[error.code])
    return REGISTER_EXIT_REASONS[error.code];
  return "failed";
}

// A renderer connected to a different center cannot borrow the local center's cookies
// to inspect private directory names. Source-mode development can use its loopback proxy.
function isWorkDirectoryOrigin(frameURL, centerURL, developmentProxy = false) {
  try {
    const page = new URL(frameURL);
    const center = new URL(centerURL);
    if (!["https:", "http:"].includes(page.protocol)) return false;
    return (
      page.origin === center.origin ||
      (developmentProxy &&
        page.protocol === center.protocol &&
        page.hostname === center.hostname &&
        ["127.0.0.1", "[::1]", "localhost"].includes(page.hostname))
    );
  } catch {
    return false;
  }
}

module.exports = {
  isWorkDirectoryOrigin,
  INSTALL_TOKEN_MAX_LENGTH,
  INSTALL_TOKEN_PREFIX,
  activeRunCount,
  buildCsp,
  createSleepGuard,
  injectCspMeta,
  isTrustedSender,
  isSafeWebLink,
  newWindowAction,
  isTrustedURL,
  isValidInstallToken,
  loadSettings,
  parseRegistrationStatus,
  parseWebURL,
  registerFailureReason,
  resolveLocalFile,
  saveSettings,
  trustedSources,
};
