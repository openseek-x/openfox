import type { MessageKey } from "@openfox/i18n";
import type {
  IntegrationCategory,
  IntegrationConnection,
  IntegrationProvider,
} from "@openfox/api";

// 外部应用集成的前端常量与纯函数。服务商名称是品牌名，不进 i18n。

/** 总览分组顺序。 */
export const GROUPS: {
  category: IntegrationCategory;
  title: MessageKey;
}[] = [
  { category: "task", title: "integrations.groups.task" },
  { category: "git", title: "integrations.groups.git" },
  { category: "collaboration", title: "integrations.groups.collaboration" },
];

/** 同一服务商在非主分组里的展示名。 */
export const EXTRA_ENTRIES: Record<
  string,
  { category: IntegrationCategory; name: string }
> = {
  github: { category: "task", name: "GitHub issues" },
  gitlab_cn: { category: "task", name: "极狐 GitLab.cn issues" },
  gitlab: { category: "task", name: "GitLab issues（自部署版）" },
  gitee: { category: "task", name: "Gitee Issue" },
};

export interface Card {
  /** 同一服务商在不同分组里 key 不同 */
  key: string;
  provider: IntegrationProvider;
  /** 该分组里展示的名称 */
  name: string;
}

/** 某分组要展示的卡片：主分组的服务商 + 映射到该分组的附加入口；available 在前，组内保持目录顺序。 */
export function cardsFor(
  providers: readonly IntegrationProvider[],
  category: IntegrationCategory,
): Card[] {
  const cards: Card[] = [];
  for (const provider of providers) {
    if (provider.category === category)
      cards.push({
        key: `${category}:${provider.id}`,
        provider,
        name: provider.name,
      });
    const extra = EXTRA_ENTRIES[provider.id];
    if (extra && extra.category === category && provider.category !== category)
      cards.push({
        key: `${category}:${provider.id}`,
        provider,
        name: extra.name,
      });
  }
  const rank = (card: Card) =>
    card.provider.availability === "available" ? 0 : 1;
  // Array.prototype.sort 稳定：同级别保持目录顺序
  return cards.sort((a, b) => rank(a) - rank(b));
}

/** 「已启用」分组：有未断开连接的服务商（按目录顺序）。 */
export function enabledProviders(
  providers: readonly IntegrationProvider[],
): IntegrationProvider[] {
  return providers.filter((provider) => provider.summary.connections > 0);
}

/** 已启用卡片的副文案键（按服务商类型）。 */
export function enabledSummaryKey(provider: IntegrationProvider): MessageKey {
  if (
    provider.id === "jira" ||
    provider.id === "tapd" ||
    provider.id === "zentao"
  )
    return "integrations.enabled.instances";
  if (provider.id === "github") return "integrations.enabled.githubOrgs";
  if (provider.id === "gitee") return "integrations.enabled.accounts";
  if (provider.category === "collaboration")
    return "integrations.enabled.enterprise";
  return "integrations.enabled.connections";
}

export type ConnectionTone = "ok" | "attention" | "off";

export function connectionTone(
  status: IntegrationConnection["status"],
): ConnectionTone {
  if (status === "ACTIVE") return "ok";
  if (status === "NEEDS_ATTENTION") return "attention";
  return "off";
}

/** 断开确认短语：「断开 {服务商名称}」。 */
export function confirmPhrase(name: string): string {
  return `断开 ${name}`;
}

export function initialOf(name: string): string {
  return Array.from(name.trim())[0]?.toUpperCase() ?? "?";
}

/** GitLab 系连接所在实例的主机名（取自契约字段 instance_url）；为空或无法解析时返回空串。 */
export function connectionInstanceHost(
  connection: IntegrationConnection,
): string {
  const raw = connection.instance_url;
  if (!raw) return "";
  try {
    return new URL(raw).host;
  } catch {
    return raw;
  }
}

// ---------- GitHub 绑定 ----------

export type OAuthErrorCode =
  | "access_denied"
  | "exchange_failed"
  | "account_mismatch"
  | "invalid_state"
  | "account_in_use";

const OAUTH_ERRORS: Record<OAuthErrorCode, MessageKey> = {
  access_denied: "integrations.github.errors.access_denied",
  exchange_failed: "integrations.github.errors.exchange_failed",
  account_mismatch: "integrations.github.errors.account_mismatch",
  invalid_state: "integrations.github.errors.invalid_state",
  account_in_use: "integrations.feishu.errors.account_in_use",
};

/** 授权失败原因对应的文案键；未知错误码、会话过期统一落到「授权已过期」。 */
export function oauthErrorKey(code: string | null | undefined): MessageKey {
  if (code && code in OAUTH_ERRORS) return OAUTH_ERRORS[code as OAuthErrorCode];
  return "integrations.github.errors.invalid_state";
}

/** 授权弹窗尺寸与居中位置（980×760）。 */
export function popupFeatures(
  screenLike: { width: number; height: number; left?: number; top?: number } = {
    width: window.screen.width,
    height: window.screen.height,
  },
): string {
  const width = 980;
  const height = 760;
  const left = Math.max(
    0,
    Math.round((screenLike.left ?? 0) + (screenLike.width - width) / 2),
  );
  const top = Math.max(
    0,
    Math.round((screenLike.top ?? 0) + (screenLike.height - height) / 2),
  );
  return `popup=yes,width=${width},height=${height},left=${left},top=${top}`;
}

export interface OAuthMessage {
  type: "openfox:oauth";
  provider: string;
  session: string;
  status: string;
  reason: string;
}

/** 只接受同源且 type 为 openfox:oauth 的消息。 */
export function parseOAuthMessage(
  event: { origin: string; data: unknown },
  origin: string,
): OAuthMessage | null {
  if (event.origin !== origin) return null;
  const data = event.data as Partial<OAuthMessage> | null;
  if (!data || typeof data !== "object" || data.type !== "openfox:oauth")
    return null;
  return {
    type: "openfox:oauth",
    provider: String(data.provider ?? ""),
    session: String(data.session ?? ""),
    status: String(data.status ?? ""),
    reason: String(data.reason ?? ""),
  };
}

/** 轮询授权会话的上限：10 分钟（与服务端 state 有效期一致）。 */
export const OAUTH_POLL_MS = 2000;
export const OAUTH_POLL_LIMIT_MS = 10 * 60 * 1000;

export const BRANCH_FORMATS = [
  "username/identifier-title",
  "identifier-title",
  "username/identifier",
  "identifier",
] as const;

// ---------- Issue 同步 ----------

/** 导入已有 Issue 期间刷新详情的间隔。 */
export const BACKFILL_POLL_MS = 5000;

// ---------- 飞书 ----------

/** 飞书详情：PENDING 时 5 秒刷新，其余 30 秒。 */
export const FEISHU_PENDING_POLL_MS = 5000;
export const FEISHU_POLL_MS = 30_000;
/** PENDING 超过这个时长仍未连上，诊断条给出排查建议。 */
export const FEISHU_PENDING_SLOW_MS = 30_000;

/** 飞书 App ID 格式：cli_ 加 8–40 位字母数字。 */
export const FEISHU_APP_ID_PATTERN = /^cli_[A-Za-z0-9]{8,40}$/;
// 钉钉 AppKey（Client ID）以 ding 开头
export const DINGTALK_APP_KEY_PATTERN = /^ding[A-Za-z0-9]{8,40}$/;
