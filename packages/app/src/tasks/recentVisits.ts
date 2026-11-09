import { readLocal, writeLocal } from "../storage";

const key = (orgId: string, userId: string) =>
  `openfox:recent-tasks:${orgId}:${userId}`;

// 只存 ID；标题及是否仍可选择由当前空间的授权查询决定。
export function recentTaskIds(orgId: string, userId?: string): string[] {
  if (!userId) return [];
  return (readLocal(key(orgId, userId)) ?? "")
    .split(",")
    .filter(Boolean)
    .slice(0, 20);
}

export function recordTaskVisit(orgId: string, userId: string, id: string) {
  const previous = recentTaskIds(orgId, userId);
  if (previous[0] !== id) {
    writeLocal(
      key(orgId, userId),
      [id, ...previous.filter((v) => v !== id)].slice(0, 20).join(","),
    );
  }
}
