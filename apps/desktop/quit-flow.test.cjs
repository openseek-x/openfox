const { test } = require("node:test");
const assert = require("node:assert/strict");
const { createQuitFlow } = require("./quit-flow.cjs");
const { MESSAGES } = require("./messages.cjs");

const RUNNING = (active_runs = 0) => ({
  installed: true,
  daemon: { state: "RUNNING", active_runs },
});

/** 组装一个带记录的假环境；dialog 返回值可按用例指定。 */
function setup({
  service = RUNNING(),
  keep = true,
  dialog = { response: 0, checkboxChecked: true },
  stopError = null,
  shuttingDown = false,
  serviceError = false,
  locale = "zh-CN",
} = {}) {
  const log = { dialogs: [], stops: 0, saved: [], errors: [], keep };
  const confirm = createQuitFlow({
    locale,
    isShuttingDown: () => shuttingDown,
    getService: async () => {
      if (serviceError) throw new Error("boom");
      return service;
    },
    getKeepService: () => log.keep,
    setKeepService: async (value) => {
      log.saved.push(value);
      log.keep = value;
    },
    showDialog: async (options) => {
      log.dialogs.push(options);
      return dialog;
    },
    stopService: async () => {
      log.stops += 1;
      if (stopError) throw stopError;
    },
    showError: async (title, detail) => {
      log.errors.push({ title, detail });
    },
  });
  return { confirm, log };
}

test("默认勾选「保持后台服务运行」，勾选时只退出应用、不停止服务", async () => {
  const { confirm, log } = setup();
  assert.equal(await confirm(), true);
  assert.equal(log.dialogs.length, 1);
  assert.equal(log.dialogs[0].checkboxChecked, true);
  assert.equal(log.dialogs[0].checkboxLabel, "保持后台服务运行");
  assert.equal(log.stops, 0);
  assert.deepEqual(log.saved, []);
});

test("没有运行中任务也会弹窗（主动退出一律询问）", async () => {
  const { confirm, log } = setup({ service: RUNNING(0) });
  await confirm();
  assert.equal(log.dialogs.length, 1);
  assert.equal(log.dialogs[0].detail.includes("个正在运行的任务"), false);
});

test("取消勾选后确认退出：先停止本地服务再允许退出，并记住选择", async () => {
  const { confirm, log } = setup({
    dialog: { response: 0, checkboxChecked: false },
  });
  assert.equal(await confirm(), true);
  assert.equal(log.stops, 1);
  assert.deepEqual(log.saved, [false]);
  assert.equal(log.errors.length, 0);
});

test("停止在退出之前完成：停止未返回时不放行", async () => {
  let release;
  const gate = new Promise((resolve) => {
    release = resolve;
  });
  let resolved = false;
  const confirm = createQuitFlow({
    locale: "en-US",
    isShuttingDown: () => false,
    getService: async () => RUNNING(0),
    getKeepService: () => true,
    setKeepService: async () => {},
    showDialog: async () => ({ response: 0, checkboxChecked: false }),
    stopService: () => gate,
    showError: async () => {},
  });
  const pending = confirm().then((ok) => {
    resolved = true;
    return ok;
  });
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(resolved, false);
  release();
  assert.equal(await pending, true);
});

test("停止失败仍然退出，并提示失败原因", async () => {
  const { confirm, log } = setup({
    dialog: { response: 0, checkboxChecked: false },
    stopError: new Error("daemon action failed"),
  });
  assert.equal(await confirm(), true);
  assert.equal(log.stops, 1);
  assert.equal(log.errors.length, 1);
  assert.equal(log.errors[0].detail.includes("daemon action failed"), true);
});

test("有运行中任务时文案明确写出停止会中断 N 个任务（中英文）", async () => {
  const zh = setup({ service: RUNNING(3) });
  await zh.confirm();
  assert.equal(
    zh.log.dialogs[0].detail.includes("停止后台服务会中断 3 个正在运行的任务"),
    true,
  );
  const en = setup({ service: RUNNING(2), locale: "en-US" });
  await en.confirm();
  assert.equal(
    en.log.dialogs[0].detail.includes(
      "Stopping the background service will interrupt 2 running task(s)",
    ),
    true,
  );
});

test("点取消：不退出、不停止服务、不改偏好", async () => {
  const { confirm, log } = setup({
    dialog: { response: 1, checkboxChecked: false },
  });
  assert.equal(await confirm(), false);
  assert.equal(log.stops, 0);
  assert.deepEqual(log.saved, []);
});

test("上次选择作为默认值：上次取消勾选则下次默认不勾选", async () => {
  const { confirm, log } = setup({
    keep: false,
    dialog: { response: 0, checkboxChecked: false },
  });
  await confirm();
  assert.equal(log.dialogs[0].checkboxChecked, false);
  // 选择没变化时不重复写盘
  assert.deepEqual(log.saved, []);
});

test("系统关机/注销：不弹窗、不停止服务", async () => {
  const { confirm, log } = setup({
    shuttingDown: true,
    service: RUNNING(5),
  });
  assert.equal(await confirm(), true);
  assert.equal(log.dialogs.length, 0);
  assert.equal(log.stops, 0);
});

test("未安装 / 已停止 / 状态不可用：复选框不出现并说明，不停止服务、不报多余错误", async () => {
  const cases = [
    [{ installed: false, daemon: null }, "尚未安装本地服务"],
    [{ installed: true, daemon: { state: "STOPPED" } }, "已停止"],
    [
      { installed: true, daemon: { state: "UNAVAILABLE" } },
      "无法获取本地服务状态",
    ],
  ];
  for (const [service, text] of cases) {
    const { confirm, log } = setup({
      service,
      dialog: { response: 0, checkboxChecked: false },
    });
    assert.equal(await confirm(), true);
    assert.equal("checkboxLabel" in log.dialogs[0], false);
    assert.equal(log.dialogs[0].detail.includes(text), true, text);
    assert.equal(log.stops, 0);
    assert.equal(log.errors.length, 0);
  }
});

test("读取服务状态抛错：按状态不可用处理，仍可退出", async () => {
  const { confirm, log } = setup({ serviceError: true });
  assert.equal(await confirm(), true);
  assert.equal(log.stops, 0);
  assert.equal("checkboxLabel" in log.dialogs[0], false);
});

test("退出确认文案中英文键一致", () => {
  const keys = (locale) =>
    Object.keys(MESSAGES[locale])
      .filter((key) => key.startsWith("quit."))
      .sort();
  assert.deepEqual(keys("zh-CN"), keys("en-US"));
  assert.equal(keys("zh-CN").includes("quit.keepService"), true);
});
