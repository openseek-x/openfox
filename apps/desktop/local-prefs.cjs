// 桌面端本机偏好（userData/desktop-local-prefs.json）。
// 与 desktop-settings.json 分开存放：后者是防止休眠与网页地址，字段已有严格测试；
// 这里的关闭行为、跳过的版本等只属于这台电脑，不随账号同步。

const fs = require("node:fs/promises");
const path = require("node:path");

const UPDATE_STATUSES = new Set([
  "up_to_date",
  "update_available",
  "no_update_info",
  "invalid_manifest",
  "unsafe_manifest",
  "unsupported_origin",
  "failed",
]);

const DEFAULTS = Object.freeze({
  keep_in_tray: false,
  // 退出确认里「保持后台服务运行」的上次选择（默认勾选）
  keep_service_running: true,
  skipped_version: null,
  last_check: null,
  pending_service_restart: false,
});

function shortString(value, max) {
  return typeof value === "string" && value.length > 0 && value.length <= max
    ? value
    : null;
}

/** 规范化：只保留已知字段，非法值回退默认。 */
function normalizePrefs(value) {
  const src = value && typeof value === "object" ? value : {};
  let lastCheck = null;
  const lc = src.last_check;
  if (
    lc &&
    typeof lc === "object" &&
    typeof lc.at === "string" &&
    !Number.isNaN(Date.parse(lc.at)) &&
    UPDATE_STATUSES.has(lc.status)
  ) {
    lastCheck = {
      at: lc.at,
      status: lc.status,
      version: shortString(lc.version, 64),
    };
  }
  return {
    keep_in_tray: src.keep_in_tray === true,
    keep_service_running: src.keep_service_running !== false,
    skipped_version: shortString(src.skipped_version, 64),
    last_check: lastCheck,
    pending_service_restart: src.pending_service_restart === true,
  };
}

async function writeAtomic(file, body) {
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
 * 创建偏好存储。get() 返回内存快照；update(patch) 串行写盘，失败时抛错且内存值不变。
 */
function createLocalPrefs(file) {
  let current = { ...DEFAULTS };
  let queue = Promise.resolve();
  return {
    async load() {
      try {
        current = normalizePrefs(JSON.parse(await fs.readFile(file, "utf8")));
      } catch {
        current = { ...DEFAULTS };
      }
      return current;
    },
    get() {
      return current;
    },
    update(patch) {
      const run = queue.then(async () => {
        const next = normalizePrefs({ ...current, ...patch });
        await writeAtomic(file, JSON.stringify(next));
        current = next;
        return current;
      });
      // 前一次失败不能卡住后面的写入
      queue = run.catch(() => {});
      return run;
    },
  };
}

module.exports = {
  DEFAULTS,
  UPDATE_STATUSES,
  createLocalPrefs,
  normalizePrefs,
};
