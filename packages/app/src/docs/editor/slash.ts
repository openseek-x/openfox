import { Extension, type Editor, type Range } from "@tiptap/core";
import Suggestion, {
  type SuggestionKeyDownProps,
  type SuggestionProps,
} from "@tiptap/suggestion";

import type { Translate } from "@openfox/i18n";
import type { IconName } from "@openfox/ui";

// 斜杠菜单：输入「/」弹出，菜单项按固定顺序排列，可用中英文关键词过滤。

export interface SlashItem {
  id: string;
  label: string;
  /** 额外的过滤关键词（英文缩写与中文），不显示 */
  keywords: string[];
  /** 图标；没有对应图标时用 short 文字（标题统一用 H1–H6 文字） */
  icon?: IconName;
  short?: string;
  run: (editor: Editor) => void;
}

export interface SlashActions {
  /** 上传图片：打开文件选择（仅图片） */
  pickImage: () => void;
  /** 上传附件：打开文件选择（任意文件）；只有可上传时才列出 */
  pickFile: () => void;
  /** 是否可以上传（只读文档没有上传项） */
  uploadable: boolean;
}

/** 按固定顺序生成菜单项。 */
export function buildSlashItems(
  t: Translate,
  actions: SlashActions,
): SlashItem[] {
  const headings: SlashItem[] = [1, 2, 3, 4, 5, 6].map((level) => ({
    id: `h${level}`,
    label: t("docs.editor.slash.heading", { level }),
    keywords: [`h${level}`, `heading${level}`, `标题${level}`],
    short: `H${level}`,
    run: (editor) =>
      editor
        .chain()
        .focus()
        .setHeading({ level: level as 1 })
        .run(),
  }));
  const items: SlashItem[] = [
    {
      id: "text",
      label: t("docs.editor.slash.text"),
      keywords: ["text", "paragraph", "p", "文本", "正文"],
      icon: "type",
      run: (editor) => editor.chain().focus().setParagraph().run(),
    },
    ...headings,
    {
      id: "bullet",
      label: t("docs.editor.slash.bulletList"),
      keywords: ["bullet", "ul", "list", "无序"],
      icon: "list",
      run: (editor) => editor.chain().focus().toggleBulletList().run(),
    },
    {
      id: "ordered",
      label: t("docs.editor.slash.orderedList"),
      keywords: ["ordered", "ol", "number", "有序"],
      icon: "list-ordered",
      run: (editor) => editor.chain().focus().toggleOrderedList().run(),
    },
    {
      id: "todo",
      label: t("docs.editor.slash.taskList"),
      keywords: ["todo", "task", "check", "待办"],
      icon: "square-check",
      run: (editor) => editor.chain().focus().toggleTaskList().run(),
    },
  ];
  if (actions.uploadable)
    items.push(
      {
        id: "image",
        label: t("docs.editor.slash.image"),
        keywords: ["image", "img", "picture", "图片"],
        icon: "image",
        run: () => actions.pickImage(),
      },
      {
        id: "attachment",
        label: t("docs.editor.slash.attachment"),
        keywords: ["file", "attachment", "upload", "附件", "上传"],
        icon: "paperclip",
        run: () => actions.pickFile(),
      },
    );
  items.push(
    {
      id: "code",
      label: t("docs.editor.slash.codeBlock"),
      keywords: ["code", "codeblock", "代码"],
      icon: "code",
      run: (editor) => editor.chain().focus().toggleCodeBlock().run(),
    },
    {
      id: "quote",
      label: t("docs.editor.slash.quote"),
      keywords: ["quote", "blockquote", "引用"],
      icon: "quote",
      run: (editor) => editor.chain().focus().toggleBlockquote().run(),
    },
    {
      id: "table",
      label: t("docs.editor.slash.table"),
      keywords: ["table", "grid", "表格"],
      icon: "file-spreadsheet",
      run: (editor) =>
        editor
          .chain()
          .focus()
          .insertTable({ rows: 3, cols: 3, withHeaderRow: true })
          .run(),
    },
    {
      id: "divider",
      label: t("docs.editor.slash.divider"),
      keywords: ["hr", "divider", "rule", "分割线"],
      icon: "minus",
      run: (editor) => editor.chain().focus().setHorizontalRule().run(),
    },
  );
  return items;
}

/** 关键词过滤：标题或任一关键词包含查询（不区分大小写）。空查询返回全部。 */
export function filterSlashItems(
  items: readonly SlashItem[],
  query: string,
): SlashItem[] {
  const q = query.trim().toLowerCase();
  if (!q) return [...items];
  return items.filter((item) =>
    [item.label, ...item.keywords].some((word) =>
      word.toLowerCase().includes(q),
    ),
  );
}

/** 交给 React 渲染的弹层状态；null 表示关闭。 */
export interface SlashState {
  items: SlashItem[];
  index: number;
  rect: DOMRect;
  select: (item: SlashItem) => void;
  /** 鼠标悬停时同步高亮项 */
  highlight: (index: number) => void;
}

interface SlashOptions {
  items: (query: string) => SlashItem[];
  onState: (state: SlashState | null) => void;
}

/** 斜杠触发扩展：@tiptap/suggestion 负责匹配「/」与查询文本，弹层由 React 组件渲染。 */
export const SlashCommand = Extension.create<SlashOptions>({
  name: "slashCommand",
  addOptions() {
    return { items: () => [], onState: () => undefined };
  },
  addProseMirrorPlugins() {
    const { items, onState } = this.options;
    return [
      Suggestion<SlashItem>({
        editor: this.editor,
        char: "/",
        allowSpaces: false,
        startOfLine: false,
        items: ({ query }) => items(query),
        command: ({
          editor,
          range,
          props,
        }: {
          editor: Editor;
          range: Range;
          props: SlashItem;
        }) => {
          editor.chain().focus().deleteRange(range).run();
          props.run(editor);
        },
        render: () => {
          let list: SlashItem[] = [];
          let index = 0;
          let command: ((item: SlashItem) => void) | null = null;
          let clientRect: (() => DOMRect | null) | null | undefined = null;
          const publish = () => {
            const rect = clientRect?.();
            if (!rect || !command) return onState(null);
            const run = command;
            onState({
              items: list,
              index,
              rect,
              select: (item) => run(item),
              highlight: (next) => {
                index = next;
                publish();
              },
            });
          };
          const update = (props: SuggestionProps<SlashItem, SlashItem>) => {
            list = props.items;
            command = props.command;
            clientRect = props.clientRect;
            if (index >= list.length) index = 0;
            publish();
          };
          return {
            onStart: update,
            onUpdate: update,
            onKeyDown: ({ event }: SuggestionKeyDownProps) => {
              if (event.key === "Escape") {
                onState(null);
                return true;
              }
              if (list.length === 0) return false;
              if (event.key === "ArrowDown" || event.key === "ArrowUp") {
                const step = event.key === "ArrowDown" ? 1 : -1;
                index = (index + step + list.length) % list.length;
                publish();
                return true;
              }
              if (event.key === "Enter") {
                const current = list[index];
                if (current && command) command(current);
                return true;
              }
              return false;
            },
            onExit: () => {
              list = [];
              command = null;
              onState(null);
            },
          };
        },
      }),
    ];
  },
});
