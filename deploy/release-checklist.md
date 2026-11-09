# Release checklist

Use this checklist for a candidate release. It is a manual gate, not an automatic
deployment script.

## Build and compatibility

- [ ] Record the center binary version, migration manifest, API version, device
      protocol major/minor, and supported Runtime adapter versions separately.
- [ ] Confirm the candidate only requires expand-only schema changes and that the
      previous center can read the new schema during the compatibility window.
- [ ] Validate `openfox-server migrate --check` from the candidate artifact.
- [ ] Run the full Go tests, `go vet`, relevant race tests, and the disposable
      PostgreSQL migration/backup harness.
- [ ] Review dependency and license changes; do not release with embedded
      credentials, private URLs, or debug payload logging.

## Integration acceptance gate

- [ ] Run `bash tests/integration/wp16/run.sh` and retain the generated
      `report.json`, `report.md`, and per-case logs as release evidence.
- [ ] Confirm every required case is `pass`; `not-run` is a release blocker and
      cannot be converted to pass by a demo, unit test, or pseudo Runtime.
- [ ] List every unverified OS, Runtime, desktop path, and production-scale
      performance limitation.
- [ ] Complete the real Runtime/real computer dual-link and live Web/API path
      before claiming the corresponding AC-1/4/11/15/16/21 evidence.
- [ ] For any failed security, tenant, duplicate-execution, recovery, or data
      integrity case, stop the release and attach the root cause and retest.

## Desktop app gate

- [ ] Build the Web assets and launch the Electron shell with the approved
      Electron artifact; verify the sandboxed preload exposes only local status
      and the three daemon actions.
- [ ] Verify `OPENFOX_DESKTOP_OPENFOX_BINARY` is an absolute, operator-selected
      path and that renderer input cannot change the executable or argument list.
- [ ] Do not claim task dispatch or real Runtime execution from the desktop shell;
      those require an approved center dispatch contract and real-device E2E.

## Maintenance window

- [ ] Verify a fresh backup with `deploy/verify-backup.sh` and record its ID.
- [ ] Pause new dispatch and document the active Run, reservation, outbox, and
      artifact watermarks.
- [ ] Apply `migrate --apply` once from one operator process; record the ledger
      tail and checksum.
- [ ] Deploy the compatible center, verify `/live` and `/ready`, then upgrade
      Daemons within the supported protocol window.
- [ ] Observe DB pool wait, error rate, outbox age, heartbeat freshness,
      reconciliation cases, and artifact/disk watermarks before resuming work.

## Rollback gate

- [ ] If the candidate is incompatible, stop new work and return to the previous
      compatible binary without running a destructive down migration.
- [ ] Keep the database and artifact volume untouched until active Runs and device
      generations are reconciled.
- [ ] Use the isolated restore runbook for data rollback; record elapsed time and
      the actual recovery point before declaring the release recovered.
