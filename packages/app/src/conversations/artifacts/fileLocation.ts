import type { ArtifactReviewAnchor } from "@openfox/api";

export type PendingFileLocation = {
  id: string;
  anchor: ArtifactReviewAnchor;
};

const pending = new Map<string, PendingFileLocation>();
const key = (conversation: string, id: string, sha: string) =>
  `${conversation}:${id}:${sha}`;

export function requestFileLocation(
  conversation: string,
  id: string,
  sha: string,
  anchor: ArtifactReviewAnchor,
  open: () => void,
) {
  pending.set(key(conversation, id, sha), {
    id: crypto.randomUUID(),
    anchor: structuredClone(anchor),
  });
  if (pending.size > 50) pending.delete(pending.keys().next().value!);
  open();
  window.dispatchEvent(new Event("openfox:file-locate"));
}

/** 只读查看待定位请求，避免开发模式 React StrictMode 挂载重放提前吞掉请求。 */
export function peekFileLocation(
  conversation: string,
  id: string,
  sha: string,
): PendingFileLocation | undefined {
  return pending.get(key(conversation, id, sha));
}

/** 确认定位落地后消费请求；按 requestId 核对以免误删新发起的定位。 */
export function consumeFileLocation(
  conversation: string,
  id: string,
  sha: string,
  requestId?: string,
) {
  const k = key(conversation, id, sha);
  const current = pending.get(k);
  if (!current) return;
  if (requestId && current.id !== requestId) return;
  pending.delete(k);
}
