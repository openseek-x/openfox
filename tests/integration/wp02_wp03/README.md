# WP-02/WP-03/WP-04 integration and contract baseline

These tests cover WP-02 identity and membership, WP-03 device authorization,
and WP-04 collaboration using PostgreSQL and the application's HTTP handlers.

## Coverage

The HTTP tests cover:

- hidden cross-organization member reads and writes;
- `OWNER`, `ADMIN`, `MEMBER`, and `GUEST` management permissions;
- last active owner protection with `409 OWNER_REQUIRED`;
- cookie mutation CSRF enforcement;
- invitation creation without a plaintext token field, expiry, revocation,
  replay conflict, and concurrent single acceptance;
- device challenge replay protection, invalid signatures, expiry, device
  revocation, and cross-organization grant denial;
- fixed-category status seeding with multiple records in one category, status
  create/update/archive/delete protection, task-type workflow/default
  validation, enable/disable behavior, task creation, stable display IDs,
  label persistence, optimistic task updates, and cross-organization task
  hiding;
- user-cookie/user-bearer/device-bearer/missing/malformed authentication
  boundaries;
- the shared JSON error envelope and `X-Request-ID` correlation value.

The SQL checks provide a minimal isolated fixture and assertions for:

- transaction-local tenant context and pool hygiene;
- no-context read/write fail-closed behavior;
- forced RLS and runtime-role privilege boundaries;
- organization A visibility of its own rows and zero visibility of B rows;
- cross-organization update and composite-association rejection;
- active-owner fixture invariants;
- invitation/device-session digest-only storage checks;
- required WP-02/WP-03 table and column preflight.

The SQL preflight checks these tables and columns.

| Relation | Required contract columns |
| --- | --- |
| `openfox.invitations` | `id`, `organization_id`, `normalized_email`, `role`, `token_hash`, `status`, `expires_at`, `consumed_at` |
| `openfox.device_keys` | `device_id`, `key_id`, `public_key`, `status`, `generation` |
| `openfox.device_challenges` | `device_id`, `key_id`, `nonce`, `expires_at`, `consumed_at` |
| `openfox.device_sessions` | `token_hash`, `device_id`, `key_generation`, `expires_at`, `revoked_at` |

## Running

```sh
tests/integration/wp02_wp03/run.sh
```

With PostgreSQL tools on `PATH`, the script creates a temporary cluster and
applies each migration in its own transaction. It loads the SQL fixture,
runs the SQL assertions, and passes the database connection to the Go tests.
The HTTP adapter starts the application handler in an `httptest.Server` and
clones the fixture database for each test. It exercises the `/v1` routes
and supplies test hooks for invitation delivery, device signing, and CSRF.
The script removes the cluster on exit.

To use an existing database, set `OPENFOX_TEST_DATABASE_URL` to a dedicated,
already-migrated test database. The script loads the fixture and runs the same
SQL and Go tests. Its PostgreSQL role needs permission to clone the database.

When there is no configured database and the PostgreSQL tools are unavailable,
the database-backed tests skip. Set `OPENFOX_WP02_WP03_REQUIRE_HTTP=1` in CI
to make a missing HTTP adapter fail the run. Set `GOFLAGS=-v` to see each test
result.

## Historical baseline note

The WP-02/WP-03 baseline was originally implemented only in this directory.
The WP-04 delivery adds production code and migration `000007` outside this
test directory; its details are recorded in the WP-04 development record.

`fixture.sql` contains test-data inserts only, and `assertions.sql` uses
transaction-local checks and read-only catalog validation apart from its
deliberate negative test statements, which are rolled back by their surrounding
transactions.
