# API authorization contract

## User routes

User routes use the existing session authentication and a validated
organization path parameter. The path parameter selects a candidate only; it
does not establish tenant context. The trusted selector and RLS transaction
must validate access before the handler reads or writes tenant data.

Route shapes:

- `GET /api/v1/organizations/{organizationID}/members`
- `PATCH /api/v1/organizations/{organizationID}/members/{userID}`
- `POST /api/v1/organizations/{organizationID}/invites`
- `GET /api/v1/organizations/{organizationID}/invites`
- `POST /api/v1/organizations/{organizationID}/invites/{inviteID}/revoke`
- `POST /api/v1/invites/{inviteID}/accept`
- `POST /api/v1/organizations/{organizationID}/devices`
- `POST /api/v1/organizations/{organizationID}/devices/{deviceID}/revoke`

## Device routes

- `POST /api/v1/device/auth/challenge`
- `POST /api/v1/device/auth/token`
- `GET /api/v1/device/organizations/{organizationID}/agents`

Device routes accept only device sessions and use `device_request`; user
session cookies must not authorize them.

## Cross-cutting requirements

- All state-changing cookie requests require the existing CSRF header.
- Mutations use bounded JSON bodies and stable error codes.
- Membership, invitation, device registration, grant, revoke, and role changes
  emit an audit event without plaintext tokens, passwords, or private keys.
- A retry-safe mutation must define an idempotency key and conflict semantics;
  invitation acceptance and challenge consumption are single-use operations.
