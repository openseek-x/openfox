import { useEffect, useRef, useState } from "react";
import { isApiError } from "@openfox/api";
import { useT, type Translate } from "@openfox/i18n";
export function teamsError(t: Translate, e: unknown) {
  if (isApiError(e)) {
    if (e.status === 403) return t("integrations.teams.forbidden");
    if (e.code === "VERSION_CONFLICT") return t("integrations.teams.conflict");
    if (e.code === "INVALID_REQUEST") return t("integrations.teams.invalid");
    if (e.code === "TEAMS_CONFIGURATION_UNAVAILABLE")
      return t("integrations.teams.configuration");
    if (e.code === "TEAMS_VERIFICATION_FAILED")
      return t("integrations.teams.credentials");
    if (e.code === "TEAMS_UNSUPPORTED_SCOPE")
      return t("integrations.teams.unsupported");
    if (e.status === 429) return t("integrations.teams.rateLimited");
  }
  return t("integrations.teams.error");
}
export function teamsReason(t: Translate, code: string) {
  if (code === "delivery_unknown") return t("integrations.teams.unknown");
  if (
    [
      "authorization_changed",
      "permission_denied",
      "scope_disabled",
      "identity_removed",
      "bot_removed",
    ].includes(code)
  )
    return t("integrations.teams.authorization");
  if (code.startsWith("rate_limit")) return t("integrations.teams.rateLimited");
  if (code.includes("credential")) return t("integrations.teams.credentials");
  return t("integrations.teams.error");
}
export function useTeamsAction() {
  const t = useT(),
    current = useRef<AbortController | null>(null),
    mounted = useRef(true);
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      current.current?.abort();
    };
  }, []);
  const run = async (fn: (signal: AbortSignal) => Promise<void>) => {
    if (current.current) return;
    const c = new AbortController();
    current.current = c;
    setBusy(true);
    setError("");
    try {
      await fn(c.signal);
    } catch (e) {
      if (mounted.current && !c.signal.aborted) setError(teamsError(t, e));
    } finally {
      if (current.current === c) current.current = null;
      if (mounted.current) setBusy(false);
    }
  };
  return { busy, error, run };
}
