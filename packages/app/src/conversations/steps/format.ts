import type { RunFailureCode } from "@openfox/api";
import type { MessageKey, Translate } from "@openfox/i18n";

/**
 * 统一的耗时格式：
 * < 10 秒保留一位小数（「1.2 秒」，整数不带 .0）；10–59 秒取整（「12 秒」）；≥ 60 秒「x 分 y 秒」。
 * 英文为「1.2s」「12s」「1m 18s」。负数、NaN 按 0 处理。
 */
export function formatDuration(ms: number, t: Translate): string {
  const value = Number.isFinite(ms) && ms > 0 ? ms : 0;
  if (value < 10_000) {
    const seconds = Math.round(value / 100) / 10;
    if (seconds < 10)
      return t("steps.duration.seconds", { seconds: String(seconds) });
  }
  const total = Math.round(value / 1000);
  if (total < 60) return t("steps.duration.seconds", { seconds: total });
  return t("steps.duration.minutes", {
    minutes: Math.floor(total / 60),
    seconds: total % 60,
  });
}

/** 两个时间点之间的毫秒数；任一缺失或非法时返回 null。 */
export function elapsedMs(
  from: string | null | undefined,
  to: string | Date | null | undefined,
): number | null {
  if (!from || !to) return null;
  const start = new Date(from).getTime();
  const end = (to instanceof Date ? to : new Date(to)).getTime();
  if (!Number.isFinite(start) || !Number.isFinite(end)) return null;
  return Math.max(0, end - start);
}

type FailureCode = Exclude<RunFailureCode, "">;

/** 契约 RunFailureCode 的全部取值（空串除外）；类型保证新增枚举时这里必须同步。 */
export const FAILURE_CODES = [
  "CREDENTIAL_REJECTED",
  "QUOTA_EXHAUSTED",
  "RATE_LIMITED",
  "PROVIDER_OVERLOADED",
  "MODEL_NOT_FOUND",
  "CONTEXT_TOO_LARGE",
  "IMAGE_TOO_LARGE",
  "NETWORK_UNREACHABLE",
  "WORKTREE_PREP_FAILED",
  "REPO_UNAVAILABLE",
  "RUNTIME_UNAVAILABLE",
  "QUEUE_TIMEOUT",
  "RUN_TIMEOUT",
  "AUTHORIZATION_REVOKED",
  "UNKNOWN_PROCESS",
  "PREPARE_FAILED",
  "INTERNAL",
] as const satisfies readonly FailureCode[];

// 反向检查：FAILURE_CODES 覆盖全部枚举
type Missing = Exclude<FailureCode, (typeof FAILURE_CODES)[number]>;
const coverage: Missing extends never ? true : Missing = true;
void coverage;

const isFailureCode = (code: string): code is FailureCode =>
  (FAILURE_CODES as readonly string[]).includes(code);

/** failure_code → 标题与说明的文案键；空串或未知码用通用文案。 */
export function failureMessageKeys(code: string | null | undefined): {
  title: MessageKey;
  description: MessageKey;
} {
  const key = code && isFailureCode(code) ? code : "generic";
  return {
    title: `steps.error.titles.${key}`,
    description: `steps.error.descriptions.${key}`,
  };
}
