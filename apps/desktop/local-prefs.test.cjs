const { test } = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const os = require("node:os");
const path = require("node:path");
const {
  DEFAULTS,
  createLocalPrefs,
  normalizePrefs,
} = require("./local-prefs.cjs");

async function tempFile(t) {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), "openfox-prefs-"));
  t.after(() => fs.rm(dir, { recursive: true, force: true }));
  return path.join(dir, "prefs.json");
}

test("缺失或损坏的文件回退默认值，关闭行为默认不保留在菜单栏", async (t) => {
  const file = await tempFile(t);
  const prefs = createLocalPrefs(file);
  assert.deepEqual(await prefs.load(), DEFAULTS);
  assert.equal(prefs.get().keep_in_tray, false);
  await fs.writeFile(file, "{not json");
  assert.deepEqual(await prefs.load(), DEFAULTS);
});

test("更新后写盘并可重新读取，权限 0600，未知字段被丢弃", async (t) => {
  const file = await tempFile(t);
  const prefs = createLocalPrefs(file);
  await prefs.update({ keep_in_tray: true, skipped_version: "0.2.0", evil: 1 });
  const again = createLocalPrefs(file);
  const loaded = await again.load();
  assert.equal(loaded.keep_in_tray, true);
  assert.equal(loaded.skipped_version, "0.2.0");
  assert.equal("evil" in loaded, false);
  assert.equal((await fs.stat(file)).mode & 0o777, 0o600);
});

test("非法值被规范化：只有严格为 true 才开启，版本与检查记录要合法", () => {
  assert.equal(normalizePrefs({ keep_in_tray: "true" }).keep_in_tray, false);
  assert.equal(normalizePrefs({ skipped_version: "" }).skipped_version, null);
  assert.equal(
    normalizePrefs({ skipped_version: "x".repeat(65) }).skipped_version,
    null,
  );
  assert.equal(
    normalizePrefs({ last_check: { at: "bad", status: "failed" } }).last_check,
    null,
  );
  assert.equal(
    normalizePrefs({ last_check: { at: "2026-11-12T00:00:00Z", status: "x" } })
      .last_check,
    null,
  );
  assert.deepEqual(
    normalizePrefs({
      last_check: { at: "2026-11-12T00:00:00Z", status: "up_to_date" },
    }).last_check,
    { at: "2026-11-12T00:00:00Z", status: "up_to_date", version: null },
  );
});

test("写盘失败时抛错且内存值不变，后续写入不被卡住", async (t) => {
  const file = await tempFile(t);
  // 目标路径是目录：rename 必然失败
  await fs.mkdir(file);
  const prefs = createLocalPrefs(file);
  await assert.rejects(prefs.update({ keep_in_tray: true }));
  assert.equal(prefs.get().keep_in_tray, false);
  await fs.rm(file, { recursive: true });
  await prefs.update({ keep_in_tray: true });
  assert.equal(prefs.get().keep_in_tray, true);
});

test("退出确认的「保持后台服务运行」默认勾选，只有明确 false 才取消", () => {
  assert.equal(DEFAULTS.keep_service_running, true);
  assert.equal(normalizePrefs({}).keep_service_running, true);
  assert.equal(
    normalizePrefs({ keep_service_running: "x" }).keep_service_running,
    true,
  );
  assert.equal(
    normalizePrefs({ keep_service_running: false }).keep_service_running,
    false,
  );
});
