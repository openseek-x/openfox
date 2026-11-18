const test = require("node:test");
const assert = require("node:assert/strict");
const {
  desktopSnapshot,
  readView,
  saveRequest,
} = require("./machine-proxy.cjs");
const { createLocalService } = require("./local-service.cjs");

const target = {
  orgId: "11111111-1111-4111-8111-111111111111",
  deviceId: "22222222-2222-4222-8222-222222222222",
  centerUrl: "https://center.example",
};
const scope = "a".repeat(64);
function fixture() {
  let source = { mode: "direct" };
  let view = { mode: "DISABLED", revision: 0, scope_token: scope };
  const calls = [];
  const registration = {
    registered: true,
    organization_id: target.orgId,
    device_id: target.deviceId,
    center_url: target.centerUrl,
  };
  const io = {
    platform: "darwin",
    registration: async () => registration,
    request: async (path) =>
      path.endsWith("/center")
        ? { center_url: target.centerUrl }
        : path === "/api/v1/me"
          ? { id: "owner" }
          : { id: target.deviceId, owner_user_id: "owner" },
    ensureCurrent() {},
    desktopProxy: () => source,
    run: async (args, input) => {
      calls.push({ args, input });
      if (args[1] === "proxy-save") {
        const v = JSON.parse(input);
        if (
          v.expected_revision !== view.revision ||
          (v.follow_update && view.mode !== "FOLLOW_DESKTOP")
        )
          throw Error("machine_proxy_conflict");
        view = {
          mode: v.mode,
          effective: v.effective,
          source_fingerprint: v.source_fingerprint,
          scope_token: scope,
          revision: view.revision + 1,
        };
      }
      return { stdout: JSON.stringify(view) };
    },
  };
  return {
    io,
    calls,
    registration,
    source: (v) => (source = v),
    view: (v) => (view = v),
    read: () => view,
  };
}

test("follow converts only the approved desktop subset without reading credentials", () => {
  assert.deepEqual(desktopSnapshot({ mode: "direct" }).effective, {
    mode: "direct",
  });
  const manual = {
    mode: "manual",
    protocol: "https",
    host: "proxy.example",
    port: 443,
  };
  assert.equal(desktopSnapshot(manual).effective.protocol, "https");
  for (const source of [
    { mode: "system" },
    { ...manual, protocol: "socks5" },
    { ...manual, authenticated: true, password: "must-not-leak" },
    { ...manual, bypass: "localhost" },
  ])
    assert.throws(() => desktopSnapshot(source));
});

test("owner follows the main-process source, not a renderer supplied endpoint", async () => {
  const f = fixture();
  const result = await createLocalService()(
    "proxy-save",
    {
      ...target,
      mode: "FOLLOW_DESKTOP",
      expected_revision: 0,
      scope_token: scope,
      effective: {
        mode: "proxy",
        protocol: "http",
        host: "evil.example",
        port: 80,
      },
      source_fingerprint: "f".repeat(64),
    },
    f.io,
  );
  assert.equal(result.source_state, "synced");
  assert.deepEqual(f.read().effective, { mode: "direct" });
  assert.equal(f.calls[0].args.includes("evil.example"), false);
  assert.equal(f.calls[0].input.includes("evil.example"), false);
});

test("automatic follow requires current owner and updates only last confirmed FOLLOW", async () => {
  const f = fixture(),
    service = createLocalService();
  await service(
    "proxy-save",
    {
      ...target,
      mode: "FOLLOW_DESKTOP",
      expected_revision: 0,
      scope_token: scope,
    },
    f.io,
  );
  f.source({ mode: "manual", protocol: "http", host: "127.0.0.1", port: 7890 });
  const followed = await service("proxy-status", target, f.io);
  assert.equal(followed.revision, 2);
  assert.equal(followed.source_state, "synced");
  assert.equal(JSON.parse(f.calls.at(-1).input).follow_update, true);
  const saves = f.calls.filter((v) => v.input).length;
  f.io.request = async () => ({ id: "not-owner" });
  await assert.rejects(service("proxy-status", target, f.io));
  assert.equal(f.calls.filter((v) => v.input).length, saves);
});

test("unsupported sources keep the saved follow snapshot; custom never follows", async () => {
  const f = fixture(),
    service = createLocalService();
  await service(
    "proxy-save",
    {
      ...target,
      mode: "FOLLOW_DESKTOP",
      expected_revision: 0,
      scope_token: scope,
    },
    f.io,
  );
  f.source({ mode: "system" });
  const result = await service("proxy-status", target, f.io);
  assert.equal(result.source_state, "unsupported");
  assert.equal(result.revision, 1);
  await service(
    "proxy-save",
    {
      ...target,
      mode: "CUSTOM",
      expected_revision: 1,
      scope_token: scope,
      effective: {
        mode: "proxy",
        protocol: "http",
        host: "localhost",
        port: 7890,
      },
    },
    f.io,
  );
  f.source({ mode: "direct" });
  assert.equal((await service("proxy-status", target, f.io)).mode, "CUSTOM");
  assert.equal(f.read().revision, 2);
});

test("registration or session changes before a follow update cannot mutate", async () => {
  for (const scenario of ["registration", "navigation"]) {
    const f = fixture(),
      service = createLocalService();
    await service(
      "proxy-save",
      {
        ...target,
        mode: "FOLLOW_DESKTOP",
        expected_revision: 0,
        scope_token: scope,
      },
      f.io,
    );
    f.source({
      mode: "manual",
      protocol: "http",
      host: "localhost",
      port: 7890,
    });
    const run = f.io.run;
    f.io.run = async (args, input) => {
      const result = await run(args, input);
      if (scenario === "registration") f.registration.device_id = "changed";
      else
        f.io.ensureCurrent = () => {
          throw Error("navigated");
        };
      return result;
    };
    await assert.rejects(service("proxy-status", target, f.io));
    assert.equal(f.read().revision, 1);
  }
});

test("a concurrent CUSTOM change wins over a stale follow read", async () => {
  const f = fixture(),
    service = createLocalService();
  await service(
    "proxy-save",
    {
      ...target,
      mode: "FOLLOW_DESKTOP",
      expected_revision: 0,
      scope_token: scope,
    },
    f.io,
  );
  f.source({ mode: "manual", protocol: "http", host: "localhost", port: 7890 });
  const run = f.io.run;
  f.io.run = async (args, input) => {
    if (input && JSON.parse(input).follow_update)
      f.view({
        mode: "CUSTOM",
        revision: 2,
        scope_token: scope,
        effective: {
          mode: "proxy",
          protocol: "https",
          host: "new.example",
          port: 443,
        },
      });
    return run(args, input);
  };
  const result = await service("proxy-status", target, f.io);
  assert.equal(result.mode, "CUSTOM");
  assert.equal(result.effective.host, "new.example");
  assert.equal(result.sync_error, true);
});

test("IPC JSON is bounded and strips unexpected output; credentials are rejected", () => {
  assert.throws(() => readView(" ".repeat(16385)));
  assert.throws(() =>
    readView(
      JSON.stringify({ revision: -1, scope_token: scope, mode: "DISABLED" }),
    ),
  );
  const result = readView(
    JSON.stringify({
      revision: 0,
      scope_token: scope,
      mode: "DISABLED",
      secret: "not-returned",
    }),
  );
  assert.equal(result.secret, undefined);
  assert.throws(() =>
    saveRequest({
      mode: "CUSTOM",
      scope_token: scope,
      expected_revision: 0,
      effective: {
        mode: "proxy",
        protocol: "http",
        host: "proxy.example",
        port: 80,
        password: "no",
      },
    }),
  );
});
