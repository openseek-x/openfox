const { test } = require("node:test");
const assert = require("node:assert/strict");
const { buildTrayMenu, serviceStatus } = require("./tray.cjs");
const { message, MESSAGES, pickLocale } = require("./messages.cjs");
const { trayIconPngs } = require("./tray-icon.cjs");

test("本地服务状态映射：未安装优先，其次按 daemon 状态", () => {
  assert.equal(
    serviceStatus({ installed: false, daemonState: "RUNNING" }),
    "not_installed",
  );
  assert.equal(
    serviceStatus({ installed: true, daemonState: "RUNNING" }),
    "running",
  );
  assert.equal(
    serviceStatus({ installed: true, daemonState: "STARTING" }),
    "running",
  );
  assert.equal(
    serviceStatus({ installed: true, daemonState: "STOPPED" }),
    "stopped",
  );
  assert.equal(
    serviceStatus({ installed: true, daemonState: "UNAVAILABLE" }),
    "unknown",
  );
  assert.equal(serviceStatus({ installed: true, daemonState: "?" }), "unknown");
});

test("托盘菜单：打开窗口、只读服务状态、检查更新、退出，点击回调正确", () => {
  const calls = [];
  const handlers = {
    open: () => calls.push("open"),
    checkUpdate: () => calls.push("check"),
    quit: () => calls.push("quit"),
  };
  const menu = buildTrayMenu({
    locale: "zh-CN",
    service: "running",
    updateVersion: null,
    handlers,
  });
  const items = menu.filter((item) => item.type !== "separator");
  assert.deepEqual(
    items.map((item) => item.label),
    ["打开 OpenFox", "本地服务：运行中", "检查更新…", "退出 OpenFox"],
  );
  const status = items[1];
  assert.equal(status.enabled, false);
  assert.equal(status.click, undefined);
  items[0].click();
  items[2].click();
  items[3].click();
  assert.deepEqual(calls, ["open", "check", "quit"]);
});

test("托盘菜单：服务各状态与新版本提示（英文）", () => {
  const handlers = { open() {}, checkUpdate() {}, quit() {} };
  const label = (service, updateVersion = null) =>
    buildTrayMenu({ locale: "en-US", service, updateVersion, handlers })
      .filter((item) => item.type !== "separator")
      .map((item) => item.label);
  assert.equal(label("stopped")[1], "Local service: stopped");
  assert.equal(label("not_installed")[1], "Local service: not installed");
  assert.equal(label("unknown")[1], "Local service: unknown");
  assert.equal(label("running", "0.2.0")[2], "New version 0.2.0 available…");
});

test("文案：中英文键一致，占位符替换，缺失键原样返回", () => {
  assert.deepEqual(
    Object.keys(MESSAGES["zh-CN"]).sort(),
    Object.keys(MESSAGES["en-US"]).sort(),
  );
  assert.equal(
    message("zh-CN", "quit.detail.runs", { count: 3 }).includes("3"),
    true,
  );
  assert.equal(message("en-US", "nope"), "nope");
  assert.equal(pickLocale("zh-Hans-CN"), "zh-CN");
  assert.equal(pickLocale("fr"), "en-US");
  assert.equal(pickLocale(undefined), "en-US");
});

test("占位托盘图标是合法 PNG（16px 与 32px，含透明度）", () => {
  const { x1, x2 } = trayIconPngs();
  for (const [png, size] of [
    [x1, 16],
    [x2, 32],
  ]) {
    assert.deepEqual(
      [...png.subarray(0, 8)],
      [137, 80, 78, 71, 13, 10, 26, 10],
    );
    assert.equal(png.readUInt32BE(16), size);
    assert.equal(png.readUInt32BE(20), size);
    assert.equal(png[25], 6);
  }
});
