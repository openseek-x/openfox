import type { BadgeTone } from "@openfox/ui";
import type {
  CloneJob,
  CloneJobPhase,
  CloneJobStatus,
  ClonePreview,
} from "@openfox/api";

// 克隆空间的前端常量与纯函数。

/** 终态：停止轮询并刷新两个组织的空间列表。 */
export const CLONE_TERMINAL: readonly CloneJobStatus[] = [
  "SUCCEEDED",
  "CLEANED",
  "FAILED",
  "CLEANUP_FAILED",
  "CANCELLED",
];

export function isCloneTerminal(status: CloneJobStatus): boolean {
  return CLONE_TERMINAL.includes(status);
}

/** 状态徽标颜色：等待中 / 运行中 / 清理中 = 蓝，已完成 / 已清理 = 绿，失败 / 清理失败 = 红，已取消 = 灰。 */
export function cloneStatusTone(status: CloneJobStatus): BadgeTone {
  switch (status) {
    case "SUCCEEDED":
    case "CLEANED":
      return "success";
    case "FAILED":
    case "CLEANUP_FAILED":
      return "danger";
    case "CANCELLED":
      return "muted";
    default:
      return "info";
  }
}

/** 预览里计数卡的显示顺序（值为 0 的不显示）。 */
export const CLONE_COUNT_KEYS = [
  "spaces",
  "tasks",
  "projects",
  "comments",
  "activities",
  "task_types",
  "task_statuses",
  "labels",
  "agents",
  "custom_fields",
] as const satisfies ReadonlyArray<keyof ClonePreview["counts"]>;

export type CloneCountKey = (typeof CLONE_COUNT_KEYS)[number];

export function visibleCounts(
  counts: ClonePreview["counts"],
): Array<{ key: CloneCountKey; value: number }> {
  return CLONE_COUNT_KEYS.map((key) => ({ key, value: counts[key] })).filter(
    (item) => item.value > 0,
  );
}

export function phaseKey(phase: CloneJobPhase) {
  return phase === "" ? null : (`spaces.clone.job.phase.${phase}` as const);
}

/** 取消按钮：非终态显示；已请求取消时禁用。 */
export function canCancelClone(job: CloneJob): boolean {
  return !isCloneTerminal(job.status);
}

/** 新的幂等键（满足后端 ^[A-Za-z0-9_-]{8,128}$）。 */
export function newCloneKey(): string {
  return `clone-${crypto.randomUUID()}`;
}

/** 失败原因只在清理 / 失败类状态显示；运行中的重试、已完成与用户主动取消都不显示。 */
const CLONE_FAILURE_STATUSES: readonly CloneJobStatus[] = [
  "CLEANING",
  "CLEANED",
  "FAILED",
  "CLEANUP_FAILED",
];

export const CLONE_ERROR_CODES = [
  "CLONE_FAILED",
  "TOO_MANY_ATTEMPTS",
  "IMPORTER_FORBIDDEN",
  "SOURCE_FORBIDDEN",
  "STORAGE_UNAVAILABLE",
  "BUNDLE_INVALID",
  "BUNDLE_MISSING",
  "IDENTIFIER_TAKEN",
  "CLEANUP_FAILED",
] as const;

export type CloneErrorCode = (typeof CLONE_ERROR_CODES)[number];

/**
 * 要展示的失败原因：返回已知错误码（前端按码取文案）或后端给的可读文案；不该显示时返回 null。
 * 后端只把可读文案放进 error_message，原始内部错误只写服务端日志。
 */
export function cloneFailureReason(
  job: CloneJob,
): { code: CloneErrorCode } | { message: string } | null {
  if (!CLONE_FAILURE_STATUSES.includes(job.status)) return null;
  if (job.error_code === "CANCELLED") return null;
  const known = CLONE_ERROR_CODES.find((code) => code === job.error_code);
  if (known) return { code: known };
  return job.error_message ? { message: job.error_message } : null;
}
