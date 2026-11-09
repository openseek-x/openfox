import type { Messages } from "../translate";

/** Stage 10b · Pick the run conversation when assigning from the summon menu (English). Mounted at tasks.runChoice. */
export const runChoiceEn: Messages["tasks"]["runChoice"] = {
  title: "Choose a conversation to run in",
  description:
    "{agent} already has an assignment conversation on this task. Choose where to run this time.",
  reuse: "Run in the existing conversation ({title})",
  new: "Run in a new conversation",
  newHint:
    "The new conversation becomes this task's assignment conversation, and later runs happen there",
  start: "Start run",
  cancel: "Cancel",
  errors: {
    notAssigned:
      "This Agent is no longer an assignee of the task. Assign it again.",
    notRunnable:
      "The task's current status doesn't allow an Agent run (only to-do and in-progress tasks can run)",
  },
};
