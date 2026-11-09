// @vitest-environment node
import { Editor } from "@tiptap/core";
import { describe, expect, it } from "vitest";

import { buildDecorations } from "./codeHighlight";
import { docExtensions } from "./extensions";

const EMPTY_DOC = { type: "doc", content: [{ type: "paragraph" }] };

// 正文 Markdown 往返：无头编辑器（不渲染 DOM）解析后再导出，应得到等价的 Markdown。

/** 按 Markdown 载入正文，再导出 Markdown。 */
function roundtrip(markdown: string): string {
  // 空串会走 HTML 解析（node 下没有 window），所以空文档用 JSON 载入
  const editor = new Editor({
    extensions: docExtensions(""),
    ...(markdown
      ? { content: markdown, contentType: "markdown" as const }
      : { content: EMPTY_DOC }),
  });
  const out = editor.getMarkdown();
  editor.destroy();
  return out;
}

describe("正文 Markdown 往返", () => {
  it("标题保留层级", () => {
    expect(roundtrip("# 标题一\n\n## 标题二\n\n### 标题三")).toBe(
      "# 标题一\n\n## 标题二\n\n### 标题三",
    );
  });

  it("无序列表、有序列表与嵌套", () => {
    const out = roundtrip("- 甲\n- 乙\n\n1. 第一\n2. 第二");
    expect(out).toContain("- 甲");
    expect(out).toContain("- 乙");
    expect(out).toContain("1. 第一");
    expect(out).toContain("2. 第二");
  });

  it("待办列表保留选中状态", () => {
    const out = roundtrip("- [ ] 未完成\n- [x] 已完成");
    expect(out).toContain("- [ ] 未完成");
    expect(out).toContain("- [x] 已完成");
  });

  it("代码块保留语言与内容", () => {
    expect(roundtrip("```ts\nconst a = 1;\n```")).toContain(
      "```ts\nconst a = 1;\n```",
    );
  });

  it("表格保留表头与单元格", () => {
    const out = roundtrip("| 名称 | 数量 |\n| --- | --- |\n| 苹果 | 3 |");
    expect(out).toMatch(/\| 名称\s+\| 数量\s+\|/);
    expect(out).toMatch(/\| 苹果\s+\| 3\s+\|/);
  });

  it("图片与链接", () => {
    const out = roundtrip("![图](/api/v1/x)\n\n[链接](https://example.com)");
    expect(out).toContain("![图](/api/v1/x)");
    expect(out).toContain("[链接](https://example.com)");
  });

  it("行内强调：粗体、斜体、删除线、行内代码", () => {
    const out = roundtrip("**粗** *斜* ~~删~~ `code`");
    expect(out).toContain("**粗**");
    expect(out).toContain("*斜*");
    expect(out).toContain("~~删~~");
    expect(out).toContain("`code`");
  });

  it("导出结果再次载入后不变（幂等）", () => {
    const once = roundtrip(
      "# 标题\n\n- [ ] 待办\n\n| a | b |\n| - | - |\n| 1 | 2 |",
    );
    expect(roundtrip(once)).toBe(once);
  });

  it("空文档导出为空字符串", () => {
    expect(roundtrip("")).toBe("");
  });
});

describe("正文代码块语法高亮", () => {
  /** 载入 Markdown，返回代码块里的高亮装饰（类名 + 覆盖的文本）。 */
  function highlights(markdown: string) {
    const editor = new Editor({
      extensions: docExtensions(""),
      content: markdown,
      contentType: "markdown" as const,
    });
    const found = buildDecorations(editor.state.doc)
      .find()
      .map((d) => ({
        text: editor.state.doc.textBetween(d.from, d.to),
        className: (d as unknown as { type: { attrs: { class: string } } }).type
          .attrs.class,
      }));
    const markdownOut = editor.getMarkdown();
    editor.destroy();
    return { found, markdownOut };
  }

  it("按围栏语言加 hljs 装饰，不改变导出的 Markdown", () => {
    const source = "```ts\nconst a: number = 1;\n```";
    const { found, markdownOut } = highlights(source);
    expect(found).toContainEqual({ text: "const", className: "hljs-keyword" });
    expect(markdownOut).toContain(source);
  });

  it("普通段落与没有可识别语言的短文本不加装饰", () => {
    expect(highlights("这是一段普通文字").found).toEqual([]);
    expect(highlights("```\n随便写点\n```").found).toEqual([]);
  });
});
