import type { Messages } from "../translate";

/** Stage 10d · Conversation artifacts (English). Mounted under the artifacts key. */
export const artifactsEn: Messages["artifacts"] = {
  canvas: {
    label: "Document canvas",
    open: "Edit in chat workspace",
    openLibrary: "Open in library",
    search: "Search documents in this space",
    save: "Save document",
    saved: "Saved",
    saving: "Saving…",
    unsaved: "Unsaved",
    saveFailed: "Save failed; draft retained",
    selectionChanged:
      "The selection changed or cannot be mapped precisely. Select it again.",
    finishEditing:
      "Open the document and save your local changes before reviewing this proposal.",
  },
  resolution: {
    resolve: "Locate in current version",
    rebind: "Select a new page element",
    rebindHint:
      "Select and confirm a new element. The original comment is retained.",
    confirmRebind: "Confirm binding",
    cancelRebind: "Cancel rebinding",
    exact: "Exact location found",
    approximate: "Content changed; select again",
    ambiguous: "Multiple matches; select again",
    orphaned: "Location lost; select again",
    failed: "Location failed. Retry; the original comment is unchanged.",
    open: "Show resolved location",
    unavailable:
      "The current version has no exact location. Select again before requesting edits.",
  },
  proposals: {
    empty: "No change proposals yet",
    title: "Change proposals",
    review: "Review changes",
    view: "Review view",
    PENDING: "Awaiting review",
    ACCEPTED: "Accepted",
    REJECTED: "Rejected",
    base: "Original",
    candidate: "Candidate",
    diff: "Diff",
    accept: "Accept changes",
    reject: "Reject",
    retryDecision: "Retry decision",
    download: "Download this file",
    pendingHint: "The original is unchanged. Review before accepting.",
    acceptedHint: "Changes were applied as a new version.",
    rejectedHint: "Candidate rejected. The original was not changed.",
    loadFailed: "Could not load proposals",
    decisionFailed: "Could not confirm the result. Retry the decision.",
    conflict:
      "The original or proposal has changed. Review the latest version and generate a new candidate.",
    decidedElsewhere:
      "Another operation decided this proposal. The status is up to date.",
    diffHint: "− Removed · + Added. Shows the changed block with context.",
    diffLimited:
      "Diff display is truncated. Open the original and candidate or download the full files to review.",
    identical: "File contents are identical",
    restore: "Propose restoring this version",
    restoreFailed: "Could not create a restore candidate. Retry.",
  },
  comments: {
    saved: "Saved comments",
    save: "Save as comment",
    saving: "Saving…",
    hint: "Comments are saved separately. Send to Agent adds a snapshot to your draft; sending the chat starts the run.",
    empty: "No saved comments yet",
    more: "Load more",
    open: "Open",
    resolved: "Resolved",
    resolve: "Resolve",
    reopen: "Reopen",
    edit: "Edit",
    editBody: "Edit comment",
    replyBody: "Reply to comment",
    saveEdit: "Save changes",
    reply: "Reply",
    you: "You",
    member: "Another member",
    agent: "Send to Agent",
    locate: "View source",
    retryUpdate: "Retry last update",
    conflict:
      "The comment changed or this request was saved. Refresh to check; your input is retained.",
    saveFailed:
      "Could not confirm the comment was saved. Retry uses the same comment ID.",
    updateFailed: "Could not confirm the update. Retry the last update.",
    loadFailed: "Could not load comments.",
    queueFailed: "Could not load the comment reference. Refresh and retry.",
  },
  version: {
    current: "Current",
    file: "File",
    unavailable: "This revision is unavailable or inaccessible",
    following: "Current · v{version}",
    fixed: "Fixed revision · v{version}",
    openFixed: "Open as a fixed revision",
    openCurrent: "Open current version",
  },
  persistence: {
    cancelPreparation: "Cancel preparation, keep draft",
    eraseFailed:
      "Signed out, but local drafts could not be removed. Clear this site’s data in browser settings.",
    unfinished: "Recovered web annotation",
    capturedSource:
      "The original page reference is retained. Select the element again to locate it on the current page.",
    discard: "Discard this unfinished annotation",
    loading: "Restoring workspace…",
    loadFailed: "Local drafts could not be loaded. Please retry.",
    saving: "Saving locally…",
    saved: "Saved locally",
    failed:
      "Draft not saved locally. Keep this window open and retry. Storage may be full.",
    conflict:
      "Another window changed this draft. Copy your current input before reloading.",
    pending:
      "The previous send is unconfirmed. Check its result before sending again.",
    check: "Check send result",
    retrySend: "Retry previous send",
    missing:
      "The previous message has not arrived. Retry its original request; current input will be kept.",
    uncertain: "Unable to confirm the send result. Please retry later.",
  },
  button: "Artifact: {name}",
  buttonsLabel: "Artifacts from this run",
  menu: "Artifacts",
  workspace: {
    newTab: "New tab",
    addTab: "Open new tab",
    choose: "Choose what to open",
    openBrowser: "Open webpage",
    openArtifacts: "Browse artifacts ({count})",
  },
  browser: {
    navigation: "Web navigation",
    selectElement: "Select a page element to annotate",
    stopSelecting: "Stop selecting elements",
    more: "More browser actions",
    noDownloads: "No downloads yet",

    annotations: "Web annotations",
    annotate: "Annotate",
    annotationMode: "Selection mode",
    element: "Element",
    text: "Text",
    region: "Region",
    annotationEditor: "Add web annotation",
    annotationPlaceholder: "Add a comment…",
    annotationBody: "Write your feedback…",
    annotationPrompt: "Please address these web annotations.",
    annotationLimit:
      "Annotation or attachment limit reached. Shorten or remove an item and retry.",
    addToChat: "Add to chat",
    addImageToChat: "Add to chat with region image",
    locate: "Locate on page",
    staleAnnotation:
      "The page changed or the tab closed. The original reference is preserved.",
    selectionFailed:
      "Select visible text, an ordinary element, or a region without passwords or verification codes.",
    allowControl: "Allow browser for this turn",
    takeOver: "Take over",
    agentControl: "Agent is sharing the browser",
    waitingRun: "Authorized for this message",
    nextMessage: "Browser enabled for the next message",
    userControl: "You are in control",
    connecting: "Connecting…",
    replaceBinding: "Use this window",
    bindingConflict:
      "This conversation's browser is connected to another window. You can explicitly switch to this window.",
    originDenied:
      "This site is not authorized. Open it in a new tab, then authorize the browser again.",
    controlFailed:
      "Browser authorization failed. Check the local service, device ownership, and connection, then retry.",
    label: "Browser",
    tabs: "Web tabs",
    newTab: "New tab",
    closeTab: "Close web tab",
    back: "Back",
    forward: "Forward",
    stop: "Stop loading",
    reload: "Reload page",
    address: "Enter a URL",
    go: "Go",
    empty: "Enter a URL to start browsing",
    privateSession: "Website sign-ins are isolated to this conversation",
    failed: "Browser unavailable. Try again.",
    reset: "Sign-in or network settings changed. Reopen the browser.",
    limit:
      "Limit: 8 tabs per conversation and 24 on this computer. Close some tabs first.",
    networkDenied: "This address is blocked by the network access policy",
    protectedEndpoint:
      "This address belongs to a protected local service and cannot be accessed.",
    publicDnsFailed:
      "The proxy returned a synthetic address and the real website address could not be resolved. Check your proxy connection and try again.",
    dnsFailed:
      "The website address could not be resolved. Check the URL and connection and try again.",
    loadFailed:
      "The page could not load. Check the URL or connection and try again.",
    allowLocal: "Allow this local address",
    clear: "Clear website data",
    clearConfirm:
      "Close all web tabs in this conversation and clear website sign-ins?",
    downloads: "Downloads",
    downloadComplete: "Completed",
    downloading: "Downloading",
    downloadFailed: "Download incomplete (20 MiB file limit)",
    reveal: "Show in folder",
  },
  panel: {
    expand: "Expand workspace",
    restore: "Restore sidebar width",

    label: "Conversation artifacts",
    tabs: "Artifact tabs",
    list: "{count} artifacts",
    close: "Close artifacts panel",
    resize: "Resize artifacts panel",
    closeTab: "Close {name}",
  },
  list: {
    empty: "No artifacts in this conversation yet",
    loadFailed: "Failed to load artifacts",
    open: "Open {name}",
    more: "Showing the latest {count} artifacts",
  },
  review: {
    pdfNoText: "No selectable text on this page. Use region selection.",
    pdfTextMode: "Select text",
    pdfRegionMode: "Select region",
    pdfExistingNote: "Original annotation (read only)",
    closeDraft: "Close and keep annotation draft",
    fitWidth: "Fit width",
    officeSelectionUnmapped:
      "This selection cannot be mapped precisely. Select a smaller text range or annotate a region.",
    officeSelectText: "Select text",
    sheetCells: "Select cells",
    sheetTabs: "Worksheets",
    officeSelectObject: "Select object",
    officeSelectionHint:
      "Choose text by its source location for edits. Page selections provide region feedback without directly modifying the file.",
    annotate: "Annotate",
    officePreviewFailed:
      "Office layout preview could not load. Retry or download the original file.",
    cancel: "Cancel comment",
    selectionUnmapped:
      "This selection cannot be mapped precisely. Select it in source view (up to 8 KiB).",
    chatHint:
      "Add feedback on a selection. Comments stay in the chat draft until you send a message.",
    draftLimit:
      "Use up to 20 comments across 10 files. Remove old comments before selecting changed content.",
    addToChat: "Add to chat",
    pending: "Pending comments ({count})",
    removeComment: "Remove comment",
    openChat: "Open chat to send",
    defaultPrompt: "Please update the files using these comments.",
    messageLimit:
      "The message and comments are too long. Shorten them before sending.",
    chatComments: "File comments",
    contextChip: "{name} · {count} comments",
    removeFile: "Remove comments for {name}",

    officeHint:
      "Preview uses the original print layout. Enable annotations and use “Select text” for source edits, or mark regions for feedback. Region feedback does not directly edit the file. Missing fonts may affect layout.",
    officeFormulaHint:
      "Cells show raw constants and formulas without calculation or number formatting. Edits clear formula caches; recalculate in Excel. Numeric cells remain numeric and text is never converted to a formula.",
    officeSection: "Select sheet or slide",
    officeContent: "Office document content",
    officeReadOnly: "Read-only",
    officeMarked: "Open comment",
    officeEmpty: "No body content to display. Download the file to inspect it.",
    officeBlank: "Empty text fragment or cell",
    officeTypes: {
      text: "Text",
      number: "Number",
      boolean: "Boolean (0/1)",
      formula: "Formula",
      unsupported: "Complex content",
    },
    visualOnly:
      "Image and PDF comments provide region feedback only; these files are not directly edited.",
    regionHint:
      "Drag to select a region, then comment. Use Select whole page with the keyboard. Zoom and rotation preserve annotation positions.",
    regionLabel: "Region on page {page}",
    wholePage: "Select whole page",
    previousPage: "Previous page",
    nextPage: "Next page",
    rotate: "Rotate 90°",
    zoom: "Zoom",
    pdfFailed:
      "PDF rendering failed. It may be encrypted, damaged, over 1,000 pages, or timed out. Download to view it.",
    nativeEnter: "Annotate and revise with AI",
    nativeClose: "Return to document editing",
    nativeHint:
      "Save first, then select your private Codex chat. Add comments to its draft and send a message to request changes. If the document changes in the meantime, select the content again.",
    nativeConversation: "Select a private Codex chat",
    nativeNone:
      "Create a private Codex chat in this space to start annotating.",
    nativeStart: "Save and open annotations",
    nativeFailed:
      "Cannot open or refresh review. Check that saving succeeded, the document is unchanged, and the chat has no other members.",
    locate: "Locate in source",
    enter: "Annotate",
    exit: "Back to preview",
    loading: "Loading annotations…",
    retry: "Failed to load. Retry",
    conflict: "The file has changed. Refresh it and select the content again.",
    unavailable: "The file has expired or is unavailable.",
    unsupported:
      "File comments currently require a single-agent Codex conversation.",
    cellHint:
      "Select a cell to comment. Coordinates refer to the original CSV.",
    cell: "Row {row}, column {column}",
    previous: "Previous",
    next: "Next",
    csvInvalid:
      "Invalid CSV or more than 100,000 cells. Annotation is unavailable.",
    source: "Annotate source",
    htmlSource: "View source",
    htmlTitle: "Static HTML preview: {name}",
    htmlStatic:
      "Static HTML preview. Page scripts and external resources are disabled.",
    htmlHint:
      "Click an element, or focus with Tab and press Enter, to comment. Changes apply only to that element's source; annotate other inline styles separately in source view.",
    htmlNoMapping:
      "No elements can be mapped reliably. Switch to source annotation.",
    htmlUnavailable:
      "HTML exceeds preview limits (512 KiB, 10,000 nodes or 128 levels), or cannot be parsed. Switch to source view.",
    rendered: "Rendered view",
    textHint:
      "Select source text and add an instruction. Maximum selection: 8 KiB.",
    sourceText: "Source text for annotation selection",
    comment: "Instruction for this selection",
    emptyCell: "Empty cell",
    placeholder:
      "For example: shorten this paragraph while keeping the conclusion.",
    selectionLimit: "Select complete text, up to 8 KiB.",
  },
  preview: {
    toolbar: "File preview toolbar",
    more: "More preview actions",
    info: "File information",
    mode: "View mode",
    rendered: "Preview",
    source: "Source",
    htmlTitle: "HTML preview: {name}",
    htmlHint:
      "Static preview · Scripts, external resources and navigation are disabled",
    expired: "Artifact expired",
    expiredHint:
      "This artifact passed its retention period or was deleted and can no longer be previewed or downloaded.",
    missing: "Artifact not found",
    tooLarge: "This file is too large to preview here. Download it to view.",
    download: "Download",
    meta: "{type} · {size} · {time}",
  },
};
