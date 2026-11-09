import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
  type InfiniteData,
} from "@tanstack/react-query";

import type {
  InboxBulkInput,
  InboxGroup,
  InboxListQuery,
  InboxPage,
  InboxSection,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import { useToast } from "@openfox/ui";

import { keys, useApi } from "../context";

/** 实时事件（inbox.changed）负责及时刷新，轮询只做兜底 */
export const INBOX_POLL_MS = 120_000;

type Pages = InfiniteData<InboxPage, string>;

/** 侧栏未读角标与收件箱页签共用的计数查询。 */
export function useInboxStats(orgId: string) {
  const api = useApi();
  return useQuery({
    queryKey: keys.inboxStats(orgId),
    queryFn: () => api.inbox.stats(orgId),
    refetchInterval: INBOX_POLL_MS,
    refetchOnWindowFocus: true,
  });
}

/** 一个分区的通知组，next_cursor 无限滚动；实时事件触发刷新，另有 120 秒兜底和窗口重新聚焦时刷新。 */
export function useInboxList(orgId: string, params: InboxListQuery) {
  const api = useApi();
  return useInfiniteQuery({
    queryKey: keys.inboxList(orgId, params),
    queryFn: ({ pageParam }) =>
      api.inbox.list(orgId, {
        ...params,
        limit: 50,
        cursor: pageParam || undefined,
      }),
    initialPageParam: "",
    getNextPageParam: (last) => last.next_cursor || undefined,
    refetchInterval: INBOX_POLL_MS,
    refetchOnWindowFocus: true,
  });
}

export type InboxAction =
  "read" | "unread" | "clear" | "unclear" | "snooze" | "unsnooze";

/** 这些操作会把组移出当前分区 */
const LEAVES: ReadonlySet<InboxAction> = new Set([
  "clear",
  "unclear",
  "snooze",
  "unsnooze",
]);

/** 按组操作：乐观更新当前列表，失败回滚并提示；结束后刷新列表与计数。 */
export function useInboxAction(orgId: string) {
  const api = useApi();
  const t = useT();
  const toast = useToast();
  const queryClient = useQueryClient();
  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: keys.inbox(orgId) });
    void queryClient.invalidateQueries({ queryKey: keys.inboxStats(orgId) });
  };
  return useMutation({
    mutationFn: (vars: {
      action: InboxAction;
      section: InboxSection;
      groupIds: string[];
      snoozedUntil?: string;
    }) =>
      api.inbox.action(orgId, {
        action: vars.action,
        section: vars.section,
        group_ids: vars.groupIds,
        ...(vars.snoozedUntil ? { snoozed_until: vars.snoozedUntil } : {}),
      }),
    onMutate: async ({ action, section, groupIds }) => {
      await queryClient.cancelQueries({ queryKey: keys.inbox(orgId) });
      const snapshot = queryClient.getQueriesData<Pages>({
        queryKey: keys.inbox(orgId),
      });
      const ids = new Set(groupIds);
      for (const [key, data] of snapshot) {
        if (!data) continue;
        const params = key[3] as InboxListQuery | undefined;
        const inSection = (params?.section ?? "inbox") === section;
        if (!inSection) continue;
        queryClient.setQueryData<Pages>(key, {
          ...data,
          pages: data.pages.map((page) => ({
            ...page,
            groups: LEAVES.has(action)
              ? page.groups.filter((g) => !ids.has(g.id))
              : page.groups.map((g): InboxGroup =>
                  ids.has(g.id) ? { ...g, unread: action === "unread" } : g,
                ),
          })),
        });
      }
      return { snapshot };
    },
    onError: (_error, _vars, context) => {
      for (const [key, data] of context?.snapshot ?? [])
        queryClient.setQueryData(key, data);
      toast(t("inbox.actionFailed"), { tone: "error" });
    },
    onSettled: refresh,
  });
}

export function useInboxBulk(orgId: string) {
  const api = useApi();
  const t = useT();
  const toast = useToast();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: InboxBulkInput) => api.inbox.bulk(orgId, input),
    onError: () => toast(t("inbox.actionFailed"), { tone: "error" }),
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: keys.inbox(orgId) });
      void queryClient.invalidateQueries({ queryKey: keys.inboxStats(orgId) });
    },
  });
}
