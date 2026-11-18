import { parseFragment, defaultTreeAdapter } from "parse5";
import { textAnchor } from "./anchors";

export type SelectionRect = {
  left: number;
  top: number;
  width: number;
  height: number;
};

/** Map rendered text boundaries to the original source, within one parser node.
 * Never search the whole file: repeated paragraphs must keep their own offsets. */
export function sourceOffsets(
  raw: string,
  shown: string,
  markdown = false,
): number[] | null {
  let value = "";
  const offsets = [0];
  for (let i = 0; i < raw.length;) {
    let token = raw[i]!,
      length = 1;
    if (raw[i] === "\r") {
      token = "\n";
      length = raw[i + 1] === "\n" ? 2 : 1;
    } else if (
      markdown &&
      raw[i] === "\\" &&
      /[!"#$%&'()*+,\-./:;<=>?@[\]\\^_`{|}~]/.test(raw[i + 1] ?? "")
    ) {
      token = raw[i + 1]!;
      length = 2;
    } else if (raw[i] === "&") {
      const entity = raw
        .slice(i)
        .match(
          /^&(?:#[xX][0-9a-fA-F]+;?|#[0-9]+;?|[a-zA-Z][a-zA-Z0-9]*;?)/,
        )?.[0];
      if (entity) {
        const nodes = parseFragment(entity).childNodes;
        if (nodes.length === 1 && defaultTreeAdapter.isTextNode(nodes[0]!)) {
          token = nodes[0].value;
          length = entity.length;
        }
      }
    }
    value += token;
    // A boundary inside a decoded entity is deliberately unmappable.
    for (let j = 1; j < token.length; j++) offsets.push(-1);
    i += length;
    offsets.push(i);
  }
  return value === shown ? offsets : null;
}

export function domTextAnchor(root: HTMLElement, source: string, range: Range) {
  const endpoint = (node: Node, offset: number) => {
    const el =
      node.nodeType === Node.ELEMENT_NODE
        ? (node as Element)
        : node.parentElement;
    const mapped = el?.closest<HTMLElement>("[data-comment-start]");
    if (mapped && root.contains(mapped)) {
      const base = Number(
        mapped.closest<HTMLElement>("[data-comment-body-offset]")?.dataset
          .commentBodyOffset ?? 0,
      );
      const start = base + Number(mapped.dataset.commentStart),
        end = base + Number(mapped.dataset.commentEnd);
      const prefix = document.createRange();
      prefix.selectNodeContents(mapped);
      prefix.setEnd(node, offset);
      const normalized = source.replace(/^\ufeff/, "").replace(/\r\n/g, "\n");
      const map = sourceOffsets(
        normalized.slice(start, end),
        mapped.textContent ?? "",
        true,
      );
      const at = map?.[prefix.toString().length];
      if (at === undefined || at < 0) return null;
      let raw = source.startsWith("\ufeff") ? 1 : 0;
      for (let i = 0; i < start + at; i++)
        raw += source[raw] === "\r" && source[raw + 1] === "\n" ? 2 : 1;
      return raw;
    }
    const code = el?.closest("pre > code");
    if (code && root.contains(code) && root.dataset.commentPlain === "true") {
      const prefix = document.createRange();
      prefix.selectNodeContents(code);
      prefix.setEnd(node, offset);
      const shown = code.textContent ?? "";
      return source.startsWith(shown) ? prefix.toString().length : null;
    }
    return null;
  };
  if (
    !root.contains(range.startContainer) ||
    !root.contains(range.endContainer) ||
    range.collapsed
  )
    return null;
  const start = endpoint(range.startContainer, range.startOffset);
  const end = endpoint(range.endContainer, range.endOffset);
  return start === null || end === null ? null : textAnchor(source, start, end);
}

// A rehype plugin using the parser's exact source positions. Generated markup
// (footnote controls, etc.) has no source range and cannot become an edit target.
type TreeNode = {
  type: string;
  value?: string;
  tagName?: string;
  properties?: Record<string, unknown>;
  position?: { start: { offset?: number }; end: { offset?: number } };
  children?: TreeNode[];
};
export function remarkSelectionMap() {
  return (root: TreeNode, file: { value: unknown }) => {
    const source = String(file.value);
    const visit = (node: TreeNode) => {
      if (!node.children) return;
      node.children = node.children.map((child) => {
        if (child.type !== "text") {
          visit(child);
          return child;
        }
        let start = child.position?.start.offset,
          end = child.position?.end.offset;
        if (start === undefined && node.tagName === "code") {
          const a = node.position?.start.offset,
            b = node.position?.end.offset;
          if (a !== undefined && b !== undefined) {
            const raw = source.slice(a, b);
            // Inline and fenced code positions include their delimiters.
            const prefix =
              /^(?:`{3,}|~{3,})[^\r\n]*\r?\n/.exec(raw)?.[0].length ??
              /^`+/.exec(raw)?.[0].length ??
              0;
            const value = (child.value ?? "").replace(/\n$/, "");
            if (raw.slice(prefix).startsWith(value)) {
              start = a + prefix;
              end = start + value.length;
              child = { ...child, value };
            }
          }
        }
        if (
          start === undefined ||
          end === undefined ||
          !sourceOffsets(source.slice(start, end), child.value ?? "", true)
        )
          return child;
        return {
          type: "element",
          tagName: "span",
          properties: {
            dataCommentStart: start,
            dataCommentEnd: end,
            className: ["whitespace-pre-wrap"],
          },
          children: [child],
        };
      });
    };
    visit(root);
  };
}
