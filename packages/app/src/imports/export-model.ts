import type { ExportJob, ExportSpace, ExportStatus } from "@openfox/api";
import { isApiError } from "@openfox/api";
import type { MessageKey } from "@openfox/i18n";

// 导出任务数据的前端常量与纯函数。

/** 单次导出上限，与后端 internal/exports 一致。 */
export const MAX_EXPORT_SPACES = 20;
export const MAX_EXPORT_TASKS = 100_000;

export function newExportKey(): string {
  return `export-${crypto.randomUUID()}`;
}

/** 已选空间的合计；超过任一上限时 over 为 true。 */
export function selectionSummary(
  spaces: readonly ExportSpace[],
  selected: ReadonlySet<string>,
) {
  let tasks = 0;
  let count = 0;
  for (const space of spaces) {
    if (!selected.has(space.id)) continue;
    count += 1;
    tasks += space.task_count;
  }
  return {
    spaces: count,
    tasks,
    over: count > MAX_EXPORT_SPACES || tasks > MAX_EXPORT_TASKS,
  };
}

export const isExportActive = (status: ExportStatus) =>
  status === "QUEUED" || status === "RUNNING";

/** 有生成中的导出时 2 秒刷新，否则不轮询。 */
export function exportPollInterval(
  jobs: readonly ExportJob[] | undefined,
): number | false {
  return jobs?.some((job) => isExportActive(job.status)) ? 2000 : false;
}

export function exportStatusKey(status: ExportStatus): MessageKey {
  switch (status) {
    case "SUCCEEDED":
      return "imports.export.status.succeeded";
    case "FAILED":
      return "imports.export.status.failed";
    case "EXPIRED":
      return "imports.export.status.expired";
    case "RUNNING":
      return "imports.export.status.running";
    default:
      return "imports.export.status.queued";
  }
}

export function exportStatusTone(status: ExportStatus) {
  switch (status) {
    case "SUCCEEDED":
      return "success" as const;
    case "FAILED":
      return "danger" as const;
    case "QUEUED":
    case "RUNNING":
      return "info" as const;
    default:
      return "muted" as const;
  }
}

/** 发起导出失败的提示：超限直接用后端文案，其余按状态码映射到本地化文案。 */
export function exportErrorMessage(
  error: unknown,
): { key: MessageKey } | { text: string } {
  if (isApiError(error)) {
    if (error.code === "EXPORT_TOO_LARGE" && error.message)
      return { text: error.message };
    if (error.status === 403) return { key: "imports.export.errors.forbidden" };
    if (error.code === "SPACE_NOT_FOUND")
      return { key: "imports.export.errors.space_missing" };
    if (error.code === "STORAGE_UNAVAILABLE")
      return { key: "imports.export.errors.storage" };
  }
  return { key: "imports.export.errors.generic" };
}
