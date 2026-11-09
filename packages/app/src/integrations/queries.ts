import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useRef } from "react";

import {
  isApiError,
  type FeishuAppInput,
  type FeishuBotUpdateInput,
  type FeishuRegistrationInput,
  type IntegrationSettingsValues,
} from "@openfox/api";
import type { MessageKey, Translate } from "@openfox/i18n";

import { keys, useApi } from "../context";
import { useIMProvider } from "./im-provider";
import {
  BACKFILL_POLL_MS,
  FEISHU_PENDING_POLL_MS,
  FEISHU_POLL_MS,
  OAUTH_POLL_LIMIT_MS,
  OAUTH_POLL_MS,
} from "./model";

// 外部应用集成的查询与断开操作。

export function useIntegrationOverview(orgId: string) {
  const api = useApi();
  return useQuery({
    queryKey: keys.integrationOverview(orgId),
    queryFn: () => api.integrations.list(orgId),
  });
}

export function useIntegrationDetail(orgId: string, provider: string) {
  const api = useApi();
  return useQuery({
    queryKey: keys.integrationDetail(orgId, provider),
    queryFn: () => api.integrations.get(orgId, provider),
    // 有同步关联正在导入已有 Issue 时，每 5 秒刷新一次进度
    refetchInterval: (query) =>
      query.state.data?.sync_links.some((l) => l.backfill.status === "running")
        ? BACKFILL_POLL_MS
        : false,
  });
}

export function useWeComPersonal(orgId: string) {
  const api = useApi();
  return useQuery({
    queryKey: [...keys.integrations(orgId), "wecom-personal"],
    queryFn: () => api.integrations.wecomPersonal(orgId),
    enabled: Boolean(orgId),
  });
}

/** 断开连接：成功后总览与详情一起失效。 */
export function useDisconnectConnection(orgId: string) {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: {
      connectionId: string;
      version: number;
      confirmPhrase?: string;
    }) =>
      api.integrations.disconnect(orgId, input.connectionId, {
        version: input.version,
        ...(input.confirmPhrase ? { confirm_phrase: input.confirmPhrase } : {}),
      }),
    onSettled: () =>
      queryClient.invalidateQueries({ queryKey: keys.integrations(orgId) }),
  });
}

export function integrationErrorText(t: Translate, error: unknown): string {
  if (isApiError(error, "VERSION_CONFLICT"))
    return t("integrations.errors.versionConflict");
  if (isApiError(error, "CONFIRMATION_MISMATCH"))
    return t("integrations.errors.mismatch");
  if (isApiError(error, "FEISHU_APP_NOT_CONNECTED"))
    return t("integrations.feishu.errors.notConnected");
  if (isApiError(error, "FEISHU_UNAVAILABLE"))
    return t("integrations.feishu.errors.unavailable");
  if (isApiError(error, "INVALID_INSTANCE_URL"))
    return t("integrations.gitlab.errors.invalidInstanceUrl");
  if (isApiError(error, "SYNC_LINK_EXISTS"))
    return t("integrations.sync.errors.linkExists");
  if (isApiError(error, "SYNC_BIDIRECTIONAL_EXISTS"))
    return t("integrations.sync.errors.bidirectionalExists");
  if (isApiError(error, "INTEGRATION_NEEDS_REAUTH"))
    return t("integrations.sync.errors.needsReauth");
  if (isApiError(error, "INVALID_REPOSITORY"))
    return t("integrations.sync.errors.invalidRepository");
  if (isApiError(error) && error.status === 502)
    return t("integrations.sync.errors.upstream");
  if (isApiError(error) && error.status === 403)
    return t("integrations.errors.forbidden");
  return t("integrations.errors.generic");
}

/** 绑定 / 连接飞书家族应用时的错误文案（凭证无效、应用已被占用、钉钉不可达等），其余走通用映射。 */
export function feishuSubmitErrorText(t: Translate, error: unknown): string {
  const map: Record<string, MessageKey> = {
    FEISHU_CREDENTIALS_INVALID: "integrations.feishu.errors.credentialsInvalid",
    FEISHU_BOT_UNAVAILABLE: "integrations.feishu.errors.botUnavailable",
    FEISHU_APP_IN_USE: "integrations.feishu.errors.appInUse",
    FEISHU_APP_MISMATCH: "integrations.feishu.errors.appMismatch",
    FEISHU_UNAVAILABLE: "integrations.feishu.errors.unavailable",
    VERSION_CONFLICT: "integrations.feishu.errors.versionConflict",
  };
  if (isApiError(error)) {
    const key = map[error.code];
    if (key) return t(key);
  }
  return integrationErrorText(t, error);
}

// ---------- GitHub ----------

export const DEFAULT_SETTINGS: IntegrationSettingsValues = {
  link_pull_requests: true,
  link_commits: false,
  pr_backlink_private: true,
  pr_backlink_public: false,
  issue_backlink_private: true,
  issue_backlink_public: false,
  branch_format: "username/identifier-title",
};

export function useIntegrationSettings(orgId: string, provider: string) {
  const api = useApi();
  return useQuery({
    queryKey: keys.integrationSettings(orgId, provider),
    queryFn: () => api.integrations.settings(orgId, provider),
    enabled: Boolean(orgId),
    retry: false,
    staleTime: 60_000,
  });
}

/** 保存设置：成功后直接写入缓存；版本冲突时重新拉取。 */
export function usePutIntegrationSettings(orgId: string, provider: string) {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: {
      version: number;
      settings: IntegrationSettingsValues;
    }) => api.integrations.putSettings(orgId, provider, input),
    onSuccess: (data) =>
      queryClient.setQueryData(keys.integrationSettings(orgId, provider), data),
    onError: (error) => {
      if (isApiError(error, "VERSION_CONFLICT"))
        void queryClient.invalidateQueries({
          queryKey: keys.integrationSettings(orgId, provider),
        });
    },
  });
}

export function useIntegrationWebhook(
  orgId: string,
  connectionId: string,
  enabled: boolean,
) {
  const api = useApi();
  return useQuery({
    queryKey: keys.integrationWebhook(orgId, connectionId),
    queryFn: () => api.integrations.webhook(orgId, connectionId),
    enabled,
    retry: false,
  });
}

export function useRotateWebhook(orgId: string, connectionId: string) {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (version: number) =>
      api.integrations.rotateWebhook(orgId, connectionId, version),
    onSuccess: (data) =>
      queryClient.setQueryData(
        keys.integrationWebhook(orgId, connectionId),
        data,
      ),
    onSettled: () =>
      queryClient.invalidateQueries({ queryKey: keys.integrations(orgId) }),
  });
}

export function useCompleteOAuth(orgId: string) {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: { sessionId: string; accounts: string[] }) =>
      api.integrations.completeOAuthSession(
        orgId,
        input.sessionId,
        input.accounts,
      ),
    onSettled: () =>
      queryClient.invalidateQueries({ queryKey: keys.integrations(orgId) }),
  });
}

/** 授权会话：PENDING 时每 2 秒轮询一次，最长 10 分钟。 */
export function useOAuthSession(orgId: string, sessionId: string | null) {
  const api = useApi();
  const startedAt = useRef(0);
  return useQuery({
    queryKey: keys.integrationOAuth(orgId, sessionId ?? ""),
    queryFn: () => {
      if (!startedAt.current) startedAt.current = Date.now();
      return api.integrations.oauthSession(orgId, sessionId!);
    },
    enabled: Boolean(sessionId),
    retry: false,
    gcTime: 0,
    refetchInterval: (query) => {
      const status = query.state.data?.status;
      const expired = Date.now() - startedAt.current > OAUTH_POLL_LIMIT_MS;
      if (expired || (status && status !== "PENDING")) return false;
      return OAUTH_POLL_MS;
    },
  });
}

// ---------- Issue 同步 ----------

/** 某个组织连接下的仓库（搜索词与页码在查询键之外，换连接即失效）。 */
export function useIntegrationRepositories(
  orgId: string,
  connectionId: string,
  q: string,
  page: number,
) {
  const api = useApi();
  return useQuery({
    queryKey: [...keys.integrationRepositories(orgId, connectionId), q, page],
    queryFn: () =>
      api.integrations.repositories(orgId, connectionId, {
        ...(q ? { q } : {}),
        page,
      }),
    enabled: Boolean(connectionId),
    retry: false,
  });
}

/** 同步关联的写操作：成功或失败都刷新集成详情。 */
export function useSyncLinkMutations(orgId: string) {
  const api = useApi();
  const queryClient = useQueryClient();
  const settled = () =>
    queryClient.invalidateQueries({ queryKey: keys.integrations(orgId) });
  return {
    create: useMutation({
      mutationFn: (
        input: Parameters<typeof api.integrations.createSyncLink>[1],
      ) => api.integrations.createSyncLink(orgId, input),
      onSettled: settled,
    }),
    update: useMutation({
      mutationFn: (input: {
        id: string;
        body: Parameters<typeof api.integrations.updateSyncLink>[2];
      }) => api.integrations.updateSyncLink(orgId, input.id, input.body),
      onSettled: settled,
    }),
    remove: useMutation({
      mutationFn: (input: { id: string; version: number }) =>
        api.integrations.removeSyncLink(orgId, input.id, input.version),
      onSettled: settled,
    }),
    retry: useMutation({
      mutationFn: (id: string) => api.integrations.retrySyncLink(orgId, id),
      onSettled: settled,
    }),
  };
}

// ---------- 飞书 ----------

export function useFeishuApp(orgId: string) {
  const api = useApi();
  const provider = useIMProvider();
  return useQuery({
    queryKey: keys.feishuApp(orgId, provider),
    queryFn: () => api.integrations.feishuApp(orgId, provider),
    enabled: Boolean(orgId),
    refetchInterval: (query) =>
      query.state.data?.app?.status === "PENDING"
        ? FEISHU_PENDING_POLL_MS
        : FEISHU_POLL_MS,
  });
}

/** 飞书写操作：成功或失败都让飞书详情、通用详情与总览失效（同在 integrations 前缀下）。 */
export function useFeishuMutations(orgId: string) {
  const api = useApi();
  const provider = useIMProvider();
  const queryClient = useQueryClient();
  const settled = () =>
    queryClient.invalidateQueries({ queryKey: keys.integrations(orgId) });
  return {
    put: useMutation({
      mutationFn: (input: FeishuAppInput) =>
        api.integrations.putFeishuApp(orgId, input, provider),
      onSettled: settled,
    }),
    verify: useMutation({
      mutationFn: () => api.integrations.verifyFeishuApp(orgId, provider),
      onSettled: settled,
    }),
    setLinkPreview: useMutation({
      mutationFn: (input: { link_preview_enabled: boolean; version: number }) =>
        api.integrations.patchFeishuAppSettings(orgId, input, provider),
      onSettled: settled,
    }),
    authorize: useMutation({
      mutationFn: () =>
        api.integrations.startFeishuAuthorization(orgId, provider),
    }),
  };
}

// ---------- 飞书任务助手与 Agent 机器人 ----------

/** 机器人列表：有连接中的项时 5 秒刷新，否则 30 秒。 */
export function useFeishuBots(orgId: string) {
  const api = useApi();
  const provider = useIMProvider();
  return useQuery({
    queryKey: keys.feishuBots(orgId, provider),
    queryFn: () => api.integrations.feishuBots(orgId, provider),
    enabled: Boolean(orgId),
    refetchInterval: (query) => {
      const view = query.state.data;
      const connecting =
        view?.agents.some((bot) => bot.status === "CONNECTING") ||
        view?.assistants.some((row) => row.bot?.status === "CONNECTING");
      return connecting ? FEISHU_PENDING_POLL_MS : FEISHU_POLL_MS;
    },
  });
}

/** 机器人写操作：成功或失败都刷新飞书相关查询。 */
export function useFeishuBotMutations(orgId: string) {
  const api = useApi();
  const provider = useIMProvider();
  const queryClient = useQueryClient();
  const settled = () =>
    queryClient.invalidateQueries({ queryKey: keys.integrations(orgId) });
  return {
    update: useMutation({
      mutationFn: (input: { id: string; body: FeishuBotUpdateInput }) =>
        api.integrations.updateFeishuBot(orgId, input.id, input.body, provider),
      onSettled: settled,
    }),
    remove: useMutation({
      mutationFn: (input: { id: string; version: number }) =>
        api.integrations.removeFeishuBot(
          orgId,
          input.id,
          input.version,
          provider,
        ),
      onSettled: settled,
    }),
    retry: useMutation({
      mutationFn: (id: string) =>
        api.integrations.retryFeishuBot(orgId, id, provider),
      onSettled: settled,
    }),
    start: useMutation({
      mutationFn: (input: FeishuRegistrationInput) =>
        api.integrations.startFeishuRegistration(orgId, input, provider),
    }),
  };
}

/** 扫码会话：PENDING 时按服务端建议的间隔轮询（最短 1 秒），终态后停止。 */
export function useFeishuRegistration(orgId: string, id: string | null) {
  const api = useApi();
  const provider = useIMProvider();
  return useQuery({
    queryKey: ["org", orgId, "integrations", "feishu-registration", id ?? ""],
    queryFn: () => api.integrations.feishuRegistration(orgId, id!, provider),
    enabled: Boolean(id),
    retry: false,
    gcTime: 0,
    refetchInterval: (query) => {
      const data = query.state.data;
      if (!data) return 2000;
      if (data.status !== "PENDING") return false;
      return Math.max(1, data.poll_interval || 2) * 1000;
    },
  });
}

// ---------- 微信（ClawBot / iLink）扫码绑定 Agent ----------

const WECHAT_POLL_MS = 15_000;

export function useWeChatBindings(orgId: string) {
  const api = useApi();
  return useQuery({
    queryKey: keys.wechatBindings(orgId),
    queryFn: () => api.integrations.wechatBindings(orgId),
    enabled: Boolean(orgId),
    refetchInterval: WECHAT_POLL_MS,
  });
}

export function useRemoveWeChatBinding(orgId: string) {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: { id: string; version: number }) =>
      api.integrations.removeWeChatBinding(orgId, input.id, input.version),
    onSettled: () =>
      queryClient.invalidateQueries({ queryKey: keys.integrations(orgId) }),
  });
}
