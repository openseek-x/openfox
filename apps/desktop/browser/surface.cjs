const { WebContentsView } = require("electron");
const { normalizeURL, failure } = require("./policy.cjs");

function validateBounds(window, bounds) {
  const [width, height] = window.getContentSize();
  const { x, y, width: w, height: h } = bounds || {};
  if (
    ![x, y, w, h].every(Number.isFinite) ||
    x < 0 ||
    y < 0 ||
    w < 1 ||
    h < 1 ||
    x + w > width + 0.5 ||
    y + h > height + 0.5
  )
    throw failure("INVALID_BOUNDS");
  const left = Math.round(x),
    top = Math.round(y);
  return {
    x: left,
    y: top,
    width: Math.min(width, Math.round(x + w)) - left,
    height: Math.min(height, Math.round(y + h)) - top,
  };
}

// Only the trusted main process creates a surface. A web page never receives
// preload, IPC, Node, or an exposed remote-debugging endpoint.
function createBrowserSurface({
  window,
  session,
  network,
  preferences = {},
  webContents,
}) {
  if (webContents && webContents.session !== session)
    throw failure("FORBIDDEN");
  const view = new WebContentsView({
    ...(webContents ? { webContents } : {}),
    webPreferences: {
      ...preferences,
      session,
      preload: undefined,
      nodeIntegration: false,
      nodeIntegrationInWorker: false,
      nodeIntegrationInSubFrames: false,
      contextIsolation: true,
      sandbox: true,
      webSecurity: true,
      allowRunningInsecureContent: false,
      webviewTag: false,
      devTools: false,
      focusOnNavigation: false,
      safeDialogs: true,
    },
  });
  const contents = view.webContents;
  let closed = false;
  let closing;
  let generation = 0;
  const unbind = network.bind(contents);
  session.setPermissionRequestHandler((_contents, _permission, callback) =>
    callback(false),
  );
  session.setPermissionCheckHandler(() => false);
  session.setDevicePermissionHandler(() => false);
  session.setDisplayMediaRequestHandler((_request, callback) => callback({}));
  const guard = (event, url) => {
    try {
      normalizeURL(url);
    } catch {
      event.preventDefault();
    }
  };
  contents.on("will-navigate", guard);
  contents.on("will-redirect", guard);
  contents.setWindowOpenHandler(() => ({ action: "deny" }));
  contents.on("did-navigate", () => generation++);
  view.setVisible(false);
  window.contentView.addChildView(view);
  const close = () => {
    if (closed) return closing;
    closed = true;
    closing = contents.isDestroyed()
      ? Promise.resolve()
      : new Promise((resolve) => contents.once("destroyed", resolve));
    unbind();
    window.removeListener("closed", close);
    if (!window.isDestroyed()) window.contentView.removeChildView(view);
    if (!contents.isDestroyed()) contents.close({ waitForBeforeUnload: false });
    return closing;
  };
  window.once("closed", close);
  return {
    view,
    contents,
    get generation() {
      return generation;
    },
    place(bounds) {
      if (closed) throw failure("TARGET_CLOSED");
      view.setBounds(validateBounds(window, bounds));
    },
    show(visible) {
      if (closed) throw failure("TARGET_CLOSED");
      view.setVisible(Boolean(visible));
    },
    async navigate(url) {
      if (closed) throw failure("TARGET_CLOSED");
      await contents.loadURL(normalizeURL(url));
    },
    async capture() {
      if (closed || !view.getVisible()) throw failure("UNAVAILABLE");
      const before = generation;
      const image = await contents.capturePage();
      if (closed || generation !== before) throw failure("STALE_TARGET");
      if (image.isEmpty()) throw failure("CAPTURE_FAILED");
      return { image, generation: before };
    },
    close,
  };
}

module.exports = { createBrowserSurface, validateBounds };
