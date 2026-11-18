import type { SearchItem } from "@openfox/api";
import { taskPagePath } from "../tasks/navigation";

/** A message link names the message, rather than its current page or rank. */
export function searchConversationPath(
  orgId: string,
  item: SearchItem,
): string {
  const id = item.conversation_id ?? item.id;
  const base = `/o/${encodeURIComponent(orgId)}/chat`;
  const path =
    item.conversation_kind === "direct"
      ? `${base}/direct/${encodeURIComponent(id)}`
      : item.task_display_id
        ? taskPagePath(orgId, item.task_display_id, id) + "&rail=chat"
        : `${base}/${encodeURIComponent(id)}`;
  if (!item.message_id) return path;
  return (
    path +
    (path.includes("?") ? "&" : "?") +
    "message=" +
    encodeURIComponent(item.message_id)
  );
}
