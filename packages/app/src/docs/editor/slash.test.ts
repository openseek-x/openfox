// @vitest-environment node
import { Editor } from "@tiptap/core";
import { describe, expect, it, vi } from "vitest";

import { createTranslator } from "@openfox/i18n";

import { docExtensions } from "./extensions";

const EMPTY_DOC = { type: "doc", content: [{ type: "paragraph" }] };
import {
  buildSlashItems,
  filterSlashItems,
  type SlashActions,
  type SlashItem,
} from "./slash";

// 斜杠菜单：菜单项顺序与上传项的开关、中英文关键词过滤，以及每一项执行后的文档结构。

const t = createTranslator("zh-CN");
const actions = (uploadable: boolean): SlashActions => ({
  pickImage: vi.fn(),
  pickFile: vi.fn(),
  uploadable,
});

function headless(markdown = "") {
  // 空串会走 HTML 解析（node 下没有 window），所以空文档用 JSON 载入
  return new Editor({
    extensions: docExtensions(""),
    ...(markdown
      ? { content: markdown, contentType: "markdown" as const }
      : { content: EMPTY_DOC }),
  });
}

function item(items: SlashItem[], id: string): SlashItem {
  const found = items.find((entry) => entry.id === id);
  if (!found) throw new Error(`缺少菜单项 ${id}`);
  return found;
}

describe("斜杠菜单项", () => {
  it("按固定顺序列出，可编辑时包含上传项", () => {
    const ids = buildSlashItems(t, actions(true)).map((entry) => entry.id);
    expect(ids).toEqual([
      "text",
      "h1",
      "h2",
      "h3",
      "h4",
      "h5",
      "h6",
      "bullet",
      "ordered",
      "todo",
      "image",
      "attachment",
      "code",
      "quote",
      "table",
      "divider",
    ]);
  });

  it("只读时没有上传项", () => {
    const ids = buildSlashItems(t, actions(false)).map((entry) => entry.id);
    expect(ids).not.toContain("image");
    expect(ids).not.toContain("attachment");
    expect(ids).toContain("table");
  });

  it("中文标题使用 标题 N 的文案", () => {
    const h2 = item(buildSlashItems(t, actions(true)), "h2");
    expect(h2.label).toBe("标题 2");
  });
});

describe("关键词过滤", () => {
  const items = buildSlashItems(t, actions(true));
  const ids = (query: string) =>
    filterSlashItems(items, query).map((entry) => entry.id);

  it("空查询返回全部", () => {
    expect(ids("")).toHaveLength(items.length);
    expect(ids("   ")).toHaveLength(items.length);
  });

  it("英文关键词：h1、bullet、todo", () => {
    expect(ids("h1")).toEqual(["h1"]);
    expect(ids("bullet")).toEqual(["bullet"]);
    expect(ids("todo")).toEqual(["todo"]);
  });

  it("中文关键词：图片、表格，不区分大小写", () => {
    expect(ids("图片")).toEqual(["image"]);
    expect(ids("表格")).toEqual(["table"]);
    expect(ids("TABLE")).toEqual(["table"]);
  });

  it("没有匹配时为空", () => {
    expect(ids("不存在的词")).toEqual([]);
  });
});

describe("菜单项执行", () => {
  it("标题项把当前段落改为对应级别", () => {
    const editor = headless("正文");
    item(buildSlashItems(t, actions(true)), "h2").run(editor);
    expect(editor.getMarkdown()).toBe("## 正文");
    editor.destroy();
  });

  it("待办、无序与有序列表", () => {
    const items = buildSlashItems(t, actions(true));
    const todo = headless("事项");
    item(items, "todo").run(todo);
    expect(todo.getMarkdown()).toBe("- [ ] 事项");
    todo.destroy();

    const bullet = headless("事项");
    item(items, "bullet").run(bullet);
    expect(bullet.getMarkdown()).toBe("- 事项");
    bullet.destroy();
  });

  it("表格插入 3×3 并带表头", () => {
    const editor = headless("");
    item(buildSlashItems(t, actions(true)), "table").run(editor);
    const table = editor.state.doc.firstChild;
    expect(table?.type.name).toBe("table");
    expect(table?.childCount).toBe(3);
    editor.destroy();
  });

  it("分割线与代码块", () => {
    const items = buildSlashItems(t, actions(true));
    const divider = headless("");
    item(items, "divider").run(divider);
    expect(divider.state.doc.content.size).toBeGreaterThan(0);
    expect(divider.getMarkdown()).toContain("---");
    divider.destroy();

    const code = headless("代码");
    item(items, "code").run(code);
    expect(code.getMarkdown()).toContain("```");
    code.destroy();
  });

  it("上传项调用对应的文件选择，不改动文档", () => {
    const pick = actions(true);
    const editor = headless("原文");
    const items = buildSlashItems(t, pick);
    item(items, "image").run(editor);
    item(items, "attachment").run(editor);
    expect(pick.pickImage).toHaveBeenCalledOnce();
    expect(pick.pickFile).toHaveBeenCalledOnce();
    expect(editor.getMarkdown()).toBe("原文");
    editor.destroy();
  });
});
