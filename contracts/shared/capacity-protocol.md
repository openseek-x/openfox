# Capacity protocol contract

## Resource and state model

Capacity is reserved atomically for the organization, physical computer, and
Agent resources required by a Run. Counters are non-negative and never exceed
their configured limits. Lowering a limit blocks new reservations; it does
not kill or erase existing reservations.

The reservation lifecycle is:

`RESERVED -> RELEASED` or `RESERVED -> CLAIMED`.

The dispatch offer lifecycle is:

`LIVE -> CLAIMED`, `LIVE -> EXPIRED`, or `LIVE -> CANCELED`.

Each Run and generation has at most one effective offer. A valid claim keeps
the reservation; a claim that loses to expiry or cancellation releases it once.

## Operations

- `reserve`: one short transaction creates the reservation and offer together.
- `claim`: the device authenticates independently, locks the offer and the
  affected capacity rows, verifies expiry, target device, generation, grant,
  and authorization, then marks the offer claimed.
- `release`: a reservation CAS marks it released and decrements each counter
  exactly once. Repeating the same release is an idempotent no-op.
- `reap`: an expired live offer competes for the same lock as claim. Exactly
  one operation may transition it and release capacity.

## Lock order

When multiple resources are needed, locks are acquired in this order:

`organization capacity -> physical computer capacity -> Agent -> Task ->
Conversation -> Run -> Offer`.

Same-kind resources are sorted by stable ID. A candidate that cannot be
reserved is rolled back as a whole; partial capacity is never retained.

## Idempotency and conflicts

The idempotency scope includes organization, Run, generation, resource, and
operation. Reusing a key with different parameters is a conflict, not a new
operation. A valid claim after a network disconnect is not requeued merely
because the caller did not receive the response; it enters reconciliation.

## Required concurrency tests

- Two organizations compete for the last shared device slot: one succeeds.
- Two workers claim one live offer: one succeeds.
- Claim and expiry reaping race: only one transition succeeds.
- Duplicate release and duplicate reap: counters change once and remain
  non-negative.
- A stale generation or revoked grant cannot claim a new offer.

## Runtime role access (migration 000019)

`openfox_app` cannot execute `reserve_capacity`, `claim_offer` or `reap_offer`
directly. It executes source-gated wrappers that reject with SQLSTATE `42501`
otherwise:

- `dispatch_reserve_capacity` and `dispatch_reap_offer`: only in an
  `organization_worker` tenant context.
- `device_claim_offer`: only in a `device_request` context whose actor ID is
  the claiming device.
- `finish_queued_run`: ends a still-`QUEUED` Run in the order organization
  capacity -> computer -> Agent -> Run and returns `DONE`, `NOT_QUEUED` or
  `BUSY` (Run row held by another transaction, `SKIP LOCKED`).

Organization capacity created with `max_slots = 0` is initialized to the
runtime config `active_max` once; Agent `max_slots` follows the current config
`max_concurrency`.
