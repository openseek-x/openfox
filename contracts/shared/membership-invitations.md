# Membership and invitation contract

## Membership authority

`organization_members` is the source of truth for organization membership.
`user_organization_index` is a control-plane read model and must be updated in
the same transaction by the existing trigger or an equivalent write path.

Membership states are `ACTIVE`, `INVITED`, and `REVOKED`. A revoked member is
never a valid tenant selector. Membership changes increment
`authorization_version` and invalidate the caller's stale authorization
snapshot.

Organization roles are `OWNER`, `ADMIN`, `MEMBER`, and `GUEST`:

- Owners may manage every member and may transfer ownership.
- Admins may manage `MEMBER` and `GUEST` memberships, but cannot grant,
  revoke, or demote an owner/admin.
- No operation may leave an organization without an active owner.
- Membership rows are revoked rather than physically deleted so audit and
  invitation references remain explainable.

## Invitation lifecycle

Invitation states are `PENDING`, `ACCEPTED`, `REVOKED`, and `EXPIRED`.
Each invitation has one normalized target email, organization, role, creator,
opaque token hash, issued time, expiry time, and consumed time. The default
expiry is seven days and the token is single-use.

The token is never stored or logged in plaintext. Accepting an invitation is
one transaction: lock the invitation, verify pending/non-expired state and
email binding, create or select the user credential, create the active
membership, synchronize the control-plane index, and mark the invitation
accepted. Replaying the token is an idempotent conflict and cannot create a
second membership.

If the target email already has an account, acceptance requires an authenticated
session for that same normalized email. If it does not, acceptance may create
the password credential in the same transaction. No email delivery provider is
assumed by this contract; production callers must not receive a plaintext token
unless a separately approved delivery boundary explicitly permits it.

## API error semantics

- Missing or invalid invitation: `404 NOT_FOUND`.
- Expired, revoked, already accepted, or parameter-conflicting invitation:
  `409 INVITATION_CONFLICT`.
- A role change that violates the last-owner rule: `409 OWNER_REQUIRED`.
- A caller without membership-management authority: `403 ACTION_FORBIDDEN`.
