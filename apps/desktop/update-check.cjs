// Desktop releases are published under the official repository's vX.Y.Z tags.
const REPOSITORY = "openseek-x/openfox";
const RELEASE_API = `https://api.github.com/repos/${REPOSITORY}/releases/latest`;
const MANIFEST_PATH = "/desktop/latest.json"; // Feed emitted for already-installed 0.1.2 clients.
const REQUEST_TIMEOUT_MS = 10_000;
const MAX_RELEASE_BYTES = 128 * 1024;
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

/** GitHub supplies the tag and the SHA-256 of each uploaded release asset. */
async function checkForUpdate({
  current,
  skippedVersion,
  requireSignature = false,
  fetchText,
}) {
  let response;
  try {
    response = await fetchText(RELEASE_API, {
      timeoutMs: REQUEST_TIMEOUT_MS,
      maxBytes: MAX_RELEASE_BYTES,
    });
  } catch {
    return { status: "failed" };
  }
  if (response.status === 404) return { status: "no_update_info" };
  if (response.status !== 200) return { status: "failed" };

  let release;
  try {
    release = JSON.parse(response.text);
  } catch {
    return { status: "invalid_manifest" };
  }
  const match = /^v(\d+\.\d+\.\d+)$/.exec(release?.tag_name);
  if (
    !match ||
    !parseSemver(match[1]) ||
    release.draft !== false ||
    release.prerelease !== false ||
    !Array.isArray(release.assets)
  ) {
    return { status: "invalid_manifest" };
  }

  const version = match[1];
  if (compareVersions(version, current) !== 1)
    return { status: "up_to_date", version };
  const name = `OpenFox-${version}-arm64.dmg`;
  const base = `https://github.com/${REPOSITORY}/releases/download/v${version}/`;
  const assets = [];
  for (const filename of requireSignature ? [name, name + ".sig"] : [name]) {
    const asset = release.assets.find((entry) => entry?.name === filename);
    if (
      asset?.state !== "uploaded" ||
      !/^sha256:[a-f0-9]{64}$/.test(asset.digest)
    )
      return { status: "invalid_manifest" };
    if (asset.browser_download_url !== base + filename)
      return { status: "unsafe_manifest" };
    assets.push(asset);
  }
  return {
    status: "update_available",
    manifest: {
      version,
      download_url: assets[0].browser_download_url,
      sha256: assets[0].digest.slice(7),
      ...(requireSignature && { signature_sha256: assets[1].digest.slice(7) }),
      notes:
        typeof release.body === "string" ? release.body.slice(0, 2000) : "",
      min_version: null,
    },
    mandatory: false,
    skipped: skippedVersion === version,
  };
}

module.exports = { checkForUpdate, compareVersions, manifestURL };
