import { useState } from "react";

import type { Attachment } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Dialog, Icon } from "@openfox/ui";

import { AttachmentPreview } from "../../attachments/AttachmentPreview";
import { formatBytes } from "../../attachments/model";
import { useApi } from "../../context";

// OPE-19 · 智能体正式报告评论附带的附件（HTML 报告、录像、证据清单、截图）：
// 在评论正文下按上传顺序列出；可预览的类型（图片 / 视频 / HTML）点文件名弹窗预览，其余只提供下载。
// 下载与预览沿用任务附件的组织与访问权限；这类附件随评论存在，不提供单独删除。

export function CommentAttachments({
  orgId,
  attachments,
}: {
  orgId: string;
  attachments?: Attachment[];
}) {
  const t = useT();
  const api = useApi();
  const [preview, setPreview] = useState<Attachment | null>(null);
  if (!attachments || attachments.length === 0) return null;
  return (
    <>
      <ul
        aria-label={t("attachments.title")}
        className="mt-2 flex flex-col rounded-lg border border-line-default bg-surface-card"
      >
        {attachments.map((attachment) => (
          <li
            key={attachment.id}
            className="flex min-h-9 items-center gap-2 px-2 py-1 text-body-sm"
          >
            <Icon
              name={attachment.preview_kind === "image" ? "image" : "file-text"}
              size={14}
              className="shrink-0 text-fg-muted"
            />
            {attachment.preview_kind !== "none" ? (
              <button
                type="button"
                aria-label={t("attachments.preview", { name: attachment.name })}
                onClick={() => setPreview(attachment)}
                className="min-w-0 flex-1 truncate text-left text-fg-default hover:underline"
              >
                {attachment.name}
              </button>
            ) : (
              <a
                href={api.attachments.url(orgId, attachment.id)}
                className="min-w-0 flex-1 truncate text-fg-default hover:underline"
              >
                {attachment.name}
              </a>
            )}
            <span className="shrink-0 text-fg-muted">
              {formatBytes(attachment.size_bytes)}
            </span>
            <a
              href={api.attachments.url(orgId, attachment.id)}
              aria-label={t("attachments.download", { name: attachment.name })}
              className="flex size-6 shrink-0 items-center justify-center rounded-md text-fg-muted hover:bg-surface-muted"
            >
              <Icon name="download" size={14} />
            </a>
          </li>
        ))}
      </ul>
      <Dialog
        open={Boolean(preview)}
        onClose={() => setPreview(null)}
        title={preview?.name ?? ""}
        width={preview?.preview_kind === "image" ? 720 : 960}
      >
        {preview && <AttachmentPreview orgId={orgId} attachment={preview} />}
      </Dialog>
    </>
  );
}
