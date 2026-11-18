import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { Markdown } from "../../conversations/Markdown";
import { applyFormat } from "./FormatToolbar";
import {
  deserializeMentions,
  parseMentionHref,
  serializeMentions,
  type ComposerMention,
} from "./mentionTokens";

const U = "11111111-1111-4111-8111-111111111111";
const C = "22222222-2222-4222-8222-222222222222";

const user: ComposerMention = {
  kind: "user",
  id: U,
  token: "a@x.com",
  label: "小王",
};
const crew: ComposerMention = {
  kind: "crew",
  id: C,
  token: "前端小队",
  label: "前端小队",
};
const all: ComposerMention = {
  kind: "all",
  token: "全体成员",
  label: "全体成员",
};

describe("提及令牌", () => {
  it("序列化只保留正文里仍存在的提及，并按类型生成 payload", () => {
    const result = serializeMentions("hi @a@x.com @全体成员 ok", [
      user,
      crew,
      all,
    ]);
    expect(result.body).toBe(
      `hi @[小王](openfox://mention/user/${U}) @[全体成员](openfox://mention/all) ok`,
    );
    expect(result.mentions).toEqual([{ user_id: U }, { all: true }]);
  });

  it("名字里的方括号与换行会被去掉", () => {
    const odd: ComposerMention = {
      kind: "crew",
      id: C,
      token: "x",
      label: "[组]\n名",
    };
    expect(serializeMentions("@x", [odd]).body).toBe(
      `@[组 名](openfox://mention/crew/${C})`,
    );
  });

  it("编辑往返：令牌 -> @名字 -> 令牌 保持不变", () => {
    const body = `请 @[小王](openfox://mention/user/${U}) 看，@[全体成员](openfox://mention/all)`;
    const parsed = deserializeMentions(body);
    expect(parsed.text).toBe("请 @小王 看，@全体成员");
    expect(serializeMentions(parsed.text, parsed.mentions).body).toBe(body);
  });

  it("无令牌的历史正文原样返回", () => {
    expect(deserializeMentions("@a@x.com 你好")).toEqual({
      text: "@a@x.com 你好",
      mentions: [],
    });
  });

  it("parseMentionHref 只认 openfox://mention", () => {
    expect(parseMentionHref(`openfox://mention/crew/${C}`)).toEqual({
      kind: "crew",
      id: C,
    });
    expect(parseMentionHref("openfox://mention/all")).toEqual({ kind: "all" });
    expect(parseMentionHref("https://x.com")).toBeNull();
    expect(parseMentionHref("openfox://mention/user/not-a-uuid")).toBeNull();
  });
});

describe("Markdown 渲染提及", () => {
  it("渲染为高亮文字，不是链接", () => {
    const { container } = render(
      <Markdown
        source={`看 @[小王](openfox://mention/user/${U}) 和 @[全体成员](openfox://mention/all)`}
      />,
    );
    const marks = container.querySelectorAll("[data-mention]");
    expect(marks).toHaveLength(2);
    expect(marks[0]!.textContent).toBe("@小王");
    expect(marks[0]!.getAttribute("data-mention-id")).toBe(U);
    expect(container.querySelector("a")).toBeNull();
  });

  it("非法令牌按原文显示，不生成提及", () => {
    const { container } = render(
      <Markdown source="@[x](openfox://mention/user/bad)" />,
    );
    expect(container.querySelector("[data-mention]")).toBeNull();
    expect(container.querySelector("a")).toBeNull();
  });

  it("粗体、斜体、列表、引用、代码块都能渲染", () => {
    const { container } = render(
      <Markdown
        source={"**粗** *斜*\n\n- 一\n- 二\n\n> 引\n\n```\ncode\n```"}
      />,
    );
    expect(container.querySelector("strong")?.textContent).toBe("粗");
    expect(container.querySelector("em")?.textContent).toBe("斜");
    expect(container.querySelectorAll("li")).toHaveLength(2);
    expect(container.querySelector("blockquote")).not.toBeNull();
    expect(container.querySelector("pre")).not.toBeNull();
    expect(screen).toBeDefined();
  });
});

describe("格式工具条", () => {
  it("包裹选区 / 列表按行加前缀 / 超长不改动", () => {
    expect(applyFormat("abc", 0, 3, "bold")).toEqual({
      value: "**abc**",
      start: 2,
      end: 5,
    });
    expect(applyFormat("abc", 1, 2, "code")?.value).toBe("a`b`c");
    expect(applyFormat("a\nb", 0, 3, "bullet")?.value).toBe("- a\n- b");
    expect(applyFormat("a", 0, 1, "quote")?.value).toBe("> a");
    expect(applyFormat("x", 0, 1, "codeBlock")?.value).toBe("```\nx\n```");
    expect(applyFormat("字", 0, 1, "link")?.value).toBe("[字](url)");
    expect(applyFormat("abc", 0, 3, "bold", 5)).toBeNull();
  });
});
