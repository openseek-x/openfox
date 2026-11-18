import type { TaskStatus, TaskStatusCategory } from "@openfox/api";

/** 设置页展示的 6 个分类（不显示 TRIAGE），顺序固定。 */
export const STATUS_PAGE_CATEGORIES = [
  "BACKLOG",
  "TODO",
  "IN_PROGRESS",
  "DONE",
  "CANCELLED",
  "DUPLICATE",
] as const satisfies readonly TaskStatusCategory[];

export type PageCategory = (typeof STATUS_PAGE_CATEGORIES)[number];
export type StatusOrder = Record<PageCategory, string[]>;

/** 组织默认状态只能位于这两个分类。 */
export const DEFAULT_ALLOWED: ReadonlySet<TaskStatusCategory> = new Set([
  "BACKLOG",
  "TODO",
]);

export function isPageCategory(value: unknown): value is PageCategory {
  return (STATUS_PAGE_CATEGORIES as readonly unknown[]).includes(value);
}

/** 按服务端返回顺序（类别 + position）把未归档状态分到各分类。 */
export function buildOrder(statuses: TaskStatus[]): StatusOrder {
  const order = Object.fromEntries(
    STATUS_PAGE_CATEGORIES.map((category) => [category, [] as string[]]),
  ) as StatusOrder;
  for (const status of statuses) {
    if (status.archived_at || !isPageCategory(status.category)) continue;
    order[status.category].push(status.id);
  }
  return order;
}

export function findCategory(
  order: StatusOrder,
  id: string,
): PageCategory | undefined {
  if (isPageCategory(id)) return id;
  return STATUS_PAGE_CATEGORIES.find((category) =>
    order[category].includes(id),
  );
}

/** 移动前的规则校验：默认状态不能移出「待规划」「未开始」。 */
export function checkMove(
  status: TaskStatus,
  category: TaskStatusCategory,
): "ok" | "default-category" {
  if (status.is_default && !DEFAULT_ALLOWED.has(category))
    return "default-category";
  return "ok";
}

/** 乐观更新：把状态移到目标分类，放在 beforeId 之前；beforeId 为空时放到该分类末尾。 */
export function applyMove(
  statuses: TaskStatus[],
  statusId: string,
  category: TaskStatusCategory,
  beforeId: string | null,
): TaskStatus[] {
  const moving = statuses.find((status) => status.id === statusId);
  if (!moving) return statuses;
  const rest = statuses.filter((status) => status.id !== statusId);
  const moved = { ...moving, category };
  let index = beforeId
    ? rest.findIndex((status) => status.id === beforeId)
    : -1;
  if (index === -1) {
    // 末尾：该分类最后一个状态之后；分类为空时放在列表末尾（展示按分类分组，不影响）
    const last = rest.map((status) => status.category).lastIndexOf(category);
    index = last === -1 ? rest.length : last + 1;
  }
  return [...rest.slice(0, index), moved, ...rest.slice(index)];
}

/**
 * 拖拽结束后的目标位置：返回目标分类与紧随其后的状态（before）。
 * 位置与拖拽前一致时返回 null（不发请求）。
 */
export function resolveDrop(
  original: StatusOrder,
  final: StatusOrder,
  statusId: string,
): { category: PageCategory; beforeId: string | null } | null {
  const to = findCategory(final, statusId);
  const from = findCategory(original, statusId);
  if (!to || !from) return null;
  const list = final[to];
  const beforeId = list[list.indexOf(statusId) + 1] ?? null;
  const originalList = original[from];
  const originalBefore =
    originalList[originalList.indexOf(statusId) + 1] ?? null;
  if (to === from && beforeId === originalBefore) return null;
  return { category: to, beforeId };
}
