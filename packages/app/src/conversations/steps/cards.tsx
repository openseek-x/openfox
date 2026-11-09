import type { RunArtifact } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Icon, cn, useToast } from "@openfox/ui";

import { copyText } from "../../tasks/parts";
import { failureMessageKeys } from "./format";

// 运行结果卡片：Card/ModelError（页 01 244:328）与 Card/FileAttachment（页 09 253:28476）。

/** 执行失败卡片：failure_code 映射为标题与说明；「重试」由调用方实现（重新发送上一条用户消息）。 */
export function ModelErrorCard({
  failureCode,
  detail,
  onRetry,
  retrying = false,
}: {
  /** RunResponse.failure_code；空串或未知码显示通用文案 */
  failureCode?: string | null;
  /** 原始错误信息（失败事件的 detail / text 或结果摘要） */
  detail?: string | null;
  /** 不传则不显示「重试」 */
  onRetry?: () => void;
  retrying?: boolean;
}) {
  const t = useT();
  const toast = useToast();
  const keys = failureMessageKeys(failureCode);
  const message = detail?.trim() ?? "";
  const copy = async () => {
    const text = [failureCode, message].filter(Boolean).join(": ");
    if (await copyText(text || t(keys.title))) toast(t("steps.error.copied"));
    else toast(t("steps.error.copyFailed"), { tone: "error" });
  };
  return (
    <section
      role="alert"
      className="flex flex-col gap-3 rounded-lg border border-line-default bg-surface-card px-4 py-3"
    >
      <div className="flex items-start gap-3">
        <Icon
          name="circle-alert"
          size={16}
          className="mt-0.5 shrink-0 text-warning"
        />
        <div className="flex min-w-0 flex-1 flex-col gap-0.5">
          <h3 className="text-body font-semibold text-fg-default">
            {t(keys.title)}
          </h3>
          <p className="text-body-sm text-fg-secondary">
            {t(keys.description)}
          </p>
        </div>
        {onRetry && (
          <Button
            size="28"
            loading={retrying}
            onClick={onRetry}
            className="shrink-0"
          >
            {!retrying && <Icon name="rotate-cw" size={12} />}
            {t("steps.error.retry")}
          </Button>
        )}
      </div>
      <div className="flex flex-col gap-2 border-t border-line-soft pt-3 pl-7">
        {message && (
          <p className="whitespace-pre-wrap break-words text-body-sm text-fg-secondary">
            {t("steps.error.detail", { detail: message })}
          </p>
        )}
        <button
          type="button"
          onClick={() => void copy()}
          className="self-start text-body-sm text-fg-muted underline-offset-2 hover:underline"
        >
          {t("steps.error.copy")}
        </button>
      </div>
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
  | "other";

const EXTENSIONS: Array<[FileKind, RegExp]> = [
  ["word", /\.(docx?|odt|rtf)$/i],
  ["excel", /\.(xlsx?|ods|csv|tsv)$/i],
  ["powerpoint", /\.(pptx?|odp|key)$/i],
  ["pdf", /\.pdf$/i],
  ["image", /\.(png|jpe?g|gif|webp|svg|bmp)$/i],
  ["archive", /\.(zip|tar|gz|tgz|7z|rar)$/i],
  ["text", /\.(txt|md|markdown|json|ya?ml|log|html?|xml)$/i],
];

/** 按扩展名（优先）或 MIME 判断文件类型。 */
export function fileKindOf(name: string, mime: string): FileKind {
  for (const [kind, pattern] of EXTENSIONS) if (pattern.test(name)) return kind;
  if (mime === "application/pdf") return "pdf";
  if (mime.startsWith("image/")) return "image";
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
