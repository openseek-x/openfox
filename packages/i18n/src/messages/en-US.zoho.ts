export const zohoEn = {
  activity: "{actor} imported this task; source reporter: {name} (US · {id})",
  activityMember: "Mapped member: {name}",
  activityExternal: "Mapped to an external source account",
  activityUnmapped: "No local member mapping",

  title: "Zoho BugTracker (US)",
  organizationTitle: "Organization portals",
  connect: "Connect Zoho portal",
  portal: "Zoho portal",
  choosePortal: "Choose a portal you administer",
  reauthorize: "Reauthorize",
  notConfigured:
    "US Zoho organization and identity OAuth applications are not configured. Contact an administrator.",
  lifecycle:
    "This integration uses the US legacy BugTracker REST API. The shared official docs carry a deprecation warning; a separate BugTracker retirement date and migration path are unconfirmed. API failure stops new reads and preserves imported copies.",
  scope:
    "One-time import of published fields: title, status, severity, source reporter, creation time and source ID. Descriptions, remote assignees, comments and attachments are not imported. No continuous sync or Zoho writes.",
  shared:
    "An organization administrator authorizes one portal they administer for read-only project imports by organization administrators.",
  shareConsent:
    "I authorize organization administrators to use read-only access to the selected portal.",
  completeTitle: "Confirm Zoho portal authorization",
  identityTitle: "Your Zoho identity",
  identityDescription:
    "A separate OAuth flow verifies your login_id, active membership and email in the same portal. Only identity facts are saved, never personal tokens. It suggests future source-reporter attribution, not task assignees.",
  confirmIdentity: "Confirm this is my identity",
  confirmPortal: "Confirm portal connection",
  manageIdentity: "Manage my identity",
  identityDisconnect:
    "Stops future automatic attribution suggestions. Manual mappings, imported tasks and history remain.",
  disconnect:
    "Local disconnect stops new reads and future identity attribution. Imported tasks remain; remote Zoho consent is not automatically revoked.",
  openConfirmation: "Open identity confirmation",
  needsAttention: "Verification required",
  failed: "Zoho authorization or read failed. Check permissions and retry.",
  expired: "This authorization session expired. Start again.",
  changed: "The connection or configuration changed. Refresh and reauthorize.",
  mismatch: "The portal, identity or email does not match.",
  permission:
    "Check portal administrator or active member status and required permissions.",
  rateLimited:
    "Zoho is rate limited or verification is busy. Wait for the retry period before trying again.",
  grantInUse:
    "This grant is already used by another connection. Create a separate authorization.",
  limit:
    "The source directory or project list exceeds verification limits. Narrow the scope and retry.",
  refresh: "Refresh status",
  noPortals: "No administrator portal is available to connect.",
  import: "Import bugs",
  moreProjects: "Load more projects",
  projectCount: "{count} active projects listed",
  projectLimit:
    "The project list reached its limit or repeated a cursor. Select a project by ID; the server will verify it again.",
  enterID: "Use a project ID",
  projectID: "Project ID (keep every digit)",
  mappingHelp:
    "Explicitly map every source type, status and severity. Only current organization priorities are offered; the priority scheme is never upgraded automatically.",
  type: "Map type “{name}”",
  defaultType: "Space default type",
  priorityValue: "Map severity “{name}”",
  usersHelp:
    "Only the reporter is available for source-reporter attribution. No remote assignee field is imported.",
  noSecrets: "Personal authorization credentials are never saved.",
} as const;
