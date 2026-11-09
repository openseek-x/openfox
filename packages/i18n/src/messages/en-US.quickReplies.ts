import type { Messages } from "../translate";

/** Stage 7d · Quick replies (English). */
export const quickRepliesEn: Messages["quickReplies"] = {
  menuLabel: "Quick replies",
  search: "Search quick replies",
  personal: "My quick replies",
  organization: "Organization quick replies",
  empty: "No quick replies yet",
  manage: "Manage quick replies…",
  manageShort: "Manage",
  add: "Add quick reply",
  insert: "Insert “{title}” into the composer",
  loadFailed: "Could not load quick replies",
  manager: {
    title: "Manage quick replies",
    description:
      "Personal quick replies are only visible to you. Organization quick replies are maintained by owners and admins and available to everyone.",
    personalTab: "Mine",
    organizationTab: "Organization",
    add: "New quick reply",
    titleLabel: "Title",
    bodyLabel: "Content",
    titlePlaceholder: "For example: Got it",
    bodyPlaceholder: "Text inserted into the composer when picked",
    save: "Save",
    cancel: "Cancel",
    edit: "Edit {title}",
    remove: "Delete {title}",
    moveUp: "Move {title} up",
    moveDown: "Move {title} down",
    count: "{count} / {max}",
    readOnly:
      "Only organization owners and admins can maintain organization quick replies",
    noneYet: "Nothing here yet",
  },
  toast: {
    created: "Quick reply created",
    updated: "Saved",
    removed: "Deleted",
  },
  errors: {
    limit: "You reached the limit of {max} quick replies. Delete some first.",
    conflict:
      "This quick reply was changed elsewhere. It has been refreshed, please edit again.",
    forbidden: "You can't maintain organization quick replies",
    generic: "Something went wrong, please try again",
  },
};
