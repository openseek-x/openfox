# Operations and release contract

## Explicit schema changes

`openfox-server serve` never runs SQL migrations. Operators first validate the
manifest with `migrate --check`, then apply forward migrations explicitly during
an approved maintenance window with `migrate --apply --database-url-file`.

The runner creates the protected operational schema
`openfox_meta.schema_migrations` on first use. Each committed migration records
its six-digit number, filename, SHA-256 checksum, and commit time. A PostgreSQL
advisory lock serializes runners. A changed applied file, missing history prefix,
or skipped migration causes a fail-closed error. There is no automatic down
migration or startup migration.

## Compatibility order

1. Verify the backup and artifact manifest.
2. Apply expand-only database migrations.
3. Deploy a center version that reads both the old and newly added schema fields.
4. Upgrade Daemons and Runtime adapters within the supported protocol window.
5. Observe readiness, error rate, outbox age, device heartbeats, reconciliation
   cases, and storage watermarks before any later schema contraction.

Database schema version, API version, device protocol major/minor, and center
epoch are independent. A protocol major mismatch is rejected; unknown optional
fields may be ignored, while unknown required control messages are rejected.

## Recovery boundary

A complete recovery point includes the PostgreSQL custom archive, the persistent
artifact volume, and a SHA-256 manifest. Secrets are backed up separately by the
deployment owner. Restore first into an isolated target, verify rows and files,
increment the center epoch, and reconcile active Runs and device generations
before accepting new work. Never replay an old offer or infer process completion
from a pre-restore connection.
