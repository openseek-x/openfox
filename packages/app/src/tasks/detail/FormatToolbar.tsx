import type { RefObject } from "react";
import { useT, type MessageKey } from "@openfox/i18n";
import { Icon, IconButton, type IconName } from "@openfox/ui";

// 评论输入框的格式工具条：在光标/选区上套 Markdown 标记，不改变输入框的纯文本性质。

export type FormatKind =
  "bold" | "italic" | "code" | "link" | "bullet" | "quote" | "codeBlock";

export interface FormatResult {
  value: string;
  start: number;
  end: number;
}

const WRAP: Record<"bold" | "italic" | "code", string> = {
  bold: "**",
  italic: "*",
  code: "`",
};

/** 纯函数：返回套用格式后的全文与新的选区；超过 maxLength 时返回 null（不改动） */
export function applyFormat(
  value: string,
  start: number,
  end: number,
  kind: FormatKind,
  maxLength = 65536,
): FormatResult | null {
  const selected = value.slice(start, end);
  let insert: string;
  let selStart: number;
  let selEnd: number;
  if (kind === "bold" || kind === "italic" || kind === "code") {
    const mark = WRAP[kind];
    insert = `${mark}${selected}${mark}`;
    selStart = start + mark.length;
    selEnd = selStart + selected.length;
  } else if (kind === "link") {
    // 选中文字当链接文字，地址占位处选中，方便直接粘贴
    const text = selected || "link";
    insert = `[${text}](url)`;
    selStart = start + text.length + 3;
    selEnd = selStart + 3;
  } else if (kind === "codeBlock") {
    const lead = start > 0 && value[start - 1] !== "\n" ? "\n" : "";
    insert = `${lead}\`\`\`\n${selected}\n\`\`\``;
    selStart = start + lead.length + 4;
    selEnd = selStart + selected.length;
  } else {
    // 列表 / 引用：对选区涉及的每一行加行首标记
    const lineStart = value.lastIndexOf("\n", start - 1) + 1;
    const block = value.slice(lineStart, end);
    const prefix = kind === "bullet" ? "- " : "> ";
    const next = block
      .split("\n")
      .map((line) => prefix + line)
      .join("\n");
    const result = value.slice(0, lineStart) + next + value.slice(end);
    if (result.length > maxLength) return null;
    return {
      value: result,
      start: lineStart,
      end: lineStart + next.length,
    };
  }
  const result = value.slice(0, start) + insert + value.slice(end);
  if (result.length > maxLength) return null;
  return { value: result, start: selStart, end: selEnd };
}

const ITEMS: Array<{ kind: FormatKind; icon: IconName; label: MessageKey }> = [
  { kind: "bold", icon: "bold", label: "docs.editor.bubble.bold" },
  { kind: "italic", icon: "italic", label: "docs.editor.bubble.italic" },
  { kind: "code", icon: "code", label: "docs.editor.bubble.inlineCode" },
  { kind: "link", icon: "link", label: "docs.editor.bubble.link" },
  { kind: "bullet", icon: "list", label: "docs.editor.bubble.bulletList" },
  { kind: "quote", icon: "quote", label: "docs.editor.bubble.quote" },
  {
    kind: "codeBlock",
    icon: "square-code",
    label: "docs.editor.bubble.codeBlock",
  },
];

export function FormatToolbar({
  area,
  value,
  onValue,
  disabled,
}: {
  area: RefObject<HTMLTextAreaElement | null>;
  value: string;
  onValue: (next: string) => void;
  disabled?: boolean;
}) {
  const t = useT();
  return (
    <div
      role="toolbar"
      aria-label={t("docs.editor.bubble.blockType")}
      className="flex items-center gap-0.5"
    >
      {ITEMS.map((item) => (
        <IconButton
          key={item.kind}
          label={t(item.label)}
          icon={<Icon name={item.icon} />}
          size="28"
          disabled={disabled}
          onMouseDown={(event) => event.preventDefault()}
          onClick={() => {
            const el = area.current;
            const start = el?.selectionStart ?? value.length;
            const end = el?.selectionEnd ?? start;
            const result = applyFormat(value, start, end, item.kind);
            if (!result) return;
            onValue(result.value);
            requestAnimationFrame(() => {
              el?.focus();
              el?.setSelectionRange(result.start, result.end);
            });
          }}
        />
      ))}
    </div>
  );
}
