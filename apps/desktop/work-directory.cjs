const {
  generateKeyPair,
  privateDecrypt,
  createDecipheriv,
  constants,
} = require("node:crypto");
const { promisify } = require("node:util");
const { setTimeout: delay } = require("node:timers/promises");
const path = require("node:path");

const uuid = /^[0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12}$/;
const fields = [
  "organization_id",
  "device_id",
  "space_id",
  "conversation_id",
  "agent_id",
  "run_id",
  "user_id",
];
const keyPair = promisify(generateKeyPair);
const failure = (reason) => Object.assign(new Error(reason), { reason });

function validScope(scope) {
  return (
    scope &&
    fields.every(
      (key) =>
        typeof scope[key] === "string" &&
        uuid.test(scope[key]) &&
        scope[key] !== "00000000-0000-0000-0000-000000000000",
    ) &&
    Number.isSafeInteger(scope.generation) &&
    scope.generation > 0
  );
}
function sameScope(a, b) {
  return (
    validScope(a) &&
    validScope(b) &&
    [...fields, "generation"].every((key) => a[key] === b[key])
  );
}
function binding(request) {
  return Buffer.from(
    [
      "openfox-workspace-path-v1",
      request.organization_id,
      request.device_id,
      request.space_id,
      request.conversation_id,
      request.agent_id,
      request.run_id,
      request.generation,
      request.user_id,
      request.request_id,
      request.expires_at,
    ].join("\n"),
  );
}
function validPath(value) {
  return (
    typeof value === "string" &&
    Buffer.byteLength(value) <= 4096 &&
    !/[\x00-\x1f\x7f-\x9f]/.test(value) &&
    (path.posix.isAbsolute(value) || path.win32.isAbsolute(value))
  );
}
function decode(value, min, max) {
  if (typeof value !== "string" || value.length > Math.ceil(max / 3) * 4)
    throw failure("unavailable");
  const data = Buffer.from(value, "base64");
  if (
    data.toString("base64") !== value ||
    data.length < min ||
    data.length > max
  )
    throw failure("unavailable");
  return data;
}
function decrypt(envelope, request, privateKey) {
  if (envelope?.algorithm !== "RSA-OAEP-256/A256GCM")
    throw failure("unavailable");
  const associated = binding(request);
  const secret = privateDecrypt(
    {
      key: privateKey,
      padding: constants.RSA_PKCS1_OAEP_PADDING,
      oaepHash: "sha256",
      oaepLabel: associated,
    },
    decode(envelope.wrapped_key, 256, 256),
  );
  let plain;
  try {
    if (secret.length !== 32) throw failure("unavailable");
    const cipher = createDecipheriv(
      "aes-256-gcm",
      secret,
      decode(envelope.nonce, 12, 12),
      { authTagLength: 16 },
    );
    cipher.setAAD(associated);
    const sealed = decode(envelope.ciphertext, 17, 4112);
    cipher.setAuthTag(sealed.subarray(-16));
    plain = Buffer.concat([
      cipher.update(sealed.subarray(0, -16)),
      cipher.final(),
    ]);
    const value = new TextDecoder("utf-8", { fatal: true }).decode(plain);
    if (!validPath(value)) throw failure("unavailable");
    return value;
  } finally {
    secret.fill(0);
    plain?.fill(0);
  }
}

function createWorkDirectoryCopier() {
  let active;
  return {
    cancel(requestId) {
      if (active?.id === requestId)
        active.controller.abort(failure("cancelled"));
    },
    async copy(input, io) {
      if (
        !input ||
        ![input.orgId, input.conversationId, input.requestId].every(
          (id) => typeof id === "string" && uuid.test(id),
        )
      )
        return { ok: false, reason: "unavailable" };
      active?.controller.abort(failure("cancelled"));
      const controller = new AbortController();
      const operation = { id: input.requestId, controller };
      active = operation;
      const signal = controller.signal;
      const deadline = Date.now() + 30000;
      const timer = setTimeout(
        () => controller.abort(failure("timeout")),
        30000,
      );
      const removeListeners = io.onInvalidate(() =>
        controller.abort(failure("cancelled")),
      );
      let privateKey;
      let value;
      const ensureCurrent = () => {
        signal.throwIfAborted();
        if (active !== operation || !io.isCurrent()) throw failure("cancelled");
        if (Date.now() >= deadline) throw failure("timeout");
      };
      try {
        const endpoint = `/api/v1/organizations/${input.orgId}/conversations/${input.conversationId}/work-directory`;
        const target = await io.request(endpoint, { signal });
        ensureCurrent();
        if (!target.target) throw failure("workspace_unavailable");
        const scope = target.target;
        if (
          !validScope(scope) ||
          scope.organization_id !== input.orgId ||
          scope.conversation_id !== input.conversationId
        )
          throw failure("unavailable");
        const local = await io.local(scope, signal);
        ensureCurrent();
        if (local?.ok === true && validPath(local.path)) {
          value = local.path;
        } else {
          if (!target.supported) throw failure("unsupported");
          if (!target.online) throw failure("offline");
          const keys = await keyPair("rsa", {
            modulusLength: 2048,
            publicExponent: 65537,
          });
          privateKey = keys.privateKey;
          ensureCurrent();
          const request = {
            ...scope,
            request_id: input.requestId,
            expires_at: deadline,
            public_key: keys.publicKey
              .export({ type: "spki", format: "der" })
              .toString("base64"),
          };
          const base = `/api/v1/organizations/${input.orgId}/devices/${scope.device_id}/commands`;
          let response = await io.request(base, {
            method: "POST",
            body: { kind: "workspace_path", params: request },
            signal,
          });
          const id = response.command?.id;
          if (typeof id !== "string" || !uuid.test(id))
            throw failure("unavailable");
          for (;;) {
            ensureCurrent();
            const command = response.command;
            if (
              !command ||
              command.id !== id ||
              command.device_id !== scope.device_id ||
              command.kind !== "workspace_path" ||
              !sameScope(command.params, scope) ||
              command.params.request_id !== request.request_id ||
              command.params.public_key !== request.public_key ||
              command.params.expires_at !== request.expires_at
            )
              throw failure("unavailable");
            if (command.status === "SUCCEEDED") {
              value = decrypt(command.result, request, privateKey);
              break;
            }
            if (command.status === "EXPIRED") throw failure("timeout");
            if (command.status === "FAILED")
              throw failure(
                command.error_code === "WORKSPACE_UNAVAILABLE"
                  ? "workspace_unavailable"
                  : "unavailable",
              );
            if (!["PENDING", "DELIVERED"].includes(command.status))
              throw failure("unavailable");
            await delay(350, undefined, { signal });
            response = await io.request(`${base}/${id}`, { signal });
          }
        }
        const current = await io.request(endpoint, { signal });
        ensureCurrent();
        if (!sameScope(current.target, scope)) throw failure("changed");
        try {
          io.writeText(value);
        } catch {
          throw failure("clipboard_failed");
        }
        return { ok: true };
      } catch (error) {
        const reason = signal.aborted ? signal.reason?.reason : error.reason;
        return {
          ok: false,
          reason: [
            "cancelled",
            "timeout",
            "offline",
            "unsupported",
            "workspace_unavailable",
            "changed",
            "clipboard_failed",
            "forbidden",
            "busy",
          ].includes(reason)
            ? reason
            : "unavailable",
        };
      } finally {
        clearTimeout(timer);
        removeListeners();
        privateKey = undefined;
        value = undefined;
        if (active === operation) active = undefined;
      }
    },
  };
}

async function requestJSON(
  browserSession,
  origin,
  pathname,
  { signal, method = "GET", body } = {},
) {
  const headers = { Accept: "application/json" };
  const url = new URL(pathname, origin).href;
  if (method !== "GET") {
    const cookies = await browserSession.cookies.get({
      url,
      name: "openfox_csrf",
    });
    if (!cookies[0]?.value) throw failure("forbidden");
    headers["X-CSRF-Token"] = cookies[0].value;
    headers["Content-Type"] = "application/json";
  }
  const response = await browserSession.fetch(url, {
    method,
    credentials: "include",
    redirect: "error",
    cache: "no-store",
    headers,
    signal: AbortSignal.any([signal, AbortSignal.timeout(5000)]),
    ...(body ? { body: JSON.stringify(body) } : {}),
  });
  const reader = response.body.getReader();
  const chunks = [];
  let size = 0;
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      size += value.length;
      if (size > 16384) throw failure("unavailable");
      chunks.push(value);
    }
    const raw = Buffer.concat(chunks).toString("utf8");
    if (!response.ok) {
      let code;
      try {
        code = JSON.parse(raw)?.error?.code;
      } catch {}
      throw failure(
        [401, 403, 404].includes(response.status)
          ? "forbidden"
          : response.status === 409
            ? code === "COMMAND_EXPIRED"
              ? "timeout"
              : "busy"
            : "unavailable",
      );
    }
    return JSON.parse(raw);
  } finally {
    await reader.cancel();
    reader.releaseLock();
  }
}

module.exports = {
  createWorkDirectoryCopier,
  requestJSON,
  decrypt,
  binding,
  validPath,
};
