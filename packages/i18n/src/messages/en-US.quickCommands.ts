import type { Messages } from "../translate";

/** Stage 9f · Task quick commands (English). Mounted at tasks.quickCommands. */
export const quickCommandsEn: Messages["tasks"]["quickCommands"] = {
  menuGroup: "Quick commands",
  menuEmpty: "No quick commands available",
  menuManage: "Manage quick commands",
  defaultPrompt: "Please handle this task.",
  sentTo: "Sent to {name}",
  sendFailed:
    "The command was not sent: {reason}. The conversation is open so you can resend it there.",
  // Stage 10b · insert into the input box (fills the editor, does not send)
  insert: "Insert into input box",
  insertLabel: "Insert “{name}” into the input box",
  unavailable: {
    ARCHIVED: "Target is archived",
    OUTSIDE_SPACE: "Target is no longer in this space",
    NOT_VISIBLE: "Target is not available",
    INACTIVE: "Agent is not available",
    NO_MEMBERS: "Squad has no available members",
  },
  manage: {
    title: "Manage quick commands",
    description:
      "Customize quick commands for task details: name, prompt, Agent/squad and visibility.",
    empty: "This space has no quick commands yet. Click “New” to create one.",
    loadFailed: "Couldn't load quick commands",
    add: "New",
    done: "Done",
    privateGroup: "Private",
    sharedGroup: "Shared",
    dragHandle: "Drag to reorder {name}",
    edit: "Edit {name}",
    remove: "Delete {name}",
    removeTitle: "Delete quick command “{name}”?",
    removeDescription: "Once deleted, nobody can use this command anymore.",
    removeConfirm: "Delete",
    count: "{count} / {max}",
    dndPicked: "Picked up {name}",
    dndDropped: "Dropped {name}",
    dndCancelled: "Cancelled moving {name}",
    dndInstructions:
      "Press space to pick up, use the up and down arrow keys to move, press space again to drop, or Esc to cancel.",
  },
  form: {
    newTitle: "New quick command",
    editTitle: "Edit quick command",
    description:
      "When triggered, the prompt below is sent to the Agent or squad together with the current task's context.",
    back: "Back",
    name: "Name",
    namePlaceholder: "e.g. Clarify requirements",
    target: "Agent / squad",
    targetPlaceholder: "Choose an Agent or squad",
    targetSearch: "Search Agents or squads",
    targetEmpty: "No Agent or squad to choose",
    targetHint:
      "You can choose shared Agents/squads in this space, or private Agents/squads you created in this space.",
    agentsGroup: "Agents",
    crewsGroup: "Squads",
    prompt: "Prompt (optional)",
    promptPlaceholder:
      "Describe what the AI should do, e.g. clarify anything vague in this task and list open questions. Leave empty to send only the task context and let the Agent act on its own setup.",
    visibility: "Visibility",
    private: "Private",
    privateHint: "Only visible to you",
    shared: "Shared",
    sharedHint: "Visible to everyone in this space",
    cancel: "Cancel",
    save: "Save",
  },
  toast: {
    created: "Quick command created",
    updated: "Quick command saved",
    removed: "Quick command deleted",
  },
  errors: {
    limit: "A group can have at most {max} quick commands",
    target:
      "The selected Agent or squad is no longer available. Choose another one.",
    conflict:
      "This quick command was changed elsewhere. It has been refreshed; please try again.",
    forbidden: "You don't have permission to manage this quick command",
    generic: "Something went wrong. Please try again later.",
  },
};
