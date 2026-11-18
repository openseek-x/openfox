# Database migrations

Migrations are ordered, forward-only SQL files. They are intended to run against
a fresh target database with a bootstrap role that can create roles and schemas.
The explicit migration runner records each committed filename and checksum
in `openfox_meta.schema_migrations` before executing a later file. `serve` never
runs migrations implicitly.

## Current files

- `000121_task_completed_at.sql`: T02 adds nullable `tasks.completed_at` without historical backfill and extends the existing status projection trigger. Native creation supplies database time; historical/Issue/old-bundle imports without a source time remain NULL. Reopening clears time, actual transitions into DONE record time, and DONE-to-DONE preserves it, including status definition reclassification. Apply after 120 before the new API/import workers, then client. ALTER/CHECK briefly locks/scans tasks; assess persistent data volume and maintenance separately. Retain the additive column, constraint and trigger on application rollback; old writers omitting the field can create unknown-time DONE rows and old UI falls back from new range values to all. No new configuration or dependency. Only isolated migration/backup/restore verification was authorized. See the T02 development record for full SQL, compatibility and observed limits.

- `000120_share_snapshots.sql`: C03 selectively ports the conversation snapshot foundation (source commit `4c940e21`) and adds creator-only link recovery. Adds `organizations.allow_public_sharing=false`, immutable text-only `share_snapshots`, token hash plus AES-GCM ciphertext/nonce/key ID, forced tenant RLS, creator/org indexes, irreversible revocation trigger, and the bounded SECURITY DEFINER hash resolver. No backfill. Apply before the new server/client; the organization ALTER takes a short table lock, all other objects are new. Uses existing `OPENFOX_SECRETS_KEY_FILE`; no key generation or plaintext fallback. Retain all additive objects and ciphertext on application rollback. Only disposable isolated databases were authorized and exercised; never substitute this for the other branch's conflicting migration 118 or apply to its existing ledger.

- `000124_doc_references.sql`: D04 extends only `messages_payload_shape_valid` with a DOC/document-placeholder branch, retaining TASK and previous TEXT/action semantics. No table, column, index, data backfill, new grant, config or dependency. Source pointers contain only type and UUID; current per-reader space and personal-document access is enforced when resolving metadata. Apply after the coordinated 123 migration, then server/client. Replacing the CHECK scans messages under a DDL table lock. Only isolated databases were authorized; retain 124 and stored references when withdrawing DOC creation/UI, with DOC redaction/current ACL and all-REFERENCE Runtime exclusion preserved. Do not drop the CHECK back to TASK-only or delete existing DOC rows. See the D04 development record for exact SQL and backup/restore evidence.

- `000122_task_references.sql`: T04 expands the two message CHECK constraints with passive USER `REFERENCE` messages. Payload is exactly a TASK UUID pointer; body is a fixed safe placeholder, with no reply, action, copied title or content. No new columns, data backfill, config, functions or grants. Apply after coordinated migration 121, before the server/client. Constraint replacement scans messages under DDL locks; only isolated databases were authorized. Retain constraints and rows on rollback, and retain server-side reference redaction/ACL and Runtime exclusion while withdrawing the UI/creation route. A full old-server rollback would expose stored UUID pointers and include safe placeholders in history, so it does not preserve this package's full behavior.

- `000118_task_type_order.sql`: S04 adds nullable nonnegative `task_types.position`, backfills each tenant/space in `created_at,id` order, and adds the space-order index. Temporary migrator SELECT/UPDATE RLS policies exist only inside the migration transaction and are dropped before commit. Apply after the coordinated preceding migrations and before new API/import workers. The table ALTER and backfill/index build hold locks and touch all historical task types: assess production size and maintenance time separately; only isolated test databases were authorized here. Old SQL writers may omit the column; new writers append after legacy NULL rows. Roll back application code while retaining the additive column and saved order; there is no destructive down migration.

- `000119_task_type_commit.sql`: S06 adds nullable `task_types.commit_type` with a 1–32 ASCII letter/digit/hyphen constraint starting with a letter. No backfill and no schema default. New task Runs snapshot `feat` when the type field is NULL; old Run snapshots remain unchanged. Apply before API/dispatch/import workers; adding the nullable column and CHECK briefly locks/scans task types. Roll back application code while retaining the column and optional Run JSON key. No new configuration or dependency. Persistent/production migration requires separate authorization.

- `000115_message_reply_snapshot.sql`: adds nullable `messages.reply_snapshot` JSONB for C01. New quote replies retain a server-derived snapshot of at most 2,000 Unicode code points, with a 16 KiB JSONB text limit; the existing `reply_to_id` tenant/conversation FK is unchanged. Historical rows and old writers remain NULL and read through to the current original, explicitly marked as legacy. Apply after 114, before the new API/dispatch server, then the client. There is no backfill or new configuration. Adding the column/check briefly locks and scans `messages`; the migration runner commits it atomically and records its checksum. Roll back the application while retaining this additive schema and snapshots. Authorized and validated only in isolated test databases; persistent environments require separate authorization.

- `000114_space_creator.sql`: adds nullable `spaces.created_by` and a same-organization member foreign key with `ON DELETE SET NULL (created_by)`. Apply after 113 and before updating the API and import/clone workers. Only new spaces record their authenticated creator; historical rows are not backfilled and old writers continue to insert NULL. Member revocation retains the identity, while member deletion clears only this field. Adding the column/FK briefly locks `spaces` and validates the FK; it does not rewrite existing rows with inferred identities. No configuration or dependency changes. Roll back the application while retaining this additive schema and recorded data. Authorized and validated only in isolated test databases; do not run against persistent environments without separate authorization.

- `000109_workspace_path_command.sql`: expands the device-command kind constraint with `workspace_path`. Apply after 108, then update the center and daemon before exposing the updated desktop entry. The center stores only an encrypted reply and request metadata; actual paths stay in the device journal and requesting desktop. No row rewrite or backfill. Retain the expanded constraint on application rollback. Authorized and validated only in disposable test databases, not persistent QA or production.

- `000108_user_interface_density.sql`: adds account-level `density` (`comfortable` / `compact`, default `comfortable`) to `user_preferences`. Apply after 107 and before the updated API, then the client. Existing values, versions and timestamps are unchanged; existing writers retain the new value when they update old columns. Retain this additive column on application rollback. This change is authorized for isolated testing only, not persistent QA or production.

- `000001_db001_tenant_rls.sql`: creates the `openfox_migrator`,
  `openfox_app`, and `openfox_context` roles; creates the organization,
  membership, space, and task foundation; and installs the trusted
  transaction-local tenant context plus forced RLS policies.
- `000002_db002_capacity.sql`: creates physical-device control capacity,
  tenant device grants and Agent capacity, reservations, offers, and the
  narrow `reserve_capacity`, `claim_offer`, `release_reservation`, and
  `reap_offer` database functions.
- `000003_wp02_identity_auth.sql`: adds normalized user email, password
  credentials, opaque sessions, and the narrow control-plane organization
  membership index/function used before tenant context setup.
- `000004_wp02_membership.sql`: adds audit events, member-listing indexes, and
  database guards for immutable membership identity, authorization versions,
  and the last active owner invariant.
- `000005_wp02_invitations.sql`: adds hashed-token invitations, the
  transaction-local token lookup boundary, and atomic invitation acceptance.
- `000006_wp03_device_auth.sql`: adds Ed25519 device keys, one-time
  challenges, hashed device sessions, active-grant selection, and fail-closed
  device/key revocation functions.
- `000007_wp04_collaboration.sql`: extends spaces with stable prefixes and
  versioned task counters; adds fixed-category organization status records
  (including descriptions and display config), space-level task types,
  workflow mappings, labels, task activities, task fields, constraints, and
  tenant RLS. Existing task status text remains a derived compatibility
  projection; `status_id` is authoritative.
- `000008_wp04_trigger_rls.sql`: keeps `FORCE RLS` enabled while allowing the
  non-login migrator-owned status trigger functions to inspect status metadata.
- `000009_wp05_device_management.sql`: adds device owner/install/health and
  capability facts, scoped organization grants, one-time registration tokens,
  and the center epoch used by device handshakes. The registration exchange is
  a narrow security-definer function; it does not expose a general control
  plane write primitive.
- `000010_wp06_agent_management.sql`: extends the capacity-era Agent row with
  space ownership, visibility, kind, archive/version metadata, adds stable
  device-grant IDs and append-only Agent configuration versions, and installs
  typed device/grant validation plus tenant RLS. Legacy capacity-only Agent
  rows remain nullable-managed rows until a later explicit migration assigns
  them to a Space and owner.
- `000011_wp09_execution.sql`: adds the tenant-scoped conversation execution
  baseline, durable input/source-trigger deduplication, Run state and
  idempotency records, an execution outbox, queue indexes, and RLS. It also
  replaces the capacity release function so a claimed reservation can be
  released exactly once when a Run reaches a terminal state.
- `000012_wp10_conversations.sql`: evolves the initial conversation lifecycle,
  adds explicit visibility/membership, ordered messages and typed mentions,
  task comments, handoff notifications, completion-safe defaults, scope
  triggers, and tenant RLS. Fan-out notification writes use one typed security
  definer function; recipients retain recipient-only read/update access.
- `000013_wp12_event_results_artifacts.sql`: adds durable Run event sequencing,
  idempotent final results, bounded artifact metadata, tenant RLS, and the
  narrow result-time reservation release grant. Artifact bytes remain outside
  PostgreSQL behind `OPENFOX_STORAGE_ROOT`.
- `000014_wp13_reconciliation.sql`: adds typed supervisor facts, durable
  reconciliation cases, process/result evidence, CAS versions, active-case
  uniqueness, and tenant RLS. It never turns unknown process state into a
  terminal Run without an explicit, evidence-backed resolution.
- `000015_wp14_limits_cleanup.sql`: adds typed organization runtime limits,
  insert-once Run resource snapshots, artifact version/pinned metadata, and
  expiring two-phase artifact cleanup previews with tenant RLS. It is
  expand-only and does not remove existing data.
- `000016_wp17_email_login.sql`: adds salted, short-lived email login
  challenges with bounded attempts and one-time consumption. The table is
  control-plane authentication data rather than tenant data.
- `000017_wp18_email_signup_onboarding.sql`: email-code self sign-up and
  first-organization onboarding. Adds `openfox_private.ensure_email_user`
  (idempotent find-or-create of a user plus a non-password sentinel
  credential), `openfox_private.create_first_organization` (owner membership,
  default task statuses, capacity/runtime rows and audit event in one call,
  serialized per user, idempotency-keyed, owned-organization limit), and
  `openfox_private.list_user_organizations`. Adds the
  `organization_creations` idempotency table and the control-plane
  `organization_directory` read model (backfilled for existing
  organizations). Runtime roles get EXECUTE on the three functions only; no
  table write grant or RLS policy is relaxed. Expand-only.
- `000019_center_run_dispatch.sql`: central Run dispatch. Adds Run
  `started_at`, `lease_expires_at`, `queue_expires_at` and typed
  `failure_code` columns, partial indexes for pending-input, queue-timeout and
  lease-expiry scans, and migrator-only read policies used by the new
  security-definer functions. `list_dispatch_organizations` (context role
  only) returns organization IDs with dispatch work and nothing else.
  Triggers keep `agents.max_slots` equal to the current config
  `max_concurrency` and initialize an unused (`0`, no reservations)
  organization capacity to `active_max` when its runtime config is inserted;
  a one-time idempotent backfill applies the same rules to existing rows.
  `finish_queued_run` ends a still-`QUEUED` Run in the capacity lock order and
  returns `DONE`, `NOT_QUEUED` or `BUSY`. The application role still cannot
  execute `reserve_capacity`, `claim_offer` or `reap_offer` directly; it only
  receives source-gated wrappers: `dispatch_reserve_capacity` and
  `dispatch_reap_offer` require an `organization_worker` context, and
  `device_claim_offer` requires a `device_request` context whose actor is the
  claiming device. Expand-only; the backfill only raises unused zero slots.
- `000021_home_inbox.sql`: home inbox sections. Adds nullable
  `notifications.cleared_at`, `snoozed_until` and `actor_user_id` columns
  (existing rows stay in the inbox section), widens the kind check with
  `RUN_FAILED` and `TASK_ASSIGNED`, adds a partial cleared-at index, and
  replaces `enqueue_notification` (same signature, owner and grants) so it
  records the requesting member as the actor in `user_request` context and
  skips self-notifications. No grant or RLS policy is relaxed. Expand-only;
  rolling back the application leaves the new columns unused.
- `000022_home_my_tasks.sql`: home "my tasks". Adds a partial
  `task_activities (organization_id, actor_id, task_id) WHERE kind =
'task.created'` index (the creator is read from the creation activity, so no
  task column or RLS-bypassing backfill is needed) and a partial
  `tasks (organization_id, assignee_id)` index. Expand-only; rolling back the
  application leaves the indexes unused.
- `000023_home_agent_directory.sql`: home "all Agents and crews". Creates
  `agent_crews` (a named group of Agents in one space, with the same
  visibility, archive and version semantics as `agents`), `agent_crew_members`
  and `agent_directory_pins` (per-member pins targeting exactly one Agent or
  crew). All three tables force RLS with tenant isolation; the pins policy also
  restricts rows to `current_actor_id()`. `openfox_app` gets `SELECT/INSERT`
  plus column-limited `UPDATE` on crews and `SELECT/INSERT/DELETE` on members
  and pins. Crew dispatch is out of scope. Expand-only; rolling back the
  application leaves the tables unused, and if removal is required drop pins,
  then crew members, then crews.
- `000024_home_assistant.sql`: home assistant. Adds nullable
  `agents.builtin_kind` (only `ASSISTANT`, which must be a private Agent with an
  owner) plus a partial unique index allowing one assistant per member per
  organization, and rebuilds `agents_space_name_unique` to exclude built-in
  Agents (create the new index, drop the old one, rename). Adds nullable
  `conversations.title` / `archived_at` for the session picker, and
  `messages.kind` (default `TEXT`) / `payload` (Agent-authored JSON object up
  to 64 KiB) for draft cards. No data is rewritten and no grant or RLS policy
  changes; `openfox_app`'s column-limited `UPDATE` on `agents` does not include
  `builtin_kind`. Expand-only; rolling back the application leaves the columns
  unused. If removal is required, drop the message constraints and columns,
  the conversation constraint and columns, recreate the original
  `agents_space_name_unique` without the `builtin_kind IS NULL` predicate
  (only after confirming no two active Agents in a space share a name), then
  drop `agents_builtin_assistant_unique`, the check constraint and the column.
- `000025_task_core_fields.sql`: task core. Adds nullable
  `tasks.start_date` / `due_date` / `parent_task_id` / `rank` (collation `C`,
  `[0-9a-z]{1,64}`) / `deleted_at` / `deleted_by`, a
  `UNIQUE (organization_id, space_id, id)` used by child tables' composite
  foreign keys, a same-space parent foreign key, and relaxes
  `tasks_priority_valid` to the superset `NONE, LOW, NORMAL, HIGH, URGENT`.
  Rows without `rank` sort by `'h' || lpad(sequence, 12, '0')`, so nothing is
  backfilled; `tasks_board_idx` indexes that expression for live tasks. Creates
  `task_assignees` (exactly one of member / Agent / crew per row),
  `task_followers` (member or Agent), `task_links` (`http(s)` URL up to 2048
  characters) and `task_reactions` (task description or a comment), all with
  `FORCE ROW LEVEL SECURITY` and tenant isolation. `openfox_app` gets
  `SELECT, INSERT, DELETE` on assignees, followers and reactions and full DML on
  links. Soft-deleted tasks keep every foreign key valid; the application
  filters `deleted_at IS NULL`. Expand-only; rolling back the application
  leaves the columns and tables unused. If removal is required: drop the four
  new tables, `tasks_board_idx`, `tasks_parent_idx`, the new task constraints
  (restore `tasks_priority_valid` to `LOW, NORMAL, HIGH, URGENT` only after
  `UPDATE`-ing any `NONE` rows), `tasks_space_identity_unique`, then the six
  columns.
- `000026_task_configuration.sql`: global task configuration.
  Creates `label_groups`; makes `labels.space_id` nullable (NULL means an
  organization label, unique by `lower(name)` among organization labels) and
  adds `color` (hex), `description`, `group_id` (organization labels only),
  `archived_at`, `version`, `updated_at`; adds `task_labels_label_idx` and the
  `task_labels_scope_valid_trigger` (security definer, with a migrator-only
  read policy on `labels`) that rejects labels from another space. Adds
  `task_statuses.is_default` (at most one per organization, `BACKLOG`/`TODO`
  only), `collapsed_by_default` and `updated_at`; no default is backfilled.
  Creates `task_relation_types` (custom types only; the four built-ins are code
  constants), `task_relations` (built-in or custom type, symmetric uniqueness
  for `RELATES`), `custom_fields` (nine kinds, archive-as-delete),
  `custom_field_options`, `task_type_fields` (field ↔ space task type) and
  `task_field_values`, all with `FORCE ROW LEVEL SECURITY` and tenant
  isolation. Expand-only. If removal is required: drop `task_field_values`,
  `task_type_fields`, `custom_field_options`, `custom_fields`,
  `task_relations`, `task_relation_types`; drop the trigger, its function and
  `labels_migrator_trigger_access`; drop the new status index, constraint and
  columns; delete organization labels (`space_id IS NULL`, after detaching
  their `task_labels`) before restoring `labels.space_id NOT NULL`; then drop
  the label constraints, columns and `label_groups`.
- `000027_api_idempotency_inbox.sql`: creates `api_idempotency_keys`
  (organization × actor × operation × key, request SHA-256, resource id,
  24-hour expiry) with `FORCE ROW LEVEL SECURITY` restricted to the current
  organization and actor, and relaxes `notifications_kind_valid` to add
  `HANDOFF_REVIEW` (waiting for review; `HANDOFF` keeps meaning waiting for a
  reply). Rebuilding the check validates the whole table under a brief
  `ACCESS EXCLUSIVE` lock. No data is rewritten. If removal is required, drop
  the table, `UPDATE` any `HANDOFF_REVIEW` rows to `HANDOFF`, then restore the
  previous check.
- `000028_agent_profile_crews.sql`: Agent profile and crews (stage 2a). Adds
  `agents.title` (≤64), `avatar` (jsonb object ≤1 KiB), `allow_assign`,
  `allow_direct_chat`, `trigger_crew`, `trigger_mention` (default true),
  `trigger_workflow` (default false) and `profile_version`, and relaxes
  `agents_kind_valid` to the superset `CODE, OFFICE, DESIGN`. Adds defaulted run
  settings to the immutable `agent_config_versions` (`chat_concurrency` +
  value, `workdir_isolation`, `approval_mode`, `collaboration_mode`,
  `custom_args` ≤64 items, Git author name/email modes, `env_digest`); existing
  versions read as the defaults. Creates `agent_env_vars` (AES-256-GCM
  ciphertext + 12-byte nonce + key id only, names unique per Agent),
  `agent_repos` (https or ssh URL, unique per Agent), `agent_favorites`
  (personal, owner-only RLS like `agent_directory_pins`) and
  `agent_crew_human_members`; adds `agent_crews.leader_agent_id`,
  `instructions`, `shared_task_dir` and `agent_crew_members.exclusive` with a
  partial unique index (an Agent is exclusive to at most one crew). New tables
  use `FORCE ROW LEVEL SECURITY`. Column-limited `UPDATE` grants are extended
  for the new Agent and crew columns only. Expand-only. If removal is required:
  drop the four new tables, the exclusive index and new crew/member columns,
  the config-version constraints and columns, then the Agent constraints and
  columns, restoring `agents_kind_valid` to `CODE, OFFICE` only after updating
  any `DESIGN` rows.
- `000029_org_events.sql`: organization event stream (stage 2a). Creates
  `organization_events` (uncached sequence id, `txid xid8` defaulting to
  `pg_current_xact_id()`, type/entity/payload ≤16 KiB, optional recipient,
  space and conversation audience) with `FORCE ROW LEVEL SECURITY` (tenant
  isolation plus recipient-only rows) and a migrator-only insert policy for the
  security-definer triggers that write events in the same transaction as
  `tasks` / `notifications` (statement-level, coalesced) and `runs.state`,
  `conversations` and `messages` changes (row-level). Adds nullable
  `run_events.txid` (default set separately, so no table rewrite) and an index
  on `(organization_id, COALESCE(txid, '0'), stream_seq)`; delivery only emits
  rows whose `txid` is below the current snapshot xmin, so cursors never skip
  late commits. Creating the `run_events` index blocks its writes for a time
  proportional to its size. Retention (7 days): FORCE RLS hides
  recipient-targeted rows from the organization worker and the control-plane
  role has no table privileges, so cleanup goes through the security-definer
  `openfox_private.purge_organization_events(limit)` (1–1000 rows per call,
  rows older than 7 days only, across organizations, returns the deleted
  count), executable only by `openfox_context`; the dispatch worker calls it
  from a control-plane transaction at most once a minute and only logs
  failures. Supporting objects: migrator SELECT / DELETE policies limited to
  rows older than 7 days and a global `(created_at)` index. If removal is
  required: drop the purge function, the two retention policies and the
  retention index, then the five triggers and functions, the table and its
  sequence, then the `run_events` index and column.
- `000030_device_settings.sql`: adds `physical_devices.settings_version` and
  the security-definer `openfox_private.update_device_settings(device, expected
version, name, max_slots)` so only the device owner, through an organization
  with an ACTIVE grant, can rename or change the concurrency limit (1–256);
  `openfox_app` still has no direct `UPDATE` on those columns. If removal is
  required, drop the function, the constraint and the column.
- `000031_conversation_nav_usage.sql`: conversation navigation and Run usage
  (stage 2b). Creates `conversation_user_state` (per member per conversation:
  `pinned_at`, `last_read_seq`) with `FORCE ROW LEVEL SECURITY` restricted to
  the current actor, so pin/read never bump `conversations.version`; adds
  nullable `run_results.usage` (jsonb object ≤1 KiB, reported by the device
  with the result; NULL for older daemons) and the navigation index
  `conversations_owner_nav_idx (organization_id, owner_user_id, archived_at IS
NULL, updated_at DESC, id DESC)`. Creating the index blocks conversation
  writes for a time proportional to the table size. Expand-only. If removal
  is required, drop the index, the `run_results` constraint and column, then
  the table.

- `000032_device_connect_commands.sql`: computer connection, remote commands
  and independent Agent chat concurrency (stage 2c).
  - Registration tokens: adds `device_registration_tokens.scope`
    (`PERSONAL`/`ORGANIZATION`, default `ORGANIZATION` = previous behaviour)
    plus the column-level `INSERT (scope)` grant for `openfox_app`, and
    replaces `openfox_private.register_device_with_token` (same signature) so
    the grant follows the token scope (`PERSONAL` grants the issuer) and the
    issuer's role is re-checked at exchange time (organization scope needs
    OWNER/ADMIN, personal scope rejects GUEST).
  - Remote commands: creates `device_commands`
    (`runtime_probe`/`runtime_test`/`storage_cleanup`, one open command per
    device and kind, `FORCE ROW LEVEL SECURITY` tenant isolation) with a 7-day
    retention purge function `openfox_private.purge_device_commands` granted
    only to `openfox_context`.
  - Chat concurrency: adds `agents.chat_max_slots` (NULL = share the task
    pool; DEFAULT config = 10, MANUAL = configured value) and
    `agents.chat_reserved_slots`, `capacity_reservations.pool` (`TASK`/`CHAT`,
    default `TASK`, which is correct for every pre-existing reservation), the
    `AGENT_CHAT_CONCURRENCY` wait reason, helpers `chat_slots_for` and
    `release_agent_slot`, and replaces `reserve_capacity`,
    `release_reservation`, `reap_offer`,
    `dispatch_reclaim_orphan_reservations` and both `sync_agent_max_slots*`
    trigger functions (same signatures, same lock order). Conversation Runs
    (`task_id IS NULL`) reserve from the chat pool when `chat_max_slots` is not
    NULL; every release decrements the pool recorded on the reservation.
    Backfills `chat_max_slots` from each Agent's current config. Rebuilding
    `runs_wait_reason_valid` scans `runs` under an exclusive lock.
  - Removal: drain or wait out all `CHAT` reservations first (or, with
    dispatch stopped, move each open `CHAT` reservation's slot back with
    `UPDATE agents SET reserved_slots = reserved_slots + chat_reserved_slots,
chat_reserved_slots = 0`), restore the capacity, reclaim and sync
    functions from `000002`/`000011`/`000019`/`000020`, drop the helpers, the
    `pool` column and the new `agents` columns, restore the old wait-reason
    constraint (no `AGENT_CHAT_CONCURRENCY` rows may remain), drop the purge
    function and `device_commands`, restore `register_device_with_token` from
    `000009`, revoke `INSERT (scope)`, then drop the `scope` constraint and
    column.

- `000033_space_settings.sql`: space settings and lifecycle (stage 3a).
  Adds `spaces` icon, purpose (14 work categories, default `other`), feature
  switches (views/projects on, iterations/triage off), `multi_assignee`,
  estimation settings (scale `notUsed` by default), `retired_at/by` (retire
  keeps using `status = ARCHIVED`) and soft-delete `deleted_at/by`; relaxes
  `spaces_prefix_valid` to accept the old lowercase format or the new 2–7
  character uppercase format without rewriting existing prefixes; creates
  `space_prefix_history` (old identifiers keep resolving to the same space,
  unique per organization), `space_user_order` (per-member sidebar order,
  RLS to the current actor) and `space_repos`; adds `tasks.estimate`
  (points, or seconds for the time scale) and `label_groups.space_id`, and
  drops `labels_group_organization_only` (group and label scope consistency is
  checked by the application); emits `space.changed` organization events from
  a row trigger limited to settings columns. Expand-only. If removal is
  required, drop the triggers and `emit_space_event`, the three new tables,
  `tasks.estimate`, `label_groups.space_id`, restore the two prefix/label
  constraints (only after reverting any new-format prefixes and space label
  groups) and drop the new `spaces` columns.

- `000034_iterations.sql`: iterations and the iteration scheduler (stage 3b).
  Adds `spaces` iteration settings (mode `manual`/`automatic`, duration 1–8
  weeks, cooldown 0–3 weeks, start weekday, timezone, upcoming count from the
  fixed set 1,2,3,4,6,8,10,12,15, auto-add started/completed, next start date,
  `next_iteration_number`); creates `iterations` (planned → active →
  completed or canceled, at most one active per space via a partial unique
  index, completion counts frozen on the row) and `iteration_links`, both
  with `FORCE ROW LEVEL SECURITY`; adds `tasks.iteration_id` with a
  same-space composite foreign key; adds read-only migrator scan policies on
  `spaces` and `iterations` (same pattern as the `*_dispatch_scan` policies)
  for `openfox_private.list_iteration_due_organizations`, granted only to
  `openfox_context`; emits `iteration.changed` organization events.
  Expand-only. If removal is required, drop the triggers and functions, the
  two scan policies, `tasks.iteration_id`, `iteration_links`, `iterations`,
  then the new `spaces` columns.
- `000035_projects_views.sql`: projects and saved views (stage 3c). Creates
  organization-level `projects` (status, priority, lead, emoji icon) linked to
  one or more spaces through `project_spaces` (at most one primary per
  project), `project_labels`, and `tasks.project_id`; creates per-space
  `views` (personal or space visibility, saved task filters and display
  settings as bounded jsonb, built-in/emoji/uploaded icon), per-member
  `view_member_prefs` (pin, position, view mode; RLS limited to the member),
  and `uploaded_icons` metadata (files live under `OPENFOX_STORAGE_ROOT`). All
  new tables use `FORCE ROW LEVEL SECURITY`; personal views are visible only
  to their creator at the RLS layer. Emits `project.changed` / `view.changed`
  organization events carrying ids only. Expand-only. If removal is required,
  drop the two triggers and `emit_project_view_event`, then
  `view_member_prefs`, `views`, `uploaded_icons` (and the stored icon files),
  `tasks.project_id`, `project_labels`, `project_spaces`, `projects`.
- `000036_triage.sql`: triage (stage 3d). Adds `spaces.triage_require_priority`
  and `spaces.triage_action` (`none`/`notify`/`assign`); creates
  `triage_members` (responsibility members) and `triage_rules` (ordered,
  enable-able rules with bounded jsonb conditions and at least one action),
  both with `FORCE ROW LEVEL SECURITY`; adds `tasks.triage_entered_at`,
  `tasks.triage_source`, `tasks.triage_snoozed_until`, and a same-organization
  self-reference `tasks.duplicate_of_task_id` plus a partial queue index;
  widens `notifications.kind` with `TRIAGE_NEW`; recreates the
  `spaces_emit_event_update` trigger with the two new columns and emits
  statement-level `triage.changed` events for rule and member changes.
  Expand-only. If removal is required, drop the triage triggers and
  `emit_triage_event*` functions, restore the previous
  `spaces_emit_event_update` column list and `notifications_kind_valid`
  (after removing `TRIAGE_NEW` rows), drop the new `tasks` columns, then
  `triage_rules`, `triage_members`, and the new `spaces` columns.
- `000037_organization_settings.sql`: organization settings and members
  (stage 4a). Adds `organizations.description`, icon columns (same shape as
  view icons), `default_locale` (`zh-CN`/`en-US`), `priority_levels` (4/5),
  `version`, and soft-delete `deleted_at`/`deleted_by`; syncs name, slug,
  icon, and `deleted_at` into `organization_directory` from an update trigger
  (upserting rows that are missing); `resolve_organization`,
  `list_user_organizations` (recreated with icon columns), and
  `accept_invitation` ignore deleted organizations. Widens the task and
  project priority checks with `LOWEST` and rejects `LOWEST` writes while the
  organization uses 4 levels (trigger with `FOR SHARE` on the organization
  row, raising `check_violation` constraint `priority_levels`). Adds
  `invitations.space_ids`, `delivery_status`, and `last_sent_at`;
  `accept_invitation` now lets revoked members rejoin with a new invitation,
  joins the selected active spaces, and resolves its output-column name
  conflicts (`#variable_conflict use_column`). Creates `invite_links` (one per
  organization, `FORCE ROW LEVEL SECURITY`, token hash plus sealed token) with
  `preview_invite_link` and `join_by_invite_link`; grants `openfox_app`
  EXECUTE on `invitation_lookup_matches`, which the 000005 invitation policy
  needs for `INSERT … RETURNING`. Emits `organization.changed` and
  statement-level `member.changed` events. Expand-only. If removal is
  required, drop the new triggers and functions, restore the 000005
  `accept_invitation`, 000003 `resolve_organization`, and 000017
  `list_user_organizations` definitions, set `LOWEST` tasks/projects to `LOW`
  and restore the previous priority checks, drop `invite_links`, then drop the
  new `invitations`, `organization_directory`, and `organizations` columns.
- `000038_user_profile_preferences.sql`: user profile and preferences (stage
  4b). Adds `users.full_name`, `bio`, and avatar metadata columns (the file
  lives under `OPENFOX_STORAGE_ROOT`); creates `user_preferences` (one row per
  user: theme, account locale, outline color, task-session toggles, send
  shortcut, bounded jsonb command shortcuts, `version`) and
  `email_change_challenges` (two-step email change: a `current` challenge sent
  to the existing address, then a `new` challenge bound to it; codes are salted
  hashes with attempt limits and expiry). Account-level tables follow the
  `user_sessions` / `email_login_challenges` model: no RLS, column-level grants
  to `openfox_app`, every query filtered by the authenticated user ID. Adds
  `shares_active_organization(viewer, target)` (security definer, returns a
  boolean) for avatar read authorization.
  Expand-only. If removal is required, drop the function,
  `email_change_challenges` and
  `user_preferences`, revoke the new `users` column grants, then drop the new
  `users` columns (avatar files under the storage root can be deleted
  afterwards).
- `000039_tokens_notifications_beta.sql`: personal access tokens, notification
  channels and beta features (stage 4c). Creates `personal_access_tokens`
  (SHA-256 hash plus display prefix, optional expiry, last-used and revoked
  timestamps; `openfox_app` may only insert and update `last_used_at` /
  `revoked_at`), `user_notification_channels` (per-user desktop / browser
  master switch and disabled event types; no row means defaults) and
  `user_beta_features` (per-user flags; the feature list lives in the
  application); adds `users.access_tier` (`standard` / `beta` / `internal`,
  set by operators, not writable by `openfox_app`). Account-level tables, no
  RLS, column grants, every query filtered by the authenticated user ID.
  Expand-only. If removal is required, drop the three tables and then
  `users.access_tier` (revoke tokens first if the application is still
  running: dropping the table makes every token fail authentication).
- `000040_automations.sql`: Agent automations (stage 5a). Creates
  `automations`, `automation_triggers` (schedule / task_status / comment /
  webhook; bounded jsonb config, schedule `next_run_at`, webhook URL token
  hash, sealed signing secret, signature flag, IP allowlist),
  `automation_events` (pending task-status / comment events written by
  statement-level triggers on `tasks` and `task_comments`, only when the space
  has an enabled trigger of that kind), `automation_runs` (one row per
  trigger, unique dedupe key per automation) and `webhook_deliveries`, all
  with `FORCE ROW LEVEL SECURITY`; widens `notifications.kind` with
  `AUTOMATION_RESULT` and notifies the automation's recipients from a trigger
  on `runs` when a run finishes; emits `automation.changed`; adds
  `resolve_webhook_trigger` (token hash lookup for the public receiver) and
  `list_automation_due_organizations` (control-plane scan). Expand-only. If
  removal is required, drop the new triggers and functions (including
  `tasks_enqueue_automation_status`, `task_comments_enqueue_automation`,
  `runs_notify_automation_result`), delete `AUTOMATION_RESULT`
  notifications and restore the previous `notifications_kind_valid`, then
  drop `webhook_deliveries`, `automation_runs`, `automation_events`,
  `automation_triggers` and `automations`.
- `000041_documents.sql`: document library (stage 5b). Creates `doc_sets`
  (space doc sets with icon, position and `version`), `doc_nodes` (tree nodes
  in exactly one container: a doc set or a user's personal "my documents";
  kinds folder / doc (Markdown body) / sheet (CSV body) / file (stored under
  `OPENFOX_STORAGE_ROOT`); parent deletion cascades to the subtree) and
  `doc_attachments` (editor images and files owned by a node), all with
  `FORCE ROW LEVEL SECURITY`; personal nodes are visible and writable only by
  their owner at the RLS level. Emits `doc.changed` (doc-set changes per
  space, personal changes only to the owner). Expand-only. If removal is
  required, drop the doc triggers and `emit_doc_event_*` functions, then drop
  `doc_attachments`, `doc_nodes` and `doc_sets` (stored files under the
  storage root can be deleted afterwards).
- `000042_skills.sql`: skills (stage 5c). Creates `skills` (space skills with
  a unique lowercase name per space, description, source kind manual / upload /
  url / runtime / market, file count and size limits, `version`),
  `skill_files` (relative-path file manifest; contents live under
  `OPENFOX_STORAGE_ROOT`; the path CHECK rejects absolute paths, backslashes,
  control characters and empty, `.` or `..` segments), `agent_skills` (skills
  assigned to an Agent in the same space; a new unique index on
  `agents (organization_id, id, space_id)` backs the composite foreign key) and
  `skill_import_items` (per-skill progress of a Runtime import, cascaded with
  the device command), all with `FORCE ROW LEVEL SECURITY` (tenant isolation;
  space permissions are checked by the application). Adds the `skill_scan` and
  `skill_import` device command kinds and makes the result size limit per kind
  (64 KB for `skill_scan`, 16 KB otherwise). Emits `skill.changed` per space.
  Expand-only. If removal is required, drop the skill triggers and
  `emit_skill_event_*` functions, drop `skill_import_items`, `agent_skills`,
  `skill_files` and `skills`, delete `skill_scan` / `skill_import` device
  commands and restore the original `device_commands_kind_valid` and
  `device_commands_result_shape` constraints, then drop the agents index
  (stored files under the storage root can be deleted afterwards).
- `000043_market.sql`: market (stage 5d). Creates the platform-wide, read-only
  market catalog in a compatible import shape: `market_catalog` (single row:
  source, catalog version, digest, categories and packs), `market_skills`
  (current version per skill with localized name / description / tags, icon
  URL, stats, publisher, downloads, file count and size limits),
  `market_skill_files` (relative-path manifest; contents live under
  `OPENFOX_STORAGE_ROOT`; same path CHECK as `skill_files`) and
  `market_templates` (Agent templates with localized fields, instructions and
  skill references). These tables belong to no organization and have no RLS:
  `openfox_app` only gets `SELECT`; rows are written by the import tool
  (`openfox-server market import`) as `openfox_migrator`. Adds nullable
  `skills.market_skill_id` / `skills.market_version` (both set only for
  `source_kind = 'market'`) with a partial index, to detect updates.
  Expand-only. If removal is required, drop the `skills` market columns,
  constraint and index, then drop `market_skill_files`, `market_skills`,
  `market_templates` and `market_catalog` (stored files under the storage
  root can be deleted afterwards).
- `000044_task_agent_execution.sql`: assigning a task to an Agent runs it
  and Agent task tools. Adds `conversations.origin`
  (`USER` default / `TASK_ASSIGNMENT`, the latter requires `task_id`) with a
  partial unique index on open, unarchived assignment conversations per
  task + Agent. Adds `task_agent_events` (RLS, `openfox_app` SELECT / INSERT /
  UPDATE): `ASSIGNED` / `UNASSIGNED` rows written in the task write
  transaction when an Agent assignee is added or removed and processed by the
  dispatch worker (open or reuse the SHARED assignment conversation and
  mention the Agent as the assigner / conversation owner; or cancel the
  Agent's active Runs on that task). `task_comments` gains an Agent author:
  `author_user_id` becomes nullable, new `author_agent_id` (FK to `agents`) and
  `run_id`, and a CHECK requiring exactly one author (existing rows all have a
  user author). `list_dispatch_organizations` also returns organizations with
  pending assignment events, and `enqueue_automation_comment` skips
  Agent-authored comments to avoid automation loops. Expand-only. Old
  binaries read `task_comments.author_user_id` as non-null, so before rolling
  the application back delete Agent-authored comments
  (`DELETE FROM openfox.task_comments WHERE author_agent_id IS NOT NULL`); the
  new table and columns can then stay idle. Full removal: drop the constraint,
  `author_agent_id` / `run_id`, restore `NOT NULL`, drop `task_agent_events`,
  the partial index and `conversations.origin`, and restore the 000020 /
  000040 function bodies.
- `000045_integrations.sql`: external integrations foundation (stage 6a).
  Adds `integration_connections` (organization connections and personal
  connections, optionally under an organization connection; credentials only
  as an envelope-sealed `credentials_sealed`, never returned; status
  `ACTIVE` / `NEEDS_ATTENTION` / `DISCONNECTED`; partial unique indexes on the
  live organization connection per provider + external account and the live
  personal connection per member) and `integration_sync_links` (external
  project ↔ space, `INBOUND` / `BIDIRECTIONAL`, status `ACTIVE` / `PAUSED` /
  `NEEDS_ATTENTION`, soft-deleted via `deleted_at`). Both use tenant RLS;
  `openfox_app` gets SELECT / INSERT / UPDATE. The provider catalog is static
  code, not a table. Expand-only; old binaries ignore the tables. Removal:
  `DROP TABLE openfox.integration_sync_links, openfox.integration_connections`.
- `000046_imports.sql`: imports (stage 6c). Adds `import_jobs` (one row per
  import: `DRAFT` → `QUEUED` → `RUNNING` → `SUCCEEDED` / `FAILED` /
  `CANCELLED`, plus `CANCEL_REQUESTED`, `ROLLED_BACK`, `DISCARDED`; config,
  preview, counters, warnings, an execution lease and optional sealed
  credentials for API sources; at most one queued / running import per
  space), `import_items` (one row per source record with the raw row, outcome
  and the written task version), `import_mappings` (space + source system +
  instance + external id → task; a partial unique index on live mappings
  prevents duplicate creation, rollback sets `released_at`),
  `import_created_resources` (statuses, labels and workflow entries created by
  an import, cleaned up on rollback when unused) and `external_accounts`
  (source-system users kept by name, optionally carrying an invitation and
  later linked to the member who accepted it). Adds nullable
  `tasks.external_assignee_id` (FK added `NOT VALID` then validated; the column
  is all NULL). `enqueue_automation_comment` and
  `enqueue_automation_task_status` return early when the transaction-local
  setting `openfox.import_job` is set, so imports do not fire automations.
  Adds `list_import_organizations` (control plane, organization IDs only) with
  read-only scan policies for `openfox_migrator` on `import_jobs`,
  `external_accounts` and `invitations`. Tenant RLS on all new tables;
  `openfox_app` gets SELECT / INSERT / UPDATE. Expand-only; old binaries do
  not read the new tables or column, and the trigger changes only apply when
  the setting is present. Full removal: restore the 000040 / 000044 trigger
  function bodies, drop `list_import_organizations` and the scan policies,
  drop `tasks.external_assignee_id`, then drop `import_created_resources`,
  `import_mappings`, `import_items`, `import_jobs` and `external_accounts`.
- `000047_imports_api.sql`: imports via the Jira API, new target spaces and
  sprints (stage 6c-2). `import_jobs.space_id` becomes nullable (a CHECK
  allows NULL only for `DRAFT` / `DISCARDED`, so a draft that targets a new
  space creates the space only when the import starts) and gains
  `space_created`; `source` also accepts `jira_server`;
  `import_created_resources.kind` also accepts `ITERATION`;
  `external_accounts` gains `source_label` and `link_method` (`''` / `email`
  / `invite` / `manual`, backfilled for already linked rows; the CHECK allows
  an empty method on linked rows so 6c-1 binaries keep working) plus a list
  index. `list_import_organizations` also returns organizations with API
  drafts that are still fetching. Constraints are added `NOT VALID` and then
  validated. Expand-only. Old binaries assume `import_jobs.space_id` is not
  NULL: before rolling the application back, run
  `UPDATE openfox.import_jobs SET status = 'DISCARDED' WHERE space_id IS NULL AND status = 'DRAFT'`.
  Full removal: restore the 000046 function body and the `source` / `kind`
  constraints, drop the new columns, constraints and index, and restore
  `NOT NULL` on `space_id` after deleting or filling rows without a space.
- `000048_github_integration.sql`: GitHub OAuth binding, webhooks and
  pull request / commit links (stage 6b-1). `integration_connections` gains
  `webhook_token_hash` (SHA-256 of the webhook URL token, partial unique) and
  `webhook_last_delivery_at`. New tenant tables: `integration_oauth_states`
  (OAuth sessions: state stored only as SHA-256, 10-minute expiry, single use,
  sealed PKCE verifier and a sealed pending token while an admin picks the
  accounts to bind), `integration_provider_settings` (provider-level toggles
  and the branch format), `task_git_links` (task ↔ pull request / commit,
  unique per task + repository + number / sha) and
  `integration_webhook_deliveries` (deduplicated by connection +
  `X-GitHub-Delivery`). Control-plane functions `resolve_git_webhook` and
  `resolve_integration_oauth_state` return only organization / object IDs,
  with read-only scan policies for `openfox_migrator`. `openfox_app` gets
  SELECT / INSERT / UPDATE. Expand-only; old binaries ignore the new tables
  and columns. Removal: drop the two functions and scan policies, drop
  `integration_webhook_deliveries`, `task_git_links`,
  `integration_provider_settings` and `integration_oauth_states`, then drop
  the two `integration_connections` columns, constraint and index.
- `000049_github_issue_sync.sql`: GitHub issue ↔ space sync (stage 6b-2).
  `integration_sync_links` gains `last_synced_at`, a partial unique index so a
  repository links to at most one space per connection, and another so a
  space has at most one bidirectional sync. New tenant tables:
  `task_issue_links` (task ↔ issue, one-to-one while linked, with a content
  hash used to drop echo events), `task_comment_links` (comment ↔ remote
  comment, deduplicated both ways) and `integration_sync_outbox` (a
  transactional outbox: pending updates per task are merged, the worker reads
  the current task when sending and retries with backoff). Statement-level
  triggers on `tasks` (INSERT / UPDATE) and `task_comments` (INSERT) enqueue
  outbound work for active bidirectional syncs only, and skip when the
  transaction-local `openfox.sync_source` (inbound writes) or
  `openfox.import_job` (imports) setting is present, preventing loops and bulk
  issue creation. `list_integration_sync_organizations` (control plane) finds
  organizations with due outbox items or running backfills. Expand-only.
- `000050_feishu_app.sql`: Feishu app binding, long connection, personal
  binding and Feishu notifications (stage 6d-1). `user_notification_channels`
  now accepts `feishu`. A global partial unique index keeps one Feishu App ID
  bound (not disconnected) in at most one organization, because long-connection
  events go to one random client. Another keeps a Feishu user (open_id under one app)
  bound to at most one member per organization. New tenant tables: `im_connection_states`
  (lease owner and connection state per Feishu connection, so only one server
  instance holds the long connection), `im_inbound_events` (dedupe by
  connection + event_id; the application purges rows after 7 days) and
  `im_notification_outbox` (a transactional outbox filled by a statement-level
  trigger on `notifications` when the recipient has an active personal Feishu
  binding under an active app; kind, summary and source are copied because
  notifications are readable only by the recipient). Control-plane functions
  `list_im_connections` and `list_im_notification_organizations`. Expand-only;
  before rolling back the application drop trigger
  `notifications_enqueue_im_delivery`, and delete `feishu` channel rows before
  restoring the old CHECK.
- `000051_feishu_bots.sql`: Feishu task assistants, agent bots and Feishu chat
  ↔ conversation messaging (stage 6d-2). `agents.builtin_kind` gains
  `TASK_ASSISTANT` (one shared built-in agent per space) and
  `conversations.origin` gains `IM`. New tenant tables:
  `im_app_registrations` (QR / device-code app creation sessions; the device
  code is sealed and a lease keeps one poller per session), `im_bots`
  (task assistant or agent ↔ organization-scope Feishu connection; one bot per
  agent, one task assistant per space), `im_chat_bindings` (Feishu chat ↔
  conversation), `im_inbound_messages` (messages received by bots, processed by
  a worker so the long-connection handler can ACK within 3 seconds) and
  `im_reply_outbox` (filled by a statement-level trigger on `messages` for
  agent and system messages in Feishu-linked conversations). A partial index
  finds personal Feishu bindings by `settings.union_id`. `run_space_tool_calls`
  records task-assistant space tool calls per run (idempotent replay and the
  5 create / 10 update per-run limits). Control-plane
  function `list_im_worker_organizations`. Expand-only; drop trigger
  `messages_enqueue_im_reply` before rolling back the application.
- `000052_account_bindings.sql`: personal account bindings page (stage 6e).
  `external_accounts.link_method` gains `verified` (a member proved their Jira
  identity via `/myself` and the system linked that identity's external
  accounts to them). Personal Jira bindings reuse `integration_connections`
  (scope PERSONAL, no stored credentials); a partial unique index keeps one
  Jira account per site bound to at most one member. Expand-only; before
  rolling back, rewrite `verified` rows to `manual`.
  Before rolling the application back, drop the three `*_enqueue_issue_sync*`
  triggers (otherwise the queue only grows). Full removal: drop the triggers
  and functions, drop `integration_sync_outbox`, `task_comment_links` and
  `task_issue_links`, then drop the two indexes and `last_synced_at`.
- `000053_space_clone.sql`: clone a space into another organization (stage 7a).
  Reuses `import_jobs` (`source = 'space_clone'`, owned by the target
  organization) with new columns `source_organization_id`, `source_space_id`
  and `bundle_key` (snapshot package reference, no cross-organization foreign
  key). Adds statuses `CLEANING` / `CLEANED` / `CLEANUP_FAILED`, phases
  `creating_space` / `comments` / `cleaning`, and created-resource kinds
  `CUSTOM_FIELD` / `PROJECT` / `RELATION_TYPE`; `space_id` may be NULL for a
  clone until the worker creates the space. `list_import_organizations` also
  returns organizations with a `CLEANING` job. Expand-only; before rolling the
  application back, move `space_clone` jobs in the new statuses to `FAILED`
  and restore the 000046/000047 constraints and function.
- `000054_task_exports.sql`: export task data (stage 7b). New table
  `export_jobs` (CSV or JSON snapshot packages for up to 20 spaces, file kept
  7 days in the artifact store, lease/retry columns like `import_jobs`) and the
  control-plane scan `list_export_organizations`. Nothing existing is altered.
  Rollback: delete the stored `export/` files, drop the function and the table.
- `000055_attachments.sql`: task and conversation attachments (stage 7c). New
  table `attachments` (`owner_kind` TASK or CONVERSATION; conversation files are
  uploaded unbound and bound to a message when it is sent; one artifact-store
  object per row, 20 MB limit). RLS is tenant isolation only; read/delete
  permissions are enforced by the application. Nothing existing is altered.
  Rollback: delete the stored `attachments/` files, then drop the table.
- `000056_quick_replies_crew_conversations.sql`: quick replies and crew
  conversations (stage 7d). New table `quick_replies` (PERSONAL or ORG scope,
  soft delete, optimistic `version`; permissions in the application). Adds
  nullable `conversations.crew_id` and `conversation_inputs.target_agent_id`,
  and relaxes `validate_run_scope` so a Run in a crew conversation may use any
  crew member Agent (non-crew conversations validate exactly as before).
  Expand-only. Rollback: make sure no conversation has a non-NULL `crew_id`,
  restore the 000011 `validate_run_scope`, drop the two columns and the table.
- `000057_task_favorites.sql`: task favorites (stage 7e). New table
  `task_favorites` keyed by (organization, user, task); RLS limits rows to the
  owning member. Read permission is checked by the application when starring.
  `tasks.start_date` already exists (000025), so the Gantt view needs no column.
  Nothing existing is altered. Rollback: drop the table (only favorites are lost).
- `000058_lark_provider.sql`: Lark (stage 7g-1), the international Feishu
  with the same open-platform protocol. Adds three partial indexes for
  `provider = 'lark'` (app unique, user unique per app, `union_id` lookup) that
  mirror the Feishu ones, and widens `enqueue_im_notification` and
  `list_im_connections` from `'feishu'` to `IN ('feishu', 'lark')`. Nothing is
  dropped or tightened. `CREATE INDEX` is not concurrent (single transaction) and
  holds a SHARE lock on `integration_connections`; run it off-peak on large
  tables. Rollback: disconnect Lark connections, drop the three indexes, and
  restore the two functions from 000050.
- `000059_dingtalk_provider.sql`: DingTalk (stage 7g-1). Same shape as 000058
  for `provider = 'dingtalk'`: three partial indexes (AppKey unique, user unique
  per app, `union_id` lookup) and `enqueue_im_notification` /
  `list_im_connections` widened to `IN ('feishu', 'lark', 'dingtalk')`.
  Nothing is dropped or tightened; same non-concurrent `CREATE INDEX` SHARE-lock
  note as 000058. Rollback: disconnect DingTalk connections, drop the three
  indexes, restore the two functions from 000058.
- `000060_wechat_binding.sql`: WeChat ClawBot (iLink) QR binding of an Agent
  (stage 7g-W). Four new tables: `wechat_qr_sessions`, `wechat_bindings`
  (bot token sealed, long-poll cursor and lease), `wechat_inbound`
  (dedup and queue) and `wechat_reply_outbox`, all with forced tenant RLS and
  migrator scan policies; the trigger `messages_enqueue_wechat_reply` enqueues
  Agent/system messages of a bound conversation; two control-plane functions
  `list_wechat_bindings` and `list_wechat_worker_organizations`. No existing
  table, constraint or function is changed (no CHECK is relaxed). The only lock
  on an existing table is the short trigger creation on `messages`. Rollback:
  drop the trigger first, unbind in the UI, then drop the functions and tables.
- `000061_git_providers.sql`: GitLab (self-managed and JiHu) and Gitee (stage
  7g-2). Adds `integration_connections.token_expires_at` (nullable) with a
  partial index for the token-refresh scan, `task_issue_links.issue_ref`
  (`NOT NULL DEFAULT ''`, length CHECK added `NOT VALID` then validated, used by
  Gitee's alphanumeric issue numbers) and the control-plane function
  `list_git_token_refresh`. Existing unique indexes already carry `provider`
  (or the parent connection), so none are rebuilt. Nothing is dropped or
  tightened. `CREATE INDEX` is not concurrent (single transaction) and holds a
  SHARE lock on `integration_connections` (blocks writes, not reads); run it
  off-peak on large tables. Rollback: disconnect GitLab / JiHu / Gitee
  connections, drop the function, the index, the constraint and the two columns.
- `000062_conversation_overrides.sql`: per-conversation Runtime / model /
  permission-mode overrides (stage 8d-1). Adds `conversations.runtime_override`,
  `model_override`, `approval_mode_override` (nullable) and `overrides_version`
  (`NOT NULL DEFAULT 0`, optimistic lock), plus the run snapshot columns
  `runs.runtime`, `model`, `approval_mode` (nullable; NULL = a run created
  before this migration, read back from the config version). Adds CHECKs
  (all `NOT VALID` then `VALIDATE`, so only brief ACCESS EXCLUSIVE locks):
  override value sets, crew conversations carry no override, the snapshot is
  all-or-nothing, and `agent_config_versions.approval_mode` is limited to the
  four modes (the migration aborts atomically if a historical row holds any
  other value; run the pre-check query in the file header first). Extends
  `runs_wait_reason_valid` with `RUNTIME_UNAVAILABLE` and makes
  `emit_conversation_event` also fire when `overrides_version` changes. Old
  binaries ignore the new columns, so rolling back the application is enough;
  the column-drop SQL in the header is not recommended.
- `000063_reservation_agent_device_check.sql`: lets an Agent change computer
  after it has run (stage 8g). The 000002 foreign key
  `capacity_reservations (organization_id, agent_id, device_id) → agents
(organization_id, id, device_id)` also covered long-released reservations, so
  any `UPDATE agents SET device_id` on an Agent with history failed with 23503.
  Replaces it with `capacity_reservations_agent_fk` on `(organization_id,
agent_id)` (`NOT VALID` then `VALIDATE`) plus the trigger
  `capacity_reservations_agent_device`, which requires the reservation device to
  equal the Agent's current computer only on INSERT or when
  `organization_id` / `agent_id` / `device_id` change (same 23503 code), then
  drops the old key. No data is rewritten; the unused `agents` UNIQUE
  `(organization_id, id, device_id)` is kept. Locks are brief; `VALIDATE` scans
  `capacity_reservations` once without blocking reads or writes. Old binaries
  only insert matching reservations, so rolling back the application is
  enough; re-adding the old key is not recommended (see the file header).
- `000064_task_move.sql`: moving a task to another space (stage 9c-1).
  Adds `task_display_aliases` (`(organization_id, space_id, sequence)` primary
  key → `task_id`, plus `moved_at` / `moved_by`) so an old display ID keeps
  resolving after a move; forced tenant RLS in the same style as 000048 /
  000049; `openfox_app` gets `SELECT, INSERT, UPDATE` only. Its foreign keys to
  `tasks` and `spaces` are `ON DELETE CASCADE` (the application only
  soft-deletes, so aliases are untouched; a manual hard delete removes them
  instead of being blocked). Replaces the `(organization_id, space_id,
task_id)` foreign keys of `task_git_links` and `task_issue_links` with
  `task_git_links_task_fk` / `task_issue_links_task_fk`, which add `ON UPDATE
CASCADE` (`NOT VALID`, then `VALIDATE`, then drop the old key), so
  `UPDATE tasks SET space_id` no longer fails with 23503 for tasks with PR /
  commit links or Issue sync (this also fixes retiring a space with
  `open_tasks = move`). No data is rewritten or backfilled. Locks are brief;
  `VALIDATE` scans the two link tables once without blocking reads or writes.
  Old binaries ignore the new table, so rolling back the application is
  enough; the header has the pre-check, verification and full rollback SQL
  (rollback drops all aliases).
- `000065_task_quick_commands.sql`: task quick commands (stage 9f), the
  quick-command group in the task detail "summon Agent" menu. New table
  `task_quick_commands` per space: `PRIVATE` (owner only) or `SHARED` (readable
  by everyone who can read the space; edited by the creator and space
  administrators, enforced in the application), exactly one of `agent_id` /
  `crew_id`, optional icon (`icon` / `emoji` / `upload`, same values as view
  icons; the uploaded icon is checked by the application), prompt up to 4000
  characters, soft delete, optimistic `version`, two partial list indexes.
  Foreign keys reference agents and crews by `(organization_id, id)` only,
  because both can change space; the same-space rule is checked by the
  application. Forced tenant RLS; `openfox_app` gets `SELECT, INSERT, UPDATE`.
  Nothing existing is altered; locks are brief. Rollback: old binaries ignore
  the table; if needed, `DROP TABLE openfox.task_quick_commands`.
- `000066_task_trash.sql`: task trash ("recently deleted", stage 10e). Adds
  the partial index `tasks_trash_idx` on `openfox.tasks (organization_id,
space_id, deleted_at DESC, id DESC) WHERE deleted_at IS NOT NULL` for the
  per-space deleted-task list (keyset pagination by `deleted_at DESC, id
DESC`). No table, column or data change; deletion stays the existing soft
  delete and restore clears `deleted_at` / `deleted_by`. The runner executes
  files in a transaction, so the index is built without `CONCURRENTLY` and
  briefly blocks writes to `tasks` while it scans the table. Rollback: old
  binaries ignore the index; if needed, `DROP INDEX openfox.tasks_trash_idx`.
- `000067_invitation_expiry_rejection.sql`: fixes expired invitation acceptance
  returning a tenant-context/RLS error. Replaces `accept_invitation` with the
  same signature, ownership and grants; the expired branch raises the existing
  `invitation_conflict` without attempting an UPDATE that would be rolled back
  by that exception anyway. No schema or data backfill. Apply after `000066`
  in the normal migration transaction. Rollback, if required, is a new forward
  migration restoring the function from `000037`; that also restores the old
  expired-invitation defect. Do not edit already-applied migration checksums.

- `000068_document_revisions.sql`: adds document/sheet history with an append-only
  snapshot trigger and tenant/current-node visibility RLS. Apply after `000067`.
  It does not backfill every document: the first subsequent content edit retains
  the prior nonempty value, then snapshots the edit. Moves do not create content
  revisions. Restoring a version creates a new node version and history entry.
  The new table/indexes are initially empty; installing the trigger briefly locks
  `doc_nodes`. Old clients remain compatible. Roll back application binaries
  while retaining this table and trigger; do not drop history or rewrite applied
  migration checksums. No destructive rollback SQL is required.

- `000069_human_direct_chat.sql`: adds human-only private chat, ordered/idempotent
  messages and pending/sent attachments; pair uniqueness, composite foreign keys,
  participant RLS and list/message indexes. Apply after `000068`, before the new
  application. No existing rows are changed. Roll back the application while
  retaining these tables and their stored files; do not delete chat data.

- `000070_conversation_status_mark.sql` adds a nullable shared conversation display mark and includes its changes in the existing conversation update event. Apply before updating the API. Old rows need no backfill. Roll back the application first and retain the column to preserve marks.

The application role is deliberately not a table owner, superuser, or
`BYPASSRLS` role. It cannot execute the trusted context setter or capacity
functions or directly update Agent/device capacity state. A separately
authenticated context-capable database role establishes the context, and the
application transaction wrapper may then use
`SET LOCAL ROLE openfox_app` for tenant DML. The context table is temporary and
`ON COMMIT DELETE ROWS`, so a pooled connection cannot retain context after the
transaction ends. The migration revokes database `TEMP` from `PUBLIC` and the
runtime roles so only the security-definer migration owner can create that
temporary context table; this assumes the OpenFox database is dedicated to the
application rather than shared with unrelated workloads.

## Rollback and safety

These initial migrations have no automatic down migration. They create the
database security boundary and tenant data model; dropping them is destructive
and must only be done on a disposable or explicitly backed-up database during
an approved maintenance window. A rollback of an uncommitted installation is
to discard the temporary database or restore its backup, not to run an
unreviewed `DROP SCHEMA`. Future migrations must use an expand/migrate/contract
sequence and document their own reversible SQL or recovery procedure.

Migration files use six digits followed by a lowercase ASCII slug, for example
`000001_db001_control_roles.sql`. `openfox-server migrate --check` rejects
duplicate numbers and does not execute SQL. `openfox-server migrate --apply
--database-url-file <path>` is the explicit forward-only execution path; it
serializes runners with a PostgreSQL advisory lock, rejects changed or
out-of-order history, and has no down-migration mode.

The runner-owned `openfox_meta` schema is operational metadata rather than
tenant data. It is created before the first migration so `000001` itself is
recorded; the bootstrap URL therefore needs permission to create the schema and
to `SET ROLE openfox_migrator`. The metadata schema is not granted to
`openfox_app`.

- `000071_task_comment_threads.sql` adds comment edit versions and root discussion resolution. Apply before the API. Existing threads are open; retain the columns on rollback.
- `000072_comment_deletions.sql` keeps a per-comment tombstone and hides the comment and its notifications with restrictive read policies. Retain these policies on application rollback.
- `000073_conversation_offers.sql` stores recipient consent before changing conversation membership or ownership. Apply before the API, which reads offers for inbox counts and private chat cards. No backfill.
- `000074_triage_favorites.sql` stores personal triage favorites per organization, account and space under owner-only RLS. Apply before the API reads space details; no backfill. Retain the table on application rollback.
- `000075_assistant_message_action.sql` stores the optional creation action selected in the personal assistant on the user message. Apply before updating the API/dispatcher; old messages require no backfill. Retain the nullable column on application rollback.
- `000076_task_invitation_assignees.sql` and `000077_task_invitation_lifecycle.sql` reserve a task seat for a pending organization invitation, then settle it on acceptance, revocation or expiration. Apply both in order before the API; no backfill. Retain both when rolling back the application.
- Apply 000080 through 000083 in order before updating the center and device executors. These migrations store Agent MCP connections, short-lived OAuth attempts, verified human input provenance, and private device inventory. Retain them on application rollback.

- `000084_agent_market_skill_installs.sql`: stores each Agent's pinned market version and private file manifest separately from the space library. The tenant-scoped loadout view includes historical bindings and new installs; there is no backfill. Apply before the center upgrade.
- Apply 000085 (inbox event policy), 000086 (Agent social actions) and 000087 (native direct message preference) before updating the center and workers. Historical deliveries are not backfilled; reverting a binary alone will not revert triggers.
- Apply 000100–000104 in order before updating import workers and repository authorization. They add status baselines, stable comment IDs, API project mappings, explicit repository connections, and outbound comment delivery records. Historical repositories remain anonymous and historical comments are not replayed. Preserve delivery records and source mappings on rollback.
- Apply `000105_reasoning_and_git_defaults.sql` before the updated center, daemon and client. It adds conversation reasoning overrides, immutable run Git snapshots, and tenant-isolated organization/space Git defaults. Existing Agent author modes remain unchanged. Retain the additive schema on rollback, but drain new-semantics runs and restore explicit Agent modes before reverting to an older center.
- Apply `000106_git_default_sources.sql` after 105 and before the updated API/dispatcher. It adds organization/space author source modes, maps nonempty legacy values to MANUAL, and preserves old empty-value semantics and Run snapshots. Temporary migrator read/update policies are removed in the same transaction. Old clients may omit modes, but old/new writers must not run concurrently. Retain additive columns and restore old-expressible settings before application rollback.
- Apply `000110_assistant_conversation_transfer.sql` after 109 and before enabling the updated conversation-transfer API. It replaces only `validate_conversation_scope`: a private built-in assistant belonging to the current conversation owner may reside in a different space; ordinary Agents and task scope remain unchanged. No columns, backfill, grants or historical Runs change. Retain this compatible function and accepted ownership on rollback; older binaries cannot safely resume cross-space transferred sessions without verification. Tested only in an isolated database, not permanent QA or production.
- Apply `000111_runtime_session_export.sql` before enabling the updated API, device executor and client. It widens the device command kind and adds an RLS-protected export table for metadata, an RSA-wrapped AES key and a one-hour ciphertext object; session payloads are never stored in PostgreSQL. The cleanup function exposes only expired export rows to the context worker, which deletes objects before rows and retries failures. There is no backfill. Codex rollout JSONL staging stays local on the exporting computer for at most 24 hours. For application rollback, stop new exports and drain in-flight commands, then retain the additive schema; no production down migration is provided. The complete migration runner was applied twice in a fresh network-disabled PostgreSQL 16 container (111 then 0 migrations).
- Apply `000112_email_notifications.sql` before starting the email notification worker. It broadens the account notification channel CHECK to include `email` and creates an empty tenant-isolated outbox with a unique notification key, a notification insert trigger, and a control-plane due-work scanner. Only users who explicitly enabled email are queued; old notifications are never backfilled. Pending mail expires after seven days; terminal rows are removed after 30 days. Without SMTP, the worker only expires and cleans up old items, leaving recent pending mail available if SMTP is restored. Changing the CHECK scans and briefly locks the account preference table; adding the trigger briefly locks `notifications`. No new environment key is needed; the worker uses the existing `SMTP_*` settings. To roll back the application, stop the worker and `DROP TRIGGER notifications_enqueue_email_delivery ON openfox.notifications`; retain the additive table and CHECK. Restoring the old CHECK additionally requires removing `email` preference rows, which loses those user choices and therefore needs explicit approval. Do not rewrite an applied migration.
- Apply `000113_wecom_bot_binding.sql` after 112 and before deploying the WeCom API/workers. It adds tenant-isolated one-time code and personal notification-target tables, global uniqueness for an active WeCom Bot ID across organizations, scoped uniqueness for active WeCom personal user/member bindings, expands the notification channel CHECK to `wecom`, and replaces the IM connection scanner and notification enqueue function. No data backfill; old notification history is not replayed. The CHECK replacement scans and briefly locks the preferences table, and each new index scans its existing connection table. Existing EXECUTE grants on replaced functions are retained and the scanner grant is restated. On application rollback, stop WeCom bot creation and workers, retain bound data and the expanded CHECK; restoring the pre-WeCom trigger and scanner requires a forward migration after draining WeCom connections and outboxes. Do not alter an applied migration checksum.
- `000116_notification_event_overrides.sql` (HiFox N01) adds `event_overrides={}`, `muted_groups={}`, and a sequence-backed positive `revision` to the existing account channel rows. The volatile revision default assigns distinct non-reused values to old rows and may rewrite/lock this table; measure its size before an authorized deployment. It creates the shared kind/leaf decision function and a tenant/outbox-constrained SECURITY DEFINER boolean accessor, retaining recipient-only notification RLS and comment tombstones. The snapshot trigger retains the four previously discarded off events only after an explicit enabled channel opts in; no history is replayed. Apply after 115, before policy-aware API/workers/client together; old workers must not consume alongside fine settings.
- `000117_independent_im_notification_channels.sql` (HiFox N02) widens the existing CHECK for Lark/DingTalk and inserts only missing rows from saved Feishu choices. `direct_messages` is always initialized false; absent Feishu rows stay virtual defaults. The existing IM enqueue function accepts new-provider private messages only after their own explicit consent. Apply after 116. No environment keys/dependencies/new worker/outbox or historical delivery backfill. Preserve both migrations on application rollback: use the reviewed conservative shutdown projection in `docs/development-records/sql/2026-11-13-notification-conservative-rollback.sql` after backups and stopped writers/workers, or a policy-aware compatibility binary. Direct old-binary rollback ignores fine choices and is unsafe. The projection intentionally disables all channels for affected users, retains fine values, rejects unresolved SENDING rows and skips pending jobs. This batch only rehearsed isolated PostgreSQL 16; no production deployment is authorized.

- `000123_space_default_view.sql` (HiFox T03) adds nullable `spaces.default_view_id`, a same-organization/same-space composite FK to `views`, and its supporting unique key. The FK deletes only the pointer with `SET NULL (default_view_id)` (PostgreSQL 16). It extends the existing space UPDATE event trigger without duplicating events, preserving all previous columns. Apply after 122 before the new space/view API and client. Historical values stay NULL; no backfill, environment keys, dependencies, RLS or grants change. Unique index/FK creation and ALTERs require DDL locks: only isolated small-data migrations were rehearsed. On application rollback retain the additive schema, pointers, source filters/display and ledger; old clients ignore this new display baseline while existing task ACLs remain. Do not DROP data or rewrite applied checksums. Full SQL, concurrency tests, nullable-history/re-run checks and retained-data backup/restore rehearsal are recorded in `docs/development-records/2026-11-13-hifox-parity-t03.md`.

- `000126_conversation_forks.sql` (HiFox C04) adds nullable parent/message/prefix metadata with a consistency CHECK; no backfill, source FK, new grant or configuration. Apply the complete 114–126 chain in order before the new API; do not skip the separately owned 125 while this batch is being integrated. The tested 125 SHA256 is `99d55e8a18377b2830e2046b82e5fdf5802ecfd266054d0b5a58729b25e8a30b`. Fresh isolated 113→126 and a second zero-change run passed. ALTER TABLE requires a DDL lock; production scale/locks are unverified and execution is not authorized. Keep snapshot data, prefix metadata, read-only proposal gates, historical Run isolation and dynamic reference ACLs on rollback. Reverting directly to a pre-fork server could make copied pending actions executable and is unsafe. Full SQL and isolated backup/restore evidence (10 forks, 539 prefix messages) are in `docs/development-records/2026-11-13-hifox-parity-c04.md`.

### HiFox A04 / I03 / R06 增量（2026-11-13）

本批125（工作时间）、127（CNB工作/回链）、128（电脑Runtime默认值）已在与本目录相同谱系的隔离库验证，113→128共15项、再次执行0项。126属于C04已交付。禁止与其他分支同号SQL混用；应用要求先执行对应迁移，回退保留列/工作记录/密文，且须满足各单项的停用、排空和能力依赖门禁。未执行生产迁移。

完整SQL、验证与非破坏回退见 内部记录「A04」（独立源码包未附）、内部记录「I03 SQL」（独立源码包未附）、内部记录「R06」（独立源码包未附）。无新增环境变量；R06复用已有密钥文件设置。

### HiFox I19 增量129（2026-11-13）

`000129_wecom_group_authorizations.sql` SHA256 `e8054e410236bb6b889f35b3c2ddb969dafdb9ad052ba3f9c74deb6dbc311754`：一张群授权历史表、两列可空队列版本、发现索引及既有回复触发函数增量，无旧数据回填。主树隔离库113→129共16项、二跑0；备份恢复七表指纹一致。新服务/worker前先执行129。新增环境键/依赖无。

完整SQL及回退门禁见 内部记录「I19 SQL」（独立源码包未附）。完整旧应用回退须先移除有群历史的机器人并排空运行/队列（同时影响该bot私聊/通知）；不能只关新字段后启动不理解它的旧应用。保留表/列/消息/任务，未执行生产。

### HiFox I08 增量130（2026-11-13）

`000130_codeup_mr_work.sql` SHA256 `18d6cfee5bed604c36e3c58d36342c9d2587a7cb0de336de934dff87cdd9e876`：新增租户隔离的 Codeup MR 工作表、业务唯一键/租约/回链结果状态及扫描函数的 Codeup 分支，保留127的CNB分支。无历史回填。主树隔离库113→130共17项、二跑0；先迁移，再更新API/SyncWorker/UI，最后显式选库并手工配置平台webhook。

回退先取消Codeup选库并停止对应接收/执行，等待在途请求结束，再恢复迁移前扫描函数并回退应用；保留新表、Git链接、投递及密文，不重发未知评论。完整SQL和演练见内部记录「I08记录」（独立源码包未附）。新增环境配置/依赖无，仅隔离库执行，未操作生产或真实云效平台。

### HiFox R03 增量132（2026-11-14）

`000132_codebuddy_runtime.sql` SHA256 `a0dbbeb64ed091d040c18abff92e5ae8f27ce6ef457a098888b6fc865d1681e6`：只扩展 conversations 与 device_runtime_defaults 的 Runtime CHECK，保留其他约束值，无数据回填/RLS/授权变更。131保留给未批准P03，没有占位。主树隔离113→114…130→132执行18项，二跑0；未来补入131时本次一次性库须重新克隆完整链，不能倒插历史。

先迁移再更新应用；回退先停止CodeBuddy新执行并排空/取消Run、显式归档或重配Agent，保留CHECK/密文/历史，不自动替换Runtime。完整SQL/配置/隔离回退与未验证边界见内部记录「R03」（独立源码包未附）和内部记录「主树验收」（独立源码包未附）。仅隔离库执行，未部署。

### HiFox P01/P02/I04 增量133–135（2026-11-14）

仅隔离库执行；完整当前清单为1…130、132、133、134、135。131仍保留给未批准P03，无占位SQL。runner要求已应用历史是当前文件清单的前缀；验证必须从113模板新克隆，不能向已应用134/135的旧测试库倒插132/133。

- `000133_tapd_personal_identity.sql`：同组织非断开TAPD身份ID与nick唯一索引，无回填。SHA256 `65499e4cf6e5d7ab3700196994db84aa64bb1ba24617b9d81bd85a1f6247e23e`。索引扫描已有连接，发现冲突即失败，不删数据。内部记录「P01完整SQL/演练」（独立源码包未附）。
- `000134_teambition_personal_identity.sql`：企业主体唯一索引及个人身份无秘密形状CHECK，无回填。SHA256 `6886e033fcc60fe8c4f630e1cfcd92289fb39b290a8739f6f7edbd37d8f3c918`。内部记录「P02完整SQL/保留数据恢复」（独立源码包未附）。
- `000135_yunxiao_workitems.sql`：导入source CHECK增加yunxiao_api，云效身份唯一与无秘密CHECK；保留全部原source值，无回填。SHA256 `9e4e4ade5bc3f3e7720ef83114f8789629e7c224adf2d180078fca975f936db4`。内部记录「I04完整SQL/停用回退」（独立源码包未附）。

先应用迁移，再启动本版API、导入/同步worker和客户端。索引及CHECK验证需要扫描和DDL锁；只测了隔离小规模，不代表生产锁窗口。无新增环境变量/依赖；P02沿用PUBLIC_URL、密钥文件和已有企业应用，须另外登记回调与目录scope，真实应用配置未操作。

回退先停新绑定、导入/同步入口和相关worker并排空事务，保留迁移、身份与来源映射、人工映射和已导入任务。旧worker不了解verified_binding_id时不得继续处理这些配置。I04另有按组织停用/清密文的回退脚本，须按记录执行；不得删任务、收缩CHECK或改迁移历史来模拟回退。三包合并后的验证结果见内部记录「batch12」（独立源码包未附）。


### HiFox I12 增量137（2026-11-14）

`000137_azure_devops.sql` SHA256 `7750f4e67b83d9f049ff5ada3089de0a8825372e3e457f4157e05945d5d25582`：Azure 共享 OAuth grant、身份 claim、owned Hook、事件 work、PR 回链、Boards crawl/node 共七张租户隔离表；扩展连接引用、导入源、OAuth 密文上限、来源 revision 与 Git 展示名。组合外键、RLS、唯一约束及有界扫描函数随文件建立；无业务回填或远端调用，原 Git 来源唯一索引保留。完整 SQL、配置与锁影响见 内部记录「I12 交付记录」（独立源码包未附）。

先迁移，再更新 API/同步 worker/UI，最后配置两个独立 Entra 应用并显式授权。ALTER/CHECK/FK/generated 列涉及 DDL 锁与已有行验证，只在批准的隔离小库验证。根从113模板另建库，按114…130、132…138执行24项，总137条账本逐SHA匹配，二跑0；131仍未实施，不能向已应用更高号的旧私库倒插137。

回退先停止新 Azure 意图、使连接失效并清理自有 Hook/在途回链，再撤应用配置；保留导入副本、Git links、映射、UNKNOWN 和必要人工清理坐标以及扩展 schema。清理凭据不刷新，最长当前 token 到期与24小时的较小值；已发远端评论不自动删除。本地解绑不等于撤销远端 consent。没有执行破坏性 down SQL 或生产迁移；根联合验收见 内部记录「第十四批记录」（独立源码包未附）。

### HiFox R05 增量138（2026-11-14，实施中）

`000138_hermes_runtime.sql` SHA256 `37eb7bd78e25e3781f9574acd7204bce644c170eaebc783e06a0130e47e8afdd`：只扩展132的 conversations 与 device_runtime_defaults 两处Runtime CHECK以接受hermes，原值/其他条件保留；无数据回填、RLS、grant或新表列。先迁移再更新中心/daemon/UI。仅独占隔离库验证，113后应用23项（114…130、132…136、138）、二跑0；137属于并行I12，root集成后须从完整谱系重新克隆，不能向该一次性库倒插137。

回退先停止新Hermes执行、取消/排空Run、显式归档/重配Agent，再回退应用，保留CHECK/密文/业务数据，不自动替换Runtime。ALTER/VALIDATE需要DDL锁与扫描，未验证生产规模且未授权生产执行。R05搜索及硬崩溃收尾仍有未完成边界，本迁移通过不等于工作包完成；完整SQL/配置/证据见内部记录「R05实施记录」（独立源码包未附）。


### HiFox P03 增量139（2026-11-14）

`000139_shortcut_personal_identity.sql` SHA256 `9688f236d232411d13c4d4fa2655115de6374dd74cf0501b63d4aefe10817137`：Shortcut PERSONAL身份同组织/workspace/外部UUID部分唯一索引，及本人身份/默认无Token、显式评论授权才有密文的形状CHECK。无业务回填、新表/列、RLS或全局配置变化。131继续空缺，不再作为P03号；前文131预留仅是历史阶段记录。

先迁移139，再API/worker与UI。独占新库从113完整应用114…130、132…139共25项，账本138条逐SHA匹配，二跑0；两项DDL回退/原SQL恢复演练前后账本和6类业务摘要不变，未改迁移历史。索引和CHECK验证需扫描/DDL锁，仅验证隔离小库。应用回退先关闭个人评论并排空在途请求，停止不了解verified来源的旧worker，保留扩展schema/已导入副本和去重记录。完整SQL、配置、失败、回退及未验边界见内部记录「P03记录」（独立源码包未附）。未使用真实平台或生产环境。

### HiFox I09 增量140（2026-11-14）

`000140_gitcode.sql` SHA256 `ae952f1fe5841f353fe1b08ad1abdedc5a2d6c1abfaa2f14d2c02c96205d3c73`：GitCode stable repository与durable work两表、复合租户FK/active repo唯一/RLS/lease/due索引，扫描函数仅追加GitCode、保留CNB/Codeup分支。PULL工作仅保存PR REST版本摘要。无业务回填、无个人身份/克隆或新全局配置。131保持空缺。

先140，再API/worker/UI；两独占新库自113完整应用26项、二跑0，139账本逐SHA一致。回退专库事务内恢复130 scanner、撤两表/index后ROLLBACK，账本/schema/scanner前后指纹一致；未执行生产或已提交破坏性down。实际运营回退先停用/处理owned Hook/断开，保留schema、本地副本、映射和UNKNOWN审计。DDL锁/扫描只在隔离小库验证。完整SQL顺序、失败和配置/回退/未验边界见内部记录「I09记录」（独立源码包未附）。


### HiFox I11 增量142（2026-11-15）

`000142_bitbucket_cloud.sql` SHA256 `4d535fb4c1025d08d30ee4887f32111032a918eb461294496498b911848c632c`：Bitbucket runtime、仓库/空间选择、work/delivery/backlink/comment-source共七表，tenant FK/FORCE RLS、最小授权、身份形状/唯一及密文上限、QUEUED CHECK和有界scanner；保留140/141及其他provider扫描条件。不改公共GitLink索引，无业务回填。完整SQL/配置/风险见内部记录「I11交付」（独立源码包未附）。

先142，再API/worker/UI，再配置两个独立consumer及显式授权、手工Hook。主树独占113模板新库顺序应用114…130、132…142，首28/二0，141账本逐SHA匹配；131仍空缺，不向旧库倒插或改checksum。DDL锁/扫描仅验证隔离小库。回退先用当前版断开连接并停新接收/调度，保留UNKNOWN/历史关联/来源审计及扩展schema，再回退应用和配置；实际浏览器及Go覆盖停用保留数据。破坏性down SQL为无，未删表/库或生产迁移。


### HiFox I07 入站增量143（2026-11-15）

`000143_bugly_inbound.sql` SHA256 `80182958bfb22b9d54fd52320cb21512216cba79d386903dee5b819ef2c120bf`：专属绑定/来源两表及唯一、复合租户FK、RLS/最小授权、秘密入口解析。没有业务回填、全局配置或扫描函数改动；完整语句、执行顺序和风险见内部记录「I07主树交付」（独立源码包未附）。

先142再143，再API/UI。主树新私库从113应用114…130、132…143首29/二0，142账本项且131空，142143 SHA精确核对。回退使用当前版暂停/断开保留任务与来源，再回退应用；实际非破坏回退已演练。无破坏性down或生产执行。I07整包仍有外发协议缺口，迁移通过不代表全包完成。


### HiFox I13 增量144（2026-11-15）

`000144_codecommit.sql` SHA256 `43d7687a0f56bbcdbf22a7a78b7e76fb2a2671600d477de4a8ad3241dc6f6df2`：新增 CodeCommit 仓库、引用水位、持久工作三表及 delivery 可空摘要列，组合租户外键、唯一、FORCE RLS/最小授权、租约与 due 索引；桥接解析函数及累积 scanner 保留142既有平台分支。无历史业务回填，不改公共 GitLink 唯一索引。完整 SQL 位于该迁移文件，行为与风险见内部记录「I13控制面记录」（独立源码包未附）及内部记录「I13最终交付」（独立源码包未附）。

先144，再API/SyncWorker/UI，最后显式配置IAM、选仓并由操作者自行部署桥接。根两独占新库从113模板按114…130、132…144执行首30/二0；143条账本逐SHA匹配，131保持空缺。DDL锁/约束/扫描只在隔离小库验证，不代表生产规模或真实AWS部署。没有新OpenFox全局环境键，AWS桥接的四个配置键单独见[桥接说明](../../docs/integrations/codecommit/README.md)。

回退先使用当前版禁用仓库或断开连接，停新事件/扫描与工作，清凭据并使在途租约失效；保留schema、Git关联、投递摘要、固定发送意图及UNKNOWN，再回退应用。未知评论不重置token或自动重发，远端评论不删除；实际PG及浏览器覆盖禁用/断开/轮换保留数据。无破坏性down SQL、生产迁移或真实AWS写入。

### HiFox I18 增量146（2026-11-15）

`000146_telegram.sql` SHA256 `8526eeb82c33d259c36a68b50bd8a8997ecfcb7beabcab068be8e7e614370977`：Telegram控制、身份、群、不可变来源、回复等12张专属表及复合FK/唯一/FORCE RLS/最小GRANT；输入校验、队列预算、秘密路由与累积IM扫描函数。复用145真实source_run_id，无业务回填、依赖或新全局配置。完整语句与锁/权限见内部记录「I18服务记录」（独立源码包未附）。

先145再146，再API/worker/UI，最后显式连接。根新库自113首32/二0，145条账本逐文件SHA一致，131空缺；只在隔离PG16验证，未修改模板或已应用账本。逆DDL仅独占库事务内演练后ROLLBACK，12表及账本恢复。回退先断开、停专属入口/worker，保留schema、历史、固定Bot归属与UNKNOWN；不重发、不自动删除远端Webhook、不提交破坏性down。最终验收见内部记录「I18交付」（独立源码包未附）。


### HiFox R02 增量151（2026-11-15）

`000151_gemini_runtime.sql` 仅扩展 conversations.runtime_override、device_runtime_defaults.runtime 两处 CHECK 加入 `gemini`，保留原有值及全部权限/版本条件；无新表、列、数据回填或 RLS 变更。SQL 全文及锁/验证顺序在迁移文件中。先按当前累计链执行151，再更新中心/daemon/UI；131、147保持空缺，不修改已执行文件或账本。

新隔离库 `parity_r02_20261115` 从113模板首轮36项、二轮0项，149条账本逐文件SHA一致。仅验证隔离小库，DDL会获取两表锁并校验已有行，不代表生产规模。回退先取消/排空Gemini Run、显式归档或重配相关Agent，再回退应用；保留扩展CHECK、已有加密设置和运行记录，不自动切换Runtime，不执行收缩或删除SQL。整包实施状态与后续累计验证见内部记录「R02方案」（独立源码包未附）。

### HiFox I06 增量152（2026-11-15）

`000152_zoho_bugtracker.sql` SHA256 `9e05e0590ebe870d1a37a56b170c2f60f9fa1f441161cbb8bc194a89b9dd49b3`：新增组织OAuth运行状态与一次导入抓取状态两张租户表，组合外键、FORCE RLS、最小GRANT、租约/数量约束；扩导入来源CHECK、个人身份唯一及无凭据约束。另建无租户/秘密数据的单行全局请求预算和两个最小授权函数；扩展已有OAuth维护扫描，保留Asana条件。没有业务回填、远端请求或新worker。

执行顺序为当前累计链151→152→相应API/导入worker/UI→配置独立US双OAuth应用。根独占隔离库 `parity_r02_20261115` 已按顺序追加152，首轮1项、二轮0项，150条账本逐文件SHA一致；没有向owner旧库倒插151或修改历史checksum。DDL会获取相关表锁、验证CHECK并建立索引，只验证隔离小库；协议及完整SQL以该文件和内部记录「I06方案」（独立源码包未附）为准，迁移通过不代表整包验收完成。

回退先用当前版本断开Zoho连接、停止新导入并取消/排空已有作业，再撤服务器配置和回退应用。保留已导入任务、来源去重/人工映射、历史报告人快照及扩展schema；本地解绑不代表撤销远端OAuth。没有执行破坏性down SQL或生产迁移，131/147/153继续空缺。

### 工作区阶段 12 增量 166（2026-11-17）

`000166_workspace_proposals.sql` 新增独立候选表、来源与基线快照、候选对象索引、状态和决定幂等约束、租户 FK/FORCE RLS 及列级 UPDATE 授权。先迁移再更新中心、daemon 和前端；没有旧数据回填、配置开关或依赖变更。仅在隔离临时 PostgreSQL 验证全量迁移及真实权限/并发/HTTP 写入，不代表现用环境已部署。

回退先停止文件修改入口，再成套回退应用并保留候选表和对象；旧工具直接应用语义不能冒充先审阅。已接受 artifact 引用候选对象，禁止按“临时文件”批量删除。内容回退使用正常历史恢复追加版本。完整 SQL、执行顺序、验证与边界见内部记录「阶段 12 记录」（独立源码包未附）。

### 工作区阶段 13 增量 167（2026-11-17）

`000167_anchor_resolutions.sql` 新增不可变定位结果表，关联评论线程的精确修订，保存请求摘要与结果快照；包含租户复合主键/FK、按线程时间索引、FORCE RLS 及仅 SELECT/INSERT 授权。先执行 166→167，再更新 API/daemon/UI。无旧数据回填、环境配置或依赖变更；仅在自动清理的隔离测试库执行。

回退先停止新定位/重新绑定入口，再成套回退应用，保留表、原评论和截图。已经发送的消息包含 resolution ID，不能通过删除结果表撤销定位；也不改写历史锚点。完整语句、顺序和验证见内部记录「阶段 13 记录」（独立源码包未附）。

## 000168 · 设备认证 challenge 过期清理

部署时先应用迁移再升级 worker。清理复用已有过期索引，每分钟最多回收一批 1,000 条过期超过七天的记录，跳过被锁行。函数仅向控制平面角色开放。回滚先回退 worker，再执行 `DROP FUNCTION openfox_private.purge_device_challenges();`。已清理的过期 challenge 只能从备份恢复，恢复记录不会延长其有效期。
