const { test } = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const os = require("node:os");
const { createProxy } = require("./proxy.cjs");
test("stored proxy credentials apply only to the selected proxy and recover after settings removal", async (t) => {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), "openfox-proxy-"));
  t.after(() => fs.rm(dir, { recursive: true, force: true }));
  const file = path.join(dir, "proxy.json");
  const mockEncryption = {
    isEncryptionAvailable: () => true,
    getSelectedStorageBackend: () => "test",
    encryptString: (v) => Buffer.from(v.split("").reverse().join("")),
    decryptString: (b) => b.toString().split("").reverse().join(""),
  };
  const p = createProxy(file, mockEncryption);
  const server = {
    mode: "manual",
    protocol: "http",
    host: "example.test",
    port: 8080,
    authenticated: true,
    username: "account",
  };
  await p.save({ ...server, password: "fixture" });
  assert.equal((await fs.readFile(file, "utf8")).includes("fixture"), false);
  const q = createProxy(file, mockEncryption);
  await q.load();
  const info = { isProxy: true, host: server.host, port: server.port };
  assert.equal(q.authenticate({ ...info, isProxy: false }), null);
  assert.equal(q.authenticate({ ...info, port: 7777 }), null);
  assert.deepEqual(q.authenticate(info), {
    username: "account",
    password: "fixture",
  });
  await assert.rejects(q.save({ ...server, host: "elsewhere.test" }));
  await fs.writeFile(file, "{");
  await assert.rejects(q.load());
  await fs.unlink(file);
  await q.load();
  assert.deepEqual(q.config(), { mode: "system" });
  assert.equal(q.authenticate(info), null);
});
