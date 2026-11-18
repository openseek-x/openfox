# WP-08 Work-directory contract

This contract defines the local workspace boundary. It does not replace the
center authorization checks or the durable conversation/run binding model.

## Modes

- `TEMP_GIT`: create a system-owned Git worktree under the managed root.
- `SPECIFIED_GIT`: create a system-owned isolated worktree from the configured
  local Git checkout.
- `DIRECT`: use the explicitly configured directory, including a non-Git
  directory when the local guard policy allows it.
- `NO_REPO`: create a system-owned non-Git office directory under the managed
  root.

The actual selected path is canonicalized and recorded in the binding. A
symlink alias is not a second workspace. Directories that are equal, nested,
or otherwise overlap are mutually exclusive across organizations, spaces,
Agents, conversations, and Runs.

## Binding and lifecycle

The first successful preparation locks the conversation binding's mode,
configured path, actual path, repository root, base ref, and branch. Later
Agent default changes affect only new bindings; they do not migrate an
existing conversation. A bound path remains reserved until its binding is
explicitly unbound. Active preparation also holds a process-local lease.

The in-memory registry in WP-08 is an implementation seam for later durable
conversation/run persistence. It must not be described as restart recovery.

## Git rules

- Empty base ref means the literal `HEAD`; it does not fetch automatically.
- An explicit base ref may try `fetch origin <ref>`. If fetching fails, an
  already available local ref may be used and the diagnostic is retained.
- Worktree creation and result commits use argv-based Git execution with
  bounded output and cancellation; no shell interpolation is allowed.
- Only system-owned isolated workspaces may use the explicit result-commit
  operation. Direct workspaces never auto-stash, reset, clean, or commit user
  changes. No mode auto-pushes, merges, or publishes.
- Preparation failure returns a diagnostic and never silently falls back to
  direct source-directory execution.

## Path guards

Callers provide allowed and protected roots. The manager resolves existing
symlinks and the nearest existing parent for new paths, checks roots before
preparation, and rechecks the final path before returning it. Protected roots,
root escapes, NUL-containing paths, and overlong paths are rejected. This is
not a same-UID sandbox and cannot remove all TOCTOU races against a malicious
local process.

## Deferred persistence

WP-08 adds no PostgreSQL migration and no SQLite driver/schema. The durable
`working_directory_bindings` record is specified by the conversation/run
model and will be implemented with its owning persistence work. Until then,
callers must inject a durable registry before claiming restart recovery.
