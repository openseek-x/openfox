import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useCallback, useMemo, useRef, useState } from "react";

import {
  isApiError,
  type AgentConfigVersion,
  type ConversationOverrideOptions,
  type ConversationSummary,
} from "@openfox/api";
import { useT, type Translate } from "@openfox/i18n";
import { useToast } from "@openfox/ui";

import { useDevices } from "../agents/queries";
import { useApi } from "../context";
import type { OptionsStatus, OverrideControlsProps } from "./OverrideControls";
import {
  NO_OVERRIDES,
  applyPatch,
  draftRequestBody,
  effectiveOf,
  isNoop,
  optionsFromDevice,
  requestBody,
  type OverridePatch,
  type OverrideSelection,
  type OverrideValues,
} from "./overrides";
import {
  chatErrorMessage,
  invalidateConversation,
  storeConversation,
} from "./queries";

// 输入框里三个覆盖控件的数据与写操作。
// 已有会话：可选项来自只读接口，修改走 PATCH；新对话：所选值先暂存在前端，创建会话后、发第一条消息之前再 PATCH。

const optionsKey = (orgId: string, conversationId: string) =>
  ["override-options", orgId, conversationId] as const;

/** 可选项（Agent 绑定电脑最近一次心跳）；鉴权同修改覆盖，403 表示这个人只能看 */
export function useOverrideOptions(
  orgId: string,
  conversationId: string | null,
  enabled: boolean,
) {
  const api = useApi();
  return useQuery({
    queryKey: optionsKey(orgId, conversationId ?? ""),
    queryFn: () => api.conversations.overrideOptions(orgId, conversationId!),
    enabled: Boolean(orgId && conversationId) && enabled,
    // 心跳最长约 10 分钟刷新一次，不必频繁重读
    staleTime: 30_000,
    retry: false,
  });
}

/** 修改失败的提示：409 版本冲突与 422 选项失效有专门文案，其余沿用对话的通用错误 */
export function overrideErrorMessage(t: Translate, error: unknown): string {
  if (isApiError(error, "VERSION_CONFLICT"))
    return t("chat.overrides.toast.conflict");
  if (
    isApiError(error, "RUNTIME_NOT_AVAILABLE") ||
    isApiError(error, "MODEL_NOT_AVAILABLE") ||
    isApiError(error, "REASONING_EFFORT_NOT_AVAILABLE")
  )
    return t("chat.overrides.toast.unavailable");
  return chatErrorMessage(t, error);
}

const optionsStatusOf = (query: {
  isError: boolean;
  data: unknown;
  isLoading: boolean;
}): OptionsStatus =>
  query.isError ? "error" : query.data ? "ready" : "loading";

export interface ConversationOverridesState {
  /** 控件需要的数据；null 表示这个对话不显示（小队对话、没有默认值） */
  controls: OverrideControlsProps | null;
  /**
   * 新对话：会话刚创建、第一条消息发送之前调用，把暂存的选择写进会话。
   * 失败时抛出 OverridesSaveError（调用方不发送消息，toast 说明原因）。
   */
  applyDraft: (conversationId: string) => Promise<void>;
}

/** 新对话暂存的选择写入失败：对话已建好，用户可以修改后再发 */
export class OverridesSaveError extends Error {
  constructor(readonly reason: unknown) {
    super("conversation overrides could not be saved");
  }
}

/** 新对话：Agent 默认值与电脑（来自 Agent 配置） */
export interface NewConversationDefaults extends OverrideValues {
  computer_id: string;
}

/** Agent 配置版本 → 新对话的默认值（权限模式缺省按「跟随 Runtime」） */
export function newDefaultsOf(
  config: Pick<
    AgentConfigVersion,
    "runtime" | "model" | "approval_mode" | "computer_id"
  >,
): NewConversationDefaults {
  return {
    runtime: config.runtime,
    model: config.model,
    approval_mode: config.approval_mode ?? "FOLLOW_RUNTIME",
    reasoning_effort: "DEFAULT",
    computer_id: config.computer_id,
  };
}

export function useConversationOverrides({
  orgId,
  conversation,
  newDefaults,
  crew,
  closed,
}: {
  orgId: string;
  conversation: ConversationSummary | undefined;
  /** 还没有会话时的 Agent 默认值；缺省时不显示可修改的控件 */
  newDefaults?: NewConversationDefaults | null;
  crew: boolean;
  closed: boolean;
}): ConversationOverridesState {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const queryClient = useQueryClient();
  const conversationId = conversation?.id ?? null;

  // ---------- 已有会话 ----------
  const optionsQuery = useOverrideOptions(
    orgId,
    conversationId,
    !crew && !closed,
  );
  const refreshOptions = useCallback(() => {
    if (conversationId)
      void queryClient.invalidateQueries({
        queryKey: optionsKey(orgId, conversationId),
      });
  }, [queryClient, orgId, conversationId]);

  const update = useMutation({
    mutationFn: ({ patch }: { patch: OverridePatch }) =>
      api.conversations.updateOverrides(
        orgId,
        conversationId!,
        requestBody(patch, conversation!.overrides.version),
      ),
    onSuccess: (summary) => storeConversation(queryClient, orgId, summary),
    onError: (error) => {
      toast(overrideErrorMessage(t, error), { tone: "error" });
      if (isApiError(error, "VERSION_CONFLICT"))
        invalidateConversation(queryClient, orgId, conversationId!);
      else if (
        isApiError(error, "RUNTIME_NOT_AVAILABLE") ||
        isApiError(error, "MODEL_NOT_AVAILABLE") ||
        isApiError(error, "REASONING_EFFORT_NOT_AVAILABLE")
      )
        refreshOptions();
    },
  });

  // ---------- 新对话 ----------
  const [draft, setDraft] = useState<OverrideSelection>(NO_OVERRIDES);
  // 暂存的选择写入成功后会话上的覆盖版本；失败重试时不再用 0，否则会 409
  const draftVersion = useRef(0);
  const draftRef = useRef(draft);
  draftRef.current = draft;
  const devices = useDevices(orgId, !conversation && Boolean(newDefaults));
  const draftOptions = useMemo<ConversationOverrideOptions | null>(() => {
    const device = devices.data?.find(
      (item) => item.id === newDefaults?.computer_id,
    );
    return device ? optionsFromDevice(device) : null;
  }, [devices.data, newDefaults?.computer_id]);

  const applyDraft = useCallback(
    async (id: string) => {
      const body = draftRequestBody(draftRef.current, draftVersion.current);
      if (!body) return;
      try {
        const summary = await api.conversations.updateOverrides(
          orgId,
          id,
          body,
        );
        draftVersion.current = summary.overrides.version;
        storeConversation(queryClient, orgId, summary);
      } catch (error) {
        if (
          isApiError(error, "RUNTIME_NOT_AVAILABLE") ||
          isApiError(error, "MODEL_NOT_AVAILABLE") ||
          isApiError(error, "REASONING_EFFORT_NOT_AVAILABLE")
        )
          void devices.refetch();
        throw new OverridesSaveError(error);
      }
    },
    [api, orgId, queryClient, devices],
  );

  if (crew) return { controls: null, applyDraft };

  if (conversation) {
    const { effective, agent_defaults: defaults } = conversation;
    if (!effective || !defaults) return { controls: null, applyDraft };
    // 能不能改以服务端为准：可选项接口与修改接口鉴权相同，403 = 只能看
    const forbidden =
      isApiError(optionsQuery.error) && optionsQuery.error.status === 403;
    return {
      controls: {
        defaults,
        selection: conversation.overrides,
        effective,
        options: optionsQuery.data ?? null,
        optionsStatus: optionsStatusOf(optionsQuery),
        optionsRefreshing: optionsQuery.isFetching,
        readOnly: closed || forbidden,
        pending: update.isPending,
        onChange: (patch) => {
          if (update.isPending || isNoop(patch, conversation.overrides)) return;
          update.mutate({ patch });
        },
        onRetryOptions: refreshOptions,
      },
      applyDraft,
    };
  }

  if (!newDefaults) return { controls: null, applyDraft };
  const optionsStatus: OptionsStatus = draftOptions
    ? "ready"
    : devices.isLoading
      ? "loading"
      : "error";
  return {
    controls: {
      defaults: newDefaults,
      selection: draft,
      effective: effectiveOf(newDefaults, draft),
      options: draftOptions,
      optionsStatus,
      // 看不到 Agent 绑定的电脑（没有权限）时拿不到可选项：只展示 Agent 默认值
      readOnly: optionsStatus === "error",
      pending: false,
      onChange: (patch) => setDraft((current) => applyPatch(current, patch)),
      onRetryOptions: () => void devices.refetch(),
      optionsRefreshing: devices.isFetching,
    },
    applyDraft,
  };
}
