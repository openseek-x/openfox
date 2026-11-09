# Backup and restore runbook

This runbook is for an approved maintenance window. The scripts in this directory
do not restore over an existing production database or storage volume.

## Backup

1. Stop new dispatch and writes according to the deployment maintenance procedure.
2. Confirm the database URL file is readable only by the service account and the
   artifact root is the persistent volume, not a container temporary layer.
3. Run:

   ```sh
   deploy/backup.sh /run/openfox/database.url /var/lib/openfox \
     /var/backups/openfox/$(date -u +%Y%m%dT%H%M%SZ)
   deploy/verify-backup.sh /var/backups/openfox/<timestamp>
   ```

4. Store the backup directory and its checksums under the approved backup policy.
   The database archive contains tenant data and must be treated as sensitive.

The backup consists of a PostgreSQL custom archive, an artifact-volume tar archive,
the artifact SHA-256 manifest, and a manifest for the backup files. A database-only
backup is not a complete OpenFox recovery point.

## Restore to an isolated target

1. Provision a new PostgreSQL database and an empty persistent artifact volume.
2. Run `deploy/verify-backup.sh` before importing anything.
3. Restore the database archive into the isolated target with `pg_restore --no-owner
   --no-acl`; restore `artifacts.tar` into the empty artifact volume only after
   validating archive members.
4. Recalculate checksums from the restored artifact volume and compare them with
   `storage.sha256`; run `deploy/verify-backup.sh` as the archive integrity check.
   Missing or mismatched files remain unavailable; do not mark them `AVAILABLE` by
   hand.
5. Check the migration ledger and application compatibility before starting the
   center. Apply only forward migrations using the explicit `migrate --apply`
   command.
6. Before accepting devices, increment `openfox.center_state.center_epoch` in the
   isolated target under the recovery owner's approval:

   ```sql
   BEGIN;
   UPDATE openfox.center_state
      SET center_epoch = center_epoch + 1, updated_at = clock_timestamp()
    WHERE id = 1
   RETURNING center_epoch;
   COMMIT;
   ```

   This invalidates old device sessions and prevents replaying pre-restore
   in-flight work.
7. Start the center with readiness disabled until database, storage, and migration
   checks pass. Keep dispatch paused while reconciliation lists active Runs,
   reservations, outbox entries, and device generations.
8. Re-authorize or reconcile devices and Runs explicitly. Never replay an old offer
   or infer successful completion from a pre-restore socket or process record.

## Rollback and evidence

Rollback is a traffic/configuration decision, not an automatic SQL down migration.
Keep the original target untouched until the isolated restore has passed checks and
the recovery owner approves cutover. Record backup ID, schema ledger, center epoch,
artifact manifest result, reconciliation results, elapsed restore time, and the
observed data-loss boundary. Do not claim an RPO/RTO until this exercise has been
performed in the target deployment class.
