import { describe, expect, it } from "vitest";

import {
  KIND_BADGE,
  attachmentKind,
  formatBytes,
  hasAttachmentLink,
  namedPastedFile,
  precheckFile,
} from "./model";

describe("附件 · 纯逻辑", () => {
  it("formatBytes", () => {
    expect(formatBytes(0)).toBe("0 B");
    expect(formatBytes(1023)).toBe("1023 B");
    expect(formatBytes(1536)).toBe("1.5 KB");
    expect(formatBytes(20 * 1024 * 1024)).toBe("20 MB");
  });

  it("attachmentKind 按扩展名分类，徽标对应 Word / Excel / PDF", () => {
    expect(attachmentKind("年会活动方案.docx")).toBe("word");
    expect(attachmentKind("预算明细表.XLSX")).toBe("excel");
    expect(attachmentKind("供应商合同.pdf")).toBe("pdf");
    expect(attachmentKind("shot.PNG")).toBe("image");
    expect(attachmentKind("a.zip")).toBe("archive");
    expect(attachmentKind("README.md")).toBe("text");
    expect(attachmentKind("Makefile")).toBe("file");
    expect(KIND_BADGE.word.badge).toBe("W");
    expect(KIND_BADGE.excel.badge).toBe("X");
    expect(KIND_BADGE.pdf.badge).toBe("PDF");
  });

  it("precheckFile 只拒绝超过 20 MB 的文件", () => {
    const small = new File(["x"], "a.txt");
    expect(precheckFile(small)).toBeNull();
    const sized = (size: number) => {
      const file = new File(["x"], "b.bin");
      Object.defineProperty(file, "size", { value: size });
      return file;
    };
    expect(precheckFile(sized(20 * 1024 * 1024 + 1))).toBe("tooLarge");
    expect(precheckFile(sized(20 * 1024 * 1024))).toBeNull();
  });

  it("粘贴的 image.png 补上时间戳文件名，已有名字的保持不变", () => {
    const now = new Date("2026-11-01T08:09:10Z");
    const pasted = namedPastedFile(
      new File(["x"], "image.png", { type: "image/png" }),
      "粘贴的图片",
      now,
    );
    expect(pasted.name).toBe("粘贴的图片-20261101080910.png");
    const named = new File(["x"], "mine.png", { type: "image/png" });
    expect(namedPastedFile(named, "粘贴的图片", now)).toBe(named);
  });

  it("只有含附件链接的评论按 Markdown 渲染", () => {
    const id = "123e4567-e89b-12d3-a456-426614174000";
    expect(
      hasAttachmentLink(
        `[a.pdf](https://x.test/api/v1/organizations/o1/attachments/${id})`,
      ),
    ).toBe(true);
    expect(hasAttachmentLink("普通评论 **不是加粗**")).toBe(false);
    expect(hasAttachmentLink("[x](https://x.test/other)")).toBe(false);
  });
});
