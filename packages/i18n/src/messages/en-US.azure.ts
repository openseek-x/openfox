import type { azureZh } from "./zh-CN.azure";
export const azureEn: Record<keyof typeof azureZh, string> = {
  linksTitle: "Azure DevOps links",
  pushedBy: "Pushed by {name}",
  remove: "Remove",
  organizationTitle: "Shared Azure DevOps connection",
  connect: "Connect Azure DevOps",
  completeTitle: "Confirm Azure DevOps connection",
  tenant: "Microsoft Entra tenant ID",
  servicesOnly:
    "Azure DevOps Services with Microsoft Entra work or school accounts only. Enter the tenant UUID. Azure DevOps Server and personal Microsoft accounts are not supported.",
  sharedDescription:
    "This connection delegates the authorizing user's access. OpenFox administrators can import accessible projects in the selected Azure DevOps organization. Repository events can link tasks; enabled PR backlinks post as this user. It does not grant global enterprise administrator access.",
  shareConsent:
    "I agree to share this user's accessible data in the selected organization and enable configured repository hooks and PR backlinks.",
  workspace: "Azure DevOps organization",
  chooseWorkspace: "Choose the exact Azure DevOps organization",
  confirmWorkspace: "Confirm sharing this organization",
  identityTitle: "Azure DevOps personal identity",
  identityDescription:
    "One OAuth authorization verifies your active identity in the selected organization for attribution. Personal access and refresh tokens are not retained. Email is not used to infer identity.",
  confirmIdentity: "Confirm personal identity",
  manageIdentity: "Manage personal connections",
  openConfirmation: "Open confirmation",
  notConfigured:
    "An administrator must configure separate organization and identity-only Entra applications.",
  delegatingUser: "Delegating user: {name} ({id})",
  reauthorize: "Reauthorize",
  needsAttention: "Needs attention",
  affected: "Reauthorization affects these shared organizations:",
  reverifyNotice:
    "Personal identities in these organizations must be verified again. Existing tasks and historical links remain.",
  failed:
    "Azure DevOps connection did not complete. Check permissions or authorize again.",
  expired: "Authorization expired. Start again.",
  identityMismatch:
    "This is not the same active user in the selected organization.",
  permission: "The delegating user does not have the required access.",
  rateLimited: "Azure requests are busy or limited. Try again later.",
  changed: "The connection changed. Refresh and try again.",
  appChanged:
    "The Entra application changed. Disconnect this connection and create a new one.",
  scope:
    "The Entra application permissions do not meet the required scope. Ask an administrator to check them.",
  unavailable: "The selected Azure DevOps resource is unavailable.",
  alreadyConnected:
    "This Azure DevOps organization is already connected. Use the existing connection.",
  identityDisconnect:
    "Future imports and repository events will no longer be automatically attributed to you. Historical attribution remains.",
  disconnectDescription:
    "Disconnecting {workspace} stops sync and new events. OpenFox will attempt remote hook cleanup; check repository and hook status for any required manual deletion. Imported tasks and historical links remain.",
  syncTitle: "Azure Boards inbound sync",
  importScope:
    "Import work items and parent relationships with explicit type, state, priority and member mappings. Descriptions become plain text. Comments and attachments are not imported.",
  syncScope:
    "Inbound only: title, description, state and new work items. Existing members and parent relationships are not continuously replaced. Local changes remain when the source is unchanged.",
  fetchScope:
    "The project is read in batches. Mapping starts only after the full read succeeds. More than 5,000 work items or content limits stop the entire operation.",
  history: "Import existing work items",
  historyHelp:
    "By default, changes are received from activation onward; unchanged earlier items are not backfilled. Select this to import existing content, or request history later from sync settings.",
  historyFailed:
    "History import stopped. Check source access, then request history again. It will not silently switch to incremental sync.",
  requestHistory: "Import existing history",
  historyConfirm:
    "Import all existing project work items, subject to the 5,000-item limit. Existing local tasks remain.",
  priorityField: "Source priority field",
  noPriority: "Do not import priority",
  metadata: "Project types and states",
  metadataRequired: "Read project metadata before starting the import.",
  projectId: "Azure project UUID",
  manualProject: "Enter project UUID directly",
  pageLimit:
    "Project list limit reached. You can enter a project UUID directly.",
  type: "Map type “{name}”",
  defaultType: "Space default type",
  priorityValue: "Map priority “{name}”",
  mappingTitle: "Type, state and priority mappings",
  mappingHelp:
    "Explicitly choose a target for every source value. Names do not imply a mapping. Save the corrections, then resume sync.",
  mappingRequired:
    "Source mappings need attention. Open mapping settings before resuming sync.",
  reload: "Reload",
  repositories: "Repositories and hook status",
  repositorySelect: "Select repositories (20 total across all projects)",
  selectedCount: "{count} / 20 repositories selected",
  repositoryHelp:
    "Saving replaces the complete selection and automatically creates push, PR created and PR updated hooks for each repository. Switching projects keeps selections from other projects.",
  hooks: "Automatic hooks",
  cleanup:
    "This connection is disconnected. Status remains available to verify remote hook deletion.",
  manualCleanup: "Manual remote hook cleanup required",
  repair: "Recheck / recover",
  hookHTTPS: "Hooks require an HTTPS public server URL.",
  hookCleanup:
    "A previous hook still needs cleanup. Check its remote status first.",
  work: "Event processing and PR backlinks",
  noWork: "No events or backlinks yet",
  noHooks: "No hooks yet",
  unknown:
    "The remote result is unknown. Reconciliation comes first; no automatic duplicate post.",
  reconcile: "Reconcile",
  resend: "Confirm and send again",
  resendWarning:
    "The remote service may have received the previous comment. Sending again may duplicate it. Existing threads will be checked first.",
  resendConsent:
    "I understand that this may create a duplicate comment and want to send again",
  settings: "Repository links and backlinks",
  linkPR: "Link pull requests",
  linkCommits: "Link Git commits (complete pushes up to 100 commits)",
  privateBacklink: "Post PR backlinks in private projects",
  publicBacklink: "Post PR backlinks in public projects",
  backlinkHelp:
    "PR backlinks post as the delegating user and contain only a task ID and link. Commit attribution identifies the push initiator, not the commit author.",
  paused: "Disconnected",
  enableSync: "Continue syncing from Azure Boards after import",
};
