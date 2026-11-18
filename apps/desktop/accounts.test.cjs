const { test } = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const os = require("node:os");
const { openAccounts } = require("./accounts.cjs");
test("account slots stay isolated, persist across restarts and survive failed writes", async (t) => {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), "openfox-accounts-"));
  t.after(() => fs.rm(dir, { recursive: true, force: true }));
  const a = await openAccounts(dir, "https://a.example"),
    orig = a.partition();
  await a.name("first");
  await Promise.all([a.select(null), a.name("updated first")]);
  const second = a.partition();
  assert.notEqual(second, orig);
  assert.equal(a.list().find((s) => s.name === "updated first").id, "default");
  await a.name("second");
  const b = await openAccounts(dir, "https://a.example");
  assert.equal(b.partition(), second);
  await b.select("default");
  assert.equal(b.partition(), orig);
  const other = await openAccounts(dir, "https://b.example");
  assert.equal(other.list().length, 1);
  await fs.rm(dir, { recursive: true });
  await assert.rejects(b.select(null));
  assert.equal(b.partition(), orig);
});
