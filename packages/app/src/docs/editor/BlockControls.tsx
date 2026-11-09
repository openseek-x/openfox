import type { Editor } from "@tiptap/core";
import type { Node as PMNode } from "@tiptap/pm/model";
import {
  useEffect,
  useRef,
  useState,
  type DragEvent as ReactDragEvent,
  type RefObject,
} from "react";

import { useT } from "@openfox/i18n";
import { Icon, Popover, useToast, cn, type IconName } from "@openfox/ui";

// 块手柄与块菜单。悬停顶层块时左侧出现两个 20×20 按钮：
// 「转换为」打开块菜单，「拖拽移动」可以拖动整个块（HTML5 拖拽，落点用 ProseMirror 事务移动）。

/** 顶层块的位置：from 为块起点，to 为块终点。 */
interface Block {
  from: number;
  to: number;
  node: PMNode;
}

type BlockType =
  | "text"
  | "h1"
  | "h2"
  | "h3"
  | "h4"
  | "h5"
  | "h6"
  | "bulletList"
  | "orderedList"
  | "taskList"
  | "divider"
  | "quote"
  | "codeBlock";

const BLOCK_TYPES: { type: BlockType; icon?: IconName; short?: string }[] = [
  { type: "text", icon: "type" },
  { type: "h1", short: "H1" },
  { type: "h2", short: "H2" },
  { type: "h3", short: "H3" },
  { type: "h4", short: "H4" },
  { type: "h5", short: "H5" },
  { type: "h6", short: "H6" },
  { type: "bulletList", icon: "list" },
  { type: "orderedList", icon: "list-ordered" },
  { type: "taskList", icon: "square-check" },
  { type: "divider", icon: "minus" },
  { type: "quote", icon: "quote" },
  { type: "codeBlock", icon: "code" },
];

/** 鼠标位置所在的顶层块；落在块之间的空隙里时返回 null。 */
export function topBlockAt(
  editor: Editor,
  clientX: number,
  clientY: number,
): Block | null {
  const hit = editor.view.posAtCoords({ left: clientX, top: clientY });
  if (!hit) return null;
  const $pos = editor.state.doc.resolve(hit.pos);
  if ($pos.depth < 1) return null;
  const from = $pos.before(1);
  const node = $pos.node(1);
  return { from, to: from + node.nodeSize, node };
}

/** 块内第一个文本块的内容位置，用于把光标放进去后再执行块级命令。 */
function firstTextPos(editor: Editor, block: Block): number | null {
  let found: number | null = null;
  editor.state.doc.nodesBetween(block.from, block.to, (node, pos) => {
    if (found !== null) return false;
    if (node.isTextblock) {
      found = pos + 1;
      return false;
    }
    return true;
  });
  return found;
}

/** 把块转换为另一种类型。 */
function convertBlock(editor: Editor, block: Block, type: BlockType) {
  if (type === "divider") {
    editor
      .chain()
      .focus()
      .insertContentAt(
        { from: block.from, to: block.to },
        { type: "horizontalRule" },
      )
      .run();
    return;
  }
  const pos = firstTextPos(editor, block);
  if (pos === null) return;
  const chain = editor.chain().focus().setTextSelection(pos);
  if (type === "text") chain.setParagraph().run();
  else if (type.startsWith("h"))
    chain.setHeading({ level: Number(type.slice(1)) as 1 }).run();
  else if (type === "bulletList") chain.toggleBulletList().run();
  else if (type === "orderedList") chain.toggleOrderedList().run();
  else if (type === "taskList") chain.toggleTaskList().run();
  else if (type === "quote") chain.toggleBlockquote().run();
  else chain.toggleCodeBlock().run();
}

/** 把 from..to 的块移动到 insertPos（插入点在块外）。 */
export function moveBlock(editor: Editor, block: Block, insertPos: number) {
  if (insertPos >= block.from && insertPos <= block.to) return;
  const { tr } = editor.state;
  const next =
    insertPos > block.to
      ? tr.insert(insertPos, block.node).delete(block.from, block.to)
      : tr.delete(block.from, block.to).insert(insertPos, block.node);
  editor.view.dispatch(next.scrollIntoView());
}

interface Hover {
  block: Block;
  /** 相对于包裹容器的位置 */
  top: number;
  left: number;
}

interface DropLine {
  y: number;
  left: number;
  width: number;
  insertPos: number;
}

export function BlockControls({
  editor,
  wrapRef,
  docLink,
}: {
  editor: Editor;
  wrapRef: RefObject<HTMLDivElement | null>;
  /** 复制链接：由调用方给出当前文档的链接 */
  docLink: () => string;
}) {
  const t = useT();
  const toast = useToast();
  const [hover, setHover] = useState<Hover | null>(null);
  const [menuOpen, setMenuOpen] = useState(false);
  const [drop, setDrop] = useState<DropLine | null>(null);
  const [menuBlock, setMenuBlock] = useState<Block | null>(null);
  const convertButton = useRef<HTMLButtonElement | null>(null);
  const dragging = useRef<Block | null>(null);

  // 悬停：鼠标在编辑区内移动时更新顶层块；离开容器时隐藏
  useEffect(() => {
    const wrap = wrapRef.current;
    if (!wrap) return;
    const onMove = (event: MouseEvent) => {
      if (!editor.isEditable || dragging.current) return;
      const block = topBlockAt(editor, event.clientX, event.clientY);
      if (!block) return;
      const dom = editor.view.nodeDOM(block.from) as HTMLElement | null;
      if (!dom) return;
      const rect = dom.getBoundingClientRect();
      const box = wrap.getBoundingClientRect();
      setHover({ block, top: rect.top - box.top, left: rect.left - box.left });
    };
    const onLeave = () => {
      if (!menuOpen) setHover(null);
    };
    wrap.addEventListener("mousemove", onMove);
    wrap.addEventListener("mouseleave", onLeave);
    return () => {
      wrap.removeEventListener("mousemove", onMove);
      wrap.removeEventListener("mouseleave", onLeave);
    };
  }, [editor, wrapRef, menuOpen]);

  // 拖拽过程中在 window 捕获阶段处理落点，阻止 ProseMirror 把拖拽当作文本插入
  const startDrag = (block: Block, event: ReactDragEvent) => {
    dragging.current = block;
    event.dataTransfer.effectAllowed = "move";
    event.dataTransfer.setData("text/plain", "");
    const onOver = (next: DragEvent) => {
      const current = dragging.current;
      if (!current) return;
      const hit = topBlockAt(editor, next.clientX, next.clientY);
      if (!hit) return;
      const dom = editor.view.nodeDOM(hit.from) as HTMLElement | null;
      if (!dom) return;
      const rect = dom.getBoundingClientRect();
      const box = wrapRef.current?.getBoundingClientRect();
      const after = next.clientY > rect.top + rect.height / 2;
      const insertPos = after ? hit.to : hit.from;
      if (insertPos >= current.from && insertPos <= current.to) {
        setDrop(null);
        return;
      }
      next.preventDefault();
      next.stopPropagation();
      setDrop({
        y: after ? rect.bottom : rect.top,
        left: rect.left - (box?.left ?? 0),
        width: rect.width,
        insertPos,
      });
    };
    const onDropEvent = (next: DragEvent) => {
      const current = dragging.current;
      if (!current) return;
      next.preventDefault();
      next.stopPropagation();
      const hit = topBlockAt(editor, next.clientX, next.clientY);
      if (hit) {
        const dom = editor.view.nodeDOM(hit.from) as HTMLElement | null;
        const after = dom
          ? next.clientY >
            dom.getBoundingClientRect().top +
              dom.getBoundingClientRect().height / 2
          : false;
        moveBlock(editor, current, after ? hit.to : hit.from);
      }
      finish();
    };
    const finish = () => {
      dragging.current = null;
      setDrop(null);
      setHover(null);
      window.removeEventListener("dragover", onOver, true);
      window.removeEventListener("drop", onDropEvent, true);
      window.removeEventListener("dragend", finish, true);
    };
    window.addEventListener("dragover", onOver, true);
    window.addEventListener("drop", onDropEvent, true);
    window.addEventListener("dragend", finish, true);
  };

  const copyText = async (block: Block) => {
    try {
      await navigator.clipboard.writeText(block.node.textContent);
      toast(t("docs.editor.block.copied"));
    } catch {
      toast(t("docs.editor.block.copyFailed"), { tone: "error" });
    }
  };

  const copyLink = async () => {
    try {
      await navigator.clipboard.writeText(docLink());
      toast(t("docs.editor.block.linkCopied"));
    } catch {
      toast(t("docs.editor.block.copyFailed"), { tone: "error" });
    }
  };

  const openMenu = () => {
    if (!hover) return;
    setMenuBlock(hover.block);
    setMenuOpen(true);
  };

  const closeMenu = () => {
    setMenuOpen(false);
    setMenuBlock(null);
  };

  const run = (action: () => void) => {
    action();
    closeMenu();
  };

  return (
    <>
      {hover && editor.isEditable && (
        <div
          role="group"
          aria-label={t("docs.editor.block.convert")}
          style={{ top: hover.top + 2, left: hover.left - 52 }}
          className="absolute z-20 flex flex-col gap-0.5 rounded-lg border border-line-default bg-surface-card p-0.5 shadow-sm"
        >
          <button
            ref={convertButton}
            type="button"
            aria-label={t("docs.editor.block.convert")}
            aria-haspopup="menu"
            aria-expanded={menuOpen}
            onMouseDown={(event) => event.preventDefault()}
            onClick={() => (menuOpen ? closeMenu() : openMenu())}
            className="flex size-5 items-center justify-center rounded text-fg-secondary hover:bg-surface-hover"
          >
            <Icon name="openfox-more-square" size={14} />
          </button>
          <button
            type="button"
            draggable
            aria-label={t("docs.editor.block.drag")}
            onDragStart={(event) => startDrag(hover.block, event)}
            onDragEnd={() => {
              dragging.current = null;
              setDrop(null);
            }}
            className="flex size-5 cursor-grab items-center justify-center rounded text-fg-secondary hover:bg-surface-hover active:cursor-grabbing"
          >
            <Icon name="grip-vertical" size={14} />
          </button>
        </div>
      )}
      {drop && (
        <div
          aria-hidden
          style={{ top: drop.y - 1, left: drop.left, width: drop.width }}
          className="pointer-events-none absolute z-20 h-0.5 rounded bg-primary"
        />
      )}
      <Popover
        open={menuOpen && menuBlock !== null}
        onOpenChange={(open) => (open ? openMenu() : closeMenu())}
        anchorRef={convertButton}
        placement="right-start"
        role="menu"
        aria-label={t("docs.editor.block.convert")}
        className="flex w-52 flex-col gap-1 p-1.5"
      >
        {menuBlock && (
          <div className="flex flex-col gap-1">
            <div className="grid grid-cols-5 gap-0.5">
              {BLOCK_TYPES.map((item) => (
                <button
                  key={item.type}
                  type="button"
                  role="menuitem"
                  aria-label={blockTypeLabel(t, item.type)}
                  title={blockTypeLabel(t, item.type)}
                  onClick={() =>
                    run(() => convertBlock(editor, menuBlock, item.type))
                  }
                  className="flex h-8 items-center justify-center rounded-md text-fg-secondary hover:bg-surface-hover"
                >
                  {item.icon ? (
                    <Icon name={item.icon} size={14} />
                  ) : (
                    <span className="text-caption font-medium">
                      {item.short}
                    </span>
                  )}
                </button>
              ))}
            </div>
            <div className="my-1 h-px bg-line-default" />
            <MenuRow
              icon="copy"
              label={t("docs.editor.block.copy")}
              onSelect={() => run(() => void copyText(menuBlock))}
            />
            <MenuRow
              icon="copy-plus"
              label={t("docs.editor.block.duplicate")}
              onSelect={() =>
                run(() =>
                  editor.view.dispatch(
                    editor.state.tr
                      .insert(menuBlock.to, menuBlock.node)
                      .scrollIntoView(),
                  ),
                )
              }
            />
            <MenuRow
              icon="scissors"
              label={t("docs.editor.block.cut")}
              onSelect={() =>
                run(() => {
                  void copyText(menuBlock);
                  editor.view.dispatch(
                    editor.state.tr.delete(menuBlock.from, menuBlock.to),
                  );
                })
              }
            />
            <MenuRow
              icon="link"
              label={t("docs.row.copyLink")}
              onSelect={() => run(() => void copyLink())}
            />
            <div className="my-1 h-px bg-line-default" />
            <MenuRow
              icon="trash-2"
              danger
              label={t("docs.row.delete")}
              onSelect={() =>
                run(() =>
                  editor.view.dispatch(
                    editor.state.tr.delete(menuBlock.from, menuBlock.to),
                  ),
                )
              }
            />
          </div>
        )}
      </Popover>
    </>
  );
}

function blockTypeLabel(t: ReturnType<typeof useT>, type: BlockType): string {
  if (type === "text") return t("docs.editor.block.types.text");
  if (type.startsWith("h"))
    return t("docs.editor.block.types.heading", { level: type.slice(1) });
  const map: Record<string, Parameters<typeof t>[0]> = {
    bulletList: "docs.editor.block.types.bulletList",
    orderedList: "docs.editor.block.types.orderedList",
    taskList: "docs.editor.block.types.taskList",
    divider: "docs.editor.block.types.divider",
    quote: "docs.editor.block.types.quote",
    codeBlock: "docs.editor.block.types.codeBlock",
  };
  return t(map[type]!);
}

function MenuRow({
  icon,
  label,
  danger,
  onSelect,
}: {
  icon: IconName;
  label: string;
  danger?: boolean;
  onSelect: () => void;
}) {
  return (
    <button
      type="button"
      role="menuitem"
      onClick={onSelect}
      className={cn(
        "flex h-8 w-full items-center gap-2 rounded-lg px-2 text-left text-body hover:bg-surface-hover",
        danger ? "text-danger" : "text-fg-default",
      )}
    >
      <Icon
        name={icon}
        size={14}
        className={danger ? "" : "text-fg-secondary"}
      />
      {label}
    </button>
  );
}
