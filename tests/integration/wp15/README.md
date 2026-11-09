# PostgreSQL migration runner integration

`run.sh` starts a disposable PostgreSQL cluster, applies the full migration
manifest twice through the explicit `migrate --apply` command, and verifies the
checksummed ledger and runtime policy schema. The second apply proves the runner is
idempotent and does not rerun committed files.
