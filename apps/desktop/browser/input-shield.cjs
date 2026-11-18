const { WebContentsView, session } = require("electron");
const { validateBounds } = require("./surface.cjs");

// Electron emits before-mouse-event for CDP-injected clicks too. A separate,
// transparent native view receives real pointer/key input while control is
// granted; automation targets only the page underneath. No timing-based
// exemption can accidentally swallow a simultaneous human click.
function createInputShield({ window, takeOver }) {
  // All shields show the same inert document; reuse one ephemeral session.
  const isolated = session.fromPartition("openfox-browser-input");
  isolated.setPermissionRequestHandler((_contents, _permission, callback) =>
    callback(false),
  );
  isolated.setPermissionCheckHandler(() => false);
  const view = new WebContentsView({
    webPreferences: {
      session: isolated,
      sandbox: true,
      contextIsolation: true,
      nodeIntegration: false,
      nodeIntegrationInWorker: false,
      nodeIntegrationInSubFrames: false,
      javascript: false,
      devTools: false,
      webSecurity: true,
      webviewTag: false,
      focusOnNavigation: false,
    },
  });
  view.setBackgroundColor("#00000000");
  view.setVisible(false);
  window.contentView.addChildView(view);
  const contents = view.webContents;
  let closed = false;
  let active = false;
  let closing;
  contents.setWindowOpenHandler(() => ({ action: "deny" }));
  contents.on("will-navigate", (event) => event.preventDefault());
  contents.on("will-redirect", (event) => event.preventDefault());
  const stop = (event) => {
    if (!active || closed) return;
    event.preventDefault();
    active = false;
    view.setVisible(false);
    takeOver();
  };
  contents.on("before-mouse-event", (event, input) => {
    if (["mouseDown", "mouseWheel", "contextMenu"].includes(input.type))
      stop(event);
  });
  contents.on("before-input-event", (event, input) => {
    if (input.type === "keyDown") stop(event);
  });
  contents.on("render-process-gone", () => {
    if (active) {
      active = false;
      view.setVisible(false);
      takeOver();
    }
  });
  const url =
    "data:text/html;charset=utf-8," +
    encodeURIComponent(
      "<!doctype html><meta http-equiv=\"Content-Security-Policy\" content=\"default-src 'none'; style-src 'unsafe-inline'\"><style>html,body{margin:0;width:100%;height:100%;background:transparent}</style>",
    );
  isolated.webRequest.onBeforeRequest((details, callback) =>
    callback({ cancel: details.url !== url }),
  );
  const ready = contents.loadURL(url);
  // A closing shield may abort its initial navigation.
  ready.catch(() => {});
  const close = () => {
    if (closed) return closing;
    closed = true;
    active = false;
    window.removeListener("closed", close);
    if (!window.isDestroyed()) window.contentView.removeChildView(view);
    closing = contents.isDestroyed()
      ? Promise.resolve()
      : new Promise((resolve) => contents.once("destroyed", resolve));
    if (!contents.isDestroyed()) contents.close({ waitForBeforeUnload: false });
    return closing;
  };
  window.once("closed", close);
  return {
    contents,
    ready,
    show(bounds, focus = false) {
      if (closed || window.isDestroyed()) return;
      view.setBounds(validateBounds(window, bounds));
      // New tabs can have been added above the shield since its last display.
      window.contentView.removeChildView(view);
      window.contentView.addChildView(view);
      active = true;
      view.setVisible(true);
      if (focus) contents.focus();
    },
    hide() {
      active = false;
      if (!closed) view.setVisible(false);
    },
    close,
  };
}

module.exports = { createInputShield };
