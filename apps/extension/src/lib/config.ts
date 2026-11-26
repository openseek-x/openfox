// 连接配置：服务器地址校验与令牌的本地存储。
// 令牌只写入 chrome.storage.local，不进入日志、URL 或错误文案。

export type ServerUrlResult =
  { ok: true; origin: string } | { ok: false; reason: string };

/** 本机开发允许使用明文 http 的主机名。 */
const LOCAL_HTTP_HOSTS = new Set(["localhost", "127.0.0.1"]);

/**
 * 校验服务器地址：只允许 https，或本机开发用的 http://localhost、http://127.0.0.1。
 * 地址不得带账号密码、路径、查询串或片段，返回规范化后的 origin。
 */
export function parseServerUrl(input: string): ServerUrlResult {
  const text = input.trim();
  if (!text) return { ok: false, reason: "请填写服务器地址" };
  let url: URL;
  try {
    url = new URL(text);
  } catch {
    return { ok: false, reason: "服务器地址格式不正确" };
  }
  if (url.username || url.password) {
    return { ok: false, reason: "服务器地址不能包含账号或密码" };
  }
  if (url.pathname !== "/" || url.search || url.hash) {
    return { ok: false, reason: "服务器地址只需填写协议、主机和端口" };
  }
  const isLocalHttp =
    url.protocol === "http:" && LOCAL_HTTP_HOSTS.has(url.hostname);
  if (url.protocol !== "https:" && !isLocalHttp) {
    return {
      ok: false,
      reason:
        "仅支持 https 地址，本机开发可使用 http://localhost 或 http://127.0.0.1",
    };
  }
  return { ok: true, origin: url.origin };
}

/** 运行时申请跨源权限时使用的匹配模式；端口不参与 host 权限匹配。 */
export function originPermissionPattern(origin: string): string {
  const url = new URL(origin);
  return `${url.protocol}//${url.hostname}/*`;
}

export interface ConnectionSettings {
  serverOrigin: string;
  token: string;
}

const STORAGE_KEY = "openfox.connection";

/** 读取已保存的连接配置；内容缺失或结构不对时视为未配置。 */
export async function loadSettings(): Promise<ConnectionSettings | null> {
  const stored = await chrome.storage.local.get(STORAGE_KEY);
  return parseSettings(stored[STORAGE_KEY]);
}

function parseSettings(value: unknown): ConnectionSettings | null {
  if (typeof value !== "object" || value === null) return null;
  const { serverOrigin, token } = value as Record<string, unknown>;
  if (typeof serverOrigin !== "string" || typeof token !== "string") {
    return null;
  }
  const parsed = parseServerUrl(serverOrigin);
  if (!parsed.ok || !token.trim()) return null;
  return { serverOrigin: parsed.origin, token };
}

/** 监听其他窗口里的断开和换账号操作。若首读较慢，以最新存储事件为准。 */
export function watchSettings(
  onChange: (value: ConnectionSettings | null) => void,
) {
  let initial = true;
  const changed: chrome.storage.ChangeListener = (changes, area) => {
    if (area !== "local" || !(STORAGE_KEY in changes)) return;
    initial = false;
    onChange(parseSettings(changes[STORAGE_KEY]?.newValue));
  };
  chrome.storage.onChanged.addListener(changed);
  void loadSettings().then(
    (value) => {
      if (initial) onChange(value);
    },
    () => {
      if (initial) onChange(null);
    },
  );
  return () => {
    initial = false;
    chrome.storage.onChanged.removeListener(changed);
  };
}

export async function saveSettings(
  settings: ConnectionSettings,
): Promise<void> {
  await chrome.storage.local.set({ [STORAGE_KEY]: settings });
}

/** 断开连接：清除令牌。 */
export async function clearSettings(): Promise<void> {
  await chrome.storage.local.remove(STORAGE_KEY);
}
