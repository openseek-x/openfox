# PostgreSQL reconciliation integration

`run.sh` starts a disposable local PostgreSQL cluster, applies every ordered
migration including `000014_wp13_reconciliation.sql`, loads the shared tenant
fixture, and runs the reconciliation integration test.

The test proves that a live-process fact cannot resolve an unknown Run, a
matching exit fact can be resolved by an organization administrator, terminal
resolution clears the safety stop and leaves the Run failed rather than
successfully guessed, and another tenant cannot read the case.
