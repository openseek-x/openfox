const { test } = require("node:test");
const assert = require("node:assert/strict");
const { decideRestart } = require("./restart-policy.cjs");

const running = (active_runs) => ({ state: "RUNNING", active_runs });

test("CLI 没变且没有待办：不重启", () => {
  for (const cliState of ["unchanged", "foreign", "missing", "failed"]) {
    assert.equal(
      decideRestart({ cliState, daemon: running(0), pending: false }),
      "none",
    );
  }
});

test("CLI 被覆盖且服务空闲：现在重启", () => {
  assert.equal(
    decideRestart({
      cliState: "installed",
      daemon: running(0),
      pending: false,
    }),
    "restart",
  );
});

test("有运行中的 Run：推迟到空闲，空闲后（pending）再重启", () => {
  assert.equal(
    decideRestart({
      cliState: "installed",
      daemon: running(2),
      pending: false,
    }),
    "defer",
  );
  assert.equal(
    decideRestart({ cliState: "unchanged", daemon: running(1), pending: true }),
    "defer",
  );
  assert.equal(
    decideRestart({ cliState: "unchanged", daemon: running(0), pending: true }),
    "restart",
  );
});

test("拿不到可靠状态：不重启，提示手动", () => {
  assert.equal(
    decideRestart({ cliState: "installed", daemon: null, pending: false }),
    "manual",
  );
  assert.equal(
    decideRestart({
      cliState: "installed",
      daemon: { state: "UNAVAILABLE" },
      pending: false,
    }),
    "manual",
  );
  // daemon 没上报 active_runs 不能当 0
  for (const active_runs of [undefined, null, -1, 1.5, "0"]) {
    assert.equal(
      decideRestart({
        cliState: "installed",
        daemon: { state: "RUNNING", active_runs },
        pending: false,
      }),
      "manual",
    );
  }
});

test("服务没在运行：下次启动自然使用新文件；过渡状态等待", () => {
  assert.equal(
    decideRestart({
      cliState: "installed",
      daemon: { state: "STOPPED", active_runs: 0 },
      pending: true,
    }),
    "none",
  );
  for (const state of ["STARTING", "STOPPING", "RECONCILE_REQUIRED"]) {
    assert.equal(
      decideRestart({
        cliState: "installed",
        daemon: { state, active_runs: 0 },
        pending: false,
      }),
      "defer",
    );
  }
});
