// Download through the workbench proxy. Only offer installation of a signed DMG.
const { createHash, createPublicKey, verify } = require("node:crypto");
const { promisify } = require("node:util");
const verifyDMG = promisify(verify);
const fs = require("node:fs/promises");
const { createWriteStream, rmSync } = require("node:fs");
const os = require("node:os");
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

async function downloadPackage(
  manifest,
  fetchPackage,
  file,
  maxBytes = MAX_PACKAGE_BYTES,
) {
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
    Number(response.headers.get("content-length")) > maxBytes
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
        if (bytes > maxBytes) throw new Error("Update too large");
        hash.update(chunk);
        yield chunk;
      }
    },
    createWriteStream(file, { flags: "wx", mode: 0o600 }),
    { signal },
  );
  if (!bytes || hash.digest("hex") !== manifest.sha256)
    throw new Error("Update checksum mismatch");
  return bytes;
}

async function downloadUpdate(manifest, fetchPackage, publicKey) {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), "openfox-update-"));
  const discard = () => {
    process.off("exit", discard);
    try {
      rmSync(dir, { recursive: true, force: true });
    } catch {
      // The OS also reclaims temporary files after exit.
    }
  };
  process.once("exit", discard);
  try {
    const signatureFile = path.join(dir, "update.sig");
    await downloadPackage(
      {
        download_url: manifest.download_url + ".sig",
        sha256: manifest.signature_sha256,
      },
      fetchPackage,
      signatureFile,
      89,
    );
    const signature = (await fs.readFile(signatureFile, "utf8")).trim();
    if (!/^[A-Za-z0-9+/]{86}==$/.test(signature))
      throw new Error("Invalid Ed25519 signature");
    const key = createPublicKey({
      format: "jwk",
      key: {
        kty: "OKP",
        crv: "Ed25519",
        x: Buffer.from(publicKey, "base64").toString("base64url"),
      },
    });
    const file = path.join(dir, "OpenFox.dmg");
    const size = await downloadPackage(manifest, fetchPackage, file);
    if (
      !(await verifyDMG(
        null,
        await fs.readFile(file),
        key,
        Buffer.from(signature, "base64"),
      ))
    )
      throw new Error("DMG signature verification failed");
    return { file, size, signature, version: manifest.version, discard };
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
      update.manifest.sha256 === result.manifest.sha256 &&
      update.manifest.signature_sha256 === result.manifest.signature_sha256;
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

module.exports = {
  resolveGitHubDownload,
  createUpdateManager,
  downloadUpdate,
  MAX_PACKAGE_BYTES,
};
