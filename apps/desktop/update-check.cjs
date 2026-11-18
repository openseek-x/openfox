// 桌面端「检查更新并提示下载」（不做静默升级：安装包未签名未公证）。
// 清单来自当前连接的中心服务：<origin>/desktop/latest.json。
// 纯 Node 实现，请求函数由调用方注入，便于单测各项安全校验。
//
// 清单格式：{ "version": "0.2.0", "download_url": "https://…/OpenFox-0.2.0-arm64.dmg",
//     "notes": "更新说明", "sha256": "64 位十六进制", "min_version": "0.1.0"(可选) }

const MANIFEST_PATH = "/desktop/latest.json";
const MAX_MANIFEST_BYTES = 16 * 1024;
const REQUEST_TIMEOUT_MS = 10_000;
const MAX_NOTES_LENGTH = 2000;
const MAX_URL_LENGTH = 2048;
// 语义化版本（含可选预发布与构建元数据）；每段数字不带前导零
const SEMVER =
  /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$/;

function parseSemver(value) {
  if (typeof value !== "string" || value.length > 64) return null;
  const match = SEMVER.exec(value);
  if (!match) return null;
  const pre = match[4] ? match[4].split(".") : [];
  // 数字型预发布标识不允许前导零
  for (const id of pre) {
    if (/^\d+$/.test(id) && id.length > 1 && id.startsWith("0")) return null;
  }
  return { major: +match[1], minor: +match[2], patch: +match[3], pre };
}

function comparePre(a, b) {
  // 没有预发布标识的版本高于有预发布标识的
  if (!a.length && !b.length) return 0;
  if (!a.length) return 1;
  if (!b.length) return -1;
  for (let i = 0; i < Math.max(a.length, b.length); i++) {
    if (a[i] === undefined) return -1;
    if (b[i] === undefined) return 1;
    const an = /^\d+$/.test(a[i]);
    const bn = /^\d+$/.test(b[i]);
    if (an && bn) {
      const diff = Number(a[i]) - Number(b[i]);
      if (diff) return diff < 0 ? -1 : 1;
    } else if (an !== bn) {
      return an ? -1 : 1;
    } else if (a[i] !== b[i]) {
      return a[i] < b[i] ? -1 : 1;
    }
  }
  return 0;
}

/** 比较两个版本号：返回 -1 / 0 / 1；任一格式非法返回 null。 */
function compareVersions(left, right) {
  const a = parseSemver(left);
  const b = parseSemver(right);
  if (!a || !b) return null;
  for (const key of ["major", "minor", "patch"]) {
    if (a[key] !== b[key]) return a[key] < b[key] ? -1 : 1;
  }
  return comparePre(a.pre, b.pre);
}

/** 清单地址：只接受 https 的中心服务地址，取其 origin 拼接固定路径。 */
function manifestURL(centerURL) {
  let url;
  try {
    url = new URL(centerURL);
  } catch {
    return null;
  }
  if (url.protocol !== "https:" || url.username || url.password) return null;
  return url.origin + MANIFEST_PATH;
}

function parseHostList(value) {
  return String(value || "")
    .split(",")
    .map((item) => item.trim().toLowerCase())
    .filter(Boolean);
}

/**
 * 校验并规范化清单；任一项不合格返回 { ok:false, reason }。
 * reason：invalid_manifest（格式问题）/ unsafe_manifest（下载地址不可信）。
 */
function validateManifest(raw, { origin, allowedHosts = [] }) {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) {
    return { ok: false, reason: "invalid_manifest" };
  }
  if (!parseSemver(raw.version))
    return { ok: false, reason: "invalid_manifest" };
  if (typeof raw.sha256 !== "string" || !/^[0-9a-fA-F]{64}$/.test(raw.sha256)) {
    return { ok: false, reason: "invalid_manifest" };
  }
  if (raw.min_version !== undefined && !parseSemver(raw.min_version)) {
    return { ok: false, reason: "invalid_manifest" };
  }
  if (raw.notes !== undefined && typeof raw.notes !== "string") {
    return { ok: false, reason: "invalid_manifest" };
  }
  if (
    typeof raw.download_url !== "string" ||
    raw.download_url.length > MAX_URL_LENGTH
  ) {
    return { ok: false, reason: "invalid_manifest" };
  }
  let download;
  try {
    download = new URL(raw.download_url);
  } catch {
    return { ok: false, reason: "invalid_manifest" };
  }
  if (
    download.protocol !== "https:" ||
    download.username ||
    download.password
  ) {
    return { ok: false, reason: "unsafe_manifest" };
  }
  const sameOrigin = download.origin === origin;
  const allowed = allowedHosts.includes(download.hostname.toLowerCase());
  if (!sameOrigin && !allowed) return { ok: false, reason: "unsafe_manifest" };
  return {
    ok: true,
    manifest: {
      version: raw.version,
      download_url: download.href,
      sha256: raw.sha256.toLowerCase(),
      notes: (raw.notes || "").slice(0, MAX_NOTES_LENGTH),
      min_version: raw.min_version || null,
    },
  };
}

/** 判断清单相对当前版本的结果与是否被用户跳过。 */
function evaluate(manifest, { current, skippedVersion }) {
  const cmp = compareVersions(manifest.version, current);
  if (cmp === null || cmp <= 0) return { status: "up_to_date" };
  const mandatory =
    manifest.min_version !== null &&
    compareVersions(current, manifest.min_version) === -1;
  return {
    status: "update_available",
    // 低于最低版本的强提示不允许跳过
    skipped: !mandatory && skippedVersion === manifest.version,
    mandatory,
  };
}

/**
 * 检查更新。fetchText(url, {timeoutMs, maxBytes}) 由调用方注入：
 * 返回 { status, text }；超时、超限、网络错误时抛错。
 * 结果 status：up_to_date / update_available / no_update_info / invalid_manifest /
 * unsafe_manifest / unsupported_origin / failed。
 */
async function checkForUpdate({
  centerURL,
  current,
  skippedVersion = null,
  allowedHosts = [],
  fetchText,
}) {
  const url = manifestURL(centerURL);
  if (!url) return { status: "unsupported_origin" };
  let response;
  try {
    response = await fetchText(url, {
      timeoutMs: REQUEST_TIMEOUT_MS,
      maxBytes: MAX_MANIFEST_BYTES,
    });
  } catch {
    return { status: "failed" };
  }
  if (response.status === 404) return { status: "no_update_info" };
  if (response.status !== 200) return { status: "failed" };
  let raw;
  try {
    raw = JSON.parse(response.text);
  } catch {
    // 静态站点把不存在的路径回退到 index.html（状态码 200）：HTML 内容等同于没有清单
    if (/^\s*</.test(response.text)) return { status: "no_update_info" };
    return { status: "invalid_manifest" };
  }
  const checked = validateManifest(raw, {
    origin: new URL(url).origin,
    allowedHosts,
  });
  if (!checked.ok) return { status: checked.reason };
  const verdict = evaluate(checked.manifest, { current, skippedVersion });
  if (verdict.status === "up_to_date") {
    return { status: "up_to_date", version: checked.manifest.version };
  }
  return { ...verdict, manifest: checked.manifest };
}

module.exports = {
  MANIFEST_PATH,
  MAX_MANIFEST_BYTES,
  REQUEST_TIMEOUT_MS,
  checkForUpdate,
  compareVersions,
  evaluate,
  manifestURL,
  parseHostList,
  parseSemver,
  validateManifest,
};
