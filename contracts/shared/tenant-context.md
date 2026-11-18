# Tenant context contract

## Authority

The authenticated actor and a validated organization membership select the
organization. A request body, query parameter, cookie, or user-controlled
header cannot establish trusted tenant context.

The application transaction wrapper is the only application entry point that
may establish context. It must set context with transaction-local semantics,
use the connection only inside that transaction, and finish commit or rollback
before the connection returns to the pool.

The context contains:

- `organization_id`: the selected organization UUID.
- `actor_id`: the authenticated user or a separately authenticated device.
- `authorization_version`: the version checked when the request started.
- `source`: `user_request`, `device_request`, or `organization_worker`.

## Failure semantics

- Missing context: tenant reads return no rows and tenant writes fail closed.
- Invalid or revoked membership: the request is rejected before context setup.
- Context for a different organization: the request is rejected; it is not
  treated as a filter supplied by the caller.
- A backend worker processes one organization per transaction. Cross-tenant
  polling may inspect only control-plane readiness metadata, never tenant
  content.

## Database roles

- The migration role owns tenant tables and policies.
- The application role is not a table owner, superuser, or `BYPASSRLS` role.
- RLS is enabled and forced on tenant tables.
- Any security-definer helper has a fixed `search_path`, no public execute
  grant, strict input validation, and a narrow purpose.

## Pool hygiene tests

The integration suite must prove all of the following:

1. A transaction under organization A cannot see organization B rows.
2. A transaction without context cannot see tenant rows.
3. A connection reused after organization A cannot leak A context to B or to a
   context-free transaction.
4. An application role cannot call the trusted context setter directly.
5. A cross-organization composite association is rejected.

## Compatibility rule

Changing context fields, error semantics, or the trusted setter requires a
contract change and an update to the negative isolation tests before merging.
