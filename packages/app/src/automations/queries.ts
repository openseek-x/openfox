import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";

import {
  isApiError,
  type Automation,
  type AutomationInput,
  type AutomationRun,
  type AutomationSaveResult,
  type AutomationTriggerInput,
  type AutomationUpdateInput,
  type WebhookDelivery,
} from "@openfox/api";
import { useT, type MessageKey, type Translate } from "@openfox/i18n";
import { useToast } from "@openfox/ui";

import { keys, useApi } from "../context";

// 自动化查询与写操作。写操作成功后整体失效本空间的自动化前缀（列表、详情、运行与投递）。

export function useAutomations(orgId: string, spaceId: string) {
  const api = useApi();
  return useQuery({
    queryKey: keys.automations(orgId, spaceId),
    queryFn: () => api.automations.list(orgId, spaceId),
    enabled: Boolean(spaceId),
  });
}

export function useAutomation(
  orgId: string,
  spaceId: string,
  automationId: string,
) {
  const api = useApi();
  return useQuery({
    queryKey: keys.automation(orgId, spaceId, automationId),
    queryFn: () => api.automations.get(orgId, spaceId, automationId),
    enabled: Boolean(spaceId && automationId),
  });
}

/** 运行记录按游标分页，每页 50 条，「加载更多」沿用 next_cursor。 */
export function useAutomationRuns(
  orgId: string,
  spaceId: string,
  automationId: string,
) {
  const api = useApi();
  return useInfiniteQuery({
    queryKey: keys.automationRuns(orgId, spaceId, automationId),
    queryFn: ({ pageParam }) =>
      api.automations.runs(
        orgId,
        spaceId,
        automationId,
        pageParam || undefined,
      ),
    initialPageParam: "",
    getNextPageParam: (last) => last.next_cursor || undefined,
    enabled: Boolean(spaceId && automationId),
  });
}

export function useWebhookDeliveries(
  orgId: string,
  spaceId: string,
  automationId: string,
) {
  const api = useApi();
  return useInfiniteQuery({
    queryKey: keys.automationDeliveries(orgId, spaceId, automationId),
    queryFn: ({ pageParam }) =>
      api.automations.deliveries(
        orgId,
        spaceId,
        automationId,
        pageParam || undefined,
      ),
    initialPageParam: "",
    getNextPageParam: (last) => last.next_cursor || undefined,
    enabled: Boolean(spaceId && automationId),
  });
}

/** 投递详情：只在弹窗打开时拉取。 */
export function useWebhookDelivery(
  orgId: string,
  spaceId: string,
  automationId: string,
  deliveryId: string | null,
) {
  const api = useApi();
  return useQuery({
    queryKey: [
      ...keys.automationDeliveries(orgId, spaceId, automationId),
      deliveryId,
    ],
    queryFn: () =>
      api.automations.delivery(orgId, spaceId, automationId, deliveryId!),
    enabled: Boolean(deliveryId),
  });
}

/** 运行详情：只在抽屉打开时拉取。 */
export function useAutomationRunDetail(
  orgId: string,
  spaceId: string,
  automationId: string,
  runId: string | null,
) {
  const api = useApi();
  return useQuery({
    queryKey: [...keys.automationRuns(orgId, spaceId, automationId), runId],
    queryFn: () =>
      api.automations.runDetail(orgId, spaceId, automationId, runId!),
    enabled: Boolean(runId),
  });
}

/** 签名密钥只在用户显示或开启签名时读取，不随编辑页常驻缓存。 */
export function useWebhookSigningSecret(
  orgId: string,
  spaceId: string,
  automationId: string,
  triggerId: string,
  enabled: boolean,
) {
  const api = useApi();
  return useQuery({
    queryKey: keys.webhookSecret(orgId, automationId, triggerId),
    queryFn: async () =>
      (
        await api.automations.signingSecret(
          orgId,
          spaceId,
          automationId,
          triggerId,
        )
      ).secret,
    enabled,
    retry: false,
    gcTime: 0,
  });
}

/** 写操作的错误码有专门提示，其余沿用调用方给出的通用文案。 */
const AUTOMATION_ERRORS: Record<string, MessageKey> = {
  AUTOMATION_DISABLED: "automations.errors.AUTOMATION_DISABLED",
  SECRETS_UNAVAILABLE: "automations.errors.SECRETS_UNAVAILABLE",
  NOT_REPLAYABLE: "automations.errors.NOT_REPLAYABLE",
  VERSION_CONFLICT: "automations.errors.VERSION_CONFLICT",
  AGENT_DIRECT_CHAT_DISABLED: "automations.errors.AGENT_DIRECT_CHAT_DISABLED",
};

export function automationErrorMessage(
  t: Translate,
  error: unknown,
  fallback: MessageKey,
): string {
  const key = isApiError(error) ? AUTOMATION_ERRORS[error.code] : undefined;
  return t(key ?? fallback);
}

/** 写操作成功后整体失效空间的自动化查询。 */
function useAutomationWrite<T, R>(
  orgId: string,
  spaceId: string,
  mutationFn: (value: T) => Promise<R>,
) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn,
    onSettled: () =>
      void queryClient.invalidateQueries({
        queryKey: keys.automations(orgId, spaceId),
      }),
  });
}

/** 新建或编辑（编辑带上当前 version 做乐观锁）。 */
export function useSaveAutomation(
  orgId: string,
  spaceId: string,
  automationId?: string,
) {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: AutomationInput | AutomationUpdateInput) =>
      automationId
        ? api.automations.update(
            orgId,
            spaceId,
            automationId,
            input as AutomationUpdateInput,
          )
        : api.automations.create(orgId, spaceId, input as AutomationInput),
    onSuccess: (result: AutomationSaveResult) => {
      queryClient.setQueryData(
        keys.automation(orgId, spaceId, result.automation.id),
        result.automation,
      );
    },
    onSettled: () =>
      void queryClient.invalidateQueries({
        queryKey: keys.automations(orgId, spaceId),
      }),
  });
}

/** 启停：详情页直接切换，卡片先确认后再调用。 */
export function useToggleAutomation(orgId: string, spaceId: string) {
  const api = useApi();
  return useAutomationWrite(
    orgId,
    spaceId,
    ({ automation, enabled }: { automation: Automation; enabled: boolean }) =>
      api.automations.update(orgId, spaceId, automation.id, {
        version: automation.version,
        enabled,
      }),
  );
}

/** 详情页的触发器开关：整体替换触发器列表，其他触发器保持原样（webhook 的 URL 与密钥不变）。 */
export function useToggleTrigger(orgId: string, spaceId: string) {
  const api = useApi();
  return useAutomationWrite(
    orgId,
    spaceId,
    ({
      automation,
      triggers,
    }: {
      automation: Automation;
      triggers: AutomationTriggerInput[];
    }) =>
      api.automations.update(orgId, spaceId, automation.id, {
        version: automation.version,
        triggers,
      }),
  );
}

export function useDeleteAutomation(orgId: string, spaceId: string) {
  const api = useApi();
  return useAutomationWrite(orgId, spaceId, (automation: Automation) =>
    api.automations.remove(orgId, spaceId, automation),
  );
}

export function useRunAutomation(orgId: string, spaceId: string) {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (automationId: string) =>
      api.automations.run(orgId, spaceId, automationId),
    onSuccess: (_run: AutomationRun, automationId) => {
      // 新的运行记录立即出现在运行列表里
      void queryClient.invalidateQueries({
        queryKey: keys.automationRuns(orgId, spaceId, automationId),
      });
      void queryClient.invalidateQueries({
        queryKey: keys.automation(orgId, spaceId, automationId),
      });
    },
  });
}

/** 调整顺序：乐观更新列表缓存，失败回滚并提示。 */
export function useReorderAutomations(orgId: string, spaceId: string) {
  const api = useApi();
  const queryClient = useQueryClient();
  const toast = useToast();
  const t = useT();
  return useMutation({
    mutationFn: (ids: string[]) => api.automations.reorder(orgId, spaceId, ids),
    onMutate: async (ids: string[]) => {
      await queryClient.cancelQueries({
        queryKey: keys.automations(orgId, spaceId),
      });
      const previous = queryClient.getQueryData<Automation[]>(
        keys.automations(orgId, spaceId),
      );
      if (previous) {
        const byId = new Map(previous.map((item) => [item.id, item]));
        queryClient.setQueryData<Automation[]>(
          keys.automations(orgId, spaceId),
          ids.flatMap((id, position) => {
            const item = byId.get(id);
            return item ? [{ ...item, position }] : [];
          }),
        );
      }
      return { previous };
    },
    onError: (_error, _ids, context) => {
      if (context?.previous)
        queryClient.setQueryData(
          keys.automations(orgId, spaceId),
          context.previous,
        );
      toast(t("automations.reorderFailed"), { tone: "error" });
    },
    onSettled: () =>
      void queryClient.invalidateQueries({
        queryKey: keys.automations(orgId, spaceId),
      }),
  });
}

export function useRegenerateWebhookURL(orgId: string, spaceId: string) {
  const api = useApi();
  return useMutation({
    mutationFn: ({
      automationId,
      triggerId,
    }: {
      automationId: string;
      triggerId: string;
    }) =>
      api.automations.regenerateWebhookURL(
        orgId,
        spaceId,
        automationId,
        triggerId,
      ),
  });
}

/** 生成或重置签名密钥；成功后把密钥写入缓存，供显示与复制。 */
export function useResetWebhookSecret(
  orgId: string,
  spaceId: string,
  automationId: string,
  triggerId: string,
) {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () =>
      api.automations.resetSigningSecret(
        orgId,
        spaceId,
        automationId,
        triggerId,
      ),
    onSuccess: ({ secret }) => {
      queryClient.setQueryData(
        keys.webhookSecret(orgId, automationId, triggerId),
        secret,
      );
    },
  });
}

export function useReplayDelivery(
  orgId: string,
  spaceId: string,
  automationId: string,
) {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (deliveryId: string) =>
      api.automations.replayDelivery(orgId, spaceId, automationId, deliveryId),
    onSuccess: (_delivery: WebhookDelivery) =>
      void queryClient.invalidateQueries({
        queryKey: keys.automationDeliveries(orgId, spaceId, automationId),
      }),
  });
}
