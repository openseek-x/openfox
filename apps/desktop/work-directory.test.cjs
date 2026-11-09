const test = require("node:test");
const assert = require("node:assert/strict");
const {
  randomBytes,
  createCipheriv,
  publicEncrypt,
  createPublicKey,
  constants,
} = require("node:crypto");
const {
  createWorkDirectoryCopier,
  requestJSON,
  binding,
  validPath,
} = require("./work-directory.cjs");

const id = "11111111-1111-4111-8111-111111111111";
const second = "22222222-2222-4222-8222-222222222222";
const scope = {
  organization_id: id,
  device_id: second,
  space_id: id,
  conversation_id: id,
  agent_id: id,
  run_id: id,
  generation: 1,
  user_id: id,
};
const input = { orgId: id, conversationId: id, requestId: id };
const target = { target: scope, online: true, supported: true };

function seal(request, value) {
  const secret = randomBytes(32);
  const nonce = randomBytes(12);
  const associated = binding(request);
  const cipher = createCipheriv("aes-256-gcm", secret, nonce);
  cipher.setAAD(associated);
  const ciphertext = Buffer.concat([
    cipher.update(value, "utf8"),
    cipher.final(),
    cipher.getAuthTag(),
  ]);
  const wrapped = publicEncrypt(
    {
      key: createPublicKey({
        key: Buffer.from(request.public_key, "base64"),
        type: "spki",
        format: "der",
      }),
      padding: constants.RSA_PKCS1_OAEP_PADDING,
      oaepHash: "sha256",
      oaepLabel: associated,
    },
    secret,
  );
  return {
    algorithm: "RSA-OAEP-256/A256GCM",
    wrapped_key: wrapped.toString("base64"),
    nonce: nonce.toString("base64"),
    ciphertext: ciphertext.toString("base64"),
  };
}
function fixture(overrides = {}) {
  const writes = [];
  const requests = [];
  let invalidate;
  let removed = false;
  const io = {
    isCurrent: () => true,
    onInvalidate: (callback) => {
      invalidate = callback;
      return () => {
        removed = true;
      };
    },
    local: async () => null,
    writeText: (value) => writes.push(value),
    request: async (path, options) => {
      requests.push({ path, options });
      if (path.endsWith("work-directory")) return target;
      const params = options.body.params;
      return {
        command: {
          id,
          kind: "workspace_path",
          device_id: scope.device_id,
          params,
          status: "SUCCEEDED",
          result: seal(params, "/remote/中文/worktree"),
        },
      };
    },
    ...overrides,
  };
  return {
    io,
    writes,
    requests,
    invalidate: () => invalidate(),
    removed: () => removed,
  };
}

test("local directory stays out of renderer results and command channel", async () => {
  const f = fixture({
    local: async () => ({ ok: true, path: "/local/actual/worktree" }),
  });
  const result = await createWorkDirectoryCopier().copy(input, f.io);
  assert.deepEqual(result, { ok: true });
  assert.deepEqual(f.writes, ["/local/actual/worktree"]);
  assert.equal(f.requests.length, 2);
  assert.equal(f.removed(), true);
});
test("remote ciphertext decrypts only at the requesting desktop", async () => {
  const f = fixture();
  const result = await createWorkDirectoryCopier().copy(input, f.io);
  assert.deepEqual(result, { ok: true });
  assert.deepEqual(f.writes, ["/remote/中文/worktree"]);
  assert.equal(JSON.stringify(f.requests).includes("/remote/"), false);
  assert.equal(JSON.stringify(result).includes("path"), false);
});
test("missing, offline, and old remote machines do not submit commands", async () => {
  for (const [response, reason] of [
    [{ target: null }, "workspace_unavailable"],
    [{ ...target, online: false }, "offline"],
    [{ ...target, supported: false }, "unsupported"],
  ]) {
    const f = fixture({ request: async () => response });
    assert.deepEqual(await createWorkDirectoryCopier().copy(input, f.io), {
      ok: false,
      reason,
    });
    assert.deepEqual(f.writes, []);
  }
});
test("switching conversation, account or execution generation never overwrites clipboard", async () => {
  for (const mode of ["navigation", "generation", "account", "permission"]) {
    let calls = 0;
    const f = fixture({
      local: async () => {
        if (mode === "navigation") f.invalidate();
        return { ok: true, path: "/local/secret" };
      },
      isCurrent: () => mode !== "account" || calls < 2,
      request: async () => {
        calls++;
        if (calls === 2 && mode === "permission")
          throw Object.assign(new Error(), { reason: "forbidden" });
        return calls === 2 && mode === "generation"
          ? { ...target, target: { ...scope, generation: 2 } }
          : target;
      },
    });
    const result = await createWorkDirectoryCopier().copy(input, f.io);
    assert.equal(result.ok, false, mode);
    assert.deepEqual(f.writes, []);
    assert.equal(f.removed(), true);
  }
});
test("cancelled remote replies and subsequent clicks cannot write late", async () => {
  const copier = createWorkDirectoryCopier();
  let release;
  const ready = new Promise((resolve) => {
    release = resolve;
  });
  const f = fixture({
    local: async () => {
      await ready;
      return { ok: true, path: "/late/path" };
    },
  });
  const first = copier.copy(input, f.io);
  await Promise.resolve();
  copier.cancel(input.requestId);
  release();
  assert.deepEqual(await first, { ok: false, reason: "cancelled" });
  assert.deepEqual(f.writes, []);
});
test("timeout after slow local read preserves previous clipboard", async (t) => {
  const now = Date.now();
  let elapsed = 0;
  t.mock.method(Date, "now", () => now + elapsed);
  const f = fixture({
    local: async () => {
      elapsed = 31000;
      return { ok: true, path: "/late" };
    },
  });
  assert.deepEqual(await createWorkDirectoryCopier().copy(input, f.io), {
    ok: false,
    reason: "timeout",
  });
  assert.deepEqual(f.writes, []);
});
test("clipboard failures are not reported as copied", async () => {
  const f = fixture({
    local: async () => ({ ok: true, path: "/valid" }),
    writeText: () => {
      throw Error("native clipboard unavailable");
    },
  });
  assert.deepEqual(await createWorkDirectoryCopier().copy(input, f.io), {
    ok: false,
    reason: "clipboard_failed",
  });
});
test("tampered encrypted replies and mismatched request bindings fail closed", async () => {
  for (const change of [
    (command) => {
      command.params = { ...command.params, generation: 2 };
    },
    (command) => {
      command.result.ciphertext = "AAAA";
    },
    (command) => {
      command.result = seal(
        { ...command.params, run_id: second },
        "/private/path",
      );
    },
    (command) => {
      command.device_id = id;
    },
    (command) => {
      command.result = { path: "/plaintext/not-allowed" };
    },
  ]) {
    const f = fixture();
    const original = f.io.request;
    f.io.request = async (...args) => {
      const response = await original(...args);
      if (response.command) change(response.command);
      return response;
    };
    assert.equal(
      (await createWorkDirectoryCopier().copy(input, f.io)).ok,
      false,
    );
    assert.deepEqual(f.writes, []);
  }
});
test("pending command polling preserves request identity", async () => {
  let command;
  const f = fixture({
    request: async (path, options) => {
      if (path.endsWith("work-directory")) return target;
      if (options.method === "POST") {
        command = {
          id,
          kind: "workspace_path",
          device_id: second,
          params: options.body.params,
          status: "PENDING",
        };
        return { command };
      }
      return {
        command: {
          ...command,
          status: "SUCCEEDED",
          result: seal(command.params, "/remote/polled"),
        },
      };
    },
  });
  assert.deepEqual(await createWorkDirectoryCopier().copy(input, f.io), {
    ok: true,
  });
  assert.deepEqual(f.writes, ["/remote/polled"]);
});
test("cross-platform paths are bounded and never interpreted as commands", () => {
  assert.equal(validPath("C:\\Users\\测试\\project"), true);
  assert.equal(validPath("/tmp/hello world"), true);
  assert.equal(validPath("relative/path"), false);
  assert.equal(validPath("/tmp/new\ncommand"), false);
  assert.equal(validPath("/" + "a".repeat(4096)), false);
});
test("main-process fetch uses fixed origin, CSRF, bounded body and no redirects", async () => {
  let request;
  const session = {
    cookies: { get: async () => [{ value: "isolated-test-csrf" }] },
    fetch: async (url, options) => {
      request = { url, options };
      return new Response('{"ok":true}');
    },
  };
  const signal = new AbortController().signal;
  assert.deepEqual(
    await requestJSON(session, "https://test.invalid", "/api/v1/example", {
      signal,
      method: "POST",
      body: { kind: "workspace_path" },
    }),
    { ok: true },
  );
  assert.equal(request.url, "https://test.invalid/api/v1/example");
  assert.equal(request.options.redirect, "error");
  assert.equal(request.options.headers["X-CSRF-Token"], "isolated-test-csrf");
  session.fetch = async () => new Response("a".repeat(17000));
  await assert.rejects(
    requestJSON(session, "https://test.invalid", "/api/v1/example", { signal }),
  );
  session.cookies.get = async () => [];
  await assert.rejects(
    requestJSON(session, "https://test.invalid", "/api/v1/example", {
      signal,
      method: "POST",
    }),
  );
});

test("expired command responses are timeout rather than busy", async () => {
  const signal = new AbortController().signal;
  for (const [status, code, reason] of [
    [409, "COMMAND_EXPIRED", "timeout"],
    [409, "COMMAND_CONFLICT", "busy"],
    [404, "NOT_FOUND", "forbidden"],
    [503, "UNAVAILABLE", "unavailable"],
  ]) {
    const session = {
      fetch: async () =>
        new Response(JSON.stringify({ error: { code } }), { status }),
    };
    await assert.rejects(
      requestJSON(session, "https://test.invalid", "/api/v1/example", {
        signal,
      }),
      (error) => error.reason === reason,
    );
  }
});
