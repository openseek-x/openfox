# Identity, device, and collaboration integration tests

This directory covers identity, membership, device authorization, and
collaboration boundaries. The SQL checks do not create or alter production schema.

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
- required identity and device table and column preflight.

The collaboration migration is also exercised by the isolated-cluster path;
the HTTP collaboration test reports a diagnostic skip when `HTTPFactory` is
not wired to the real server.

The SQL preflight expects these implementation objects. If an implementation
uses different names, update this expectation list and the adapter wiring together:

| Relation                    | Required contract columns                                                                                |
| --------------------------- | -------------------------------------------------------------------------------------------------------- |
| `openfox.invitations`       | `id`, `organization_id`, `normalized_email`, `role`, `token_hash`, `status`, `expires_at`, `consumed_at` |
| `openfox.device_keys`       | `device_id`, `key_id`, `public_key`, `status`, `generation`                                              |
| `openfox.device_challenges` | `device_id`, `key_id`, `nonce`, `expires_at`, `consumed_at`                                              |
| `openfox.device_sessions`   | `token_hash`, `device_id`, `key_generation`, `expires_at`, `revoked_at`                                  |

## HTTP adapter wiring

Assign `HTTPFactory` in a same-package test file to an adapter that:

1. builds the real `api.New(...)` handler in an `httptest.Server`;
2. returns clients for the `AuthSpec` kinds without putting credentials in
   this repository;
3. supplies test-only invitation delivery and device-signing hooks by
   implementing `InvitationTestHooks` and `DeviceTestHooks`;
4. resets the isolated fixture between tests, or provisions a fresh database
   per harness, so lifecycle tests remain independent;
5. returns the implementation route prefix through `RoutePrefix()`.

The adapter owns passwords, session cookies, bearer tokens, private keys, and
database connections. `BearerToken` is only populated from a token returned by
the system under test. The test delivery hook must be an explicit test seam;
it must not read a plaintext invitation token from a production row.

The default route prefix is `/api/v1`, matching `contracts/shared`. The
adapter may return `/v1` while the existing compatibility route is available.

## Running

Go tests compile with no adapter and skip with a diagnostic message. Set
`OPENFOX_WP02_WP03_REQUIRE_HTTP=1` in CI when HTTP coverage is required;
then a missing factory fails instead of skipping.

```sh
go test ./tests/integration/wp02_wp03 -count=1
```

`run.sh` supports the existing `OPENFOX_TEST_DATABASE_URL` variable. The URL
value is never printed. When it is set, the script treats it as a configured
test database, runs only this directory's fixture/assertions, then runs the Go
tests. When it is not set and PostgreSQL client/server tools are available, it
creates an isolated temporary cluster, applies the repository's existing
migrations there, and removes the cluster on exit. If PostgreSQL is absent,
the script reports that SQL was not executed and runs the buildable Go
baseline.

```sh
tests/integration/wp02_wp03/run.sh
```

A configured database must contain the required identity and device schema.
Do not point the script at a production database.

`fixture.sql` contains test-data inserts only, and `assertions.sql` uses
transaction-local checks and read-only catalog validation apart from its
deliberate negative test statements, which are rolled back by their surrounding
transactions.

For validation, run `bash -n tests/integration/wp02_wp03/run.sh` and
`go test ./tests/integration/wp02_wp03 -count=1`. Run SQL only against an
isolated or explicitly configured test database.
