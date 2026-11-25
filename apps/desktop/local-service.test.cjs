const test = require("node:test");
const assert = require("node:assert/strict");
const {
  createLocalService,
  sameTarget,
  centerKey,
} = require("./local-service.cjs");

const target = {
  orgId: "11111111-1111-4111-8111-111111111111",
  deviceId: "22222222-2222-4222-8222-222222222222",
  centerUrl: "https://center.example/base/",
};
function fixture() {
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
    serviceDetails: () => ({ keep_service_running: true }),
    status: async () => ({ daemon: { state: "RUNNING" } }),
    run: async (args) => {
      calls.push(args);
      return {
        stdout: JSON.stringify(
          args[1] === "logs"
            ? { lines: ["[REDACTED]"], next_cursor: null, reset: false }
            : {
                supported: true,
                installed: true,
                keep_alive: true,
                startup: args.includes("manual") ? "manual" : "login",
              },
        ),
      };
    },
  };
  return { io, calls, registration };
}

test("strict identity includes device, organization and full center base path", () => {
  const { registration } = fixture();
  assert.equal(sameTarget(registration, target), true);
  for (const patch of [
    { deviceId: "other" },
    { orgId: "other" },
    { centerUrl: "https://center.example/other" },
    { centerUrl: "" },
  ])
    assert.equal(sameTarget(registration, { ...target, ...patch }), false);
  assert.equal(
    sameTarget({ ...registration, center_url: undefined }, target),
    false,
  );
  assert.equal(centerKey("https://user:secret@center.example"), null);
  assert.equal(
    centerKey("https://CENTER.example:443/base/"),
    "https://center.example/base",
  );
});

test("only fixed scoped CLI arguments and enums cross the bridge", async () => {
  const { io, calls } = fixture();
  const service = createLocalService();
  await service("startup", { ...target, mode: "manual" }, io);
  await service("action", { ...target, action: "stop" }, io);
  assert.deepEqual(calls[0], [
    "daemon",
    "startup",
    "--organization-id",
    target.orgId,
    "--device-id",
    target.deviceId,
    "--center-url",
    target.centerUrl,
    "--mode",
    "manual",
  ]);
  assert.equal(calls[1][1], "service-action");
  for (const [operation, input] of [
    ["startup", { mode: "login; touch /tmp/x" }],
    ["action", { action: "install" }],
    ["logs", { cursor: "../../secret" }],
  ])
    await assert.rejects(service(operation, { ...target, ...input }, io));
  assert.equal(calls.length, 2);
});

test("remote, non-owner, unsupported and changed registrations never invoke CLI", async () => {
  for (const scenario of [
    "remote",
    "non-owner",
    "platform",
    "changed",
    "center",
  ]) {
    const { io, calls, registration } = fixture();
    if (scenario === "remote") registration.device_id = "other";
    if (scenario === "platform") io.platform = "linux";
    if (scenario === "non-owner")
      io.request = async (path) =>
        path.endsWith("/center")
          ? { center_url: target.centerUrl }
          : path === "/api/v1/me"
            ? { id: "stranger" }
            : { id: target.deviceId, owner_user_id: "owner" };
    if (scenario === "center")
      io.request = async () => ({ center_url: "https://other.example" });
    if (scenario === "changed") {
      let reads = 0;
      io.registration = async () =>
        ++reads === 1
          ? registration
          : { ...registration, device_id: "replacement" };
    }
    await assert.rejects(
      createLocalService()("startup", { ...target, mode: "manual" }, io),
    );
    assert.equal(calls.length, 0, scenario);
  }
});

test("navigation, CLI failure and malformed output do not report success", async () => {
  const { io } = fixture();
  io.ensureCurrent = () => {
    throw Error("navigated");
  };
  await assert.rejects(createLocalService()("logs", target, io));
  io.ensureCurrent = () => {};
  io.run = async () => {
    throw Error("failed");
  };
  await assert.rejects(
    createLocalService()("startup", { ...target, mode: "manual" }, io),
  );
  io.run = async () => ({ stdout: "{invalid" });
  await assert.rejects(createLocalService()("logs", target, io));
  io.run = async () => ({
    stdout: JSON.stringify({ lines: Array(201).fill("x") }),
  });
  await assert.rejects(createLocalService()("logs", target, io));
});

test("concurrent changes fail busy and recover after the first completes", async () => {
  const { io } = fixture();
  const service = createLocalService();
  let release;
  const gate = new Promise((resolve) => {
    release = resolve;
  });
  const run = io.run;
  io.run = async (args) => {
    await gate;
    return run(args);
  };
  const first = service("startup", { ...target, mode: "manual" }, io);
  await assert.rejects(
    service("action", { ...target, action: "stop" }, io),
    /busy/,
  );
  release();
  await first;
  await service("startup", { ...target, mode: "login" }, io);
});

test("storage migration uses the native selection and rechecks ownership after the picker", async () => {
  const { io, calls, registration } = fixture();
  const service = createLocalService();
  const input = { ...target, parent: "/renderer-value" };
  io.chooseStorage = async () => null;
  assert.equal(await service("move-storage", input, io), null);
  assert.equal(calls.length, 0);
  io.chooseStorage = async () => "/native/folder";
  await service("move-storage", input, io);
  assert.deepEqual(calls[0].slice(-2), ["--parent", "/native/folder"]);
  io.chooseStorage = async () => {
    registration.device_id = "changed";
    return "/native/other";
  };
  await assert.rejects(service("move-storage", input, io), /wrong_device/);
  assert.equal(calls.length, 1);
});
