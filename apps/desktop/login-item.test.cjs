const { test } = require("node:test");
const assert = require("node:assert/strict");
const { createLoginItem } = require("./login-item.cjs");

function fakeApp(initial = { openAtLogin: false, status: "not-registered" }) {
  const state = { ...initial, writes: [] };
  return {
    state,
    getLoginItemSettings: () => ({
      openAtLogin: state.openAtLogin,
      status: state.status,
    }),
    setLoginItemSettings(options) {
      state.writes.push(options);
      if (state.reject) return;
      if (state.throws) throw new Error("denied");
      state.openAtLogin = options.openAtLogin;
    },
  };
}

test("未打包的开发运行不注册登录项", () => {
  const app = fakeApp();
  const item = createLoginItem({ app, platform: "darwin", packaged: false });
  assert.deepEqual(item.get(), {
    supported: false,
    enabled: false,
    reason: "not_packaged",
  });
  const result = item.set(true);
  assert.equal(result.ok, false);
  assert.equal(result.reason, "not_packaged");
  assert.deepEqual(app.state.writes, []);
});

test("非 macOS 隐藏入口且不调用系统接口", () => {
  const app = fakeApp();
  const item = createLoginItem({ app, platform: "linux", packaged: true });
  assert.equal(item.get().supported, false);
  assert.equal(item.set(true).ok, false);
  assert.deepEqual(app.state.writes, []);
});

test("打包后开关以系统实际值为准", () => {
  const app = fakeApp();
  const item = createLoginItem({ app, platform: "darwin", packaged: true });
  assert.deepEqual(item.get(), { supported: true, enabled: false });
  const on = item.set(true);
  assert.equal(on.ok, true);
  assert.equal(on.state.enabled, true);
  assert.deepEqual(app.state.writes, [{ openAtLogin: true }]);
  assert.equal(item.set(false).state.enabled, false);
});

test("系统拒绝：写入后读回值不符，开关回弹到实际值", () => {
  const app = fakeApp();
  app.state.reject = true;
  const item = createLoginItem({ app, platform: "darwin", packaged: true });
  const result = item.set(true);
  assert.equal(result.ok, false);
  assert.equal(result.state.enabled, false);
});

test("系统接口抛错：返回失败并给出实际状态", () => {
  const app = fakeApp();
  app.state.throws = true;
  const item = createLoginItem({ app, platform: "darwin", packaged: true });
  const result = item.set(true);
  assert.deepEqual(
    { ok: result.ok, reason: result.reason, enabled: result.state.enabled },
    { ok: false, reason: "rejected", enabled: false },
  );
});

test("需要用户在系统设置里批准时显示为未开启并说明原因", () => {
  const app = fakeApp({ openAtLogin: true, status: "requires-approval" });
  const item = createLoginItem({ app, platform: "darwin", packaged: true });
  assert.deepEqual(item.get(), {
    supported: true,
    enabled: false,
    reason: "requires_approval",
  });
});

test("参数必须是布尔值", () => {
  const app = fakeApp();
  const item = createLoginItem({ app, platform: "darwin", packaged: true });
  assert.equal(item.set("yes").ok, false);
  assert.equal(item.set(1).ok, false);
  assert.deepEqual(app.state.writes, []);
});
