const test = require("node:test");
const assert = require("node:assert/strict");
const { createHash, generateKeyPairSync, sign } = require("node:crypto");
const fs = require("node:fs/promises");

const {
  downloadUpdate,
  createUpdateManager,
  MAX_PACKAGE_BYTES,
} = require("./update-install.cjs");

const bytes = Buffer.from("an update archive");
const keys = generateKeyPairSync("ed25519");
const publicKey = Buffer.from(
  keys.publicKey.export({ format: "jwk" }).x,
  "base64url",
).toString("base64");
const signature = sign(null, bytes, keys.privateKey).toString("base64") + "\n";
const digest = (data) => createHash("sha256").update(data).digest("hex");
const manifest = {
  version: "0.2.0",
  download_url: "https://center.example/desktop/OpenFox.dmg",
  sha256: digest(bytes),
  signature_sha256: digest(signature),
};
const available = {
  status: "update_available",
  manifest,
  skipped: false,
  mandatory: false,
};

test("stages only the signed DMG and cleans up its private temporary copy", async () => {
  const archive = await downloadUpdate(
    manifest,
    async (url, options) => {
      assert.equal(options.redirect, "error");
      assert.equal(options.credentials, "omit");
      return new Response(url.endsWith(".sig") ? signature : bytes);
    },
    publicKey,
  );
  try {
    assert.deepEqual(await fs.readFile(archive.file), bytes);
    assert.equal(archive.size, bytes.length);
    assert.equal(archive.signature, signature.trim());
  } finally {
    archive.discard();
  }
  await assert.rejects(fs.stat(archive.file), { code: "ENOENT" });
});

test("rejects tampering even if the release advertises the modified DMG digest", async () => {
  const changed = Buffer.from("different DMG");
  await assert.rejects(
    downloadUpdate(
      { ...manifest, sha256: digest(changed) },
      async (url) => new Response(url.endsWith(".sig") ? signature : changed),
      publicKey,
    ),
    /signature verification/,
  );
  for (const invalid of ["invalid", signature.repeat(2)]) {
    await assert.rejects(
      downloadUpdate(
        { ...manifest, signature_sha256: digest(invalid) },
        async () => new Response(invalid),
        publicKey,
      ),
    );
  }
});

test("bad digest, excessive length, HTTP error and interrupted download never become ready", async () => {
  for (const response of [
    new Response("bad digest"),
    new Response(bytes, {
      headers: { "Content-Length": String(MAX_PACKAGE_BYTES + 1) },
    }),
    new Response(null, { status: 404 }),
    new Response(
      new ReadableStream({
        start(c) {
          c.error(new Error("interrupted"));
        },
      }),
    ),
  ]) {
    await assert.rejects(
      downloadUpdate(
        manifest,
        async (url) =>
          url.endsWith(".sig") ? new Response(signature) : response,
        publicKey,
      ),
    );
  }
});

function manager(overrides = {}) {
  return createUpdateManager({
    automatic: true,
    check: async () => available,
    record: async () => {},
    showResult: async () => {},
    download: async () => ({ discard() {} }),
    install: () => assert.fail("unconfirmed install"),
    ...overrides,
  });
}

test("manual and scheduled checks share the download; repeated checks require affirmative consent", async () => {
  const release = Promise.withResolvers();
  let downloads = 0;
  const prompts = [];
  const updates = manager({
    download: async () => {
      downloads++;
      await release.promise;
      return { discard() {} };
    },
    showResult: (_result, update, manual) =>
      prompts.push([update.phase, manual]),
  });
  const startup = updates.check(false);
  await new Promise(setImmediate);
  assert.equal(updates.update.phase, "downloading");
  assert.equal(await updates.skip(() => assert.fail()), false);
  assert.equal(updates.check(true), startup);
  release.resolve();
  await startup;
  await updates.check(false);
  await updates.check(true);
  assert.equal(downloads, 1);
  assert.deepEqual(prompts, [
    ["ready", true],
    ["ready", false],
    ["ready", true],
  ]);
});

test("failed preparation can be retried; skip and mandatory policies are enforced", async () => {
  let count = 0;
  const updates = manager({
    download: async () => {
      if (++count === 1) throw new Error("disk full");
      return { discard() {} };
    },
  });
  assert.equal((await updates.check(false)).status, "failed");
  assert.equal(updates.update.phase, "failed");
  await updates.check(true);
  assert.equal(updates.update.phase, "ready");
  const skipped = manager({
    check: async () => ({ ...available, skipped: true }),
  });
  await skipped.check(false);
  assert.equal(skipped.update, null);
  await skipped.check(true);
  assert.equal(skipped.update.phase, "ready");
  const manual = manager({
    automatic: false,
    check: async () => ({ ...available, mandatory: true }),
    download: () => assert.fail("development build staged an update"),
  });
  await manual.check(false);
  assert.equal(manual.update.phase, "manual");
  assert.equal(await manual.skip(() => assert.fail()), false);
});

test("Later, dismissal and a manual check do not authorize installation, including mandatory updates", async () => {
  const responses = [false, undefined, false, true];
  let downloads = 0;
  let installs = 0;
  let discarded = 0;
  const archive = {
    discard() {
      discarded++;
    },
  };
  const updates = manager({
    check: async () => ({ ...available, mandatory: true }),
    download: async () => {
      downloads++;
      return archive;
    },
    showResult: () => responses.shift(),
    install: async (item) => {
      assert.equal(item, archive);
      assert.equal(updates.update.phase, "installing");
      installs++;
    },
  });
  for (const manual of [false, false, true]) {
    await updates.check(manual);
    assert.equal(updates.update.phase, "ready");
    assert.equal(installs, 0);
    assert.equal(discarded, 0);
  }
  await updates.check(true);
  assert.deepEqual([downloads, installs, discarded], [1, 1, 1]);
});

test("withdrawn offers drop the archive and confirmation cannot apply to another package", async () => {
  let result = available;
  let discards = 0;
  const downloads = [];
  const installs = [];
  let confirm = false;
  const updates = manager({
    check: async () => result,
    download: async (m) => {
      downloads.push(m.sha256);
      return { digest: m.sha256, discard: () => discards++ };
    },
    showResult: () => confirm,
    install: async (archive) => {
      installs.push(archive.digest);
    },
  });
  await updates.check(false);
  result = { status: "no_update_info" };
  await updates.check(true);
  assert.equal(updates.update, null);
  assert.equal(discards, 1);
  result = {
    ...available,
    manifest: { ...manifest, version: "0.3.0", sha256: "b".repeat(64) },
  };
  await updates.check(false);
  assert.deepEqual(installs, []);
  confirm = true;
  await updates.check(true);
  assert.deepEqual(installs, [downloads[1]]);
  assert.equal(discards, 2);
});

test("failed installation reports the failure and retries only after fresh confirmation", async () => {
  let consent = true,
    attempts = 0;
  const updates = manager({
    showResult: (result) => result.status === "update_available" && consent,
    install: async () => {
      if (++attempts === 1) throw new Error("installer unavailable");
    },
  });
  assert.equal((await updates.check(true)).status, "failed");
  assert.equal(updates.update.phase, "failed");
  consent = false;
  await updates.check(false);
  assert.equal(attempts, 1);
  consent = true;
  await updates.check(true);
  assert.equal(attempts, 2);
});

test("GitHub download accepts only the release asset CDN redirect", async () => {
  const { EventEmitter } = require("node:events");
  const { resolveGitHubDownload } = require("./update-install.cjs");
  const url =
    "https://github.com/openseek-x/openfox/releases/download/v0.2.0/OpenFox-0.2.0-arm64.dmg";
  async function redirect(location) {
    const net = {
      request(options) {
        assert.equal(options.redirect, "manual");
        assert.equal(options.credentials, "omit");
        const req = new EventEmitter();
        req.abort = () => req.emit("abort");
        req.end = () => req.emit("redirect", 302, "HEAD", location);
        return req;
      },
    };
    return resolveGitHubDownload(net, {}, url, AbortSignal.timeout(1000));
  }
  const cdn =
    "https://release-assets.githubusercontent.com/path?signature=test";
  assert.equal(await redirect(cdn), cdn);
  for (const suffix of ["@other.com/file", ".example.org/path", ":8000/file"]) {
    await assert.rejects(
      redirect("https://release-assets.githubusercontent.com" + suffix),
    );
  }
  await assert.rejects(
    redirect("http://release-assets.githubusercontent.com/file"),
  );
  await assert.rejects(
    redirect("https://user@release-assets.githubusercontent.com/file"),
  );
});
