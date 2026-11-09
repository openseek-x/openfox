import type { HttpClient } from "./http";
import type { components, operations } from "./schema.gen";

// 阶段 4b · 个人资料、偏好与修改邮箱；阶段 4c · 个人访问令牌、通知渠道与内测功能。所有公开类型取自生成契约。
type S = components["schemas"];

export type UserPreferences = S["UserPreferences"];
export type UserPreferencesPatch = S["UserPreferencesPatch"];
export type CommandShortcuts = S["CommandShortcuts"];
export type EmailChallenge = S["EmailChallenge"];
export type PersonalAccessToken = S["PersonalAccessToken"];
export type PersonalAccessTokenInput = NonNullable<
  operations["createMyToken"]["requestBody"]
>["content"]["application/json"];
export type PersonalAccessTokenCreated = {
  token: PersonalAccessToken;
  /** ofx_pat_ 开头的明文，只在创建响应里出现一次 */
  secret: string;
};
export type InboxPreference = S["InboxPreference"];
export type InboxPreferenceLevel = S["InboxPreferenceLevel"];
export type NotificationChannel = S["NotificationChannel"];
export type NotificationChannelName = NotificationChannel["channel"];
export type NotificationEvent = S["NotificationEvent"];
export type BetaFeature = S["BetaFeature"];

export function createMeApi(http: HttpClient) {
  return {
    /** 修改全名或简介；全名去掉首尾空白后不能为空（400） */
    update: (patch: { full_name?: string; bio?: string }) =>
      http.request<S["Me"]>("/v1/me", { method: "PATCH", body: patch }),
    /** 上传头像（multipart 字段 file）；超过 5 MB 由调用方先拦截，不发请求 */
    uploadAvatar: (file: File) => {
      const body = new FormData();
      body.append("file", file);
      return http.request<S["Me"]>("/v1/me/avatar", { method: "PUT", body });
    },
    preferences: {
      get: () => http.request<UserPreferences>("/v1/me/preferences"),
      /** 带当前 version；不一致 409 VERSION_CONFLICT */
      update: (patch: UserPreferencesPatch) =>
        http.request<UserPreferences>("/v1/me/preferences", {
          method: "PATCH",
          body: patch,
        }),
    },
    /** 修改邮箱四步：start → verifyCurrent → requestNew → confirm */
    emailChange: {
      start: () =>
        http.request<EmailChallenge>("/v1/me/email-change/start", {
          method: "POST",
        }),
      verifyCurrent: (challengeId: string, code: string) =>
        http.request<void>("/v1/me/email-change/verify-current", {
          method: "POST",
          body: { challenge_id: challengeId, code },
        }),
      /** challengeId 是已验证的第 1 步验证码；409 EMAIL_TAKEN */
      requestNew: (challengeId: string, email: string) =>
        http.request<EmailChallenge>("/v1/me/email-change/request-new", {
          method: "POST",
          body: { challenge_id: challengeId, email },
        }),
      confirm: (challengeId: string, code: string) =>
        http.request<S["Me"]>("/v1/me/email-change/confirm", {
          method: "POST",
          body: { challenge_id: challengeId, code },
        }),
    },
    /** 阶段 4c · 个人访问令牌；用令牌认证时 403，界面只在浏览器登录态下使用 */
    tokens: {
      list: async () =>
        (await http.request<{ tokens: PersonalAccessToken[] }>("/v1/me/tokens"))
          .tokens,
      /** 超过 50 个未吊销令牌 409 TOKEN_LIMIT */
      create: (input: PersonalAccessTokenInput) =>
        http.request<PersonalAccessTokenCreated>("/v1/me/tokens", {
          method: "POST",
          body: input,
        }),
      revoke: (tokenId: string) =>
        http.request<void>(`/v1/me/tokens/${encodeURIComponent(tokenId)}`, {
          method: "DELETE",
        }),
    },
    /** 阶段 4c · 通知渠道（桌面端、浏览器）；保存时整体替换 enabled 与 disabled_events */
    inboxPreferences: {
      list: () => http.request<InboxPreference[]>("/v1/me/inbox-preferences"),
      update: (input: Pick<InboxPreference, "key" | "level">) =>
        http.request<InboxPreference[]>("/v1/me/inbox-preferences", {
          method: "PATCH",
          body: input,
        }),
      reset: () =>
        http.request<InboxPreference[]>("/v1/me/inbox-preferences", {
          method: "DELETE",
        }),
    },
    notificationChannels: {
      list: async () =>
        (
          await http.request<{ channels: NotificationChannel[] }>(
            "/v1/me/notification-channels",
          )
        ).channels,
      update: (
        channel: NotificationChannelName,
        input: {
          enabled: boolean;
          disabled_events: NotificationEvent[];
          direct_messages?: boolean;
        },
      ) =>
        http.request<NotificationChannel>(
          `/v1/me/notification-channels/${channel}`,
          { method: "PUT", body: input },
        ),
      /** 恢复默认设置 */
      reset: (channel: NotificationChannelName) =>
        http.request<NotificationChannel>(
          `/v1/me/notification-channels/${channel}`,
          { method: "DELETE" },
        ),
    },
    /** 阶段 4c · 内测功能开关；只有 access_tier 为 beta / internal 的账号可用（其余 403） */
    betaFeatures: {
      list: async () =>
        (
          await http.request<{ features: BetaFeature[] }>(
            "/v1/me/beta-features",
          )
        ).features,
      update: (key: string, enabled: boolean) =>
        http.request<BetaFeature>(
          `/v1/me/beta-features/${encodeURIComponent(key)}`,
          { method: "PUT", body: { enabled } },
        ),
    },
  };
}
