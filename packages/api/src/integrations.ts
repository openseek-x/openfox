import type { HttpClient } from "./http";
import type { components, operations } from "./schema.gen";

// 阶段 6a · 外部应用集成：总览、详情、断开连接。所有公开类型取自生成契约。
type S = components["schemas"];

export type IntegrationCategory = S["IntegrationCategory"];
export type IntegrationProvider = S["IntegrationProvider"];
export type IntegrationOverview = S["IntegrationOverview"];
export type IntegrationConnection = S["IntegrationConnection"];
export type IntegrationSyncLink = S["IntegrationSyncLink"];
export type IntegrationDetail = S["IntegrationDetail"];
export type IntegrationOAuthSession = S["IntegrationOAuthSession"];
export type IntegrationWebhook = S["IntegrationWebhook"];
export type IntegrationSettings = S["IntegrationSettings"];
export type IntegrationSettingsValues = S["IntegrationSettingsValues"];
export type IntegrationRepositories = NonNullable<
  operations["listIntegrationRepositories"]["responses"][200]["content"]["application/json"]
>;
export type IntegrationRepository =
  IntegrationRepositories["repositories"][number];
export type IntegrationSyncLinkCreateInput = NonNullable<
  operations["createIntegrationSyncLink"]["requestBody"]
>["content"]["application/json"];
export type IntegrationSyncLinkUpdateInput = NonNullable<
  operations["updateIntegrationSyncLink"]["requestBody"]
>["content"]["application/json"];
export type OAuthPurpose = NonNullable<
  operations["startGitHubAuthorization"]["requestBody"]
>["content"]["application/json"]["purpose"];
/** GitLab / 极狐授权请求体（比 GitHub 多实例地址与 OAuth 应用凭据，仅 organization 用）。 */
export type GitLabTokenInput = NonNullable<
  operations["bindGitLabToken"]["requestBody"]
>["content"]["application/json"];
export type GitAuthorizeInput = NonNullable<
  operations["startGitLabAuthorization"]["requestBody"]
>["content"]["application/json"];
export type FeishuAppView = S["FeishuAppView"];
export type FeishuApp = S["FeishuApp"];
export type FeishuPersonalBinding = S["FeishuPersonalBinding"];
export type FeishuGuide = S["FeishuGuide"];
export type FeishuAppInput = NonNullable<
  operations["putFeishuApp"]["requestBody"]
>["content"]["application/json"];
export type WeChatView = S["WeChatView"];
export type WeChatBinding = S["WeChatBinding"];
export type WeChatQRSession = S["WeChatQRSession"];
export type FeishuBot = S["FeishuBot"];
export type FeishuBotsView = S["FeishuBotsView"];
export type FeishuRegistration = S["FeishuRegistration"];
export type FeishuAssistantRuntime = S["FeishuAssistantRuntime"];
export type FeishuRegistrationInput = NonNullable<
  operations["startFeishuRegistration"]["requestBody"]
>["content"]["application/json"];
export type DingTalkBotInput = NonNullable<
  operations["createDingTalkBot"]["requestBody"]
>["content"]["application/json"];
export type WeComBotInput = S["WeComBotInput"];
export type WeComPersonalView = S["WeComPersonalView"];
export type WeComBindingCode = S["WeComBindingCode"];
export type FeishuBotUpdateInput = NonNullable<
  operations["updateFeishuBot"]["requestBody"]
>["content"]["application/json"];
export type IntegrationDisconnectInput = NonNullable<
  operations["disconnectIntegrationConnection"]["requestBody"]
>["content"]["application/json"];

// 阶段 6e · 第三方账号绑定
export type AccountBindingsView = S["AccountBindingsView"];
export type AccountBindingProvider = S["AccountBindingProvider"];
export type AccountBinding = S["AccountBinding"];
export type AccountBindingInstance = S["AccountBindingInstance"];
export type JiraBindingInput = NonNullable<
  operations["putMyJiraBinding"]["requestBody"]
>["content"]["application/json"];
export type JiraBindingResult = NonNullable<
  operations["putMyJiraBinding"]["responses"][200]["content"]
>["application/json"];

const base = (orgId: string) =>
  `/v1/organizations/${encodeURIComponent(orgId)}/integrations`;

/** 外部应用集成方法组，由 createApi 展开到顶层。 */
export function createIntegrationsApi(http: HttpClient) {
  return {
    accountBindings: {
      list: (orgId: string) =>
        http.request<AccountBindingsView>(
          `/v1/organizations/${encodeURIComponent(orgId)}/me/account-bindings`,
        ),
      /** 令牌只用于这一次校验，服务端不保存 */
      putJira: (orgId: string, input: JiraBindingInput) =>
        http.request<JiraBindingResult>(
          `/v1/organizations/${encodeURIComponent(orgId)}/me/account-bindings/jira`,
          { method: "PUT", body: input },
        ),
    },
    integrations: {
      bindSitePersonal: (
        orgId: string,
        input: NonNullable<
          operations["bindSitePersonal"]["requestBody"]
        >["content"]["application/json"],
      ) =>
        http.request<IntegrationConnection>(`${base(orgId)}/site-personal`, {
          method: "PUT",
          body: input,
        }),
      bindPingCodePersonal: (
        orgId: string,
        input: { connection_id: string; version: number; token: string },
      ) =>
        http.request<IntegrationConnection>(
          `${base(orgId)}/pingcode-personal`,
          { method: "PUT", body: input },
        ),
      shortcutHook: (org: string, id: string) =>
        http.request<S["ShortcutHook"]>(
          `${base(org)}/connections/${encodeURIComponent(id)}/shortcut-webhook`,
        ),
      saveShortcutHook: (
        org: string,
        id: string,
        version: number,
        secret: string,
      ) =>
        http.request<S["ShortcutHook"]>(
          `${base(org)}/connections/${encodeURIComponent(id)}/shortcut-webhook`,
          { method: "PUT", body: { version, secret } },
        ),
      bindInstanceToken: (
        orgId: string,
        provider: string,
        input: NonNullable<
          operations["bindProviderInstanceToken"]["requestBody"]
        >["content"]["application/json"],
      ) =>
        http.request<IntegrationConnection>(
          `${base(orgId)}/${encodeURIComponent(provider)}/instance-token`,
          { method: "POST", body: input },
        ),
      list: (orgId: string) => http.request<IntegrationOverview>(base(orgId)),
      get: (orgId: string, provider: string) =>
        http.request<IntegrationDetail>(
          `${base(orgId)}/${encodeURIComponent(provider)}`,
        ),
      disconnect: (
        orgId: string,
        connectionId: string,
        input: IntegrationDisconnectInput,
      ) =>
        http.request<IntegrationConnection>(
          `${base(orgId)}/connections/${encodeURIComponent(connectionId)}/disconnect`,
          { method: "POST", body: input },
        ),
      // 阶段 6b-1 · GitHub OAuth 绑定
      startGitHubAuthorization: (
        orgId: string,
        input: { purpose: OAuthPurpose; connection_id?: string },
      ) =>
        http.request<{
          authorize_url: string;
          session_id: string;
          expires_at: string;
        }>(`${base(orgId)}/github/authorize`, { method: "POST", body: input }),
      // 阶段 8 · GitLab 自部署版 / 极狐 / Gitee：与 GitHub 同形，路径按服务商区分（gitlab、gitlab_cn、gitee）
      startGitAuthorization: (
        orgId: string,
        provider: string,
        input: GitAuthorizeInput,
      ) =>
        http.request<{
          authorize_url: string;
          session_id: string;
          expires_at: string;
        }>(`${base(orgId)}/${encodeURIComponent(provider)}/authorize`, {
          method: "POST",
          body: input,
        }),
      bindGitLabToken: (orgId: string, input: GitLabTokenInput) =>
        http.request<IntegrationConnection>(`${base(orgId)}/gitlab/token`, {
          method: "POST",
          body: input,
        }),
      oauthSession: (orgId: string, sessionId: string) =>
        http.request<IntegrationOAuthSession>(
          `${base(orgId)}/oauth/${encodeURIComponent(sessionId)}`,
        ),
      completeOAuthSession: (
        orgId: string,
        sessionId: string,
        accounts: string[],
      ) =>
        http.request<{ connections: IntegrationConnection[] }>(
          `${base(orgId)}/oauth/${encodeURIComponent(sessionId)}/complete`,
          { method: "POST", body: { accounts } },
        ),
      webhook: (orgId: string, connectionId: string) =>
        http.request<IntegrationWebhook>(
          `${base(orgId)}/connections/${encodeURIComponent(connectionId)}/webhook`,
        ),
      rotateWebhook: (orgId: string, connectionId: string, version: number) =>
        http.request<IntegrationWebhook>(
          `${base(orgId)}/connections/${encodeURIComponent(connectionId)}/webhook/rotate`,
          { method: "POST", body: { version } },
        ),
      settings: (orgId: string, provider: string) =>
        http.request<IntegrationSettings>(
          `${base(orgId)}/${encodeURIComponent(provider)}/settings`,
        ),
      putSettings: (
        orgId: string,
        provider: string,
        input: { version: number; settings: IntegrationSettingsValues },
      ) =>
        http.request<IntegrationSettings>(
          `${base(orgId)}/${encodeURIComponent(provider)}/settings`,
          { method: "PUT", body: input },
        ),
      // 阶段 6d-1 · 飞书
      feishuApp: (orgId: string, provider = "feishu") =>
        http.request<FeishuAppView>(`${base(orgId)}/${provider}/app`),
      putFeishuApp: (
        orgId: string,
        input: FeishuAppInput,
        provider = "feishu",
      ) =>
        http.request<FeishuApp>(`${base(orgId)}/${provider}/app`, {
          method: "PUT",
          body: input,
        }),
      verifyFeishuApp: (orgId: string, provider = "feishu") =>
        http.request<FeishuApp>(`${base(orgId)}/${provider}/app/verify`, {
          method: "POST",
        }),
      patchFeishuAppSettings: (
        orgId: string,
        input: { link_preview_enabled: boolean; version: number },
        provider = "feishu",
      ) =>
        http.request<FeishuApp>(`${base(orgId)}/${provider}/app/settings`, {
          method: "PATCH",
          body: input,
        }),
      startFeishuAuthorization: (orgId: string, provider = "feishu") =>
        http.request<{
          authorize_url: string;
          session_id: string;
          expires_at: string;
        }>(`${base(orgId)}/${provider}/authorize`, { method: "POST" }),
      // 阶段 7g-W · 微信（ClawBot / iLink）扫码绑定 Agent
      wechatBindings: (orgId: string) =>
        http.request<WeChatView>(`${base(orgId)}/wechat/bindings`),
      startWeChatQR: (orgId: string, agentId: string) =>
        http.request<WeChatQRSession>(
          `${base(orgId)}/wechat/agent-bindings/qr`,
          { method: "POST", body: { agent_id: agentId } },
        ),
      // 每次调用向 iLink 查询一次扫码状态（服务端长轮询，最多约 25 秒）
      wechatQR: (orgId: string, id: string) =>
        http.request<WeChatQRSession>(
          `${base(orgId)}/wechat/agent-bindings/qr/${encodeURIComponent(id)}`,
        ),
      verifyWeChatQR: (orgId: string, id: string, code: string) =>
        http.request<WeChatQRSession>(
          `${base(orgId)}/wechat/agent-bindings/qr/${encodeURIComponent(id)}/verify`,
          { method: "POST", body: { code } },
        ),
      cancelWeChatQR: (orgId: string, id: string) =>
        http.request<WeChatQRSession>(
          `${base(orgId)}/wechat/agent-bindings/qr/${encodeURIComponent(id)}/cancel`,
          { method: "POST" },
        ),
      removeWeChatBinding: (orgId: string, id: string, version: number) =>
        http.request<void>(
          `${base(orgId)}/wechat/bindings/${encodeURIComponent(id)}/remove`,
          { method: "POST", body: { version } },
        ),
      // 阶段 6d-2 · 飞书任务助手与 Agent 机器人（扫码创建）
      feishuBots: (orgId: string, provider = "feishu") =>
        http.request<FeishuBotsView>(`${base(orgId)}/${provider}/bots`),
      // 阶段 7g-1 · 钉钉：AppKey + AppSecret 直接连接机器人（钉钉没有扫码创建接口）
      createDingTalkBot: (orgId: string, input: DingTalkBotInput) =>
        http.request<FeishuBot>(`${base(orgId)}/dingtalk/bots`, {
          method: "POST",
          body: input,
        }),
      createWeComBot: (orgId: string, input: WeComBotInput) =>
        http.request<FeishuBot>(`${base(orgId)}/wecom/bots`, {
          method: "POST",
          body: input,
        }),
      wecomPersonal: (orgId: string) =>
        http.request<WeComPersonalView>(`${base(orgId)}/wecom/personal`),
      createWeComBindingCode: (orgId: string, botId: string) =>
        http.request<WeComBindingCode>(`${base(orgId)}/wecom/personal/codes`, {
          method: "POST",
          body: { bot_id: botId },
        }),
      removeWeComPersonal: (orgId: string, botId: string) =>
        http.request<void>(
          `${base(orgId)}/wecom/personal/${encodeURIComponent(botId)}`,
          { method: "DELETE" },
        ),
      setWeComNotificationTarget: (orgId: string, botId: string | null) =>
        http.request<WeComPersonalView>(
          `${base(orgId)}/wecom/personal/notification-target`,
          { method: "PUT", body: { bot_id: botId } },
        ),
      startFeishuRegistration: (
        orgId: string,
        input: FeishuRegistrationInput,
        provider = "feishu",
      ) =>
        http.request<FeishuRegistration>(
          `${base(orgId)}/${provider}/registrations`,
          {
            method: "POST",
            body: input,
          },
        ),
      feishuRegistration: (orgId: string, id: string, provider = "feishu") =>
        http.request<FeishuRegistration>(
          `${base(orgId)}/${provider}/registrations/${encodeURIComponent(id)}`,
        ),
      cancelFeishuRegistration: (
        orgId: string,
        id: string,
        provider = "feishu",
      ) =>
        http.request<FeishuRegistration>(
          `${base(orgId)}/${provider}/registrations/${encodeURIComponent(id)}/cancel`,
          { method: "POST" },
        ),
      updateFeishuBot: (
        orgId: string,
        id: string,
        input: FeishuBotUpdateInput,
        provider = "feishu",
      ) =>
        http.request<FeishuBot>(
          `${base(orgId)}/${provider}/bots/${encodeURIComponent(id)}`,
          { method: "PATCH", body: input },
        ),
      removeFeishuBot: (
        orgId: string,
        id: string,
        version: number,
        provider = "feishu",
      ) =>
        http.request<void>(
          `${base(orgId)}/${provider}/bots/${encodeURIComponent(id)}/remove`,
          { method: "POST", body: { version } },
        ),
      retryFeishuBot: (orgId: string, id: string, provider = "feishu") =>
        http.request<FeishuBot>(
          `${base(orgId)}/${provider}/bots/${encodeURIComponent(id)}/retry`,
          { method: "POST" },
        ),
      // 阶段 6b-2 · GitHub Issue 同步
      repositories: (
        orgId: string,
        connectionId: string,
        query: { q?: string; page?: number },
      ) =>
        http.request<IntegrationRepositories>(
          `${base(orgId)}/connections/${encodeURIComponent(connectionId)}/repositories`,
          { query },
        ),
      createSyncLink: (orgId: string, input: IntegrationSyncLinkCreateInput) =>
        http.request<IntegrationSyncLink>(`${base(orgId)}/sync-links`, {
          method: "POST",
          body: input,
        }),
      updateSyncLink: (
        orgId: string,
        syncLinkId: string,
        input: IntegrationSyncLinkUpdateInput,
      ) =>
        http.request<IntegrationSyncLink>(
          `${base(orgId)}/sync-links/${encodeURIComponent(syncLinkId)}`,
          { method: "PATCH", body: input },
        ),
      removeSyncLink: (orgId: string, syncLinkId: string, version: number) =>
        http.request<void>(
          `${base(orgId)}/sync-links/${encodeURIComponent(syncLinkId)}/remove`,
          { method: "POST", body: { version } },
        ),
      retrySyncLink: (orgId: string, syncLinkId: string) =>
        http.request<{ requeued: number }>(
          `${base(orgId)}/sync-links/${encodeURIComponent(syncLinkId)}/retry`,
          { method: "POST" },
        ),
    },
  };
}
