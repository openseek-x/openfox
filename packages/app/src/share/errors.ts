import { isApiError } from "@openfox/api";
import type { MessageKey, Translate } from "@openfox/i18n";

/** 创建分享快照失败的提示文案；脱敏命中由对话框单独展示位置。 */
export function shareErrorMessage(t: Translate, error: unknown): string {
  if (isApiError(error)) {
    const map: Record<string, MessageKey> = {
      NOT_FOUND: "share.errors.notFound",
      SHARE_SOURCE_EMPTY: "share.errors.empty",
      SHARE_TOO_LARGE: "share.errors.tooLarge",
      SHARE_QUOTA_EXCEEDED: "share.errors.quota",
      PUBLIC_SHARING_DISABLED: "share.errors.publicDisabled",
    };
    const key = map[error.code];
    if (key) return t(key);
  }
  return t("share.errors.generic");
}
