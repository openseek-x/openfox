export const slackEn = {
  notificationToggle: "Enable Slack notifications",
  notificationRecipients:
    "Notifications go to a one-to-one DM in each Slack workspace where you have a valid binding in the notification’s organization. Your Slack event preferences are shared across workspaces. Unbind a workspace to stop delivery there.",
  bodyTooLarge:
    "This form is too large. Shorten the additional instructions or check the credentials.",
  reinstall: "Reinstall",
  localDisconnected:
    "Local installation authorization is disconnected; the Slack App has not been uninstalled. A retained target binding can be reinstalled through OAuth. A cancelled binding requires a new operation.",
  proofUnknownHelp:
    "Configuration refresh token rotation is uncertain. Do not reuse the original credential. Generate fresh configuration credentials in Slack, check any existing App, then use recovery or cancellation. It will not retry automatically.",
  verifyDelivery: "Verify existing message",
  skipDelivery: "Stop remaining delivery",
  verifyDeliveryHelp:
    "Enter the original Slack message timestamp. The server verifies delivery using the original app marker and sent-content fingerprint. This does not prove that the currently displayed Slack text is unchanged, and it will not send again. An unsuccessful check remains unknown.",
  skipDeliveryHelp:
    "Stop the remaining unfinished parts from this source. Sent parts and audit history are retained. This does not mean an uncertain message was not delivered, and it does not delete Slack messages.",
  remoteTimestamp: "Original Slack message timestamp",
  remoteTimestampHelp:
    "Keep the complete string, for example 1710000000.000100. Do not truncate decimal places.",
  verificationUnknown:
    "No matching message was confirmed. The result remains unknown; nothing was sent again.",
  verificationDone:
    "A matching sent message was verified. Nothing was sent again.",
  skipRecorded:
    "The stop action was recorded. Already sent parts are retained.",
  part: "Part",
  deliveryStates: {
    PENDING: "Pending",
    SENDING: "Sending",
    DONE: "Sent and confirmed",
    UNKNOWN: "Unknown result",
    FAILED: "Failed",
    SKIPPED: "Stopped",
  },
  title: "Slack",
  scope:
    "Workspace notifications and personal identity are authorized separately. Each Agent and space task assistant has its own Slack App. Only regular workspaces, joined non-shared channels, and one-to-one DMs are supported.",
  notConfigured:
    "Slack application credentials, encryption, or the public HTTPS URL are not configured. Contact your deployment administrator.",
  loadFailed: "Unable to load Slack configuration. Please retry.",
  reload: "Refresh status",
  organization: "Workspace installations",
  organizationHelp:
    "An organization owner or administrator installs the central notification app. Installation does not bind your personal identity.",
  installOrganization: "Install workspace app",
  personal: "My Slack identity",
  personalHelp:
    "OpenID Connect verifies your workspace and user ID. Email and display names are not used for matching. Personal tokens are not stored and notifications are not enabled automatically.",
  bindIdentity: "Bind my identity",
  unbindIdentity: "Remove my binding",
  identityMissing: "First bind your identity in an installed workspace.",
  identityActive: "Identity verified",
  identityInactive: "Identity no longer valid",
  readOnly:
    "Only target managers can manage apps. Each person must authorize their own identity.",
  agents: "Dedicated Agent apps",
  assistants: "Dedicated space assistant apps",
  createAgent: "Create Agent app",
  createAssistant: "Create space assistant app",
  targetLimit:
    "An Agent or space may have one active bot across all platforms. Creation reserves the target; an unknown result retains the reservation to prevent duplicate apps.",
  noTargets: "No eligible targets. Check your permissions and existing bots.",
  workspace: "Installed workspace",
  agent: "Agent",
  space: "Space",
  runtime: "Computer and Runtime",
  model: "Model (optional)",
  modelHelp:
    "Leave empty to use the selected Runtime default. Existing device and run permissions still apply.",
  name: "Slack App name",
  extraPrompt: "Additional instructions",
  refreshToken: "Configuration refresh token",
  tokenWarning:
    "This credential can manage apps belonging to this Slack user in the workspace, beyond this one App. Submitting rotates the credential chain. Neither the old nor the new refresh token is stored. Do not enter a Bot Token.",
  tokenConsent: "I understand the app management scope and credential rotation",
  tokenCleared:
    "Credentials have been cleared from this form after submission. If the result is unknown, refresh status before taking any further action.",
  create: "Create dedicated app",
  submit: "Submit",
  refreshConfiguration: "Continue configuring this app",
  recover: "Recover known app",
  recoverHelp:
    "Recover only the same verified App; this does not create another app. Supply its credentials and a new Configuration refresh token. Slack export cannot recover lost secrets.",
  externalAppID: "Slack App ID",
  clientID: "Client ID",
  clientSecret: "Client Secret",
  signingSecret: "Signing Secret",
  knownAppConsent:
    "I confirm these credentials belong to the same App, workspace, and original creator",
  appSettings: "Open Slack App settings",
  eventsURL: "Events Request URL",
  manualVerification:
    "Enter this URL in the Slack App Event Subscriptions settings and verify it manually. URL verification and OAuth installation are independent gates; both are required for activation.",
  urlVerified: "Request URL verified",
  urlPending: "Request URL verification pending",
  installed: "OAuth installed",
  installPending: "OAuth installation pending",
  installApp: "Install this app",
  reauthorize: "Reauthorize",
  disconnect: "Disconnect locally",
  disconnectHelp:
    "Stop future inbound and outbound work while retaining the target binding for explicit reinstallation. To switch platforms, also cancel and release the binding. The Slack App is not uninstalled or deleted; history is retained.",
  cancelOperation: "Cancel and release binding",
  cancelHelp:
    "Cancellation ends the target binding. A built-in space assistant is archived; an ordinary Agent is retained. It does not delete or uninstall the Slack App. Known App IDs remain available for inspection.",
  cleanupConsent:
    "I have checked and handled any orphaned Slack App and understand this releases the local target reservation",
  unknown: "Unknown result",
  unknownHelp:
    "The remote operation may have succeeded. Check Slack first. Refresh only reads status; it never creates or sends again.",
  status: "Status",
  waiting: "Waiting for Slack authorization…",
  openAuthorize: "Open Slack authorization",
  oauthDone: "Slack authorization completed",
  oauthFailed:
    "Slack authorization did not complete. Refresh status or start again.",
  oauthUnknown:
    "Authorization exchange result is unknown. Check the installation before recovery; the exchange will not be replayed automatically.",
  oauthExpired: "The authorization session expired. Close it and start again.",
  oauthInvalid:
    "The server returned an invalid authorization URL. It was not opened.",
  return: "Return to Slack integration",
  session: "Authorization session",
  channels: "Public channel reply authorization",
  channelsHelp:
    "The manager must be a verified member of a regular public or private channel that the bot has joined. Shared channels, group DMs, and guests are unsupported. Disabling fences old work and conversations; re-enabling creates a new authorization epoch.",
  channelID: "Slack channel ID",
  channelConsent:
    "I confirm all channel members can see responses and shared task previews",
  enableChannel: "Enable channel replies",
  disableChannel: "Disable channel replies",
  enabled: "Enabled",
  disabled: "Disabled",
  noChannels:
    "No channel authorizations yet. Enter a joined channel ID for server verification.",
  next: "Next page",
  first: "Back to first page",
  deliveries: "Delivery records",
  deliveryHelp:
    "UNKNOWN means delivery is uncertain. Check Slack first. Parts already sent will not be resent automatically.",
  noDeliveries: "No delivery records.",
  centralReplies:
    "The central app provides notifications and authorized task previews. Replies do not update tasks or resume an Agent. Open the task link or use the dedicated Agent App.",
  notificationHelp:
    "Slack has independent choices for events and group mutes. Notifications and direct messages are off by default.",
  manageIdentity: "Manage Slack identity",
  reasonGeneric:
    "Action is required. Check authorization and app configuration.",
  errorGeneric: "The operation failed. Refresh status and check configuration.",
  errorConflict:
    "State or authorization changed. Close this dialog and refresh before retrying; do not resubmit the original credentials.",
  errorForbidden: "Your current identity cannot perform this action.",
  errorUncertain:
    "The request result is uncertain. Refresh the stored status; do not create or send again.",
  errorRate: "Slack is limiting requests. Try again later.",
  errorUnavailable: "Slack is not configured or authorization is unavailable.",
  nameLimit:
    "Names are limited to 35 Unicode characters; additional instructions to 4,000.",
  states: {
    PREPARED: "Prepared",
    PROVING: "Verifying creator",
    PROOF_UNKNOWN: "Credential rotation uncertain",
    CREATING: "Creating",
    CREATE_UNKNOWN: "Creation uncertain",
    APP_CREATED: "App created",
    MANIFEST_UNKNOWN: "Configuration uncertain",
    WAITING_URL_VERIFICATION: "Manual URL verification required",
    WAITING_INSTALL: "Installation required",
    ACTIVE: "Active",
    NEEDS_REAUTH: "Reauthorization required",
    FAILED: "Failed",
    CANCELLED: "Cancelled",
    ORPHANED_APP: "App requires manual cleanup",
    DISCONNECTED: "Disconnected",
  },
  reasons: {
    needs_reauth: "Reauthorization required",
    permission_denied: "Check current management permissions",
    identity_mismatch: "Slack identity or workspace does not match",
    configuration_missing: "Application configuration is incomplete",
    credential_result_unknown:
      "Credential rotation is uncertain; recover with fresh configuration credentials",
    configuration_expired:
      "Temporary configuration credentials expired; continue configuring this app",
    shared_channel: "Shared or cross-organization channels are unavailable",
    rate_limited: "Rate limited; retry later",
    installation_revoked: "The Slack installation was revoked; reauthorize",
    target_in_use:
      "The target already has an active bot; resolve its existing connection",
    expired: "The operation expired",
    cancelled: "The operation was cancelled",
  },
};
