/** Stage 12e · organization operations: audit log, resource quotas, shared-computer trust notice, reconciliation actions (English). */
export const governanceEn = {
  forbidden: {
    title: "No permission",
    desc: "Only organization owners and admins can view this page.",
  },
  audit: {
    nav: "Audit log",
    title: "Audit log",
    subtitle:
      "Administrative and configuration actions in this organization, newest first. Run activity is not listed here; see the run history of the conversation or task.",
    filters: "Filters",
    actor: "Actor",
    actorAll: "All actors",
    action: "Action",
    actionPlaceholder: "e.g. device.registered or device.*",
    actionHelp: "Exact match; end with * to match by prefix.",
    resourceKind: "Resource type",
    resourceKindPlaceholder: "e.g. device",
    from: "From (inclusive)",
    to: "To (exclusive)",
    apply: "Search",
    reset: "Reset",
    invalidRange: "The start time must be earlier than the end time.",
    rangeTooLong: "The time range cannot exceed 366 days.",
    invalidAction:
      "Actions may only contain letters, digits, dots, underscores and hyphens, optionally ending with *.",
    invalidResourceKind:
      "Resource types may only contain letters, digits, dots, underscores and hyphens.",
    columns: {
      time: "Time",
      actor: "Actor",
      action: "Action",
      resource: "Resource",
      detail: "Details",
    },
    decisionDenied: "Denied",
    loading: "Loading audit log",
    empty: "No audit records yet",
    emptyFiltered: "No audit records match these filters",
    emptyFilteredHint: "Adjust the filters and try again.",
    loadFailed: "Failed to load the audit log",
    loadMore: "Load more",
    loadingMore: "Loading…",
    loadMoreFailed: "Failed to load more. Try again.",
    end: "All records are shown",
  },
  quota: {
    nav: "Resource quotas",
    title: "Resource quotas",
    subtitle:
      "Limit how many Agent runs can be queued and running at the same time in this organization.",
    loading: "Loading resource quotas",
    loadFailed: "Failed to load resource quotas",
    usageTitle: "Current usage",
    queued: "Queued",
    active: "Running",
    usageOf: "{current} / {max}",
    usageUnavailable: "Usage is temporarily unavailable",
    queueFull:
      "The queue is full. New messages wait until a queue slot is free.",
    limitsTitle: "Limits",
    queueMax: "Queue limit",
    queueMaxHelp: "Maximum number of queued runs, {min}–{max}.",
    activeMax: "Active run limit",
    activeMaxHelp: "Maximum number of concurrent runs, {min}–{max}.",
    effective: "Active run limit currently in effect: {value}",
    capped:
      "The platform provides {capacity} run slots for this organization. Anything above that has no effect; at most {capacity} runs execute at the same time.",
    shrinkNote:
      "Lowering a limit does not affect runs that are already queued or running. It only applies to runs that enter afterwards.",
    outOfRange: "Enter a whole number between {min} and {max}.",
    aboveCapacity:
      "The platform provides {capacity} run slots for this organization. The active run limit cannot exceed that.",
    save: "Save",
    saving: "Saving…",
    saved: "Resource quotas updated",
    conflict:
      "Another admin just changed the quotas. The latest values are shown; review them and save again.",
    saveFailed: "Failed to save. Try again later.",
  },
  queueFull: {
    title: "The organization queue is full",
    desc: "The number of queued runs has reached the organization limit. Your message is saved and will start automatically when a queue slot is free.",
    usage: "{queued} queued; the limit is {max}.",
    contactAdmin:
      "To raise the limit, ask an organization owner or admin to adjust the resource quotas.",
    adjust: "Adjust resource quotas",
  },
  trust: {
    titleWiden: "Share this computer more widely?",
    titleAddOrganization: "Add this computer to another organization?",
    leadWiden:
      "After this change, more members can have Agents run on this computer.",
    leadAddOrganization:
      "After adding it, members of that organization can also have Agents run on this computer.",
    notSandbox: "Working-directory isolation is not a security sandbox.",
    runsAsUser:
      "Runs triggered by the people you share with execute as the local user on this computer and can read any file that user can read.",
    advice:
      "Share the computer only with members you trust, and avoid keeping content on it that they should not read.",
    acknowledge: "I understand",
    confirm: "Share",
    confirmAddOrganization: "Add",
  },
  reconcile: {
    evidenceTitle: "Verification evidence",
    evidenceEmpty: "No evidence has been received yet.",
    evidenceLoadFailed: "Failed to load evidence.",
    columns: {
      source: "Source",
      kind: "Type",
      time: "Time",
      detail: "Details",
    },
    source: {
      DAEMON: "Computer",
      CENTER: "Server",
      OPERATOR: "Admin",
      unknown: "Other",
    },
    kind: {
      PROCESS_STARTED: "Process started",
      PROCESS_RUNNING: "Process still running",
      PROCESS_EXITED: "Process exited",
      PROCESS_NOT_FOUND: "Process not found",
      RESULT_CONFIRMED: "Result confirmed",
      HEARTBEAT: "Heartbeat",
      REVOCATION: "Authorization revoked",
      unknown: "Other",
    },
    notAccepted: "Not accepted",
    noDetail: "None",
    acceptResult: "Accept existing result",
    acceptConfirm:
      "The computer confirmed that this run produced a result. Accepting ends verification and lets the computer submit that result to finish the run. Accept it?",
    acceptConfirmAction: "Accept result",
    alreadyResolved:
      "Already handled: this verification was resolved by someone else or by the system. The view has been refreshed.",
    acceptFailed: "The action failed. Refresh and try again.",
  },
  computer: {
    reconciling: "Verifying",
    reconcilingHint: "{count} runs on this computer are being verified",
    pending: {
      title: "Runs awaiting verification",
      desc: "These runs are paused because the computer went offline or the process could not be confirmed. Their conversations cannot continue until they are handled. They are usually resolved automatically when the computer comes back; if it stays away, you can mark a run as failed here.",
      columns: {
        computer: "Computer",
        agent: "Agent",
        actor: "Started by",
        since: "Awaiting since",
        reason: "Reason",
        action: "Action",
      },
      markFailed: "Mark as failed",
      noCase: "Cannot be handled yet",
      confirmTitle: "Mark this run as failed?",
      confirmDesc:
        "The run of {agent} started by {actor} ends immediately and is recorded as failed, and its conversation accepts input again. Nothing is retried automatically; the person who started it can send the message again. If the task process is in fact still running on the computer, the result it submits later will not be accepted.",
      confirmAction: "Mark as failed",
      marked: "Marked as failed",
      alreadyResolved:
        "Already handled: this run was resolved by someone else or by the system. The list has been refreshed.",
      failed: "The action failed. Refresh and try again.",
    },
  },
};
