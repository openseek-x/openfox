import { memo, useEffect, useId, useMemo, useRef, useState } from "react";
import ReactMarkdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";
import remarkBreaks from "remark-breaks";

import { useT } from "@openfox/i18n";
import {
  CodeBlock,
  CodeBlockGroup,
  Icon,
  IconButton,
  cn,
  useToast,
} from "@openfox/ui";

import { languageForFence } from "../code/languages";
import { HighlightedCode } from "../code/useHighlight";
import { copyText } from "../tasks/parts";
import { safeHref } from "./Markdown";

function ChatCodeBlock({ code, language }: { code: string; language: string }) {
  const t = useT();
  const toast = useToast();
  const [copied, setCopied] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const revision = useRef(0);
  useEffect(() => {
    setCopied(false);
    return () => {
      revision.current++;
      clearTimeout(timer.current);
    };
  }, [code]);
  return (
    <CodeBlock>
      <CodeBlockGroup>
        <span className="truncate font-mono">{language || "text"}</span>
        <IconButton
          label={t(copied ? "chat.messages.copied" : "chat.messages.copy")}
          size="28"
          variant="ghost"
          icon={
            <Icon name={copied ? "copy-check" : "openfox-copy"} size={14} />
          }
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
        />
      </CodeBlockGroup>
      <pre
        data-lang={language || undefined}
        className="max-h-[400px] overflow-auto p-4 font-mono text-code leading-6 whitespace-pre"
      >
        <code>
          <HighlightedCode
            code={code}
            language={languageForFence(language)}
            auto
          />
        </code>
      </pre>
    </CodeBlock>
  );
}

function createComponents(prefix: string): Components {
  return {
    h1: ({ children }) => (
      <h3 className="text-title font-semibold">{children}</h3>
    ),
    h2: ({ children, id, className }) => (
      <h4
        id={id === "footnote-label" ? `${prefix}label` : id}
        className={cn("text-subheading font-semibold", className)}
      >
        {children}
      </h4>
    ),
    h3: ({ children }) => (
      <h5 className="text-body font-semibold">{children}</h5>
    ),
    h4: ({ children }) => <h6 className="text-body font-medium">{children}</h6>,
    h5: ({ children }) => <h6 className="text-body font-medium">{children}</h6>,
    h6: ({ children }) => <h6 className="text-body font-medium">{children}</h6>,
    a: ({ href, children, node, ...props }) => {
      if (href?.startsWith(`#${prefix}`)) {
        return (
          <a
            {...props}
            href={href}
            aria-describedby={
              node?.properties.dataFootnoteRef ? `${prefix}label` : undefined
            }
            className="text-info underline underline-offset-4"
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
          className="text-info underline decoration-info/40 underline-offset-4 hover:decoration-info"
        >
          {children}
        </a>
      ) : (
        <>{children}</>
      );
    },
    img: ({ alt }) => <span className="text-fg-muted">{alt}</span>,
    ul: ({ children }) => (
      <ul className="list-disc space-y-1 pl-6 [&:has(>li.task-list-item)]:list-none">
        {children}
      </ul>
    ),
    ol: ({ children, start }) => (
      <ol start={start} className="list-decimal space-y-1 pl-6">
        {children}
      </ol>
    ),
    li: ({ children, className, id }) => (
      <li
        id={id}
        tabIndex={id ? -1 : undefined}
        className={cn("pl-1 [&>p]:my-1", className)}
      >
        {children}
      </li>
    ),
    blockquote: ({ children }) => (
      <blockquote className="space-y-2 border-l-2 border-line-strong pl-4 text-fg-secondary">
        {children}
      </blockquote>
    ),
    hr: () => <hr className="border-line-default" />,
    code: ({ children }) => (
      <code className="rounded bg-surface-muted px-1 py-0.5 font-mono text-[0.9em]">
        {children}
      </code>
    ),
    pre: ({ node, children }) => {
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
        <table className="w-full border-collapse text-body-sm [&_td]:border-t [&_td]:border-line-default [&_td]:px-3 [&_td]:py-2 [&_th]:bg-surface-muted [&_th]:px-3 [&_th]:py-2 [&_th]:text-left [&_th]:font-medium">
          {children}
        </table>
      </div>
    ),
  };
}

export const ChatMarkdown = memo(function ChatMarkdown({
  source,
  className,
}: {
  source: string;
  className?: string;
}) {
  const t = useT();
  const prefix = `chat-footnote-${useId()}-`;
  const components = useMemo(() => createComponents(prefix), [prefix]);
  return (
    <div
      className={cn(
        "flex min-w-0 flex-col gap-3 text-body leading-7 [overflow-wrap:anywhere]",
        className,
      )}
    >
      <ReactMarkdown
        remarkPlugins={[remarkGfm, remarkBreaks]}
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
