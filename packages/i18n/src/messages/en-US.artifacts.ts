import type { Messages } from "../translate";

/** Stage 10d · Conversation artifacts (English). Mounted under the artifacts key. */
export const artifactsEn: Messages["artifacts"] = {
  button: "Artifact: {name}",
  buttonsLabel: "Artifacts from this run",
  menu: "Artifacts",
  panel: {
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
  preview: {
    expired: "Artifact expired",
    expiredHint:
      "This artifact passed its retention period or was deleted and can no longer be previewed or downloaded.",
    missing: "Artifact not found",
    tooLarge: "This file is too large to preview here. Download it to view.",
    download: "Download",
    meta: "{type} · {size} · {time}",
  },
};
