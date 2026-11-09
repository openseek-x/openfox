import {
  useIsMutating,
  useMutation,
  useQuery,
  useQueryClient,
  type QueryClient,
} from "@tanstack/react-query";
import { useEffect, useState } from "react";

import {
  isApiError,
  type BetaFeature,
  type Me,
  type NotificationChannel,
  type NotificationChannelName,
  type NotificationEvent,
  type PersonalAccessTokenInput,
  type UserPreferences,
  type UserPreferencesPatch,
} from "@openfox/api";
import type { Translate } from "@openfox/i18n";

import { keys, useApi } from "../context";

// 个人资料与偏好的查询和写入。

export const PREFERENCES_MUTATION_KEY = ["me", "preferences", "update"];

export function useMyPreferences() {
  const api = useApi();
  return useQuery({
    queryKey: keys.preferences,
    queryFn: api.me.preferences.get,
  });
}

/** 偏好保存中：页面用它禁用同组控件，避免并发写入造成 VERSION_CONFLICT。 */
export function usePreferencesSaving(): boolean {
  return useIsMutating({ mutationKey: PREFERENCES_MUTATION_KEY }) > 0;
}

/** 发送方式：偏好未加载时按默认的 Enter 发送。 */
export function useSendShortcut(): UserPreferences["send_shortcut"] {
  return useMyPreferences().data?.send_shortcut ?? "enter";
}

type PreferencesPatch = Omit<UserPreferencesPatch, "version">;

/**
 * 修改偏好：带当前 version；先乐观更新（主题、开关即时生效），失败回滚，成功用响应更新缓存。
 * 失败时作废缓存，让界面以服务端为准。
 */
export function useUpdatePreferences() {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationKey: PREFERENCES_MUTATION_KEY,
    mutationFn: (patch: PreferencesPatch) => {
      const current = queryClient.getQueryData<UserPreferences>(
        keys.preferences,
      );
      return api.me.preferences.update({
        ...patch,
        version: current?.version ?? 0,
      });
    },
    onMutate: async (patch) => {
      await queryClient.cancelQueries({ queryKey: keys.preferences });
      const previous = queryClient.getQueryData<UserPreferences>(
        keys.preferences,
      );
      if (previous)
        queryClient.setQueryData<UserPreferences>(keys.preferences, {
          ...previous,
          ...patch,
          density: patch.density ?? previous.density,
        });
      return { previous };
    },
    onError: (_error, _patch, context) => {
      if (context?.previous)
        queryClient.setQueryData(keys.preferences, context.previous);
      void queryClient.invalidateQueries({ queryKey: keys.preferences });
    },
    onSuccess: (preferences) =>
      queryClient.setQueryData(keys.preferences, preferences),
  });
}

/** 个人资料变化后刷新当前用户与本组织成员缓存（成员列表显示全名与头像）。 */
export function refreshProfile(
  queryClient: QueryClient,
  orgId: string,
  me: Me,
): void {
  queryClient.setQueryData(keys.me, me);
  void queryClient.invalidateQueries({ queryKey: keys.members(orgId) });
}

export function useUpdateProfile(orgId: string) {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (patch: { full_name?: string; bio?: string }) =>
      api.me.update(patch),
    onSuccess: (me) => refreshProfile(queryClient, orgId, me),
  });
}

export function useUploadAvatar(orgId: string) {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (file: File) => api.me.uploadAvatar(file),
    onSuccess: (me) => refreshProfile(queryClient, orgId, me),
  });
}

/** 修改邮箱各步的错误文案：已知错误码有专门提示，其余用通用文案。 */
export function emailErrorMessage(t: Translate, error: unknown): string {
  if (isApiError(error, "EMAIL_TAKEN"))
    return t("me.profile.errors.emailTaken");
  if (isApiError(error, "CODE_INVALID"))
    return t("me.profile.errors.codeInvalid");
  if (isApiError(error, "INVALID_EMAIL"))
    return t("me.profile.errors.emailInvalid");
  if (isApiError(error) && error.status === 429)
    return t("me.profile.errors.rateLimited");
  return t("me.profile.errors.generic");
}

/** 距离 target（毫秒时间戳）还剩几秒，每秒刷新；没有目标或已到点时为 0。 */
export function useCountdown(target: number | null): number {
  const [, setTick] = useState(0);
  useEffect(() => {
    if (target === null || target <= Date.now()) return;
    const timer = window.setInterval(() => setTick((n) => n + 1), 1000);
    return () => window.clearInterval(timer);
  }, [target]);
  if (target === null) return 0;
  return Math.max(0, Math.ceil((target - Date.now()) / 1000));
}

// 个人访问令牌、通知渠道、内测功能与开始使用进度。

/** 账号是否可见内测功能（access_tier 为 beta / internal）；账号未加载时为 undefined。 */
export function useBetaTier(): boolean | undefined {
  const api = useApi();
  const me = useQuery({ queryKey: keys.me, queryFn: api.me.get });
  if (!me.data) return undefined;
  const tier = me.data.access_tier;
  return tier === "beta" || tier === "internal";
}

export function useMyTokens() {
  const api = useApi();
  return useQuery({
    queryKey: keys.tokens,
    queryFn: api.me.tokens.list,
  });
}

export function useCreateToken() {
  const api = useApi();
  return useMutation({
    mutationFn: (input: PersonalAccessTokenInput) =>
      api.me.tokens.create(input),
  });
}

export function useRevokeToken() {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (tokenId: string) => api.me.tokens.revoke(tokenId),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: keys.tokens }),
  });
}

export const NOTIFICATION_MUTATION_KEY = [
  "me",
  "notification-channels",
  "save",
];

export function useNotificationChannels() {
  const api = useApi();
  return useQuery({
    queryKey: keys.notificationChannels,
    queryFn: api.me.notificationChannels.list,
  });
}

/** 通知设置保存中：页面禁用所有开关，同一时刻只允许一项保存。 */
export function useNotificationSaving(): boolean {
  return useIsMutating({ mutationKey: NOTIFICATION_MUTATION_KEY }) > 0;
}

function replaceChannel(
  list: NotificationChannel[] | undefined,
  channel: NotificationChannel,
): NotificationChannel[] | undefined {
  return list?.map((item) =>
    item.channel === channel.channel ? channel : item,
  );
}

/** 保存一个渠道的总开关与关闭的事件类型（整体替换）；先乐观更新，失败回滚。 */
export function useSaveChannel() {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationKey: NOTIFICATION_MUTATION_KEY,
    mutationFn: (input: {
      channel: NotificationChannelName;
      enabled: boolean;
      disabled_events: NotificationEvent[];
      direct_messages?: boolean;
    }) =>
      api.me.notificationChannels.update(input.channel, {
        enabled: input.enabled,
        disabled_events: input.disabled_events,
        ...(input.direct_messages === undefined
          ? {}
          : { direct_messages: input.direct_messages }),
      }),
    onMutate: async (input) => {
      await queryClient.cancelQueries({ queryKey: keys.notificationChannels });
      const previous = queryClient.getQueryData<NotificationChannel[]>(
        keys.notificationChannels,
      );
      queryClient.setQueryData<NotificationChannel[]>(
        keys.notificationChannels,
        replaceChannel(previous, {
          ...previous?.find((item) => item.channel === input.channel),
          channel: input.channel,
          available:
            previous?.find((item) => item.channel === input.channel)
              ?.available ?? true,
          enabled: input.enabled,
          disabled_events: input.disabled_events,
          ...(input.direct_messages === undefined
            ? {}
            : { direct_messages: input.direct_messages }),
          customized: true,
        }),
      );
      return { previous };
    },
    onError: (_error, _input, context) => {
      queryClient.setQueryData(keys.notificationChannels, context?.previous);
      void queryClient.invalidateQueries({
        queryKey: keys.notificationChannels,
      });
    },
    onSuccess: (channel) =>
      queryClient.setQueryData<NotificationChannel[]>(
        keys.notificationChannels,
        replaceChannel(
          queryClient.getQueryData<NotificationChannel[]>(
            keys.notificationChannels,
          ),
          channel,
        ),
      ),
  });
}

/** 恢复默认设置（开启、全部事件）。 */
export function useResetChannel() {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationKey: NOTIFICATION_MUTATION_KEY,
    mutationFn: (channel: NotificationChannelName) =>
      api.me.notificationChannels.reset(channel),
    onSuccess: (channel) =>
      queryClient.setQueryData<NotificationChannel[]>(
        keys.notificationChannels,
        replaceChannel(
          queryClient.getQueryData<NotificationChannel[]>(
            keys.notificationChannels,
          ),
          channel,
        ),
      ),
  });
}

const BETA_MUTATION_KEY = ["me", "beta-features", "save"];

export function useBetaFeatures() {
  const api = useApi();
  return useQuery({
    queryKey: keys.betaFeatures,
    queryFn: api.me.betaFeatures.list,
  });
}

/** 打开或关闭一个内测功能：先乐观翻转，失败回滚。 */
export function useSaveBetaFeature() {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationKey: BETA_MUTATION_KEY,
    mutationFn: (input: { key: string; enabled: boolean }) =>
      api.me.betaFeatures.update(input.key, input.enabled),
    onMutate: async (input) => {
      await queryClient.cancelQueries({ queryKey: keys.betaFeatures });
      const previous = queryClient.getQueryData<BetaFeature[]>(
        keys.betaFeatures,
      );
      queryClient.setQueryData<BetaFeature[]>(
        keys.betaFeatures,
        previous?.map((item) =>
          item.key === input.key ? { ...item, enabled: input.enabled } : item,
        ),
      );
      return { previous };
    },
    onError: (_error, _input, context) => {
      queryClient.setQueryData(keys.betaFeatures, context?.previous);
      void queryClient.invalidateQueries({ queryKey: keys.betaFeatures });
    },
  });
}

export function useGetStarted(orgId: string) {
  const api = useApi();
  return useQuery({
    queryKey: keys.getStarted(orgId),
    queryFn: () => api.getStarted.progress(orgId),
  });
}
