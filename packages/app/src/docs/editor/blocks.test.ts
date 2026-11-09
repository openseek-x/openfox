// @vitest-environment node
import { Editor } from "@tiptap/core";
import { describe, expect, it } from "vitest";

import { docExtensions } from "./extensions";
import { moveBlock } from "./BlockControls";

// 块拖拽：落点事务把整个顶层块移到目标位置（不会丢块、不会移入自身）。

/** 三个段落「甲、乙、丙」，返回编辑器与各顶层块的位置。 */
function three() {
  const editor = new Editor({
    extensions: docExtensions(""),
    content: "甲\n\n乙\n\n丙",
    contentType: "markdown",
  });
  const blocks: {
    from: number;
    to: number;
    node: ReturnType<typeof editor.state.doc.child>;
  }[] = [];
  let pos = 0;
  editor.state.doc.forEach((node) => {
    blocks.push({ from: pos, to: pos + node.nodeSize, node });
    pos += node.nodeSize;
  });
  return { editor, blocks };
}

describe("块移动", () => {
  it("把第一块移到最后", () => {
    const { editor, blocks } = three();
    moveBlock(editor, blocks[0]!, blocks[2]!.to);
    expect(editor.getMarkdown()).toBe("乙\n\n丙\n\n甲");
    editor.destroy();
  });

  it("把最后一块移到最前", () => {
    const { editor, blocks } = three();
    moveBlock(editor, blocks[2]!, blocks[0]!.from);
    expect(editor.getMarkdown()).toBe("丙\n\n甲\n\n乙");
    editor.destroy();
  });

  it("落点在自身或紧邻自身的位置时不改动", () => {
    const { editor, blocks } = three();
    const before = editor.getMarkdown();
    moveBlock(editor, blocks[1]!, blocks[1]!.from);
    moveBlock(editor, blocks[1]!, blocks[1]!.to);
    expect(editor.getMarkdown()).toBe(before);
    editor.destroy();
  });

  it("移动后块数量不变", () => {
    const { editor, blocks } = three();
    moveBlock(editor, blocks[0]!, blocks[1]!.to);
    expect(editor.state.doc.childCount).toBe(3);
    editor.destroy();
  });
});
