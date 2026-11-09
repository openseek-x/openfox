# Deployment examples

`compose.example.yml` is a local development shape only. It does not publish
the service publicly, does not create production credentials, and does not
replace a TLS reverse proxy, backup plan, or migration approval.

The production boundary is HTTPS at the reverse proxy, with the OpenFox
service and PostgreSQL on a private network. Database credentials must be
provided through a restricted file or credential store, never committed to the
repository.

## Web workbench and desktop app

`nginx-web.example.conf` serves the static build of `apps/web` and proxies
`/api/` to `openfox-server`. The desktop app does not bundle the web
workbench; it loads the web URL served here (set
`OPENFOX_DESKTOP_WEB_URL` or `web_url` in the desktop settings file). Build the
assets with `pnpm --filter @openfox/web build` and publish `apps/web/dist`.

## Explicit migrations

The server never runs migrations during `serve`. Validate the manifest with:

```sh
openfox-server migrate --check
```

Apply pending forward migrations during an approved maintenance window with a
restricted URL file:

```sh
openfox-server migrate --apply --database-url-file /run/openfox/database.url
```

The command uses a PostgreSQL advisory lock and records checksummed files in
`openfox_meta.schema_migrations`. It refuses changed or out-of-order history;
it has no destructive down-migration mode.

## Backup and recovery

Use `backup.sh` and `verify-backup.sh` for a database archive plus persistent
artifact-volume archive. Follow `restore-runbook.md` for isolated restore and
center-epoch reconciliation. Do not use an unverified database-only dump as a
complete recovery point.

Use `release-checklist.md` to record compatibility, maintenance-window, and
rollback gates for a candidate release.
