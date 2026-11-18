import { Editor } from "@tiptap/core";
import { afterEach, describe, expect, it, vi } from "vitest";
import { docExtensions } from "./extensions";
import {
  DocBodySearch,
  docSearchKey,
  findDocMatches,
  navigateDocSearch,
  updateDocSearch,
} from "./search";

if (!Element.prototype.scrollIntoView)
  Element.prototype.scrollIntoView = () => undefined;

const editors: Editor[] = [];
afterEach(() => editors.splice(0).forEach((editor) => editor.destroy()));
function makeEditor(markdown: string, editable = true) {
  const editor = new Editor({
    element: document.createElement("div"),
    extensions: [...docExtensions(""), DocBodySearch],
    content: markdown,
    contentType: "markdown",
    editable,
  });
  editors.push(editor);
  return editor;
}

describe("文档正文查找", () => {
  it("匹配中文、跨行内格式、代码块和字面标点，不匹配 Markdown 标记", () => {
    const editor = makeEditor(
      "# 中文标题\n\n中**文**内容.*\n\n```ts\n中文\n```\n\n[链接](https://hidden.example)",
    );
    const matches = findDocMatches(editor.state.doc, "中文", false);
    expect(matches).toHaveLength(3);
    expect(
      matches.map((m) => editor.state.doc.textBetween(m.from, m.to)),
    ).toEqual(["中文", "中文", "中文"]);
    expect(findDocMatches(editor.state.doc, ".*", false)).toHaveLength(1);
    expect(findDocMatches(editor.state.doc, "**", false)).toHaveLength(0);
    expect(
      findDocMatches(editor.state.doc, "hidden.example", false),
    ).toHaveLength(0);
  });

  it("区分大小写可切换；Unicode 大小写匹配不破坏位置", () => {
    const editor = makeEditor("Abc abc ABC İ中");
    expect(findDocMatches(editor.state.doc, "abc", false)).toHaveLength(3);
    expect(findDocMatches(editor.state.doc, "abc", true)).toHaveLength(1);
    const match = findDocMatches(editor.state.doc, "中", false)[0]!;
    expect(editor.state.doc.textBetween(match.from, match.to)).toBe("中");
    expect(findDocMatches(editor.state.doc, "", false)).toEqual([]);
  });

  it("只读文档上下项循环且不修改正文、选择或触发保存，清空后移除装饰", () => {
    const editor = makeEditor("中文 中文", false);
    const onUpdate = vi.fn();
    editor.on("update", onUpdate);
    const before = editor.getMarkdown();
    const jsonBefore = editor.getJSON();
    const selection = editor.state.selection.toJSON();
    updateDocSearch(editor, "中文", false);
    expect(docSearchKey.getState(editor.state)?.matches).toHaveLength(2);
    navigateDocSearch(editor, -1);
    expect(docSearchKey.getState(editor.state)?.active).toBe(1);
    navigateDocSearch(editor, 1);
    expect(docSearchKey.getState(editor.state)?.active).toBe(0);
    expect(editor.getMarkdown()).toBe(before);
    expect(editor.getJSON()).toEqual(jsonBefore);
    expect(editor.state.selection.toJSON()).toEqual(selection);
    expect(onUpdate).not.toHaveBeenCalled();
    updateDocSearch(editor, "", false);
    expect(
      docSearchKey.getState(editor.state)?.decorations.find(),
    ).toHaveLength(0);
  });

  it.each([
    "# 中文",
    "```ts\n中文\n```",
    "| 中文 | 值 |\n| --- | --- |\n| 一 | 二 |",
  ])("搜索末尾非段落正文也不改变编辑器文档：%s", (markdown) => {
    const editor = makeEditor(markdown, false);
    const before = editor.getJSON();
    updateDocSearch(editor, "中文", false);
    expect(editor.getJSON()).toEqual(before);
  });

  it("焦点等显示事务不补尾段，真实编辑仍保留尾段行为", () => {
    const editor = makeEditor("# 中文");
    const before = editor.getJSON();
    editor.view.dispatch(editor.state.tr.setMeta("focus", true));
    expect(editor.getJSON()).toEqual(before);
    editor.commands.insertContentAt(editor.state.doc.content.size - 1, "编辑");
    expect(editor.getJSON().content?.at(-1)?.type).toBe("paragraph");
    expect(editor.getMarkdown()).toContain("中文编辑");
  });

  it("编辑后重新计算匹配，搜索状态不进入撤销历史", () => {
    const editor = makeEditor("中文");
    updateDocSearch(editor, "中文", false);
    editor.commands.insertContentAt(editor.state.doc.content.size - 1, " 中文");
    expect(docSearchKey.getState(editor.state)?.matches).toHaveLength(2);
    navigateDocSearch(editor, 1);
    editor.commands.undo();
    expect(docSearchKey.getState(editor.state)?.matches).toHaveLength(1);
    expect(editor.getMarkdown().trim()).toBe("中文");
  });
});
