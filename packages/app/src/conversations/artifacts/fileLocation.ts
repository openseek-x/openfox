import type { ArtifactReviewAnchor } from "@openfox/api";
const pending = new Map<string, ArtifactReviewAnchor>();
const key = (conversation: string, id: string, sha: string) =>
  `${conversation}:${id}:${sha}`;
export function requestFileLocation(
  conversation: string,
  id: string,
  sha: string,
  anchor: ArtifactReviewAnchor,
  open: () => void,
) {
  pending.set(key(conversation, id, sha), structuredClone(anchor));
  if (pending.size > 50) pending.delete(pending.keys().next().value!);
  open();
  window.dispatchEvent(new Event("openfox:file-locate"));
}
export function takeFileLocation(
  conversation: string,
  id: string,
  sha: string,
) {
  const k = key(conversation, id, sha),
    value = pending.get(k);
  pending.delete(k);
  return value;
}
