export const governanceEn = {
  queueFull: {
    title: "The organization queue is full",
    desc: "The number of queued runs has reached the organization limit. Your message is saved and will start automatically when a queue slot is free.",
    usage: "{queued} queued; the limit is {max}.",
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
