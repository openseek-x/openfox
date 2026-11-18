import { useQueryClient } from "@tanstack/react-query";
import { isApiError } from "@openfox/api";
import type { Translate } from "@openfox/i18n";
import { keys } from "../context";
export { useSlackWrite as useDiscordWrite } from "./slack-hooks";
export const discordKey = (org: string) => [
  ...keys.integrations(org),
  "discord",
];
export function useDiscordRefresh(org: string) {
  const cache = useQueryClient();
  return () => {
    void cache.invalidateQueries({ queryKey: discordKey(org) });
  };
}
export function discordError(t: Translate, e: unknown): string {
  if (isApiError(e)) {
    if (e.status === 409) return t("integrations.discord.conflict");
    if (e.status === 403 || e.status === 404)
      return t("integrations.discord.forbidden");
    if (e.status === 429) return t("integrations.discord.limited");
    if (e.status >= 500) return t("integrations.discord.unavailable");
  }
  return t("integrations.discord.error");
}
export function discordInstallURL(
  raw: string,
  app: string,
): string | undefined {
  try {
    const u = new URL(raw);
    if (
      u.origin === "https://discord.com" &&
      u.pathname === "/oauth2/authorize" &&
      !u.username &&
      !u.password &&
      !u.hash &&
      u.searchParams.get("client_id") === app &&
      u.searchParams.get("scope") === "bot" &&
      u.searchParams.get("permissions") === "68608" &&
      u.searchParams.get("integration_type") === "0"
    )
      return u.href;
  } catch {
    /* Invalid provider link stays unavailable. */
  }
  return undefined;
}
