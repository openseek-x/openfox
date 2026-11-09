import { useEffect, useState } from "react";

import type { MarketSkillFileEntry } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Icon, Segmented, Skeleton } from "@openfox/ui";

import { useApi } from "../context";
import {
  BinaryInfo,
  CodeView,
  CopyButton,
  LoadFailed,
  TextView,
} from "../preview/FileViews";
import { formatBytes } from "../skills/model";
import {
  IMAGE_MAX_BYTES,
  LARGE_TEXT_BYTES,
  isSvg,
  languageLabel,
  lineCount,
  previewKind,
  type PreviewKind,
} from "./model";
import { useMarketSkillBlob, useMarketSkillFile } from "./queries";

// 市场技能的文件预览：只读，不执行脚本、不加载外链资源；代码按后缀语法高亮。
// 文本、Markdown、表格、代码、复制、加载失败等通用部件抽到 preview/FileViews.tsx，行为不变。

type ViewMode = "preview" | "source";

export function FilePreview({
  orgId,
  skillId,
  entry,
}: {
  orgId: string;
  skillId: string;
  entry: MarketSkillFileEntry;
}) {
  const t = useT();
  const api = useApi();
  const kind = previewKind(entry.path);
  const asImage = kind === "image" && entry.size_bytes <= IMAGE_MAX_BYTES;
  const asBinary = kind === "binary" || (kind === "image" && !asImage);
  const textual = !asImage && !asBinary;
  const file = useMarketSkillFile(orgId, skillId, entry.path, textual);
  const [mode, setMode] = useState<ViewMode>("preview");
  const downloadUrl = api.market.downloadUrl(orgId, skillId, entry.path);
  const fileName = entry.path.split("/").pop();

  const content = file.data?.content ?? "";
  // 未知后缀由服务端判断；已知二进制后缀不请求内容
  const binary = asBinary || (textual && file.data?.binary === true);
  const large =
    textual &&
    Boolean(file.data) &&
    (file.data!.truncated || entry.size_bytes > LARGE_TEXT_BYTES);
  const textKind: PreviewKind = kind === "unknown" ? "code" : kind;
  const canToggle =
    !binary && !large && (textKind === "markdown" || isSvg(entry.path));
  const hasText = textual && Boolean(file.data) && !binary;

  return (
    <section
      aria-label={t("market.preview.label", { path: entry.path })}
      className="flex min-w-0 flex-1 flex-col"
    >
      <div className="flex h-10 shrink-0 items-center gap-2 border-b border-line-default px-3">
        <Icon
          name={asImage ? "image" : "file-text"}
          className="shrink-0 text-fg-muted"
        />
        <span className="min-w-0 flex-1 truncate font-mono text-body-sm">
          {entry.path}
        </span>
        {!binary && !asImage && (
          <span className="shrink-0 rounded bg-surface-muted px-1.5 py-0.5 text-caption text-fg-secondary">
            {languageLabel(entry.path)}
          </span>
        )}
        <span className="shrink-0 text-caption text-fg-muted">
          {formatBytes(entry.size_bytes)}
          {hasText
            ? ` · ${t("market.preview.lines", { count: lineCount(content) })}`
            : ""}
        </span>
        {canToggle && (
          <Segmented
            label={t("market.preview.mode")}
            value={mode}
            onChange={setMode}
            className="h-7"
            options={[
              { value: "preview", label: t("market.preview.preview") },
              { value: "source", label: t("market.preview.source") },
            ]}
          />
        )}
        {hasText && <CopyButton text={content} />}
        <a
          href={downloadUrl}
          download={fileName}
          aria-label={t("market.preview.download")}
          title={t("market.preview.download")}
          className="flex size-7 shrink-0 items-center justify-center rounded-md text-fg-muted hover:bg-surface-hover hover:text-fg-default"
        >
          <Icon name="download" />
        </a>
      </div>
      <div className="min-h-0 flex-1 overflow-auto">
        {asImage ? (
          <ImageView
            orgId={orgId}
            skillId={skillId}
            entry={entry}
            source={mode === "source"}
          />
        ) : binary ? (
          <BinaryView entry={entry} downloadUrl={downloadUrl} />
        ) : file.isError ? (
          <LoadFailed onReload={() => void file.refetch()} />
        ) : !file.data ? (
          <div aria-busy="true" className="flex flex-col gap-2 p-4">
            <Skeleton className="h-4 w-1/2" />
            <Skeleton className="h-4 w-full" />
            <Skeleton className="h-4 w-3/4" />
          </div>
        ) : (
          <TextView
            path={entry.path}
            content={content}
            large={large}
            kind={textKind}
            source={mode === "source"}
            downloadUrl={downloadUrl}
          />
        )}
      </div>
    </section>
  );
}

function BinaryView({
  entry,
  downloadUrl,
}: {
  entry: MarketSkillFileEntry;
  downloadUrl: string;
}) {
  return (
    <BinaryInfo
      name={entry.path.split("/").pop() ?? entry.path}
      mime={entry.mime}
      size={entry.size_bytes}
      downloadUrl={downloadUrl}
    />
  );
}

function ImageView({
  orgId,
  skillId,
  entry,
  source,
}: {
  orgId: string;
  skillId: string;
  entry: MarketSkillFileEntry;
  source: boolean;
}) {
  const t = useT();
  const svg = isSvg(entry.path);
  const blob = useMarketSkillBlob(
    orgId,
    skillId,
    entry.path,
    IMAGE_MAX_BYTES,
    !source,
  );
  const text = useMarketSkillFile(orgId, skillId, entry.path, source && svg);
  const [url, setUrl] = useState<string | null>(null);
  const [size, setSize] = useState<{ w: number; h: number; scale: number }>();
  useEffect(() => {
    if (!blob.data) return;
    // SVG 只作 <img> 显示（不内联），并显式标成 image/svg+xml
    const typed = svg
      ? new Blob([blob.data], { type: "image/svg+xml" })
      : blob.data;
    const objectUrl = URL.createObjectURL(typed);
    setUrl(objectUrl);
    return () => {
      URL.revokeObjectURL(objectUrl);
      setUrl(null);
    };
  }, [blob.data, svg]);

  if (source && svg) {
    if (text.isError)
      return <LoadFailed onReload={() => void text.refetch()} />;
    if (!text.data)
      return (
        <div aria-busy="true" className="p-4">
          <Skeleton className="h-4 w-full" />
        </div>
      );
    return <CodeView path={entry.path} text={text.data.content} />;
  }
  if (blob.isError) return <LoadFailed onReload={() => void blob.refetch()} />;
  if (!url)
    return (
      <div aria-busy="true" className="p-4">
        <Skeleton className="h-40 w-full" />
      </div>
    );
  return (
    <div className="flex flex-col items-start gap-2 p-4">
      <img
        src={url}
        alt={entry.path}
        referrerPolicy="no-referrer"
        className="max-h-[420px] max-w-full rounded-md border border-line-default bg-surface-muted object-contain"
        onLoad={(event) => {
          const img = event.currentTarget;
          if (img.naturalWidth > 0)
            setSize({
              w: img.naturalWidth,
              h: img.naturalHeight,
              scale: Math.round((img.clientWidth / img.naturalWidth) * 100),
            });
        }}
      />
      {size && (
        <span className="text-caption text-fg-muted">
          {t("market.preview.imageInfo", {
            width: size.w,
            height: size.h,
            scale: size.scale,
          })}
        </span>
      )}
    </div>
  );
}
