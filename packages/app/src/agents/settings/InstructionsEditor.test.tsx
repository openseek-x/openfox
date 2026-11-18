import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { Editor } from "@tiptap/core";

import { docExtensions } from "../../docs/editor/extensions";
import { InstructionsEditor } from "./InstructionsEditor";
import { InstructionDisplayTransactions } from "./instructionExtensions";
import { instructionChanges } from "./InstructionsHistory";

const markdown =
  "# 中文标题\n\n- 第一项\n- **加粗**\n\n```typescript\nconst hello = '世界';\n```\n";

describe("instruction editing", () => {
  it("switches source/rich without changing Markdown or saving; shared schema preserves headings, list and code", async () => {
    const save = vi.fn(async (value: string) => value);
    const user = userEvent.setup();
    render(
      <InstructionsEditor
        saved={markdown}
        label="指令"
        editable
        onSave={save}
        onDirty={() => {}}
      />,
    );
    await user.click(screen.getByRole("button", { name: "富文本" }));
    expect(
      await screen.findByRole("heading", { name: "中文标题" }),
    ).toBeVisible();
    expect(screen.getByText("第一项")).toBeVisible();
    await user.click(screen.getByRole("button", { name: "源码" }));
    expect(screen.getByRole("textbox", { name: "指令" })).toHaveValue(markdown);
    expect(save).not.toHaveBeenCalled();
    const editor = new Editor({
      extensions: docExtensions(""),
      contentType: "markdown",
      content: markdown,
    });
    const serialized = editor.getMarkdown();
    expect(serialized).toContain("# 中文标题");
    expect(serialized).toContain("- 第一项");
    expect(serialized).toContain("```typescript\nconst hello = '世界';");
    editor.destroy();
  });

  it("preserves DOM, text and caret across expansion/Escape, then keeps the draft after a failed save", async () => {
    const user = userEvent.setup();
    const dirty = vi.fn();
    render(
      <InstructionsEditor
        saved="old"
        label="指令"
        editable
        onSave={async () => undefined}
        onDirty={dirty}
      />,
    );
    const input = screen.getByRole("textbox", {
      name: "指令",
    }) as HTMLTextAreaElement;
    fireEvent.change(input, { target: { value: "unsaved 中文" } });
    input.setSelectionRange(3, 5);
    await user.click(screen.getByRole("button", { name: "展开编辑器" }));
    expect(screen.getByRole("dialog", { name: "指令" })).toBeVisible();
    expect(screen.getByRole("textbox", { name: "指令" })).toBe(input);
    expect([input.selectionStart, input.selectionEnd]).toEqual([3, 5]);
    fireEvent.change(input, { target: { value: "unsaved 中文 updated" } });
    input.setSelectionRange(8, 10);
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(input).toHaveValue("unsaved 中文 updated");
    expect([input.selectionStart, input.selectionEnd]).toEqual([8, 10]);
    await user.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "保存" })).toBeEnabled(),
    );
    expect(input).toHaveValue("unsaved 中文 updated");
    expect(dirty).toHaveBeenLastCalledWith(true);
  });

  it("keeps the rich editor instance and its cursor while expanded, and exports actual rich edits", async () => {
    const user = userEvent.setup();
    const save = vi.fn(async (value: string) => value);
    render(
      <InstructionsEditor
        saved="hello 中文"
        label="指令"
        editable
        onSave={save}
        onDirty={() => {}}
      />,
    );
    await user.click(screen.getByRole("button", { name: "富文本" }));
    const dom = await screen.findByRole("textbox", { name: "指令" });
    // TipTap attaches its public Editor instance to its DOM root.
    const editor = (dom as HTMLElement & { editor: Editor }).editor;
    act(() => {
      editor.commands.setTextSelection(3);
    });
    await user.click(screen.getByRole("button", { name: "展开编辑器" }));
    expect(screen.getByRole("textbox", { name: "指令" })).toBe(dom);
    expect(editor.state.selection.from).toBe(3);
    await user.click(screen.getByRole("button", { name: "收起编辑器" }));
    expect(editor.state.selection.from).toBe(3);
    act(() => {
      editor.commands.insertContent("新增");
    });
    await user.click(screen.getByRole("button", { name: "源码" }));
    expect(screen.getByRole("textbox", { name: "指令" })).toHaveValue(
      "he新增llo 中文",
    );
    await user.click(screen.getByRole("button", { name: "保存" }));
    expect(save).toHaveBeenCalledWith("he新增llo 中文");
  });

  it("allows empty instructions and keeps unrelated optimistic/refresh values from overwriting a dirty draft", async () => {
    const save = vi.fn(async (value: string) => value);
    const dirty = vi.fn();
    const user = userEvent.setup();
    const props = {
      label: "指令",
      editable: true,
      onSave: save,
      onDirty: dirty,
    };
    const { rerender } = render(<InstructionsEditor {...props} saved="old" />);
    const input = screen.getByRole("textbox", { name: "指令" });
    fireEvent.change(input, { target: { value: "" } });
    rerender(<InstructionsEditor {...props} saved="a remote refresh" />);
    expect(input).toHaveValue("");
    await user.click(screen.getByRole("button", { name: "保存" }));
    expect(save).toHaveBeenCalledWith("");
  });

  it("renders readonly instructions and never enables save", async () => {
    render(
      <InstructionsEditor
        saved="readonly"
        label="指令"
        editable={false}
        onSave={vi.fn()}
        onDirty={() => {}}
      />,
    );
    expect(screen.getByRole("textbox", { name: "指令" })).toHaveAttribute(
      "readonly",
    );
    expect(screen.getByRole("button", { name: "保存" })).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "富文本" }));
    await waitFor(() =>
      expect(screen.getByRole("textbox", { name: "指令" })).toHaveAttribute(
        "contenteditable",
        "false",
      ),
    );
  });

  it("focus/selection and source sync preserve the actual rich document; genuine edits retain trailing-node behavior", () => {
    const editor = new Editor({
      extensions: [...docExtensions(""), InstructionDisplayTransactions],
      contentType: "markdown",
      content: "# 标题",
    });
    const before = editor.getJSON();
    const markdownBefore = editor.getMarkdown();
    const update = vi.fn();
    editor.on("update", update);
    editor.view.dispatch(editor.state.tr.setMeta("focus", true));
    editor.commands.setTextSelection(2);
    expect(editor.getJSON()).toEqual(before);
    expect(editor.getMarkdown()).toBe(markdownBefore);
    expect(update).not.toHaveBeenCalled();
    editor.commands.setContent("# 恢复的标题", {
      contentType: "markdown",
      emitUpdate: false,
    });
    expect(editor.getJSON().content).toHaveLength(1);
    expect(update).not.toHaveBeenCalled();
    editor.commands.insertContentAt(editor.state.doc.content.size - 1, "编辑");
    expect(editor.getJSON().content?.at(-1)?.type).toBe("paragraph");
    expect(editor.getMarkdown()).toContain("恢复的标题编辑");
    editor.destroy();
  });

  it("compares changed ranges, empty instructions and unchanged instructions without quadratic diff", () => {
    expect(instructionChanges("a\nb\nc", "a\nd\nc")).toEqual({
      removed: ["b"],
      added: ["d"],
    });
    expect(instructionChanges("", "new")).toEqual({
      removed: [""],
      added: ["new"],
    });
    expect(instructionChanges(markdown, markdown)).toEqual({
      removed: [],
      added: [],
    });
  });
});
