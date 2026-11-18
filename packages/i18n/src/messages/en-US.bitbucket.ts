import type { bitbucketZh } from "./zh-CN.bitbucket";
export const bitbucketEn: Record<keyof typeof bitbucketZh, string> = {
  linksTitle: "Bitbucket links",
  completed:
    "Authorization confirmation is complete. Return to settings to see the current connection status.",
  organizationTitle: "Bitbucket Cloud workspace",
  organizationDescription:
    "Connect one Bitbucket Cloud workspace per organization. Authorization requires an OpenFox administrator who also administers the workspace. Organization backlinks use this authorized account, never personal identity credentials.",
  scopeNotice:
    "Organization OAuth requests account + pullrequest. The latter also permits PR approvals, PR task management and comments. OpenFox uses only the approved read and task backlink features; it does not create, approve or merge PRs.",
  notConfigured:
    "The server's Bitbucket organization consumer is not configured. Personal identity requires a separate account-only consumer. Contact your administrator.",
  completeTitle: "Confirm Bitbucket organization authorization",
  identityTitle: "Your Bitbucket identity",
  identityDescription:
    "A separate account-only OAuth verifies your UUID once for future PR and PR comment activity authors in this workspace. No personal token is retained. This does not prove ongoing workspace membership; revoking upstream OAuth does not unlink this identity automatically. Unlink it locally in OpenFox. Historical authors and task assignees stay unchanged.",
  identityConsent:
    "I confirm this is my Bitbucket identity and understand this is a one-time identity association.",
  organizationConsent:
    "I authorize this workspace for the organization. Future task backlinks will use the displayed authorized account.",
  senderChanged:
    "The organization backlink sender will change. Existing unknown sends remain reconciled against the original sender and are not automatically resent.",
  previousSender: "Previous sender: {name} · {id}",
  newSender: "New sender: {name} · {id}",
  senderConsent:
    "I explicitly agree to change the sender for future backlinks.",
  confirmWorkspace: "Confirm organization authorization",
  confirmIdentity: "Confirm my identity",
  workspace: "Bitbucket workspace",
  chooseWorkspace: "Choose a workspace you administer",
  sameWorkspace:
    "Reauthorization keeps the original workspace. Disconnect first to connect a different workspace.",
  noWorkspace:
    "This account has no selectable administrator workspace. Organization authorization cannot be completed.",
  connect: "Authorize Bitbucket workspace",
  reauthorize: "Reauthorize",
  delegatingUser: "Organization sender: {name} · {id}",
  manageIdentity: "Manage my identity association",
  openConfirmation: "Open authorization confirmation",
  disconnectDescription:
    "Disconnecting stops new links and backlinks and disables personal attribution. Existing links and activities remain. Requests already sent cannot be recalled. Remove Webhooks manually in Bitbucket; local disconnection does not revoke upstream consumer authorization.",
  identityDisconnect:
    "Unlinking stops attribution of future PR and PR comment activities. Existing activities remain. This does not revoke authorization in Bitbucket.",
  failed:
    "Authorization was not completed, was already consumed or is no longer valid. Start again.",
  expired: "This authorization expired. Start again.",
  permission:
    "This account lacks the required organization or workspace administrator permissions.",
  scopeDenied:
    "Consumer permissions do not match this authorization purpose. Check the organization and personal consumer configuration.",
  appChanged:
    "The OAuth consumer configuration changed. Start authorization again.",
  changed:
    "The organization connection or permissions changed. Refresh and authorize again.",
  rateLimited:
    "Bitbucket is temporarily rate limiting requests. Try again later.",
  needsAttention:
    "Authorization needs attention. Check the connection and reauthorize.",
  commentActivity: "Bitbucket PR #{number} comment #{comment} by {author}",
  externalAuthor: "External author",
  boundAuthor: "Verified attribution: {name}",
  boundIdentity: "Verified local identity attribution",
  commentDeleted: "This source comment was deleted",
  commentUpdated: "This source comment was updated",
  sourceUpdated: "Source updated:",
  openSourcePR: "View source PR",
  jobsTitle: "Association and backlink diagnostics",
  jobsDescription:
    "Shows at most 50 recently updated jobs, not a complete history. Opening the page, reauthorizing or refreshing never automatically clears UNKNOWN and resends.",
  backlinkJob: "Task backlink",
  eventJob: "Source event",
  jobAttempts: "Attempted {count} times",
  unknownDescription:
    "The send result is unknown; the upstream service may already have received the comment. Automatic resending has stopped. Inspect the source PR first. Not finding a comment does not prove it was not sent.",
  inspectSource: "Inspect source",
  reviewResend: "Review and request resend",
  resendDescription:
    "Sending again may duplicate a comment. Continue only after manually checking the source PR. The retry uses the current organization-authorized account at execution time and is audited.",
  unsentConsent:
    "I checked the source PR and confirm it was not sent. I understand and accept the duplicate comment risk.",
  confirmResend: "Confirm unsent and resend",
  alreadyConnected:
    "This organization already has a Bitbucket workspace. Use the existing connection or disconnect it first.",
  repositoriesTitle: "Repositories and Webhook",
  repositoryScope:
    "Associate tasks only in selected repositories and allowed spaces. PR linking defaults on; commits default off. No Issue import, source cloning or automatic task status changes.",
  selectRepository: "Select repository",
  repository: "Bitbucket repository",
  repositorySettings: "Repository association settings",
  hookReady: "Current signed event received",
  hookPending: "Waiting for manual Webhook verification",
  privateRepository: "Private repository",
  publicRepository: "Public repository",
  privacyUnknown: "Privacy unknown; sending disabled",
  repositoryPaused:
    "Repository work is paused. Review diagnostics and explicitly confirm settings again.",
  removeRepository: "Remove repository",
  removeDescription:
    "Removal stops new repository work and preserves existing links and history. Requests already sent cannot be recalled. Delete the Webhook manually in Bitbucket.",
  removeConsent:
    "I confirm removal and will clean up the remote Webhook manually.",
  confirmRemove: "Confirm removal",
  webhookTitle: "Manual Webhook setup",
  webhookInstructions:
    "In each selected repository, open Repository settings / Webhooks and manually add the callback URL and signing Secret, enabling the events below. Repository administrator permission is required. OpenFox does not create or delete remote Webhooks. Backlinks wait for a valid new signed event from each repository.",
  viewWebhook: "View manual Webhook configuration",
  payloadURL: "Payload URL",
  signingSecret: "Signing Secret",
  showSecret: "Show secret",
  hideSecret: "Hide secret",
  webhookUnconfigured:
    "No callback URL or signing secret has been generated. Viewing does not generate them.",
  rotateDescription:
    "Generation or rotation changes local configuration only. The old URL becomes invalid immediately. Backlinks pause for every repository until a new signed event arrives. Update every remote Webhook manually. Requests already sent cannot be recalled.",
  rotateConsent:
    "I understand and will manually configure or update all selected repositories.",
  rotateWebhook: "Rotate callback URL and secret",
  createWebhook: "Generate callback URL and secret",
  recentDeliveries: "Recent deliveries",
  queuedNotice:
    "QUEUED means durably accepted for processing, not that association or sending has succeeded.",
  loadedRepositories: "Loaded {count} repositories",
  loadMore: "Load more repositories",
  browseLimit:
    "This browse reached 1000 rows or 20 pages; later results were not loaded. Reopen the browser when the ten-minute cursor expires.",
  allowedSpaces: "Allowed spaces",
  pullRequests: "Associate Pull Requests",
  commits: "Associate commits (at most 100 distinct commits per push)",
  privateBacklink: "Send task backlinks to private repository PRs",
  publicBacklink: "Send task backlinks to public repository PRs",
  metadataConsent:
    "I agree that members of these spaces can see repository PR, commit and author metadata, including private repository information.",
  backlinkConsent:
    "I confirm that future task backlinks use the organization-authorized account {name}.",
  publicConsent:
    "I explicitly agree to publish task identifiers, titles and links in a public repository.",
  copyPayloadURL: "Copy callback URL",
  copySecret: "Copy signing secret",
  copyFailed: "Copy failed. Select the value and copy it manually.",
};
