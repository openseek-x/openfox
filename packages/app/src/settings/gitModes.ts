import type { AgentConfigVersion, GitSettingsInput } from "@openfox/api";

export const GIT_NAME_MODES = [
  "INHERIT",
  "USER_AND_AGENT",
  "USER",
  "OWNER_AND_AGENT",
  "OWNER",
  "MACHINE",
  "MANUAL",
] as const satisfies ReadonlyArray<AgentConfigVersion["git_author_name_mode"]>;

export const GIT_EMAIL_MODES = [
  "INHERIT",
  "USER",
  "OWNER",
  "MACHINE",
  "MANUAL",
] as const satisfies ReadonlyArray<AgentConfigVersion["git_author_email_mode"]>;

export function gitSettingsDraft(settings: GitSettingsInput, space: boolean) {
  return {
    version: settings.version,
    author_name: settings.author_name,
    author_email: settings.author_email,
    co_author_enabled: settings.co_author_enabled,
    author_name_mode:
      settings.author_name_mode ??
      (settings.author_name ? "MANUAL" : space ? "INHERIT" : "USER_AND_AGENT"),
    author_email_mode:
      settings.author_email_mode ??
      (settings.author_email ? "MANUAL" : space ? "INHERIT" : "USER"),
  } satisfies GitSettingsInput;
}
