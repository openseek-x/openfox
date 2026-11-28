import type { RunArtifact } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { useState } from "react";

import { Button, ConfirmDialog, Icon, cn, useToast } from "@openfox/ui";

import { copyText } from "../../tasks/parts";
import { failureMessageKeys } from "./format";

// 运行结果卡片：Card/ModelError（页 01 244:328）与 Card/FileAttachment（页 09 253:28476）。

/** 执行失败卡片：failure_code 映射为标题与说明；「重试」由调用方实现（重新发送上一条用户消息）。 */
export function ModelErrorCard({
  failureCode,
  detail,
  onRetry,
  retrying = false,
  onRetryDirect,
  directDirectory,
}: {
  /** RunResponse.failure_code；空串或未知码显示通用文案 */
  failureCode?: string | null;
  /** 原始错误信息（失败事件的 detail / text 或结果摘要） */
  detail?: string | null;
  /** 不传则不显示「重试」 */
  onRetry?: () => void;
  retrying?: boolean;
  /**
   * 阶段 12d（F-08）：WORKTREE_PREP_FAILED 时「改为直接在源目录运行并重试」。
   * 调用方只在原输入作者本人且能确定源目录时传入；确认前不会切换（不自动切换）。
   */
  onRetryDirect?: () => void;
  /** 改为直接运行的源目录（逻辑目录名），显示在确认对话框里 */
  directDirectory?: string;
}) {
  const t = useT();
  const toast = useToast();
  const [confirmDirect, setConfirmDirect] = useState(false);
  const showDirect = failureCode === "WORKTREE_PREP_FAILED" && !!onRetryDirect;
  const keys = failureMessageKeys(failureCode, detail);
  const message = detail?.trim() ?? "";
  const copy = async () => {
    const text = [failureCode, message].filter(Boolean).join(": ");
    if (await copyText(text || t(keys.title))) toast(t("steps.error.copied"));
    else toast(t("steps.error.copyFailed"), { tone: "error" });
  };
  return (
    <section
      role="alert"
      className="flex min-w-0 flex-col gap-2 rounded-xl border border-line-soft bg-surface-card p-4"
    >
      <div className="grid grid-cols-[16px_minmax(0,1fr)_auto] items-start gap-x-3 gap-y-1">
        <Icon
          name="circle-alert"
          size={16}
          className="mt-0.5 shrink-0 text-danger"
        />
        <h3 className="min-w-0 text-body-sm font-medium text-fg-default">
          {t(keys.title)}
        </h3>
        {onRetry && (
          <Button
            size="28"
            variant="outline"
            loading={retrying}
            onClick={onRetry}
            className="col-start-3 row-start-1 -mt-1"
          >
            {!retrying && <Icon name="rotate-cw" size={16} />}
            {t("steps.error.retry")}
          </Button>
        )}
        <p className="col-span-2 col-start-2 row-start-2 text-body-sm text-fg-secondary">
          {t(keys.description)}
        </p>
      </div>
      <div className="min-w-0 pl-7">
        <details className="group min-w-0 text-caption text-fg-secondary">
          <summary className="w-fit cursor-pointer rounded py-1 focus-visible:outline-2 focus-visible:outline-accent">
            {t("steps.error.details")}
          </summary>
          {message && (
            <pre className="my-2 max-h-40 overflow-auto whitespace-pre-wrap break-all rounded-lg bg-surface-muted p-3 font-mono text-code">
              {message}
            </pre>
          )}
          <button
            type="button"
            onClick={() => void copy()}
            className="inline-flex min-h-8 items-center gap-2 rounded px-1 text-caption text-fg-secondary hover:bg-surface-hover focus-visible:outline-2 focus-visible:outline-accent"
          >
            <Icon name="copy" size={16} />
            {t("steps.error.copy")}
          </button>
        </details>
        {showDirect && (
          <Button
            size="28"
            variant="outline"
            disabled={retrying}
            onClick={() => setConfirmDirect(true)}
          >
            {t("steps.error.directRetry")}
          </Button>
        )}
      </div>
      {showDirect && (
        <ConfirmDialog
          open={confirmDirect}
          title={t("steps.error.directRetryTitle")}
          confirmLabel={t("steps.error.directRetryConfirm")}
          danger
          onCancel={() => setConfirmDirect(false)}
          onConfirm={() => {
            setConfirmDirect(false);
            onRetryDirect?.();
          }}
        >
          <div className="flex flex-col gap-2 text-body-sm text-fg-secondary">
            <p>{t("steps.error.directRetryWarning")}</p>
            {directDirectory && (
              <p>
                {t("steps.error.directRetryDirectory", {
                  directory: directDirectory,
                })}
              </p>
            )}
          </div>
        </ConfirmDialog>
      )}
    </section>
  );
}

type FileKind =
  | "word"
  | "excel"
  | "powerpoint"
  | "pdf"
  | "image"
  | "text"
  | "archive"
  | "video"
  | "other";

const EXTENSIONS: Array<[FileKind, RegExp]> = [
  ["word", /\.(docx?|odt|rtf)$/i],
  ["excel", /\.(xlsx?|ods|csv|tsv)$/i],
  ["powerpoint", /\.(pptx?|odp|key)$/i],
  ["pdf", /\.pdf$/i],
  ["image", /\.(png|jpe?g|gif|webp|svg|bmp)$/i],
  ["archive", /\.(zip|tar|gz|tgz|7z|rar)$/i],
  ["video", /\.(webm|mp4)$/i],
  ["text", /\.(txt|md|markdown|json|ya?ml|log|html?|xml)$/i],
];

/** 按扩展名（优先）或 MIME 判断文件类型。 */
export function fileKindOf(name: string, mime: string): FileKind {
  for (const [kind, pattern] of EXTENSIONS) if (pattern.test(name)) return kind;
  if (mime === "application/pdf") return "pdf";
  if (mime.startsWith("image/")) return "image";
  if (mime.startsWith("video/")) return "video";
  if (mime.startsWith("text/")) return "text";
  return "other";
}

const BADGE: Record<FileKind, { text: string; className: string }> = {
  word: { text: "W", className: "bg-[#2b579a]" },
  excel: { text: "X", className: "bg-[#217346]" },
  powerpoint: { text: "P", className: "bg-[#c43e1c]" },
  pdf: { text: "PDF", className: "bg-[#d93025] text-caption" },
  image: { text: "IMG", className: "bg-info text-caption" },
  text: { text: "TXT", className: "bg-fg-muted text-caption" },
  archive: { text: "ZIP", className: "bg-fg-secondary text-caption" },
  video: { text: "VID", className: "bg-[#7c3aed] text-caption" },
  other: { text: "", className: "bg-fg-subtle" },
};

/** 字节数：「128 KB」「1.2 MB」。 */
export function formatBytes(bytes: number, locale?: string): string {
  const units = ["B", "KB", "MB", "GB"];
  let value = Math.max(0, bytes);
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit++;
  }
  const digits = unit === 0 || value >= 10 ? 0 : 1;
  return `${new Intl.NumberFormat(locale, { maximumFractionDigits: digits }).format(value)} ${units[unit]}`;
}

/** 产物是否仍可下载（AVAILABLE 且未过保留期）。 */
export function artifactAvailable(artifact: RunArtifact, now = Date.now()) {
  if (artifact.state !== "AVAILABLE") return false;
  return (
    !artifact.retention_until ||
    new Date(artifact.retention_until).getTime() > now
  );
}

/** 生成文件卡片：名称、类型、大小，打开（新标签页）/ 下载。 */
export function FileAttachmentCard({
  artifact,
  href,
}: {
  artifact: RunArtifact;
  /** 下载地址：api.runs.artifactUrl(...) */
  href: string;
}) {
  const t = useT();
  const kind = fileKindOf(artifact.name, artifact.mime_type);
  const badge = BADGE[kind];
  const available = artifactAvailable(artifact);
  const typeLabel = t(`steps.attachment.types.${kind}`);
  return (
    <div className="flex flex-col gap-2">
      <p className="flex items-center gap-1.5 text-body-sm text-fg-muted">
        <Icon name="file-text" size={14} />
        <span>{t("steps.attachment.generated")}</span>
        <span className="truncate">{artifact.name}</span>
      </p>
      <div className="flex w-full max-w-[360px] items-center gap-3 rounded-lg border border-line-default bg-surface-card p-3">
        <span
          aria-hidden
          className={cn(
            "flex size-9 shrink-0 items-center justify-center rounded-md font-semibold text-fg-inverse",
            badge.className,
          )}
        >
          {badge.text || <Icon name="file-text" size={16} />}
        </span>
        <div className="flex min-w-0 flex-1 flex-col">
          <span className="truncate text-body font-medium text-fg-default">
            {artifact.name}
          </span>
          <span className="text-caption text-fg-muted">
            {available
              ? `${typeLabel} · ${formatBytes(artifact.size_bytes)}`
              : t("steps.attachment.unavailable")}
          </span>
        </div>
        {available && (
          <>
            <a
              href={href}
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex h-7 shrink-0 items-center rounded-md border border-line-default px-2.5 text-body-sm font-medium text-fg-default hover:bg-surface-hover"
            >
              {t("steps.attachment.open")}
            </a>
            <a
              href={href}
              download={artifact.name}
              aria-label={t("steps.attachment.download")}
              title={t("steps.attachment.download")}
              className="inline-flex size-7 shrink-0 items-center justify-center rounded-md text-fg-muted hover:bg-surface-muted"
            >
              <Icon name="download" size={14} />
            </a>
          </>
        )}
      </div>
    </div>
  );
}
