# Run execution contract

Run execution is governed by this contract alongside the authorization,
local Runtime, and work-directory contracts.
PostgreSQL rows and unique constraints are authoritative in production; the Go
`internal/execution` engine is a concurrency-safe test seam.

## Run state machine

The only Run states are `QUEUED`, `PREPARING`, `RUNNING`,
`CANCEL_REQUESTED`, `SUCCEEDED`, `FAILED`, and `CANCELED`. Terminal states
cannot be revived. A retry creates a new Run with `retry_of`; it never mutates
the terminal Run back to a live state.

Valid transitions are:

```text
QUEUED -> PREPARING | CANCELED | FAILED
PREPARING -> RUNNING | CANCEL_REQUESTED | FAILED | CANCELED
RUNNING -> SUCCEEDED | FAILED | CANCEL_REQUESTED
CANCEL_REQUESTED -> SUCCEEDED | FAILED | CANCELED
```

Every mutation carries an organization ID and expected `version`. A stale
version is rejected. Cancellation commands are scoped by `(organization,
run, command_id)` and are idempotent.

## Task gate and conversation input

- `TRIAGE`, `BACKLOG`, `CANCELLED`, and `DUPLICATE` do not auto-start.
- `TODO` and `IN_PROGRESS` may start.
- `DONE` requires an explicit wake/action.
- An active Run is unique per organization/conversation. A new input is
  persisted as `PENDING` for that Run instead of creating a second Run.
- Source events are deduplicated by `(organization_id, source_event_id)`.
  Replaying the same event is a no-op; a reused event ID with different
  parameters is rejected.
- Run creation idempotency is scoped by
  `(organization_id, operation, idempotency_key)` and stores a request hash.

## Permission snapshot

Run creation receives a typed snapshot of the human actor, Agent owner,
active Agent configuration, device grant, and versions. The snapshot must
match the requested organization, Space, Agent, computer, and grant. All
authorization booleans must be true. This is an execution-time intersection,
not a generic permission or key/value table; revocation is checked again by
the durable capacity/grant boundary before claim.

## Capacity and outbox

Scheduling reserves resources in the fixed order organization -> computer ->
Agent and creates a short-lived offer. A device claims the offer, which moves
the Run to `PREPARING`; starting the runtime moves it to `RUNNING`. Terminal
completion releases the reservation, including after a successful claim.
Offer expiry releases capacity and returns the Run to `QUEUED` with
`OFFER_EXPIRED`. Capacity exhaustion records the blocking dimension such as
`ORGANIZATION_CAPACITY`, `COMPUTER_CONCURRENCY`, or `AGENT_CONCURRENCY`. A
release is idempotent and must never decrement a counter twice.

Run changes append a bounded, safe outbox payload. The outbox is delivered at
least once; consumers must use the stable event ID and aggregate/version
tuple for deduplication. Slow runtime output, artifacts, comments, and full
conversation payloads must not be placed in this control payload.

## Central dispatch (migration 000019)

The `internal/dispatch` worker is the durable PostgreSQL path. The process
lists candidate organizations through the context role
(`list_dispatch_organizations` returns IDs only), then runs these phases in fixed order, each in its own short
`organization_worker` transaction:

1. `reap_offers`: expired live offers release capacity; the Run returns to
   `QUEUED` with `OFFER_EXPIRED`.
2. `expire_leases`: a claimed Run whose lease expired is marked
   `reconciliation_required` (reason `LEASE_EXPIRED`, failure `UNKNOWN_PROCESS`); the reservation is kept
   until reconciliation decides the process fact.
3. `expire_queued`: a Run past `queue_expires_at` ends as `FAILED` /
   `QUEUE_TIMEOUT` through `finish_queued_run`.
4. `materialize_inputs`: consecutive `PENDING` inputs of a conversation with
   no active Run become one `QUEUED` Run after the permission snapshot is
   re-evaluated for the original human actor.
5. `offer_queued`: reserve capacity and create a short-lived offer.

A phase failure is reported and the next phase still runs. Defaults: offer TTL
15s, lease TTL 90s, renew interval 20s, worker poll 1s, batch 50.

Device lifecycle (all calls authenticate with the device session; tenant
context source is `device_request` and the actor is the device):

```text
GET  /v1/device/organizations/{org}/offers
POST /v1/device/organizations/{org}/offers/{offer}/claim       -> PREPARING + lease
GET  /v1/device/organizations/{org}/runs/{run}/context?generation=N
POST /v1/device/organizations/{org}/runs/{run}/start           -> RUNNING (start permission)
POST /v1/device/organizations/{org}/runs/{run}/lease           -> renew, echoes challenge
POST /v1/device/organizations/{org}/runs/{run}/prepare-failed  -> FAILED/CANCELED before start
POST /v1/device/organizations/{org}/runs/{run}/events | result (event-delivery contract)
```

The runtime process may only start after `start` succeeds. `start` returns
`CANCEL_REQUESTED` when the user canceled during preparation; the daemon then
reports `prepare-failed` with `CANCELED` and never starts a process. Renewal
returns `cancel_requested` so a running process can be stopped even if the WSS
wake was lost. A rejected renewal (`RECONCILIATION_REQUIRED`,
`AUTHORIZATION_REVOKED`, `GENERATION_CONFLICT`) stops the process and the
daemon does not upload a result.

Users cancel with `POST /v1/organizations/{org}/runs/{run}/cancel` and an
`Idempotency-Key`. A `QUEUED` Run ends directly as `CANCELED`; a claimed Run
moves to `CANCEL_REQUESTED` and the computer is woken. A `BUSY` outcome from
`finish_queued_run` maps to `503 RETRY`; the client retries with the same key.

WSS `run.dispatch` and `run.cancel` messages are wake hints only and carry
organization, Run and generation. The HTTPS endpoints above are the source of
truth. The hub is in-process, so in a multi-instance deployment a device on
another instance falls back to its offer polling interval.
