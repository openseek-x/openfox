# Device management contract

Device management keeps physical device identity, device authentication, and
organization use authorization separate:

- `physical_devices.owner_user_id` is the user who owns the installation.
  Organization membership or Admin role does not grant local device-management
  authority.
- `device_keys` and device sessions are independent from user credentials. A
  device keeps the Ed25519 private key locally; the center stores only the
  public key and generation facts.
- `device_grants` controls use scope. `ORGANIZATION` permits the organization,
  `SPACE` requires an organization-owned space, and `PERSONAL` is bound to the
  owner user. The scope is explicit data, not a role or a generic dictionary.
- Registration tokens are short-lived, one-time, hashed server-side, and bound
  to the issuing organization/member. A successful exchange consumes the token
  in the same transaction that creates the device, key, and initial grant.
- Heartbeats update only device health and capability facts. Server receipt time
  is authoritative; device timestamps do not determine online status.
- A device hello/heartbeat carries a center epoch and protocol version. An
  epoch mismatch is fail-closed and requires reconciliation; it is not an
  implicit re-registration.

## Local service boundary

The macOS daemon is a user-level service managed by `launchd`. Its management
socket is Unix-domain, user-private, and never an unauthenticated HTTP port.
The socket prevents accidental calls by other users but is not a same-UID
security boundary. Closing the UI does not stop the daemon. Stopping the
service and revoking the device are separate explicit actions.

The daemon connects to `/v1/device/connect` with the device bearer session and
the `openfox.device.v1` subprotocol. The center authenticates the session
before upgrade, binds every message to the authenticated device ID, and
accepts only bounded JSON text messages. `hello` and `heartbeat` are
acknowledged with the same message ID; the ACK contains the server receipt
time, current device version, and center epoch.

Dial failures and transport closes use bounded exponential backoff with
jitter. The backoff resets only after a stable connection has produced a
heartbeat ACK, preventing a server that accepts one frame and immediately
drops the connection from causing a tight reconnect loop. An epoch conflict is
reported as `reconcile_required` and stops automatic reconnect; it never
silently updates the local epoch or re-registers the device. The local daemon
control socket reports `RECONCILE_REQUIRED` while the operator repairs trust.
An unacknowledged heartbeat has a bounded timeout and is treated as a transport
failure, so a dead connection cannot suppress future heartbeats indefinitely.

## API rules

- User routes must authenticate the organization before reading or mutating a
  grant; object and scope checks happen again inside the tenant transaction.
- Device routes authenticate with a device bearer session and re-check the
  active grant and generation. A revoked key, device, or grant fails closed.
- Registration and rotation responses may contain newly generated one-time
  material only at the authorized exchange boundary. Tokens and private keys
  must not be logged or persisted in server-side database columns.
- Heartbeat, protocol, capability, and grant updates are versioned or row
  locked. Stale versions return a conflict rather than overwriting newer facts.
