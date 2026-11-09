import { Fragment, type ReactNode } from "react";

import { languageForFence } from "../code/languages";
import { HighlightedCode } from "../code/useHighlight";

// 最小安全 Markdown 渲染（Agent 回复）：不引入依赖，不使用 dangerouslySetInnerHTML。
// 只生成 React 元素，正文一律作为文本节点；链接只允许 http / https / mailto。
// 支持：段落、# 标题（1–3 级）、无序 / 有序列表、> 引用、``` 代码块（语法高亮，懒加载）、`行内代码`、**粗体**、*斜体*、[文字](链接)、---。

const SAFE_URL = /^(https?:\/\/|mailto:)/i;

/** 只放行 http / https / mailto 的链接地址，其余返回 null（按纯文本显示） */
export function safeHref(raw: string): string | null {
  const value = raw.trim();
  if (!SAFE_URL.test(value)) return null;
  try {
    const url = new URL(value);
    return ["http:", "https:", "mailto:"].includes(url.protocol)
      ? url.href
      : null;
  } catch {
    return null;
  }
}

// 行内标记：代码优先（其中不再解析其他标记），再是链接、粗体、斜体
const INLINE =
  /(`[^`\n]+`)|\[([^\]\n]+)\]\(([^)\s]+)\)|\*\*([^*\n]+)\*\*|(?<![*\w])\*([^*\n]+)\*(?!\w)|(https?:\/\/[^\s<>()]+[^\s<>().,;:!?'"])/g;

type TextRenderer = (text: string, key: string) => ReactNode;

export function renderInline(
  text: string,
  keyPrefix = "i",
  renderText?: TextRenderer,
): ReactNode[] {
  const nodes: ReactNode[] = [];
  let last = 0;
  let index = 0;
  for (const match of text.matchAll(INLINE)) {
    const start = match.index;
    if (start > last) {
      const plain = text.slice(last, start);
      nodes.push(
        renderText ? renderText(plain, `${keyPrefix}-text-${index}`) : plain,
      );
    }
    const key = `${keyPrefix}-${index++}`;
    const [whole, code, label, href, bold, italic, bare] = match;
    if (code) {
      nodes.push(
        <code
          key={key}
          className="rounded bg-surface-muted px-1 py-0.5 font-mono text-[0.9em]"
        >
          {code.slice(1, -1)}
        </code>,
      );
    } else if (label !== undefined && href !== undefined) {
      const safe = safeHref(href);
      nodes.push(
        safe ? (
          <a
            key={key}
            href={safe}
            target="_blank"
            rel="noopener noreferrer"
            className="text-info underline underline-offset-2"
          >
            {renderInline(label, key)}
          </a>
        ) : (
          <Fragment key={key}>{whole}</Fragment>
        ),
      );
    } else if (bold) {
      nodes.push(
        <strong key={key} className="font-semibold">
          {renderInline(bold, key, renderText)}
        </strong>,
      );
    } else if (italic) {
      nodes.push(<em key={key}>{renderInline(italic, key, renderText)}</em>);
    } else if (bare) {
      const safe = safeHref(bare);
      nodes.push(
        safe ? (
          <a
            key={key}
            href={safe}
            target="_blank"
            rel="noopener noreferrer"
            className="text-info underline underline-offset-2"
          >
            {bare}
          </a>
        ) : (
          bare
        ),
      );
    }
    last = start + whole.length;
  }
  if (last < text.length) {
    const plain = text.slice(last);
    nodes.push(
      renderText ? renderText(plain, `${keyPrefix}-text-${index}`) : plain,
    );
  }
  return nodes;
}

type Block =
  | { kind: "heading"; level: 1 | 2 | 3; text: string }
  | { kind: "paragraph"; lines: string[] }
  | { kind: "list"; ordered: boolean; start: number; items: string[] }
  | { kind: "quote"; lines: string[] }
  | { kind: "code"; lang: string; text: string }
  | { kind: "rule" };

const LIST_ITEM = /^\s{0,3}([-*+]|\d{1,9}[.)])\s+(.*)$/;

/** 把正文切成块；未闭合的代码块延续到末尾（流式输出时常见） */
export function parseBlocks(source: string): Block[] {
  const lines = source.replace(/\r\n?/g, "\n").split("\n");
  const blocks: Block[] = [];
  let i = 0;
  while (i < lines.length) {
    const line = lines[i]!;
    const fence = /^\s{0,3}```\s*([\w+-]*)\s*$/.exec(line);
    if (fence) {
      const body: string[] = [];
      i++;
      while (i < lines.length && !/^\s{0,3}```\s*$/.test(lines[i]!))
        body.push(lines[i++]!);
      i++;
      blocks.push({
        kind: "code",
        lang: fence[1] ?? "",
        text: body.join("\n"),
      });
      continue;
    }
    if (!line.trim()) {
      i++;
      continue;
    }
    const heading = /^\s{0,3}(#{1,6})\s+(.*?)\s*#*\s*$/.exec(line);
    if (heading) {
      const level = Math.min(heading[1]!.length, 3) as 1 | 2 | 3;
      blocks.push({ kind: "heading", level, text: heading[2]! });
      i++;
      continue;
    }
    if (/^\s{0,3}([-*_])(\s*\1){2,}\s*$/.test(line)) {
      blocks.push({ kind: "rule" });
      i++;
      continue;
    }
    if (/^\s{0,3}>/.test(line)) {
      const quote: string[] = [];
      while (i < lines.length && /^\s{0,3}>/.test(lines[i]!))
        quote.push(lines[i++]!.replace(/^\s{0,3}>\s?/, ""));
      blocks.push({ kind: "quote", lines: quote });
      continue;
    }
    const item = LIST_ITEM.exec(line);
    if (item) {
      const ordered = /\d/.test(item[1]!);
      const items: string[] = [];
      while (i < lines.length) {
        const current = LIST_ITEM.exec(lines[i]!);
        if (current && /\d/.test(current[1]!) === ordered) {
          items.push(current[2]!);
          i++;
        } else if (
          lines[i]!.trim() &&
          /^\s{2,}/.test(lines[i]!) &&
          items.length > 0
        ) {
          // 缩进的续行并入上一项
          items[items.length - 1] += `\n${lines[i]!.trim()}`;
          i++;
        } else break;
      }
      blocks.push({
        kind: "list",
        ordered,
        start: ordered ? Number.parseInt(item[1]!, 10) || 1 : 1,
        items,
      });
      continue;
    }
    const paragraph: string[] = [];
    while (
      i < lines.length &&
      lines[i]!.trim() &&
      !/^\s{0,3}(```|#{1,6}\s|>)/.test(lines[i]!) &&
      !LIST_ITEM.test(lines[i]!)
    )
      paragraph.push(lines[i++]!);
    blocks.push({ kind: "paragraph", lines: paragraph });
  }
  return blocks;
}

/** 多行文本：行内标记 + 换行 */
function withBreaks(
  lines: string[],
  key: string,
  renderText?: TextRenderer,
): ReactNode[] {
  return lines.flatMap((line, index) => [
    ...(index > 0 ? [<br key={`${key}-br-${index}`} />] : []),
    ...renderInline(line, `${key}-${index}`, renderText),
  ]);
}

const headingClass: Record<1 | 2 | 3, string> = {
  1: "text-title font-bold",
  2: "text-subheading font-semibold",
  3: "text-body font-semibold",
};

export function Markdown({
  source,
  className,
  renderText,
}: {
  source: string;
  className?: string;
  renderText?: TextRenderer;
}) {
  const blocks = parseBlocks(source);
  return (
    <div
      className={`flex flex-col gap-2 text-body break-words ${className ?? ""}`}
    >
      {blocks.map((block, index) => {
        const key = `b${index}`;
        switch (block.kind) {
          case "heading": {
            const Tag = `h${block.level + 2}` as "h3" | "h4" | "h5";
            return (
              <Tag key={key} className={headingClass[block.level]}>
                {renderInline(block.text, key, renderText)}
              </Tag>
            );
          }
          case "paragraph":
            return <p key={key}>{withBreaks(block.lines, key, renderText)}</p>;
          case "list": {
            const items = block.items.map((item, n) => (
              <li key={`${key}-${n}`}>
                {withBreaks(item.split("\n"), `${key}-${n}`, renderText)}
              </li>
            ));
            return block.ordered ? (
              <ol
                key={key}
                start={block.start}
                className="list-decimal space-y-0.5 pl-5"
              >
                {items}
              </ol>
            ) : (
              <ul key={key} className="list-disc space-y-0.5 pl-5">
                {items}
              </ul>
            );
          }
          case "quote":
            return (
              <blockquote
                key={key}
                className="border-l-2 border-line-default pl-3 text-fg-secondary"
              >
                {withBreaks(block.lines, key, renderText)}
              </blockquote>
            );
          case "code":
            return (
              <pre
                key={key}
                data-lang={block.lang || undefined}
                className="max-h-[400px] overflow-auto rounded-lg bg-surface-muted p-3 font-mono text-code whitespace-pre"
              >
                <code>
                  <HighlightedCode
                    code={block.text}
                    language={languageForFence(block.lang)}
                    auto
                  />
                </code>
              </pre>
            );
          case "rule":
            return <hr key={key} className="border-line-default" />;
        }
      })}
    </div>
  );
}
