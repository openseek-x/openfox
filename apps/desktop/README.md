# OpenFox Desktop

The Electron desktop app wraps the shared React workbench. The
renderer receives only the narrow `openfoxDesktop` bridge from `preload.cjs`:
read local daemon/runtime status and request one of the explicit daemon
actions `start`, `stop`, or `restart`.

The main process never accepts a renderer-supplied executable or argument
list. It invokes the fixed `openfox` command, or the absolute path supplied by
`OPENFOX_DESKTOP_OPENFOX_BINARY`, with `shell: false`. Runtime probing is
capability-only; it does not spend a model request or start a Run.

## Web URL

The desktop app loads the web workbench from a server instead of bundling it.
The URL comes from `OPENFOX_DESKTOP_WEB_URL`, or `web_url` in
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

The desktop app ships the `openfox` CLI. On start it copies the
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
`desktop-settings.json`, then the packaged default. The build is not signed
or notarized (`identity: null`) and uses the default Electron icon; a release
for other people needs a Developer ID certificate, notarization and an icon.

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
execution remain on the server until an explicit dispatch API is added.
