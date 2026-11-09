import type { Messages } from "../translate";

/** Stage 2b · Execution steps and run views (English). Mounted under the steps key. */
export const stepsEn: Messages["steps"] = {
  expand: "Expand",
  collapse: "Collapse",
  duration: {
    seconds: "{seconds}s",
    minutes: "{minutes}m {seconds}s",
  },
  prep: {
    running: "Preparing environment",
    done: "Environment ready",
    failed: "Failed to prepare environment",
    interrupted: "Environment preparation interrupted",
    denied: "Environment preparation denied",
    progress: "Preparation progress",
    substeps: {
      workdir: "Working directory ready",
      repo: "Repository ready",
      runtime: "Runtime started",
      session: "Session ready",
    },
    substepsRunning: {
      workdir: "Preparing working directory",
      repo: "Preparing repository",
      runtime: "Starting runtime",
      session: "Creating session",
    },
  },
  thinking: {
    running: "Thinking",
    done: "Thought",
    empty: "No thinking content to show",
  },
  exec: {
    running: "Running command",
    done: "Ran command",
    settled: "Run command",
    failed: "Command failed",
    interrupted: "Command interrupted",
    denied: "Command denied",
    exitCode: "Exit code: {code}",
    copy: "Copy command",
    copied: "Command copied",
    noOutput: "The command produced no output.",
    deniedBeforeOutput: "The command was denied before producing output.",
    interruptedBeforeOutput:
      "The command was interrupted before producing output.",
    truncated: "Output truncated; only the end is kept.",
  },
  patch: {
    running: "Editing",
    done: "Edited",
    failed: "Edit failed",
    interrupted: "Edit interrupted",
    denied: "Edit denied",
    more: "and {count} more files",
    added: "Created",
    deleted: "Deleted",
    moved: "Moved to {path}",
    truncated: "The diff was too long and has been truncated.",
    noDiff: "No diff to show.",
  },
  write: {
    running: "Writing",
    done: "Wrote",
    failed: "Write failed",
    interrupted: "Write interrupted",
    denied: "Write denied",
    notApplied: "Not written to disk yet.",
    noDiff: "No diff is available for this write.",
    truncated: "The content was too long and has been truncated.",
    copyPath: "Copy path",
    copied: "Path copied",
  },
  read: {
    running: "Reading",
    done: "Read",
    lines: "lines {start}–{end}",
    from: "from line {start}",
  },
  search: {
    running: "Searching",
    done: "Searched",
    listRunning: "Listing files",
    listDone: "Listed files",
    results: "{count} results",
    scope: "Scope: {scope}",
    matches: "{count} matches",
    more: "…and {count} more files",
    empty: "No matching files.",
  },
  webSearch: {
    running: "Searching the web",
    done: "Searched the web",
    empty: "No results.",
  },
  webFetch: {
    running: "Reading web page",
    done: "Read web page",
  },
  todo: {
    running: "Updating plan",
    done: "Updated plan",
    progress: "{done}/{total} done",
    status: {
      pending: "Pending",
      in_progress: "In progress",
      completed: "Completed",
    },
  },
  group: {
    untitled: "Steps",
  },
  stateSuffix: {
    failed: "failed",
    interrupted: "interrupted",
    denied: "denied",
  },
  summary: {
    done: "Completed in {duration}",
    failed: "Failed after {duration}",
    cancelled: "Cancelled after {duration}",
    doneNoTime: "Completed",
    failedNoTime: "Failed",
    cancelledNoTime: "Cancelled",
  },
  status: {
    queued: "Queued",
    processing: "Working",
    thinking: "Thinking",
    canceling: "Cancelling",
    done: "Completed",
    failed: "Failed",
    cancelled: "Cancelled",
  },
  timeline: {
    label: "Execution steps",
    loadFailed: "Failed to load execution steps",
    truncated: "Too many events; only the first {count} are shown.",
    warning: "Notice",
  },
  error: {
    retry: "Retry",
    copy: "Copy error details",
    copied: "Error details copied",
    copyFailed: "Copy failed. Please select the text and copy it manually.",
    detail: "Error: {detail}",
    titles: {
      generic: "Run failed",
      CREDENTIAL_REJECTED: "Model credentials rejected",
      QUOTA_EXHAUSTED: "Model quota exhausted",
      RATE_LIMITED: "Too many requests",
      PROVIDER_OVERLOADED: "Model service request failed",
      MODEL_NOT_FOUND: "Model not found",
      CONTEXT_TOO_LARGE: "Context too long",
      IMAGE_TOO_LARGE: "Image too large",
      NETWORK_UNREACHABLE: "Can't reach the model service",
      WORKTREE_PREP_FAILED: "Failed to prepare working directory",
      REPO_UNAVAILABLE: "Repository unavailable",
      RUNTIME_UNAVAILABLE: "Runtime unavailable",
      QUEUE_TIMEOUT: "Queue timed out",
      RUN_TIMEOUT: "Run timed out",
      AUTHORIZATION_REVOKED: "Computer authorization revoked",
      UNKNOWN_PROCESS: "Run process lost",
      PREPARE_FAILED: "Failed to prepare environment",
      INTERNAL: "Internal error",
    },
    descriptions: {
      generic:
        "This request didn't complete. Retry, or check the error details below.",
      CREDENTIAL_REJECTED:
        "The model service rejected the credentials on this computer. Sign in to the runtime again on that computer and retry.",
      QUOTA_EXHAUSTED:
        "This account has used up its model quota. Top up or switch accounts, then retry.",
      RATE_LIMITED:
        "Too many requests in a short time, so the model service is throttling. Wait a moment and retry.",
      PROVIDER_OVERLOADED:
        "The model service is busy and couldn't complete this request. Retry, or check the error details below.",
      MODEL_NOT_FOUND:
        "The configured model doesn't exist or this account can't use it. Change the model in the agent settings.",
      CONTEXT_TOO_LARGE:
        "The conversation exceeds the model's context limit. Start a new session or shorten the request.",
      IMAGE_TOO_LARGE:
        "An attached image exceeds the model's size limit. Compress it and retry.",
      NETWORK_UNREACHABLE:
        "The computer can't reach the model service. Check its network or proxy settings.",
      WORKTREE_PREP_FAILED:
        "Couldn't prepare the working directory on the computer. Check disk space and directory permissions.",
      REPO_UNAVAILABLE:
        "Couldn't fetch the agent's repository. Check the repository URL and this computer's access.",
      RUNTIME_UNAVAILABLE:
        "The runtime on the computer isn't running or is offline. Check the computer and retry.",
      QUEUE_TIMEOUT:
        "The run waited longer than the agent's queue limit without a computer picking it up. Make sure a computer is online and retry.",
      RUN_TIMEOUT:
        "The run exceeded the agent's maximum duration and was stopped.",
      AUTHORIZATION_REVOKED:
        "This computer's authorization for the organization was revoked, so the run stopped.",
      UNKNOWN_PROCESS:
        "The computer restarted or the process exited unexpectedly, and the run state was lost. Retry.",
      PREPARE_FAILED:
        "The computer failed before starting the run. Retry, or check the error details below.",
      INTERNAL:
        "Something went wrong on our side. Retry, and contact an admin if it keeps failing.",
    },
  },
  attachment: {
    generated: "Generated file",
    open: "Open",
    download: "Download",
    unavailable: "The file has expired or is unavailable",
    types: {
      word: "Word document",
      excel: "Excel spreadsheet",
      powerpoint: "PowerPoint presentation",
      pdf: "PDF document",
      image: "Image",
      text: "Text file",
      archive: "Archive",
      other: "File",
    },
  },
  history: {
    columns: {
      state: "Status",
      conversation: "Conversation",
      startedAt: "Started",
      duration: "Duration",
      tokens: "Tokens",
    },
    open: "Open conversation: {title}",
    none: "—",
  },
};
