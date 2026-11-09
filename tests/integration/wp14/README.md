# PostgreSQL runtime policy integration

`run.sh` starts a disposable local PostgreSQL cluster, applies all ordered migrations through
`000015_wp14_limits_cleanup.sql`, loads the shared tenant fixture, and runs the runtime policy store test.

The test covers typed runtime policy defaults and optimistic updates, device acknowledgement,
Run policy snapshots, cleanup preview/confirmation/deletion, the runtime active reservation limit,
and cross-tenant access denial.
