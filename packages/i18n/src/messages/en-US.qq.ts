export const qqEn = {
  scope:
    "Connect an Agent through Tencent’s official QR flow. Accept bound direct messages and authorized group mentions, and return the final text to the originating conversation. Attachments, full group history, and notifications are not included.",
  notConfigured: "QQ requires server secret encryption and a public HTTPS URL.",
  qrNotConfigured:
    "The official QR component is not configured or verified. Contact your deployment administrator.",
  create: "Connect Agent to QQ",
  agent: "Choose Agent",
  name: "QQ connection name",
  slot: "Each Agent can have one messaging bot. The server rejects targets already bound to another platform.",
  empty: "No QQ connections",
  manage: "Manage QQ connection",
  reload: "Refresh status",
  loadFailed: "Could not load QQ settings",
  error: "The operation did not complete. Refresh and check its status.",
  conflict:
    "The state changed or an operation is still stopping. Refresh before continuing; do not submit it again.",
  forbidden:
    "Your permission is insufficient or has been revoked. Reload this page.",
  qrStart: "Show official connection QR",
  qrTitle: "Official QQ QR connection",
  qrHint:
    "Scan with QQ and confirm bot creation or connection on Tencent’s official page. Credentials remain sealed on the server.",
  qrLong:
    "This official URL exceeds the QR display capacity. Continue on the official connection page.",
  officialLink: "Open Tencent’s official connection page",
  qrWaiting: "Waiting for the official component to provide a QR code…",
  qrCancel: "Stop this QR session",
  qrUnknown:
    "The remote outcome is unknown. Stopping locally does not prove that no QQ app was created. Check the Tencent platform before continuing.",
  remoteChecked:
    "I checked the Tencent platform and understand that local actions do not delete remote apps",
  qrCloseUnknown: "Confirm inspection and close this session",
  qrStopWait:
    "Waiting for the QR process to stop. After a server interruption, wait until the original session deadline and stop grace have passed.",
  qrRecover: "Revalidate the obtained app credentials",
  webhook: "Show callback URL",
  webhookTitle: "Configure QQ callback",
  webhookHelp:
    "Enable and verify this HTTP callback in Tencent’s bot settings. Subscribe to direct messages, group mentions, friend removal, and bot removal from groups. Keep the secret URL private. Activation also requires a valid signed event after verification.",
  credentials: "Update credentials for the same app",
  secret: "QQ AppSecret",
  credentialHint:
    "Restore only the same verified AppID. Updating credentials invalidates existing identity and conversation authorization; members must bind again.",
  disconnect: "Disconnect QQ locally",
  disconnectHint:
    "Stop receiving, executing, and replying. History and the verified AppID remain. This does not uninstall the Tencent app.",
  release: "Release Agent binding",
  releaseHint:
    "Release only the local Agent slot. Manage the remote app separately. The verified AppID remains reserved to this organization.",
  personal: "My QQ identities",
  privateBind: "Verify my QQ direct-message identity",
  bindingsEmpty: "No bound identities",
  revoke: "Unbind identity",
  revokeHint:
    "Old inputs and runs lose permission to execute or reply. Binding again does not reactivate old messages.",
  groupDiscovery: "Authorize QQ group replies",
  bindingTitle: "Confirm QQ identity or group authorization",
  privateCommand:
    "Send the complete command below directly to this QQ bot, then return to confirm your identity.",
  groupCommand:
    "A QQ group owner or administrator must mention the bot with the command below, then confirm replies visible to the group.",
  waitingProof:
    "Waiting for signed proof. The command will not execute through the Agent.",
  confirmIdentity: "Confirm my identity binding",
  confirmGroup: "Confirm this group authorization",
  groupConsent:
    "I agree that Agent replies are visible to everyone in this QQ group",
  cancelBinding: "Cancel this verification",
  invites: "Identity invitations awaiting confirmation",
  groupIdentityHelp:
    "Group member identifiers and direct-message identifiers are independent and are not global QQ numbers. A manager selects a candidate from this group and invites the corresponding OpenFox member to confirm.",
  candidate: "QQ group candidate",
  member: "Corresponding OpenFox member",
  invite: "Invite member to confirm",
  acceptHint:
    "Check the group and member identifier chosen by the manager. After acceptance, messages from this source run with your OpenFox permissions.",
  groups: "Authorized QQ groups",
  disableGroup: "Disable this group authorization",
  disableGroupHint:
    "Stop subsequent execution and replies for this group. Reauthorization creates a new authorization and does not revive old messages.",
  noCandidates: "No signed group-message candidates to invite",
  diagnostics: "Recent reply status",
  replyHint:
    "Only a terminal Run response with an original QQ input is sent: one message up to 8,000 characters, within 5 minutes of the original message. Unknown outcomes are never automatically resent.",
  noReplies: "No reply records",
  expires: "Deadline: {time}",
  source: "Source: {scope} · {chat} · {subject}",
  invitationPending: "Invitation created. Waiting for that member to confirm.",
  statePrepared: "Ready to connect",
  stateValidating: "Validating credentials",
  stateWebhook: "Awaiting callback and signed event",
  stateActive: "Active",
  stateReauth: "Revalidation required",
  stateDisconnected: "Disconnected locally",
  stateCancelled: "Cancelled",
  statePending: "Pending",
  statePolling: "Waiting for scan",
  stateScan: "Waiting for scan confirmation",
  stateDone: "Completed",
  stateUnknown: "Outcome unknown",
  stateFailed: "Failed",
  stateExpired: "Expired",
  stateObserved: "Signed proof received",
  stateConfirmed: "Confirmed",
  stateSending: "Sending",
  stateSkipped: "Sending stopped",
  stateOther: "Needs inspection",
} as const;
