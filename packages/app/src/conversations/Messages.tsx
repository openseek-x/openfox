import { assistantActions } from "./assistantActions";
import { useState, type ReactNode } from "react";

import type { ConversationMessage } from "@openfox/api";
import { useI18n, useT } from "@openfox/i18n";
import {
  Icon,
  IconButton,
  Message,
  MessageContent,
  MessageActions,
  cn,
} from "@openfox/ui";

import { KIND_BADGE, attachmentKind, formatBytes } from "../attachments/model";
import { useApi } from "../context";
import { copyText } from "../tasks/parts";
import { formatDuration, useRun } from "./steps";
import { ChatMarkdown } from "./ChatMarkdown";

// 消息气泡：Message/User（右侧气泡）、Message/Agent（Markdown 正文 + 操作 + Meta）、系统消息。

function timeOf(value: string, locale: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  return new Intl.DateTimeFormat(locale, {
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  }).format(date);
}

function CopyButton({ text }: { text: string }) {
  const t = useT();
  const [copied, setCopied] = useState(false);
  return (
    <IconButton
      label={copied ? t("chat.messages.copied") : t("chat.messages.copy")}
      icon={<Icon name={copied ? "copy-check" : "openfox-copy"} size={14} />}
      size="28"
      variant="ghost"
      className="size-6 text-fg-muted"
      onClick={async () => {
        if (await copyText(text)) {
          setCopied(true);
          window.setTimeout(() => setCopied(false), 1500);
        }
      }}
    />
  );
}

/**
 * 消息附件卡片：左侧类型徽标（Word 蓝 W、Excel 绿 X、PDF 红 PDF，
 * 其余中性底色加文件图标；图片显示缩略图），中间文件名与「类型 • 大小」，右侧「打开」与下载图标。
 */
export function MessageAttachments({
  message,
}: {
  message: ConversationMessage;
}) {
  const t = useT();
  const api = useApi();
  const items = message.attachments ?? [];
  if (items.length === 0) return null;
  const orgId = message.organization_id;
  return (
    <ul
      aria-label={t("attachments.title")}
      className="flex w-full max-w-[85%] flex-col items-end gap-2"
    >
      {items.map((attachment) => {
        const kind = attachmentKind(attachment.name);
        const badge = KIND_BADGE[kind];
        const url = api.attachments.url(orgId, attachment.id);
        // 图片在新标签页里预览（inline），其余直接下载
        const openUrl = attachment.previewable
          ? api.attachments.previewUrl(orgId, attachment.id)
          : url;
        return (
          <li
            key={attachment.id}
            className="flex w-full max-w-[420px] flex-wrap items-center gap-3 rounded-xl border border-line-default bg-surface-card p-3"
          >
            {attachment.previewable ? (
              <img
                src={api.attachments.previewUrl(orgId, attachment.id)}
                alt=""
                className="size-8 shrink-0 rounded-md object-cover"
              />
            ) : (
              <span
                aria-hidden="true"
                className={cn(
                  "flex size-8 shrink-0 items-center justify-center rounded-md text-caption font-semibold",
                  badge.className,
                )}
              >
                {badge.badge || <Icon name="file-text" size={16} />}
              </span>
            )}
            <span className="flex min-w-16 flex-1 flex-col">
              <span className="truncate text-body-sm font-medium">
                {attachment.name}
              </span>
              <span className="text-caption text-fg-muted">
                {t(`attachments.kinds.${kind}`)} •{" "}
                {formatBytes(attachment.size_bytes)}
              </span>
            </span>
            <a
              href={openUrl}
              target="_blank"
              rel="noopener noreferrer"
              aria-label={t("attachments.openFile", { name: attachment.name })}
              className="flex h-7 shrink-0 items-center rounded-md border border-line-default px-2 text-body-sm hover:bg-surface-hover"
            >
              {t("attachments.open")}
            </a>
            <a
              href={url}
              aria-label={t("attachments.download", { name: attachment.name })}
              className="flex size-7 shrink-0 items-center justify-center rounded-md text-fg-muted hover:bg-surface-muted"
            >
              <Icon name="download" size={16} />
            </a>
          </li>
        );
      })}
    </ul>
  );
}

export function UserMessage({ message }: { message: ConversationMessage }) {
  const t = useT();
  const { locale } = useI18n();
  // 只发附件、不带文字时服务端写入的默认正文不重复显示
  const attachmentOnly =
    (message.attachments?.length ?? 0) > 0 && message.body === "请查看附件。";
  const badge = message.assistant_action
    ? assistantActions[message.assistant_action]
    : undefined;
  return (
    <Message data-seq={message.seq} className="items-end">
      {!attachmentOnly && (
        <MessageContent>
          {badge && (
            <span className="mx-0.5 inline-flex h-[22px] -translate-y-px items-center gap-1 rounded-[5px] bg-surface-card px-1.5 align-middle text-caption shadow-card">
              <Icon name={badge.icon} size={12} />
              {t(badge.label)}
            </span>
          )}
          {message.body}
        </MessageContent>
      )}
      <MessageAttachments message={message} />
      <MessageActions className="opacity-70 transition-opacity group-hover/message:opacity-100 group-focus-within/message:opacity-100">
        <span>{timeOf(message.created_at, locale)}</span>
        <CopyButton text={message.body} />
      </MessageActions>
    </Message>
  );
}

/** Meta 行：耗时 · 时间 · 模型（耗时来自对应 Run，与执行时间线共用缓存） */
function AgentMeta({
  orgId,
  message,
  model,
}: {
  orgId: string;
  message: ConversationMessage;
  model?: string | null;
}) {
  const t = useT();
  const { locale } = useI18n();
  const run = useRun(orgId, message.run_id ?? "");
  const duration = run.data?.duration_ms;
  const parts = [
    typeof duration === "number"
      ? t("chat.messages.meta", { duration: formatDuration(duration, t) })
      : null,
    timeOf(message.created_at, locale),
    model && model !== "default" ? model : null,
  ].filter(Boolean);
  return <span>{parts.join(" · ")}</span>;
}

export function AgentMessage({
  orgId,
  message,
  model,
  authorName,
  children,
}: {
  orgId: string;
  message: ConversationMessage;
  model?: string | null;
  /** 小队对话：显示这条消息是哪位成员 Agent 回复的 */
  authorName?: string;
  /** 草稿卡等附加内容 */
  children?: ReactNode;
}) {
  return (
    <Message data-seq={message.seq}>
      {authorName && (
        <span className="text-caption font-medium text-fg-secondary">
          {authorName}
        </span>
      )}
      {message.body.trim() && (
        <MessageContent variant="assistant">
          <ChatMarkdown source={message.body} />
        </MessageContent>
      )}
      {children}
      <MessageActions>
        {message.body.trim() && <CopyButton text={message.body} />}
        <AgentMeta orgId={orgId} message={message} model={model} />
      </MessageActions>
    </Message>
  );
}

export function SystemMessage({ message }: { message: ConversationMessage }) {
  return (
    <p
      data-seq={message.seq}
      className={cn(
        "self-center rounded-full bg-surface-muted px-3 py-1 text-caption text-fg-muted",
      )}
    >
      {message.body}
    </p>
  );
}
