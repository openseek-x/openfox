import { isApiError } from "@openfox/api";
import type { MessageKey, Translate } from "@openfox/i18n";

const states: Record<string, MessageKey> = {
  SETTING: "integrations.telegram.states.SETTING",
  ACTIVE: "integrations.telegram.states.ACTIVE",
  ATTENTION: "integrations.telegram.states.ATTENTION",
  DELETING: "integrations.telegram.states.DELETING",
  DISCONNECTED: "integrations.telegram.states.DISCONNECTED",
  PENDING: "integrations.telegram.states.PENDING",
  SENDING: "integrations.telegram.states.SENDING",
  UNKNOWN: "integrations.telegram.states.UNKNOWN",
  DONE: "integrations.telegram.states.DONE",
  FAILED: "integrations.telegram.states.FAILED",
  PENDING_CONFIRM: "integrations.telegram.states.PENDING_CONFIRM",
  COMPLETED: "integrations.telegram.states.COMPLETED",
  CANCELLED: "integrations.telegram.states.CANCELLED",
  CANDIDATE: "integrations.telegram.states.CANDIDATE",
  WAITING: "integrations.telegram.states.WAITING",
  SENT: "integrations.telegram.states.SENT",
  SKIPPED: "integrations.telegram.states.SKIPPED",
};
export const telegramState = (t: Translate, value: string) =>
  t(states[value] ?? "integrations.telegram.states.generic");

export function telegramError(t: Translate, error: unknown) {
  if (isApiError(error)) {
    const fixed: Record<string, MessageKey> = {
      TELEGRAM_BOT_OCCUPIED: "integrations.telegram.errors.occupied",
      TELEGRAM_WEBHOOK_CONFLICT: "integrations.telegram.errors.webhook",
      TELEGRAM_SESSION_STATE: "integrations.telegram.errors.expired",
      TELEGRAM_UNSUPPORTED: "integrations.telegram.errors.unsupported",
      TELEGRAM_VERIFICATION_FAILED: "integrations.telegram.errors.credentials",
      TELEGRAM_CONFIGURATION_UNAVAILABLE: "integrations.telegram.configured",
      TELEGRAM_IDENTITY_MISMATCH: "integrations.telegram.errors.conflict",
      TELEGRAM_UNAVAILABLE: "integrations.telegram.errors.unavailable",
    };
    const key = fixed[error.code];
    if (key) return t(key);
    if (error.status === 403)
      return t("integrations.telegram.errors.forbidden");
    if (error.code === "VERSION_CONFLICT")
      return t("integrations.telegram.errors.conflict");
    if (error.code === "INVALID_REQUEST")
      return t("integrations.telegram.errors.invalid");
    if (error.status === 429)
      return t("integrations.telegram.errors.rateLimited");
  }
  return t("integrations.telegram.errors.generic");
}

/** The sole secret link shape issued by the server; never persist its return. */
export function telegramBindingURL(
  raw: string,
  username: string,
): string | null {
  try {
    const url = new URL(raw);
    if (
      url.protocol !== "https:" ||
      url.host !== "t.me" ||
      url.username ||
      url.password ||
      url.hash ||
      url.pathname !== `/${username}` ||
      [...url.searchParams.keys()].length !== 1 ||
      !/^bind_[A-Za-z0-9_-]{43}$/.test(url.searchParams.get("start") ?? "")
    )
      return null;
    return url.href;
  } catch {
    return null;
  }
}

export function telegramReason(t: Translate, code: string) {
  if (code === "delivery_unknown" || code === "control_result_unknown")
    return t("integrations.telegram.errors.unknown");
  if (
    code === "authorization_changed" ||
    code === "permission_denied" ||
    code === "target_forbidden"
  )
    return t("integrations.telegram.errors.authorization");
  if (code === "credentials_invalid" || code === "credentials_unavailable")
    return t("integrations.telegram.errors.credentials");
  if (code === "rate_limited" || code === "queue_full")
    return t("integrations.telegram.errors.rateLimited");
  return t("integrations.telegram.errors.generic");
}
