import { useSyncExternalStore } from "react";
import { workspaceStore } from "../workspaceStorage";
import type { AnchorResolution, WorkspaceCommentThread } from "@openfox/api";
export const resolutionKey = (conversation: string, thread: string) =>
  `anchor-resolution:${conversation}:${thread}`;
export const rebindKey = (conversation: string) =>
  `anchor-rebind:${conversation}`;
export type Rebind = Pick<WorkspaceCommentThread, "id" | "version" | "source">;
export function useStoredValue<T>(key: string) {
  const store = workspaceStore();
  return useSyncExternalStore(
    (listener) => {
      store.listeners.add(listener);
      return () => {
        store.listeners.delete(listener);
      };
    },
    () => store.read<T>(key),
  );
}
export function useResolution(
  conversation: string,
  thread: WorkspaceCommentThread,
) {
  const result = useStoredValue<AnchorResolution>(
    resolutionKey(conversation, thread.id),
  );
  return result?.thread_version === thread.version ? result : undefined;
}
export function beginRebind(
  conversation: string,
  thread: WorkspaceCommentThread,
  open: () => void,
) {
  workspaceStore().write(rebindKey(conversation), {
    id: thread.id,
    version: thread.version,
    source: thread.source,
  });
  freshRebinds.add(conversation);
  open();
  window.dispatchEvent(new Event("openfox:browser-rebind"));
}

const freshRebinds = new Set<string>();
export function takeFreshRebind(conversation: string) {
  const yes = freshRebinds.has(conversation);
  freshRebinds.delete(conversation);
  return yes;
}
