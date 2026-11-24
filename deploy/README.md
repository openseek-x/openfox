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

## Desktop updates

Push a `vX.Y.Z` Git tag matching the version in `apps/desktop/package.json`.
GitHub Actions builds the arm64 app, verifies the DMG contains the same app as
the PKG, and attaches both installers to a draft GitHub Release. After the
SHA-256 digests match, the workflow publishes the release and then deploys
the web workbench and the update feed used by older clients.
Rerun failed jobs on the original run to reuse its verified build artifacts.

From 0.1.3, the app checks the latest stable GitHub Release in `openseek-x/openfox`.
The tag supplies the version. People installing manually use the DMG. The release
app downloads the PKG with a 512 MiB limit and 20-minute timeout, checks its
GitHub digest and opens macOS Installer only on explicit consent. The app uses
ad hoc signing and installation is subject to the user's macOS security policy.

Configure repository variable `OPENFOX_PUBLIC_URL` with the HTTPS workbench
origin. The workflow also publishes the same PKG and `desktop/latest.json` on
that origin so existing 0.1.2 installations can receive the GitHub-enabled app.

The `desktop-production` GitHub environment permits release tags. It contains
`OPENFOX_DEPLOY_KEY` (secret), `OPENFOX_DEPLOY_TARGET` (user@host) and
`OPENFOX_KNOWN_HOSTS` (pinned SSH host keys). The build job has no deployment key.
On the server, install `publish-static.py` in a root-owned location and restrict
this dedicated SSH public key to the following forced command, supplying the live
web root, shared release lock and public HTTPS origin for that environment.

```text
restrict,command="/usr/local/libexec/openfox-publish-static --web-root <web-root> --lock <lock> --origin <https-origin>" ssh-ed25519 <public-key>
```

The deployment job sends the GitHub artifact's short-lived download URL over
SSH. The server fetches parallel HTTPS ranges from Actions storage, verifies
the ZIP against the SHA-256 emitted by the upload action, then checks the bundle
and package. It keeps old hashed web assets and atomically
switches the web root's `current` link. A failed public check restores the prior
target. The job logs both targets. To roll back manually, take the same lock and
atomically restore the previous symlink. Users already on the new version need
a higher-version repair release or a manual reinstall. Database and service
changes use the separate center deployment procedure.

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
