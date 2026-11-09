import { useEffect, useState } from "react";

import { isApiError, type RunArtifact } from "@openfox/api";
import { useI18n, useT } from "@openfox/i18n";
import { Icon, Skeleton } from "@openfox/ui";

import { useApi } from "../../context";
import {
  BinaryInfo,
  CopyButton,
  LoadFailed,
  TextView,
} from "../../preview/FileViews";
import { fileKindOf, formatBytes } from "../steps/cards";
import {
  ARTIFACT_LARGE_TEXT_BYTES,
  artifactExpired,
  artifactPreviewKind,
  artifactPreviewable,
} from "./model";
import { useArtifactContent } from "./queries";

// 单个产物的预览：Markdown 用会话的 Markdown 渲染、图片用 blob URL（≤10MB）、文本 / 代码按语法高亮、
// CSV 表格、其他文件只显示信息与下载；过期显示「产物已失效」。SVG、HTML 等在中心只存为纯文本或二进制，绝不渲染执行。

export function ArtifactPreview({
  orgId,
  artifact,
}: {
  orgId: string;
  artifact: RunArtifact;
}) {
  const t = useT();
  const { formatDate } = useI18n();
  const api = useApi();
  const expired = artifactExpired(artifact);
  const kind = artifactPreviewKind(artifact);
  const previewable = !expired && artifactPreviewable(artifact);
  const content = useArtifactContent(orgId, artifact, previewable);
  const downloadUrl = api.runs.artifactUrl(orgId, artifact.run_id, artifact.id);
  // 下载时中心返回 404（已被清理但列表还没刷新）也按失效显示
  const gone =
    expired ||
    (content.isError &&
      isApiError(content.error) &&
      content.error.status === 404);
  const text = content.data?.text ?? "";
  const hasText = previewable && kind !== "image" && Boolean(content.data);

  return (
    <section
      aria-label={artifact.name}
      className="flex min-h-0 min-w-0 flex-1 flex-col"
    >
      <div className="flex h-10 shrink-0 items-center gap-2 border-b border-line-default px-3">
        <Icon
          name={kind === "image" ? "image" : "file-text"}
          className="shrink-0 text-fg-muted"
        />
        <div className="flex min-w-0 flex-1 flex-col leading-tight">
          <span className="truncate text-body-sm font-medium">
            {artifact.name}
          </span>
          <span className="truncate text-caption text-fg-muted">
            {t("artifacts.preview.meta", {
              type: t(
                `steps.attachment.types.${fileKindOf(artifact.name, artifact.mime_type)}`,
              ),
              size: formatBytes(artifact.size_bytes),
              time: formatDate(artifact.created_at, {
                dateStyle: "medium",
                timeStyle: "short",
              }),
            })}
          </span>
        </div>
        {hasText && <CopyButton text={text} />}
        {!gone && (
          <a
            href={downloadUrl}
            download={artifact.name}
            aria-label={t("artifacts.preview.download")}
            title={t("artifacts.preview.download")}
            className="flex size-7 shrink-0 items-center justify-center rounded-md text-fg-muted hover:bg-surface-hover hover:text-fg-default"
          >
            <Icon name="download" />
          </a>
        )}
      </div>
      <div className="min-h-0 flex-1 overflow-auto">
        {gone ? (
          <div role="status" className="flex flex-col items-start gap-1 p-6">
            <p className="text-body font-medium text-fg-default">
              {t("artifacts.preview.expired")}
            </p>
            <p className="text-body-sm text-fg-muted">
              {t("artifacts.preview.expiredHint")}
            </p>
          </div>
        ) : !previewable ? (
          kind === "binary" ? (
            <BinaryInfo
              name={artifact.name}
              mime={artifact.mime_type}
              size={artifact.size_bytes}
              downloadUrl={downloadUrl}
            />
          ) : (
            <div className="flex flex-col items-start gap-3 p-6">
              <p className="text-body-sm text-fg-muted">
                {t("artifacts.preview.tooLarge")}
              </p>
              <a
                href={downloadUrl}
                download={artifact.name}
                className="inline-flex h-8 items-center gap-1.5 rounded-lg border border-line-default px-3 text-body font-medium hover:bg-surface-hover"
              >
                <Icon name="download" />
                {t("artifacts.preview.download")}
              </a>
            </div>
          )
        ) : content.isError ? (
          <LoadFailed onReload={() => void content.refetch()} />
        ) : !content.data ? (
          <div aria-busy="true" className="flex flex-col gap-2 p-4">
            <Skeleton className="h-4 w-1/2" />
            <Skeleton className="h-4 w-full" />
            <Skeleton className="h-4 w-3/4" />
          </div>
        ) : kind === "image" ? (
          <ImageBlob blob={content.data.blob!} name={artifact.name} />
        ) : (
          <TextView
            path={artifact.name}
            content={text}
            large={artifact.size_bytes > ARTIFACT_LARGE_TEXT_BYTES}
            kind={kind}
            source={false}
            downloadUrl={downloadUrl}
          />
        )}
      </div>
    </section>
  );
}

function ImageBlob({ blob, name }: { blob: Blob; name: string }) {
  const [url, setUrl] = useState<string | null>(null);
  useEffect(() => {
    const objectUrl = URL.createObjectURL(blob);
    setUrl(objectUrl);
    return () => {
      URL.revokeObjectURL(objectUrl);
      setUrl(null);
    };
  }, [blob]);
  if (!url)
    return (
      <div aria-busy="true" className="p-4">
        <Skeleton className="h-40 w-full" />
      </div>
    );
  return (
    <div className="p-4">
      <img
        src={url}
        alt={name}
        referrerPolicy="no-referrer"
        className="max-w-full rounded-md border border-line-default bg-surface-muted object-contain"
      />
    </div>
  );
}
