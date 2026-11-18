# WP-15 PostgreSQL integration

`run.sh` starts a disposable PostgreSQL cluster, applies the full migration
manifest twice through the explicit `migrate --apply` command, and verifies the
checksummed ledger and WP-14 schema. The second apply proves the runner is
idempotent and does not rerun committed files.
