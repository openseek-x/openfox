# Conversation contract

This contract defines the server boundary for conversations, messages, task
comments, handoff, and notifications. It extends `execution.md` within the
tenant boundary; `workbench.md` and `event-delivery.md` define the UI and
event/result/artifact stream separately.

## Conversation model

- A conversation is either task-bound (`task_id` is present) or independent;
  both forms require an active Agent in the same organization and space.
- `visibility` is `PRIVATE` by default. `SHARED` access is explicit through
  `conversation_members`, with `VIEWER` or `COMMENTER` access levels.
- `lifecycle` is `OPEN`, `COMPLETED`, or `ARCHIVED`. `handoff` is `NONE`,
  `WAITING_REPLY`, or `WAITING_REVIEW`. These are fixed behavioral enums, not
  user-configurable records.
- Every mutable conversation response includes `version`. Completion, reopen,
  and handoff updates are compare-and-swap operations and stale versions
  return a conflict.

## Messages and mentions

- Messages are ordered by the database-assigned conversation `seq`, not client
  timestamps. `client_message_id` is required and is idempotent per
  organization/conversation; the same ID with different content is a conflict.
- Mentions are typed `user_id` or `agent_id` records. A user mention requires
  active organization membership and conversation access; an Agent mention
  must identify the conversation's active Agent in the same space.
- An `@Agent` message writes one durable conversation input, trigger, and
  execution outbox record in the same transaction. Replayed client messages do
  not enqueue a second input.
- A normal task comment never starts an execution. Comments have their own
  required `client_comment_id` idempotency key and typed mention records.

## Completion and handoff

- Completing a task-bound conversation may mark its task `DONE` only when the
  caller has task update permission and the task type has a non-archived DONE
  workflow status. The conversation and task update commit together.
- An active Run (`QUEUED`, `PREPARING`, `RUNNING`, or `CANCEL_REQUESTED`)
  blocks completion with `RUN_ACTIVE`; the service never silently kills a Run.
- Completing an independent conversation never creates or mutates a task.
  Reopen and handoff actions require the owner and the expected version.
- Handoff notifications contain only a safe summary. Notification delivery is
  deduplicated and source visibility is rechecked when reading.

## Authorization and limits

- Every operation establishes the existing trusted organization context, checks
  the actor authorization snapshot, and rechecks space/object permission. RLS
  is a database backstop, not a substitute for object authorization.
- Cross-organization IDs, inactive members, revoked space access, and Agent or
  task IDs from another space are rejected without exposing the resource.
- Message/comment bodies are bounded to 64 KiB, mentions to 32 entries, and
  list pages to the shared API maximum of 100 items.
- API writes use the existing CSRF and authorization middleware. Conversation
  writes do not require a generic dictionary table or broaden the user
  authorization boundary.
