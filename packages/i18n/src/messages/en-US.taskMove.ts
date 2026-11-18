import type { Messages } from "../translate";

/** Stage 9c-3 · Move task to another space (English). Mounted under tasks.move by en-US.ts. */
export const taskMoveEn: Messages["tasks"]["move"] = {
  menu: "Move to space",
  previewList: "Tasks to move",
  separator: ", ",
  searchPlaceholder: "Search spaces...",
  searchLabel: "Search spaces by name or prefix",
  noMatch: "No matching spaces",
  noTargets: "No spaces to move into",
  currentSpace: "Current space",
  noPermission: "You can only view this space, so you can't move tasks",
  pickTitle: "Move to space",
  pickDescription: "Choose the space to move {id} into.",
  title: "Move to {name}",
  description:
    "{count} task(s) including subtasks. They get new IDs; old IDs keep working.",
  estimated: "est.",
  typeChange: "Type: {from} → {to}",
  statusChange: "Status: {from} → {to}",
  estimateChange: "Estimate: {from} → {to}",
  cleared: "Will clear: {fields}",
  clearedFields: {
    iteration: "Iteration",
    project: "Project",
    estimate: "Estimate",
    parent: "Parent task",
    triage: "Triage",
  },
  droppedLabels: "Will drop labels: {names}",
  detachedParent: "Will detach from parent task {id} {title}",
  removedAssignees: "Will remove Agent / crew assignees: {names}",
  activeRuns: "Will cancel {count} active run(s)",
  unlinkedIssues:
    "Will unlink issue sync: {issues} (remote issues are not affected)",
  staleRuns:
    "An Agent was assigned after the preview: {agents} Agent / crew assignee(s) and {runs} active run(s). Confirming cancels them too.",
  keepNote:
    "Comments, activity, attachments and Git links move with the task; conversations and runs stay in the original space.",
  showMore: "Show {count} more task(s)",
  showLess: "Show less",
  submit: "Move",
  submitCancelRuns: "Cancel runs and move",
  moving: "Moving...",
  moved: "Moved to {name}, new ID {id}",
  errors: {
    invalidTarget: "The target space is unavailable. Choose another space.",
    retired: "This space is retired; its tasks can't be moved.",
    tooMany: "Too many subtasks. At most 5000 tasks can be moved at once.",
    forbidden: "You don't have permission to move tasks in this space.",
    notFound: "The task doesn't exist or has been deleted.",
    versionConflict: "The task was changed. Refresh and try again.",
    idempotencyConflict:
      "The previous move has an unknown result. Close and reopen, then try again.",
    previewFailed: "Couldn't load the move preview.",
    failed: "Move failed. Try again later.",
  },
};
