import { AppErrorBoundary } from "../ErrorBoundary";
import {
  grantBrowserForMessage,
  consumeBrowserDraft,
} from "./browser/draftControl";
import { useRun, useRunResult } from "./steps/queries";
import { isDuplicateRunError } from "./steps/format";
import { ForkConversationDialog } from "./ForkConversationDialog";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import {
  useEffectEvent,
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  useSyncExternalStore,
  type ReactNode,
} from "react";
import { useSearchParams } from "react-router-dom";

import {
  isApiError,
  newIdempotencyKey,
  type ConversationMessage,
  type ConversationSummary,
  type ConversationWorkspaceSelection,
  type SpaceAgent,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Icon, Skeleton, Spinner, cn, useToast } from "@openfox/ui";

import { useMe } from "../agents/queries";
import { keys, useApi } from "../context";
import { useOrgPeople } from "../tasks/queries";
import { attachmentErrorMessage } from "../attachments/model";
import { RunArtifactButtons } from "./artifacts/ArtifactButtons";
import { Composer, type ComposerAgent, type ComposerSubmit } from "./Composer";
import { ComputerNotice } from "./ComputerNotice";
import { ContextUsage } from "./ContextUsage";
import { QueueFullNotice } from "./QueueFullNotice";
import {
  WorkDirectoryPicker,
  workspaceOptionsKey,
  type WorkDirectoryDraft,
} from "./WorkDirectoryPicker";
import { newDraftKey, quoteDraft, readDraft, notifyDraft } from "./drafts";
import { workspaceStore } from "./workspaceStorage";
import {
  AttachmentUploadError,
  consumeSentDraft,
  pendingKey,
  prepareRequest,
  withSendLock,
  type PendingSend,
} from "./pendingSend";
import { useMessageTarget, useLocateMessage } from "./messageWindow";
import { replyPreviewText } from "./ReplyPreview";
import { ChatMarkdown } from "./ChatMarkdown";
import { AgentMessage, SystemMessage, UserMessage } from "./Messages";
import { AgentTurn } from "./AgentTurn";
import { activeRunId, mergeMessagePages, timelineItems } from "./model";
import { ExecutedCard, ProposalCard } from "./ProposalCard";
import {
  appendMessage,
  chatErrorMessage,
  invalidateConversation,
  useMarkRead,
  useMessages,
} from "./queries";
import {
  directRetryDirectory,
  RunTimeline,
  type RunTimelineDisplayProps,
} from "./steps";
import {
  OverridesSaveError,
  overrideErrorMessage,
  useConversationOverrides,
  type NewConversationDefaults,
} from "./useOverrides";

/** 离底部多近算「在底部」（像素） */
const BOTTOM_SLACK = 80;
/** 离顶部多近时自动加载更早的消息（像素） */
const TOP_SLACK = 120;

const renderMarkdown = (text: string) => <ChatMarkdown source={text} />;

/**
 * 对话主体（对话页与小助手共用）：消息流 + 执行时间线 + 输入框。
 * conversation 为空表示新会话：第一条消息发送前由 ensureConversation 创建会话。
 */
function ConversationViewContent({
  orgId,
  agent,
  conversation,
  ensureConversation,
  onConversationCreated,
  empty,
  spaceId,
  onOpenSettings,
  onOpenDirectorySettings,
  composerPlaceholder,
  crew,
  newDefaults,
  agentDetail,
}: {
  orgId: string;
  agent: ComposerAgent;
  /** 小队对话（阶段 7d）：成员 Agent 与队长名；Agent 消息按作者显示名称 */
  crew?: { members: ComposerAgent[]; leaderName: string };
  conversation: ConversationSummary | undefined;
  /** 新会话：创建会话并返回 id（幂等，失败抛错） */
  ensureConversation?: (
    selection?: ConversationWorkspaceSelection,
    requestedId?: string,
  ) => Promise<string>;
  /** 新会话的第一条消息发送成功 */
  onConversationCreated?: (conversationId: string) => void;
  /** 没有消息时显示的空态 */
  empty: ReactNode;
  /** 草稿卡确认时的默认空间 */
  spaceId: string;
  onOpenSettings?: () => void;
  onOpenDirectorySettings?: () => void;
  composerPlaceholder?: string;
  /** 新会话：Agent 默认的 Runtime / 模型 / 权限模式与绑定电脑；有它输入框里的三个控件才能在建会话前选择 */
  newDefaults?: NewConversationDefaults | null;
  /** 已拿到的 Agent 详情（小助手）；有它时电脑提示不再请求 Agent 详情 */
  agentDetail?: SpaceAgent;
}) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const queryClient = useQueryClient();
  const me = useMe();
  const people = useOrgPeople(orgId);
  const conversationId = conversation?.id ?? null;
  const [forkMessage, setForkMessage] = useState<string | null>(null);
  const forkAttempts = useRef(new Map<string, string>());
  const [workspace, setWorkspace] = useState<WorkDirectoryDraft>();
  const [draftCreated, setDraftCreated] = useState(false);

  const { target, leave: leaveTarget } = useMessageTarget();
  const messagesQuery = useMessages(orgId, conversationId, target);
  const messages = useMemo(
    () =>
      messagesQuery.data ? mergeMessagePages(messagesQuery.data.pages) : [],
    [messagesQuery.data],
  );
  const runId = activeRunId(conversation);
  const items = useMemo(
    () => timelineItems(messages, target ? null : runId),
    [messages, runId, target],
  );
  const owner = !conversation || conversation.owner_user_id === me.data?.id;
  const closed = conversation && conversation.lifecycle !== "OPEN";
  const overrides = useConversationOverrides({
    orgId,
    conversation,
    newDefaults,
    crew: Boolean(crew),
    closed:
      Boolean(closed) ||
      (conversation?.agent.builtin_kind === "ASSISTANT" && !owner),
  });

  // ---------- 发送 ----------
  // 输入框草稿按会话隔离（阶段 10a）：新会话还没有 ID 时按 Agent 区分
  const draftKey = conversationId ?? newDraftKey(agent.id, Boolean(crew));
  const store = workspaceStore();
  const pending = useSyncExternalStore(
    (listener) => {
      store.listeners.add(listener);
      return () => {
        store.listeners.delete(listener);
      };
    },
    () => store.read<PendingSend>(pendingKey(draftKey)),
  );
  const [receiptState, setReceiptState] = useState<
    "pending" | "missing" | "uncertain"
  >("pending");
  const [checking, setChecking] = useState(false);
  const received = async (
    item: PendingSend,
    message: ConversationMessage,
    key: string,
  ) => {
    consumeSentDraft(store, key, item.source, false);
    store.write(pendingKey(key), undefined);
    try {
      await store.flush();
    } catch (error) {
      store.write(pendingKey(key), item);
      throw error;
    }
    consumeBrowserDraft(orgId, item.conversationId, item.clientId);
    if (workspaceStore() !== store) return;
    notifyDraft(key);
    appendMessage(queryClient, orgId, item.conversationId, message);
    invalidateConversation(queryClient, orgId, item.conversationId, true);
    if (!conversationId) onConversationCreated?.(item.conversationId);
  };
  const send = useMutation({
    mutationFn: ({
      input,
      retry = false,
    }: {
      input: ComposerSubmit;
      retry?: boolean;
    }) =>
      withSendLock(store, draftKey, async () => {
        const key = draftKey;
        let item = store.read<PendingSend>(pendingKey(key));
        if (item?.request) {
          // A failed lookup never means "not sent". Retrying uses the frozen payload even if the user has edited the draft.
          const receipt = await api.conversations.messageReceipt(
            orgId,
            item.conversationId,
            item.clientId,
          );
          if (receipt.message) {
            await received(item, receipt.message, key);
            return;
          }
          if (!retry) {
            setReceiptState("missing");
            return;
          }
        }
        if (!item) {
          item = {
            clientId: crypto.randomUUID(),
            conversationId: conversationId ?? crypto.randomUUID(),
            created: Boolean(conversationId),
            selection: workspace?.selection,
            input,
            source: readDraft(key) ?? {
              body: input.body,
              picked: [],
              files: input.files,
            },
            uploaded: [],
          };
          store.write(pendingKey(key), item);
          await store.flush();
        }
        if (!item.created) {
          await ensureConversation!(item.selection, item.conversationId);
          await overrides.applyDraft(item.conversationId);
          item = { ...item, created: true };
          store.write(pendingKey(key), item);
          await store.flush();
          setDraftCreated(true);
          void queryClient.invalidateQueries({
            queryKey: workspaceOptionsKey(orgId, agent.id),
          });
        }
        item = await prepareRequest(api, orgId, store, key, item);
        await grantBrowserForMessage(orgId, item.conversationId, item.clientId);
        let message: ConversationMessage;
        try {
          message = await api.conversations.postMessage(
            orgId,
            item.conversationId,
            item.request!,
          );
        } catch (error) {
          // Only a definitive rejection of this POST can release its identity.
          // A receipt lookup failure says nothing about whether it was accepted.
          if (
            isApiError(error) &&
            error.status >= 400 &&
            error.status < 500 &&
            ![408, 429].includes(error.status) &&
            error.code !== "IDEMPOTENCY_CONFLICT"
          ) {
            store.write(pendingKey(key), undefined);
            await store.flush().catch(() => {});
          }
          throw error;
        }
        await received(item, message, key);
      }),
  });
  const checkReceipt = async () => {
    if (!pending || checking || send.isPending) return;
    setChecking(true);
    try {
      if (!pending.request) {
        setReceiptState("missing");
        return;
      }
      const result = await api.conversations.messageReceipt(
        orgId,
        pending.conversationId,
        pending.clientId,
      );
      if (result.message) await received(pending, result.message, draftKey);
      else setReceiptState("missing");
    } catch {
      setReceiptState("uncertain");
    } finally {
      setChecking(false);
    }
  };
  const checkDraftReceipt = useEffectEvent(() => {
    setReceiptState("pending");
    if (pending) void checkReceipt();
    // 每次切换草稿只查询一次既有回执。
  });
  useEffect(() => {
    checkDraftReceipt();
  }, [draftKey]);
  const submit = async (input: ComposerSubmit, retry = false) => {
    if (
      !conversationId &&
      !draftCreated &&
      workspace &&
      workspace.deviceId !== agentDetail?.config?.computer_id
    ) {
      toast(t("chat.workspace.changedComputer"), { tone: "error" });
      return false;
    }
    try {
      await send.mutateAsync({ input, retry });
      if (target) leaveTarget();
      stickToBottom.current = true;
      return false; // received consumes the frozen draft, including recovery; Composer must not consume newer input.
    } catch (error) {
      if (error instanceof Error && error.message.startsWith("STORAGE_")) {
        toast(t("artifacts.persistence.failed"), { tone: "error" });
        return false;
      }
      if (error instanceof OverridesSaveError) {
        toast(
          t("chat.overrides.toast.newChatFailed", {
            reason: overrideErrorMessage(t, error.reason),
          }),
          { tone: "error" },
        );
        return false;
      }
      if (error instanceof AttachmentUploadError) {
        toast(attachmentErrorMessage(t, error.reason, error.fileName), {
          tone: "error",
        });
        return false;
      }
      if (
        isApiError(error) &&
        [
          "REVIEW_RUNTIME_UNSUPPORTED",
          "REVIEW_CONFLICT",
          "REVIEW_UNAVAILABLE",
          "INVALID_ANNOTATION",
        ].includes(error.code)
      ) {
        const key =
          error.code === "REVIEW_RUNTIME_UNSUPPORTED"
            ? "unsupported"
            : error.code === "REVIEW_CONFLICT"
              ? "conflict"
              : error.code === "INVALID_ANNOTATION"
                ? "selectionLimit"
                : "unavailable";
        toast(t(`artifacts.review.${key}`), { tone: "error" });
        return false;
      }
      const text = chatErrorMessage(t, error);
      toast(
        text === t("chat.errors.generic")
          ? t("chat.composer.sendFailed")
          : text,
        { tone: "error" },
      );
      return false;
    }
  };

  // 一轮回复头部显示的作者：小队按成员 Agent 查找，找不到（如队长）或普通对话用当前 Agent。
  const authorOf = (agentId?: string | null): ComposerAgent =>
    (agentId && crew?.members.find((member) => member.id === agentId)) || agent;

  const isAssistant =
    agentDetail?.builtin_kind === "ASSISTANT" ||
    conversation?.agent.builtin_kind === "ASSISTANT";
  // 从历史链接打开时没有助手详情；动作只能出现在本人的内置助手会话。
  const retryOriginalRun =
    isAssistant || messages.some((message) => message.assistant_action);
  const retryScope = retryOriginalRun
    ? { agent_id: agent.id, actor_id: me.data?.id ?? "" }
    : undefined;
  // 阶段 12b：普通对话的重试走专用端点（新建 Run 并写入 retry_of，保留原运行历史）。
  // 每个失败 Run 固定一个幂等键：重复点击 / 超时重试都落到同一次重试。
  const retryKeys = useRef(new Map<string, string>());
  const retry = useMutation({
    mutationFn: async (runId: string) => {
      if (!conversationId) return;
      if (retryOriginalRun) {
        await api.conversations.retryAssistantRun(orgId, conversationId, runId);
        return;
      }
      let key = retryKeys.current.get(runId);
      if (!key) {
        key = crypto.randomUUID();
        retryKeys.current.set(runId, key);
      }
      await api.conversations.retryRun(orgId, conversationId, runId, key);
    },
    onSuccess: () => {
      if (!conversationId) return;
      invalidateConversation(queryClient, orgId, conversationId, true);
      stickToBottom.current = true;
    },
    onError: (error) => toast(chatErrorMessage(t, error), { tone: "error" }),
  });
  // 阶段 12d（F-08）：worktree 准备失败时，原输入作者可确认后改为直接在源目录运行并重试（不自动切换）。
  // 与普通重试用不同的幂等键：同键不同参数会被拒绝（IDEMPOTENCY_CONFLICT）。
  const directDirectory = directRetryDirectory(
    conversation?.workspace_selection,
    agentDetail?.config,
  );
  const retryDirect = useMutation({
    mutationFn: async (runId: string) => {
      if (!conversationId || !directDirectory) return;
      const mapKey = `direct:${runId}`;
      let key = retryKeys.current.get(mapKey);
      if (!key) {
        key = crypto.randomUUID();
        retryKeys.current.set(mapKey, key);
      }
      await api.conversations.retryRun(orgId, conversationId, runId, key, {
        mode: "DIRECT",
        directory: directDirectory,
      });
    },
    onSuccess: () => {
      if (!conversationId) return;
      invalidateConversation(queryClient, orgId, conversationId, true);
      stickToBottom.current = true;
    },
    onError: (error) => toast(chatErrorMessage(t, error), { tone: "error" }),
  });
  const directRetry =
    !closed &&
    !retryOriginalRun &&
    conversationId &&
    me.data?.id &&
    directDirectory
      ? {
          actorId: me.data.id,
          directory: directDirectory,
          onRetry: (runId: string) => retryDirect.mutate(runId),
        }
      : undefined;
  const retryFor = (failedId: string | null) =>
    !closed && (!retryOriginalRun || owner) && failedId
      ? () => retry.mutate(failedId)
      : undefined;

  // ---------- 停止 ----------
  // 每个 Run 一个固定的幂等键：重复点击 / 超时重试都落到同一次取消
  const cancelKeys = useRef(new Map<string, string>());
  const stop = useMutation({
    mutationFn: (id: string) => {
      let key = cancelKeys.current.get(id);
      if (!key) {
        key = newIdempotencyKey();
        cancelKeys.current.set(id, key);
      }
      return api.runs.cancel(orgId, id, key);
    },
    onSuccess: ({ run }) => {
      queryClient.setQueryData(keys.run(orgId, run.id), run);
      if (conversationId)
        invalidateConversation(queryClient, orgId, conversationId);
      toast(t("chat.composer.stopped"));
    },
    onError: (error) => toast(chatErrorMessage(t, error), { tone: "error" }),
  });

  // ---------- 滚动与翻页 ----------
  const scroller = useRef<HTMLDivElement | null>(null);
  const stickToBottom = useRef(true);
  const [atBottom, setAtBottom] = useState(true);
  /** 加载更早消息前记下的滚动高度，加载后据此保持视口位置 */
  const anchorHeight = useRef<number | null>(null);
  const loadOlder = useCallback(() => {
    if (!messagesQuery.hasNextPage || messagesQuery.isFetchingNextPage) return;
    anchorHeight.current = scroller.current?.scrollHeight ?? null;
    void messagesQuery.fetchNextPage();
  }, [messagesQuery]);

  useLayoutEffect(() => {
    const el = scroller.current;
    if (!el) return;
    if (anchorHeight.current !== null) {
      el.scrollTop += el.scrollHeight - anchorHeight.current;
      anchorHeight.current = null;
      return;
    }
    if (!target && stickToBottom.current) el.scrollTop = el.scrollHeight;
  }, [items, target]);

  // 切换会话时回到底部
  useEffect(() => {
    stickToBottom.current = !target;
    setAtBottom(!target);
  }, [conversationId, target]);
  useLocateMessage(target, scroller, messagesQuery.data, () => {
    stickToBottom.current = false;
    setAtBottom(false);
  });

  const onScroll = () => {
    const el = scroller.current;
    if (!el) return;
    const bottom =
      el.scrollHeight - el.scrollTop - el.clientHeight <= BOTTOM_SLACK;
    stickToBottom.current = bottom;
    setAtBottom(bottom);
    if (el.scrollTop <= TOP_SLACK) loadOlder();
    else if (
      target &&
      bottom &&
      messagesQuery.hasPreviousPage &&
      !messagesQuery.isFetching
    )
      void messagesQuery.fetchPreviousPage();
  };

  // ---------- ?run= 定位（运行历史跳转） ----------
  const [searchParams] = useSearchParams();
  const targetRun = searchParams.get("run");
  const located = useRef<string | null>(null);
  const locateTries = useRef(0);
  useEffect(() => {
    if (!targetRun || located.current === targetRun || !messagesQuery.data)
      return;
    const el = Array.from(
      scroller.current?.querySelectorAll<HTMLElement>("[data-anchor-run]") ??
        [],
    ).find((node) => node.dataset.anchorRun === targetRun);
    if (el) {
      located.current = targetRun;
      stickToBottom.current = false;
      el.scrollIntoView?.({ block: "center" });
      return;
    }
    // 不在已加载的页里：最多再往前翻 5 页
    if (messagesQuery.hasNextPage && locateTries.current < 5) {
      locateTries.current += 1;
      loadOlder();
    }
  }, [
    targetRun,
    items,
    messagesQuery.data,
    messagesQuery.hasNextPage,
    loadOlder,
  ]);

  // ---------- 已读 ----------
  const markRead = useMarkRead(orgId);
  const reported = useRef(new Map<string, number>());
  const lastSeq = messages.at(-1)?.seq ?? 0;
  useEffect(() => {
    if (target || !conversation || !atBottom || lastSeq === 0) return;
    const known = Math.max(
      conversation.last_read_seq,
      reported.current.get(conversation.id) ?? 0,
    );
    if (lastSeq <= known) return;
    reported.current.set(conversation.id, lastSeq);
    markRead.mutate({ id: conversation.id, seq: lastSeq });
  }, [target, conversation, atBottom, lastSeq, markRead]);

  const scrollToBottom = () => {
    if (target) leaveTarget();
    const el = scroller.current;
    if (!el) return;
    el.scrollTop = el.scrollHeight;
    stickToBottom.current = true;
    setAtBottom(true);
  };

  const disabledReason = closed ? t("chat.composer.closed") : null;
  const showEmpty =
    !conversationId ||
    (messagesQuery.isSuccess && messages.length === 0 && !runId);

  const assistantLanding = isAssistant && showEmpty;
  const newChat = !conversationId && !isAssistant;
  const centeredComposer = newChat || assistantLanding;
  return (
    <>
      <div
        className={
          centeredComposer
            ? "flex min-h-0 min-w-0 flex-1 flex-col overflow-y-auto"
            : "flex min-h-0 min-w-0 flex-1 flex-col"
        }
      >
        <div
          ref={scroller}
          onScroll={onScroll}
          className={
            centeredComposer
              ? "relative mt-auto shrink-0"
              : "relative min-h-0 flex-1 overflow-y-auto"
          }
        >
          <div
            className={cn(
              "mx-auto flex min-h-full flex-col gap-6 px-4 @lg:px-6",
              newChat ? "max-w-[800px] pt-6 pb-4" : "max-w-[848px]",
              !newChat &&
                (assistantLanding
                  ? conversationId
                    ? "py-0"
                    : "py-12"
                  : "py-6"),
            )}
          >
            {conversationId && messagesQuery.isLoading ? (
              <MessagesSkeleton />
            ) : conversationId &&
              messagesQuery.isError &&
              !messagesQuery.data ? (
              <div
                role="alert"
                className="m-auto flex flex-col items-center gap-2 text-body text-fg-muted"
              >
                <Icon
                  name="hifox-alert-circle"
                  size={20}
                  className="text-danger"
                />
                {t(
                  target &&
                    isApiError(messagesQuery.error) &&
                    messagesQuery.error.status === 404
                    ? "chat.messages.targetUnavailable"
                    : "chat.messages.loadFailed",
                )}
                <Button
                  variant="outline"
                  size="28"
                  onClick={() => void messagesQuery.refetch()}
                >
                  {t("common.retry")}
                </Button>
              </div>
            ) : showEmpty ? (
              <div className={newChat ? "w-full" : "m-auto"}>{empty}</div>
            ) : (
              <>
                {messagesQuery.hasNextPage && (
                  <div className="flex justify-center">
                    {messagesQuery.isFetchingNextPage ? (
                      <span
                        role="status"
                        className="flex items-center gap-2 text-caption text-fg-muted"
                      >
                        <Spinner />
                        {t("chat.messages.loadingOlder")}
                      </span>
                    ) : (
                      <Button
                        variant="ghost-muted"
                        size="28"
                        onClick={loadOlder}
                      >
                        {t("chat.messages.loadOlder")}
                      </Button>
                    )}
                  </div>
                )}
                {items.map((item) =>
                  item.kind === "run" ? (
                    <RunAgentTurn
                      key={`run:${item.runId}`}
                      orgId={orgId}
                      runId={item.runId}
                      authorOf={authorOf}
                    >
                      <div data-anchor-run={item.runId}>
                        <RunTimeline
                          orgId={orgId}
                          runId={item.runId}
                          renderText={renderMarkdown}
                          showFinalText={!isAssistant}
                          onRetry={retryFor(item.runId)}
                          retryScope={retryScope}
                          retrying={retry.isPending || retryDirect.isPending}
                          directRetry={directRetry}
                        />
                      </div>
                    </RunAgentTurn>
                  ) : (
                    <MessageBlock
                      key={item.message.id}
                      focused={item.message.id === target}
                      orgId={orgId}
                      message={item.message}
                      onFork={
                        conversation &&
                        !conversation.archived_at &&
                        !closed &&
                        (owner || conversation.offer_participant) &&
                        item.message.kind === "TEXT" &&
                        ["USER", "AGENT"].includes(item.message.author_kind)
                          ? () => setForkMessage(item.message.id)
                          : undefined
                      }
                      onReply={
                        closed ||
                        item.message.kind !== "TEXT" ||
                        !item.message.body.trim()
                          ? undefined
                          : () =>
                              quoteDraft(draftKey, {
                                id: item.message.id,
                                authorKind: item.message.author_kind,
                                ...replyPreviewText(item.message.body),
                              })
                      }
                      runBefore={item.runBefore}
                      model={agent.model}
                      author={authorOf(item.message.author_agent_id)}
                      authorName={
                        crew
                          ? crew.members.find(
                              (member) =>
                                member.id === item.message.author_agent_id,
                            )?.name
                          : undefined
                      }
                      spaceId={spaceId}
                      onRetry={retryFor(item.runBefore)}
                      retryScope={retryScope}
                      retrying={retry.isPending || retryDirect.isPending}
                      directRetry={directRetry}
                    />
                  ),
                )}
                {target && messagesQuery.hasPreviousPage && (
                  <Button
                    variant="ghost-muted"
                    size="28"
                    disabled={messagesQuery.isFetchingPreviousPage}
                    onClick={() => void messagesQuery.fetchPreviousPage()}
                  >
                    {t("chat.messages.loadNewer")}
                  </Button>
                )}
              </>
            )}
          </div>
        </div>
        <div
          className={
            centeredComposer
              ? "relative mb-auto shrink-0 px-4 pb-4 @lg:px-6"
              : "relative shrink-0 px-4 pt-2 pb-5 @lg:px-6 @lg:pb-6"
          }
        >
          {(target || (!atBottom && !showEmpty)) && (
            <button
              type="button"
              aria-label={t("chat.messages.scrollBottom")}
              title={t("chat.messages.scrollBottom")}
              onClick={scrollToBottom}
              className="absolute -top-10 left-1/2 flex size-8 -translate-x-1/2 items-center justify-center rounded-full border border-line-default bg-surface-card shadow-popover hover:bg-surface-hover"
            >
              <Icon name="arrow-up" size={16} className="rotate-180" />
            </button>
          )}
          <div className="mx-auto max-w-[752px]">
            {!!conversation?.fork_prefix_seq && (
              <p
                role="note"
                className="mb-2 rounded border border-line-default p-2 text-caption text-fg-muted"
              >
                {t("chat.fork.notice")} {t("chat.fork.limits")}
              </p>
            )}
            <ContextUsage
              orgId={orgId}
              runId={conversation?.run?.id}
              effective={
                crew
                  ? null
                  : (overrides.controls?.effective ??
                    conversation?.effective ??
                    newDefaults)
              }
            />
            <Composer
              showAssistantActions={assistantLanding}
              landingComposer={assistantLanding && !conversationId}
              notice={
                <>
                  {pending && !send.isPending && (
                    <div
                      role="status"
                      className="mb-2 rounded-lg border border-line-default p-2 text-caption"
                    >
                      <p>{t(`artifacts.persistence.${receiptState}`)}</p>
                      <button
                        type="button"
                        className="mr-3 mt-1 underline"
                        disabled={checking}
                        onClick={() => void checkReceipt()}
                      >
                        {t("artifacts.persistence.check")}
                      </button>
                      {!pending.request && (
                        <button
                          type="button"
                          className="mr-3 mt-1 underline"
                          disabled={checking}
                          onClick={() => {
                            store.write(pendingKey(draftKey), undefined);
                            void store.flush().catch(() => {});
                          }}
                        >
                          {t("artifacts.persistence.cancelPreparation")}
                        </button>
                      )}
                      {receiptState === "missing" && (
                        <button
                          type="button"
                          className="mt-1 underline"
                          disabled={checking}
                          onClick={() => void submit(pending.input, true)}
                        >
                          {t("artifacts.persistence.retrySend")}
                        </button>
                      )}
                    </div>
                  )}
                  {/* 阶段 12e · 组织队列已满提示 */}
                  <QueueFullNotice orgId={orgId} />
                  {!crew && (
                    <ComputerNotice
                      orgId={orgId}
                      spaceId={conversation?.agent.space_id ?? spaceId}
                      agentId={agent.id}
                      agent={agentDetail}
                    />
                  )}
                </>
              }
              workDirectory={
                !crew &&
                (agentDetail || conversationId) &&
                !conversation?.task_id && (
                  <WorkDirectoryPicker
                    orgId={orgId}
                    agentId={agent.id}
                    config={agentDetail?.config}
                    value={
                      conversation?.workspace_selection
                        ? { selection: conversation.workspace_selection }
                        : workspace
                    }
                    locked={!!conversationId || draftCreated}
                    busy={send.isPending}
                    onChange={setWorkspace}
                    onEditSettings={onOpenDirectorySettings ?? onOpenSettings}
                  />
                )
              }
              draftKey={draftKey}
              orgId={orgId}
              slashAgent={agentDetail}
              agent={agent}
              crew={crew}
              people={
                conversation?.visibility === "SHARED" ? (people.data ?? []) : []
              }
              autoMentionAgent={owner}
              disabledReason={disabledReason}
              running={Boolean(runId)}
              stopping={
                stop.isPending ||
                conversation?.run?.state === "CANCEL_REQUESTED"
              }
              sending={send.isPending}
              onSubmit={submit}
              onStop={runId ? () => stop.mutate(runId) : undefined}
              onOpenSettings={onOpenSettings}
              overrides={overrides.controls}
              placeholder={composerPlaceholder}
            />
          </div>
        </div>
      </div>
      {forkMessage && conversation && (
        <ForkConversationDialog
          orgId={orgId}
          conversation={conversation}
          messageId={forkMessage}
          attempts={forkAttempts.current}
          onClose={() => setForkMessage(null)}
        />
      )}
    </>
  );
}

function MessageBlock({
  onReply,
  onFork,
  orgId,
  message,
  focused,
  runBefore,
  model,
  authorName,
  delegatedTo,
  spaceId,
  onRetry,
  retryScope,
  retrying,
  directRetry,
  author,
}: {
  onReply?: () => void;
  onFork?: () => void;
  orgId: string;
  message: ConversationMessage;
  focused: boolean;
  runBefore: string | null;
  /** 头部显示的作者（头像与名字） */
  author: ComposerAgent;
  model?: string | null;
  /** 小队对话里 Agent 消息的作者名 */
  authorName?: string;
  /** 队长委派消息的被委派成员名 */
  delegatedTo?: string;
  spaceId: string;
  onRetry?: () => void;
  retryScope?: RunTimelineDisplayProps["retryScope"];
  retrying: boolean;
  directRetry?: RunTimelineDisplayProps["directRetry"];
}) {
  const result = useRunResult(orgId, runBefore ?? "", Boolean(runBefore));
  const hideErrorBody = isDuplicateRunError(message.body, result.data);
  const anchor = {
    "data-message-id": message.id,
    className: focused ? "message-search-hit" : undefined,
  };
  if (message.author_kind === "USER")
    return (
      <div {...anchor}>
        <UserMessage message={message} onReply={onReply} onFork={onFork} />
      </div>
    );
  const system = message.author_kind === "SYSTEM" && message.kind === "TEXT";
  const content = (
    <>
      {runBefore && (
        <div data-anchor-run={runBefore}>
          <RunTimeline
            orgId={orgId}
            runId={runBefore}
            renderText={renderMarkdown}
            showFinalText={system}
            showArtifacts={system}
            onRetry={onRetry}
            retryScope={retryScope}
            retrying={retrying}
            directRetry={directRetry}
          />
        </div>
      )}
      <div {...anchor} className={cn("flex flex-col", anchor.className)}>
        {system ? (
          <SystemMessage message={message} />
        ) : message.kind === "ACTION_EXECUTED" ? (
          <ExecutedCard message={message} />
        ) : message.kind === "ACTION_PROPOSAL" ? (
          <ProposalCard
            orgId={orgId}
            message={message}
            defaultSpaceId={spaceId}
          />
        ) : (
          <AgentMessage
            hideBody={hideErrorBody}
            onReply={onReply}
            onFork={onFork}
            orgId={orgId}
            message={message}
            model={model}
            authorName={authorName}
            delegatedTo={delegatedTo}
          >
            {/* 阶段 10d：本次回复对应 Run 的产物按钮（回复与 Run 经 runBefore 关联） */}
            {runBefore && (
              <RunArtifactButtons orgId={orgId} runId={runBefore} />
            )}
          </AgentMessage>
        )}
      </div>
    </>
  );
  // 系统消息居中显示，不加 Agent 头部
  if (system) return <div className="flex flex-col gap-2">{content}</div>;
  return (
    <AgentTurn name={author.name} avatar={author.avatar}>
      {content}
    </AgentTurn>
  );
}

/** 没有对应回复消息的运行（运行中、失败无回复）：按 Run 的 agent_id 找作者，同样显示头部。 */
function RunAgentTurn({
  orgId,
  runId,
  authorOf,
  children,
}: {
  orgId: string;
  runId: string;
  authorOf: (agentId?: string | null) => ComposerAgent;
  children: ReactNode;
}) {
  const run = useRun(orgId, runId);
  const author = authorOf(run.data?.agent_id);
  return (
    <AgentTurn name={author.name} avatar={author.avatar}>
      {children}
    </AgentTurn>
  );
}

function MessagesSkeleton() {
  return (
    <div aria-busy="true" className="m-auto flex flex-col items-center gap-2">
      <Skeleton className="size-9 rounded-lg" />
      <Skeleton className="h-3 w-28" />
      <Skeleton className="h-3 w-20" />
    </div>
  );
}

export function ConversationView(
  props: Parameters<typeof ConversationViewContent>[0],
) {
  return (
    <AppErrorBoundary key={props.conversation?.id ?? props.agent.id}>
      <ConversationViewContent {...props} />
    </AppErrorBoundary>
  );
}
