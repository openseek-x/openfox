# Shared contracts

These files define the integration boundaries for authorization, collaboration,
device and Agent management, execution, conversations, event delivery,
reconciliation, operations, and desktop clients.
Agents must update the contract first when implementation needs a semantic
change. A code-only change must not silently change these rules.

- `tenant-context.md`: trusted transaction-local tenant isolation.
- `capacity-protocol.md`: reservation, offer, claim, release, and reap rules.
- `error-schema.md`: safe API error shape and status semantics.
- `membership-invitations.md`: member roles, invitation lifecycle, and
  last-owner protection.
- `device-auth.md`: Ed25519 challenge-response, device sessions, and grant
  authorization.
- `api-authz.md`: route shapes and cross-cutting authorization
  requirements.
- `collaboration.md`: space, workflow, task, and activity rules.
- `device-management.md`: physical device ownership, registration, and
  grant management rules.
- `agent-management.md`: Agent visibility, immutable configuration
  versions, device/grant binding, and bounded execution policy.
- `runtime-adapter.md`: Runtime capability, bounded JSONL execution,
  cancellation, redaction, and recovery boundaries.
- `workdir.md`: workspace modes, binding stability, path locks, Git
  isolation, and direct-directory safety boundaries.
- `execution.md`: Run state machine, task gate, source/idempotency
  deduplication, input queue, permission snapshot, capacity lifecycle, and
  outbox boundary.
- `conversations.md`: task/independent conversations, explicit sharing,
  ordered messages, typed mentions, comments, handoff, completion, and safe
  notifications.
- `event-delivery.md`: durable Run events/results/artifact metadata,
  device ACK/idempotency, bounded local storage, REST reads, and SSE delivery.
- `reconciliation.md`: typed supervisor facts, process/result evidence,
  fail-closed epoch boundaries, local manifests/journals, and explicit
  operator resolution.
- `runtime-limits-cleanup.md`: typed runtime configuration, immutable
  Run resource snapshots, bounded cleanup previews, and safe artifact deletion.
- `operations.md`: explicit migrations, compatibility order, backup contents, and
  recovery boundary.
- `desktop.md`: Electron renderer boundary, local daemon actions, and
  capability-only Runtime probing.
