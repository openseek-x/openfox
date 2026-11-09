# Run reconciliation contract

Reconciliation handles facts that the center cannot infer from a lost
connection. A Run's `reconciliation_required` flag is a safety stop, not a
terminal state.
Unknown process state must not be changed to success, failure, or retry merely
because a lease or heartbeat expired.

## Typed facts

- The daemon persists a typed manifest containing organization, Run,
  generation, authenticated device generation, center epoch, supervisor nonce,
  PID/start time, workdir binding reference, event watermark, and result
  reference. The manifest contains no credentials.
- The center stores the latest supervisor fact for `(organization, run,
generation)` and keeps a durable reconciliation case for each incident.
- Evidence is typed as `PROCESS_STARTED`, `PROCESS_RUNNING`,
  `PROCESS_EXITED`, `PROCESS_NOT_FOUND`, `RESULT_CONFIRMED`, `HEARTBEAT`, or
  `REVOCATION`. Evidence is idempotent by its stable ID and payload hash.
- An old Run generation, center epoch, device generation, supervisor nonce,
  PID, or process start time is stale evidence. It may be retained for audit,
  but it cannot mutate the current Run. Device fact/evidence writes also
  re-check the current authenticated device generation and center epoch.

## Resolution

Only an explicit, versioned resolution can close an open case:

- `PROCESS_EXITED_FAILED` or `PROCESS_NOT_STARTED_CANCELED` requires accepted
  exit evidence. It then terminalizes the active Run and releases capacity in
  the same transaction.
- `RESULT_ACCEPTED` requires accepted result confirmation and only clears the
  safety stop; final result ingestion remains subject to the generation and
  authorization checks in `event-delivery.md`.
- `MANUAL_REVIEW` records an operator decision without releasing an active Run.
- `REJECTED` records a rejected case and never releases capacity.

Terminal Runs cannot be revived, and a retry always creates a new Run with
`retry_of`. Cancellation is idempotent; if completion evidence wins the race,
the accepted result remains authoritative.

## Revocation and tenant boundary

Grant/key/device revocation fails closed for new claims, renewals, and business
reads. A still-authenticated device may submit only the minimum termination
fact for an already claimed Run. A fully revoked device credential cannot
submit a fact; the Run remains pending reconciliation for an authorized
operator. Every PostgreSQL table is organization-scoped and force-RLS
protected, and user resolution requires organization Owner/Admin authority
plus the existing Run object visibility check.

## Local journal

The local journal uses `runs/<organization>/<run>/<generation>/manifest.json`
and append-only `journal.jsonl` with private file permissions and atomic
manifest replacement. Corruption or a missing manifest is a diagnostic and
fail-closed condition; the daemon must not delete its state and blindly accept
new work.
