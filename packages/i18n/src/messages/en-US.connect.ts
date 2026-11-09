import type { Messages } from "../translate";

/** Stage 2c · Connect computer, storage cleanup, remote commands, prevent sleep (English). Mounted under the connect key. */
export const connectEn: Messages["connect"] = {
  title: "Connect a computer",
  intro:
    "Computers are where Agents run. Run the command below on the computer you want to connect; this dialog finishes automatically once it comes online.",
  name: "Computer name",
  namePlaceholder: "e.g. build-server-01",
  defaultName: "My computer",
  nameRequired: "Enter a computer name",
  scope: "Who can use this computer",
  scopePersonal: "Only me",
  scopePersonalDesc:
    "The computer is granted to you only; other members can't see or use it",
  scopeOrganization: "Whole organization",
  scopeOrganizationDesc:
    "Organization members can bind Agents to this computer",
  scopeOrganizationDisabled:
    "Only organization owners or admins can connect organization computers",
  guest: "Guests can't connect computers. Ask an organization admin.",
  generate: "Generate command",
  regenerate: "Regenerate",
  command: "Connect command",
  commandHint:
    "The command contains a one-time install token. Share it only with whoever connects the computer.",
  prerequisite:
    "The OpenFox CLI (openfox) must be installed on the target computer first.",
  steps: {
    paste: "Paste the command into a terminal on the target computer.",
    wait: "Wait for registration; on macOS the background service is installed and started.",
    keep: "Keep the computer online; it will be detected here automatically.",
  },
  expiresIn: "Expires in {time}",
  expiredTitle: "Command expired",
  expiredDesc: "The install token is no longer valid. Generate a new command.",
  copy: "Copy command",
  copied: "Connect command copied",
  copyFailed: "Copy failed. Select the command and copy it manually",
  waiting: "Waiting for the computer to come online…",
  connectedTitle: "Computer connected",
  connectedDesc: "“{name}” is online. This dialog will close shortly.",
  connectedInline: "“{name}” is online. You can continue.",
  issueFailed: "Couldn't generate the command. Try again later",
  forbidden: "You don't have permission to connect computers with this scope",
  local: "Connect this computer",
  localDesc:
    "The desktop app runs the register command here and installs the background service.",
  localDone: "This computer is registered. Waiting for it to come online",
  localErrors: {
    invalid_token: "The install token is malformed. Generate a new command",
    already_registered:
      "This computer is already connected to OpenFox. To reconnect, run the command in a terminal with --force.",
    token_rejected:
      "The center rejected the install token (expired or already used). Generate a new command",
    center_unavailable:
      "Couldn't reach the OpenFox center. Check the network and try again",
    busy: "Registration in progress, please wait",
    timeout: "Registration timed out. Check the network and try again",
    unavailable:
      "The openfox command wasn't found. Install the OpenFox CLI first",
    failed: "Registration failed. Run the command in a terminal instead",
  },
  auto: {
    done: "This computer was connected automatically",
    registering: "Connecting this computer to the current organization…",
    notRegistered: "This computer isn't connected to the current organization.",
    connect: "Connect this computer",
    retry: "Retry",
    otherOrg:
      "This computer is connected to another organization ({name}). A computer can join only one organization.",
    otherOrgUnknown: "another organization",
    stale:
      "This computer was removed from the organization, but its old connection settings are still here.",
    centerChanged:
      "This computer is connected to an old server address ({url}). Reconnect it to the current server.",
    reconnect: "Reconnect",
    moved: "Moved {count} Agents to this computer",
    moveFailed:
      "The Agents on the old computer weren't moved. You can move them from the old computer's detail page",
    cliMissing:
      "The OpenFox CLI bundled with the desktop app wasn't found, so it can't connect automatically. When running from source, run pnpm --filter @openfox/desktop build:cli first.",
    errors: {
      invalid_token: "The install token is malformed. Try again",
      already_registered: "This computer is already connected to OpenFox",
      token_rejected: "The center rejected the install token. Try again",
      center_unavailable:
        "Couldn't reach the OpenFox center. Check the network and try again",
      busy: "Connecting, please wait",
      timeout: "Connecting timed out. Check the network and try again",
      unavailable:
        "The openfox command wasn't found, so this computer can't be connected automatically",
      failed: "Couldn't connect this computer automatically. Try again",
      issue_failed: "Couldn't create an install token. Try again later",
      forbidden:
        "You don't have permission to connect computers in this organization",
    },
  },
  devicePolicy: {
    session_idle_minutes: "Persistent session idle timeout (minutes)",
    session_max_minutes: "Persistent session maximum lifetime (minutes)",
    cli: "Codex CLI path",
    args: "Additional CLI arguments",
    protection: "Resource protection",
    saved: "Device settings saved",
    verify: "Verify and save",
    findCli:
      "Run this command on the computer and paste the installed CLI path below.",
    restore: "Restore default",
    argsHint:
      "Enter one allowed -c option and its value per line. Agent arguments follow the runtime arguments.",
    memory: "Available memory protection",
    minimum: "Minimum available memory (MiB)",
    load: "System load protection",
    policy: "Protection policy",
    conservative: "Conservative",
    balanced: "Balanced (recommended)",
    performance: "Performance",
    guardHint:
      "New tasks wait when a threshold is reached. The computer can still run one task.",
  },
  storage: {
    autoPolicy: "Automatic cleanup",
    autoEnable: "Enable automatic cleanup",
    interval_hours: "Cleanup interval (hours)",
    repo_days: "Repository cache retention (days)",
    retain_days: "Completed run retention (days)",
    min_free_gib: "Minimum free space (GiB, 0 to disable)",
    max_gib: "Managed workspace limit (GiB, 0 to disable)",
    autoHint:
      "Only clean workspaces of finished runs in this organization are eligible. Under storage pressure, workspaces unused for at least a day can be removed.",

    manage: "Manage storage",
    byTime: "Clean up by time",
    byStatus: "Clean up by task status",
    retentionCleanup: "Clean up expired data",
    allTime: "All time",
    customDays: "Custom days",
    olderThan: "Older than {days} days",
    preview: "Preview",
    previewResult: "{count} eligible workspaces, approximately {size}.",
    usageLabel: "Usage breakdown",
    category: {
      cache: "Cache",
      task: "Tasks",
      conversation: "Conversations",
      automation: "Automations",
      workflow: "Workflows",
      other: "Other",
    },
    total: "Total storage",
    totalDesc: "Used by OpenFox on this computer · measured {time}",
    worktrees: "Task workspaces",
    worktreesDesc: "{count} git worktrees",
    codexHome: "Runtime temp directories",
    codexHomeDesc: "CODEX_HOME",
    journal: "Run journal",
    journalDesc: "Local Run logs",
    partial:
      "Too many files; measurement stopped early, so actual usage may be higher.",
    empty:
      "This computer hasn't reported storage yet (measured every 10 minutes).",
    cleanup: "Clean up",
    ownerOnly: "Only the computer owner can clean up",
    footnote:
      "Cleanup runs in the background on the computer and doesn't affect running tasks.",
    confirmTitle: "Clean up storage on this computer?",
    confirmDesc:
      "Only the following is removed; running tasks are always skipped:",
    scopeWorktrees:
      "Task workspaces of finished Runs past retention (git worktrees; branches and commits stay in the repository)",
    scopeCodexHome: "Expired Runtime temp directories (CODEX_HOME)",
    scopeJournal: "Run journal entries past retention",
    keepDirty:
      "Workspaces with uncommitted changes are kept; a workspace is only cleaned 24 hours after it was last used.",
    confirmOffline:
      "The computer is offline: if it doesn't come online within 2 minutes, cleanup is canceled.",
    confirm: "Start cleanup",
    done: "Cleanup finished, freed {size}",
    result:
      "Last cleanup freed {size}: {worktrees} workspaces, {homes} temp directories, {rows} journal entries; skipped {skipped} in use",
    resultDirty: "; kept {dirty} workspaces with uncommitted changes",
  },
  probe: {
    ownerOnly: "Only the computer owner can re-detect",
    done: "Runtimes re-detected",
    checkedAt: "Checked {time}",
  },
  test: {
    button: "Test connection",
    ownerOnly: "Only the computer owner can test the connection",
    offline: "The computer is offline",
    unsupported: "Only Codex can be tested for now",
    ok: "Connected · {ms} ms",
    modelMissing: "Model {model} isn't in the available list",
    failed: "Connection failed",
    loginRequired: "Runtime isn't signed in",
  },
  remote: {
    failed: "The computer reported a failure ({code})",
    expired: "The computer didn't respond; it may be offline",
    requestFailed: "Couldn't send the request. Try again later",
    notFound: "The computer doesn't exist or you're not its owner",
  },
  env: {
    reserved:
      "{name} is reserved and ignored on the computer (CODEX_HOME, HOME, PATH and variables starting with OPENFOX_, CODEX_, DYLD_ or LD_ have no effect)",
  },
  waitReason: {
    AGENT_CHAT_CONCURRENCY: "Chat concurrency limit reached",
  },
  sleep: {
    label: "Prevent sleep",
    desc: "Keep the system awake while tasks run on this computer",
    active: "Preventing sleep",
    failed: "Couldn't save. Try again",
  },
};
