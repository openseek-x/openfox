import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { Markdown } from "../conversations/Markdown";
import { CodeView } from "./CodeView";
import { highlightTokens, MAX_HIGHLIGHT_CHARS } from "./highlight";
import { languageForFence, languageForPath } from "./languages";

// 代码语法高亮：语言识别、片段生成（拼回原文、不生成 HTML）、懒加载后的渲染。

describe("语言识别", () => {
  it("按后缀与特殊文件名推断，后缀本身是别名时原样返回", () => {
    expect(languageForPath("src/app.tsx")).toBe("typescript");
    expect(languageForPath("main.go")).toBe("go");
    expect(languageForPath("scripts/run.sh")).toBe("sh");
    expect(languageForPath("pkg/Makefile")).toBe("makefile");
    expect(languageForPath(".env.local")).toBe("bash");
    expect(languageForPath("config.yml")).toBe("yml");
    expect(languageForPath("README")).toBeNull();
  });

  it("围栏语言只取第一个词并转小写，非法字符返回 null", () => {
    expect(languageForFence("TS")).toBe("ts");
    expect(languageForFence("python {title=a.py}")).toBe("python");
    expect(languageForFence("tsx")).toBe("typescript");
    expect(languageForFence("")).toBeNull();
    expect(languageForFence("<script>")).toBeNull();
  });
});

describe("highlightTokens", () => {
  const source =
    'const answer: number = 42; // 注释\nfunction hi() { return "ok"; }';

  it("指定语言时输出带 hljs 类名的片段，拼起来等于原文", () => {
    const tokens = highlightTokens(source, "ts");
    expect(tokens).not.toBeNull();
    expect(tokens!.map((token) => token.text).join("")).toBe(source);
    const classes = new Set(tokens!.flatMap((token) => token.classes));
    expect(classes.has("hljs-keyword")).toBe(true);
    expect(classes.has("hljs-string")).toBe(true);
    expect(classes.has("hljs-comment")).toBe(true);
  });

  it("不支持的语言、plaintext、空串与超长代码不高亮", () => {
    expect(highlightTokens(source, "no-such-lang")).toBeNull();
    expect(highlightTokens(source, "plaintext")).toBeNull();
    expect(highlightTokens("", "ts")).toBeNull();
    expect(
      highlightTokens("a".repeat(MAX_HIGHLIGHT_CHARS + 1), "ts"),
    ).toBeNull();
  });

  it("没有语言时：不开 auto 不高亮；开 auto 能识别明显的代码，普通文字不着色", () => {
    const python =
      'def main():\n    import os\n    for x in range(3):\n        print(f"{x}")\n    return None\n';
    expect(highlightTokens(python, null)).toBeNull();
    const auto = highlightTokens(python, null, { auto: true });
    expect(auto).not.toBeNull();
    expect(auto!.map((token) => token.text).join("")).toBe(python);
    expect(
      highlightTokens("今天下午三点开会，记得带上周报。", null, { auto: true }),
    ).toBeNull();
  });
});

describe("渲染", () => {
  it("CodeView 先显示纯文本与行号，高亮模块加载后出现 hljs 片段", async () => {
    const { container } = render(
      <CodeView code={"const a = 1;\nlet b = 2;"} language="ts" />,
    );
    expect(container.querySelector("pre")?.textContent).toBe("1\n2");
    expect(await screen.findByText("const")).toHaveClass("hljs-keyword");
    expect(container.querySelector("code")?.textContent).toBe(
      "const a = 1;\nlet b = 2;",
    );
    // 只生成 React 元素，没有内联 HTML 注入的脚本节点
    expect(container.querySelector("script")).toBeNull();
  });

  it("不支持的语言保持纯文本", async () => {
    const { container } = render(
      <CodeView code="FROM node:22" language="dockerfile" />,
    );
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(container.querySelector("code span")).toBeNull();
    expect(container.querySelector("code")?.textContent).toBe("FROM node:22");
  });

  it("对话 Markdown 的代码块按围栏语言高亮，代码内容里的 HTML 按文本显示", async () => {
    const { container } = render(
      <Markdown
        source={'```js\nconst el = "<img src=x onerror=alert(1)>";\n```'}
      />,
    );
    expect(await screen.findByText("const")).toHaveClass("hljs-keyword");
    expect(container.querySelector("img")).toBeNull();
    expect(container.querySelector("pre code")?.textContent).toBe(
      'const el = "<img src=x onerror=alert(1)>";',
    );
  });
});
