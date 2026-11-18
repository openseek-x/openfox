import { EditorContent, useEditor } from "@tiptap/react";
import { useEffect, useRef } from "react";

import { InstructionDisplayTransactions } from "./instructionExtensions";
import { docExtensions } from "../../docs/editor/extensions";
import { BubbleToolbar } from "../../docs/editor/BubbleToolbar";
import "../../docs/editor/editor.css";

/** Reuses the document Markdown schema, without its document upload or autosave paths. */
export default function RichInstructions({
  value,
  onChange,
  editable,
  expanded,
  label,
}: {
  value: string;
  onChange: (value: string) => void;
  editable: boolean;
  expanded: boolean;
  label: string;
}) {
  const lastValue = useRef(value);
  const change = useRef(onChange);
  change.current = onChange;
  const editor = useEditor(
    {
      extensions: [...docExtensions(label), InstructionDisplayTransactions],
      content: value,
      contentType: "markdown",
      editable,
      immediatelyRender: false,
      editorProps: {
        attributes: {
          class: "doc-body min-h-[240px] outline-none",
          role: "textbox",
          "aria-label": label,
          "aria-multiline": "true",
        },
      },
      onUpdate: ({ editor: current }) => {
        lastValue.current = current.getMarkdown().trimEnd();
        change.current(lastValue.current);
      },
    },
    [],
  );
  useEffect(() => {
    editor?.setEditable(editable, false);
  }, [editor, editable]);
  useEffect(() => {
    if (!editor || value === lastValue.current) return;
    lastValue.current = value;
    editor.commands.setContent(value, {
      contentType: "markdown",
      emitUpdate: false,
    });
  }, [editor, value]);
  const lastExpanded = useRef(expanded);
  useEffect(() => {
    if (!editor || lastExpanded.current === expanded) return;
    lastExpanded.current = expanded;
    // Moving the stable portal host can clear the browser selection; TipTap retains its model selection.
    editor.commands.focus(undefined, { scrollIntoView: false });
  }, [editor, expanded]);
  return (
    <div className="min-w-0 overflow-x-auto rounded border border-line-default p-3">
      <EditorContent editor={editor} />
      {editor && editable && <BubbleToolbar editor={editor} />}
    </div>
  );
}
