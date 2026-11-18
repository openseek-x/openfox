import type { Messages } from "../translate";

/** Stage 12g · Share snapshots (English). */
export const shareEn: Messages["share"] = {
  messageAction: "Share this message",
  menu: "Share snapshot…",
  dialog: {
    messageDescription:
      "Share only this message’s text, author, and time. Replies, attachments, and other context are excluded.",
    title: "Share snapshot",
    description:
      "A snapshot is a read-only text copy of the content right now. Later edits or deletion of the original do not affect it. Attachments and artifacts are not included.",
    scopeLabel: "Who can view",
    scopePublic: "Anyone (public link)",
    scopePublicHelp:
      "Anyone with the link can view it without signing in. Share with care.",
    scopeOrg: "Organization members only",
    scopeOrgHelp:
      "Requires signing in as an active member of this organization.",
    publicDisabled:
      "Public sharing is turned off for this organization. Ask an owner or admin to turn it on in organization settings.",
    expiryLabel: "Expires",
    expiry7d: "7 days",
    expiry30d: "30 days",
    expiryNever: "Never",
    titleLabel: "Title (optional)",
    titlePlaceholder: "Defaults to the original title or first message",
    create: "Create link",
    cancel: "Cancel",
    done: "Done",
    linkReady: "Link created",
    linkOnce: "You can recover active links you created from Manage shares.",
    linkLabel: "Share link",
    copy: "Copy link",
    copied: "Link copied",
    copyFailed: "Copy failed. Select the link and copy it manually.",
    manage: "Manage share links",
    findingsTitle:
      "Possible sensitive information found. Creation was blocked.",
    findingsHint:
      "Edit the original content to remove it, then try again. Nothing is replaced for you automatically.",
    findingTitle: "Title",
    findingItem: "Item {item}",
    findingPosition: "{target}, line {line}, column {column}: {rule}",
  },
  rules: {
    private_key_block: "Private key",
    aws_access_key_id: "AWS access key",
    github_token: "GitHub token",
    gitlab_token: "GitLab token",
    anthropic_api_key: "Anthropic key",
    openai_api_key: "API key",
    slack_token: "Slack token",
    google_api_key: "Google API key",
    stripe_live_key: "Stripe key",
    jwt: "JWT",
    authorization_header: "Authorization credential",
    bearer_token: "Bearer token",
    url_with_credentials: "URL with password",
    credential_assignment: "Password or key assignment",
    unknown: "Possible sensitive information",
  },
  errors: {
    recoverFailed:
      "Cannot recover this link. It may be revoked, expired, or unavailable.",
    generic: "Could not create the share link. Try again later.",
    notFound: "Nothing to share, or you don't have permission to view it",
    empty: "There is no text to share",
    tooLarge: "The content is too large for a snapshot",
    quota:
      "You have reached the limit of active share links. Revoke ones you no longer need.",
    publicDisabled: "Public sharing is turned off for this organization",
    revokeFailed: "Could not revoke. Try again later.",
    listFailed: "Could not load share links",
  },
  manage: {
    copy: "Copy link",
    title: "Share links",
    description:
      "Snapshots you created. Owners and admins can see and revoke every share link in the organization.",
    empty: "No share links yet",
    kindConversation: "Conversation",
    kindComment: "Comment thread",
    scopePublic: "Public",
    scopeOrg: "Organization only",
    status: { ACTIVE: "Active", EXPIRED: "Expired", REVOKED: "Revoked" },
    items: "{count} items",
    creator: "By {name}",
    createdAt: "Created {date}",
    expiresAt: "Expires {date}",
    neverExpires: "Never expires",
    revoke: "Revoke {title}",
    revokeTitle: "Revoke this share link?",
    revokeBody: "The link stops working immediately. This can't be undone.",
    revokeConfirm: "Revoke",
    revoked: "Share link revoked",
  },
  view: {
    loading: "Loading…",
    notAvailableTitle: "This content isn't available",
    notAvailableHelp:
      "The link may have been revoked or expired, or you may not have permission. Organization-only content requires signing in first.",
    signIn: "Sign in to view",
    meta: "Shared by {name} · {org}",
    createdAt: "Created {date}",
    expiresAt: "Valid until {date}",
    snapshotNote:
      "This is a read-only snapshot from the time of sharing and won't update with the original.",
    agent: "Agent",
    replyTo: "Reply to item {n}",
  },
};
