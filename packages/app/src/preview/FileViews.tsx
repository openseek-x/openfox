import { useMemo, useState, type ReactNode } from "react";

import { useT } from "@openfox/i18n";
import { Button, Icon, IconButton, useToast } from "@openfox/ui";

import { CodeView as SharedCodeView } from "../code/CodeView";
import { languageForPath } from "../code/languages";
import { Markdown } from "../conversations/Markdown";
import {
  CSV_MAX_ROWS,
  LARGE_MAX_LINES,
  isEnvFile,
  parseCsv,
  splitEnvLine,
  splitFrontmatter,
  type PreviewKind,
} from "../market/model";
import { formatBytes } from "../skills/model";

// 通用的只读文件预览部件：市场技能文件与会话产物共用。
// 不执行脚本、不加载外链资源；代码按后缀语法高亮。文案沿用 market.preview.*（市场行为与文案不变）。

export function LoadFailed({ onReload }: { onReload: () => void }) {
  const t = useT();
  return (
    <div role="alert" className="flex flex-col items-start gap-2 p-4">
      <p className="text-body text-danger">{t("market.preview.loadFailed")}</p>
      <Button variant="outline" size="28" onClick={onReload}>
        {t("market.preview.reload")}
      </Button>
    </div>
  );
}

export function CopyButton({ text }: { text: string }) {
  const t = useT();
  const toast = useToast();
  return (
    <IconButton
      label={t("market.preview.copy")}
      icon={<Icon name="copy" />}
      size="28"
      onClick={() => {
        const write = navigator.clipboard?.writeText(text);
        if (!write) {
          toast(t("market.preview.copyFailed"), { tone: "error" });
          return;
        }
        write
          .then(() => toast(t("market.preview.copied")))
          .catch(() =>
            toast(t("market.preview.copyFailed"), { tone: "error" }),
          );
      }}
    />
  );
}

/** 无法预览的文件：类型、大小与下载按钮。 */
export function BinaryInfo({
  name,
  mime,
  size,
  downloadUrl,
}: {
  name: string;
  mime: string;
  size: number;
  downloadUrl: string;
}) {
  const t = useT();
  return (
    <div className="flex flex-col items-start gap-3 p-6">
      <p className="text-body-sm text-fg-muted">
        {t("market.preview.binary", {
          type: mime || "application/octet-stream",
          size: formatBytes(size),
        })}
      </p>
      <a
        href={downloadUrl}
        download={name}
        className="inline-flex h-8 items-center gap-1.5 rounded-lg border border-line-default px-3 text-body font-medium hover:bg-surface-hover"
      >
        <Icon name="download" />
        {t("market.preview.downloadFile")}
      </a>
    </div>
  );
}

export function TextView({
  path,
  content,
  large,
  kind,
  source,
  downloadUrl,
}: {
  path: string;
  content: string;
  large: boolean;
  kind: PreviewKind;
  source: boolean;
  downloadUrl: string;
}) {
  const t = useT();
  // Large 模式：只渲染前 500 行，按 Code 显示
  const shown = useMemo(
    () =>
      large
        ? content.split("\n").slice(0, LARGE_MAX_LINES).join("\n")
        : content,
    [content, large],
  );
  let body: ReactNode;
  if (large || source) body = <CodeView path={path} text={shown} />;
  else if (kind === "markdown") body = <MarkdownView text={content} />;
  else if (kind === "table") body = <TableView path={path} text={content} />;
  else body = <CodeView path={path} text={content} />;
  return (
    <>
      {large && (
        <div className="flex items-center gap-3 border-b border-line-default bg-warning-soft px-3 py-2 text-body-sm">
          <span className="flex-1">
            {t("market.preview.large", { count: LARGE_MAX_LINES })}
          </span>
          <a
            href={downloadUrl}
            download={path.split("/").pop()}
            className="font-medium text-info underline underline-offset-2"
          >
            {t("market.preview.downloadFull")}
          </a>
        </div>
      )}
      {body}
    </>
  );
}

export function MarkdownView({ text }: { text: string }) {
  const t = useT();
  const { entries, body } = splitFrontmatter(text);
  return (
    <div className="flex flex-col gap-4 p-4">
      {entries.length > 0 && (
        <dl
          aria-label={t("market.preview.frontmatter")}
          className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1.5 rounded-lg border border-line-default bg-surface-muted p-3 text-body-sm"
        >
          {entries.map(([key, value]) => (
            <div key={key} className="contents">
              <dt className="font-mono text-fg-muted">{key}</dt>
              <dd className="min-w-0 break-words">{value}</dd>
            </div>
          ))}
        </dl>
      )}
      <Markdown source={body} />
    </div>
  );
}

export function TableView({ path, text }: { path: string; text: string }) {
  const t = useT();
  const rows = useMemo(
    () => parseCsv(text, path.toLowerCase().endsWith(".tsv") ? "\t" : ","),
    [path, text],
  );
  // 解析失败退回 Code
  if (!rows) return <CodeView path={path} text={text} />;
  const [head = [], ...rest] = rows;
  const body = rest.slice(0, CSV_MAX_ROWS);
  return (
    <div className="flex flex-col gap-2 p-4">
      <div className="overflow-x-auto rounded-lg border border-line-default">
        <table className="w-full border-collapse text-body-sm">
          <thead className="bg-surface-muted">
            <tr>
              {head.map((cell, index) => (
                <th
                  key={index}
                  className="border-b border-line-default px-3 py-1.5 text-left font-medium whitespace-nowrap"
                >
                  {cell}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {body.map((row, index) => (
              <tr
                key={index}
                className="border-b border-line-default last:border-0"
              >
                {row.map((cell, cellIndex) => (
                  <td key={cellIndex} className="px-3 py-1.5 whitespace-nowrap">
                    {cell}
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <span className="text-caption text-fg-muted">
        {rest.length > CSV_MAX_ROWS
          ? t("market.preview.tableTruncated", {
              shown: CSV_MAX_ROWS,
              total: rest.length,
            })
          : t("market.preview.tableTotal", { total: rest.length })}
      </span>
    </div>
  );
}

/** 行号 + 语法高亮（按路径推断语言）；.env 类文件的值默认 •••• 遮挡，点击显示（不高亮）。 */
export function CodeView({ path, text }: { path: string; text: string }) {
  const lines = useMemo(() => text.split("\n"), [text]);
  if (isEnvFile(path)) return <EnvView lines={lines} />;
  return <SharedCodeView code={text} language={languageForPath(path)} />;
}

function EnvView({ lines }: { lines: string[] }) {
  const t = useT();
  const [shown, setShown] = useState<ReadonlySet<number>>(new Set());
  return (
    <div className="py-3 font-mono text-body-sm leading-6">
      {lines.map((line, index) => {
        const split = splitEnvLine(line);
        const hidden = split !== null && !shown.has(index);
        return (
          <div key={index} className="flex">
            <span
              aria-hidden
              className="w-10 shrink-0 bg-surface-muted pr-3 text-right text-fg-muted select-none"
            >
              {index + 1}
            </span>
            <span className="min-w-0 flex-1 px-3 break-all whitespace-pre-wrap">
              {split ? (
                <>
                  {split[0]}
                  {hidden ? (
                    <button
                      type="button"
                      aria-label={t("market.preview.revealValue")}
                      onClick={() => setShown(new Set(shown).add(index))}
                      className="rounded bg-surface-muted px-1.5 text-fg-muted hover:bg-surface-active"
                    >
                      ••••
                    </button>
                  ) : (
                    split[1]
                  )}
                </>
              ) : (
                line
              )}
            </span>
          </div>
        );
      })}
    </div>
  );
}
