import { BubbleMenu } from "@tiptap/react/menus";
import { useEditorState, type Editor } from "@tiptap/react";
import { useEffect, useRef, useState, type MouseEvent } from "react";

import { useT } from "@openfox/i18n";
import {
  Icon,
  IconButton,
  Menu,
  MenuItem,
  MenuSeparator,
  type IconName,
} from "@openfox/ui";

// 选区浮动工具栏：选中文字后在选区上方 8px 出现。
// 按钮 28×28、图标 14；激活态底色 fg 9%；链接切换为 288 宽的链接输入条；光标在表格内时追加「表格」下拉。

type HeadingLevel = 1 | 2 | 3 | 4;

/** 工具栏的激活态与当前块类型，由 useEditorState 订阅，避免按钮状态滞后。 */
function toolState(editor: Editor) {
  return {
    bold: editor.isActive("bold"),
    italic: editor.isActive("italic"),
    strike: editor.isActive("strike"),
    underline: editor.isActive("underline"),
    link: editor.isActive("link"),
    code: editor.isActive("code"),
    quote: editor.isActive("blockquote"),
    codeBlock: editor.isActive("codeBlock"),
    bullet: editor.isActive("bulletList"),
    ordered: editor.isActive("orderedList"),
    table: editor.isActive("table"),
    heading: ([1, 2, 3, 4] as HeadingLevel[]).find((level) =>
      editor.isActive("heading", { level }),
    ),
  };
}

export function BubbleToolbar({ editor }: { editor: Editor }) {
  const t = useT();
  const [linking, setLinking] = useState(false);
  const state = useEditorState({
    editor,
    selector: ({ editor }) => toolState(editor),
  });
  // 工具栏是浮层，不能抢走正文的选区：按下时阻止默认行为
  const keepSelection = (event: MouseEvent) => event.preventDefault();

  return (
    <BubbleMenu
      editor={editor}
      options={{ placement: "top", offset: 8 }}
      shouldShow={({ editor, state }) =>
        editor.isEditable &&
        !state.selection.empty &&
        !editor.isActive("codeBlock")
      }
      onMouseDown={keepSelection}
      className="z-40"
    >
      {linking ? (
        <LinkBar editor={editor} onDone={() => setLinking(false)} />
      ) : (
        <div className="flex items-center gap-0.5 rounded-[10px] border border-line-default bg-surface-card p-1 shadow-lg">
          <BlockTypeMenu
            label={
              state.heading
                ? t("docs.editor.bubble.heading", { level: state.heading })
                : t("docs.editor.bubble.paragraph")
            }
            current={state.heading}
            onParagraph={() => editor.chain().focus().setParagraph().run()}
            onHeading={(level) =>
              editor.chain().focus().setHeading({ level }).run()
            }
          />
          <Divider />
          <Tool
            icon="bold"
            label={t("docs.editor.bubble.bold")}
            active={state.bold}
            onClick={() => editor.chain().focus().toggleBold().run()}
          />
          <Tool
            icon="italic"
            label={t("docs.editor.bubble.italic")}
            active={state.italic}
            onClick={() => editor.chain().focus().toggleItalic().run()}
          />
          <Tool
            icon="strikethrough"
            label={t("docs.editor.bubble.strike")}
            active={state.strike}
            onClick={() => editor.chain().focus().toggleStrike().run()}
          />
          <Tool
            icon="underline"
            label={t("docs.editor.bubble.underline")}
            active={state.underline}
            onClick={() => editor.chain().focus().toggleUnderline().run()}
          />
          <Tool
            icon="link"
            label={t("docs.editor.bubble.link")}
            active={state.link}
            onClick={() => setLinking(true)}
          />
          <Divider />
          <Tool
            icon="quote"
            label={t("docs.editor.bubble.quote")}
            active={state.quote}
            onClick={() => editor.chain().focus().toggleBlockquote().run()}
          />
          <Tool
            icon="type"
            label={t("docs.editor.bubble.clearFormat")}
            onClick={() =>
              editor.chain().focus().unsetAllMarks().clearNodes().run()
            }
          />
          <Tool
            icon="code"
            label={t("docs.editor.bubble.inlineCode")}
            active={state.code}
            onClick={() => editor.chain().focus().toggleCode().run()}
          />
          <Divider />
          <Tool
            icon="code-xml"
            label={t("docs.editor.bubble.codeBlock")}
            active={state.codeBlock}
            onClick={() => editor.chain().focus().toggleCodeBlock().run()}
          />
          <Tool
            icon="list"
            label={t("docs.editor.bubble.bulletList")}
            active={state.bullet}
            onClick={() => editor.chain().focus().toggleBulletList().run()}
          />
          <Tool
            icon="list-ordered"
            label={t("docs.editor.bubble.orderedList")}
            active={state.ordered}
            onClick={() => editor.chain().focus().toggleOrderedList().run()}
          />
          {state.table && (
            <>
              <Divider />
              <TableMenu editor={editor} />
            </>
          )}
        </div>
      )}
    </BubbleMenu>
  );
}

function Divider() {
  return <span aria-hidden className="mx-0.5 h-4 w-px bg-line-default" />;
}

function Tool({
  icon,
  label,
  active,
  onClick,
}: {
  icon: IconName;
  label: string;
  active?: boolean;
  onClick: () => void;
}) {
  return (
    <IconButton
      label={label}
      icon={<Icon name={icon} size={14} />}
      size="28"
      variant="ghost"
      active={active}
      onClick={onClick}
    />
  );
}

/** 块类型下拉：正文与标题 1–4。 */
function BlockTypeMenu({
  label,
  current,
  onParagraph,
  onHeading,
}: {
  label: string;
  current: HeadingLevel | undefined;
  onParagraph: () => void;
  onHeading: (level: HeadingLevel) => void;
}) {
  const t = useT();
  return (
    <Menu
      placement="bottom-start"
      label={t("docs.editor.bubble.blockType")}
      trigger={(props) => (
        <button
          ref={props.ref}
          type="button"
          aria-label={t("docs.editor.bubble.blockType")}
          aria-expanded={props.open}
          onClick={props.toggle}
          className="flex h-7 items-center gap-1 rounded-lg px-2 text-body-sm hover:bg-surface-hover"
        >
          {label}
          <Icon name="chevron-down" size={12} className="text-fg-muted" />
        </button>
      )}
    >
      <MenuItem selected={current === undefined} onSelect={onParagraph}>
        {t("docs.editor.bubble.paragraph")}
      </MenuItem>
      {([1, 2, 3, 4] as HeadingLevel[]).map((level) => (
        <MenuItem
          key={level}
          selected={current === level}
          onSelect={() => onHeading(level)}
        >
          {t("docs.editor.bubble.heading", { level })}
        </MenuItem>
      ))}
    </Menu>
  );
}

/** 链接输入条（288 宽）：回车确认，输入空串则取消链接，Esc 返回工具栏。 */
function LinkBar({ editor, onDone }: { editor: Editor; onDone: () => void }) {
  const t = useT();
  const [href, setHref] = useState(
    () => editor.getAttributes("link").href ?? "",
  );
  const input = useRef<HTMLInputElement | null>(null);
  useEffect(() => input.current?.focus(), []);
  const apply = () => {
    const value = href.trim();
    const chain = editor.chain().focus().extendMarkRange("link");
    if (value) chain.setLink({ href: value }).run();
    else chain.unsetLink().run();
    onDone();
  };
  return (
    <div className="flex w-72 items-center gap-1 rounded-[10px] border border-line-default bg-surface-card p-1 shadow-lg">
      <input
        ref={input}
        aria-label={t("docs.editor.bubble.link")}
        value={href}
        placeholder={t("docs.editor.bubble.linkPlaceholder")}
        onChange={(event) => setHref(event.target.value)}
        onKeyDown={(event) => {
          if (event.key === "Enter") {
            event.preventDefault();
            apply();
          } else if (event.key === "Escape") {
            event.preventDefault();
            onDone();
          }
        }}
        className="h-7 min-w-0 flex-1 rounded-md bg-transparent px-2 text-body-sm outline-none focus:bg-surface-hover"
      />
    </div>
  );
}

/** 表格下拉：只在光标位于表格内时出现。 */
function TableMenu({ editor }: { editor: Editor }) {
  const t = useT();
  const items: { label: string; run: () => void; danger?: boolean }[] = [
    {
      label: t("docs.editor.bubble.tableAddRowAfter"),
      run: () => editor.chain().focus().addRowAfter().run(),
    },
    {
      label: t("docs.editor.bubble.tableAddColumnAfter"),
      run: () => editor.chain().focus().addColumnAfter().run(),
    },
    {
      label: t("docs.editor.bubble.tableDeleteRow"),
      run: () => editor.chain().focus().deleteRow().run(),
    },
    {
      label: t("docs.editor.bubble.tableDeleteColumn"),
      run: () => editor.chain().focus().deleteColumn().run(),
    },
    {
      label: t("docs.editor.bubble.tableDelete"),
      run: () => editor.chain().focus().deleteTable().run(),
      danger: true,
    },
  ];
  return (
    <Menu
      placement="bottom-start"
      label={t("docs.editor.bubble.table")}
      trigger={(props) => (
        <button
          ref={props.ref}
          type="button"
          aria-expanded={props.open}
          onClick={props.toggle}
          className="flex h-7 items-center gap-1 rounded-lg px-2 text-body-sm hover:bg-surface-hover"
        >
          {t("docs.editor.bubble.table")}
          <Icon name="chevron-down" size={12} className="text-fg-muted" />
        </button>
      )}
    >
      {items.slice(0, 2).map((item) => (
        <MenuItem key={item.label} onSelect={item.run}>
          {item.label}
        </MenuItem>
      ))}
      <MenuSeparator />
      {items.slice(2).map((item) => (
        <MenuItem key={item.label} danger={item.danger} onSelect={item.run}>
          {item.label}
        </MenuItem>
      ))}
    </Menu>
  );
}
