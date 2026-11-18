import type { discordZh } from "./zh-CN.discord";
export const discordEn: Record<keyof typeof discordZh, string> = {
  scope:
    "Supports ordinary server text channels and bot DMs. Task assistants act only when mentioned in an authorized channel. Source messages edited before execution-time verification are ignored; known deletions stop pending execution, while committed tasks remain.",
  title: "Discord bots",
  personal: "My Discord identities",
  createAgent: "Connect an Agent bot",
  createAssistant: "Connect a task assistant",
  connect: "Connect bot",
  token: "Bot Token",
  tokenHelp:
    "Get a bot Token from Discord Developer Portal. It is scoped to this organization and target; it never signs in as a user.",
  target: "Target",
  space: "Space",
  agent: "Agent",
  runtime: "Computer and Runtime",
  model: "Model",
  extra: "Additional instructions",
  prepare: "Verify credentials",
  confirm: "Confirm connection",
  confirmHelp:
    "Check the bot and application identities. Then install the bot in a server and authorize each channel separately.",
  confirmMe: "I confirm this is the bot to connect",
  verified: "Verified bot",
  application: "Application ID",
  expires: "Expires at {time}",
  tokenCleared: "The Token has been cleared from the form.",
  install: "Install in Discord",
  installHelp:
    "If not installed yet, open the official installation page and select a server. Installation does not authorize OpenFox to read or reply in channels.",
  reconnect: "Reconnect",
  rotate: "Replace Token",
  disconnect: "Disconnect",
  disconnectHelp:
    "Stop local messages and replies and erase credentials and temporary material. Tasks and history stay. This does not uninstall the Discord bot or revoke its Token; manage those in Developer Portal.",
  disconnected: "Disconnected",
  active: "Credentials verified",
  attention: "Needs attention",
  online: "Gateway online",
  pending: "Gateway connecting",
  gap: "{count} connection gaps occurred. Messages may be missing; historical commands will not be executed automatically.",
  contentMissing:
    "Message Content is unavailable. The task assistant cannot read channel context.",
  bind: "Bind my identity",
  unbind: "Unbind identity",
  unbindHelp:
    "The old identity and channels it authorized stop working immediately. A new binding requires confirmation and does not inherit old conversation permissions.",
  codeHelp:
    "DM this bot in Discord with the exact one-time command below, then return to confirm your identity. Do not post the code in a channel.",
  waiting: "Waiting for the bot to receive your DM…",
  candidate: "Discord user awaiting confirmation",
  confirmIdentity: "Confirm this is me",
  identityConsent:
    "This Discord user is me. Allow the bot to use my current OpenFox permissions",
  expired:
    "This binding session expired or was cancelled. Close it and start again.",
  bound: "Bound",
  bindFirst: "Bind your own Discord identity before managing channels.",
  channels: "Authorized channels",
  guild: "Server",
  channel: "Text channel",
  enable: "Enable channel",
  disable: "Disable channel",
  publicConsent:
    "Allow public replies in this channel; channel members may see their content",
  historyConsent:
    "Allow up to 20 messages before the trigger as task context. The OpenFox conversation history will be shared",
  channelHelp:
    "Only ordinary text channels are supported. Discord and OpenFox management permissions are checked on the server. Re-enabling creates a new shared conversation.",
  enabled: "Enabled",
  paused: "Paused",
  disabled: "Disabled",
  noChannels: "No channels have been authorized.",
  next: "Next page",
  reload: "Reload",
  deliveries: "Reply history",
  unknown: "Delivery unknown",
  unknownHelp:
    "The request may have been delivered. It will not be resent automatically; check Discord first.",
  emptyDeliveries: "No replies yet.",
  loadFailed: "Could not load Discord settings.",
  error: "The request could not be completed. Reload and try again.",
  conflict:
    "The configuration or identity changed. Close this dialog and reload.",
  forbidden: "Permission is insufficient or the target is unavailable.",
  limited: "Discord is rate limiting requests. Try again later.",
  unavailable: "Discord verification is temporarily unavailable.",
  empty: "No bots are connected.",
  state: "Status",
  noTargets: "No available targets",
  botID: "Bot ID",
  identityID: "User ID",
};
