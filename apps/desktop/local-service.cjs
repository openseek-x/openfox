// Narrow local-device IPC policy. All IO is injected so identity changes,
// command failures and remote-device rejection can be exercised without launchd.
const uuid = /^[0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12}$/;
const machineProxy = require("./machine-proxy.cjs");

function centerKey(value) {
  try {
    const url = new URL(value);
    if (
      !["http:", "https:"].includes(url.protocol) ||
      url.username ||
      url.password ||
      url.search ||
      url.hash
    )
      return null;
    return url.origin + url.pathname.replace(/\/+$/, "");
  } catch {
    return null;
  }
}

function sameTarget(registration, target) {
  const center = centerKey(target?.centerUrl);
  return Boolean(
    center &&
    registration?.registered &&
    registration.device_id === target.deviceId &&
    registration.organization_id === target.orgId &&
    centerKey(registration.center_url) === center,
  );
}

async function authorizeLocalService(input, io) {
  if (!sameTarget(await io.registration(), input)) throw Error("wrong_device");
  const [center, device, me] = await Promise.all([
    io.request(`/api/v1/organizations/${input.orgId}/devices/center`),
    io.request(
      `/api/v1/organizations/${input.orgId}/devices/${input.deviceId}`,
    ),
    io.request("/api/v1/me"),
  ]);
  io.ensureCurrent();
  if (
    centerKey(center.center_url) !== centerKey(input.centerUrl) ||
    device.id !== input.deviceId ||
    !me.id ||
    device.owner_user_id !== me.id
  )
    throw Error("forbidden");
  // Registration can change while the session requests are in flight.
  if (!sameTarget(await io.registration(), input)) throw Error("wrong_device");
  io.ensureCurrent();
}

function createLocalService() {
  const changing = new Set();
  return async function localService(operation, input, io) {
    if (io.platform !== "darwin") throw Error("unsupported");
    if (
      !input ||
      !uuid.test(input.orgId) ||
      !uuid.test(input.deviceId) ||
      !centerKey(input.centerUrl)
    )
      throw Error("wrong_device");
    if (
      ![
        "service-status",
        "keep-running",
        "open-storage",
        "move-storage",
        "timed-awake",
        "startup",
        "logs",
        "action",
        "proxy-status",
        "proxy-save",
      ].includes(operation)
    )
      throw Error("unsupported");
    if (
      operation === "timed-awake" &&
      ![0, 60, 120, 240, 480].includes(input.minutes)
    )
      throw Error("invalid_request");
    if (operation === "keep-running" && typeof input.enabled !== "boolean")
      throw Error("invalid_request");
    if (operation === "startup" && !["login", "manual"].includes(input.mode))
      throw Error("invalid_request");
    if (
      operation === "action" &&
      !["start", "stop", "restart"].includes(input.action)
    )
      throw Error("invalid_request");
    if (
      operation === "logs" &&
      input.cursor !== undefined &&
      (typeof input.cursor !== "string" ||
        input.cursor.length > 256 ||
        !/^[A-Za-z0-9_-]*$/.test(input.cursor))
    )
      throw Error("invalid_request");
    const mutation = [
      "startup",
      "keep-running",
      "move-storage",
      "timed-awake",
      "action",
      "proxy-status",
      "proxy-save",
    ].includes(operation);
    const group = operation.startsWith("proxy-") ? "proxy" : "service";
    if (changing.has(group)) throw Error("busy");
    if (mutation) changing.add(group);
    try {
      await authorizeLocalService(input, io);
      let storageParent;
      if (operation === "move-storage") {
        storageParent = await io.chooseStorage();
        io.ensureCurrent();
        if (!storageParent) return null;
        await authorizeLocalService(input, io);
      }
      const args = [
        "daemon",
        operation === "action"
          ? "service-action"
          : ["keep-running", "open-storage", "timed-awake"].includes(operation)
            ? "service-status"
            : operation,
        "--organization-id",
        input.orgId,
        "--device-id",
        input.deviceId,
        "--center-url",
        input.centerUrl,
      ];
      if (operation === "proxy-status" || operation === "proxy-save") {
        const source = io.desktopProxy();
        const body =
          operation === "proxy-save"
            ? machineProxy.saveRequest(input, source)
            : undefined;
        let view = machineProxy.readView((await io.run(args, body)).stdout);
        io.ensureCurrent();
        let decorated = machineProxy.sourceState(view, io.desktopProxy());
        if (
          operation === "proxy-status" &&
          !view.problem &&
          decorated.source_state === "outdated"
        ) {
          if (!sameTarget(await io.registration(), input))
            throw Error("wrong_device");
          io.ensureCurrent();
          const update = machineProxy.saveRequest(
            { ...view, expected_revision: view.revision },
            io.desktopProxy(),
            true,
          );
          try {
            const saveArgs = [...args];
            saveArgs[1] = "proxy-save";
            view = machineProxy.readView(
              (await io.run(saveArgs, update)).stdout,
            );
            io.ensureCurrent();
            decorated = machineProxy.sourceState(view, io.desktopProxy());
          } catch {
            io.ensureCurrent();
            // A failed follow update does not erase the last saved snapshot.
            view = machineProxy.readView((await io.run(args)).stdout);
            io.ensureCurrent();
            decorated = {
              ...machineProxy.sourceState(view, io.desktopProxy()),
              sync_error: true,
            };
          }
        }
        return decorated;
      }
      if (operation === "action") {
        args.push("--action", input.action);
        await io.run(args);
        io.ensureCurrent();
        return io.status();
      }
      if (operation === "move-storage") args.push("--parent", storageParent);
      if (operation === "startup") args.push("--mode", input.mode);
      if (operation === "logs" && input.cursor)
        args.push("--cursor", input.cursor);
      const result = JSON.parse((await io.run(args)).stdout);
      io.ensureCurrent();
      if (operation === "logs") {
        if (
          !Array.isArray(result.lines) ||
          result.lines.length > 200 ||
          result.lines.some(
            (line) => typeof line !== "string" || line.length > 256,
          ) ||
          (result.next_cursor !== null &&
            (typeof result.next_cursor !== "string" ||
              result.next_cursor.length > 256)) ||
          typeof result.reset !== "boolean"
        )
          throw Error("unavailable");
      } else if (
        result.supported !== true ||
        typeof result.installed !== "boolean" ||
        typeof result.keep_alive !== "boolean" ||
        (result.installed && !["login", "manual"].includes(result.startup))
      )
        throw Error("unavailable");
      if (operation === "logs") return result;
      if (["open-storage", "keep-running", "timed-awake"].includes(operation)) {
        if (!result.installed) throw Error("unavailable");
        if (operation === "open-storage")
          return io.openStorage(result.storage_path);
        if (operation === "timed-awake") io.setAwakeMinutes(input.minutes);
        else await io.setKeepRunning(input.enabled);
        io.ensureCurrent();
      }
      return { ...result, ...io.serviceDetails() };
    } finally {
      if (mutation) changing.delete(group);
    }
  };
}

module.exports = { centerKey, sameTarget, createLocalService };
