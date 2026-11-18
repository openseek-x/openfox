import { Extension } from "@tiptap/core";
import type { Node as ProseMirrorNode } from "@tiptap/pm/model";
import { Plugin, PluginKey } from "@tiptap/pm/state";
import { Decoration, DecorationSet } from "@tiptap/pm/view";

import { highlightTokens, type CodeToken } from "../../code/highlight";
import { languageForFence } from "../../code/languages";

// 文档正文代码块的语法高亮：只加 ProseMirror 行内装饰（hljs-* 类名），不改文档内容，
// 所以 Markdown 往返不受影响。代码块的语言取 ```ts 之类的围栏标记；没有标记时按内容自动识别。

const highlightKey = new PluginKey<DecorationSet>("docCodeHighlight");

/** 按「语言 + 内容」缓存片段：打字时只有正在编辑的代码块需要重新高亮。 */
const cache = new Map<string, CodeToken[] | null>();
const CACHE_LIMIT = 200;

function tokensFor(code: string, language: string | null): CodeToken[] | null {
  const key = `${language ?? ""}\u0000${code}`;
  const hit = cache.get(key);
  if (hit !== undefined) return hit;
  const tokens = highlightTokens(code, language, { auto: true });
  if (cache.size >= CACHE_LIMIT) cache.clear();
  cache.set(key, tokens);
  return tokens;
}

/** 为文档里所有代码块生成高亮装饰（导出供无头测试使用：未挂载的编辑器不初始化插件）。 */
export function buildDecorations(doc: ProseMirrorNode): DecorationSet {
  const decorations: Decoration[] = [];
  doc.descendants((node, pos) => {
    if (node.type.name !== "codeBlock") return true;
    const language = languageForFence(node.attrs.language as string | null);
    const tokens = tokensFor(node.textContent, language);
    if (tokens) {
      // 代码块内容从 pos + 1 开始；片段拼起来正好等于原文，位置一一对应
      let from = pos + 1;
      for (const token of tokens) {
        const to = from + token.text.length;
        if (token.classes.length > 0)
          decorations.push(
            Decoration.inline(from, to, { class: token.classes.join(" ") }),
          );
        from = to;
      }
    }
    return false;
  });
  return DecorationSet.create(doc, decorations);
}

export const CodeHighlight = Extension.create({
  name: "docCodeHighlight",
  addProseMirrorPlugins() {
    return [
      new Plugin<DecorationSet>({
        key: highlightKey,
        state: {
          init: (_, state) => buildDecorations(state.doc),
          apply: (tr, previous) =>
            tr.docChanged
              ? buildDecorations(tr.doc)
              : previous.map(tr.mapping, tr.doc),
        },
        props: {
          decorations: (state) => highlightKey.getState(state),
        },
      }),
    ];
  },
});
