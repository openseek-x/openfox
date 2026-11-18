# WP-14 runtime limits and cleanup contract

WP-14 uses typed organization records rather than a general configuration or
key/value table. `organization_runtime_configs` stores a monotonic
`config_version`, the last `applied_version`, an explicit restart flag, and the
bounded queue, storage, retention, and disk policies. Updates require the
expected version and return a conflict on a stale write.

New Runs take an insert-once row in `run_resource_snapshots`. A later policy
edit therefore changes admission and future cleanup decisions, but does not
silently change the resource contract of an existing Run.

Cleanup is two phase:

1. A preview records typed artifact IDs, storage keys, byte counts, hashes,
   retention timestamps, and artifact versions. It expires after a bounded
   interval.
2. Confirmation locks the preview and rechecks ownership, state, hash, key,
   version, active Run state, and open reconciliation cases. Only `READY`
   items may be removed. A storage deletion is followed by a conditional
   database state transition to `DELETED`; stale items are skipped and failed
   storage deletes remain auditable as `FAILED`.

The first WP-14 cleanup target is system-owned artifact storage. It never
selects a user-specified direct work directory, active/continuable Run, pinned
artifact, artifact without a confirmed result, or Run under reconciliation.
Run events, audit records, and pending control events remain outside this
destructive path until their own typed retention and recovery rules exist.

Resource failures are explainable: version conflicts are reported as a
conflict, disk/storage unavailability is a bounded service error, and a stale
preview item is skipped rather than force-deleted.

The organization active limit is enforced at the existing serialized capacity
reservation boundary. Queue admission also has a typed decision helper; the
durable Run writer must apply it when that writer is introduced, rather than
using the UI or an in-memory count as authority.
