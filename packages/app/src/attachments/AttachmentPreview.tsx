import { useQuery } from "@tanstack/react-query";

import type { Attachment } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Skeleton } from "@openfox/ui";

import { useApi } from "../context";
import { HtmlPreview } from "../conversations/artifacts/HtmlPreview";
import { VideoPlayer } from "../conversations/artifacts/VideoPreview";

// OPE-19 · 任务附件、对话附件与报告评论附件共用的页面内预览：
// 图片 inline 显示；视频直接指向下载地址按 Range 分段播放；HTML 取回后做隔离静态渲染（无脚本、仅内嵌位图）。
// 预览方式由服务端的 preview_kind 决定，其余类型不渲染（调用方只提供下载）。

/** HTML 附件在页面内渲染的上限，与服务端 preview_kind=html 的判定（10 MB）一致。 */
const HTML_PREVIEW_MAX_BYTES = 10 * 1024 * 1024;

export function AttachmentPreview({
  orgId,
  attachment,
}: {
  orgId: string;
  attachment: Attachment;
}) {
  const t = useT();
  const api = useApi();
  const kind = attachment.preview_kind;
  const html = useQuery({
    queryKey: ["attachment-html", orgId, attachment.id, attachment.size_bytes],
    queryFn: async ({ signal }) => {
      const blob = await api.attachments.blob(
        orgId,
        attachment.id,
        HTML_PREVIEW_MAX_BYTES,
        signal,
      );
      return new TextDecoder("utf-8", { fatal: true, ignoreBOM: true }).decode(
        await blob.arrayBuffer(),
      );
    },
    enabled: kind === "html",
    staleTime: Infinity,
    gcTime: 0, // 关闭预览后释放解码后的文本。
    retry: false,
  });
  if (kind === "image")
    return (
      <img
        src={api.attachments.previewUrl(orgId, attachment.id)}
        alt={attachment.name}
        className="mx-auto max-h-[70vh] max-w-full object-contain"
      />
    );
  if (kind === "video")
    return (
      <VideoPlayer
        src={api.attachments.url(orgId, attachment.id)}
        name={attachment.name}
      />
    );
  if (kind === "html") {
    if (html.isError)
      return (
        <p role="status" className="p-4 text-body-sm text-fg-muted">
          {t("attachments.errors.generic")}
        </p>
      );
    if (html.data === undefined)
      return (
        <div aria-busy="true" className="p-4">
          <Skeleton className="h-40 w-full" />
        </div>
      );
    return <HtmlPreview text={html.data} name={attachment.name} />;
  }
  return null;
}
