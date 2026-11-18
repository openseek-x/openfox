import { isApiError, type GongfengProject } from "@openfox/api";
import type { useT } from "@openfox/i18n";
import { integrationErrorText } from "./queries";

type T = ReturnType<typeof useT>;
export function gongfengError(t: T, error: unknown) {
  if (isApiError(error)) {
    if (error.code === "VERSION_CONFLICT")
      return t("integrations.gongfeng.conflict");
    const keys = {
      GONGFENG_RESULT_UNKNOWN: "unknown",
      GONGFENG_RATE_LIMITED: "limited",
      GONGFENG_INCOMPLETE: "incomplete",
      GONGFENG_UNAVAILABLE: "unavailable",
      GONGFENG_INVALID: "invalid",
      GONGFENG_QUEUE_FULL: "queueFull",
    } as const;
    const key = keys[error.code as keyof typeof keys];
    if (key) return t(`integrations.gongfeng.${key}`);
  }
  return integrationErrorText(t, error);
}

// Never use upstream web_url or an unvalidated path as a navigation target.
export function gongfengRepositoryURL(path: string): string | undefined {
  const parts = path.split("/");
  if (
    parts.length < 2 ||
    parts.some((p) => !/^[A-Za-z0-9_.-]+$/.test(p) || p === "." || p === "..")
  )
    return;
  return `https://git.tencent.com/${parts.join("/")}`;
}
export function gongfengPrivacy(project: GongfengProject): boolean | null {
  if (project.visibility_level === 0 && project.public === false) return true;
  if (
    (project.visibility_level === 10 && project.public === false) ||
    (project.visibility_level === 20 && project.public === true)
  )
    return false;
  return null;
}
export function gongfengSelectable(project: GongfengProject) {
  return (
    project.id > 0 &&
    project.type === "GIT" &&
    project.archived === false &&
    project.issues_enabled === true &&
    !!gongfengRepositoryURL(project.path_with_namespace)
  );
}
export function gongfengWindowValid(from: string, to: string) {
  const rfc3339 =
    /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/;
  const start = Date.parse(from),
    end = Date.parse(to);
  return (
    rfc3339.test(from) &&
    rfc3339.test(to) &&
    Number.isFinite(start) &&
    Number.isFinite(end) &&
    start < end &&
    end <= Date.now() + 60_000
  );
}
export function gongfengPushValid(from: string, to: string, ref: string) {
  const sha = /^[0-9a-f]{40}$/;
  return (
    sha.test(from) &&
    sha.test(to) &&
    !/^0+$/.test(from) &&
    !/^0+$/.test(to) &&
    from !== to &&
    /^refs\/heads\/[^\s]+$/.test(ref)
  );
}
