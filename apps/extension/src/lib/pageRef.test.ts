import { describe, expect, it } from "vitest";

import {
  MAX_SELECTION_CHARS,
  buildMessageBody,
  sanitizePageUrl,
  splitPageReference,
} from "./pageRef";

describe("sanitizePageUrl", () => {
  it("去掉查询串、片段与账号信息，只保留来源与路径", () => {
    expect(
      sanitizePageUrl("https://user:pw@example.com/a/b?token=1#frag"),
    ).toBe("https://example.com/a/b");
  });

  it("只接受 http 与 https 页面", () => {
    expect(sanitizePageUrl("chrome://extensions")).toBeNull();
    expect(sanitizePageUrl("file:///tmp/a.html")).toBeNull();
    expect(sanitizePageUrl("不是地址")).toBeNull();
  });
});

describe("buildMessageBody", () => {
  const page = {
    title: "  示例\n页面  ",
    url: "https://example.com/doc?secret=1",
    selection: "第一行\n第二行",
  };

  it("不勾选页面引用时正文就是用户输入", () => {
    expect(buildMessageBody("  你好  ", null)).toBe("你好");
  });

  it("勾选后正文包含标题、地址与选中文本，且地址不含查询串", () => {
    const body = buildMessageBody("总结一下", page);
    expect(body).toContain("总结一下");
    expect(body).toContain("标题：示例 页面");
    expect(body).toContain("地址：https://example.com/doc");
    expect(body).not.toContain("secret");
    expect(body).toContain("> 第一行\n> 第二行");
  });

  it("没有选中文本时不输出选中文本段", () => {
    const body = buildMessageBody("hi", { ...page, selection: "  " });
    expect(body).not.toContain("选中文本");
  });

  it("页面不可引用时退回为纯用户输入", () => {
    expect(
      buildMessageBody("hi", { ...page, url: "chrome://extensions" }),
    ).toBe("hi");
  });

  it("超长选中文本被截断并标注", () => {
    const body = buildMessageBody("hi", {
      ...page,
      selection: "字".repeat(MAX_SELECTION_CHARS + 50),
    });
    expect(body).toContain("（已截断）");
    expect(body).not.toContain("字".repeat(MAX_SELECTION_CHARS + 1));
  });
});

describe("splitPageReference", () => {
  const page = {
    title: "示例页面",
    url: "https://example.com/doc",
    selection: "第一行\n\n第三行",
  };
  it.each([page, { ...page, title: "", selection: "" }])(
    "用同一条消息还原正文和引用",
    (reference) => {
      const text = "保留正文中的\n\n---\n引用的页面：\n普通文本";
      expect(splitPageReference(buildMessageBody(text, reference))).toEqual({
        text,
        reference,
      });
    },
  );
  it.each([
    "普通正文",
    "标题：范例\n地址：不是地址",
    "标题：范例\n地址：https://example.com/?private=x",
    "标题：范例\n地址：https://example.com/\n没有引用的尾行",
    "标题：范例\n地址：https://example.com/\n选中文本：\n正文",
  ])("格式不完整时保留全部原文", (suffix) => {
    const body = "请阅读\n\n---\n引用的页面：\n" + suffix;
    expect(splitPageReference(body)).toEqual({ text: body, reference: null });
  });
});
