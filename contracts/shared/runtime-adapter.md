# Runtime adapter contract

This contract is the local execution boundary between the daemon/supervisor and
an installed Runtime. It is intentionally independent of HTTP, PostgreSQL,
task state, and the center's Run persistence. The work-directory, execution,
and event-delivery contracts compose this boundary with directories,
scheduling, event persistence, and result sharing.

## Capability report

An adapter must report, at minimum:

- Runtime name, resolved executable, and observed version;
- login state as `LOGGED_IN`, `LOGGED_OUT`, or `UNKNOWN`;
- whether JSON events, cancellation, resume, and supported directory modes are
  available;
- a bounded diagnostic reason when it is not runnable.

`UNKNOWN` login state and an unlisted Runtime version are not runnable. A
successful version/help probe is not proof that a Runtime is ready to execute.
The Codex adapter therefore requires an explicit supported-version matrix; a
capability probe alone does not make an unlisted build a release baseline.

The Codex adapter can also list the models available to the logged-in account
(`codex app-server` `model/list`, no model request). The daemon reports them in
its heartbeat capabilities, refreshed at startup and every 10 minutes:

```json
{
  "runtimes": {
    "codex": {
      "version": "…",
      "login_status": "LOGGED_IN",
      "runnable": true,
      "reason": "",
      "models": [{ "id": "…", "display_name": "…", "is_default": true }],
      "models_error": "",
      "checked_at": "…"
    }
  }
}
```

`models` is bounded to 100 visible entries with fields of at most 128
characters. When a refresh fails, the last successful list is kept and
`models_error` explains why. The center stores capabilities as opaque JSON
(64 KiB limit); agent configuration selects `models[].id`. The list may be
stale, so it is advisory: an unknown model fails the Run at execution time.

## Run input

The adapter accepts a bounded prompt, optional long-lived instructions, an
optional model identifier (empty or `default` means the Runtime's own default), an already-resolved absolute working directory, a
finite run timeout, and an explicit environment allowlist. It starts argv
directly; it never passes a caller-provided shell command or arbitrary CLI flag
list to a shell.

The Codex adapter runs each Run with an isolated `CODEX_HOME` that contains only
a 0600 copy of the user's `auth.json` and an adapter-generated `config.toml`;
the user's personal configuration, instructions, skills, and MCP servers are
not inherited. Refreshed credentials are written back atomically only when the
source file is unchanged, and the directory is removed after the process exits.
Every turn explicitly requests approval policy `never` and a `workspaceWrite`
sandbox whose only writable root is the working directory, with network access
enabled. Center sessions, device credentials, and organization secrets must not be
placed in the prompt. Environment values are never included in diagnostics;
known secret-like values are redacted from returned text as defense in depth.

## Events and result

Runtime stdout is newline-delimited JSON (for Codex, `app-server` JSON-RPC over
stdio) and is decoded into bounded normalized events. Each event receives a local sequence number. A malformed line,
critical unknown event, line/output/event limit, or unsupported approval request
must fail the execution; it must not become a successful result.

The final result separates:

- process exit evidence and exit code;
- normalized output and event count;
- `SUCCEEDED`, `FAILED`, `CANCELLED`, or `TIMED_OUT` status;
- a safe error code/message;
- recovery status.

Model text claiming that a check passed is not execution evidence. Tool events,
exit status, and persisted event/result checks are the evidence boundary.

## Cancellation and recovery

Cancellation is idempotent. The Codex adapter first requests `turn/interrupt`
and allows the Runtime 5 seconds to stop; the supervisor then requests
process-group termination. It returns `CANCELLED` only after process exit is observed. A timeout is
reported separately and also requires exit evidence. If the process cannot be
confirmed stopped, callers must retain reconciliation state and must not free a
directory or start a replacement run.

The adapter does not claim session resume or exactly-once external side effects.
Adapters report resume as unsupported until a concrete versioned protocol is
validated. Unknown execution facts require reconciliation before retry; a
retry creates a new Run under the execution and event-delivery contracts.

## Implementation

- `internal/runtime`: generic request, capability, event, result, and process
  lifecycle contract;
- `internal/runtime/codex`: discovery, capability probe, `codex app-server`
  session, isolated `CODEX_HOME`, and model listing;
- tests use a deterministic local fake app-server for process and protocol
  faults. The installed Codex probe performs only `--version`,
  `app-server --help`, and `login status`; it does not spend a model request or
  modify a repository. Tests against the real Codex are opt-in
  (`OFD_REAL_CODEX=1`).

## Permission modes

`RunRequest.ApprovalMode` carries one of four modes (`FOLLOW_RUNTIME`,
`READ_ONLY`, `WORKSPACE_WRITE`, `FULL_ACCESS`; empty = `FOLLOW_RUNTIME`).
`FOLLOW_RUNTIME` is exactly the behaviour each adapter had before the mode
existed and its output must stay byte-identical. An adapter that does not
support a mode must fail in `Start` with `ErrUnsupported` before probing or
spawning a process; it must never fall back to a wider behaviour. Adapters
advertise what they support in the heartbeat
(`capabilities.runtimes.<provider>.approval_modes`, always containing
`FOLLOW_RUNTIME`); a heartbeat without the field (old daemon) means
`FOLLOW_RUNTIME` only, and the center refuses to dispatch any other mode.

| Mode              | Codex (app-server)                                                                                                  | Claude Code (`-p`)                                                                                                                                 | OpenCode (`opencode run`)                                                | pi                  |
| ----------------- | ------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------ | ------------------- |
| `FOLLOW_RUNTIME`  | `sandbox: workspace-write`, `sandboxPolicy.type: workspaceWrite` (writable root = workdir), `approvalPolicy: never` | `--permission-mode acceptEdits`, fixed `--allowedTools`                                                                                            | `OPENCODE_PERMISSION={"edit":"allow","bash":"allow","webfetch":"allow"}` | no permission flags |
| `READ_ONLY`       | `sandbox: read-only`, `sandboxPolicy.type: readOnly`                                                                | `--permission-mode plan`, `--allowedTools Read,Glob,Grep,WebFetch,WebSearch,TodoWrite`, `--disallowedTools Bash,Edit,Write,MultiEdit,NotebookEdit` | `edit` and `bash` deny, `webfetch` allow, `external_directory` deny      | unsupported         |
| `WORKSPACE_WRITE` | same as `FOLLOW_RUNTIME`                                                                                            | same as `FOLLOW_RUNTIME` (`acceptEdits`)                                                                                                           | `edit`/`bash`/`webfetch` allow, `external_directory` deny                | unsupported         |
| `FULL_ACCESS`     | `sandbox: danger-full-access`, `sandboxPolicy.type: dangerFullAccess`                                               | `--permission-mode bypassPermissions`                                                                                                              | everything allow incl. `external_directory`                              | unsupported         |

Codex keeps `approvalPolicy: never` in every mode: interactive approval is not
supported and an approval request still fails the run. The per-run
`config.toml` fallback `sandbox_mode` matches the requested mode.

Sources (checked 2026-11): Codex app-server protocol (`thread/start.sandbox`
= `read-only | workspace-write | danger-full-access`, `turn/start.sandboxPolicy`
= `readOnly | workspaceWrite | dangerFullAccess`; github.com/openai/codex
`codex-rs/app-server-protocol`); Claude Code CLI reference
(code.claude.com/docs/en/cli-reference, `--permission-mode`
`default|acceptEdits|plan|auto|dontAsk|bypassPermissions|manual`,
`--allowedTools`, `--disallowedTools`) and permission modes
(code.claude.com/docs/en/permission-modes: plan mode keeps its blocks in
`-p` non-interactive runs); OpenCode permissions (opencode.ai/docs/agents,
Permissions: keys `edit`, `bash`, `webfetch`, `external_directory`, values
`allow | ask | deny`). Known limits: Claude Code has no filesystem sandbox
flag, so `WORKSPACE_WRITE` equals `FOLLOW_RUNTIME` there; OpenCode has no
directory-level bash sandbox, `external_directory` only gates file access
outside the project worktree.

## Collaboration modes

`RunRequest.CollaborationMode` carries one of three modes (`FOLLOW_RUNTIME`,
`DEFAULT`, `PLAN`). Empty and the legacy value `FOLLOW_AGENT` normalize to
`FOLLOW_RUNTIME`; any other value is `ErrInvalidRequest`. `FOLLOW_RUNTIME`
passes nothing and keeps the adapter output byte-identical. `PLAN` means
"make a plan, do not change the workspace" and overrides every approval mode
(including `FULL_ACCESS`) without an error; `DEFAULT` is the normal mode and
never widens the approval mode (with `READ_ONLY` it stays read-only).
Adapters advertise support in
`capabilities.runtimes.<provider>.collaboration_modes` (always containing
`FOLLOW_RUNTIME`). A heartbeat without the field means `FOLLOW_RUNTIME` only:
the center fails the run with `RUNTIME_UNAVAILABLE` instead of dispatching,
and the daemon checks again before starting (`ErrUnsupported`, no process
spawned). There is no per-conversation override.

| Mode             | Codex (app-server) | Claude Code (`-p`)                                                                                                                      | OpenCode (`opencode run`) | pi          |
| ---------------- | ------------------ | --------------------------------------------------------------------------------------------------------------------------------------- | ------------------------- | ----------- |
| `FOLLOW_RUNTIME` | nothing            | nothing (approval-mode flags only)                                                                                                      | nothing                   | nothing     |
| `DEFAULT`        | unsupported        | same flags as the approval mode (already an explicit `--permission-mode`)                                                               | unsupported               | unsupported |
| `PLAN`           | unsupported        | the `READ_ONLY` flags: `--permission-mode plan`, read-only `--allowedTools`, `--disallowedTools Bash,Edit,Write,MultiEdit,NotebookEdit` | unsupported               | unsupported |

Why the rest are unsupported: the Codex app-server `thread/start` /
`turn/start` parameters used here have no collaboration or plan field
(`turn/plan/updated` is only a notification); OpenCode's `--agent plan` is
not verified against the installed CLI in this repository; pi has no known
plan flag. They report `["FOLLOW_RUNTIME"]` until a parameter is verified.
