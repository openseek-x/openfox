import type { Messages } from "../translate";

/** Stage 10e · Task trash "Recently deleted" (English). Mounted under tasks.trash by en-US.ts. */
export const taskTrashEn: Messages["tasks"]["trash"] = {
  title: "Recently deleted",
  allDescription: "Tasks deleted in this space. Restored tasks keep their IDs.",
  mineTitle: "Tasks you deleted",
  mineDescription:
    "Only tasks you deleted are listed here. Tasks deleted by others can only be seen and restored by whoever deleted them and space admins.",
  retired:
    "This space is retired. You can view these tasks but not restore them.",
  columns: {
    task: "Task",
    type: "Type",
    status: "Status",
    deletedBy: "Deleted by",
    deletedAt: "Deleted",
    actions: "Actions",
  },
  listLabel: "Deleted tasks",
  unknownUser: "Unknown user",
  restore: "Restore",
  restoreLabel: "Restore {id}",
  restoreTitle: "Restore {id}?",
  restoreDesc:
    "The task gets its original ID back. If its parent was deleted, it becomes a top-level task; subtasks detached when it was deleted are not re-attached.",
  restored: "Restored {id}",
  open: "Open",
  conflict:
    "This task was already restored or changed. The list has been refreshed.",
  empty: "No recently deleted tasks",
  emptyDescription:
    "Deleted tasks show up here, and you can restore them at any time.",
  loadMore: "Load more",
  loadFailed: "Failed to load",
  undo: "Undo",
  activityRestored: "{actor} restored this task",
  activityRestoredDetached:
    "{actor} restored this task (its parent was deleted, so it is now a top-level task)",
};
