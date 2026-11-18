# Shared contracts

These files are the integration boundary for WP-02, WP-03, WP-04, WP-05, WP-06,
WP-09, WP-10, WP-11, WP-12, WP-13, WP-14, and WP-17.
Agents must update the contract first when implementation needs a semantic
change. A code-only change must not silently change these rules.

- `tenant-context.md`: trusted transaction-local tenant isolation.
- `capacity-protocol.md`: reservation, offer, claim, release, and reap rules.
- `error-schema.md`: safe API error shape and status semantics.
- `membership-invitations.md`: member roles, invitation lifecycle, and
  last-owner protection.
- `device-auth.md`: Ed25519 challenge-response, device sessions, and grant
  authorization.
- `api-authz.md`: WP-02/WP-03 route shapes and cross-cutting authorization
  requirements.
- `collaboration.md`: WP-04 space, workflow, task, and activity rules.
- `device-management.md`: WP-05 physical device ownership, registration, and
  grant management rules.
- `agent-management.md`: WP-06 Agent visibility, immutable configuration
  versions, device/grant binding, and bounded execution policy.
- `runtime-adapter.md`: WP-07 Runtime capability, bounded JSONL execution,
  cancellation, redaction, and recovery boundaries.
- `workdir.md`: WP-08 workspace modes, binding stability, path locks, Git
  isolation, and direct-directory safety boundaries.
- `execution.md`: WP-09 Run state machine, task gate, source/idempotency
  deduplication, input queue, permission snapshot, capacity lifecycle, and
  outbox boundary.
- `conversations.md`: WP-10 task/independent conversations, explicit sharing,
  ordered messages, typed mentions, comments, handoff, completion, and safe
  notifications.
- `event-delivery.md`: WP-12 durable Run events/results/artifact metadata,
  device ACK/idempotency, bounded local storage, REST reads, and SSE delivery.
- `reconciliation.md`: WP-13 typed supervisor facts, process/result evidence,
  fail-closed epoch boundaries, local manifests/journals, and explicit
  operator resolution.
- `runtime-limits-cleanup.md`: WP-14 typed runtime configuration, immutable
  Run resource snapshots, bounded cleanup previews, and safe artifact deletion.
- `operations.md`: explicit migrations, compatibility order, backup contents, and
  recovery boundary.
- `desktop.md`: WP-17 Electron renderer boundary, local daemon actions, and
  capability-only Runtime probing.
