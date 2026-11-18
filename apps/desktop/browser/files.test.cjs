const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const os = require("node:os");
const path = require("node:path");
const { createBrowserFiles } = require("./files.cjs");

const runID = "10000000-0000-4000-8000-000000000001";
const image = {
  mime_type: "image/png",
  data: Buffer.from("fixture").toString("base64"),
};
async function fixture(t) {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "openfox-files-"));
  const files = createBrowserFiles({ root });
  t.after(async () => {
    await files.close();
    await fs.rm(root, { recursive: true, force: true });
  });
  files.start(runID, 1);
  return { files, root };
}

test("revocation waits for a screenshot write and preserves a newer grant", async (t) => {
  const { files, root } = await fixture(t);
  const writeFile = fs.writeFile;
  let release, started;
  const blocked = new Promise((resolve) => {
    release = resolve;
  });
  const writing = new Promise((resolve) => {
    started = resolve;
  });
  let firstPath;
  fs.writeFile = async (file, ...args) => {
    if (!firstPath && String(file).endsWith("screenshot.png")) {
      firstPath = file;
      started();
      await blocked;
    }
    return writeFile(file, ...args);
  };
  t.after(() => {
    fs.writeFile = writeFile;
    release();
  });
  const old = files.screenshot("tab", image, () => {});
  const rejected = assert.rejects(old, { code: "CONTROL_REVOKED" });
  await writing;
  const cleanup = files.cleanup();
  files.start(runID, 1);
  const fresh = await files.screenshot("tab", image, () => {});
  const freshPath = files.get("tab", fresh).file.path;
  release();
  await rejected;
  await cleanup;
  await assert.rejects(fs.stat(firstPath), { code: "ENOENT" });
  assert.equal(await fs.readFile(freshPath, "utf8"), "fixture");
  const grants = await fs.readdir(path.join(root, "downloads", `${runID}-1`));
  assert.equal(grants.length, 1);
});

test("failed screenshots are not left in progress or exposed as completed files", async (t) => {
  const { files } = await fixture(t);
  await assert.rejects(
    files.screenshot("tab", image, () => {
      throw Object.assign(new Error("CONTROL_REVOKED"), {
        code: "CONTROL_REVOKED",
      });
    }),
    { code: "CONTROL_REVOKED" },
  );
  const [failed] = files.list("tab");
  assert.equal(failed.state, "failed");
  assert.throws(() => files.get("tab", failed.id), { code: "UNAVAILABLE" });
  await assert.rejects(
    files.screenshot(
      "tab",
      {
        ...image,
        data: Buffer.alloc(4 * 1024 * 1024 + 1).toString("base64"),
      },
      () => {},
    ),
    { code: "LIMIT_EXCEEDED" },
  );
  assert.equal(files.list("tab").length, 1);
});

test("closing a grant removes its empty managed Run parent", async (t) => {
  const { files, root } = await fixture(t);
  await files.screenshot("tab", image, () => {});
  await files.cleanup();
  assert.deepEqual(await fs.readdir(path.join(root, "downloads")), []);
});
