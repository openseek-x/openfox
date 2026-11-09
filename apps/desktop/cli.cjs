// 桌面端自带 CLI：把安装包里的 openfox 拷到 ~/.local/bin/openfox，并把该目录加进终端 PATH。
// 纯 Node 实现，不依赖 electron，便于用 node:test 单测（cli.test.cjs）。
//
// - 安装：sha256 相同则跳过；否则写同目录临时文件 → chmod 755 → rename（原子替换）。
// - 只替换自己装的文件：目标已存在且不是本应用安装的（记录文件里的 sha256 对不上）就不动它，改用安装包里的路径。
// - PATH：向已存在的 shell 配置文件（以及 ~/.zprofile）追加一段带标记的脚本，已有标记则跳过；只在目录存在时才加入 PATH。

const crypto = require("node:crypto");
const fs = require("node:fs/promises");
const path = require("node:path");

const CLI_NAME = "openfox";
const MARKER = "# Added by OpenFox CLI installer";
// zsh / bash / sh 的常见启动文件
const PROFILE_FILES = [
  ".zprofile",
  ".zshrc",
  ".bash_profile",
  ".bashrc",
  ".profile",
];
// 文件不存在时也创建的配置（macOS 默认 shell 是 zsh）
const CREATE_PROFILES = new Set([".zprofile"]);

function installPaths(home) {
  const binDir = path.join(home, ".local", "bin");
  return {
    binDir,
    target: path.join(binDir, CLI_NAME),
    // 记录本应用安装的文件哈希：用于判断目标是不是自己装的
    record: path.join(home, ".local", "share", "openfox", "cli-install.json"),
  };
}

async function sha256File(file) {
  const hash = crypto.createHash("sha256");
  hash.update(await fs.readFile(file));
  return hash.digest("hex");
}

async function readRecord(file) {
  try {
    const parsed = JSON.parse(await fs.readFile(file, "utf8"));
    return parsed && typeof parsed.sha256 === "string" ? parsed : null;
  } catch {
    return null;
  }
}

async function writeAtomic(file, data, mode) {
  await fs.mkdir(path.dirname(file), { recursive: true });
  const temp = `${file}.${process.pid}.${Date.now()}.tmp`;
  await fs.writeFile(temp, data, { mode });
  try {
    await fs.chmod(temp, mode);
    await fs.rename(temp, file);
  } catch (error) {
    await fs.rm(temp, { force: true });
    throw error;
  }
}

/**
 * 安装自带 CLI。返回 { path, state }：
 * state = installed（新装或更新）/ unchanged（已是同一版本）/ foreign（目标是别人的文件，未覆盖，path 指向安装包）。
 * 安装包里没有 CLI 时抛错 bundled_missing。
 */
async function installBundledCli({ bundled, home }) {
  let bundledSha;
  try {
    const info = await fs.stat(bundled);
    if (!info.isFile()) throw new Error("not a file");
    bundledSha = await sha256File(bundled);
  } catch {
    const error = new Error("bundled CLI is missing");
    error.code = "bundled_missing";
    throw error;
  }
  const { target, record } = installPaths(home);
  let current = null;
  try {
    const info = await fs.lstat(target);
    current = info.isFile() ? await sha256File(target) : "not-a-file";
  } catch {
    current = null;
  }
  if (current === bundledSha) {
    // 同一版本：补写记录（旧版本装过但没有记录时）
    await writeAtomic(
      record,
      JSON.stringify({ sha256: bundledSha, path: target }),
      0o600,
    );
    return { path: target, state: "unchanged" };
  }
  if (current !== null) {
    const previous = await readRecord(record);
    if (!previous || previous.sha256 !== current) {
      return { path: bundled, state: "foreign" };
    }
  }
  await writeAtomic(target, await fs.readFile(bundled), 0o755);
  await writeAtomic(
    record,
    JSON.stringify({ sha256: bundledSha, path: target }),
    0o600,
  );
  return { path: target, state: "installed" };
}

/** shell 配置里追加的脚本：目录存在且不在 PATH 里时才加到最前面。 */
function pathSnippet(binDir) {
  const quoted = binDir.replace(/(["\\$`])/g, "\\$1");
  return [
    "",
    MARKER,
    `if [ -d "${quoted}" ]; then`,
    '  case ":$PATH:" in',
    `    *":${quoted}:"*) ;;`,
    `    *) export PATH="${quoted}:$PATH" ;;`,
    "  esac",
    "fi",
    "",
  ].join("\n");
}

/**
 * 把 CLI 目录加进终端 PATH。返回改动过的文件名列表（已有标记的文件不改）。
 * 不跟随符号链接写入：配置文件是链接时跳过，避免改到仓库或别处的文件。
 */
async function ensureOnPath({ home, binDir }) {
  const changed = [];
  const snippet = pathSnippet(binDir);
  for (const name of PROFILE_FILES) {
    const file = path.join(home, name);
    let content = null;
    try {
      const info = await fs.lstat(file);
      if (!info.isFile()) continue;
      content = await fs.readFile(file, "utf8");
    } catch {
      if (!CREATE_PROFILES.has(name)) continue;
    }
    if (content !== null && content.includes(MARKER)) continue;
    await fs.appendFile(file, snippet, { mode: 0o644 });
    changed.push(name);
  }
  return changed;
}

module.exports = {
  MARKER,
  PROFILE_FILES,
  ensureOnPath,
  installBundledCli,
  installPaths,
  pathSnippet,
};
