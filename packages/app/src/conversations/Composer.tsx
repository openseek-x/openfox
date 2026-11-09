import {
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type ReactNode,
  type ClipboardEvent,
} from "react";

import { useT } from "@openfox/i18n";
import { ATTACHMENT_MAX_BYTES, MESSAGE_ATTACHMENT_LIMIT } from "@openfox/api";
import {
  Icon,
  IconButton,
  PromptInput,
  PromptInputActions,
  PromptInputTextarea,
  Spinner,
  Popover,
  Tooltip,
  cn,
  useToast,
  type SearchMenuOption,
} from "@openfox/ui";

import { AgentAvatar } from "../agents/AgentAvatar";
import { filesOf, formatBytes, namedPastedFile } from "../attachments/model";
import { runtimeLabel } from "../agents/model";
import { isSendKey } from "../me/shortcuts";
import { useSendShortcut } from "../me/queries";
import { personName, type Person } from "../tasks/queries";
import {
  clearDraft,
  readDraft,
  saveDraft,
  subscribeDraft,
  takeDraftFocus,
  type DraftMention,
} from "./drafts";
import { assistantActions, type AssistantAction } from "./assistantActions";
import { buildMentions } from "./model";
import {
  OverrideControls,
  type OverrideControlsProps,
} from "./OverrideControls";
import { QuickReplyManager, QuickReplyMenu } from "./QuickReplies";

export interface ComposerAgent {
  id: string;
  name: string;
  avatar?: Record<string, unknown> | null;
  runtime?: string | null;
  model?: string | null;
}

export interface ComposerSubmit {
  assistantAction?: AssistantAction;
  body: string;
  /** 待上传并随消息发送的文件；上传由调用方完成，失败时草稿与文件都保留 */
  files: File[];
  mentions: Array<{ user_id: string } | { agent_id: string }>;
}

type Picked = DraftMention;

/**
 * 输入框：多行，Enter 发送 / Shift+Enter 换行，@ 提及；
 * 运行中「发送」换成「停止」；Runtime / 模型 / 权限模式三个控件就地切换本对话的覆盖（8d-2），
 * 没有覆盖数据时退回只读 chip（点击打开 Agent 设置）；附件、快捷回复可用（7c / 7d）。
 * 小队对话（crew）：@ 列表是小队成员，不自动 @；没有 @ 时由队长处理（服务端规则）。
 * 发送失败时保留草稿，由调用方用同一个 client_message_id 重试。
 * 草稿（正文、已选提及、待发送附件）按 draftKey 隔离：切换会话时换成目标会话自己的草稿。
 */
export function Composer({
  draftKey,
  orgId,
  agent,
  crew,
  people,
  autoMentionAgent,
  disabledReason,
  running,
  stopping,
  sending,
  onSubmit,
  onStop,
  onOpenSettings,
  overrides,
  placeholder,
  showAssistantActions,
  landingComposer,
  workDirectory,
  notice,
}: {
  /** 草稿键：会话 ID，新会话用 newDraftKey；切换会话时输入框换成该键下的草稿 */
  draftKey: string;
  /** 快捷回复需要组织 ID；缺省时快捷回复按钮禁用 */
  orgId?: string;
  agent: ComposerAgent;
  /** 小队对话：可 @ 的成员 Agent（含队长）；设置后输入框按小队模式工作 */
  crew?: { members: ComposerAgent[]; leaderName: string };
  /** 可提及的成员（共享会话的成员）；为空时只能 @ Agent */
  people: Person[];
  /** 自己是会话所有者：每条消息自动 @ Agent */
  autoMentionAgent: boolean;
  /** 不能发送的原因（会话已完成等） */
  disabledReason?: string | null;
  running: boolean;
  stopping?: boolean;
  sending: boolean;
  /** 返回 true 表示已发送（清空草稿） */
  onSubmit: (input: ComposerSubmit) => Promise<boolean>;
  onStop?: () => void;
  onOpenSettings?: () => void;
  /** Runtime / 模型 / 权限模式的就地切换；小队对话与拿不到默认值时为空 */
  overrides?: OverrideControlsProps | null;
  placeholder?: string;
  showAssistantActions?: boolean;
  landingComposer?: boolean;
  workDirectory?: ReactNode;
  notice?: ReactNode;
}) {
  const t = useT();
  const sendShortcut = useSendShortcut();
  const [body, setBody] = useState(() => readDraft(draftKey)?.body ?? "");
  const [action, setAction] = useState<AssistantAction | undefined>(
    () => readDraft(draftKey)?.assistantAction,
  );
  const chip = useRef<HTMLSpanElement>(null);
  const selected = action ? assistantActions[action] : undefined;
  const [indent, setIndent] = useState(0);
  useLayoutEffect(() => {
    if (!action || !chip.current) return;
    const element = chip.current;
    const measure = () => setIndent(element.offsetWidth + 4);
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    return () => observer.disconnect();
  }, [action]);
  const [picked, setPicked] = useState<Picked[]>(
    () => readDraft(draftKey)?.picked ?? [],
  );
  const [mention, setMention] = useState<{
    start: number;
    end: number;
    query: string;
  } | null>(null);
  const [activeMention, setActiveMention] = useState(0);
  const mentionId = useId();
  const composing = useRef(false);
  const pendingCaret = useRef<number | null>(null);
  const [files, setFiles] = useState<File[]>(
    () => readDraft(draftKey)?.files ?? [],
  );
  // 当前状态属于哪个草稿键。键变化（切换会话）时在渲染中换成新键的草稿，
  // 旧键的草稿已由下面的 effect 随每次修改写入，不会带到新会话。
  const [loadedKey, setLoadedKey] = useState(draftKey);
  if (loadedKey !== draftKey) {
    const next = readDraft(draftKey);
    setLoadedKey(draftKey);
    setAction(next?.assistantAction);
    setBody(next?.body ?? "");
    setPicked(next?.picked ?? []);
    setFiles(next?.files ?? []);
    setMention(null);
  }
  // 输入框外追加到草稿（快捷指令「插入到输入框」）：换成存储里的草稿并把焦点放到末尾。
  // 挂载 / 换键时也检查一次聚焦请求（先写草稿再打开会话的情况）。放在保存草稿的 effect 之前：
  // 即使写入发生在渲染与提交之间，这里读到的是新草稿，随后的重渲染会把它写回。
  useEffect(() => {
    const reload = (focus: boolean) => {
      const next = readDraft(loadedKey);
      setAction(next?.assistantAction);
      setBody(next?.body ?? "");
      setPicked(next?.picked ?? []);
      setFiles(next?.files ?? []);
      if (!focus) return;
      requestAnimationFrame(() => {
        const el = area.current;
        if (!el) return;
        el.focus();
        el.setSelectionRange(el.value.length, el.value.length);
      });
    };
    if (takeDraftFocus(loadedKey)) reload(true);
    return subscribeDraft(loadedKey, () => reload(takeDraftFocus(loadedKey)));
  }, [loadedKey]);
  useEffect(() => {
    saveDraft(loadedKey, {
      body,
      picked,
      files,
      ...(action && { assistantAction: action }),
    });
  }, [loadedKey, body, picked, files, action]);
  // 发送是异步的：完成时用它判断输入框是否还停留在发送时的会话
  const currentKey = useRef(draftKey);
  currentKey.current = loadedKey;
  const [quickOpen, setQuickOpen] = useState(false);
  const [manage, setManage] = useState<"create" | "manage" | null>(null);
  const toast = useToast();
  const fileInput = useRef<HTMLInputElement | null>(null);
  const area = useRef<HTMLTextAreaElement | null>(null);
  const quickAnchor = useRef<HTMLButtonElement | null>(null);
  const disabled = Boolean(disabledReason);
  const chooseAction = (choice?: AssistantAction) => {
    setAction(choice);
    area.current?.focus();
  };
  const canSend =
    !disabled && !sending && (body.trim().length > 0 || files.length > 0);
  useLayoutEffect(() => {
    if (pendingCaret.current === null) return;
    const caret = pendingCaret.current;
    pendingCaret.current = null;
    area.current?.focus();
    area.current?.setSelectionRange(caret, caret);
  }, [body]);
  useEffect(() => {
    setQuickOpen(false);
    setManage(null);
  }, [loadedKey]);

  /** 选择或粘贴的文件加入待发送列表：超过大小或数量上限的丢弃并提示 */
  const addFiles = (incoming: File[]) => {
    const accepted: File[] = [];
    for (const file of incoming) {
      if (file.size > ATTACHMENT_MAX_BYTES) {
        toast(t("attachments.errors.tooLarge", { name: file.name }), {
          tone: "error",
        });
        continue;
      }
      if (files.length + accepted.length >= MESSAGE_ATTACHMENT_LIMIT) {
        toast(
          t("attachments.messageLimit", { max: MESSAGE_ATTACHMENT_LIMIT }),
          { tone: "error" },
        );
        break;
      }
      accepted.push(file);
    }
    if (accepted.length > 0) setFiles((prev) => [...prev, ...accepted]);
  };
  const onPaste = (event: ClipboardEvent<HTMLTextAreaElement>) => {
    const images = filesOf(event.clipboardData.files).filter((file) =>
      file.type.startsWith("image/"),
    );
    if (images.length === 0) return;
    event.preventDefault();
    addFiles(
      images.map((file) => namedPastedFile(file, t("attachments.pastedImage"))),
    );
  };

  const submit = async () => {
    if (!canSend) return;
    const sentKey = loadedKey;
    const text = body.trim();
    const ok = await onSubmit({
      body: text,
      ...(action && { assistantAction: action }),
      files,
      mentions: buildMentions(
        text,
        picked,
        agent.id,
        crew ? false : autoMentionAgent,
        crew?.members.map((member) => member.id),
      ),
    });
    if (ok) {
      // 只清发送的那个会话的草稿；发送期间已切到别的会话时，不动那个会话的输入
      clearDraft(sentKey);
      if (currentKey.current === sentKey) {
        setAction(undefined);
        setBody("");
        setPicked([]);
        setFiles([]);
      }
    }
  };

  const mentionAgents = crew?.members ?? [agent];
  const options: SearchMenuOption[] = [
    ...mentionAgents.map((item) => ({
      value: `agent:${item.id}`,
      label: item.name,
      icon: <AgentAvatar name={item.name} avatar={item.avatar} size={20} />,
    })),
    ...people.map((person) => ({
      value: `user:${person.id}`,
      label: personName(person),
      // 有全名时邮箱作为说明
      description: person.name ? person.email : undefined,
      keywords: person.email,
      icon: (
        <span className="flex size-5 items-center justify-center rounded-full bg-surface-muted text-caption">
          {personName(person).slice(0, 1).toUpperCase()}
        </span>
      ),
    })),
  ];
  const mentionOptions = options.filter((option) =>
    `${option.label} ${option.keywords ?? ""}`
      .toLowerCase()
      .includes(mention?.query.toLowerCase() ?? ""),
  );
  const mentionIndex = Math.min(
    activeMention,
    Math.max(0, mentionOptions.length - 1),
  );
  const updateMention = (element: HTMLTextAreaElement) => {
    if (composing.current || disabled || sending) return;
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
    if (match) setQuickOpen(false);
  };

  const insertMention = (value: string) => {
    if (!mention || disabled || sending) return;
    const [kind, id] = value.split(":") as ["user" | "agent", string];
    const name =
      kind === "agent"
        ? mentionAgents.find((item) => item.id === id)?.name
        : people.find((person) => person.id === id)?.email;
    if (!name) return;
    const text = `@${name} `;
    pendingCaret.current = mention.start + text.length;
    setBody(
      `${body.slice(0, mention.start)}${text}${body.slice(mention.end)}`.slice(
        0,
        65536,
      ),
    );
    setMention(null);
    setPicked((prev) =>
      prev.some((item) => item.kind === kind && item.id === id)
        ? prev
        : [...prev, { kind, id, name }],
    );
  };

  // 小队对话没有单一的 Runtime / 模型，不显示这两个 chip
  const runtime = crew ? null : runtimeLabel(agent.runtime);
  const model = agent.model && agent.model !== "default" ? agent.model : null;

  /** 把快捷回复正文插入光标处（有选区时替换选区），不自动发送 */
  const insertQuickReply = (text: string) => {
    if (disabled || sending) return;
    const el = area.current;
    const start = el?.selectionStart ?? body.length;
    const end = el?.selectionEnd ?? body.length;
    const next = `${body.slice(0, start)}${text}${body.slice(end)}`;
    setBody(next.slice(0, 65536));
    const caret = start + text.length;
    requestAnimationFrame(() => {
      el?.focus();
      el?.setSelectionRange(caret, caret);
    });
  };

  return (
    <>
      {showAssistantActions && !action && (
        <div className="mb-2 flex flex-wrap justify-center gap-2">
          {(["create_agent", "create_task"] as const).map((choice) => {
            const capability = assistantActions[choice];
            return (
              <button
                key={choice}
                type="button"
                aria-pressed={false}
                disabled={disabled || sending}
                onClick={() => chooseAction(choice)}
                className="flex flex-col items-start gap-0.5 rounded-lg border border-line-default bg-surface-card px-3 py-2 text-left hover:bg-surface-hover"
              >
                <span className="flex items-center gap-1.5 text-[13px] font-medium">
                  <Icon
                    name={capability.icon}
                    size={14}
                    className="text-fg-muted"
                  />
                  {t(capability.label)}
                </span>
                <span className="text-caption text-fg-muted">
                  {t(capability.hint)}
                </span>
              </button>
            );
          })}
        </div>
      )}
      {notice}
      <PromptInput
        value={body}
        onValueChange={setBody}
        disabled={disabled}
        aria-busy={sending || stopping || undefined}
      >
        <div className="relative flex">
          {selected && (
            <span
              ref={chip}
              className="absolute top-0 left-0.5 inline-flex h-[22px] items-center gap-1 rounded-full bg-surface-muted px-2 text-[0.875em] leading-5 text-fg-default"
            >
              <button
                type="button"
                onClick={() => chooseAction()}
                aria-label={t("chat.assistant.capabilities.remove", {
                  name: t(selected.label),
                })}
                className="group inline-flex size-3.5 items-center justify-center rounded-sm"
              >
                <Icon
                  name="wrench"
                  size={14}
                  className="group-hover:hidden group-focus-visible:hidden"
                />
                <Icon
                  name="x"
                  size={12}
                  className="hidden group-hover:block group-focus-visible:block"
                />
              </button>
              {t(selected.label)}
            </span>
          )}
          <PromptInputTextarea
            style={action ? { textIndent: indent } : undefined}
            ref={area}
            aria-label={t("chat.composer.label")}
            placeholder={
              disabledReason ??
              (selected ? t(selected.placeholder) : undefined) ??
              placeholder ??
              (crew
                ? t("chat.composer.crewPlaceholder", {
                    leader: crew.leaderName,
                  })
                : t("chat.composer.placeholder"))
            }
            rows={2}
            maxLength={65536}
            disabled={disabled}
            onPaste={onPaste}
            onChange={(event) => {
              updateMention(event.target);
            }}
            onSelect={(event) => updateMention(event.currentTarget)}
            onCompositionStart={() => {
              composing.current = true;
              setMention(null);
            }}
            onCompositionEnd={(event) => {
              composing.current = false;
              updateMention(event.currentTarget);
            }}
            aria-controls={mention ? mentionId : undefined}
            aria-autocomplete="list"
            aria-activedescendant={
              mention && mentionOptions[mentionIndex]
                ? `${mentionId}-${mentionOptions[mentionIndex]!.value}`
                : undefined
            }
            onKeyDown={(event) => {
              // 发送方式跟随账号偏好：Enter 发送（Shift+Enter 换行），或 Shift+Enter 发送（Enter 换行）
              if (
                composing.current ||
                event.nativeEvent.isComposing ||
                event.keyCode === 229
              )
                return;
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
                  if (option) insertMention(option.value);
                  return;
                }
              }
              if (
                action &&
                event.key === "Backspace" &&
                event.currentTarget.selectionStart === 0 &&
                event.currentTarget.selectionEnd === 0
              ) {
                event.preventDefault();
                chooseAction();
                return;
              }
              if (isSendKey(event, sendShortcut)) {
                event.preventDefault();
                void submit();
              }
            }}
            className={cn(landingComposer ? "min-h-[96px]" : "min-h-[72px]")}
          />
        </div>
        {files.length > 0 && (
          <ul
            aria-label={t("attachments.pending")}
            className="flex flex-wrap gap-1.5"
          >
            {files.map((file, index) => (
              <li
                key={`${file.name}-${index}`}
                className="flex h-7 max-w-[240px] items-center gap-1 rounded-md bg-surface-muted pr-0.5 pl-2 text-body-sm"
              >
                <Icon
                  name={file.type.startsWith("image/") ? "image" : "file-text"}
                  size={14}
                  className="shrink-0 text-fg-muted"
                />
                <span className="truncate">{file.name}</span>
                <span className="shrink-0 text-fg-muted">
                  {formatBytes(file.size)}
                </span>
                <IconButton
                  label={t("attachments.remove", { name: file.name })}
                  icon={<Icon name="x" size={12} />}
                  size="28"
                  variant="ghost"
                  className="size-5"
                  disabled={sending}
                  onClick={() =>
                    setFiles((prev) => prev.filter((_, i) => i !== index))
                  }
                />
              </li>
            ))}
          </ul>
        )}
        <PromptInputActions>
          <div className="flex shrink-0 items-center gap-1">
            <input
              ref={fileInput}
              type="file"
              multiple
              hidden
              data-testid="composer-file-input"
              onChange={(event) => {
                const picked = filesOf(event.target.files);
                event.target.value = "";
                addFiles(picked);
              }}
            />
            <IconButton
              label={t("chat.composer.attach")}
              icon={<Icon name="plus" size={18} />}
              size="28"
              variant="ghost"
              disabled={disabled || sending}
              title={t("chat.composer.attach")}
              onClick={() => fileInput.current?.click()}
            />
            {(crew || people.length > 0) && (
              <IconButton
                label={t("chat.composer.mention")}
                icon={<Icon name="at-sign" size={16} />}
                size="28"
                variant="ghost"
                disabled={disabled || sending}
                onClick={() => {
                  const start = area.current?.selectionStart ?? body.length;
                  const end = area.current?.selectionEnd ?? body.length;
                  const prefix = body.slice(0, start);
                  const text = /(?:^|\s)$/.test(prefix) ? "@" : " @";
                  const caret = start + text.length;
                  pendingCaret.current = caret;
                  setBody(`${prefix}${text}${body.slice(end)}`.slice(0, 65536));
                  setMention({ start: caret - 1, end: caret, query: "" });
                  setActiveMention(0);
                  setQuickOpen(false);
                }}
              />
            )}
          </div>
          <div className="flex min-w-0 flex-1 items-center gap-1">
            {overrides ? (
              <OverrideControls {...overrides} afterRuntime={workDirectory} />
            ) : (
              <>
                {runtime && (
                  <Tooltip
                    content={t("chat.composer.runtime", { name: runtime })}
                  >
                    <button
                      type="button"
                      onClick={onOpenSettings}
                      className="flex h-7 max-w-40 shrink-0 items-center gap-1 rounded-md px-1.5 text-body-sm text-fg-secondary hover:bg-surface-muted"
                    >
                      <Icon name="monitor" size={14} className="shrink-0" />
                      <span className="truncate">{runtime}</span>
                    </button>
                  </Tooltip>
                )}
                {workDirectory}
                <span className="flex-1" />
                {!crew && (
                  <Tooltip
                    content={t("chat.composer.model", {
                      name: model ?? t("chat.messages.modelDefault"),
                    })}
                  >
                    <button
                      type="button"
                      onClick={onOpenSettings}
                      className="flex h-7 min-w-0 max-w-40 items-center rounded-md px-1.5 text-body-sm text-fg-secondary hover:bg-surface-muted"
                    >
                      <span className="truncate">
                        {model ?? t("agents.modelDefault")}
                      </span>
                    </button>
                  </Tooltip>
                )}
              </>
            )}
          </div>
          <IconButton
            ref={quickAnchor}
            label={t("chat.composer.quickReply")}
            icon={<Icon name="clipboard" size={16} />}
            size="28"
            variant="ghost"
            disabled={disabled || sending || !orgId}
            title={t("chat.composer.quickReply")}
            aria-haspopup="dialog"
            aria-expanded={quickOpen}
            onClick={() => {
              setMention(null);
              setQuickOpen((open) => !open);
            }}
          />
          {running && onStop ? (
            <IconButton
              label={
                stopping ? t("chat.composer.stopping") : t("chat.composer.stop")
              }
              icon={<Icon name="openfox-circle-stop" size={16} />}
              size="28"
              variant="secondary"
              disabled={stopping}
              onClick={onStop}
            />
          ) : (
            <IconButton
              label={t("chat.composer.send")}
              icon={
                sending ? (
                  <Spinner size={16} />
                ) : (
                  <Icon name="openfox-arrow-up" size={18} />
                )
              }
              size="28"
              variant="round"
              className="enabled:bg-fg-default enabled:text-surface-card enabled:hover:opacity-85 disabled:bg-fg-subtle disabled:text-surface-card"
              disabled={!canSend && !(action && !disabled && !sending)}
              onClick={() => void submit()}
            />
          )}
        </PromptInputActions>
        {orgId && (
          <>
            <QuickReplyMenu
              orgId={orgId}
              open={quickOpen && !disabled && !sending}
              onOpenChange={setQuickOpen}
              anchorRef={quickAnchor}
              onPick={insertQuickReply}
              onManage={() => setManage("manage")}
              onCreate={() => setManage("create")}
            />
            {manage && (
              <QuickReplyManager
                orgId={orgId}
                open
                initialCreate={manage === "create"}
                onClose={() => setManage(null)}
              />
            )}
          </>
        )}
        {mention && !disabled && !sending && (
          <Popover
            open
            onOpenChange={(open) => {
              if (!open) setMention(null);
            }}
            anchorRef={area}
            placement="top-start"
            aria-label={t("chat.composer.mention")}
            className="w-[280px] max-w-[calc(100vw-16px)]"
          >
            <div
              id={mentionId}
              role="listbox"
              aria-label={t("chat.composer.mention")}
              className="max-h-60 overflow-y-auto"
            >
              {mentionOptions.map((option, index) => (
                <div
                  key={option.value}
                  id={`${mentionId}-${option.value}`}
                  role="option"
                  aria-selected={index === mentionIndex}
                  onMouseDown={(event) => event.preventDefault()}
                  onMouseMove={() => setActiveMention(index)}
                  onClick={() => insertMention(option.value)}
                  ref={(element) => {
                    if (index === mentionIndex)
                      element?.scrollIntoView?.({ block: "nearest" });
                  }}
                  className={cn(
                    "flex min-h-9 cursor-pointer items-center gap-2 rounded-lg px-2 py-1.5 text-body",
                    index === mentionIndex && "bg-surface-active",
                  )}
                >
                  <span aria-hidden="true" className="shrink-0">
                    {option.icon}
                  </span>
                  <span className="min-w-0 truncate">{option.label}</span>
                  {option.description && (
                    <span className="min-w-0 truncate text-caption text-fg-muted">
                      {option.description}
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
      </PromptInput>
    </>
  );
}
