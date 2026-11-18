import type { Messages } from "../translate";
export const codecommitEn: Messages["integrations"]["codecommit"] = {
  title: "AWS CodeCommit organization connections",
  linksTitle: "CodeCommit links",
  scope:
    "Supports 25 verified AWS commercial regions. Links PRs and commits, with PR backlinks governed by existing Git settings. No personal binding, Issue import, cloning or automatic AWS deployment.",
  notConfigured: "The server public URL or encryption key is not configured.",
  bind: "Bind AWS account and region",
  credentials: "Update IAM credentials",
  reconnect: "Update credentials and reconnect",
  credentialsHelp:
    "Uses only the IAM credentials entered here, never local AWS configuration or the default credential chain. Temporary credentials require manual renewal.",
  replaceHelp:
    "Account and region are fixed. You may choose another IAM principal in the same account; subsequent sends use that identity. Reconnecting does not automatically enable retained repositories.",
  region: "AWS region",
  accessKey: "Access key ID",
  secretKey: "Secret access key",
  sessionToken: "Session token (optional)",
  verify: "Verify and save",
  account: "AWS account",
  principal: "Current IAM principal",
  active: "Credentials verified",
  disconnected: "Disconnected",
  needsAttention: "Credentials need attention",
  disconnectHelp:
    "Stops new work while retaining task links and history. In-flight comments may remain unknown; AWS comments are not deleted. Re-enable each repository explicitly after reconnecting.",
  refresh: "Refresh",
  next: "Next page",
  first: "First page",
  loadFailed: "Could not load CodeCommit settings. Refresh to retry.",
  failed:
    "The operation did not complete. Refresh and check before explicitly retrying. If a one-time token was created but its response was lost, rotate it; the original cannot be recovered.",
  conflict:
    "The version changed. Close and reopen, then review the latest account, repository and state. Requests are not automatically replayed.",
  forbidden:
    "You do not have the required organization management or AWS permissions.",
  invalid: "Check the region, credentials, repository and confirmation.",
  credentialError:
    "IAM credentials are invalid or expired. Update them before retrying.",
  kmsError:
    "The repository encryption key is unavailable. Check KMS permissions and key state.",
  upstreamError: "AWS is currently unavailable. Check again later.",
  rateError: "AWS requests are rate limited. Wait before explicitly retrying.",
  settings: "Task links and PR backlinks",
  pr: "Link Pull Requests",
  commits: "Link Git commits",
  backlink: "Send PR backlinks in controlled repositories",
  settingsHelp:
    "Defaults: PR links on, commit links off, private PR backlinks on. Permissions and settings are checked before each send. Commit comments are not sent.",
  repositories: "Selected repositories",
  addRepository: "Select and enable repository",
  repository: "Repository",
  repositoryHelp:
    "Enable up to 20 repositories per connection. Remote pages load explicitly, never in an unbounded loop. Names are case-sensitive.",
  selectRepository: "Choose a repository on this page",
  noRepositories: "No selected repositories on this page.",
  noRemote: "No repositories on this page.",
  initialize: "Enable and establish baseline",
  initializationHelp:
    "Initialization records existing PR and branch baselines without bulk historical links or backlinks. Events and approximately five-minute scans follow. Non-snapshot lists and undelivered short-lived branches cannot guarantee complete recovery.",
  initializationConfirm:
    "I confirm initial baselining only, with subsequent PR backlinks governed by current Git settings.",
  initializing: "Establishing baseline",
  initialized: "Baseline established",
  disabled: "Repository disabled",
  reenable: "Re-enable repository",
  reconcile: "Retry repository scan",
  reconcileConflict:
    "The repository version changed or a scan is already running. Close, refresh and review; wait for an active scan to finish before explicitly retrying.",
  reconcileHelp:
    "Resume from saved scan progress within the existing read budget. The bridge token and repository generation are retained. The server rejects overlapping active scans.",
  disable: "Disable repository",
  disableHelp:
    "Stops the old repository generation while retaining task links and unknown comment results. AWS resources are not deleted.",
  rotate: "Rotate bridge token",
  rotateHelp:
    "The old token stops working immediately and old-generation work stops. Unknown comment intents remain. Update the AWS bridge configuration manually.",
  bridgeTitle: "One-time bridge configuration",
  bridgeURL: "OpenFox receiver URL",
  bridgeToken: "Bearer token",
  bridgeOnce:
    "The URL and token are shown only now and cannot be recovered after closing. Save them in your AWS secret storage, never public templates or logs. Rotate explicitly if lost.",
  bridgeReady: "Token created; bridge deployment unverified",
  bridgeMissing: "No valid bridge token",
  received: "Last accepted event",
  neverReceived: "No event received yet",
  bridgeHelp:
    "Deploy EventBridge → Lambda → OpenFox manually (optionally SNS → Lambda). Lambda POSTs the original EventBridge JSON envelope to the receiver using Authorization: Bearer <token>, at most 256 KiB. Account, region and repository ARN must match exactly. This is bearer-authenticated bridging, not native AWS/SNS signature verification.",
  copy: "Copy bridge configuration",
  copied: "Copied",
  copyFailed: "Could not copy. Copy the displayed configuration manually.",
  requestedAt: "Requested at",
  nextAttempt: "Next attempt",
  work: "Processing and backlink diagnostics",
  noWork: "No work on this page.",
  workHelp:
    "Up to 50 items per page. States describe received or scanned work, not a complete event audit.",
  retry: "Reconcile again",
  unknown: "Resume original comment intent",
  unknownHelp:
    "An unknown result does not mean unsent. This resumes the original token and complete parameters only while current scope and original-sender permissions allow it. No new comment intent or comment send generation is created.",
  unknownConfirm:
    "I understand it may already have been sent; resume only the original intent without creating a new send.",
  retryHelp:
    "Rechecks the connection, repository and permissions. Existing count, graph-read and retry budgets remain.",
  jobLink: "View AWS object",
  waiting: "Still running; wait for the result.",
  states: {
    PENDING: "Pending",
    RUNNING: "Running",
    DONE: "Done",
    FAILED: "Failed",
    IGNORED: "Stopped",
  },
  backlinks: {
    NONE: "No backlink",
    PENDING: "Backlink pending",
    SENDING: "Backlink sending",
    SENT: "Backlink sent",
    UNKNOWN: "Backlink result unknown",
    SKIPPED: "Backlink skipped",
    FAILED: "Backlink failed",
  },
  reasons: {
    credentials: "Credentials invalid or expired",
    permission: "Insufficient permissions",
    kms: "KMS key unavailable",
    repository: "Repository identity changed",
    notFound: "Resource unavailable",
    rate: "Rate limited",
    upstream: "Upstream unavailable",
    incomplete: "Incomplete response or pagination",
    budget: "Commit graph budget exceeded; no partial application",
    commits: "More than 100 new commits; no partial application",
    graph: "Commit graph completeness could not be proven",
    version: "Version changed",
    sender: "Original sender changed",
    unknown: "Comment result unknown",
    generation: "Repository generation changed",
    disconnected: "Connection disconnected",
    other: "Check current permissions, configuration and processing state",
  },
};
