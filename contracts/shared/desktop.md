# Desktop and local-management contract

The desktop app uses a thin Electron shell around the shared React workbench.
The browser and desktop renderer use the same `/api/v1` contracts,
cookies, organization scope, task status records, and authorization rules.

## Renderer boundary

- The desktop renderer runs with `contextIsolation: true`,
  `nodeIntegration: false`, and a sandboxed preload.
- The preload exposes only `window.openfoxDesktop` with
  `getLocalStatus()` and `daemonAction("start" | "stop" | "restart")`.
- The renderer cannot supply an executable path, shell command, arbitrary
  arguments, filesystem path, token, or IPC channel.
- The main process invokes the fixed `openfox` executable with `shell: false`.
  `OPENFOX_DESKTOP_OPENFOX_BINARY`, when used, must be an absolute path.

## Local status

`getLocalStatus()` returns two independently reported records:

- `daemon`: the user-level daemon control status, including
  `RECONCILE_REQUIRED` and its safe reason when applicable;
- `runtime`: capability-only Codex detection with executable/version/login
  facts and a `runnable` boolean.

Runtime probing runs `--version`, `exec --help`, and `login status` through the
existing adapter. It does not spend a model request, create a Run, or modify a
repository. An unsupported version or unknown login state is not runnable.

## Boundaries

- The desktop shell does not add a center Run-dispatch endpoint.
- The renderer cannot initiate arbitrary process execution.
- The desktop shell does not change user authorization, device grants, RLS, or
  migration schema.
- Task dispatch, Runtime execution, event delivery, result persistence, and
  cancellation are governed by `execution.md` and `event-delivery.md`.
