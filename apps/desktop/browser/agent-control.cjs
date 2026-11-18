const { randomUUID } = require("node:crypto");
const { createPageControl } = require("./page-control.cjs");
const { createInputShield } = require("./input-shield.cjs");
const { createBrowserFiles } = require("./files.cjs");
const { connectBrowserBroker } = require("./broker-client.cjs");
const { validateTool } = require("./tool-contract.cjs");
const { failure, normalizeURL } = require("./policy.cjs");

const uuid = /^[0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12}$/;
const result = (value) => ({ ok: true, text: JSON.stringify(value) });

// One instance belongs to one authenticated conversation/profile group.
// BrowserHost owns tabs; this layer owns only explicit per-Run control.
function createAgentControl({
  window,
  connect,
  tabs,
  open,
  focus,
  closeTab,
  visible,
  changed,
}) {
  const instance = randomUUID();
  const pages = new Map();
  let connection = null,
    files = null,
    shield = null,
    pending = false,
    disposed = false;
  let epoch = 0,
    bindingEpoch = 0,
    grant = null,
    error = null;
  const operations = new Set();
  const state = () => ({
    connected: Boolean(connection),
    pending,
    active: Boolean(grant),
    epoch,
    runId: grant?.run_id || null,
    generation: grant?.generation || null,
    clientMessageId: grant?.client_message_id || null,
    origins: grant?.origins || [],
    error,
  });
  const announce = () => changed(state());
  const clearControl = (nextEpoch) => {
    epoch = Math.max(epoch + 1, nextEpoch || 0);
    grant = null;
    for (const operation of operations) operation.abort();
    shield?.hide();
    for (const page of pages.values()) page.close();
    pages.clear();
    void files?.cleanup().catch(() => {});
    announce();
  };
  const takeOver = () => {
    if (!grant && !pending) return;
    clearControl();
    if (connection)
      void connection.grant({ epoch, origins: [] }).catch(() => {
        connection?.close();
      });
  };
  const page = (tab) => {
    if (!pages.has(tab.id)) {
      const control = createPageControl(tab.surface.contents);
      const destroyed = () => {
        dispose();
        pages.delete(tab.id);
      };
      const dispose = () => {
        tab.surface.contents.removeListener("destroyed", destroyed);
        control.close();
      };
      pages.set(tab.id, { ...control, close: dispose });
      tab.surface.contents.once("destroyed", destroyed);
    }
    return pages.get(tab.id);
  };
  const origin = (url) => {
    const normalized = normalizeURL(url);
    if (normalized === "about:blank") return null;
    return new URL(normalized).origin;
  };
  const execute = async (request, signal) => {
    const args = validateTool(request.tool, request.arguments);
    if (!visible()) {
      takeOver();
      throw failure("CONTROL_REVOKED");
    }
    if (
      !grant ||
      request.control_epoch !== epoch ||
      typeof request.read_only !== "boolean" ||
      !uuid.test(request.run_id) ||
      !Number.isSafeInteger(request.generation) ||
      request.generation < 1
    )
      throw failure("CONTROL_REVOKED");
    const before = epoch;
    if (
      grant.run_id &&
      (grant.run_id !== request.run_id ||
        grant.generation !== request.generation)
    )
      throw failure("FORBIDDEN");
    if (
      !Array.isArray(request.origins) ||
      request.origins.length !== grant.origins.length ||
      request.origins.some((url) => !grant.origins.includes(url))
    )
      throw failure("FORBIDDEN");
    const readonlyAction =
      request.tool === "openfox_browser_observe" ||
      ((request.tool === "openfox_browser_tabs" ||
        request.tool === "openfox_browser_files") &&
        args.action === "list");
    if (request.read_only && !readonlyAction) throw failure("FORBIDDEN");
    if (!grant.run_id) {
      grant = {
        ...grant,
        run_id: request.run_id,
        generation: request.generation,
        client_message_id: undefined,
      };
      announce();
    }
    files.start(request.run_id, request.generation);
    const controller = new AbortController();
    operations.add(controller);
    const aborted = () => controller.abort();
    signal.addEventListener("abort", aborted, { once: true });
    if (signal.aborted) controller.abort();
    const deadline = Date.parse(request.lease_expires_at);
    const guard = (url) => {
      if (!visible()) takeOver();
      if (
        disposed ||
        controller.signal.aborted ||
        !grant ||
        epoch !== before ||
        !connection ||
        !visible() ||
        !Number.isFinite(deadline) ||
        deadline <= Date.now()
      )
        throw failure("CONTROL_REVOKED");
      if (url && (!origin(url) || !grant.origins.includes(origin(url)))) {
        error = "ORIGIN_NOT_ALLOWED";
        announce();
        throw failure("ORIGIN_NOT_ALLOWED");
      }
    };
    const ensureTab = (id) => {
      const tab = tabs().find((item) => item.id === id);
      if (!tab || tab.surface.contents.isDestroyed())
        throw failure("TARGET_CLOSED");
      guard(tab.surface.contents.getURL() || tab.url);
      return tab;
    };
    const work = async () => {
      guard();
      if (request.tool === "openfox_browser_tabs") {
        if (args.action === "list")
          return result({
            tabs: tabs()
              .filter((tab) => {
                try {
                  return grant.origins.includes(origin(tab.url));
                } catch {
                  return false;
                }
              })
              .map((tab) => ({
                tab_id: tab.id,
                title: tab.title,
                url: tab.url,
                loading: tab.loading,
              })),
          });
        if (args.action === "open") {
          const url = normalizeURL(args.url);
          guard(url);
          const tab = open(url);
          guard();
          return result({ tab_id: tab.id });
        }
        const tab = ensureTab(args.tab_id);
        if (args.action === "focus") {
          focus(tab.id);
          guard();
          return result({ tab_id: tab.id });
        }
        if (args.action === "close") {
          await closeTab(tab.id);
          guard();
          return result({ closed: true });
        }
      }
      const tab = ensureTab(args.tab_id);
      // Every native operation also rechecks the real top-level URL; frame
      // metadata alone must not keep an old origin grant alive after navigation.
      const pageGuard = (url) => {
        guard(tab.surface.contents.getURL());
        guard(url);
      };
      if (
        request.tool === "openfox_browser_act" &&
        ["navigate", "back", "forward", "reload", "stop"].includes(args.action)
      ) {
        const contents = tab.surface.contents;
        if (args.action === "navigate") {
          const url = normalizeURL(args.url);
          guard(url);
          await tab.surface.navigate(url);
        } else if (args.action === "stop") contents.stop();
        else if (args.action === "reload") contents.reload();
        else {
          const history = contents.navigationHistory;
          const index =
            history.getActiveIndex() + (args.action === "back" ? -1 : 1);
          const entry = history.getEntryAtIndex(index);
          if (!entry) throw failure("STALE_TARGET");
          guard(entry.url);
          history.goToIndex(index);
        }
        guard();
        return result({ completed: true });
      }
      const control = page(tab);
      if (request.tool === "openfox_browser_observe") {
        if (args.action === "frames")
          return result({ frames: await control.frames(pageGuard) });
        if (args.action === "console")
          return result(control.console(pageGuard));
        if (args.action === "screenshot") {
          focus(tab.id);
          const image = await control.screenshot(pageGuard);
          const screenshotID = await files.screenshot(tab.id, image, pageGuard);
          return {
            ...result({ tab_id: tab.id, screenshot_id: screenshotID }),
            image,
          };
        }
        if (args.action === "wait") {
          const until = Math.min(
            deadline,
            Date.now() + (args.timeout_ms ?? 10000),
          );
          for (;;) {
            const value = await control.snapshot(args, pageGuard);
            if (value.nodes.length > 0) return result(value);
            if (Date.now() >= until) throw failure("TIMEOUT");
            await new Promise((resolve) =>
              setTimeout(resolve, Math.min(100, until - Date.now())),
            );
            pageGuard();
          }
        }
        return result(await control.snapshot(args, pageGuard));
      }
      if (request.tool === "openfox_browser_act") {
        focus(tab.id);
        return result(await control.act(args, pageGuard));
      }
      if (request.tool === "openfox_browser_files") {
        if (args.action === "list")
          return result({ files: files.list(tab.id) });
        if (args.action === "get")
          return result(files.get(tab.id, args.file_id));
        const paths = await files.uploads(request.files, pageGuard);
        return result(await control.upload(args, paths, pageGuard));
      }
      throw failure("INVALID_REQUEST");
    };
    // A stopped/hung page may leave a CDP promise unresolved. Release the
    // protocol call immediately; each subsequent command still checks guard.
    let onAbort;
    const cancellation = new Promise((_, reject) => {
      onAbort = () => reject(failure("OUTCOME_UNKNOWN"));
      controller.signal.addEventListener("abort", onAbort, { once: true });
      if (controller.signal.aborted) onAbort();
    });
    try {
      return await Promise.race([work(), cancellation]);
    } finally {
      controller.signal.removeEventListener("abort", onAbort);
      signal.removeEventListener("abort", aborted);
      operations.delete(controller);
    }
  };
  return {
    state,
    takeOver,
    navigationAllowed(url) {
      if (!grant) return true;
      try {
        if (grant.origins.includes(origin(url))) return true;
      } catch {}
      error = "ORIGIN_NOT_ALLOWED";
      announce();
      return false;
    },
    captureDownload(item, tab) {
      if (
        grant &&
        item.getURLChain().some((url) => !this.navigationAllowed(url))
      ) {
        item.cancel();
        return true;
      }
      return Boolean(grant && files?.captureDownload(item, tab));
    },
    layout(bounds, shown) {
      if (!grant || !shown) {
        shield?.hide();
        return;
      }
      shield?.show(bounds);
    },
    async allow(input) {
      if (disposed || pending || !visible()) throw failure("UNAVAILABLE");
      if (
        (input.runId && input.clientMessageId) ||
        (!input.runId && !input.clientMessageId) ||
        (input.runId &&
          (!uuid.test(input.runId) ||
            !Number.isSafeInteger(input.generation) ||
            input.generation < 1)) ||
        (input.clientMessageId && !uuid.test(input.clientMessageId))
      )
        throw failure("INVALID_REQUEST");
      if (
        !Array.isArray(input.origins) ||
        input.origins.length < 1 ||
        input.origins.length > 32
      )
        throw failure("INVALID_REQUEST");
      const origins = [...new Set(input.origins.map(origin))];
      if (origins.includes(null)) throw failure("INVALID_REQUEST");
      clearControl();
      const requestedEpoch = epoch;
      pending = true;
      error = null;
      announce();
      try {
        if (!connection) {
          const info = await connect({
            hostInstanceID: instance,
            hostEpoch: ++bindingEpoch,
            replace: input.replace === true,
          });
          if (disposed || epoch !== requestedEpoch || !visible())
            throw failure("CONTROL_REVOKED");
          files = createBrowserFiles({ root: info.root, changed: announce });
          connection = await connectBrowserBroker({
            socketPath: info.socketPath,
            claim: info.claim,
            execute,
            disconnected() {
              connection = null;
              clearControl();
            },
            revoked(nextEpoch) {
              if (nextEpoch > epoch) clearControl(nextEpoch);
            },
          });
          if (disposed || epoch !== requestedEpoch || !visible()) {
            connection.close();
            throw failure("CONTROL_REVOKED");
          }
        }
        if (!shield) {
          shield = createInputShield({ window, takeOver });
          await shield.ready;
        }
        if (disposed || epoch !== requestedEpoch || !visible())
          throw failure("CONTROL_REVOKED");
        grant = {
          epoch,
          origins,
          run_id: input.runId,
          generation: input.generation,
          client_message_id: input.clientMessageId,
        };
        await connection.grant(grant);
        if (disposed || epoch !== requestedEpoch || !visible())
          throw failure("CONTROL_REVOKED");
      } catch (cause) {
        clearControl();
        connection?.close();
        connection = null;
        error = cause.code || "UNAVAILABLE";
        throw cause;
      } finally {
        pending = false;
        announce();
      }
    },
    async close() {
      if (disposed) return;
      disposed = true;
      clearControl();
      connection?.close();
      connection = null;
      await Promise.allSettled([shield?.close(), files?.close()]);
    },
  };
}

module.exports = { createAgentControl };
