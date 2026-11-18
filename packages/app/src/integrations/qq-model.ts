import { isApiError } from "@openfox/api";
import type { MessageKey, Translate } from "@openfox/i18n";
const states: Record<string, MessageKey> = {
  PREPARED: "integrations.qq.statePrepared",
  VALIDATING: "integrations.qq.stateValidating",
  WAITING_WEBHOOK: "integrations.qq.stateWebhook",
  ACTIVE: "integrations.qq.stateActive",
  NEEDS_REAUTH: "integrations.qq.stateReauth",
  DISCONNECTED: "integrations.qq.stateDisconnected",
  CANCELLED: "integrations.qq.stateCancelled",
  PENDING: "integrations.qq.statePending",
  POLLING: "integrations.qq.statePolling",
  WAITING_SCAN: "integrations.qq.stateScan",
  DONE: "integrations.qq.stateDone",
  UNKNOWN: "integrations.qq.stateUnknown",
  FAILED: "integrations.qq.stateFailed",
  EXPIRED: "integrations.qq.stateExpired",
  OBSERVED: "integrations.qq.stateObserved",
  CONFIRMED: "integrations.qq.stateConfirmed",
  SENDING: "integrations.qq.stateSending",
  SKIPPED: "integrations.qq.stateSkipped",
};
export const qqState = (t: Translate, state: string) =>
  t(states[state] ?? "integrations.qq.stateOther");
export const qqError = (t: Translate, error: unknown) =>
  t(
    isApiError(error) && error.status === 409
      ? "integrations.qq.conflict"
      : isApiError(error) && [403, 404].includes(error.status)
        ? "integrations.qq.forbidden"
        : "integrations.qq.error",
  );
export function qqOfficialURL(value: string) {
  try {
    const u = new URL(value);
    return u.origin === "https://q.qq.com" &&
      u.pathname === "/qqbot/openclaw/connect.html" &&
      !u.username &&
      !u.password &&
      !u.hash
      ? u.href
      : null;
  } catch {
    return null;
  }
}
