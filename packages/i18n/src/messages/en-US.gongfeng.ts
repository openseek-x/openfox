import type { Messages } from "../translate";
export const gongfengEn: Messages["integrations"]["gongfeng"] = {
  linksTitle: "Gongfeng links",
  scope:
    "Supports the partner edition at git.tencent.com only, with an organization PAT. Personal binding, assignee writeback and private Git cloning are not included.",
  bind: "Connect Gongfeng",
  reconnect: "Reconnect",
  reconnectHelp:
    "Explicitly restore this connection using a new PAT for the same Gongfeng user. Saving updates and activates remote Hooks for the selected repositories. Existing mappings remain; uncertain sends are reconciled without changing the sender.",
  replace: "Update access token",
  token: "Personal access token (PAT)",
  tokenHelp:
    "Use your own PAT with api access. It is encrypted on the server. A replacement must belong to the same Gongfeng user.",
  tokenInvalid: "Use 1–4096 printable ASCII characters without whitespace.",
  repositories: "Repository sync",
  add: "Add repository sync",
  browse: "Read accessible repositories",
  search: "Search repositories",
  project: "Select repository",
  noProjects:
    "No eligible repositories on this page. Only unarchived Git repositories with Issues enabled can be selected.",
  space: "Target space",
  noSpaces: "No available spaces. Create a space or request access first.",
  direction: "Sync direction",
  inbound: "Inbound only",
  bidirectional: "Bidirectional",
  directionHelp:
    "Imports Issues and new comments. Bidirectional sync also writes titles, bodies, open/closed state and new comments, but not assignees.",
  history: "Import existing Issues",
  mr: "Link merge requests",
  commits: "Link commits",
  backlink: "Post task backlink comments",
  visibility:
    "Backlinks in a non-private repository may expose task names and links to more people.",
  unknownPrivacy:
    "Repository visibility is unknown. Backlinks cannot be enabled yet.",
  hookConsent: "I confirm creating and activating a remote Webhook",
  hookHelp:
    "Adding a repository automatically creates and activates a Push, Issue, Note and merge request Webhook, including for inbound-only sync.",
  create: "Create sync",
  pending:
    "Request saved. Refresh to check Hook activation and processing results.",
  refresh: "Refresh status",
  previous: "Previous page",
  next: "Next page",
  more: "More records are available on the next page.",
  empty: "No repository syncs yet.",
  edit: "Edit sync",
  remove: "Remove sync",
  removeHelp:
    "Stops sync and queues deletion of the remote Hook created by this connection. Imported tasks remain. Uncertain operations must be reconciled first.",
  pause: "Pause sync",
  resume: "Resume sync",
  retry: "Retry reconciliation",
  reconcile: "Recheck events",
  reconcileHelp:
    "Rereads the selected range; this does not mean processing is complete. Commit comparisons must be complete and contain no more than 100 new commits.",
  mode: "Recheck type",
  issues: "Issues",
  merge_requests: "Merge requests",
  push: "Commit range",
  from: "Start time (RFC3339)",
  to: "End time (RFC3339)",
  fromSHA: "Starting commit SHA",
  toSHA: "Ending commit SHA",
  ref: "Branch ref (refs/heads/…)",
  windowInvalid:
    "Enter a valid RFC3339 range. The end must follow the start and cannot be in the future.",
  pushInvalid:
    "Enter two distinct nonzero 40-character lowercase commit SHAs and a refs/heads/ branch ref.",
  rotate: "Rotate Webhook endpoint",
  rotateHelp:
    "The old endpoint expires immediately. Repositories pause while Hooks are updated and activated. Events may be missed; recheck the gap afterwards.",
  disconnectHelp:
    "Clears the local PAT and endpoint credentials and stops sync. Imported copies remain. Remote Hooks are retained: remove them manually using the repository and Hook ID below.",
  disconnected:
    "Connection disconnected. These are retained sync records only. Manually remove remote Hooks in each repository.",
  hook: "Webhook status",
  hookID: "Remote Hook ID",
  status: "Sync status",
  reason: "Status detail",
  unknown:
    "Result awaiting reconciliation; the request will not be blindly resent.",
  excluded: "Confidential Issues skipped: {count}",
  scanTitle: "Scan progress",
  scanHelp:
    "Two scan passes are tracked separately from subsequent processing. A completed scan does not mean every object was imported or processed successfully. Check processing records too.",
  scanDetail:
    "{kind}: pass {pass}, page {page}; scanned {count} in this pass and {first} in the first pass.",
  scanHistory: "Historical import adds missing objects only.",
  scanWindow: "Range: {from} — {to}",
  scanStatus: {
    running: "Scanning",
    complete: "Scan complete",
    incomplete: "Scan incomplete",
  },
  work: "Processing records",
  workHelp:
    "Received does not mean processed. Completion, backlink state and next attempt are shown separately. Comment bodies and credentials are not displayed.",
  noWork: "No processing records yet.",
  workItem: "{kind} · {key}",
  generation: "Requested version {requested} / completed version {completed}",
  backlinkState: "Backlink state: {state}",
  nextAttempt: "Next attempt: {time}",
  conflict:
    "Content changed. Versions refreshed and your draft was kept. Review the latest state before saving again.",
  unavailable:
    "Gongfeng is unavailable. Try again later; completion was not confirmed.",
  incomplete:
    "Gongfeng returned incomplete results. This operation could not be confirmed.",
  limited: "Gongfeng rate limit reached. Try again later.",
  invalid:
    "Invalid Gongfeng request or configuration. Check the inputs and repository permissions.",
  queueFull: "The processing queue is full. Try again later.",
};
