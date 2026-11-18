const { WebContentsView, session, nativeImage } = require("electron");
const { randomUUID, createHash } = require("node:crypto");
const { createPageControl } = require("./page-control.cjs");
const { failure } = require("./policy.cjs");

function createAnnotations({ window, active, changed }) {
  const instance = randomUUID(),
    pages = new Map(),
    regionHashes = new Map();
  let enabled = false,
    mode = "element",
    selection = null,
    frozen = false,
    error = null;
  let overlay = null,
    bounds = null,
    shown = false,
    closed = false,
    start = null,
    serial = 0,
    hovering = false,
    hoverPoint = null,
    hoverEpoch = 0,
    layoutTab = null,
    drawing = 0,
    selecting = false,
    wheelTimer = null,
    css = null;
  const state = () => ({
    enabled,
    mode,
    selection: selection?.tabId === active()?.id ? selection : null,
    frozen,
    error,
  });
  const notify = () => changed();
  const guard = () => {
    if (closed || !active() || active().surface.contents.isDestroyed())
      throw failure("TARGET_CLOSED");
  };
  const page = (tab) => {
    if (!pages.has(tab.id)) {
      const control = createPageControl(tab.surface.contents);
      // A page's JS dialog can pause CDP reads. Detach on timeout so a stuck
      // selection cannot retain pending reads or block subsequent control.
      const timed =
        (operation) =>
        async (...args) => {
          let timer;
          try {
            return await Promise.race([
              operation(...args),
              new Promise((_, reject) => {
                timer = setTimeout(() => {
                  control.close();
                  if (pages.get(tab.id)?.close === control.close)
                    pages.delete(tab.id);
                  reject(failure("TIMEOUT"));
                }, 10000);
              }),
            ]);
          } finally {
            clearTimeout(timer);
          }
        };
      pages.set(tab.id, {
        ...control,
        screenshot: timed(control.screenshot),
        annotations: {
          select: timed(control.annotations.select),
          locate: timed(control.annotations.locate),
        },
      });
    }
    return pages.get(tab.id);
  };
  const draw = async (rect) => {
    const current = ++drawing,
      target = overlay?.webContents;
    if (!target || target.isDestroyed()) return;
    try {
      const next = await target.insertCSS(
        rect
          ? `html #highlight{left:${rect.x}px;top:${rect.y}px;width:${rect.width}px;height:${rect.height}px;display:block}`
          : "html #highlight{display:none}",
      );
      // A leave/cancel can overtake an in-flight CSS insertion.
      if (current !== drawing || target.isDestroyed()) {
        if (!target.isDestroyed()) await target.removeInsertedCSS(next);
        return;
      }
      const previous = css;
      css = next;
      if (previous) await target.removeInsertedCSS(previous);
    } catch {
      // Closing an overlay aborts pending style operations.
    }
  };
  const clearHover = () => {
    hoverPoint = null;
    ++hoverEpoch;
    void draw(null);
  };
  const hover = async () => {
    if (hovering) return;
    hovering = true;
    try {
      // Keep only the latest position while a DOM read is in flight. Dropping
      // those events leaves the outline on an old element when the mouse stops.
      while (hoverPoint) {
        const point = hoverPoint,
          tab = active(),
          current = serial,
          epoch = hoverEpoch;
        hoverPoint = null;
        const valid = () =>
          !closed &&
          shown &&
          enabled &&
          mode === "element" &&
          current === serial &&
          epoch === hoverEpoch &&
          active()?.id === tab?.id &&
          !start &&
          selection?.tabId !== tab?.id &&
          !selecting;
        if (!tab || !valid()) continue;
        const hoverGuard = () => {
          guard();
          if (!valid()) throw failure("STALE_TARGET");
        };
        let rect = null;
        try {
          const anchor = await page(tab).annotations.select(
            "element",
            point,
            point,
            hoverGuard,
            false,
          );
          rect = anchor.visual.rect;
        } catch {
          /* Unselectable targets have no outline. */
        }
        if (valid()) await draw(rect);
      }
    } finally {
      hovering = false;
    }
  };
  const hash = (value) => createHash("sha256").update(value).digest("hex");
  const crop = (url, visual) => {
    const bitmap = nativeImage.createFromDataURL(url),
      size = bitmap.getSize(),
      r = visual.rect,
      v = visual;
    const x = Math.floor((r.x * size.width) / v.width),
      y = Math.floor((r.y * size.height) / v.height);
    const cropped = bitmap
      .crop({
        x,
        y,
        width: Math.min(
          size.width - x,
          Math.max(1, Math.floor((r.width * size.width) / v.width)),
        ),
        height: Math.min(
          size.height - y,
          Math.max(1, Math.floor((r.height * size.height) / v.height)),
        ),
      })
      .toPNG();
    if (cropped.length > 4 * 1024 * 1024) throw failure("LIMIT_EXCEEDED");
    return `data:image/png;base64,${cropped.toString("base64")}`;
  };
  const select = async (first, last) => {
    const current = ++serial,
      tab = active();
    if (!tab) return;
    selecting = true;
    clearHover();
    const selectedMode = mode;
    try {
      const control = page(tab);
      const anchor = await control.annotations.select(
        selectedMode,
        first,
        last,
        guard,
      );
      const url = new URL(tab.surface.contents.getURL());
      url.search = "";
      url.hash = "";
      url.username = "";
      url.password = "";
      const next = {
        id: randomUUID(),
        tabId: tab.id,
        source_url: url.href,
        title: tab.title.slice(0, 256),
        captured_at: new Date().toISOString(),
        anchor: {
          ...anchor,
          live: { ...anchor.live, host_instance_id: instance, tab_id: tab.id },
        },
      };
      try {
        const image = await control.screenshot(guard);
        next.image = `data:${image.mime_type};base64,${image.data}`;
        if (selectedMode === "region") {
          next.screenshot = crop(next.image, anchor.visual);
          if (regionHashes.size >= 1000)
            regionHashes.delete(regionHashes.keys().next().value);
          regionHashes.set(anchor.live.ref, hash(next.screenshot));
        }
      } catch (e) {
        if (selectedMode === "region") throw e;
      }
      await control.annotations.locate(anchor.live, guard, false);
      if (
        current !== serial ||
        active()?.id !== tab.id ||
        !enabled ||
        selectedMode !== mode
      )
        return;
      selection = next;
      frozen = false;
      error = null;
      await draw(anchor.visual.rect);
      notify();
    } catch {
      if (current === serial) {
        error = "selection";
        notify();
      }
    } finally {
      selecting = false;
    }
  };
  const ensureOverlay = () => {
    if (overlay) return;
    const isolated = session.fromPartition("openfox-browser-annotations");
    isolated.setPermissionRequestHandler((_c, _p, callback) => callback(false));
    isolated.setPermissionCheckHandler(() => false);
    overlay = new WebContentsView({
      webPreferences: {
        session: isolated,
        sandbox: true,
        contextIsolation: true,
        nodeIntegration: false,
        nodeIntegrationInWorker: false,
        nodeIntegrationInSubFrames: false,
        preload: require("node:path").join(__dirname, "annotation-preload.cjs"),
        devTools: false,
        webSecurity: true,
        webviewTag: false,
        focusOnNavigation: false,
      },
    });
    overlay.setBackgroundColor("#00000000");
    overlay.setVisible(false);
    window.contentView.addChildView(overlay);
    const contents = overlay.webContents;
    contents.setWindowOpenHandler(() => ({ action: "deny" }));
    contents.on("will-navigate", (event) => event.preventDefault());
    contents.on("will-redirect", (event) => event.preventDefault());
    contents.on("render-process-gone", () => {
      enabled = false;
      frozen = false;
      notify();
    });
    contents.on("before-input-event", (event, input) => {
      event.preventDefault();
      if (input.type === "keyDown" && input.key === "Escape") {
        start = null;
        ++serial;
        clearHover();
      }
    });
    contents.ipc.on("openfox:annotation-wheel", (_event, input) => {
      if (!shown || !enabled || selection?.tabId === active()?.id || selecting)
        return;
      if (
        !input ||
        !["x", "y", "deltaX", "deltaY"].every((key) =>
          Number.isFinite(input[key]),
        )
      )
        return;
      if (
        !bounds ||
        input.x < 0 ||
        input.y < 0 ||
        input.x >= bounds.width ||
        input.y >= bounds.height
      )
        return;
      const tab = active(),
        current = serial,
        point = { x: input.x, y: input.y };
      if (!tab || tab.surface.contents.isDestroyed()) return;
      start = null;
      clearHover();
      // Wheel is not emitted by Electron's before-mouse-event. Only our isolated
      // overlay preload can use this contents-scoped channel; websites cannot.
      tab.surface.contents.sendInputEvent({
        type: "mouseWheel",
        x: Math.round(point.x),
        y: Math.round(point.y),
        deltaX: -Math.max(-2000, Math.min(2000, input.deltaX)),
        deltaY: -Math.max(-2000, Math.min(2000, input.deltaY)),
        hasPreciseScrollingDeltas: true,
        canScroll: true,
      });
      clearTimeout(wheelTimer);
      wheelTimer = setTimeout(() => {
        if (
          closed ||
          serial !== current ||
          active()?.id !== tab.id ||
          !enabled ||
          !shown
        )
          return;
        hoverPoint = point;
        void hover();
      }, 80);
    });
    contents.on("before-mouse-event", (event, input) => {
      event.preventDefault();
      // A newer pointer event owns the outline; do not replay the wheel position.
      clearTimeout(wheelTimer);
      if (!shown || !enabled || selection?.tabId === active()?.id || selecting)
        return;
      if (input.type === "mouseLeave") {
        start = null;
        clearHover();
        return;
      }
      const point = { x: input.x, y: input.y };
      if (input.type === "mouseDown" && input.button === "left") {
        start = point;
        clearHover();
        return;
      }
      if (input.type === "mouseUp" && input.button === "left" && start) {
        const first = start;
        start = null;
        void select(first, point);
        return;
      }
      if (input.type !== "mouseMove") return;
      if (start) {
        if (mode === "element") return;
        void draw({
          x: Math.min(start.x, point.x),
          y: Math.min(start.y, point.y),
          width: Math.abs(start.x - point.x),
          height: Math.abs(start.y - point.y),
        });
        return;
      }
      if (mode !== "element") return;
      hoverPoint = point;
      void hover();
    });
    const url =
      "data:text/html;charset=utf-8," +
      encodeURIComponent(
        '<!doctype html><meta http-equiv="Content-Security-Policy" content="default-src \'none\'; style-src \'unsafe-inline\'"><style>html,body{margin:0;width:100%;height:100%;background:transparent;cursor:crosshair}#highlight{position:fixed;pointer-events:none;box-sizing:border-box;border:2px solid #4f8cff;background:#4f8cff22;display:none}</style><div id="highlight"></div>',
      );
    isolated.webRequest.onBeforeRequest((details, callback) =>
      callback({ cancel: details.url !== url }),
    );
    void contents.loadURL(url).catch(() => {});
  };
  return {
    state,
    frozen: () => frozen && selection?.tabId === active()?.id,
    layout(next, visible) {
      const tabId = active()?.id;
      if (
        layoutTab !== tabId ||
        !visible ||
        (bounds &&
          next &&
          ["x", "y", "width", "height"].some(
            (key) => bounds[key] !== next[key],
          ))
      ) {
        ++serial;
        start = null;
        clearHover();
      }
      layoutTab = tabId;
      bounds = next;
      shown = Boolean(
        enabled && visible && !(frozen && selection?.tabId === active()?.id),
      );
      if (shown) {
        ensureOverlay();
        overlay.setBounds(bounds);
        window.contentView.removeChildView(overlay);
        window.contentView.addChildView(overlay);
        overlay.setVisible(true);
      } else overlay?.setVisible(false);
    },
    async command(input) {
      if (input.action === "annotate") {
        if (!["element", "text", "region"].includes(input.mode))
          throw failure("INVALID_REQUEST");
        start = null;
        clearHover();
        ++serial;
        enabled = input.enabled === true;
        mode = input.mode;
        error = null;
        if (!enabled) {
          selection = null;
          frozen = false;
        }
      } else if (input.action === "annotation-ready") {
        if (selection?.id !== input.selectionId || !selection.image)
          throw failure("STALE_TARGET");
        frozen = true;
      } else if (input.action === "annotation-cancel") {
        selection = null;
        frozen = false;
        error = null;
        ++serial;
        start = null;
        clearHover();
      } else if (input.action === "annotation-locate") {
        ++serial;
        clearHover();
        const live = input.live,
          tab = active();
        if (
          !live ||
          live.host_instance_id !== instance ||
          live.tab_id !== tab?.id ||
          !pages.has(tab.id)
        ) {
          error = "stale";
          notify();
          return;
        }
        try {
          const value = await pages.get(tab.id).annotations.locate(live, guard);
          if (value.kind === "region") {
            const image = await pages.get(tab.id).screenshot(guard);
            if (
              hash(
                crop(
                  `data:${image.mime_type};base64,${image.data}`,
                  value.visual,
                ),
              ) !== regionHashes.get(live.ref)
            )
              throw failure("STALE_TARGET");
          }
          enabled = true;
          selection = null;
          frozen = false;
          ensureOverlay();
          await draw(value.visual.rect);
          error = null;
        } catch {
          error = "stale";
        }
      } else throw failure("INVALID_REQUEST");
      notify();
    },
    release() {
      start = null;
      clearHover();
      enabled = false;
      frozen = false;
      selection = null;
      ++serial;
      for (const control of pages.values()) control.close();
      pages.clear();
      regionHashes.clear();
      overlay?.setVisible(false);
    },
    remove(id) {
      start = null;
      clearHover();
      pages.get(id)?.close();
      pages.delete(id);
      if (selection?.tabId === id) {
        selection = null;
        frozen = false;
      }
      ++serial;
    },
    async close() {
      clearTimeout(wheelTimer);
      closed = true;
      this.release();
      if (overlay) {
        if (!window.isDestroyed()) window.contentView.removeChildView(overlay);
        const c = overlay.webContents;
        if (!c.isDestroyed()) {
          const done = new Promise((resolve) => c.once("destroyed", resolve));
          c.close({ waitForBeforeUnload: false });
          await done;
        }
      }
    },
  };
}
module.exports = { createAnnotations };
