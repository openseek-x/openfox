# Web workbench contract

## Scope

The shared React workbench uses the authorization, collaboration, and
conversation server contracts without changing authorization, database schema,
or task status semantics. Run execution and event/result delivery are governed
by `execution.md` and `event-delivery.md`.

## Client boundary

- The running Go server mounts routes at `/api/v1`, not `/v1`. The Web
  client sends relative, same-origin requests only. Development proxies
  `/api` without removing that prefix.
- Login uses `POST /api/v1/auth/login` with `client_kind: WEB`. Cookies remain
  browser-managed; no password, session token, or business object is stored
  in localStorage/sessionStorage. Mutations echo the readable
  `openfox_csrf` cookie in `X-CSRF-Token`.
- Session and organization discovery use `GET /api/v1/me` and
  `GET /api/v1/me/organizations`. The spaces endpoint checks the selected
  organization against authenticated membership. `POST /api/v1/auth/logout`
  revokes the server session.
- Endpoint clients in `packages/api` use generated OpenAPI types and explicit
  client methods.
- Requests have a 15-second timeout and a 2 MiB response limit. Redirects
  are rejected. Writes are not automatically retried, including after a
  network error or timeout with an unknown server outcome.
- Go int64 `next_cursor` values are preserved as decimal strings before
  JSON parsing loses precision. Cursors are opaque and never incremented
  client-side.

## State and authorization

- Each organization/preview scope owns its own TanStack Query client and
  Zustand UI store. Keys also include organization and entity context.
- Unmounting the scope aborts pending reads/writes, cancels queries, and
  clears cached objects. Aborting a write does not guarantee server rollback.
  Late responses cannot populate the new scope.
- Zustand stores only sidebar width/open state; Task/Conversation data has
  one source of truth in TanStack Query. Nothing persists across organizations.
- HTTP 401 exits the current scope. Failed reads, including 403/404, replace
  previously rendered content with an error rather than showing stale data.
  The server continues to enforce all object/tenant permissions.
- Task status names and workflow membership come from database records.
  Category constants are presentation/filter semantics only. PATCH sends
  `status_id` and `version`, never a free-text status name.
- A version conflict retains the task draft and blocks another save until
  explicit refresh. Refresh updates the base version without replacing the
  draft; the user is told to compare it with the current server value.

## Pagination, writes, and UI limits

- Task lists request at most 50 rows per page. Search/category/priority and
  cursor live in the URL; search is debounced by 250 ms.
- Conversations are organization-paged, then filtered to the selected space
  and task among loaded rows. Counts say "loaded", not global totals.
- Messages/comments/conversations load 50 rows per page, up to 10 pages per
  mounted view. This is bounded pagination, not a virtualized full history.
- Task title/description inputs are limited to 500/10000 UTF-16 code units
  (conservative relative to the server's Unicode-code-point bounds).
  The composer limits text to 4000 code units and checks at most 16000 UTF-8
  bytes before sending.
- Message/comment retries keep the same generated client ID and body.
  An uncertain write locks the body until an explicit same-message retry.
  This retry is in-memory only; navigating away discards the draft and ID.
- Task/conversation creation has no automatic replay. Unknown creation
  results require refreshing the list before another explicit creation.
- Comments do not schedule Runs. Conversation messages can include typed user
  or Agent mentions; an `@Agent` message creates a durable execution input as
  defined in `conversations.md`. Saving another message does not imply that a
  Run was scheduled or executed.
- Task and standalone conversations share one message pane. The task pane
  stays mounted when closed or when its container crosses 700 px; narrow
  containers use a modal drawer with Escape, focus trapping, and focus return.
- Plain text rendering escapes message/comment content. No raw HTML,
  Markdown HTML, iframe, terminal emulation, or remote asset execution exists.

## Preview and platform boundary

`/preview` uses an explicit synthetic read-only transport. It never falls
back to a real API and rejects all mutation requests. The banner labels all
data as demonstration content; production routes never silently fall back
to fixtures.

`PlatformAdapter` exposes platform identity, local-management availability,
and validated HTTPS external navigation only. The Web implementation does
not expose filesystem, shell, IPC, credentials, or daemon controls. Electron
packaging, browser authorization, signed installation, and desktop security
acceptance are outside this Web adapter contract; see `desktop.md` for the
renderer boundary.
