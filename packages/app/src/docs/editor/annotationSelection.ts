import { Extension, type Editor } from "@tiptap/core";
import type {} from "@tiptap/markdown";
import { Plugin, PluginKey } from "@tiptap/pm/state";
import { Transform } from "@tiptap/pm/transform";
import { Decoration, DecorationSet } from "@tiptap/pm/view";
import type { ArtifactReviewAnchor } from "@openfox/api";

export type CanvasRange = { from: number; to: number; text: string };
export const canvasSelection = new PluginKey<CanvasRange | null>(
  "canvas-selection",
);
export const AnnotationSelection = Extension.create({
  name: "annotationSelection",
  addProseMirrorPlugins() {
    return [
      new Plugin<CanvasRange | null>({
        key: canvasSelection,
        state: {
          init: () => null,
          apply(tr, value) {
            const explicit = tr.getMeta(canvasSelection);
            if (explicit !== undefined) return explicit;
            if (!value || !tr.docChanged) return value;
            // Boundary inserts stay outside; changes inside the quote invalidate it.
            const from = tr.mapping.mapResult(value.from, 1);
            const to = tr.mapping.mapResult(value.to, -1);
            if (
              from.deleted ||
              to.deleted ||
              from.pos >= to.pos ||
              tr.doc.textBetween(from.pos, to.pos, "\n") !== value.text
            )
              return null;
            return { ...value, from: from.pos, to: to.pos };
          },
        },
        props: {
          decorations(state) {
            const range = canvasSelection.getState(state);
            return range
              ? DecorationSet.create(state.doc, [
                  Decoration.inline(range.from, range.to, {
                    class:
                      "bg-blue-500/20 outline outline-1 outline-blue-500/50",
                  }),
                ])
              : DecorationSet.empty;
          },
        },
      }),
    ];
  },
});

export function selectCanvasRange(editor: Editor) {
  const { from, to } = editor.state.selection;
  const text = editor.state.doc.textBetween(from, to, "\n");
  if (from === to || !text.trim()) return null;
  const range = { from, to, text };
  editor.view.dispatch(editor.state.tr.setMeta(canvasSelection, range));
  return range;
}

/** Serialize a detached document with boundary sentinels. Never mutate the editor
 * or guess among duplicate strings. Accept only a byte-identical serialization
 * after removing sentinels; unsupported Markdown boundaries fail closed. */
export function canvasMarkdownAnchor(
  editor: Editor,
): ArtifactReviewAnchor | null {
  const range = canvasSelection.getState(editor.state);
  if (!range || !editor.markdown) return null;
  const body = editor.getMarkdown().trimEnd();
  const a = "\uE000",
    b = "\uE001";
  if (body.includes(a) || body.includes(b)) return null;
  try {
    const doc = editor.state.doc;
    const mapped = new Transform(doc)
      .insert(
        range.to,
        editor.schema.text(b, doc.resolve(range.to).nodeBefore?.marks),
      )
      .insert(
        range.from,
        editor.schema.text(a, doc.resolve(range.from).nodeAfter?.marks),
      ).doc;
    const marked = editor.markdown.serialize(mapped.toJSON()).trimEnd();
    const from = marked.indexOf(a),
      to = marked.indexOf(b);
    if (from < 0 || to <= from || marked.replace(a, "").replace(b, "") !== body)
      return null;
    const quote = marked.slice(from + 1, to);
    const bytes = new TextEncoder();
    if (!quote || bytes.encode(quote).length > 8192) return null;
    const start = bytes.encode(marked.slice(0, from)).length;
    return {
      kind: "text",
      start,
      end: start + bytes.encode(quote).length,
      quote,
    };
  } catch {
    return null;
  }
}
