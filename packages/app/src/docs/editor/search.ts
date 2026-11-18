import { Extension, type Editor } from "@tiptap/core";
import { skipTrailingNodeMeta } from "@tiptap/extensions";
import type { Node as ProseMirrorNode } from "@tiptap/pm/model";
import { Plugin, PluginKey } from "@tiptap/pm/state";
import { Decoration, DecorationSet } from "@tiptap/pm/view";

export interface SearchMatch {
  from: number;
  to: number;
}
interface SearchState {
  query: string;
  matchCase: boolean;
  active: number;
  matches: SearchMatch[];
  decorations: DecorationSet;
}
export const docSearchKey = new PluginKey<SearchState>("docBodySearch");

/** Search whole text blocks, including adjacent nodes split by inline formatting.
 * Literal regex matching preserves UTF-16 offsets even for Unicode case folding.
 */
export function findDocMatches(
  doc: ProseMirrorNode,
  query: string,
  matchCase: boolean,
): SearchMatch[] {
  if (!query) return [];
  const pattern = new RegExp(
    query.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"),
    matchCase ? "gu" : "giu",
  );
  const matches: SearchMatch[] = [];
  doc.descendants((node, pos) => {
    if (!node.isTextblock) return true;
    const text = node.textBetween(0, node.content.size, "", "\ufffc");
    for (const match of text.matchAll(pattern)) {
      const from = pos + 1 + match.index;
      matches.push({ from, to: from + match[0].length });
    }
    return false;
  });
  return matches;
}

function searchState(
  doc: ProseMirrorNode,
  query: string,
  matchCase: boolean,
  active: number,
): SearchState {
  const matches = findDocMatches(doc, query, matchCase);
  const selected = matches.length
    ? ((active % matches.length) + matches.length) % matches.length
    : 0;
  return {
    query,
    matchCase,
    active: selected,
    matches,
    decorations: DecorationSet.create(
      doc,
      matches.map((match, index) =>
        Decoration.inline(match.from, match.to, {
          class:
            index === selected
              ? "doc-find-match doc-find-active"
              : "doc-find-match",
        }),
      ),
    ),
  };
}

export const DocBodySearch = Extension.create({
  name: "docBodySearch",
  addProseMirrorPlugins() {
    return [
      new Plugin<SearchState>({
        key: docSearchKey,
        // Opening/focusing the find UI can also dispatch selection/focus
        // transactions. TrailingNode must not turn display-only transactions
        // into document edits; it still runs for actual typing and commands.
        filterTransaction: (transaction) => {
          if (!transaction.docChanged)
            transaction.setMeta(skipTrailingNodeMeta, true);
          return true;
        },
        state: {
          init: (_, state) => searchState(state.doc, "", false, 0),
          apply: (tr, previous) => {
            const update = tr.getMeta(docSearchKey) as
              | Partial<Pick<SearchState, "query" | "matchCase" | "active">>
              | undefined;
            if (!update && !tr.docChanged) return previous;
            return searchState(
              tr.doc,
              update?.query ?? previous.query,
              update?.matchCase ?? previous.matchCase,
              update?.active ?? previous.active,
            );
          },
        },
        props: {
          decorations: (state) => docSearchKey.getState(state)?.decorations,
        },
      }),
    ];
  },
});

export function updateDocSearch(
  editor: Editor,
  query: string,
  matchCase: boolean,
) {
  editor.view.dispatch(
    editor.state.tr
      .setMeta(docSearchKey, { query, matchCase, active: 0 })
      .setMeta("addToHistory", false)
      .setMeta("preventUpdate", true)
      .setMeta(skipTrailingNodeMeta, true),
  );
}

export function navigateDocSearch(editor: Editor, direction: number) {
  const state = docSearchKey.getState(editor.state);
  if (!state?.matches.length) return;
  editor.view.dispatch(
    editor.state.tr
      .setMeta(docSearchKey, { active: state.active + direction })
      .setMeta("addToHistory", false)
      .setMeta("preventUpdate", true)
      .setMeta(skipTrailingNodeMeta, true),
  );
  // Decorations work in read-only documents too. Scrolling does not focus the
  // editor or replace the user's selection, so the query field keeps focus.
  editor.view.dom
    .querySelector(".doc-find-active")
    ?.scrollIntoView({ block: "nearest" });
}
