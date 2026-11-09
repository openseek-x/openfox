import {
  type InfiniteData,
  useMutation,
  useQueryClient,
} from "@tanstack/react-query";
import {
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type ClipboardEvent,
} from "react";
import {
  isApiError,
  type AgentDirectoryEntry,
  type Me,
  type Task,
  type TimelineItem,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Avatar,
  Icon,
  IconButton,
  Popover,
  PromptInput,
  PromptInputActions,
  PromptInputTextarea,
  SearchMenu,
  cn,
  type IconName,
  useToast,
} from "@openfox/ui";
import {
  attachmentMarkdown,
  filesOf,
  namedPastedFile,
} from "../../attachments/model";
import {
  QuickReplyManager,
  QuickReplyMenu,
} from "../../conversations/QuickReplies";
import { keys, useApi } from "../../context";
import { isSendKey } from "../../me/shortcuts";
import { useSendShortcut } from "../../me/queries";
import {
  personName,
  taskErrorMessage,
  useAgentDirectory,
  type Person,
} from "../queries";
import { AgentAvatar } from "../../agents/AgentAvatar";
import { useTaskAttachments, useUploadTaskFiles } from "./AttachmentsSection";
import { ReadOnlyTip, useReadOnly } from "../../spaces/readonly";

// ---------- 评论框 ----------

interface Mention {
  id: string;
  kind?: "user" | "agent";
  email?: string;
  token?: string;
}

export type ReplyDraft = { body: string; mentions: Mention[]; id: string };

/** 评论框：输入 @ 弹出成员选择，提交时带 mentions；client_comment_id 保证重试幂等。 */
export function CommentBox({
  orgId,
  task,
  people,
  replyToId,
  inline,
  me,
  focusOnOpen,
  drafts,
}: {
  orgId: string;
  task: Task;
  people: Person[];
  replyToId?: string;
  inline?: boolean;
  me?: Me;
  focusOnOpen?: boolean;
  drafts?: Map<string, ReplyDraft>;
}) {
  const t = useT();
  const readOnly = useReadOnly();
  const sendShortcut = useSendShortcut();
  const api = useApi();
  const toast = useToast();
  const queryClient = useQueryClient();
  const draft = replyToId ? drafts?.get(replyToId) : undefined;
  const [body, setBody] = useState(draft?.body ?? "");
  const [mentions, setMentions] = useState<Mention[]>(draft?.mentions ?? []);
  const [clientId, setClientId] = useState(
    () => draft?.id ?? crypto.randomUUID(),
  );
  useEffect(() => {
    if (!replyToId || !drafts) return;
    if (body) drafts.set(replyToId, { body, mentions, id: clientId });
    else drafts.delete(replyToId);
  }, [body, mentions, clientId, replyToId, drafts]);
  const [mention, setMention] = useState<{
    start: number;
    end: number;
    query: string;
  } | null>(null);
  const [activeMention, setActiveMention] = useState(0);
  const mentionId = useId();
  const [slash, setSlash] = useState<{
    start: number;
    end: number;
    query: string;
  } | null>(null);
  const [activeSlash, setActiveSlash] = useState(0);
  const slashId = useId();
  const [attachOpen, setAttachOpen] = useState(false);
  const [quickOpen, setQuickOpen] = useState(false);
  const [manage, setManage] = useState<"list" | "create" | null>(null);
  const quickAttempt = useRef<{ body: string; id: string } | null>(null);
  const area = useRef<HTMLTextAreaElement | null>(null);
  const quickAnchor = useRef<HTMLButtonElement | null>(null);
  const attachAnchor = useRef<HTMLButtonElement | null>(null);
  const composing = useRef(false);
  const pendingCaret = useRef<number | null>(null);
  const fileInput = useRef<HTMLInputElement | null>(null);
  useLayoutEffect(() => {
    if (pendingCaret.current === null) return;
    const caret = pendingCaret.current;
    pendingCaret.current = null;
    area.current?.focus();
    area.current?.setSelectionRange(caret, caret);
  }, [body]);
  useEffect(() => {
    if (!focusOnOpen) return;
    const frame = requestAnimationFrame(() => area.current?.focus());
    return () => cancelAnimationFrame(frame);
  }, [focusOnOpen]);
  const attachments = useTaskAttachments(orgId, task);
  const { upload, uploading } = useUploadTaskFiles(orgId, task);
  const directory = useAgentDirectory(orgId);

  /** 在光标处插入附件链接（Markdown 链接，评论按链接渲染）。 */
  const insertLinks = (items: { name: string; link: string }[]) => {
    if (items.length === 0) return;
    const el = area.current;
    const caret = el?.selectionStart;
    const text = items.map((item) => item.link).join("\n");
    setBody((value) => {
      const pos = caret ?? value.length;
      return `${value.slice(0, pos)}${text}${value.slice(pos)}`.slice(0, 65536);
    });
    requestAnimationFrame(() => el?.focus());
  };
  const uploadAndInsert = async (files: File[]) => {
    const uploaded = await upload(files);
    insertLinks(
      uploaded.map((a) => ({
        name: a.name,
        link: attachmentMarkdown(a, api.attachments.url(orgId, a.id)),
      })),
    );
  };
  const onPaste = (event: ClipboardEvent<HTMLTextAreaElement>) => {
    const images = filesOf(event.clipboardData.files).filter((file) =>
      file.type.startsWith("image/"),
    );
    if (images.length === 0) return;
    event.preventDefault();
    void uploadAndInsert(
      images.map((file) => namedPastedFile(file, t("attachments.pastedImage"))),
    );
  };

  const send = useMutation({
    mutationFn: ({
      text,
      id,
      fromDraft,
    }: {
      text: string;
      id: string;
      fromDraft: boolean;
    }) => {
      // 只提交正文里还保留着的提及
      const kept = fromDraft
        ? mentions.filter((m) => text.includes(`@${m.token ?? m.email ?? ""}`))
        : [];
      return api.comments.create(orgId, task.space_id, task.id, {
        body: text,
        client_comment_id: id,
        ...(replyToId ? { reply_to_id: replyToId } : {}),
        ...(kept.length
          ? {
              mentions: kept.map((m) =>
                m.kind === "agent" ? { agent_id: m.id } : { user_id: m.id },
              ),
            }
          : {}),
      });
    },
    onSuccess: (saved, { fromDraft }) => {
      if (fromDraft) {
        if (replyToId) drafts?.delete(replyToId);
        setBody("");
        setMentions([]);
        setClientId(crypto.randomUUID());
      } else {
        quickAttempt.current = null;
      }
      const timeline = keys.taskSub(orgId, task.id, "timeline");
      if (replyToId) {
        // Update the topic immediately. The activity tab keeps the task
        // snapshot until the timeline is opened/refreshed again.
        const roots = [...timeline, "roots"];
        const author = people.find((p) => p.id === saved.author_user_id);
        const preview = {
          id: saved.id,
          author_user_id: saved.author_user_id,
          author_name: author
            ? personName(author)
            : me?.full_name || me?.email || "",
          body: saved.body.slice(0, 180),
        };
        queryClient.setQueryData<
          InfiniteData<{
            items: TimelineItem[];
            next_cursor: string | null;
            total?: number;
          }>
        >(
          roots,
          (old) =>
            old && {
              ...old,
              pages: old.pages.map((page) => ({
                ...page,
                items: page.items.map((item) => {
                  const c = item.comment;
                  if (
                    c?.id !== replyToId ||
                    c.recent_replies?.some((r) => r.id === saved.id)
                  )
                    return item;
                  return {
                    ...item,
                    comment: {
                      ...c,
                      reply_count: (c.reply_count ?? 0) + 1,
                      recent_replies: [
                        ...(c.recent_replies ?? []),
                        preview,
                      ].slice(-5),
                    },
                  };
                }),
              })),
            },
        );
        void queryClient.invalidateQueries({
          queryKey: roots,
          refetchType: "none",
        });
        void queryClient.invalidateQueries({
          queryKey: [...timeline, replyToId],
        });
      } else {
        void queryClient.invalidateQueries({ queryKey: timeline });
      }
      void queryClient.invalidateQueries({
        queryKey: keys.taskSub(orgId, task.id, "followers"),
      });
    },
    onError: (error) => {
      if (isApiError(error, "THREAD_RESOLVED"))
        void queryClient.invalidateQueries({
          queryKey: keys.taskSub(orgId, task.id, "timeline"),
        });
      toast(taskErrorMessage(t, error) || t("tasks.detail.commentFailed"), {
        tone: "error",
      });
    },
  });

  const submit = (text = body, fromDraft = true) => {
    text = text.trim();
    if (readOnly || send.isPending || uploading.length > 0 || !text) return;
    if (!fromDraft && quickAttempt.current?.body !== text)
      quickAttempt.current = { body: text, id: crypto.randomUUID() };
    send.mutate({
      text,
      fromDraft,
      id: fromDraft ? clientId : quickAttempt.current!.id,
    });
  };
  const insertQuickReply = (text: string) => {
    const el = area.current;
    const start = el?.selectionStart ?? body.length;
    const end = el?.selectionEnd ?? body.length;
    setBody(`${body.slice(0, start)}${text}${body.slice(end)}`.slice(0, 65536));
    requestAnimationFrame(() => {
      el?.focus();
      el?.setSelectionRange(start + text.length, start + text.length);
    });
  };

  const slashItems: Array<{
    id: string;
    label: string;
    icon: IconName;
    insert?: string;
    caretOffset?: number;
    action?: () => void;
  }> = [
    {
      id: "text",
      label: t("docs.editor.slash.text"),
      icon: "type",
      insert: "",
    },
    ...[1, 2, 3].map((level) => ({
      id: `heading-${level}`,
      label: t("docs.editor.slash.heading", { level }),
      icon: "type" as IconName,
      insert: `${"#".repeat(level)} `,
    })),
    {
      id: "bullet",
      label: t("docs.editor.slash.bulletList"),
      icon: "list",
      insert: "- ",
    },
    {
      id: "ordered",
      label: t("docs.editor.slash.orderedList"),
      icon: "list-ordered",
      insert: "1. ",
    },
    {
      id: "todo",
      label: t("docs.editor.slash.taskList"),
      icon: "square-check",
      insert: "- [ ] ",
    },
    {
      id: "code",
      label: t("docs.editor.slash.codeBlock"),
      icon: "code",
      insert: "```\n\n```",
      caretOffset: 4,
    },
    {
      id: "quote",
      label: t("docs.editor.slash.quote"),
      icon: "quote",
      insert: "> ",
    },
    {
      id: "attachment",
      label: t("docs.editor.slash.attachment"),
      icon: "paperclip",
      action: () => fileInput.current?.click(),
    },
  ];
  const filteredSlashItems = slashItems.filter((item) =>
    item.label.toLowerCase().includes(slash?.query.toLowerCase() ?? ""),
  );
  const agentMentionOptions = (directory.data?.entries ?? [])
    .filter(
      (entry): entry is AgentDirectoryEntry =>
        entry.type === "agent" &&
        entry.space_id === task.space_id &&
        !entry.archived_at,
    )
    .map((agent) => ({ type: "agent" as const, agent }));
  const mentionOptions = [
    ...people.map((person) => ({ type: "user" as const, person })),
    ...agentMentionOptions,
  ].filter((option) => {
    const label =
      option.type === "user"
        ? `${personName(option.person)} ${option.person.email}`
        : `${option.agent.name} ${option.agent.title}`;
    return label.toLowerCase().includes(mention?.query.toLowerCase() ?? "");
  });
  const mentionIndex = Math.min(
    activeMention,
    Math.max(0, mentionOptions.length - 1),
  );
  const slashIndex = Math.min(
    activeSlash,
    Math.max(0, filteredSlashItems.length - 1),
  );
  const updateSlash = (element: HTMLTextAreaElement) => {
    const caret = element.selectionStart;
    const match =
      element.selectionEnd === caret
        ? element.value.slice(0, caret).match(/(?:^|\s)\/([^\s/]*)$/)
        : null;
    setSlash(
      match
        ? { start: caret - match[1]!.length - 1, end: caret, query: match[1]! }
        : null,
    );
    setActiveSlash(0);
    if (match) {
      setMention(null);
      setQuickOpen(false);
      setAttachOpen(false);
    }
  };
  const updateMention = (element: HTMLTextAreaElement) => {
    if (composing.current || readOnly || send.isPending) return;
    const caret = element.selectionStart;
    const match =
      element.selectionEnd === caret
        ? element.value
            .slice(0, caret)
            .match(/(?:^|[^a-z0-9_.+@-])@([^\s@]*)$/i)
        : null;
    setMention(
      match
        ? { start: caret - match[1]!.length - 1, end: caret, query: match[1]! }
        : null,
    );
    setActiveMention(0);
    if (match) {
      setSlash(null);
      setQuickOpen(false);
      setAttachOpen(false);
    }
  };
  const insertSlash = (item: (typeof slashItems)[number]) => {
    if (!slash) return;
    const value = item.insert ?? "";
    const caret = slash.start + (item.caretOffset ?? value.length);
    pendingCaret.current = caret;
    setBody(
      `${body.slice(0, slash.start)}${value}${body.slice(slash.end)}`.slice(
        0,
        65536,
      ),
    );
    setSlash(null);
    setMention(null);
    item.action?.();
  };
  const insertMention = (option: (typeof mentionOptions)[number]) => {
    if (!mention) return;
    const isAgent = option.type === "agent";
    const token = isAgent ? option.agent.name : option.person.email;
    const text = `@${token} `;
    const caret = mention.start + text.length;
    pendingCaret.current = caret;
    setBody(
      `${body.slice(0, mention.start)}${text}${body.slice(mention.end)}`.slice(
        0,
        65536,
      ),
    );
    setMention(null);
    setMentions((prev) =>
      prev.some(
        (m) =>
          m.id === (isAgent ? option.agent.id : option.person.id) &&
          (m.kind ?? "user") === option.type,
      )
        ? prev
        : [
            ...prev,
            isAgent
              ? { id: option.agent.id, kind: "agent", token }
              : { id: option.person.id, kind: "user", email: token, token },
          ],
    );
  };

  return (
    <ReadOnlyTip block>
      <PromptInput
        value={body}
        onValueChange={setBody}
        maxHeight={180}
        disabled={readOnly || send.isPending}
        data-comment-composer={
          inline ? "inline" : replyToId ? "reply" : "comment"
        }
        className={cn(
          "w-full shadow-none! focus-within:border-line-strong!",
          inline
            ? "rounded-none! border-0! border-t! px-4! py-2!"
            : "rounded-xl! p-2!",
        )}
      >
        <div className={cn("flex items-start gap-2", !inline && "px-2 pt-1")}>
          {inline && (
            <Avatar
              name={me?.full_name || me?.email || "?"}
              src={me?.avatar_url}
              size={20}
            />
          )}
          <PromptInputTextarea
            ref={area}
            disabled={readOnly || send.isPending}
            aria-label={t(
              replyToId
                ? "tasks.detail.replyPlaceholder"
                : "tasks.detail.commentPlaceholder",
            )}
            placeholder={t(
              replyToId
                ? "tasks.detail.replyPlaceholder"
                : "tasks.detail.commentPlaceholder",
            )}
            rows={1}
            maxLength={65536}
            onPaste={onPaste}
            onChange={(event) => {
              updateSlash(event.target);
              updateMention(event.target);
            }}
            onSelect={(event) => {
              updateSlash(event.currentTarget);
              updateMention(event.currentTarget);
            }}
            onCompositionStart={() => {
              composing.current = true;
              setMention(null);
              setSlash(null);
            }}
            onCompositionEnd={(event) => {
              composing.current = false;
              updateSlash(event.currentTarget);
              updateMention(event.currentTarget);
            }}
            aria-controls={mention ? mentionId : slash ? slashId : undefined}
            aria-autocomplete="list"
            aria-activedescendant={
              mention && mentionOptions[mentionIndex]
                ? `${mentionId}-${mentionOptions[mentionIndex]!.type === "user" ? mentionOptions[mentionIndex]!.person.id : mentionOptions[mentionIndex]!.agent.id}`
                : slash && filteredSlashItems[slashIndex]
                  ? `${slashId}-${filteredSlashItems[slashIndex]!.id}`
                  : undefined
            }
            onKeyDown={(event) => {
              if (
                composing.current ||
                event.nativeEvent.isComposing ||
                event.keyCode === 229
              )
                return;
              if (slash) {
                if (event.key === "ArrowDown" || event.key === "ArrowUp") {
                  event.preventDefault();
                  if (filteredSlashItems.length)
                    setActiveSlash(
                      (slashIndex +
                        (event.key === "ArrowDown" ? 1 : -1) +
                        filteredSlashItems.length) %
                        filteredSlashItems.length,
                    );
                  return;
                }
                if (
                  event.key === "Enter" &&
                  !event.metaKey &&
                  !event.ctrlKey &&
                  !event.shiftKey
                ) {
                  event.preventDefault();
                  const item = filteredSlashItems[slashIndex];
                  if (item) insertSlash(item);
                  return;
                }
              }
              if (mention) {
                if (event.key === "ArrowDown" || event.key === "ArrowUp") {
                  event.preventDefault();
                  if (mentionOptions.length)
                    setActiveMention(
                      (mentionIndex +
                        (event.key === "ArrowDown" ? 1 : -1) +
                        mentionOptions.length) %
                        mentionOptions.length,
                    );
                  return;
                }
                if (
                  event.key === "Enter" &&
                  !event.metaKey &&
                  !event.ctrlKey &&
                  !event.shiftKey
                ) {
                  event.preventDefault();
                  const option = mentionOptions[mentionIndex];
                  if (option) insertMention(option);
                  return;
                }
              }
              // ⌘/Ctrl+Enter 始终发送；其余按账号的发送方式（Enter 或 Shift+Enter）
              const commandSend =
                event.key === "Enter" &&
                (event.metaKey || event.ctrlKey) &&
                !event.nativeEvent.isComposing;
              if (
                (commandSend || isSendKey(event, sendShortcut)) &&
                body.trim() &&
                !readOnly
              ) {
                event.preventDefault();
                submit();
              }
            }}
            className="min-h-6 min-w-0"
          />
        </div>
        <input
          ref={fileInput}
          type="file"
          multiple
          hidden
          disabled={readOnly || send.isPending}
          data-testid={
            replyToId ? "reply-attachment-input" : "comment-attachment-input"
          }
          onChange={(event) => {
            const files = filesOf(event.target.files);
            event.target.value = "";
            void uploadAndInsert(files);
          }}
        />
        <PromptInputActions className="shrink-0 justify-end">
          <IconButton
            ref={attachAnchor}
            label={t(
              replyToId ? "attachments.add" : "attachments.insertIntoComment",
            )}
            icon={<Icon name="paperclip" />}
            size="28"
            disabled={readOnly || send.isPending}
            aria-haspopup={!replyToId ? "dialog" : undefined}
            aria-expanded={!replyToId ? attachOpen : undefined}
            onClick={() => {
              setMention(null);
              setSlash(null);
              setQuickOpen(false);
              if (replyToId) fileInput.current?.click();
              else setAttachOpen((open) => !open);
            }}
          />
          <IconButton
            ref={quickAnchor}
            label={t("quickReplies.menuLabel")}
            icon={<Icon name="openfox-clipboard-task" />}
            size="28"
            disabled={readOnly || send.isPending}
            aria-haspopup="dialog"
            aria-expanded={quickOpen}
            onClick={() => {
              setMention(null);
              setSlash(null);
              setAttachOpen(false);
              setQuickOpen((open) => !open);
            }}
          />
          <button
            type="button"
            aria-label={t(
              !replyToId
                ? "tasks.detail.commentSend"
                : inline
                  ? "tasks.detail.submitReply"
                  : "tasks.detail.submitTopicComment",
            )}
            title={t(
              !replyToId
                ? "tasks.detail.commentSend"
                : inline
                  ? "tasks.detail.submitReply"
                  : "tasks.detail.submitTopicComment",
            )}
            className={cn(
              "inline-flex shrink-0 items-center justify-center rounded-full bg-fg-default text-fg-inverse disabled:opacity-25",
              inline ? "size-6" : "size-7",
            )}
            disabled={
              readOnly || !body.trim() || send.isPending || uploading.length > 0
            }
            onClick={() => submit()}
          >
            <Icon name="openfox-arrow-up" size={16} />
          </button>
        </PromptInputActions>
        {quickOpen && (
          <QuickReplyMenu
            orgId={orgId}
            open={quickOpen && !readOnly && !send.isPending}
            onOpenChange={setQuickOpen}
            anchorRef={quickAnchor}
            onPick={insertQuickReply}
            onSend={(text) => submit(text, false)}
            onManage={() => setManage("list")}
            onCreate={() => setManage("create")}
          />
        )}
        {manage && (
          <QuickReplyManager
            orgId={orgId}
            open
            initialCreate={manage === "create"}
            onClose={() => setManage(null)}
          />
        )}
        {attachOpen && (
          <SearchMenu
            open={attachOpen && !readOnly && !send.isPending}
            onOpenChange={setAttachOpen}
            anchorRef={attachAnchor}
            placement="top-end"
            className="max-w-[calc(100vw-16px)]"
            label={t("attachments.insertIntoComment")}
            groups={[
              {
                options: [
                  {
                    value: "upload",
                    label: t("attachments.insertUpload"),
                    icon: <Icon name="plus" size={16} />,
                  },
                  ...(attachments.data ?? []).map((a) => ({
                    value: `attachment:${a.id}`,
                    label: a.name,
                    icon: (
                      <Icon
                        name={a.previewable ? "image" : "file-text"}
                        size={16}
                      />
                    ),
                  })),
                ],
              },
            ]}
            selected={[]}
            onSelect={(value) => {
              if (value === "upload") {
                fileInput.current?.click();
                return;
              }
              const found = attachments.data?.find(
                (a) => `attachment:${a.id}` === value,
              );
              if (found)
                insertLinks([
                  {
                    name: found.name,
                    link: attachmentMarkdown(
                      found,
                      api.attachments.url(orgId, found.id),
                    ),
                  },
                ]);
            }}
          />
        )}
        {mention && !readOnly && !send.isPending && (
          <Popover
            open
            onOpenChange={(open) => {
              if (!open) setMention(null);
            }}
            anchorRef={area}
            placement="top-start"
            aria-label={t("tasks.detail.mention")}
            className="w-[280px] max-w-[calc(100vw-16px)]"
          >
            <div
              id={mentionId}
              role="listbox"
              aria-label={t("tasks.detail.mention")}
              className="max-h-60 overflow-y-auto"
            >
              {mentionOptions.map((option, index) => (
                <div
                  key={`${option.type}:${option.type === "user" ? option.person.id : option.agent.id}`}
                  id={`${mentionId}-${option.type === "user" ? option.person.id : option.agent.id}`}
                  role="option"
                  aria-selected={index === mentionIndex}
                  onMouseDown={(event) => event.preventDefault()}
                  onMouseMove={() => setActiveMention(index)}
                  onClick={() => insertMention(option)}
                  ref={(element) => {
                    if (index === mentionIndex)
                      element?.scrollIntoView?.({ block: "nearest" });
                  }}
                  className={cn(
                    "flex min-h-9 cursor-pointer items-center gap-2 rounded-lg px-2 py-1.5 text-body",
                    index === mentionIndex && "bg-surface-active",
                  )}
                >
                  {option.type === "user" ? (
                    <Avatar
                      name={personName(option.person)}
                      src={option.person.avatar_url}
                      size={20}
                    />
                  ) : (
                    <AgentAvatar
                      name={option.agent.name}
                      avatar={option.agent.avatar}
                      size={20}
                    />
                  )}
                  <span className="min-w-0 truncate">
                    {option.type === "user"
                      ? personName(option.person)
                      : option.agent.name}
                  </span>
                  {option.type === "user" && option.person.name && (
                    <span className="min-w-0 truncate text-caption text-fg-muted">
                      {option.person.email}
                    </span>
                  )}
                  {option.type === "agent" && (
                    <span className="min-w-0 truncate text-caption text-fg-muted">
                      Agent
                    </span>
                  )}
                </div>
              ))}
              {!mentionOptions.length && (
                <p className="px-2 py-3 text-body-sm text-fg-muted">
                  {t("ui.searchMenu.empty")}
                </p>
              )}
            </div>
          </Popover>
        )}
        {slash && !mention && !readOnly && !send.isPending && (
          <Popover
            open
            onOpenChange={(open) => {
              if (!open) setSlash(null);
            }}
            anchorRef={area}
            placement="top-start"
            aria-label={t("docs.editor.slash.label")}
            className="w-[248px] max-w-[calc(100vw-16px)]"
          >
            <div
              id={slashId}
              role="listbox"
              aria-label={t("docs.editor.slash.label")}
              className="max-h-60 overflow-y-auto p-1"
              onMouseDown={(event) => event.preventDefault()}
            >
              {filteredSlashItems.map((item, index) => (
                <button
                  key={item.id}
                  type="button"
                  role="option"
                  aria-selected={index === slashIndex}
                  id={`${slashId}-${item.id}`}
                  onMouseEnter={() => setActiveSlash(index)}
                  onClick={() => insertSlash(item)}
                  className={cn(
                    "flex h-[34px] w-full items-center gap-2 rounded-lg px-2.5 text-left text-body",
                    index === slashIndex
                      ? "bg-surface-active"
                      : "hover:bg-surface-hover",
                  )}
                >
                  <Icon name={item.icon} size={14} />
                  <span className="truncate">{item.label}</span>
                </button>
              ))}
              {!filteredSlashItems.length && (
                <p className="px-2.5 py-2 text-body-sm text-fg-muted">
                  {t("docs.editor.slash.empty")}
                </p>
              )}
            </div>
          </Popover>
        )}
      </PromptInput>
    </ReadOnlyTip>
  );
}
