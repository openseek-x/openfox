import { Editor } from "@tiptap/core";
import { afterEach, expect, it } from "vitest";
import { docExtensions } from "./extensions";
import {
  AnnotationSelection,
  canvasSelection,
  canvasMarkdownAnchor,
} from "./annotationSelection";
let editor: Editor;
afterEach(() => editor?.destroy());
function select(markdown: string, quote: string, nth = 0) {
  editor = new Editor({
    element: document.createElement("div"),
    extensions: [...docExtensions(""), AnnotationSelection],
    content: markdown,
    contentType: "markdown",
  });
  const matches: { from: number; to: number }[] = [];
  editor.state.doc.descendants((n, pos) => {
    if (n.isText) {
      let from = 0;
      while (true) {
        const at = n.text!.indexOf(quote, from);
        if (at < 0) break;
        matches.push({ from: pos + at, to: pos + at + quote.length });
        from = at + quote.length;
      }
    }
  });
  editor.view.dispatch(
    editor.state.tr.setMeta(canvasSelection, { ...matches[nth]!, text: quote }),
  );
}
it("serializes the selected duplicate and Unicode into exact UTF-8 source offsets", () => {
  select("前🙂 **重复**\n\n第二段 **重复**", "重复", 1);
  const anchor = canvasMarkdownAnchor(editor)!;
  expect(anchor).not.toBeNull();
  expect(anchor.quote).toBe("重复");
  const bytes = new TextEncoder().encode(editor.getMarkdown().trimEnd());
  expect(new TextDecoder().decode(bytes.slice(anchor.start, anchor.end))).toBe(
    "重复",
  );
  expect(anchor.start).toBe(
    new TextEncoder().encode(
      editor.getMarkdown().slice(0, editor.getMarkdown().lastIndexOf("重复")),
    ).length,
  );
});
it("maps preceding and boundary inserts without growing the quote", () => {
  select("前面 目标 后面", "目标");
  editor.view.dispatch(editor.state.tr.insertText("🙂", 1));
  let r = canvasSelection.getState(editor.state)!;
  editor.view.dispatch(editor.state.tr.insertText("边界", r.from));
  r = canvasSelection.getState(editor.state)!;
  editor.view.dispatch(editor.state.tr.insertText("后边界", r.to));
  expect(canvasMarkdownAnchor(editor)?.quote).toBe("目标");
});
it("invalidates edits within or deletion of the selected content", () => {
  select("前面目标后面", "目标");
  const r = canvasSelection.getState(editor.state)!;
  editor.view.dispatch(editor.state.tr.insertText("改变", r.from + 1));
  expect(canvasSelection.getState(editor.state)).toBeNull();
  expect(canvasMarkdownAnchor(editor)).toBeNull();
});
it("does not reinterpret a replaced selection as the same object", () => {
  select("前面目标后面", "目标");
  const r = canvasSelection.getState(editor.state)!;
  editor.view.dispatch(editor.state.tr.insertText("目标", r.from, r.to));
  expect(canvasSelection.getState(editor.state)).toBeNull();
});
