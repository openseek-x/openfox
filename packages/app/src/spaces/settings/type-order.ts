import type { SpaceTaskType } from "@openfox/api";

/** Reorder visible rows while retaining archived rows in their original slots. */
export function moveTaskType(
  all: SpaceTaskType[],
  id: string,
  target: string,
): SpaceTaskType[] {
  const visible = all.filter((type) => !type.archived_at);
  const from = visible.findIndex((type) => type.id === id);
  const to = visible.findIndex((type) => type.id === target);
  if (from < 0 || to < 0 || from === to) return all;
  visible.splice(to, 0, visible.splice(from, 1)[0]!);
  let index = 0;
  return all.map((type) => (type.archived_at ? type : visible[index++]!));
}
