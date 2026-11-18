const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const os = require("node:os");
const net = require("node:net");
const { once } = require("node:events");
const { connectBrowserBroker } = require("./broker-client.cjs");

async function fixture(t) {
  const root = await fs.mkdtemp(
    path.join(os.tmpdir(), "openfox-broker-client-"),
  );
  const socketPath = path.join(root, "browser.sock");
  await fs.chmod(root, 0o700);
  const server = net.createServer();
  server.listen(socketPath);
  await once(server, "listening");
  await fs.chmod(socketPath, 0o600);
  const sockets = new Set();
  server.on("connection", (socket) => {
    sockets.add(socket);
    socket.on("close", () => sockets.delete(socket));
  });
  t.after(async () => {
    for (const socket of sockets) socket.destroy();
    await new Promise((resolve) => server.close(resolve));
    await fs.rm(root, { recursive: true, force: true });
  });
  const claim = {
    organization_id: "org",
    binding_id: "binding",
    host_instance_id: "host",
    host_epoch: 2,
    revision: 3,
    claim_secret: "single-use",
  };
  const binding = { id: "binding", ...claim };
  delete binding.binding_id;
  delete binding.claim_secret;
  const connected = once(server, "connection");
  let received = [],
    waiters = [];
  const ready = connectBrowserBroker({
    socketPath,
    claim,
    execute: async (_request, signal) => {
      await new Promise((resolve) =>
        signal.addEventListener("abort", resolve, { once: true }),
      );
      return { ok: true, text: "too late" };
    },
    disconnected: () => {},
    revoked: (epoch) => {
      received.push({ type: "revocation", epoch });
    },
  });
  const [socket] = await connected;
  const send = (value) => {
    const payload = Buffer.from(JSON.stringify({ version: 1, ...value }));
    const header = Buffer.alloc(4);
    header.writeUInt32BE(payload.length);
    socket.write(Buffer.concat([header, payload]));
  };
  let buffer = Buffer.alloc(0);
  socket.on("data", (chunk) => {
    buffer = Buffer.concat([buffer, chunk]);
    while (buffer.length >= 4 && buffer.length >= 4 + buffer.readUInt32BE(0)) {
      const size = buffer.readUInt32BE(0),
        message = JSON.parse(buffer.subarray(4, size + 4));
      buffer = buffer.subarray(size + 4);
      received.push(message);
      if (message.type === "attach") send({ type: "attached", binding });
      if (message.type === "grant")
        send({ type: "grant-result", id: message.id });
      for (const resolve of waiters) resolve();
      waiters = [];
    }
  });
  const client = await ready;
  t.after(client.close);
  const take = async (type) => {
    for (;;) {
      const item = received.find((m) => m.type === type);
      if (item) {
        received = received.filter((m) => m !== item);
        return item;
      }
      await new Promise((resolve) => waiters.push(resolve));
    }
  };
  return { client, claim, send, socket, take, socketPath };
}

test(
  "local broker client authenticates once, grants, cancels in-flight output and receives run revocation",
  { timeout: 10000 },
  async (t) => {
    const f = await fixture(t);
    assert.equal((await f.take("attach")).attach.claim_secret, "single-use");
    await f.client.grant({
      epoch: 1,
      run_id: "run",
      generation: 1,
      origins: ["https://example.test"],
    });
    f.send({
      type: "execute",
      id: "operation",
      request: { lease_expires_at: new Date(Date.now() + 10000).toISOString() },
    });
    f.send({ type: "cancel", id: "operation" });
    const result = (await f.take("result")).result;
    assert.deepEqual(result, { ok: false, text: "OUTCOME_UNKNOWN" });
    f.send({ type: "revoked", grant: { epoch: 2 } });
    // A following ping response ensures the revocation frame has been processed.
    await new Promise((resolve) => setTimeout(resolve, 20));
    assert.equal((await f.take("revocation")).epoch, 2);
    const closed = once(f.socket, "close");
    f.send({ type: "unknown" });
    await closed;
    await assert.rejects(f.client.grant({ epoch: 3 }), { code: "UNAVAILABLE" });
  },
);

test("local broker client refuses a socket exposed to other users", async (t) => {
  const f = await fixture(t);
  await fs.chmod(f.socketPath, 0o666);
  await assert.rejects(connectBrowserBroker({ socketPath: f.socketPath }), {
    code: "FORBIDDEN",
  });
});
