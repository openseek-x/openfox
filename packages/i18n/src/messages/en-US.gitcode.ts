export const gitcodeEn = {
  title: "GitCode / AtomGit organization repositories",
  scope:
    "Connect public or private repositories with an organization administrator's PAT. Official GitCode / AtomGit only; no personal account binding or clone credentials. Assignees remain external identities inbound and are never written upstream.",
  token: "Organization administrator PAT",
  discover: "Verify PAT and choose organization",
  organization: "Remote organization",
  connect: "Connect organization",
  credentials: "Update PAT",
  saveCredentials: "Verify and update PAT",
  sameSubject:
    "A replacement PAT must belong to the same stable user. Login renames do not change UNKNOWN reconciliation. Disconnect and reconnect to change the holder.",
  identity: "Authorized identity: {login} ({subject})",
  repositories: "Repositories and work",
  add: "Add repository",
  search: "Search repositories",
  repository: "Repository",
  space: "Target space",
  select: "Select",
  previous: "Previous page",
  next: "Next page",
  refresh: "Refresh",
  inbound: "Inbound only",
  bidirectional: "Bidirectional",
  direction: "Sync direction",
  bidiHelp:
    "Bidirectional sync uses this organization PAT to send task titles, descriptions, states and new comments. Assignees are never written upstream.",
  history: "Import existing Issues",
  historyHelp:
    "By default only subsequent changes are processed. History is limited to 50 pages / 5,000 items; existing links are not recreated and existing local tasks are not rewritten by history.",
  setupHelp:
    "Saving automatically creates a password Webhook. Issue / PR backlinks are on for private repositories and off for public ones by default; adjust the settings below. Sync starts after Hook verification.",
  addConfirm: "Add and configure Webhook",
  empty: "No repositories selected.",
  status: "Sync: {status} · Hook: {hook}",
  hookId: "Remote Hook ID: {id}",
  permissions: "Issue writes: {issue} · Comment writes: {comment}",
  permissionHelp:
    "UNVERIFIED means no actual write has occurred. Reading or configuring a Hook does not verify write access. VERIFIED / DENIED reflect the most recent actual write result.",
  repositoryChanged:
    "The remote repository identity or visibility changed. Sync is paused. Remove and select the repository again to confirm the new scope; local copies are retained.",
  pause: "Pause",
  resume: "Resume",
  changeDirection: "Change sync direction",
  historyNow: "Import existing Issues",
  historyConfirm: "Start history import?",
  historyBody:
    "Only unlinked Issues are added; existing copies and manual edits are retained. Limited to 50 pages / 5,000 items; failures are reported as incomplete.",
  remove: "Remove sync",
  removeTitle: "Remove this repository sync?",
  removeBody:
    "Stop new work and retain imported content and links. If selected, only the remote Hook owned by OpenFox is deleted. Unknown results are reconciled without recreating it.",
  deleteHook: "Also delete the remote Hook created by OpenFox",
  cleanup:
    "Disconnected; credentials have been destroyed. Repository and Hook identifiers below remain for manual cleanup. OpenFox no longer contacts the platform.",
  disconnectBody:
    "Disconnect immediately destroys the PAT and callback secrets and stops sync, retaining tasks and links. Remote Hooks are not automatically deleted. To clean up, remove each repository with Hook deletion and wait for success before disconnecting.",
  rotate: "Rotate callback secrets",
  rotateTitle: "Rotate all Webhooks on this connection?",
  rotateBody:
    "Old callback secrets stop working immediately. All repositories pause until every Hook is verified. Issues are polled to catch up; missed PR / commit events cannot be fully recovered and need review. Reconcile sending or UNKNOWN Hooks first.",
  work: "Background work and recovery",
  workEmpty: "No work records.",
  unknown:
    "Unknown result: only the original sender, repository and marker are reconciled. No new POST is sent, even if no match is found.",
  reconcile: "Reconcile again",
  retry: "Retry failed work",
  pending: "Processing",
  error: "Status reason: {code}",
  settings: "Links and backlinks",
  outboundWarning:
    "Enabling backlinks sends remote comments using the organization PAT. Comments on public repositories are publicly visible.",
  link_pull_requests: "Link Pull Requests",
  link_commits: "Link Git Commits",
  pr_backlink_private: "Private repository PR backlinks",
  pr_backlink_public: "Public repository PR backlinks",
  issue_backlink_private: "Private repository Issue backlinks",
  issue_backlink_public: "Public repository Issue backlinks",
  branch_format: "Branch name format",
  enableTitle: "Enable remote writes?",
  bidiTitle: "Enable bidirectional sync?",
  confirm: "Enable",
  readOnly:
    "Only organization owners and administrators can manage connections and repositories.",
  repoLimit:
    "Reached the 50-page remote limit; results may be incomplete. Narrow your search.",
  workPage: "Earlier work records",
  repoPage: "More repository records",
  gap: "A Webhook gap occurred. Issues will catch up; missed PR / commit events require review.",
} as const;
