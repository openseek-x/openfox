import { isApiError } from "@openfox/api";
import type { Translate } from "@openfox/i18n";

export function codecommitError(t: Translate, error: unknown) {
  if (isApiError(error)) {
    if (error.code === "VERSION_CONFLICT")
      return t("integrations.codecommit.conflict");
    if (
      error.code === "CODECOMMIT_CREDENTIALS_INVALID" ||
      error.code === "INTEGRATION_NEEDS_REAUTH"
    )
      return t("integrations.codecommit.credentialError");
    if (error.code === "CODECOMMIT_KMS_UNAVAILABLE")
      return t("integrations.codecommit.kmsError");
    if (error.code === "CODECOMMIT_UNAVAILABLE")
      return t("integrations.codecommit.upstreamError");
    if (error.code === "CODECOMMIT_RATE_LIMITED" || error.status === 429)
      return t("integrations.codecommit.rateError");
    if (error.status === 403) return t("integrations.codecommit.forbidden");
    if (
      error.code === "INTEGRATION_NOT_CONFIGURED" ||
      error.code === "SECRETS_UNAVAILABLE"
    )
      return t("integrations.codecommit.notConfigured");
    if (error.status === 400) return t("integrations.codecommit.invalid");
  }
  return t("integrations.codecommit.failed");
}
const reasons = {
  credentials_invalid_or_expired: "credentials",
  permission_denied: "permission",
  kms_unavailable: "kms",
  repository_changed: "repository",
  not_found: "notFound",
  rate_limited: "rate",
  upstream_unavailable: "upstream",
  incomplete_response: "incomplete",
  commit_graph_budget_exceeded: "budget",
  commit_limit: "commits",
  invalid_commit_graph: "graph",
  version_conflict: "version",
  sender_changed: "sender",
  comment_result_unknown: "unknown",
  repository_generation_changed: "generation",
  connection_disconnected: "disconnected",
} as const;
export function codecommitReason(t: Translate, code: string) {
  const key = reasons[code as keyof typeof reasons] ?? "other";
  return t(`integrations.codecommit.reasons.${key}`);
}
export function codecommitObjectURL(value: string) {
  try {
    const url = new URL(value);
    return url.protocol === "https:" &&
      !url.username &&
      !url.password &&
      (url.hostname === "console.aws.amazon.com" ||
        url.hostname.endsWith(".console.aws.amazon.com"))
      ? url.href
      : undefined;
  } catch {
    return undefined;
  }
}
