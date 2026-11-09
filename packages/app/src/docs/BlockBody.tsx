import type { Editor } from "@tiptap/core";
import { EditorContent, useEditor } from "@tiptap/react";
import { useEffect, useRef, useState } from "react";

import { isApiError } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { useToast } from "@openfox/ui";

import { useApi } from "../context";
import { BlockControls } from "./editor/BlockControls";
import { BubbleToolbar } from "./editor/BubbleToolbar";
import { docExtensions } from "./editor/extensions";
import { SlashMenu } from "./editor/SlashMenu";
import {
  buildSlashItems,
  filterSlashItems,
  SlashCommand,
  type SlashItem,
  type SlashState,
} from "./editor/slash";
import "../code/highlight.css";
import "./editor/editor.css";
import { docLink } from "./model";

// 文档正文。由 DocEditor 懒加载，TipTap 不进入主包。
// 正文以 Markdown 存取：打开时按 Markdown 解析，改动后由上层防抖保存 editor.getMarkdown()。

/** 附件上传上限（与服务端一致，超过时不发请求） */
export const ATTACHMENT_MAX_BYTES = 20 * 1024 * 1024;

export default function BlockBody({
  orgId,
  spaceId,
  nodeId,
  initialMarkdown,
  editable,
  onChange,
}: {
  orgId: string;
  spaceId: string;
  nodeId: string;
  initialMarkdown: string;
  editable: boolean;
  onChange: (markdown: string) => void;
}) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const wrapRef = useRef<HTMLDivElement | null>(null);
  const imageInput = useRef<HTMLInputElement | null>(null);
  const fileInput = useRef<HTMLInputElement | null>(null);
  const [slash, setSlash] = useState<SlashState | null>(null);
  // 斜杠菜单的数据源与上传动作在 useEditor 创建后仍需读到最新值，用 ref 中转
  const itemsRef = useRef<(query: string) => SlashItem[]>(() => []);
  const onChangeRef = useRef(onChange);
  useEffect(() => {
    onChangeRef.current = onChange;
  });

  const uploadAttachment = async (
    editor: Editor,
    file: File,
    asImage: boolean,
  ) => {
    if (file.size > ATTACHMENT_MAX_BYTES) {
      toast(t("docs.editor.attachmentTooLarge"), { tone: "error" });
      return;
    }
    try {
      const attachment = await api.docNodes.uploadAttachment(
        orgId,
        spaceId,
        nodeId,
        file,
      );
      const isImage =
        asImage || (attachment.content_type ?? "").startsWith("image/");
      if (isImage) {
        editor
          .chain()
          .focus()
          .setImage({ src: attachment.url, alt: attachment.name })
          .run();
      } else {
        // 非图片附件插入为链接文字
        editor
          .chain()
          .focus()
          .insertContent({
            type: "text",
            text: attachment.name,
            marks: [{ type: "link", attrs: { href: attachment.url } }],
          })
          .run();
      }
    } catch (error) {
      toast(
        t(
          isApiError(error) && error.status === 413
            ? "docs.editor.attachmentTooLarge"
            : "docs.editor.attachmentFailed",
        ),
        { tone: "error" },
      );
    }
  };

  const editor = useEditor(
    {
      extensions: [
        ...docExtensions(t("docs.editor.bodyPlaceholder")),
        SlashCommand.configure({
          items: (query: string) => itemsRef.current(query),
          onState: setSlash,
        }),
      ],
      contentType: "markdown",
      content: initialMarkdown,
      editable,
      // 面板里的首屏渲染不需要同步完成，避免 React 严格模式下的重复渲染警告
      immediatelyRender: false,
      // 末尾的空段落（TrailingNode 保证末尾总有一个段落）导出为多余的空行，去掉
      onUpdate: ({ editor: current }) =>
        onChangeRef.current(current.getMarkdown().trimEnd()),
      editorProps: {
        attributes: { class: "doc-body min-h-[240px] outline-none" },
      },
    },
    [],
  );

  useEffect(() => {
    editor?.setEditable(editable);
  }, [editor, editable]);

  // 斜杠菜单项随只读状态与语言变化（上传项仅可编辑时出现）
  useEffect(() => {
    itemsRef.current = (query) =>
      filterSlashItems(
        buildSlashItems(t, {
          uploadable: editable,
          pickImage: () => imageInput.current?.click(),
          pickFile: () => fileInput.current?.click(),
        }),
        query,
      );
  }, [t, editable]);

  return (
    <div ref={wrapRef} className="relative">
      <EditorContent editor={editor} />
      {editor && <BubbleToolbar editor={editor} />}
      {editor && (
        <BlockControls
          editor={editor}
          wrapRef={wrapRef}
          docLink={() =>
            docLink(window.location.origin, orgId, spaceId, nodeId)
          }
        />
      )}
      {slash && <SlashMenu state={slash} />}
      <input
        ref={imageInput}
        type="file"
        accept="image/*"
        aria-label={t("docs.editor.insertImage")}
        className="sr-only"
        tabIndex={-1}
        onChange={(event) => {
          const file = event.target.files?.[0];
          event.target.value = "";
          if (file && editor) void uploadAttachment(editor, file, true);
        }}
      />
      <input
        ref={fileInput}
        type="file"
        aria-label={t("docs.editor.uploadAttachment")}
        className="sr-only"
        tabIndex={-1}
        onChange={(event) => {
          const file = event.target.files?.[0];
          event.target.value = "";
          if (file && editor) void uploadAttachment(editor, file, false);
        }}
      />
    </div>
  );
}
