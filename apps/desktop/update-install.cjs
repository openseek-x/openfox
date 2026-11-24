// Download through the workbench proxy. Only hand a verified local package to Installer.
const { createHash } = require("node:crypto");
const fs = require("node:fs/promises");
const { createWriteStream, rmSync } = require("node:fs");
const os = require("node:os");
const { execFile } = require("node:child_process");
const { promisify } = require("node:util");
const runFile = promisify(execFile);
const path = require("node:path");
const { pipeline } = require("node:stream/promises");

const MAX_PACKAGE_BYTES = 512 * 1024 * 1024;
const DOWNLOAD_TIMEOUT_MS = 20 * 60 * 1000;

// Electron session.fetch cancels manual redirects. Inspect GitHub's redirect
// with net.request before streaming the package with redirects disabled.
async function resolveGitHubDownload(net, session, url, signal) {
  signal.throwIfAborted();
  const request = net.request({
    url,
    session,
    method: "HEAD",
    redirect: "manual",
    credentials: "omit",
  });
  const abort = () => request.abort();
  try {
    return await new Promise((resolve, reject) => {
      signal.addEventListener("abort", abort, { once: true });
      request.once("abort", () => reject(new Error("Update lookup aborted")));
      request.once("error", reject);
      request.once("redirect", (_status, _method, location) => {
        const target = URL.canParse(location) ? new URL(location) : null;
        if (
          target?.origin === "https://release-assets.githubusercontent.com" &&
          !target.username &&
          !target.password
        )
          resolve(target.href);
        else reject(new Error("Unexpected GitHub release asset redirect"));
        request.abort();
      });
      request.once("response", (response) => {
        response.resume();
        if (response.statusCode === 200) resolve(url);
        else reject(new Error("GitHub release asset unavailable"));
      });
      request.end();
    });
  } finally {
    signal.removeEventListener("abort", abort);
  }
}

async function downloadPackage(manifest, fetchPackage, file) {
  const signal = AbortSignal.timeout(DOWNLOAD_TIMEOUT_MS);
  const response = await fetchPackage(manifest.download_url, {
    credentials: "omit",
    redirect: "error",
    cache: "no-store",
    signal,
  });
  if (
    response.status !== 200 ||
    !response.body ||
    Number(response.headers.get("content-length")) > MAX_PACKAGE_BYTES
  ) {
    await response.body?.cancel();
    throw new Error("Update download rejected");
  }
  let bytes = 0;
  const hash = createHash("sha256");
  await pipeline(
    response.body,
    async function* (chunks) {
      for await (const chunk of chunks) {
        bytes += chunk.byteLength;
        if (bytes > MAX_PACKAGE_BYTES) throw new Error("Update too large");
        hash.update(chunk);
        yield chunk;
      }
    },
    createWriteStream(file, { flags: "wx", mode: 0o600 }),
    { signal },
  );
  if (!bytes || hash.digest("hex") !== manifest.sha256)
    throw new Error("Update checksum mismatch");
}

// Installer may still be reading its package after OpenFox exits. Move the
// confirmed file to our private cache before handing it to the system.
async function downloadUpdate(manifest, fetchPackage) {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), "openfox-update-"));
  const discard = () => {
    process.off("exit", discard);
    try {
      rmSync(dir, { recursive: true, force: true });
    } catch {
      // The system owns cleanup of its temporary directory after a hard exit.
    }
  };
  process.once("exit", discard);
  try {
    const pkg = path.join(dir, "update.pkg");
    await downloadPackage(manifest, fetchPackage, pkg);
    return {
      discard,
      async openInstaller(cache, openPath) {
        await fs.mkdir(cache, { recursive: true, mode: 0o700 });
        // A stable name for this digest allows retries without accumulating copies.
        const target = path.join(
          cache,
          `OpenFox-${manifest.version}-${manifest.sha256}.pkg`,
        );
        const tmp = await fs.mkdtemp(path.join(cache, ".copy-"));
        try {
          const copy = path.join(tmp, "update.pkg");
          await fs.copyFile(pkg, copy);
          if (process.platform === "darwin") {
            // Keep downloaded installers subject to the user's normal Gatekeeper policy.
            const timestamp = Math.floor(Date.now() / 1000).toString(16);
            await runFile("/usr/bin/xattr", [
              "-w",
              "com.apple.quarantine",
              `0083;${timestamp};OpenFox;`,
              copy,
            ]);
          }
          await fs.rename(copy, target);
        } finally {
          await fs.rm(tmp, { recursive: true, force: true });
        }
        const error = await openPath(target);
        if (error) throw new Error("Could not open macOS Installer");
      },
    };
  } catch (error) {
    discard();
    throw error;
  }
}

// Timer, tray and About share one download and one native confirmation dialog.
function createUpdateManager({
  automatic,
  check,
  download,
  install,
  record,
  showResult,
}) {
  let update = null;
  let archive = null;
  let pending = null;
  let manual = false;

  async function run() {
    let result = await check();
    await record(result);
    const found =
      result.status === "update_available" && (!result.skipped || manual);
    const reuse =
      archive &&
      found &&
      update.manifest.version === result.manifest.version &&
      update.manifest.sha256 === result.manifest.sha256;
    if (!reuse) {
      archive?.discard();
      archive = null;
    }
    update = found
      ? {
          manifest: result.manifest,
          mandatory: result.mandatory,
          phase: automatic ? "ready" : "manual",
        }
      : null;
    if (automatic && found && !archive) {
      update.phase = "downloading";
      try {
        archive = await download(update.manifest);
        update.phase = "ready";
      } catch {
        update.phase = "failed";
        result = { status: "failed", version: update.manifest.version };
        await record(result);
      }
    }
    const confirmed = await showResult(result, update, manual);
    if (confirmed === true && update?.phase === "ready") {
      update.phase = "installing";
      try {
        await install(archive);
        update.phase = "installer_opened";
      } catch {
        update.phase = "failed";
        result = { status: "failed", version: update.manifest.version };
        await record(result);
        await showResult(result, update, true);
      } finally {
        archive.discard();
        archive = null;
      }
    }
    return result;
  }

  return {
    get update() {
      return update;
    },
    check(isManual) {
      manual ||= isManual;
      if (!pending)
        pending = run().finally(() => {
          pending = null;
          manual = false;
        });
      return pending;
    },
    async skip(save) {
      const current = update;
      if (current?.phase !== "manual" || current.mandatory) return false;
      await save(current.manifest.version);
      if (update === current) update = null;
      return true;
    },
  };
}

async function removeInstalledPackages(cache, version) {
  const { compareVersions } = require("./update-check.cjs");
  const entries = await fs
    .readdir(cache, { withFileTypes: true })
    .catch((error) => {
      if (error.code === "ENOENT") return [];
      throw error;
    });
  for (const entry of entries) {
    const match = /^OpenFox-(\d+\.\d+\.\d+)-[a-f0-9]{64}\.pkg$/.exec(
      entry.name,
    );
    if (
      entry.isFile() &&
      match &&
      [0, 1].includes(compareVersions(version, match[1]))
    ) {
      await fs.unlink(path.join(cache, entry.name)).catch((error) => {
        if (error.code !== "ENOENT") throw error;
      });
    }
  }
}

module.exports = {
  resolveGitHubDownload,
  removeInstalledPackages,
  createUpdateManager,
  downloadUpdate,
  MAX_PACKAGE_BYTES,
};
