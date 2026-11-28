const { session, dialog, shell } = require("electron");
const { createHash, randomUUID } = require("node:crypto");
const path = require("node:path");
const { createBrowserSurface, validateBounds } = require("./surface.cjs");
const { createBrowserNetwork } = require("./network.cjs");
const { failure, normalizeURL } = require("./policy.cjs");
const { createAnnotations } = require("./annotations.cjs");
const { createAgentControl } = require("./agent-control.cjs");

const MAX_TABS = 8;
const MAX_TOTAL_TABS = 24;
const MAX_DOWNLOAD_BYTES = 20 * 1024 * 1024;
const uuid = /^[0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12}$/;

// Owns website contents only. Authorization and all renderer IPC remain in the
// trusted main window. Agent access is a separate authenticated local socket;
// neither the renderer nor a website receives a raw CDP entry point.
function createBrowserHost({
  window,
  authorize,
  connectControl,
  networkOptions = () => ({}),
}) {
  const groups = new Map();
  let epoch = 0,
    mounted = null,
    disposed = false,
    suspended = false;
  let creation = Promise.resolve();
  const totalTabs = () =>
    [...groups.values()].reduce((sum, group) => sum + group.tabs.size, 0);
  const state = (group) => ({
    lease: group.lease,
    activeTab: group.active,
    control: group.control?.state(),
    annotation: group.annotations?.state(),
    tabs: [...group.tabs.values()].map((tab) => ({
      id: tab.id,
      title: tab.title,
      url: tab.url,
      loading: tab.loading,
      error: tab.error,
      canAllowLocal: tab.canAllowLocal === true,
      canBack:
        !tab.surface.contents.isDestroyed() &&
        tab.surface.contents.navigationHistory.canGoBack(),
      canForward:
        !tab.surface.contents.isDestroyed() &&
        tab.surface.contents.navigationHistory.canGoForward(),
    })),
    downloads: group.downloads.map(
      ({ item: _item, savePath: _savePath, ...value }) => value,
    ),
  });
  const emit = (group) => {
    if (
      !disposed &&
      !group.closing &&
      mounted === group &&
      !window.webContents.isDestroyed()
    )
      window.webContents.send("openfox:browser-state", state(group));
  };
  const show = (group) => {
    if (group.closing || window.isDestroyed()) return;
    for (const tab of group.tabs.values()) {
      const visible =
        mounted === group &&
        !suspended &&
        group.visible &&
        group.bounds &&
        group.active === tab.id &&
        tab.url !== "about:blank" &&
        !tab.error &&
        !group.annotations?.frozen();
      if (visible) {
        try {
          tab.surface.place(group.bounds);
        } catch (error) {
          // A resize may precede React's next measurement. Hide the old bounds
          // until layout catches up instead of throwing from a page event.
          if (error.code !== "INVALID_BOUNDS") throw error;
          tab.surface.show(false);
          continue;
        }
      }
      tab.surface.show(Boolean(visible));
    }
    const active = group.tabs.get(group.active);
    group.annotations?.layout(
      group.bounds,
      Boolean(
        mounted === group &&
        !suspended &&
        group.visible &&
        group.bounds &&
        active &&
        !active.error &&
        active.url !== "about:blank",
      ),
    );
    group.control?.layout(
      group.bounds,
      Boolean(active && active.surface.view.getVisible()),
    );
  };
  const hide = () => {
    if (mounted) {
      mounted.control?.takeOver();
      mounted.visible = false;
      mounted.occluded = false;
      show(mounted);
    }
    mounted = null;
  };
  const setActive = (group, id) => {
    if (!group.tabs.has(id)) throw failure("TARGET_CLOSED");
    group.active = id;
    show(group);
    emit(group);
  };
  const removeTab = async (group, id) => {
    const tab = group.tabs.get(id);
    if (!tab) return;
    group.annotations?.remove(id);
    group.tabs.delete(id);
    if (group.active === id)
      group.active = [...group.tabs.keys()].at(-1) || null;
    await tab.surface.close();
    show(group);
    emit(group);
  };
  const openTab = (
    group,
    url = "about:blank",
    preferences,
    popup = false,
    webContents,
  ) => {
    if (disposed || group.closing) throw failure("UNAVAILABLE");
    url = normalizeURL(url);
    if (group.tabs.size >= MAX_TABS || totalTabs() >= MAX_TOTAL_TABS)
      throw failure("LIMIT_EXCEEDED");
    const surface = createBrowserSurface({
      window,
      session: group.session,
      network: group.network,
      preferences,
      webContents,
    });
    const tab = {
      id: randomUUID(),
      surface,
      url,
      title: "",
      loading: url !== "about:blank",
      error: null,
    };
    group.tabs.set(tab.id, tab);
    const contents = surface.contents;
    const restrictNavigation = (event, target) => {
      if (group.control && !group.control.navigationAllowed(target))
        event.preventDefault();
    };
    contents.on("will-navigate", restrictNavigation);
    contents.on("will-redirect", restrictNavigation);
    const update = () => {
      if (contents.isDestroyed() || !group.tabs.has(tab.id)) return;
      tab.url = contents.getURL() || tab.url;
      tab.title = contents.getTitle().slice(0, 512);
      tab.loading = contents.isLoading();
      show(group);
      emit(group);
    };
    for (const event of [
      "page-title-updated",
      "did-navigate",
      "did-navigate-in-page",
      "did-stop-loading",
    ])
      contents.on(event, update);
    contents.on("did-start-loading", () => {
      tab.error = null;
      tab.canAllowLocal = false;
      update();
    });
    contents.on(
      "did-fail-load",
      (_event, code, _description, url, mainFrame) => {
        if (!mainFrame || code === -3) return;
        const blocked = group.network.failureFor(contents.id, url);
        tab.error = blocked
          ? [
              "NETWORK_DENIED",
              "PROTECTED_ENDPOINT",
              "PUBLIC_DNS_FAILED",
            ].includes(blocked.code)
            ? blocked.code
            : "DNS_FAILED"
          : "LOAD_FAILED";
        tab.canAllowLocal = blocked?.allowLocal === true;
        tab.url = url || tab.url;
        tab.loading = false;
        show(group);
        emit(group);
      },
    );
    contents.on("render-process-gone", () => {
      group.control?.takeOver();
      group.annotations?.remove(tab.id);
      tab.error = "PAGE_CRASHED";
      tab.loading = false;
      show(group);
      emit(group);
    });
    contents.on("close", () => {
      void removeTab(group, tab.id);
    });
    contents.once("destroyed", () => {
      void removeTab(group, tab.id);
    });
    contents.on("before-input-event", (event, input) => {
      if (input.type === "keyDown" && group.control?.state().active) {
        event.preventDefault();
        group.control.takeOver();
      }
      if (input.type !== "keyDown" || !(input.meta || input.control)) return;
      const key = input.key.toLowerCase();
      // 支持在网页聚焦时透传侧边栏快捷键（⌘\ / ⌘⇧\）至主窗口
      if (!input.alt && (key === "\\" || input.code === "Backslash")) {
        event.preventDefault();
        window.webContents.send("openfox:browser-toggle-sidebar", {
          lease: group.lease,
          side: input.shift ? "right" : "left",
        });
        return;
      }
      if (!["l", "t", "w", "r"].includes(key)) return;
      event.preventDefault();
      if (key === "l") {
        window.webContents.focus();
        window.webContents.send("openfox:browser-focus-address", {
          lease: group.lease,
        });
      }
      if (key === "t") {
        try {
          openTab(group);
        } catch {}
      }
      if (key === "w") void removeTab(group, tab.id);
      if (key === "r") contents.reload();
    });
    contents.setWindowOpenHandler((details) => {
      if (group.control && !group.control.navigationAllowed(details.url))
        return { action: "deny" };
      try {
        normalizeURL(details.url);
      } catch {
        return { action: "deny" };
      }
      if (group.tabs.size >= MAX_TABS || totalTabs() >= MAX_TOTAL_TABS)
        return { action: "deny" };
      return {
        action: "allow",
        overrideBrowserWindowOptions: {
          webPreferences: {
            session: group.session,
            preload: undefined,
            sandbox: true,
            contextIsolation: true,
            nodeIntegration: false,
            webSecurity: true,
            webviewTag: false,
            allowRunningInsecureContent: false,
            devTools: false,
          },
        },
        createWindow: (options) =>
          openTab(
            group,
            details.url,
            options.webPreferences,
            true,
            options.webContents,
          ).surface.contents,
      };
    });
    setActive(group, tab.id);
    if (!popup) void surface.navigate(url).catch(() => {});
    return tab;
  };
  const attachDownloads = (group) => {
    group.onDownload = (_event, item, contents) => {
      const tab = [...group.tabs.values()].find(
        (tab) => tab.surface.contents === contents,
      );
      if (tab && group.control?.captureDownload(item, tab.id)) return;
      if (
        ![...group.tabs.values()].some(
          (tab) => tab.surface.contents === contents,
        ) ||
        item.getTotalBytes() > MAX_DOWNLOAD_BYTES ||
        group.downloads.filter((file) => file.item).length >= 4
      ) {
        item.cancel();
        return;
      }
      while (group.downloads.length >= 20) {
        const index = group.downloads.findIndex((file) => !file.item);
        if (index < 0) {
          item.cancel();
          return;
        }
        group.downloads.splice(index, 1);
      }
      const filename =
        path.basename(item.getFilename().replaceAll("\\", "/")).slice(0, 200) ||
        "download";
      item.setSaveDialogOptions({ title: filename, defaultPath: filename });
      const file = {
        id: randomUUID(),
        filename,
        state: "progressing",
        received: 0,
        total: item.getTotalBytes(),
        item,
        savePath: null,
      };
      group.downloads.push(file);
      item.on("updated", (_event, status) => {
        file.received = item.getReceivedBytes();
        file.total = item.getTotalBytes();
        if (
          file.received > MAX_DOWNLOAD_BYTES ||
          file.total > MAX_DOWNLOAD_BYTES
        ) {
          file.state = "too_large";
          item.cancel();
        } else file.state = status;
        emit(group);
      });
      item.once("done", (_event, status) => {
        file.state = file.state === "too_large" ? "too_large" : status;
        file.received = item.getReceivedBytes();
        if (status === "completed") file.savePath = item.getSavePath();
        file.item = null;
        emit(group);
      });
      emit(group);
    };
    group.session.on("will-download", group.onDownload);
  };
  const closeGroup = async (group) => {
    group.closing = true;
    await group.control?.close();
    await group.annotations?.close();
    group.session.off("will-download", group.onDownload);
    for (const file of group.downloads) file.item?.cancel();
    const tabs = [...group.tabs.values()];
    group.tabs.clear();
    await Promise.all(tabs.map((tab) => tab.surface.close()));
    await group.network.dispose();
  };
  const revokeHidden = () => mounted?.control?.takeOver();
  window.on("hide", revokeHidden);
  window.on("minimize", revokeHidden);
  return {
    suspend() {
      suspended = true;
      for (const group of groups.values()) {
        group.control?.takeOver();
        show(group);
        emit(group);
      }
    },
    resume() {
      suspended = false;
      // Control stays revoked; resuming never replays input or re-grants.
      if (mounted) {
        show(mounted);
        emit(mounted);
      }
    },
    async mount(input) {
      if (
        disposed ||
        !uuid.test(input?.orgId) ||
        !uuid.test(input?.conversationId)
      )
        throw failure("INVALID_REQUEST");
      const current = ++epoch;
      hide();
      const identity = await authorize(input);
      if (disposed || current !== epoch) throw failure("STALE_TARGET");
      if (!identity?.userId || !identity.center) throw failure("FORBIDDEN");
      const previous = creation;
      let release;
      creation = new Promise((resolve) => {
        release = resolve;
      });
      await previous;
      try {
        if (disposed || current !== epoch) throw failure("STALE_TARGET");
        const scope = [
          identity.center,
          input.orgId,
          identity.userId,
          identity.deviceId || "unregistered",
          input.conversationId,
        ].join("\n");
        let group = groups.get(scope);
        if (!group) {
          // A bounded number of conversations can retain live pages. Closing
          // their tabs releases the slot; login cookies remain partitioned.
          for (const [key, item] of groups)
            if (item.tabs.size === 0) {
              groups.delete(key);
              await closeGroup(item);
            }
          if (groups.size >= MAX_TOTAL_TABS) throw failure("LIMIT_EXCEEDED");
          const partition =
            "persist:openfox-browser-" +
            createHash("sha256").update(scope).digest("hex");
          const browserSession = session.fromPartition(partition);
          const network = await createBrowserNetwork(await networkOptions());
          for (const other of groups.values()) {
            other.network.policy.blockPort(Number(new URL(network.url).port));
            network.policy.blockPort(Number(new URL(other.network.url).port));
          }
          try {
            await network.attach(browserSession);
          } catch (error) {
            await network.dispose();
            throw error;
          }
          group = {
            session: browserSession,
            network,
            scope,
            lease: randomUUID(),
            tabs: new Map(),
            active: null,
            bounds: null,
            visible: false,
            occluded: false,
            downloads: [],
          };
          group.annotations = createAnnotations({
            window,
            active: () => group.tabs.get(group.active),
            changed: () => {
              show(group);
              emit(group);
            },
          });
          if (connectControl)
            group.control = createAgentControl({
              window,
              connect: (control) => connectControl(input, identity, control),
              tabs: () => [...group.tabs.values()],
              open: (url) => openTab(group, url),
              focus: (id) => setActive(group, id),
              closeTab: (id) => removeTab(group, id),
              visible: () => {
                const active = group.tabs.get(group.active);
                return Boolean(
                  mounted === group &&
                  !suspended &&
                  window.isVisible() &&
                  !window.isMinimized() &&
                  group.bounds &&
                  active &&
                  !active.error &&
                  active.url !== "about:blank" &&
                  (group.occluded ||
                    (group.visible && active.surface.view.getVisible())),
                );
              },
              changed: () => {
                show(group);
                emit(group);
              },
            });
          if (disposed || current !== epoch) {
            await network.dispose();
            throw failure("STALE_TARGET");
          }
          groups.set(scope, group);
          attachDownloads(group);
        }
        mounted = group;
        group.lease = randomUUID();
        if (!group.tabs.size) openTab(group);
        return state(group);
      } finally {
        release();
      }
    },
    async command(input) {
      const group = mounted;
      if (disposed || !group || input?.lease !== group.lease)
        throw failure("STALE_TARGET");
      if (
        input.action === "annotate" ||
        input.action.startsWith("annotation-")
      ) {
        group.control?.takeOver();
        if (
          input.action === "annotation-locate" &&
          group.tabs.has(input.live?.tab_id)
        )
          setActive(group, input.live.tab_id);
        await group.annotations.command(input);
        show(group);
        emit(group);
        return state(group);
      }
      if (input.action === "allow-control") {
        group.annotations.release();
        if (!group.control) throw failure("UNAVAILABLE");
        await group.control.allow(input);
        show(group);
        emit(group);
        return state(group);
      }
      if (input.action === "take-over") {
        group.control?.takeOver();
        emit(group);
        return state(group);
      }
      const transientOcclusion =
        input.action === "layout" &&
        input.visible !== true &&
        input.occluded === true &&
        Boolean(group.bounds);
      if (
        !["layout", "reveal-download", "open-external"].includes(
          input.action,
        ) ||
        (input.action === "layout" &&
          input.visible !== true &&
          !transientOcclusion)
      )
        group.control?.takeOver();
      if (input.action === "layout") {
        group.visible = input.visible === true;
        group.occluded = transientOcclusion;
        if (input.bounds) {
          const zoom = window.webContents.getZoomFactor();
          const bounds = Object.fromEntries(
            Object.entries(input.bounds).map(([key, value]) => [
              key,
              value * zoom,
            ]),
          );
          group.bounds = validateBounds(window, bounds);
        }
        show(group);
      } else if (input.action === "hide") {
        group.visible = false;
        group.occluded = false;
        show(group);
      } else if (input.action === "open") openTab(group, input.url);
      else if (input.action === "focus") setActive(group, input.tabId);
      else if (input.action === "close") await removeTab(group, input.tabId);
      else if (input.action === "clear") {
        await Promise.all(
          [...group.tabs.keys()].map((id) => removeTab(group, id)),
        );
        await group.session.clearStorageData();
        await group.session.clearCache();
        if (disposed || mounted !== group || input.lease !== group.lease)
          throw failure("STALE_TARGET");
        openTab(group);
      } else if (input.action === "reveal-download") {
        const file = group.downloads.find(
          (file) => file.id === input.downloadId,
        );
        if (!file?.savePath || file.state !== "completed")
          throw failure("UNAVAILABLE");
        shell.showItemInFolder(file.savePath);
      } else if (input.action === "open-external") {
        // 仅允许在系统默认浏览器中打开当前标签已加载的 http(s) 地址，不信任渲染进程任意传参
        const tab = group.tabs.get(input.tabId || group.active);
        if (!tab || tab.surface.contents.isDestroyed())
          throw failure("TARGET_CLOSED");
        const url = normalizeURL(tab.url);
        if (url === "about:blank") throw failure("INVALID_URL");
        await shell.openExternal(url);
      } else if (input.action === "allow-origin") {
        const url = new URL(normalizeURL(input.url));
        // The trusted policy, not a renderer-supplied URL, decides whether a
        // local grant is meaningful. Reserved/Fake-IP/protected endpoints fail.
        let denied;
        try {
          await group.network.policy.resolve(url.href);
        } catch (error) {
          denied = error;
        }
        if (!denied?.allowLocal) throw denied || failure("INVALID_REQUEST");
        const result = await dialog.showMessageBox(window, {
          type: "question",
          message: `允许此会话访问本地地址 ${url.origin}？`,
          detail: "仅允许这个地址及端口。其他组织和会话仍保持隔离。",
          buttons: ["取消", "允许"],
          defaultId: 0,
          cancelId: 0,
        });
        if (mounted !== group || input.lease !== group.lease)
          throw failure("STALE_TARGET");
        if (result.response === 1) group.network.policy.allowOrigin(url.origin);
      } else {
        const tab = group.tabs.get(input.tabId);
        if (!tab || tab.surface.contents.isDestroyed())
          throw failure("TARGET_CLOSED");
        const contents = tab.surface.contents;
        if (input.action === "navigate") {
          const url = normalizeURL(input.url);
          tab.url = url;
          tab.error = null;
          tab.loading = url !== "about:blank";
          void tab.surface.navigate(url).catch(() => {});
        } else if (input.action === "back") {
          if (contents.navigationHistory.canGoBack())
            contents.navigationHistory.goBack();
        } else if (input.action === "forward") {
          if (contents.navigationHistory.canGoForward())
            contents.navigationHistory.goForward();
        } else if (input.action === "reload") contents.reload();
        else if (input.action === "stop") contents.stop();
        else throw failure("INVALID_REQUEST");
      }
      emit(group);
      return state(group);
    },
    async reset() {
      ++epoch;
      const previous = mounted;
      hide();
      if (previous && !window.webContents.isDestroyed())
        window.webContents.send("openfox:browser-state", {
          ...state(previous),
          reset: true,
          tabs: [],
          activeTab: null,
        });
      const items = [...groups.values()];
      groups.clear();
      await Promise.all(items.map(closeGroup));
    },
    blockPort(port) {
      for (const group of groups.values()) group.network.policy.blockPort(port);
    },
    async dispose() {
      disposed = true;
      window.removeListener("hide", revokeHidden);
      window.removeListener("minimize", revokeHidden);
      await this.reset();
    },
  };
}

module.exports = { createBrowserHost };
