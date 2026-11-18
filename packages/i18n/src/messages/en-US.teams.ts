export const teamsEn = {
  title: "Teams bots and personal identity",
  scope:
    "Supports commercial classic single-tenant bots in personal chats, explicitly enabled group chats and verified standard post channels. Private/shared channels, preview threads, attachments and notifications are excluded.",
  configuration:
    "Encrypted credentials or a public HTTPS callback are not configured. Contact an administrator.",
  bots: "Manage bots",
  noBots: "No Teams bots you can manage.",
  create: "Connect Teams bot",
  save: "Verify and save",
  refresh: "Refresh status",
  kind: "Target",
  agent: "Agent",
  assistant: "Space task assistant",
  space: "Space",
  runtime: "Runtime",
  extra: "Extra instructions",
  name: "App name",
  tenant: "Tenant ID",
  app: "Microsoft App ID",
  secret: "Client secret",
  credentialHelp:
    "The secret is verified and stored encrypted. Lists and downloads never return it. Closing this form clears the input.",
  manifestVersion: "App version",
  developer: "Developer name",
  website: "Developer website HTTPS URL",
  privacy: "Privacy policy HTTPS URL",
  terms: "Terms of use HTTPS URL",
  realURLs:
    "Use your actual developer and policy URLs. The package uses OpenFox icons and only channel-settings read permission. Installation and RSC consent are completed manually in Teams.",
  endpoint:
    "Configure this messaging endpoint in Azure Bot and install the app manually:",
  verified: "Credentials verified: {time}",
  inbound: "Authenticated activity received: {time}",
  delivered: "Reply delivery confirmed: {time}",
  notObserved: "Not yet observed",
  noEndToEnd:
    "Successful credential verification does not prove installation, inbound activity or reply delivery.",
  rotate: "Update this app’s secret",
  download: "Download Teams app package",
  remove: "Remove local connection",
  removeHelp:
    "Stops new input and replies, clears the secret and revokes local identities and scopes. History and unknown results remain. It does not uninstall the Microsoft app.",
  confirmRemove: "Confirm local removal",
  details: "Scopes and delivery records",
  scopes: "Group and channel scopes",
  noScopes:
    "Send an activity in a group or channel containing the installed bot to discover the scope. Execution is disabled by default.",
  scopeHelp:
    "Replies are visible to members of the enabled group or channel. Disabling invalidates prior input, runs and replies; reopening starts a new OpenFox conversation. Channels are verified as standard post channels before execution and delivery.",
  consent: "I consent to replies visible in this group or channel",
  enable: "Enable replies",
  disable: "Disable replies",
  enabled: "Enabled",
  disabled: "Disabled",
  more: "Load more scopes",
  deliveries: "Recent delivery records",
  noDeliveries: "No delivery records.",
  unknown:
    "Delivery is unknown and automatic retries have stopped. Check Teams; this page will not resend.",
  queued: "Pending",
  sent: "Delivery confirmed",
  failed: "Stopped; attention needed",
  active: "Active connection",
  disconnected: "Disconnected",
  personal: "My Teams identity",
  personalHelp:
    "Bind each bot separately. Send the one-time command only in a personal chat with that bot. Authentication and Connector member/AAD checks bind the identity immediately, without a second web confirmation.",
  noTargets: "No active bot you can access.",
  bind: "Bind my identity",
  issue: "Generate one-time binding command",
  commandHelp:
    "Valid for ten minutes. Do not share it. Closing hides it; generating another invalidates the previous unused command.",
  expires: "Expires {time}",
  hide: "Hide command",
  expired: "This command expired. Generate another.",
  unbind: "Unbind my identity",
  unbindHelp:
    "Unbinding stops future execution and replies. Binding again does not restore authority for old conversations or runs.",
  identityActive: "Identity active",
  identityInactive: "Identity inactive",
  error: "The request failed. Refresh and check the configuration.",
  forbidden:
    "Your current identity cannot manage this target. Check organization, space and Agent permissions.",
  conflict: "Configuration or binding changed. Refresh before retrying.",
  invalid: "Check the input fields.",
  credentials:
    "Credentials or target could not be verified. Check the tenant, app, secret and permissions.",
  unsupported:
    "The supported scope could not be verified. Only unarchived standard post channels are allowed.",
  rateLimited:
    "The upstream service requested a later retry. Automatic retries stop when the wait is invalid.",
  authorization:
    "Authorization changed or was revoked. Previous work will not resume.",
};
