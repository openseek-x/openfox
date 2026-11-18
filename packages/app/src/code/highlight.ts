import { common, createLowlight } from "lowlight";

// 代码高亮（lowlight / highlight.js 常用 37 种语言）。这个模块较大，由 useHighlight 懒加载；
// 文档编辑器（本身就是懒加载分包）直接静态引用。输出拍平的片段列表，调用方自行渲染成 React 元素
// 或 ProseMirror 装饰，不生成 HTML 字符串。

const lowlight = createLowlight(common);

/** 超过这个长度不做高亮，按纯文本显示（避免大文件卡顿）。 */
export const MAX_HIGHLIGHT_CHARS = 200_000;
/** 没有语言标记时，自动识别只用于这个长度以内的代码。 */
export const MAX_AUTO_CHARS = 10_000;
/** 自动识别的相关度低于这个值时不着色（避免把普通文本误判成某种语言）。 */
const MIN_AUTO_RELEVANCE = 5;

/** 一段同样式的文本；classes 为空表示普通文本。 */
export interface CodeToken {
  text: string;
  classes: readonly string[];
}

interface HastNode {
  type: string;
  value?: string;
  properties?: { className?: unknown };
  children?: HastNode[];
}

function flatten(
  node: HastNode,
  inherited: readonly string[],
  out: CodeToken[],
) {
  if (node.type === "text") {
    if (node.value) out.push({ text: node.value, classes: inherited });
    return;
  }
  let classes = inherited;
  const own = node.properties?.className;
  if (Array.isArray(own) && own.length > 0)
    classes = [
      ...inherited,
      ...own.filter((c): c is string => typeof c === "string"),
    ];
  for (const child of node.children ?? []) flatten(child, classes, out);
}

/** lowlight 是否支持这种语言（含别名）。 */
export function supportsLanguage(language: string): boolean {
  return lowlight.registered(language);
}

/**
 * 高亮一段代码。language 为空或不支持时：auto 为 true 且代码不长则自动识别，否则返回 null（按纯文本显示）。
 * 返回的片段按顺序拼起来正好等于原文。
 */
export function highlightTokens(
  code: string,
  language: string | null,
  options: { auto?: boolean } = {},
): CodeToken[] | null {
  if (!code || code.length > MAX_HIGHLIGHT_CHARS) return null;
  let root: HastNode;
  if (language && language !== "plaintext" && lowlight.registered(language)) {
    root = lowlight.highlight(language, code) as unknown as HastNode;
  } else if (!language && options.auto && code.length <= MAX_AUTO_CHARS) {
    const result = lowlight.highlightAuto(code);
    const relevance =
      (result.data as { relevance?: number } | undefined)?.relevance ?? 0;
    if (relevance < MIN_AUTO_RELEVANCE) return null;
    root = result as unknown as HastNode;
  } else {
    return null;
  }
  const out: CodeToken[] = [];
  flatten(root, [], out);
  return out;
}
