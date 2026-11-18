# Deployment examples

`compose.example.yml` is a local development shape only. It does not publish
the service publicly, does not create production credentials, and does not
replace a TLS reverse proxy, backup plan, or migration approval.

The production boundary is HTTPS at the reverse proxy, with the OpenFox
service and PostgreSQL on a private network. Database credentials must be
provided through a restricted file or credential store, never committed to the
repository.

Set `OPENFOX_ENV=production` explicitly in the API service environment before
exposing the reverse proxy. Provide the production HTTPS public URL and the
database and encryption key files. Startup validates the production settings
and session cookies use `Secure` in this mode.

Email notifications can be delivered more than once. If SMTP accepts a message and
the worker stops before recording completion, the recipient may get another
copy after the delivery lease expires. An expired notification can also end undelivered.

## Web workbench and desktop app

`nginx-web.example.conf` serves the static build of `apps/web` and proxies
`/api/` to `openfox-server`. The desktop app does not bundle the web
workbench; like HiFox, it loads the web URL served here (set
`OPENFOX_DESKTOP_WEB_URL` or `web_url` in the desktop settings file). Build the
assets with `pnpm --filter @openfox/web build` and publish `apps/web/dist`.

## Desktop update manifest (optional)

The desktop app checks `<web origin>/desktop/latest.json` (https only) and
prompts the user to download; it never downloads or installs by itself because
the installer is not signed or notarized. Without the file (404) the app simply
reports "no update". To publish an update:

1. Put the DMG and the manifest under `/srv/openfox/desktop/` (the
   `location /desktop/` block in `nginx-web.example.conf` serves them; keep it
   above the SPA fallback so a missing file is a real 404).
2. Write `/srv/openfox/desktop/latest.json`:

   ```json
   {
     "version": "0.2.0",
     "download_url": "https://<same host>/desktop/OpenFox-0.2.0-arm64.dmg",
     "notes": "What changed (plain text, max 2000 chars).",
     "sha256": "<64 hex chars of the DMG, from: shasum -a 256 OpenFox-0.2.0-arm64.dmg>",
     "min_version": "0.1.0"
   }
   ```

   `min_version` is optional; users below it get a prompt that cannot be skipped.

3. `download_url` must be https and on the same origin as the web URL. To host
   the DMG elsewhere, list the host in the desktop app's environment variable
   `OPENFOX_DESKTOP_UPDATE_HOSTS` (comma separated host names). Anything else
   is ignored by the app.
4. Rollback: delete `latest.json` (clients treat it as "no update") or restore
   the previous one. Users who already skipped a version keep skipping it.

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

## Shared computers are not a sandbox

A connected computer runs Agent work through the local `openfox` daemon. Each
run gets its own working directory, but **working-directory isolation is not a
security sandbox**: runs triggered by the people a computer is shared with
execute as the local operating-system user that runs the daemon and can read
any file that user can read (and write wherever that user can write).

Operational consequences:

- Sharing a computer with a space, with the whole organization, or adding it
  to a second organization extends that local user's file access to every
  member who can then trigger runs on it. The web and desktop apps ask the
  computer owner to acknowledge this before the sharing scope is widened.
- For shared computers, run the daemon under a dedicated operating-system
  account that owns only the repositories and credentials Agents are meant to
  use. Do not run it under a personal account that holds SSH keys, cloud
  credentials, browser profiles or unrelated source trees.
- The daemon's protected-directory list and per-run directories reduce
  accidents; they are not an access-control boundary. Use operating-system
  permissions, a separate machine, a VM or a container when members must be
  isolated from each other or from the host.
- Narrowing the sharing scope or revoking a grant stops new runs from those
  members; it does not undo what earlier runs already read or changed.

## Backup and recovery

Use `backup.sh` and `verify-backup.sh` for a database archive plus persistent
artifact-volume archive. Follow `restore-runbook.md` for isolated restore and
center-epoch reconciliation. Do not use an unverified database-only dump as a
complete recovery point.

Use `release-checklist.md` to record compatibility, maintenance-window, and
rollback gates for a candidate release.
