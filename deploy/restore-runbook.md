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

The database archive keeps object ownership and grants (`pg_dump` without
`--no-owner` / `--no-acl`). OpenFox relies on them: tenant isolation is enforced
by row-level security for the non-owner role `openfox_app`, and the
`openfox_private` functions are owned by `openfox_migrator`. An archive without
ownership and grants cannot be turned back into a working database.

The scripts take every location as an argument (`backup.sh DATABASE_URL_FILE
STORAGE_ROOT OUTPUT_DIRECTORY`, `verify-backup.sh BACKUP_DIRECTORY`); the paths
above are examples. `verify-backup.sh` extracts into `$TMPDIR` (default `/tmp`),
so point `TMPDIR` at a volume with room for the artifact archive.

## Restore to an isolated target

1. Provision a new PostgreSQL database and an empty persistent artifact volume.
2. Run `deploy/verify-backup.sh` before importing anything.
3. Create the OpenFox roles in the target cluster **before** restoring. Roles are
   cluster-wide objects and are not part of the database archive; without them the
   restore fails on every policy, grant, and ownership statement (the 2026-11-12
   drill saw 76 errors and an unusable database when this step was skipped). Use
   the same definitions as migration `000001`:

   ```sql
   CREATE ROLE openfox_migrator NOLOGIN NOSUPERUSER NOCREATEDB NOREPLICATION NOBYPASSRLS;
   CREATE ROLE openfox_app NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS NOINHERIT;
   CREATE ROLE openfox_context NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS NOINHERIT;
   GRANT openfox_app TO openfox_context;
   ```

   Then give the service's login role the same role memberships it has in the
   original deployment. The login role and its credentials are deployment
   specific and are not created by OpenFox migrations.
4. Restore the database archive into the isolated target as a role that may set
   ownership (a superuser or the database owner with membership in the roles
   above). Do **not** pass `--no-owner` or `--no-acl`:

   ```sh
   pg_restore --exit-on-error --dbname=<target database> <backup>/database.dump
   ```

   Restore `artifacts.tar` into the empty artifact volume only after
   `verify-backup.sh` has validated the archive members:

   ```sh
   tar -xf <backup>/artifacts.tar -C <empty artifact volume>
   ```

5. Recalculate checksums from the restored artifact volume and compare them with
   `storage.sha256` (one `<sha256>  <relative path>` line per file). Missing or
   mismatched files remain unavailable; do not mark them `AVAILABLE` by hand.
6. Check the migration ledger (`openfox_meta.schema_migrations`) and application
   compatibility before starting the center. Apply only forward migrations using
   the explicit `migrate --apply` command; on a same-version restore it applies
   nothing. Confirm that `openfox_app` still has its table privileges and that
   `openfox_private` is owned by `openfox_migrator`:

   ```sql
   SELECT has_table_privilege('openfox_app', 'openfox.tasks', 'SELECT');
   SELECT nspowner::regrole FROM pg_namespace WHERE nspname = 'openfox_private';
   ```

7. Before accepting devices, increment `openfox.center_state.center_epoch` in the
   isolated target under the recovery owner's approval:

   ```sql
   BEGIN;
   UPDATE openfox.center_state
      SET center_epoch = center_epoch + 1, updated_at = clock_timestamp()
    WHERE id = 1
   RETURNING center_epoch;
   COMMIT;
   ```

   What this does and does not do (verified in the 2026-11-12 drill): a daemon
   that still holds the old epoch has its heartbeat rejected, reports
   `RECONCILE_REQUIRED`, shows as offline, and is not offered new Runs. It does
   **not** invalidate device sessions, and it does **not** stop a daemon from
   renewing the lease of a Run that was already in flight when the backup was
   taken: the lease endpoint does not check the epoch. Step 9 is therefore
   mandatory, not optional.
8. Start the center against the restored database URL file and artifact root
   (`OPENFOX_DATABASE_URL_FILE`, `OPENFOX_STORAGE_ROOT`). `/ready` reports ready as
   soon as the database answers; it does not know about the checks above, so keep
   the center unreachable for users and devices (reverse proxy or firewall) until
   steps 5–7 have passed.
9. Re-authorize or reconcile devices and Runs explicitly. Never replay an old offer
   or infer successful completion from a pre-restore socket or process record.
   List the Runs that were active at backup time and close each one before
   devices are let back in:

   ```sql
   SELECT organization_id, id, state, computer_id, lease_expires_at
     FROM openfox.runs
    WHERE state NOT IN ('SUCCEEDED', 'FAILED', 'CANCELED')
    ORDER BY organization_id, created_at;
   ```

   For each of them an organization owner or admin cancels the Run
   (`POST /api/v1/organizations/{org}/runs/{run}/cancel`, or the stop button in
   the conversation). A daemon that is still running the pre-restore process
   learns about the cancellation on its next lease renewal, stops the process, and
   the Run ends as `CANCELED`. Runs whose computer never comes back stay in
   "reconciliation required" after their lease expires and are closed with
   "mark as failed" by an organization owner or admin. In the drill the old-epoch
   daemon stayed in `RECONCILE_REQUIRED` for the rest of the exercise; bringing
   such a computer back was not exercised, so plan to register it again.

## Rollback and evidence

Rollback is a traffic/configuration decision, not an automatic SQL down migration.
Keep the original target untouched until the isolated restore has passed checks and
the recovery owner approves cutover. Record backup ID, schema ledger, center epoch,
artifact manifest result, reconciliation results, elapsed restore time, and the
observed data-loss boundary. Do not claim an RPO/RTO until this exercise has been
performed in the target deployment class.
