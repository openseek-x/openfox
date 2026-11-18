import type { ConversationMessage } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Icon, IconButton } from "@openfox/ui";

export const REPLY_PREVIEW_LENGTH = 200;

export function replyPreviewText(body: string) {
  const characters = Array.from(body);
  return {
    body: characters.slice(0, REPLY_PREVIEW_LENGTH).join(""),
    truncated: characters.length > REPLY_PREVIEW_LENGTH,
  };
}

export function ReplyPreview({
  body,
  authorKind,
  truncated,
  legacy,
  onCancel,
}: {
  body: string;
  authorKind: string;
  truncated?: boolean;
  legacy?: boolean;
  onCancel?: () => void;
}) {
  const t = useT();
  const preview = replyPreviewText(body);
  return (
    <blockquote
      aria-label={t("chat.reply.preview")}
      className="flex min-w-0 gap-2 rounded-md border-l-2 border-line-default bg-surface-muted px-3 py-2 text-body-sm"
    >
      <div className="min-w-0 flex-1">
        <p className="text-caption text-fg-muted">
          {t(
            authorKind === "AGENT"
              ? "chat.reply.agent"
              : authorKind === "SYSTEM"
                ? "chat.reply.system"
                : "chat.reply.user",
          )}
          {legacy && ` · ${t("chat.reply.legacy")}`}
        </p>
        <p className="whitespace-pre-wrap break-words">
          {preview.body}
          {(preview.truncated || truncated) && "…"}
        </p>
      </div>
      {onCancel && (
        <IconButton
          label={t("chat.reply.cancel")}
          icon={<Icon name="x" size={14} />}
          size="28"
          onClick={onCancel}
        />
      )}
    </blockquote>
  );
}

export function MessageReply({ message }: { message: ConversationMessage }) {
  const t = useT();
  if (!message.reply_to_id) return null;
  if (!message.reply)
    return (
      <p className="text-caption text-fg-muted">
        {t("chat.reply.unavailable")}
      </p>
    );
  return (
    <ReplyPreview
      body={message.reply.body}
      authorKind={message.reply.author_kind}
      truncated={message.reply.truncated}
      legacy={!message.reply.snapshot}
    />
  );
}
