/** Stage 12e · computer operations: concurrency limits and protected directory notice (English). Mounted under the ops key by en-US.ts. */
export const opsEn = {
  limits: {
    title: "Concurrency limit",
    center: "Center limit",
    local: "Local limit",
    effective: "Effective",
    unreported: "—",
    hint: "The effective value is the smaller of the center limit and the local limit (the daemon's --max-runs)",
  },
  config: {
    label: "Runtime config",
    applied: "Applied v{version}",
    restartRequired: "Restart required",
    restartHint:
      "Restart the daemon on this computer for the organization runtime config change to take effect",
    failed: "Apply failed",
    error: {
      INVALID_POLICY:
        "The daemon on this computer could not validate the organization runtime config",
      ACK_FAILED:
        "The daemon on this computer failed to confirm the config version with the center; it will retry automatically",
    },
  },
  revoke: {
    action: "Revoke access",
    title: "Revoke this computer's access to the current organization?",
    desc: "This computer will stop claiming tasks for the current organization: queued runs fail with AUTHORIZATION_REVOKED and runs in progress are no longer accepted by the center. Bound agents are not archived; they work again after the computer owner adds the computer back to the organization.",
    done: "Access revoked for this computer",
  },
  rotate: {
    title: "Device key",
    desc: "Replace the key this computer uses to connect to the center",
    action: "Rotate key",
    unsupported:
      "This daemon does not support online rotation. Upgrade the daemon or run openfox computer rotate on this computer",
    confirmTitle: "Rotate this computer's key?",
    confirmDesc:
      "The old key stays valid until the new key is saved to disk, so the connection is not interrupted.",
    done: "Key rotated",
    resumed: "Finished the previously interrupted key rotation",
    keyId: "Current key: {id}",
    failed: {
      KEY_ROTATION_FAILED:
        "Key rotation failed. The old key is still valid; try again later",
      KEY_ROTATION_UNSUPPORTED:
        "The daemon on this computer does not support online rotation. Upgrade the daemon or run openfox computer rotate on this computer",
    },
  },
  orgs: {
    removedByAdmin:
      "An admin of that organization removed this computer; an admin must allow it again",
    title: "Allowed organizations",
    loadFailed: "Failed to load organizations",
    current: "Current",
    status: {
      ACTIVE: "Authorized",
      REVOKED: "Revoked",
      EXPIRED: "Expired",
      removedByAdmin: "Removed by an admin",
      none: "Not joined",
    },
    join: "Join",
    joinLabel: "Join {name}",
    joined: "Computer joined {name}",
    revoke: "Revoke",
    revokeLabel: "Revoke access for {name}",
    revokeTitle: "Revoke access for {name}?",
    revokeDesc:
      "This computer will stop claiming tasks for that organization: queued runs fail with AUTHORIZATION_REVOKED and runs in progress are no longer accepted by the center.",
    revoked: "Access revoked for {name}",
    remove: "Remove from organization",
    removeLabel: "Remove from {name}",
    removeTitle: "Remove this computer from {name}?",
    removeDesc:
      "Agents bound to this computer in that organization will be archived, and runs in progress will be canceled.",
    removed: "Computer removed from {name}",
  },
  removed: {
    title: "Computers removed by admins",
    desc: "These computers were removed by an owner or admin of this organization, so their owners cannot add them back on their own. After you allow one again, its owner can add it to this organization.",
    allow: "Allow again",
    allowLabel: "Allow {name} again",
    allowed:
      "Allowed again. The computer owner can now add it to this organization",
    removedAt: "Removed {time}",
    unknownOwner: "Unknown owner",
  },
  protectedRoots: {
    title: "Some registered directories are on the protected list",
    desc: "These directories cannot be used as work directories:",
  },
};
