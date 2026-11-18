import { memo, useId, useMemo } from "react";
import ReactMarkdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";
import remarkBreaks from "remark-breaks";

import { useT } from "@openfox/i18n";
import { cn } from "@openfox/ui";

import { languageForFence, languageLabel } from "../code/languages";
import { CodeCard } from "./CodeCard";
import { remarkSelectionMap } from "./artifacts/selection";
import { safeHref } from "./Markdown";

const PLAIN_LANGUAGES = new Set(["text", "plaintext", "plain", "txt"]);

function ChatCodeBlock({ code, language }: { code: string; language: string }) {
  const label = PLAIN_LANGUAGES.has(language.toLowerCase())
    ? null
    : languageLabel(language);
  return (
    <CodeCard
      code={code}
      language={languageForFence(language)}
      label={label}
      auto
    />
  );
}

/** h1/h2：上方共 24px，下方共 6px。 */
const HEADING_MAJOR = "mt-3 -mb-1.5 first:mt-0";
/** h3 及以下：上方共 16px，下方共 6px。 */
const HEADING_MINOR = "mt-1 -mb-1.5 first:mt-0";
const HEADING_SMALL = cn(
  "text-body font-semibold text-fg-secondary",
  HEADING_MINOR,
);

function createComponents(prefix: string, selectable: boolean): Components {
  return {
    // 正文容器是 flex gap-3（12px）：标题再加上边距拉开章节，负下边距让标题贴近下文。
    h1: ({ children }) => (
      <h3 className={cn("text-subheading font-semibold", HEADING_MAJOR)}>
        {children}
      </h3>
    ),
    h2: ({ children, id, className }) => (
      <h4
        id={id === "footnote-label" ? `${prefix}label` : id}
        className={cn("text-title font-semibold", HEADING_MAJOR, className)}
      >
        {children}
      </h4>
    ),
    h3: ({ children }) => (
      <h5
        className={cn("text-body leading-[22px] font-semibold", HEADING_MINOR)}
      >
        {children}
      </h5>
    ),
    h4: ({ children }) => <h6 className={HEADING_SMALL}>{children}</h6>,
    h5: ({ children }) => <h6 className={HEADING_SMALL}>{children}</h6>,
    h6: ({ children }) => <h6 className={HEADING_SMALL}>{children}</h6>,
    a: ({ href, children, node, ...props }) => {
      if (href?.startsWith(`#${prefix}`)) {
        return (
          <a
            {...props}
            href={href}
            aria-describedby={
              node?.properties.dataFootnoteRef ? `${prefix}label` : undefined
            }
            className="text-info underline underline-offset-3"
            onClick={(event) => {
              event.preventDefault();
              const target = event.currentTarget.ownerDocument.getElementById(
                href.slice(1),
              );
              target?.scrollIntoView({ block: "nearest" });
              target?.focus({ preventScroll: true });
            }}
          >
            {children}
          </a>
        );
      }
      const safe = safeHref(href ?? "");
      return safe ? (
        <a
          href={safe}
          target="_blank"
          rel="noopener noreferrer"
          className="text-info underline decoration-info/40 underline-offset-3 hover:decoration-info"
        >
          {children}
        </a>
      ) : (
        <>{children}</>
      );
    },
    img: ({ alt }) => <span className="text-fg-muted">{alt}</span>,
    ul: ({ children }) => (
      <ul className="list-disc space-y-1 pl-5 marker:text-fg-subtle [&:has(>li.task-list-item)]:list-none">
        {children}
      </ul>
    ),
    ol: ({ children, start }) => (
      <ol
        start={start}
        className="list-decimal space-y-1 pl-5 marker:text-fg-muted marker:tabular-nums"
      >
        {children}
      </ol>
    ),
    li: ({ children, className, id }) => (
      <li
        id={id}
        tabIndex={id ? -1 : undefined}
        className={cn("pl-0.5 [&>ol]:mt-1 [&>p]:my-1 [&>ul]:mt-1", className)}
      >
        {children}
      </li>
    ),
    blockquote: ({ children }) => (
      <blockquote className="space-y-2 rounded-r-lg border-l-[3px] border-line-strong bg-surface-hover px-3 py-2 text-fg-secondary">
        {children}
      </blockquote>
    ),
    hr: () => <hr className="my-1 border-line-default" />,
    code: ({ children }) => (
      <code className="rounded-[5px] border border-line-soft bg-surface-code-inline px-[5px] py-px font-mono text-[0.86em]">
        {children}
      </code>
    ),
    pre: ({ node, children }) => {
      if (selectable)
        return (
          <pre className="overflow-auto whitespace-pre-wrap font-mono">
            {children}
          </pre>
        );
      const code = node?.children.find(
        (child) => child.type === "element" && child.tagName === "code",
      );
      if (!code || code.type !== "element")
        return <pre className="overflow-auto">{children}</pre>;
      const text = code.children
        .map((child) => (child.type === "text" ? child.value : ""))
        .join("")
        .replace(/\n$/, "");
      const classes = code.properties.className;
      const languageClass = Array.isArray(classes)
        ? classes.find(
            (item) => typeof item === "string" && item.startsWith("language-"),
          )
        : undefined;
      return (
        <ChatCodeBlock
          code={text}
          language={
            typeof languageClass === "string" ? languageClass.slice(9) : ""
          }
        />
      );
    },
    table: ({ children }) => (
      <div className="max-w-full overflow-x-auto rounded-lg border border-line-default">
        <table className="w-full border-collapse text-body-sm leading-5 tabular-nums [&_tbody_tr:hover_td]:bg-surface-hover [&_tbody_tr+tr_td]:border-t [&_tbody_tr+tr_td]:border-line-soft [&_td]:min-w-20 [&_td]:px-3 [&_td]:py-2 [&_th]:border-b [&_th]:border-line-default [&_th]:bg-surface-hover [&_th]:px-3 [&_th]:py-2 [&_th]:text-left [&_th]:text-caption [&_th]:font-medium [&_th]:whitespace-nowrap [&_th]:text-fg-secondary">
          {children}
        </table>
      </div>
    ),
  };
}

export const ChatMarkdown = memo(function ChatMarkdown({
  source,
  className,
  selectable = false,
}: {
  source: string;
  className?: string;
  selectable?: boolean;
}) {
  const t = useT();
  const prefix = `chat-footnote-${useId()}-`;
  const components = useMemo(
    () => createComponents(prefix, selectable),
    [prefix, selectable],
  );
  return (
    <div
      className={cn(
        "flex min-w-0 flex-col gap-3 text-body leading-6 [overflow-wrap:anywhere]",
        className,
      )}
    >
      <ReactMarkdown
        remarkPlugins={selectable ? [remarkGfm] : [remarkGfm, remarkBreaks]}
        rehypePlugins={selectable ? [remarkSelectionMap] : []}
        remarkRehypeOptions={{
          clobberPrefix: prefix,
          footnoteLabel: t("chat.messages.footnotes"),
          footnoteBackLabel: t("chat.messages.footnoteBack"),
        }}
        components={components}
        skipHtml
        urlTransform={(url, key, node) =>
          key === "href" &&
          url.startsWith(`#${prefix}`) &&
          (node.properties.dataFootnoteRef !== undefined ||
            node.properties.dataFootnoteBackref !== undefined)
            ? url
            : (safeHref(url) ?? "")
        }
      >
        {source}
      </ReactMarkdown>
    </div>
  );
});
