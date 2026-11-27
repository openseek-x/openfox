import { RestoreProposal } from "../proposals/RestoreProposal";
import { previewCapabilities } from "./previewProtocol";
import { useViewState, usePreviewScroll } from "./viewState";
import { ArtifactReview } from "./ArtifactReview";
import { PreviewNavigationTarget } from "./PreviewNavigation";
import { OfficeReview } from "./OfficeReview";
import { VisualReview } from "./VisualReview";
import { useEffect, useRef, useState } from "react";

import { isApiError, type RunArtifact } from "@openfox/api";
import { useI18n, useT } from "@openfox/i18n";
import { Icon, IconButton, Popover, Skeleton } from "@openfox/ui";

import { useApi } from "../../context";
import {
  BinaryInfo,
  CopyButton,
  LoadFailed,
  MarkdownView,
  TextView,
} from "../../preview/FileViews";
import { ChatMarkdown } from "../ChatMarkdown";
import { fileKindOf, formatBytes } from "../steps/cards";
import { HtmlPreview } from "./HtmlPreview";
import { VideoBlob } from "./VideoPreview";
import {
  ARTIFACT_LARGE_TEXT_BYTES,
  artifactAnnotatable,
  artifactExpired,
  artifactPreviewKind,
  artifactPreviewable,
} from "./model";
import { useArtifactContent } from "./queries";

// Reuse the authenticated attachment download. HTML is static and sandboxed;
// source and copy always retain the original file, including omitted scripts.

export function ArtifactPreview({
  orgId,
  artifact,
  conversationId,
  versionMode = "fixed",
  onOpenVersion,
}: {
  orgId: string;
  artifact: RunArtifact;
  conversationId?: string;
  versionMode?: "fixed" | "current";
  onOpenVersion?: () => void;
}) {
  const t = useT();
  const { formatDate } = useI18n();
  const api = useApi();
  const expired = artifactExpired(artifact);
  const kind = artifactPreviewKind(artifact);
  const capabilities = previewCapabilities(artifact);
  const historical = artifact.id !== artifact.head_artifact_id;
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
  const hasText =
    previewable &&
    !["image", "pdf", "office", "video"].includes(kind) &&
    Boolean(content.data);
  const [source, setSource] = useViewState("source", false);
  const [annotate, setAnnotate] = useState(false);
  const [infoOpen, setInfoOpen] = useState(false);
  const [navigation, setNavigation] = useState<HTMLDivElement | null>(null);
  const infoRef = useRef<HTMLButtonElement>(null);
  const scrollRef = useRef<HTMLDivElement>(null);
  usePreviewScroll(
    scrollRef,
    Boolean(content.data),
    source ? "source" : "preview",
  );
  const large = artifact.size_bytes > ARTIFACT_LARGE_TEXT_BYTES;
  // HTML 报告超过大文件阈值仍渲染（只有源码视图按大文件模式显示前 500 行），因此保留源码切换。
  const canSwitch =
    hasText && (!large || kind === "html") && capabilities.sourceToggle;
  const fileType =
    kind === "html"
      ? "HTML"
      : t(
          `steps.attachment.types.${fileKindOf(artifact.name, artifact.mime_type)}`,
        );

  return (
    <PreviewNavigationTarget.Provider value={navigation}>
      <section
        aria-label={artifact.name}
        className="flex min-h-0 min-w-0 flex-1 flex-col"
      >
        <div
          aria-label={t("artifacts.preview.toolbar")}
          className="flex h-[52px] min-w-0 shrink-0 items-center gap-1.5 border-b border-line-default px-2"
        >
          <div ref={setNavigation} className="shrink-0 empty:hidden" />
          {canSwitch && (
            <div
              role="group"
              aria-label={t("artifacts.preview.mode")}
              className="flex shrink-0 items-center rounded-full border border-line-default bg-surface-muted p-0.5"
            >
              {([false, true] as const).map((showSource) => (
                <IconButton
                  key={String(showSource)}
                  label={t(
                    showSource
                      ? "artifacts.preview.source"
                      : "artifacts.preview.rendered",
                  )}
                  icon={<Icon name={showSource ? "code" : "eye"} size={16} />}
                  size="28"
                  active={source === showSource}
                  aria-pressed={source === showSource}
                  className="rounded-full!"
                  onClick={() => setSource(showSource)}
                />
              ))}
            </div>
          )}
          {conversationId &&
            capabilities.selection.length > 0 &&
            artifactAnnotatable(artifact) &&
            !historical &&
            previewable &&
            !gone &&
            content.data && (
              <IconButton
                label={t("artifacts.review.annotate")}
                icon={<Icon name="hifox-scan-eye" size={18} />}
                size="32"
                variant="round"
                active={annotate}
                aria-pressed={annotate}
                onClick={() => setAnnotate((value) => !value)}
                className={
                  annotate
                    ? "border-accent! bg-accent-soft! text-accent!"
                    : "bg-surface-muted"
                }
              />
            )}
          <div
            className="flex min-w-0 flex-1 items-center justify-center gap-1.5 text-caption text-fg-muted"
            title={artifact.name}
          >
            <Icon
              name={
                kind === "image"
                  ? "image"
                  : kind === "video"
                    ? "play"
                    : kind === "table"
                      ? "file-spreadsheet"
                      : "file-text"
              }
              size={15}
              className="shrink-0"
            />
            <span className="truncate">{artifact.name}</span>
          </div>
          {!gone && (
            <a
              href={downloadUrl}
              download={artifact.name}
              aria-label={t("artifacts.preview.download")}
              title={t("artifacts.preview.download")}
              className="flex size-8 shrink-0 items-center justify-center rounded-full border border-line-default bg-surface-muted text-fg-muted hover:bg-surface-hover hover:text-fg-default"
            >
              <Icon name="download" size={16} />
            </a>
          )}
          <IconButton
            ref={infoRef}
            label={t("artifacts.preview.more")}
            icon={<Icon name="more" size={18} />}
            size="32"
            variant="round"
            className="bg-surface-muted"
            aria-expanded={infoOpen}
            aria-haspopup="dialog"
            onClick={() => setInfoOpen((open) => !open)}
          />
          <Popover
            open={infoOpen}
            onOpenChange={setInfoOpen}
            anchorRef={infoRef}
            placement="bottom-end"
            role="dialog"
            aria-label={t("artifacts.preview.info")}
            className="w-64 space-y-2 p-3 text-body-sm"
          >
            <div className="flex items-center justify-between gap-2">
              <span className="text-caption text-fg-muted">
                {t("artifacts.preview.info")}
              </span>
              {hasText && <CopyButton text={text} />}
            </div>
            <p className="break-words font-medium text-fg-default">
              {artifact.name}
            </p>
            <p className="text-fg-secondary">
              {fileType} · {formatBytes(artifact.size_bytes)}
            </p>
            <time
              className="block text-caption text-fg-muted"
              dateTime={artifact.created_at}
            >
              {formatDate(artifact.created_at, {
                dateStyle: "medium",
                timeStyle: "short",
              })}
            </time>
            <p className="text-caption text-fg-muted">
              {t(
                versionMode === "current"
                  ? "artifacts.version.following"
                  : "artifacts.version.fixed",
                { version: artifact.content_revision },
              )}
            </p>
            {onOpenVersion && (
              <button
                type="button"
                onClick={() => {
                  setInfoOpen(false);
                  onOpenVersion();
                }}
                className="text-accent hover:underline"
              >
                {t(
                  versionMode === "current"
                    ? "artifacts.version.openFixed"
                    : "artifacts.version.openCurrent",
                )}
              </button>
            )}
            {historical && conversationId && !gone && (
              <RestoreProposal
                orgId={orgId}
                conversationId={conversationId}
                artifact={artifact}
              />
            )}
            {kind === "office" && (
              <p className="text-caption text-fg-muted">
                {t("artifacts.review.officeHint")}
              </p>
            )}
            {kind === "html" && (
              <p className="border-t border-line-default pt-2 text-caption text-fg-muted">
                {t("artifacts.preview.htmlHint")}
              </p>
            )}
          </Popover>
        </div>
        <div ref={scrollRef} className="min-h-0 flex-1 overflow-auto">
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
            <div>
              {kind === "office" && (
                <p role="status" className="p-3 text-body-sm text-fg-muted">
                  {t("artifacts.review.officePreviewFailed")}
                </p>
              )}
              <LoadFailed onReload={() => void content.refetch()} />
            </div>
          ) : !content.data ? (
            <div aria-busy="true" className="flex flex-col gap-2 p-4">
              <Skeleton className="h-4 w-1/2" />
              <Skeleton className="h-4 w-full" />
              <Skeleton className="h-4 w-3/4" />
            </div>
          ) : kind === "video" ? (
            <VideoBlob blob={content.data.blob!} name={artifact.name} />
          ) : conversationId ? (
            <ArtifactReview
              key={artifact.id}
              orgId={orgId}
              conversationId={conversationId}
              artifact={artifact}
              embedded
              sourceMode={source}
              annotate={!historical && annotate}
            />
          ) : kind === "office" ? (
            <OfficeReview
              key={artifact.id}
              view={content.data.office!}
              blob={content.data.blob!}
              name={artifact.name}
              loadFallback={(signal) =>
                api.artifactReviews.officePreview(
                  orgId,
                  artifact.run_id,
                  artifact.id,
                  signal,
                )
              }
            />
          ) : kind === "pdf" ? (
            <VisualReview blob={content.data.blob!} name={artifact.name} pdf />
          ) : kind === "image" ? (
            <ImageBlob blob={content.data.blob!} name={artifact.name} />
          ) : kind === "html" && !source ? (
            <HtmlPreview text={text} name={artifact.name} />
          ) : kind === "markdown" && !source && !large ? (
            <MarkdownView
              text={text}
              renderBody={(body) => <ChatMarkdown source={body} />}
            />
          ) : (
            <TextView
              path={artifact.name}
              content={text}
              large={large}
              kind={kind === "html" ? "code" : kind}
              source={source || kind === "html"}
              downloadUrl={downloadUrl}
            />
          )}
        </div>
      </section>
    </PreviewNavigationTarget.Provider>
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
