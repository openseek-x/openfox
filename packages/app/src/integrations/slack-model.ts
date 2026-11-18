import { isApiError } from "@openfox/api";
import type { MessageKey, Translate } from "@openfox/i18n";

const states = [
  "PREPARED",
  "PROVING",
  "PROOF_UNKNOWN",
  "CREATING",
  "CREATE_UNKNOWN",
  "APP_CREATED",
  "MANIFEST_UNKNOWN",
  "WAITING_URL_VERIFICATION",
  "WAITING_INSTALL",
  "ACTIVE",
  "NEEDS_REAUTH",
  "FAILED",
  "CANCELLED",
  "ORPHANED_APP",
  "DISCONNECTED",
] as const;
const reasons = [
  "needs_reauth",
  "permission_denied",
  "identity_mismatch",
  "configuration_missing",
  "credential_result_unknown",
  "configuration_expired",
  "shared_channel",
  "rate_limited",
  "installation_revoked",
  "target_in_use",
  "expired",
  "cancelled",
] as const;
export function slackState(t: Translate, state: string): string {
  const known = states.find((s) => s === state);
  return t(
    known
      ? `integrations.slack.states.${known}`
      : "integrations.slack.reasonGeneric",
  );
}
export function slackReason(t: Translate, reason: string): string {
  const aliases: Record<string, MessageKey> = {
    configuration_proof_unknown: "integrations.slack.proofUnknownHelp",
    configuration_identity_mismatch:
      "integrations.slack.reasons.identity_mismatch",
    session_expired: "integrations.slack.reasons.expired",
    reauthorization_pending: "integrations.slack.reasons.needs_reauth",
    url_verification_pending: "integrations.slack.urlPending",
  };
  if (aliases[reason]) return t(aliases[reason]);
  const known = reasons.find((s) => s === reason);
  return t(
    known
      ? `integrations.slack.reasons.${known}`
      : "integrations.slack.reasonGeneric",
  );
}
export function slackError(t: Translate, error: unknown): string {
  let key: MessageKey = "integrations.slack.errorGeneric";
  if (isApiError(error)) {
    if (error.uncertain || error.code === "INVALID_RESPONSE")
      key = "integrations.slack.errorUncertain";
    else if (error.status === 409) key = "integrations.slack.errorConflict";
    else if (error.status === 403 || error.status === 404)
      key = "integrations.slack.errorForbidden";
    else if (error.status === 429) key = "integrations.slack.errorRate";
    else if (error.status === 503) key = "integrations.slack.errorUnavailable";
  }
  return t(key);
}
export function slackAuthorizeURL(raw: string, purpose: string): boolean {
  try {
    const u = new URL(raw);
    return (
      u.origin === "https://slack.com" &&
      !u.username &&
      !u.password &&
      !u.hash &&
      u.pathname ===
        (purpose === "personal"
          ? "/openid/connect/authorize"
          : "/oauth/v2/authorize")
    );
  } catch {
    return false;
  }
}
export function slackSettingsURL(id: string): string | undefined {
  return /^[A-Za-z0-9_.-]{1,64}$/.test(id) && id !== "." && id !== ".."
    ? `https://api.slack.com/apps/${id}`
    : undefined;
}
export const slackPending = new Set([
  "PREPARED",
  "PROVING",
  "CREATING",
  "APP_CREATED",
  "WAITING_URL_VERIFICATION",
  "WAITING_INSTALL",
]);
export const slackUnknown = new Set([
  "PROOF_UNKNOWN",
  "CREATE_UNKNOWN",
  "MANIFEST_UNKNOWN",
  "ORPHANED_APP",
]);

/** Product storage uses a 64-code-point opaque ID, without a guessed Slack prefix. */
export function slackID(value: string): boolean {
  return (
    value.length > 0 &&
    Array.from(value).length <= 64 &&
    !/[\s\p{Cc}]/u.test(value)
  );
}
export function slackBodyFits(value: unknown): boolean {
  return new TextEncoder().encode(JSON.stringify(value)).length <= 8 * 1024;
}

export function slackDeliveryState(t: Translate, state: string): string {
  const known = (
    ["PENDING", "SENDING", "DONE", "UNKNOWN", "FAILED", "SKIPPED"] as const
  ).find((s) => s === state);
  return t(
    known
      ? `integrations.slack.deliveryStates.${known}`
      : "integrations.slack.reasonGeneric",
  );
}
export function slackMessageTimestamp(value: string): boolean {
  return /^\d{1,16}\.\d{1,9}$/.test(value);
}
