const net = require("node:net");
const fs = require("node:fs/promises");
const path = require("node:path");
const { failure } = require("./policy.cjs");

const MAX_FRAME = 6 * 1024 * 1024;
const codes = new Set([
  "INVALID_REQUEST",
  "TARGET_CLOSED",
  "STALE_TARGET",
  "TARGET_NOT_VISIBLE",
  "INVALID_TARGET",
  "CONTROL_REVOKED",
  "ORIGIN_NOT_ALLOWED",
  "FORBIDDEN",
  "UNAVAILABLE",
  "TIMEOUT",
  "OUTCOME_UNKNOWN",
  "LIMIT_EXCEEDED",
  "CAPTURE_FAILED",
  "NETWORK_DENIED",
]);

// The host chooses this path from the trusted local daemon status, never IPC
// parameters. Claim secret is transmitted once and is not retained or logged.
async function connectBrowserBroker({
  socketPath,
  claim,
  execute,
  disconnected,
  revoked,
}) {
  if (!path.isAbsolute(socketPath)) throw failure("UNAVAILABLE");
  const [directory, file] = await Promise.all([
    fs.lstat(path.dirname(socketPath)),
    fs.lstat(socketPath),
  ]);
  const uid = process.getuid?.();
  if (
    !directory.isDirectory() ||
    !file.isSocket() ||
    directory.mode & 0o077 ||
    file.mode & 0o077 ||
    (uid !== undefined && (directory.uid !== uid || file.uid !== uid))
  )
    throw failure("FORBIDDEN");
  const socket = net.createConnection(socketPath);
  const pending = new Map(),
    running = new Map();
  let buffer = Buffer.alloc(0),
    expected = null,
    closed = false,
    attached = false,
    sequence = 0;
  let lastMessage = Date.now(),
    timer;
  let resolveReady, rejectReady;
  const ready = new Promise((resolve, reject) => {
    resolveReady = resolve;
    rejectReady = reject;
  });
  const close = () => {
    if (closed) return;
    closed = true;
    clearInterval(timer);
    clearTimeout(connectTimeout);
    socket.destroy();
    buffer = Buffer.alloc(0);
    for (const controller of running.values()) controller.abort();
    running.clear();
    for (const waiter of pending.values()) {
      clearTimeout(waiter.timer);
      waiter.reject(failure("UNAVAILABLE"));
    }
    pending.clear();
    rejectReady(failure("UNAVAILABLE"));
    disconnected();
  };
  const connectTimeout = setTimeout(close, 5000);
  const send = (value) => {
    if (closed) throw failure("UNAVAILABLE");
    const payload = Buffer.from(JSON.stringify({ version: 1, ...value }));
    if (
      payload.length > MAX_FRAME ||
      socket.writableLength + payload.length > MAX_FRAME * 2
    )
      throw failure("LIMIT_EXCEEDED");
    const header = Buffer.alloc(4);
    header.writeUInt32BE(payload.length);
    socket.write(Buffer.concat([header, payload]));
  };
  const respond = async (message) => {
    const request = message.request;
    if (
      !request ||
      typeof message.id !== "string" ||
      message.id.length > 128 ||
      running.size >= 1 ||
      running.has(message.id)
    )
      throw failure("INVALID_REQUEST");
    const deadline = Date.parse(request.lease_expires_at);
    if (!Number.isFinite(deadline) || deadline <= Date.now())
      throw failure("CONTROL_REVOKED");
    const controller = new AbortController();
    running.set(message.id, controller);
    const expiry = setTimeout(
      () => controller.abort(),
      Math.min(60000, deadline - Date.now()),
    );
    let result;
    try {
      result = await execute(request, controller.signal);
      if (controller.signal.aborted)
        result = { text: "OUTCOME_UNKNOWN", ok: false };
    } catch (error) {
      result = {
        text: codes.has(error.code) ? error.code : "UNAVAILABLE",
        ok: false,
      };
    } finally {
      clearTimeout(expiry);
      running.delete(message.id);
    }
    if (!closed) send({ type: "result", id: message.id, result });
  };
  const receive = (message) => {
    if (!message || message.version !== 1 || typeof message.type !== "string")
      throw failure("INVALID_REQUEST");
    lastMessage = Date.now();
    if (!attached) {
      if (
        message.type !== "attached" ||
        !message.binding ||
        message.binding.id !== claim.binding_id ||
        message.binding.organization_id !== claim.organization_id ||
        message.binding.host_instance_id !== claim.host_instance_id ||
        message.binding.host_epoch !== claim.host_epoch ||
        message.binding.revision !== claim.revision
      )
        throw failure("FORBIDDEN");
      attached = true;
      clearTimeout(connectTimeout);
      resolveReady(message.binding);
      return;
    }
    if (message.type === "execute") {
      void respond(message).catch(close);
      return;
    }
    if (message.type === "cancel") {
      running.get(message.id)?.abort();
      return;
    }
    if (message.type === "revoked") {
      if (
        !Number.isSafeInteger(message.grant?.epoch) ||
        message.grant.epoch < 1
      )
        throw failure("INVALID_REQUEST");
      for (const controller of running.values()) controller.abort();
      revoked(message.grant.epoch);
      return;
    }
    if (message.type === "pong") return;
    if (message.type === "grant-result") {
      const waiter = pending.get(message.id);
      if (!waiter) throw failure("INVALID_REQUEST");
      pending.delete(message.id);
      clearTimeout(waiter.timer);
      if (message.error) waiter.reject(failure("CONTROL_REVOKED"));
      else waiter.resolve();
      return;
    }
    throw failure("INVALID_REQUEST");
  };
  socket.on("error", close);
  socket.on("close", close);
  socket.on("connect", () => {
    try {
      send({ type: "attach", attach: claim });
    } catch {
      close();
    }
  });
  socket.on("data", (chunk) => {
    try {
      buffer = Buffer.concat([buffer, chunk]);
      if (buffer.length > MAX_FRAME + 4) throw failure("LIMIT_EXCEEDED");
      while (buffer.length >= (expected === null ? 4 : expected)) {
        if (expected === null) {
          expected = buffer.readUInt32BE(0);
          buffer = buffer.subarray(4);
          if (expected < 1 || expected > MAX_FRAME)
            throw failure("LIMIT_EXCEEDED");
        } else {
          const payload = buffer.subarray(0, expected);
          buffer = buffer.subarray(expected);
          expected = null;
          receive(JSON.parse(payload.toString("utf8")));
        }
      }
    } catch {
      close();
    }
  });
  timer = setInterval(() => {
    if (Date.now() - lastMessage > 30000) {
      close();
      return;
    }
    if (attached) {
      try {
        send({ type: "ping", id: String(++sequence) });
      } catch {
        close();
      }
    }
  }, 10000);
  const binding = await ready;
  // Erase the one-time claim from the retained closure after attach.
  claim = { ...claim, claim_secret: undefined };
  return {
    binding,
    close,
    async grant(grant) {
      if (closed || pending.size >= 4) throw failure("UNAVAILABLE");
      for (const controller of running.values()) controller.abort();
      const id = String(++sequence);
      await new Promise((resolve, reject) => {
        pending.set(id, { resolve, reject, timer: setTimeout(close, 5000) });
        try {
          send({ type: "grant", id, grant });
        } catch {
          close();
        }
      });
    },
  };
}

module.exports = { connectBrowserBroker };
