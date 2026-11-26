import { describe, expect, it } from "vitest";

import {
  MAX_SELECTION_CHARS,
  buildMessageBody,
  sanitizePageUrl,
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
