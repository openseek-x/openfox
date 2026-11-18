const fs = require("node:fs/promises");
const { constants } = require("node:fs");
const path = require("node:path");
const { createHash, randomUUID } = require("node:crypto");
const { failure } = require("./policy.cjs");

const MAX_FILE = 20 * 1024 * 1024;
const MAX_RUN = 100 * 1024 * 1024;
const uuid = /^[0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12}$/;
const fileName = (value) =>
  path
    .basename(String(value))
    .replace(/[\\/:\x00-\x1f\x7f]/g, "_")
    .slice(0, 180) || "download";

async function readBounded(file, expected) {
  const handle = await fs.open(
    file,
    constants.O_RDONLY | constants.O_NOFOLLOW | constants.O_NONBLOCK,
  );
  try {
    const stat = await handle.stat();
    if (
      !stat.isFile() ||
      stat.size > MAX_FILE ||
      (expected !== undefined && stat.size !== expected)
    )
      throw failure("LIMIT_EXCEEDED");
    const buffer = Buffer.alloc(stat.size + 1);
    let offset = 0;
    while (offset < buffer.length) {
      const { bytesRead } = await handle.read(
        buffer,
        offset,
        buffer.length - offset,
        offset,
      );
      if (!bytesRead) break;
      offset += bytesRead;
    }
    if (offset !== stat.size) throw failure("STALE_TARGET");
    return buffer.subarray(0, offset);
  } finally {
    await handle.close();
  }
}

// Per-Run storage is separate from manual downloads chosen by the user.
// Only completed, hashed files can be handed back to the daemon.
function createBrowserFiles({ root, changed = () => {} }) {
  let current = null;
  const pendingCleanup = new Set();
  const track = (run, work) => {
    run.pending.add(work);
    work.finally(() => run.pending.delete(work)).catch(() => {});
    return work;
  };
  const check = (run) => {
    if (current !== run || run.closed) throw failure("CONTROL_REVOKED");
  };
  const cleanup = () => {
    const run = current;
    current = null;
    if (!run) return Promise.resolve();
    run.closed = true;
    for (const file of run.files.values()) file.item?.cancel();
    const pending = run.ready
      .catch(() => {})
      .then(async () => {
        // A screenshot or download setup may already be writing when control
        // is revoked. Wait for it before removing this grant's directory.
        await Promise.allSettled([...run.pending]);
        if (run.created) {
          await fs.rm(run.directory, { recursive: true, force: true });
          // rmdir removes only an empty Run parent, never a newer grant.
          await fs.rmdir(path.dirname(run.directory)).catch((error) => {
            if (!["ENOENT", "ENOTEMPTY", "EEXIST"].includes(error.code))
              throw error;
          });
        }
      });
    pendingCleanup.add(pending);
    pending.finally(() => pendingCleanup.delete(pending)).catch(() => {});
    return pending;
  };
  const start = (id, generation) => {
    if (!uuid.test(id) || !Number.isSafeInteger(generation) || generation < 1)
      throw failure("INVALID_REQUEST");
    if (current?.id === id && current.generation === generation) return;
    void cleanup().catch(() => {});
    const runRoot = path.join(root, "downloads", `${id}-${generation}`);
    const directory = path.join(runRoot, randomUUID());
    const run = {
      id,
      generation,
      directory,
      files: new Map(),
      pending: new Set(),
      closed: false,
      used: 0,
    };
    run.ready = (async () => {
      await fs.mkdir(path.join(root, "downloads"), {
        recursive: true,
        mode: 0o700,
      });
      const parent = await fs.lstat(path.join(root, "downloads"));
      if (!parent.isDirectory() || parent.mode & 0o077)
        throw failure("FORBIDDEN");
      await fs.mkdir(runRoot, { recursive: true, mode: 0o700 });
      const info = await fs.lstat(runRoot);
      if (!info.isDirectory() || info.mode & 0o077) throw failure("FORBIDDEN");
      // Each explicit control grant owns a fresh subdirectory, so a new grant
      // can start while cancellation cleans the previous grant's files.
      try {
        await fs.mkdir(directory, { mode: 0o700 });
      } catch (error) {
        if (error.code !== "ENOENT") throw error;
        // The last previous grant may remove its now-empty parent between
        // the mkdir/check above and this grant's mkdir.
        await fs.mkdir(runRoot, { recursive: true, mode: 0o700 });
        const retryParent = await fs.lstat(runRoot);
        if (!retryParent.isDirectory() || retryParent.mode & 0o077)
          throw failure("FORBIDDEN");
        await fs.mkdir(directory, { mode: 0o700 });
      }
      run.created = true;
    })();
    run.ready.catch(() => {});
    current = run;
  };
  const reserve = (tab, name) => {
    const run = current;
    if (!run || run.closed) throw failure("CONTROL_REVOKED");
    if (run.files.size >= 20) throw failure("LIMIT_EXCEEDED");
    const id = randomUUID();
    const file = {
      id,
      tab_id: tab,
      name: fileName(name),
      state: "in_progress",
      size: 0,
      path: path.join(run.directory, id, fileName(name)),
    };
    run.files.set(id, file);
    return { run, file };
  };
  const finish = async (run, file) => {
    const content = await readBounded(file.path);
    check(run);
    if (run.used + content.length - file.size > MAX_RUN)
      throw failure("LIMIT_EXCEEDED");
    run.used += content.length - file.size;
    file.size = content.length;
    file.sha256 = createHash("sha256").update(content).digest("hex");
    file.state = "completed";
    changed();
  };
  return {
    start,
    cleanup,
    async close() {
      await cleanup();
      await Promise.allSettled([...pendingCleanup]);
    },
    async uploads(files, guard) {
      const run = current;
      if (
        !run ||
        !Array.isArray(files) ||
        files.length < 1 ||
        files.length > 10
      )
        throw failure("INVALID_REQUEST");
      const transferRoot = await fs.realpath(path.join(root, "transfers"));
      const result = [];
      let bytes = 0;
      for (const file of files) {
        guard();
        check(run);
        if (
          !file ||
          typeof file.path !== "string" ||
          !Number.isSafeInteger(file.size) ||
          file.size < 0 ||
          file.size > MAX_FILE ||
          !/^[a-f0-9]{64}$/.test(file.sha256)
        )
          throw failure("INVALID_REQUEST");
        const real = await fs.realpath(file.path);
        const relative = path.relative(transferRoot, real);
        if (
          relative.startsWith("..") ||
          path.isAbsolute(relative) ||
          relative.split(path.sep).length !== 3
        )
          throw failure("FORBIDDEN");
        const content = await readBounded(real, file.size);
        if (createHash("sha256").update(content).digest("hex") !== file.sha256)
          throw failure("STALE_TARGET");
        bytes += content.length;
        if (run.used + bytes > MAX_RUN) throw failure("LIMIT_EXCEEDED");
        result.push(real);
      }
      guard();
      check(run);
      run.used += bytes;
      return result;
    },
    captureDownload(item, tab) {
      if (!current) return false;
      let run, file;
      try {
        ({ run, file } = reserve(tab, item.getFilename()));
        if (item.getTotalBytes() > MAX_FILE) throw failure("LIMIT_EXCEEDED");
      } catch {
        if (file) file.state = "cancelled";
        item.cancel();
        return true;
      }
      file.item = item;
      // Prevent Chromium from opening a Save dialog before the private folder
      // exists. Resuming an interrupted download is not supported.
      item.setSavePath(file.path);
      item.pause();
      void track(
        run,
        run.ready
          .then(() => {
            check(run);
            return fs.mkdir(path.dirname(file.path), { mode: 0o700 });
          })
          .then(() => {
            check(run);
            item.resume();
          }),
      ).catch(() => item.cancel());
      item.on("updated", () => {
        if (current !== run || run.closed) {
          item.cancel();
          return;
        }
        const size = item.getReceivedBytes();
        run.used += Math.max(0, size - file.size);
        file.size = size;
        if (size > MAX_FILE || run.used > MAX_RUN) item.cancel();
        changed();
      });
      item.once("done", (_event, state) => {
        file.item = null;
        if (state !== "completed" || run.closed) {
          file.state = "cancelled";
          void fs.rm(file.path, { force: true }).catch(() => {});
          changed();
          return;
        }
        void finish(run, file).catch(() => {
          file.state = "failed";
          void fs.rm(file.path, { force: true }).catch(() => {});
          changed();
        });
      });
      return true;
    },
    async screenshot(tab, image, guard) {
      const bytes = Buffer.from(image.data, "base64");
      if (bytes.length > 4 * 1024 * 1024) throw failure("LIMIT_EXCEEDED");
      const { run, file } = reserve(
        tab,
        image.mime_type === "image/jpeg" ? "screenshot.jpg" : "screenshot.png",
      );
      return track(
        run,
        (async () => {
          try {
            if (run.used + bytes.length > MAX_RUN)
              throw failure("LIMIT_EXCEEDED");
            await run.ready;
            guard();
            check(run);
            await fs.mkdir(path.dirname(file.path), { mode: 0o700 });
            await fs.writeFile(file.path, bytes, { mode: 0o600, flag: "wx" });
            await finish(run, file);
            guard();
            return file.id;
          } catch (error) {
            file.state = "failed";
            await fs
              .rm(path.dirname(file.path), { recursive: true, force: true })
              .catch(() => {});
            throw error;
          }
        })(),
      );
    },
    list(tab) {
      return [...(current?.files.values() || [])]
        .filter((f) => f.tab_id === tab)
        .map(({ id, name, size, state }) => ({ id, name, size, state }));
    },
    get(tab, id) {
      const file = current?.files.get(id);
      if (!file || file.tab_id !== tab || file.state !== "completed")
        throw failure("UNAVAILABLE");
      return {
        file: {
          path: file.path,
          name: file.name,
          size: file.size,
          sha256: file.sha256,
        },
      };
    },
  };
}

module.exports = { createBrowserFiles };
