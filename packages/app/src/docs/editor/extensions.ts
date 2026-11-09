import { ListKit } from "@tiptap/extension-list";
import { Image } from "@tiptap/extension-image";
import { TableKit } from "@tiptap/extension-table";
import { Placeholder } from "@tiptap/extensions";
import { Markdown } from "@tiptap/markdown";
import { StarterKit } from "@tiptap/starter-kit";

import { CodeHighlight } from "./codeHighlight";

// 文档正文的 TipTap 扩展。正文以 Markdown 存取，编辑器与无头测试共用这一份配置。

/** 正文扩展：标题、列表（含待办）、代码块（语法高亮）、表格、图片与 Markdown 读写。 */
export function docExtensions(placeholder: string) {
  return [
    StarterKit.configure({
      // 列表与待办由 ListKit 提供，避免两份同名扩展冲突
      bulletList: false,
      orderedList: false,
      listItem: false,
      listKeymap: false,
      link: { openOnClick: false, autolink: true },
    }),
    ListKit.configure({ taskItem: { nested: true } }),
    TableKit.configure({ table: { resizable: false } }),
    Image.configure({ inline: false, allowBase64: false }),
    Placeholder.configure({ placeholder }),
    Markdown,
    CodeHighlight,
  ];
}
