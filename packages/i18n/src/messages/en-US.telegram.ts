export const telegramEn = {
  ignoredTopics: "Ignored topic messages: {count}",
  title: "Telegram Bots and personal identity",
  scope:
    "Connect one fixed Bot to an Agent or space task assistant in this organization. Only plain private and group text is supported; topics, channels, anonymous senders and attachments are excluded. This integration is not a notification channel.",
  configured:
    "Secure credentials or a public HTTPS callback are not configured. Contact an administrator.",
  bots: "Manage Bots",
  noBots: "No Bots you can manage.",
  create: "Connect Bot",
  agent: "Agent",
  assistant: "Space task assistant",
  kind: "Connection target",
  space: "Space",
  runtime: "Runtime",
  extraPrompt: "Additional prompt",
  token: "BotFather Token",
  tokenHelp:
    "The Token is verified and stored encrypted. It is never returned in lists. Closing clears this form.",
  permanent:
    "Confirmation permanently reserves this Bot for this organization and target, including after disconnect. Existing external Webhooks will not be overwritten.",
  prepare: "Verify Bot",
  preparation: "Review connection",
  remoteID: "Telegram Bot ID",
  username: "Bot username",
  confirmBot: "I have checked the Bot and fixed target",
  confirmConnection: "Confirm and configure Webhook",
  preparedOnly:
    "Verification has not configured a Webhook. Confirmation queues configuration; check the resulting status.",
  expires: "Expires at {time}",
  refresh: "Refresh status",
  rotate: "Update this Bot's Token",
  recover: "Recover Webhook configuration",
  recoverConsent:
    "I confirm recovery for this same Bot. Credentials and the current remote configuration will be checked again; messages will not be resent.",
  configuredAt: "Webhook configuration accepted: {time}",
  firstUpdate: "Authenticated update received: {time}",
  noUpdate:
    "No authenticated update received yet. End-to-end connectivity is not confirmed.",
  reconcile: "Read-only Webhook check",
  reconcileHelp:
    "Checks remote configuration without resending messages. Unknown results remain until reconciled.",
  disconnect: "Disconnect",
  disconnectHelp:
    "Stops new local inputs and replies and clears the Token. History, unknown results and the fixed Bot reservation remain.",
  local: "Disconnect locally and keep the remote Webhook",
  remote: "Remove the remote Webhook, then disconnect locally",
  remoteHelp:
    "Remote removal may require waiting or reconciliation. Pending Telegram updates are not dropped.",
  personal: "My Telegram identity",
  personalHelp:
    "Bind separately for each Bot. Sending the command alone is not confirmation: return here and verify your identity.",
  noTargets:
    "No Bots are currently available to bind. The connection must be active and its target accessible to you.",
  bind: "Bind my identity",
  unbind: "Unbind my identity",
  unbindHelp:
    "Unbinding affects future execution immediately. Existing conversations remain; binding again creates a new identity and private conversation.",
  issue: "Create one-time binding link",
  openTelegram: "Open Telegram to submit my candidate",
  secretLink:
    "This link contains a one-time secret and expires in 10 minutes. Do not share it. Closing hides it; you may create another.",
  hideLink: "Hide one-time link",
  waiting:
    "Waiting for the binding command in Telegram private chat. Refresh to check your candidate.",
  candidate: "Telegram identity awaiting confirmation",
  candidateID: "Telegram user ID",
  confirmIdentity: "Confirm this is me",
  identityActive: "Binding is active",
  identityInactive: "Binding is inactive",
  groups: "Group authorization",
  privacyHelp:
    "Telegram privacy mode affects which group text the Bot receives. Check it in BotFather; only messages actually delivered can be used here.",
  groupHelp:
    "Bind your own identity, then send /openfox_connect@{username} in the target group. It must be an ordinary group where the Bot is still present. This does not verify that you are a Telegram administrator.",
  noDiscoveries: "No current group candidates initiated by you.",
  publicConsent: "I accept that everyone in this group can see Bot replies",
  background: "Use ordinary text in this task-assistant group as context",
  backgroundHelp:
    "Separate opt-in uses only received text: up to 20 messages, 10 minutes and 8,000 characters. No history is fetched and context does not grant execution authority.",
  enableGroup: "Enable group replies",
  disableGroup: "Disable group replies",
  groupEnabled: "Group replies enabled",
  groupDisabled: "Group replies disabled",
  groupEpoch:
    "Disabling stops new group input and clears context. Reopening needs a new candidate and consent and starts a new conversation.",
  diagnostics: "Reply diagnostics",
  noDeliveries: "No reply records.",
  loadMore: "Load more",
  unknownDelivery:
    "Delivery is unknown and may have reached Telegram. No automatic or manual resend is available; check the original conversation.",
  states: {
    SETTING: "Configuration queued",
    ACTIVE: "Configuration active",
    ATTENTION: "Action needed",
    DELETING: "Removing remote configuration",
    DISCONNECTED: "Disconnected locally",
    PENDING: "Pending",
    SENDING: "In progress",
    UNKNOWN: "Unknown result",
    DONE: "Done",
    FAILED: "Failed",
    PENDING_CONFIRM: "Awaiting confirmation",
    COMPLETED: "Confirmed",
    CANCELLED: "Cancelled",
    CANDIDATE: "Candidate received",
    WAITING: "Awaiting candidate",
    SENT: "Sent",
    SKIPPED: "Stopped",
    generic: "Refresh to check the current state",
  },
  errors: {
    generic: "The action did not complete. Refresh the status before retrying.",
    invalid: "Check the input fields.",
    forbidden: "You do not currently have permission for this action.",
    conflict: "Configuration or permissions changed. Refresh and review again.",
    occupied: "This Bot or target is reserved and cannot be transferred.",
    webhook:
      "This Bot already has another Webhook. OpenFox will not overwrite it.",
    expired: "This session expired or changed state. Start again.",
    unavailable: "Telegram could not complete verification. Try later.",
    credentials: "The Bot Token is invalid. Check it in BotFather.",
    unsupported: "This Bot or chat is outside the supported scope.",
    rateLimited: "Too many requests. Try again later.",
    authorization:
      "Authorization is no longer valid. Check membership, target access and your identity binding.",
    unknown:
      "The result is unknown. Its state is retained for reconciliation; it will not be resent.",
  },
} as const;
