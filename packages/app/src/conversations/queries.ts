import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
  type InfiniteData,
  type QueryClient,
} from "@tanstack/react-query";

import {
  isApiError,
  type ConversationListQuery,
  type ConversationMessage,
  type ConversationPage,
  type ConversationSummary,
  type ConversationWorkspaceSelection,
  type MessagePage,
} from "@openfox/api";
import { useT, type MessageKey, type Translate } from "@openfox/i18n";
import { useToast } from "@openfox/ui";

import { keys, useApi } from "../context";
import { useMessagePages } from "./messageWindow";
import { activeRunId, mergeConversationPages } from "./model";

// 对话页签、对话页、小助手共用的查询与写操作。
// 置顶 / 已读 / 重命名 / 归档不产生 conversation.updated 事件（或不改 version），写成功后由这里直接更新缓存。

const errorMessages: Record<string, MessageKey> = {
  ASSISTANT_TRANSFER_NOT_READY: "chat.assistant.transferNotReady",
  ASSISTANT_TRANSFER_BUSY: "chat.assistant.transferBusy",
  RUN_ACTIVE: "chat.errors.runActive",
  CONVERSATION_TASK_BOUND: "chat.errors.taskBound",
  TASK_SPACE_MISMATCH: "chat.errors.taskSpaceMismatch",
  VERSION_CONFLICT: "chat.errors.versionConflict",
  CONVERSATION_CLOSED: "chat.errors.closed",
  IDEMPOTENCY_CONFLICT: "chat.errors.idempotencyConflict",
  NOT_FOUND: "chat.errors.notFound",
  PROPOSAL_RESOLVED: "chat.errors.proposalResolved",
  WORKSPACE_UNAVAILABLE: "chat.workspace.notAllowed",
  WORKSPACE_SELECTION_UNSUPPORTED: "chat.workspace.unsupported",
  NO_ASSISTANT_SPACE: "chat.errors.noAssistantSpace",
  ACTION_FORBIDDEN: "chat.errors.forbidden",
};

/** A timed-out creation can already exist; a changed working directory is a different draft. */
export function sameWorkspaceSelection(
  left?: ConversationWorkspaceSelection,
  right?: ConversationWorkspaceSelection,
) {
  return (
    left?.mode === right?.mode &&
    left?.directory === right?.directory &&
    left?.source_conversation_id === right?.source_conversation_id &&
    left?.base_ref === right?.base_ref
  );
}

export function chatErrorMessage(t: Translate, error: unknown): string {
  if (!isApiError(error)) return t("chat.errors.generic");
  const key = errorMessages[error.code];
  if (key) return t(key);
  if (error.uncertain) return t("chat.errors.uncertain");
  if (error.status === 403) return t("chat.errors.forbidden");
  return t("chat.errors.generic");
}

/** 会话列表单页条数；侧栏全部模式与首页「对话」分组共用第一页 */
export const CONVERSATION_PAGE_SIZE = 50;

type ListData = InfiniteData<ConversationPage, string | null>;
type MessageData = InfiniteData<MessagePage, number>;

/**
 * 会话列表（未归档，最近优先）。params 为额外筛选（agent_id 等）；同一参数共用缓存。
 * 刷新失败时 TanStack 保留旧数据，界面据 isError + data 显示「刷新失败」而不清空列表。
 */
export function useConversations(
  orgId: string,
  params: Omit<ConversationListQuery, "cursor" | "limit"> = {},
  enabled = true,
) {
  const api = useApi();
  const query = useInfiniteQuery({
    queryKey: keys.conversationList(orgId, params),
    queryFn: ({ pageParam }) =>
      api.conversations.page(orgId, {
        ...params,
        limit: CONVERSATION_PAGE_SIZE,
        cursor: pageParam ?? undefined,
      }),
    initialPageParam: null as string | null,
    getNextPageParam: (last) => last.next_cursor ?? undefined,
    enabled: Boolean(orgId) && enabled,
  });
  const list = query.data ? mergeConversationPages(query.data.pages) : [];
  return { ...query, list };
}

export function useConversation(orgId: string, conversationId: string | null) {
  const api = useApi();
  return useQuery({
    queryKey: keys.conversation(orgId, conversationId ?? ""),
    queryFn: () => api.conversations.get(orgId, conversationId!),
    enabled: Boolean(orgId && conversationId),
    // 运行中兜底轮询（主要靠实时事件失效）
    refetchInterval: (query) =>
      activeRunId(query.state.data) ? 15_000 : false,
  });
}

/** 消息分页：pages[0] 为最新一页，翻页参数是 before（页内最小 seq） */
export function useMessages(
  orgId: string,
  conversationId: string | null,
  target: string | null = null,
) {
  const api = useApi();
  return useMessagePages(
    keys.conversationMessages(orgId, conversationId ?? ""),
    Boolean(orgId && conversationId),
    target,
    (cursor) =>
      typeof cursor === "number"
        ? api.conversations.messagesBefore(orgId, conversationId!, cursor)
        : api.conversations.messageWindow(orgId, conversationId!, cursor),
  );
}

// ---------- 缓存工具 ----------

/** 在所有列表缓存与详情缓存里更新同一个会话；updater 返回 null 表示从列表移除 */
export function patchConversation(
  queryClient: QueryClient,
  orgId: string,
  conversationId: string,
  updater: (item: ConversationSummary) => ConversationSummary | null,
) {
  queryClient.setQueriesData<ListData>(
    { queryKey: keys.conversationLists(orgId) },
    (data) =>
      data && {
        ...data,
        pages: data.pages.map((page) => ({
          ...page,
          conversations: page.conversations.flatMap((item) => {
            if (item.id !== conversationId) return [item];
            const next = updater(item);
            return next ? [next] : [];
          }),
        })),
      },
  );
  queryClient.setQueryData<ConversationSummary>(
    keys.conversation(orgId, conversationId),
    (old) => (old ? (updater(old) ?? old) : old),
  );
}

/** 写回服务端返回的最新摘要（列表里没有时插到第一页最前） */
export function storeConversation(
  queryClient: QueryClient,
  orgId: string,
  summary: ConversationSummary,
) {
  queryClient.setQueryData(keys.conversation(orgId, summary.id), summary);
  queryClient.setQueriesData<ListData>(
    { queryKey: keys.conversationLists(orgId) },
    (data) => {
      if (!data) return data;
      const exists = data.pages.some((page) =>
        page.conversations.some((item) => item.id === summary.id),
      );
      return {
        ...data,
        pages: data.pages.map((page, index) => ({
          ...page,
          conversations: exists
            ? page.conversations.map((item) =>
                item.id === summary.id ? summary : item,
              )
            : index === 0
              ? [summary, ...page.conversations]
              : page.conversations,
        })),
      };
    },
  );
}

/** 刷新会话相关缓存：列表 + 该会话详情（+ 消息） */
export function invalidateConversation(
  queryClient: QueryClient,
  orgId: string,
  conversationId?: string,
  messages = false,
) {
  void queryClient.invalidateQueries({
    queryKey: keys.conversationLists(orgId),
  });
  if (!conversationId) return;
  void queryClient.invalidateQueries({
    queryKey: keys.conversation(orgId, conversationId),
  });
  if (messages)
    void queryClient.invalidateQueries({
      queryKey: keys.conversationMessages(orgId, conversationId),
    });
  void queryClient.invalidateQueries({
    queryKey: keys.assistantSessions(orgId),
  });
}

/** 把刚发出的消息追加到最新一页（真正的 run_id 等刷新后由列表接口给出） */
export function appendMessage(
  queryClient: QueryClient,
  orgId: string,
  conversationId: string,
  message: ConversationMessage,
) {
  queryClient.setQueryData<MessageData>(
    keys.conversationMessages(orgId, conversationId),
    (data) => {
      if (!data || data.pages.length === 0)
        return {
          pages: [{ messages: [message], next_cursor: 0 }],
          pageParams: [0],
        };
      const [first, ...rest] = data.pages;
      if (first!.messages.some((m) => m.id === message.id)) return data;
      return {
        ...data,
        pages: [
          { ...first!, messages: [...first!.messages, message] },
          ...rest,
        ],
      };
    },
  );
}

// ---------- 写操作 ----------

/** 列表行与对话页头部共用的会话操作。失败时提示并刷新，成功后直接写缓存。 */
export function useConversationActions(orgId: string) {
  const api = useApi();
  const t = useT();
  const toast = useToast();
  const queryClient = useQueryClient();
  const fail = (error: unknown, conversationId?: string) => {
    toast(chatErrorMessage(t, error), { tone: "error" });
    invalidateConversation(queryClient, orgId, conversationId);
  };

  const rename = useMutation({
    mutationFn: ({ id, title }: { id: string; title: string }) =>
      api.conversations.update(orgId, id, { title: title.trim() || null }),
    onSuccess: (summary) => {
      storeConversation(queryClient, orgId, summary);
      void queryClient.invalidateQueries({
        queryKey: keys.assistantSessions(orgId),
      });
      toast(t("chat.toast.renamed"));
    },
    onError: (error, { id }) => fail(error, id),
  });

  const pin = useMutation({
    mutationFn: ({ id, pinned }: { id: string; pinned: boolean }) =>
      api.conversations.pin(orgId, id, pinned),
    onMutate: ({ id, pinned }) =>
      patchConversation(queryClient, orgId, id, (item) => ({
        ...item,
        pinned,
      })),
    onSuccess: (_void, { pinned }) =>
      toast(t(pinned ? "chat.toast.pinned" : "chat.toast.unpinned")),
    onError: (error, { id, pinned }) => {
      patchConversation(queryClient, orgId, id, (item) => ({
        ...item,
        pinned: !pinned,
      }));
      fail(error, id);
    },
  });

  const restore = useMutation({
    mutationFn: (id: string) =>
      api.conversations.update(orgId, id, { archived: false }),
    onSuccess: (summary) => {
      storeConversation(queryClient, orgId, summary);
      invalidateConversation(queryClient, orgId, summary.id);
      toast(t("chat.toast.restored"));
    },
    onError: (error, id) => fail(error, id),
  });

  /** 归档后从列表移除，toast 提供撤销 */
  const archive = useMutation({
    mutationFn: (id: string) =>
      api.conversations.update(orgId, id, { archived: true }),
    onSuccess: (summary) => {
      patchConversation(queryClient, orgId, summary.id, () => null);
      queryClient.setQueryData(keys.conversation(orgId, summary.id), summary);
      void queryClient.invalidateQueries({
        queryKey: keys.assistantSessions(orgId),
      });
      toast(t("chat.toast.archived"), {
        duration: 6000,
        action: {
          label: t("chat.toast.undo"),
          onClick: () => restore.mutate(summary.id),
        },
      });
    },
    onError: (error, id) => fail(error, id),
  });

  const complete = useMutation({
    mutationFn: ({ id, version }: { id: string; version: number }) =>
      api.conversations.complete(orgId, id, version),
    onSuccess: (conversation) => {
      invalidateConversation(queryClient, orgId, conversation.id);
      toast(t("chat.toast.completed"));
    },
    onError: (error, { id }) => fail(error, id),
  });

  const reopen = useMutation({
    mutationFn: ({ id, version }: { id: string; version: number }) =>
      api.conversations.reopen(orgId, id, version),
    onSuccess: (conversation) => {
      invalidateConversation(queryClient, orgId, conversation.id);
      toast(t("chat.toast.reopened"));
    },
    onError: (error, { id }) => fail(error, id),
  });

  return { rename, pin, archive, restore, complete, reopen };
}

/**
 * 已读上报：只在 seq 超过已知已读位置时发送，同一会话同一 seq 只发一次。
 * 已读不产生事件，成功前先乐观把列表的 unread 清掉。
 */
export function useMarkRead(orgId: string) {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, seq }: { id: string; seq: number }) =>
      api.conversations.markRead(orgId, id, seq),
    onMutate: ({ id, seq }) =>
      patchConversation(queryClient, orgId, id, (item) => ({
        ...item,
        unread: false,
        last_read_seq: Math.max(item.last_read_seq, seq),
      })),
    onError: (_error, { id }) => invalidateConversation(queryClient, orgId, id),
  });
}
