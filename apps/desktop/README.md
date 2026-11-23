# OpenFox Desktop

WP-17 adds the first Electron shell around the shared React workbench. The
renderer receives only the narrow `openfoxDesktop` bridge from `preload.cjs`:
read local daemon/runtime status and request one of the explicit daemon
actions `start`, `stop`, or `restart`.

The main process never accepts a renderer-supplied executable or argument
list. It invokes the fixed `openfox` command, or the absolute path supplied by
`OPENFOX_DESKTOP_OPENFOX_BINARY`, with `shell: false`. Runtime probing is
capability-only; it does not spend a model request or start a Run.

## Web URL

Like HiFox, the desktop app loads the web workbench from a server instead of
bundling it. The URL comes from `OPENFOX_DESKTOP_WEB_URL`, or `web_url` in
`<userData>/desktop-settings.json` (environment variable wins). It must be an
HTTP(S) URL without credentials, query or fragment. The page calls the API on
the same origin (`/api/`), so the static server must proxy `/api/` to
`openfox-server` (see `deploy/nginx-web.example.conf`).

When no URL is configured, the URL is invalid, or the page fails to load, the
window shows a local offline page (`renderer/offline.html`) with a retry
button; retry re-reads the settings file. Navigation initiated by the page is
limited to the web origin. HTTP(S) external links open in the system browser;
the existing named OAuth flows open sandboxed child windows with no preload
bridge, device permissions, or nested popups. Non-web protocols and URLs with
embedded credentials are rejected. IPC is accepted only
from the main window's top frame on the web origin or the local offline page.

On macOS the window uses `titleBarStyle: "hiddenInset"`; the web title bar
leaves room for the traffic lights and is draggable. The bridge exposes
`platform` so the page can adjust.

## Bundled CLI and automatic registration

Like HiFox, the desktop app ships the `openfox` CLI. On start it copies the
bundled binary to `~/.local/bin/openfox` (skipped when the hash matches;
replaced atomically otherwise; a file it did not install is left alone and the
bundled path is used instead) and appends a marked PATH snippet
(`# Added by OpenFox CLI installer`) to existing `~/.zprofile`, `~/.zshrc`,
`~/.bash_profile`, `~/.bashrc` and `~/.profile` (`~/.zprofile` is created if
missing; symlinked profiles are skipped). `OPENFOX_DESKTOP_OPENFOX_BINARY`
still overrides the CLI path.

When an organization is opened and `openfox computer status --json` reports no
registration, the web app issues a PERSONAL install token for the signed-in
user and the main process runs `openfox computer register --install-token -`
(token on stdin), which installs the launchd service. Each organization is
tried automatically once per window; failures, a registration in another
organization, or a registration deleted on the server are shown on the
"This computer" card with retry / reconnect (`--force`).

When running from source, build the CLI first:

```sh
pnpm --filter @openfox/desktop build:cli   # → apps/desktop/resources/bin/openfox
```

## Packaging (macOS)

```sh
OPENFOX_DESKTOP_DEFAULT_WEB_URL=https://openfox.example.com \
  pnpm --filter @openfox/desktop dist:mac   # → apps/desktop/dist/OpenFox-<version>-arm64.dmg
```

`build-defaults.cjs` writes `resources/desktop-defaults.json` with the default
web URL (validated like `web_url`; omitted when the variable is unset), then the
CLI is built for the host architecture and `electron-builder` produces an
arm64 DMG. The URL order is `OPENFOX_DESKTOP_WEB_URL`, then `web_url` in
`desktop-settings.json`, then the packaged default. Local builds use an ad hoc
signature (`identity: "-"`). The CI release uses the same ad hoc app signature and distributes a PKG.
Installation is subject to the Mac’s policy for unidentified developers.

## Desktop experience (stage 12h)

- **Menu bar (macOS)**: the monochrome OpenFox icon opens a menu with the
  main window action, read-only local service status
  (running / stopped / not installed), check for updates, quit. Preference
  "Keep in the menu bar when the window is closed" (Settings > Preferences >
  Desktop app, default off) hides the window instead of quitting. Quitting
  (Cmd+Q or the tray) asks for confirmation only when this computer has running
  tasks; the local service is managed by launchd and keeps running either way.
- **Launch at login**: uses `app.setLoginItemSettings`; the switch always shows
  the value read back from the system and bounces back if the system refuses.
  Hidden in unpackaged development runs and on non-macOS platforms.
- Both settings are local to this computer (`<userData>/desktop-local-prefs.json`)
  and are not synced to the account.
- **Desktop updates on macOS arm64**. The release app checks the HTTPS origin
  embedded during packaging 30 seconds after launch, every 24 hours while running
  and on demand. It downloads the PKG through the workbench proxy and verifies
  SHA-256. Choose **Open macOS Installer** to hand the verified package to the
  system. Finish installation there and reopen OpenFox. **Later** is the default.
  Closing the dialog or quitting leaves the current version installed, including
  for a required update. Local development builds retain manual downloads.
  See [publishing and rollback](../../deploy/README.md#desktop-updates).
- **Bundled CLI after an upgrade**: when the bundled `openfox` replaces the
  installed one, the app restarts the local service only if `daemon status`
  reports it running with `active_runs == 0`; with running tasks the restart is
  deferred (re-checked every 30 s, remembered across launches); if the status is
  unavailable it does not restart and asks the user to restart manually.

## App icon

The development Dock uses `build/icon.png` (1024 px). The packaged app uses
`build/icon.icns`, which contains the same artwork at standard and Retina sizes.
The menu bar uses `renderer/openfoxTemplate.png` and its Retina `@2x` variant
so macOS can apply the current menu bar color.

## Development

```sh
pnpm --filter @openfox/web dev        # http://127.0.0.1:4173, proxies /api
OPENFOX_DESKTOP_WEB_URL=http://127.0.0.1:4173 pnpm --filter @openfox/desktop start
```

The daemon binary can be selected with an absolute
`OPENFOX_DESKTOP_OPENFOX_BINARY` path.

The assistant's **Copy working directory path** action reads the actual
directory recorded for the current Run, then writes it in the desktop main
process. Local reads use `computer workspace-path`; remote reads use the
`workspace_path` device command with a temporary public key. The center retains
only ciphertext and command metadata, and the renderer receives only success
or a reason code. Both conversation access and execution-device ownership are
required. New daemon journal records are needed; old runs are not backfilled.
Browser clients do not offer path copying.

Run the isolated native IPC regression with
`pnpm --filter @openfox/desktop test:work-directory:electron`. It uses temporary
profiles, a loopback fixture and a fake CLI; clipboard writes are captured in
memory so the user's clipboard remains untouched.

This is not yet a task-dispatch client. Center Run creation and device
execution remain behind the existing WP-09/WP-12 server boundaries until an
explicit dispatch API is added.
