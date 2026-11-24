# Device authentication contract

## Credential model

Devices authenticate with an Ed25519 public key. The private key is generated
and retained by the device; the server stores only the public key, key ID,
status, generation, and timestamps. A device may have more than one key during
rotation, but only active keys can answer a challenge.

For the install-token exchange the device signs the ASCII domain
`openfox-device-registration-v1` followed by the submitted token and key ID.
Each field uses its UTF-8 bytes prefixed with its unsigned four-byte big-endian
length. The HTTP request carries the unpadded base64url signature.

The server creates a fresh 32-byte random nonce with a five-minute challenge
expiry. The challenge bytes are SHA-256 of the ASCII domain
`openfox-device-center-v1`, followed by the configured public API base URL
(including `/api`, without trailing slashes) and the raw nonce, both
length-prefixed as above. The CLI recomputes this value using its configured
endpoint before signing. This binds the proof to the receiving center when
the installation reconnects at a different address. The device signs a canonical payload containing the challenge ID, device
ID, key ID, and challenge bytes. Verification consumes the challenge in the
same transaction before issuing an opaque device session token. A challenge
cannot be accepted twice, with a different device/key, or after expiry.

Device session tokens are random bearer values whose SHA-256 hashes are stored.
They are bound to the device ID and key generation, have an absolute expiry,
and stop working after device/key/grant revocation. Plaintext tokens and private
keys must never be logged or persisted by the server.

## Organization authorization

Device organization selection is authorized only by an active
`device_grants` row. The selector returns the organization and permission
version before the transaction installs tenant context with
`source=device_request` and `actor_id=device_id`.

A device grant is not a user membership and does not inherit organization
roles. Device endpoints must use device-specific actions and must not expose
user-only member or invitation operations.

## Failure semantics

- Unknown device, key, challenge, revoked credential, expired challenge, bad
  signature, or inactive grant: `401 DEVICE_UNAUTHENTICATED` or hidden `404` as
  appropriate; do not reveal which component failed.
- A valid device session without a grant for the requested organization:
  `404 NOT_FOUND`.
- Replayed or stale challenge: `409 CHALLENGE_CONFLICT`.
- Device revoke and key rotation are fail-closed for all new requests.
