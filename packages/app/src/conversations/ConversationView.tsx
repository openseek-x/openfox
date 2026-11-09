import { useMutation, useQueryClient } from "@tanstack/react-query";
import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
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
import { ArtifactPanel } from "./artifacts/ArtifactPanel";
import { RunArtifactButtons } from "./artifacts/ArtifactButtons";
import { ArtifactOpenContext } from "./artifacts/context";
import { useArtifactPanel } from "./artifacts/params";
import { Composer, type ComposerAgent, type ComposerSubmit } from "./Composer";
import { ComputerNotice } from "./ComputerNotice";
import {
  WorkDirectoryPicker,
  workspaceOptionsKey,
  type WorkDirectoryDraft,
} from "./WorkDirectoryPicker";
import { newDraftKey } from "./drafts";
import { useMessageTarget, useLocateMessage } from "./messageWindow";
import { ChatMarkdown } from "./ChatMarkdown";
import { AgentMessage, SystemMessage, UserMessage } from "./Messages";
import {
  activeRunId,
  lastUserMessage,
  mergeMessagePages,
  timelineItems,
} from "./model";
import { ExecutedCard, ProposalCard } from "./ProposalCard";
import {
  appendMessage,
  chatErrorMessage,
  invalidateConversation,
  useMarkRead,
  useMessages,
} from "./queries";
import { RunTimeline, type RunTimelineDisplayProps } from "./steps";
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
/** 附件上传失败：保留原始错误和文件名，用于给出具体提示（草稿与待发送文件都保留）。 */
class AttachmentUploadError extends Error {
  constructor(
    readonly fileName: string,
    readonly reason: unknown,
  ) {
    super(`attachment upload failed: ${fileName}`);
  }
}

export function ConversationView({
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
  /** 小队对话：成员 Agent 与队长名；Agent 消息按作者显示名称 */
  crew?: { members: ComposerAgent[]; leaderName: string };
  conversation: ConversationSummary | undefined;
  /** 新会话：创建会话并返回 id（幂等，失败抛错） */
  ensureConversation?: (
    selection?: ConversationWorkspaceSelection,
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
  // 输入框草稿按会话隔离：新会话还没有 ID 时按 Agent 区分。
  const draftKey = conversationId ?? newDraftKey(agent.id, Boolean(crew));
  // 同一份草稿重试时复用 client_message_id；发送成功后换新的。
  // 草稿按会话隔离后，重试标识也跟着草稿走：每个草稿键一个，切换会话不会互相顶替
  const clientIds = useRef(new Map<string, string>());
  const clientIdFor = (key: string) => {
    let id = clientIds.current.get(key);
    if (!id) {
      id = crypto.randomUUID();
      clientIds.current.set(key, id);
    }
    return id;
  };
  // 已上传的附件按 File 对象缓存，发送失败后重试不会重复上传。
  const uploaded = useRef(new WeakMap<File, string>());
  const send = useMutation({
    mutationFn: async ({
      input,
      clientMessageId,
    }: {
      input: ComposerSubmit;
      clientMessageId: string;
    }) => {
      const id =
        conversationId ?? (await ensureConversation!(workspace?.selection));
      if (!conversationId) {
        setDraftCreated(true);
        void queryClient.invalidateQueries({
          queryKey: workspaceOptionsKey(orgId, agent.id),
        });
      }
      // 新对话：先把输入框里选的 Runtime / 模型 / 权限写进会话，第一次运行才会用所选的值
      if (!conversationId) await overrides.applyDraft(id);
      const attachmentIds: string[] = [];
      for (const file of input.files) {
        let attachmentId = uploaded.current.get(file);
        if (!attachmentId) {
          try {
            attachmentId = (
              await api.attachments.uploadToConversation(orgId, id, file)
            ).id;
          } catch (error) {
            throw new AttachmentUploadError(file.name, error);
          }
          uploaded.current.set(file, attachmentId);
        }
        attachmentIds.push(attachmentId);
      }
      const message = await api.conversations.postMessage(orgId, id, {
        body: input.body,
        ...(input.assistantAction && {
          assistant_action: input.assistantAction,
        }),
        client_message_id: clientMessageId,
        ...(input.mentions.length ? { mentions: input.mentions } : {}),
        ...(attachmentIds.length ? { attachment_ids: attachmentIds } : {}),
      });
      return { id, message };
    },
    onSuccess: ({ id, message }) => {
      appendMessage(queryClient, orgId, id, message);
      // 发消息接口返回的 run_id 为 null，刷新拿到派发后的 Run
      invalidateConversation(queryClient, orgId, id, true);
      if (!conversationId) onConversationCreated?.(id);
    },
  });
  const submit = async (input: ComposerSubmit) => {
    if (
      !conversationId &&
      !draftCreated &&
      workspace &&
      workspace.deviceId !== agentDetail?.config?.computer_id
    ) {
      toast(t("chat.workspace.changedComputer"), { tone: "error" });
      return false;
    }
    const key = draftKey;
    try {
      await send.mutateAsync({ input, clientMessageId: clientIdFor(key) });
      clientIds.current.delete(key);
      if (target) leaveTarget();
      stickToBottom.current = true;
      return true;
    } catch (error) {
      // 内容不同的重放冲突：换一个新的 client_message_id 才能再发
      if (isApiError(error, "IDEMPOTENCY_CONFLICT"))
        clientIds.current.delete(key);
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

  const isAssistant =
    agentDetail?.builtin_kind === "ASSISTANT" ||
    conversation?.agent.builtin_kind === "ASSISTANT";
  // 从历史链接打开时没有助手详情；动作只能出现在本人的内置助手会话。
  const retryOriginalRun =
    isAssistant || messages.some((message) => message.assistant_action);
  const retryScope = retryOriginalRun
    ? { agent_id: agent.id, actor_id: me.data?.id ?? "" }
    : undefined;
  const retryTarget = lastUserMessage(messages);
  const retry = useMutation({
    mutationFn: async (runId: string) => {
      if (!conversationId) return;
      if (retryOriginalRun) {
        await api.conversations.retryAssistantRun(orgId, conversationId, runId);
        return;
      }
      if (!retryTarget) return;
      return api.conversations.postMessage(orgId, conversationId, {
        body: retryTarget.body,
        client_message_id: crypto.randomUUID(),
        mentions:
          owner && !crew
            ? [{ agent_id: agent.id }]
            : (retryTarget.mentions ?? []).filter((m) => m.agent_id),
      });
    },
    onSuccess: (message) => {
      if (!conversationId) return;
      if (message) appendMessage(queryClient, orgId, conversationId, message);
      invalidateConversation(queryClient, orgId, conversationId, true);
      stickToBottom.current = true;
    },
    onError: (error) => toast(chatErrorMessage(t, error), { tone: "error" }),
  });
  const retryFor = (failedId: string | null) =>
    !closed &&
    (!retryOriginalRun || owner) &&
    failedId &&
    (retryOriginalRun || retryTarget)
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

  // ---------- 产物面板（?artifact=） ----------
  const artifactPanel = useArtifactPanel(conversationId);
  const openArtifact = conversationId ? artifactPanel.openArtifact : null;

  const disabledReason = closed ? t("chat.composer.closed") : null;
  const showEmpty =
    !conversationId ||
    (messagesQuery.isSuccess && messages.length === 0 && !runId);

  const assistantLanding = isAssistant && showEmpty;
  return (
    <ArtifactOpenContext.Provider value={openArtifact}>
      <div className="relative flex min-h-0 flex-1 @container">
        <div
          className={
            assistantLanding
              ? "flex min-h-0 min-w-0 flex-1 flex-col overflow-y-auto"
              : "flex min-h-0 min-w-0 flex-1 flex-col"
          }
        >
          <div
            ref={scroller}
            onScroll={onScroll}
            className={
              assistantLanding
                ? "relative mt-auto shrink-0"
                : "relative min-h-0 flex-1 overflow-y-auto"
            }
          >
            <div
              className={cn(
                "mx-auto flex min-h-full max-w-[848px] flex-col gap-6 px-4 @lg:px-6",
                assistantLanding ? (conversationId ? "py-0" : "py-12") : "py-6",
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
                    name="openfox-alert-circle"
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
                <div className="m-auto">{empty}</div>
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
                      <div
                        key={`run:${item.runId}`}
                        data-anchor-run={item.runId}
                      >
                        <RunTimeline
                          orgId={orgId}
                          runId={item.runId}
                          renderText={renderMarkdown}
                          showFinalText={!isAssistant}
                          onRetry={retryFor(item.runId)}
                          retryScope={retryScope}
                          retrying={retry.isPending}
                        />
                      </div>
                    ) : (
                      <MessageBlock
                        key={item.message.id}
                        focused={item.message.id === target}
                        orgId={orgId}
                        message={item.message}
                        runBefore={item.runBefore}
                        model={agent.model}
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
                        retrying={retry.isPending}
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
              assistantLanding
                ? "relative mb-auto shrink-0 px-4 pb-4 @lg:px-6"
                : "relative shrink-0 px-4 pt-3 pb-4 @lg:px-6"
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
              <Composer
                showAssistantActions={assistantLanding}
                landingComposer={assistantLanding && !conversationId}
                notice={
                  !crew && (
                    <ComputerNotice
                      orgId={orgId}
                      spaceId={conversation?.agent.space_id ?? spaceId}
                      agentId={agent.id}
                      agent={agentDetail}
                    />
                  )
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
                agent={agent}
                crew={crew}
                people={
                  conversation?.visibility === "SHARED"
                    ? (people.data ?? [])
                    : []
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
        {conversationId && artifactPanel.open && (
          <ArtifactPanel
            orgId={orgId}
            conversationId={conversationId}
            panel={artifactPanel}
          />
        )}
      </div>
    </ArtifactOpenContext.Provider>
  );
}

function MessageBlock({
  orgId,
  message,
  focused,
  runBefore,
  model,
  authorName,
  spaceId,
  onRetry,
  retryScope,
  retrying,
}: {
  orgId: string;
  message: ConversationMessage;
  focused: boolean;
  runBefore: string | null;
  model?: string | null;
  /** 小队对话里 Agent 消息的作者名 */
  authorName?: string;
  spaceId: string;
  onRetry?: () => void;
  retryScope?: RunTimelineDisplayProps["retryScope"];
  retrying: boolean;
}) {
  const anchor = {
    "data-message-id": message.id,
    className: focused ? "message-search-hit" : undefined,
  };
  if (message.author_kind === "USER")
    return (
      <div {...anchor}>
        <UserMessage message={message} />
      </div>
    );
  const system = message.author_kind === "SYSTEM" && message.kind === "TEXT";
  return (
    <div className="flex flex-col gap-2">
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
            orgId={orgId}
            message={message}
            model={model}
            authorName={authorName}
          >
            {/* 本次回复对应 Run 的产物按钮（回复与 Run 经 runBefore 关联） */}
            {runBefore && (
              <RunArtifactButtons orgId={orgId} runId={runBefore} />
            )}
          </AgentMessage>
        )}
      </div>
    </div>
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
