import { useEffect, useId, useRef, useState } from "react";

import { useT } from "@openfox/i18n";
import { CodeBlock, CodeBlockGroup, Icon, cn, useToast } from "@openfox/ui";

import { HighlightedCode } from "../code/useHighlight";
import { copyText } from "../tasks/parts";

/** 超过这个行数的代码块默认折叠，点「展开全部」查看；复制始终拿完整代码。 */
const FOLD_LINES = 30;
/** 折叠高度：30 行 × 20px 行高 + 顶部内边距 12px（与 pre 的 leading-5、py-3 对应）。 */
const FOLDED_HEIGHT = "max-h-[612px]";

/**
 * 带顶栏的只读代码块：语言名 + 复制按钮，语法高亮，超过 30 行折叠、可展开。
 * 对话 Markdown 的代码块与执行步骤里的写入内容共用。
 */
export function CodeCard({
  code,
  language,
  label,
  auto,
}: {
  code: string;
  /** highlight.js 语言名或别名；null 表示不指定（auto 时自动识别，否则纯文本） */
  language: string | null;
  /** 顶栏显示的语言名；null 显示「纯文本」 */
  label: string | null;
  auto?: boolean;
}) {
  const t = useT();
  const toast = useToast();
  const preId = useId();
  const [copied, setCopied] = useState(false);
  const [expanded, setExpanded] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const revision = useRef(0);
  useEffect(() => {
    setCopied(false);
    return () => {
      revision.current++;
      clearTimeout(timer.current);
    };
  }, [code]);
  const lines = code.split("\n").length;
  const foldable = lines > FOLD_LINES;
  const folded = foldable && !expanded;
  return (
    <CodeBlock>
      <CodeBlockGroup>
        <span className="truncate font-medium text-fg-secondary">
          {label ?? t("chat.messages.plainText")}
        </span>
        <button
          type="button"
          className="inline-flex h-7 shrink-0 items-center gap-1.5 rounded-md px-2 text-caption text-fg-muted transition-colors hover:bg-surface-hover hover:text-fg-default"
          onClick={async () => {
            const request = ++revision.current;
            const ok = await copyText(code);
            if (request !== revision.current) return;
            if (!ok) {
              toast(t("steps.error.copyFailed"), { tone: "error" });
              return;
            }
            setCopied(true);
            clearTimeout(timer.current);
            timer.current = setTimeout(() => setCopied(false), 1500);
          }}
        >
          <Icon name={copied ? "copy-check" : "hifox-copy"} size={14} />
          {t(copied ? "chat.messages.copied" : "chat.messages.copy")}
        </button>
      </CodeBlockGroup>
      <pre
        id={preId}
        data-lang={language ?? undefined}
        className={cn(
          "overflow-x-auto px-4 py-3 font-mono text-code leading-5 whitespace-pre",
          folded && cn(FOLDED_HEIGHT, "overflow-y-hidden"),
        )}
      >
        <code>
          <HighlightedCode code={code} language={language} auto={auto} />
        </code>
      </pre>
      {folded && (
        <div
          aria-hidden="true"
          className="pointer-events-none absolute inset-x-0 bottom-9 h-16 bg-linear-to-b from-transparent to-surface-code"
        />
      )}
      {foldable && (
        <button
          type="button"
          aria-expanded={expanded}
          aria-controls={preId}
          onClick={() => setExpanded((value) => !value)}
          className="flex h-9 w-full shrink-0 items-center justify-center gap-1.5 border-t border-line-code text-caption text-fg-muted transition-colors hover:bg-surface-hover hover:text-fg-default"
        >
          <Icon name={expanded ? "chevron-up" : "chevron-down"} size={14} />
          {expanded
            ? t("chat.messages.collapseCode")
            : t("chat.messages.expandCode", { count: lines })}
        </button>
      )}
    </CodeBlock>
  );
}
