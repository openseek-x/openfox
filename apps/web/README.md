# OpenFox Web

Browser entry for the OpenFox app. This package only mounts `@openfox/app`;
the UI lives in:

| Package         | Contents                                                                                                                     |
| --------------- | ---------------------------------------------------------------------------------------------------------------------------- |
| `packages/ui`   | Design tokens (`tokens.css`), base components and icons                                                                      |
| `packages/i18n` | `zh-CN` (baseline) and `en-US` dictionaries; every UI string goes through `t()`                                              |
| `packages/api`  | HTTP client (CSRF, `Idempotency-Key`, timeouts, error parsing), SSE helper and types generated from `contracts/openapi.yaml` |
| `packages/app`  | Routes, shell, login and onboarding, tasks, my tasks, inbox, global settings                                                 |

Every screen uses real backend data; pages that are not built yet show the
"not available yet" state instead of placeholder data.

## Start

Use Node 22.22.2 or later in the Node 22 line and pnpm 11.15.1.
From the repository root:

```sh
pnpm install --frozen-lockfile
pnpm dev
```

Open `http://127.0.0.1:4173/login`. Sign-in is email + 6-digit code. Start the
Go API separately against a development database with all migrations applied.
For local development without SMTP, set a development-only universal code on
the server (rejected when `OPENFOX_ENV=production`):

```sh
OPENFOX_ENV=development OPENFOX_AUTH_EMAIL_OTP_UNIVERSAL_CODE=<6 digits> \
  openfox-server serve
```

`OPENFOX_WEB_API_TARGET` is a **development-server-only** HTTP(S) origin,
defaulting to `http://127.0.0.1:8080`. It rejects credentials, paths, query
strings and fragments. Restart Rsbuild after changing it:

```sh
OPENFOX_WEB_API_TARGET=http://127.0.0.1:18090 pnpm dev
```

All browser API calls use `/api/v1`, matching `openfox-server serve`.
Do not put tokens into the frontend environment.

## API types

`contracts/openapi.yaml` is the source of truth. After changing it run:

```sh
pnpm gen:api
```

which regenerates `packages/api/src/schema.gen.ts` (ignored by Prettier).
Add the new method to `packages/api/src/client.ts` using the generated types.

## Verify

```sh
pnpm check      # Prettier, strict TypeScript, Vitest, production build
pnpm test:e2e   # Playwright; the live test skips without a backend
```

For real Go/PostgreSQL/browser integration use a pre-existing **temporary**
OpenFox PostgreSQL cluster (never a shared or production database):

```sh
OPENFOX_WP11_PG_SOCKET=/private/tmp/openfox-example/socket \
OPENFOX_WP11_PG_PORT=55047 \
OPENFOX_LIVE_PORT=18090 \
bash tests/frontend/run-live.sh
```

The script creates a unique `openfox_wp11_*` database, applies all migrations
and fixtures, starts the Go server on `127.0.0.1:$OPENFOX_LIVE_PORT` (default
18090; it refuses to start if the port is taken) with a random universal
login code, runs the Playwright live test (code login → create task → move it
on the board → comment → inbox) and the pgx integration suites, then drops its
own database and removes the database URL file. Port 4173 must also be free.
Logs are kept under the printed temporary directory.

## Build and deployment boundary

`pnpm build` writes `apps/web/dist`. A deployment must apply the Go migrations,
serve those assets, rewrite SPA navigation to `index.html`, and proxy `/api` to
Go under the **same origin**. Use HTTPS and the backend's production Secure
cookies. Configure headers such as CSP (`default-src 'self'; object-src
'none'; frame-ancestors 'none'`) at the serving proxy. The development server
is not a production hosting recommendation.

Dependencies are pinned by exact manifests and `pnpm-lock.yaml`.
