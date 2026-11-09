import { describe, expect, it } from "vitest";

import { parseCsv, serializeCsv } from "./csv";

// 表格 CSV 的解析与生成：引号、逗号、换行与往返一致性。

describe("parseCsv", () => {
  it("解析普通的逗号分隔行", () => {
    expect(parseCsv("a,b,c\n1,2,3")).toEqual([
      ["a", "b", "c"],
      ["1", "2", "3"],
    ]);
  });

  it("支持 CRLF 与末尾换行，末尾换行不产生空行", () => {
    expect(parseCsv("a,b\r\n1,2\r\n")).toEqual([
      ["a", "b"],
      ["1", "2"],
    ]);
  });

  it("双引号包裹的字段可以含逗号、换行与转义引号", () => {
    expect(parseCsv('"a,1","line\nbreak","say ""hi"""')).toEqual([
      ["a,1", "line\nbreak", 'say "hi"'],
    ]);
  });

  it("保留空字段与末尾的空字段", () => {
    expect(parseCsv("a,,\n,b,")).toEqual([
      ["a", "", ""],
      ["", "b", ""],
    ]);
  });

  it("空文本没有行，去掉 BOM", () => {
    expect(parseCsv("")).toEqual([]);
    expect(parseCsv("﻿x,y")).toEqual([["x", "y"]]);
  });
});

describe("serializeCsv", () => {
  it("只为需要的字段加引号", () => {
    expect(serializeCsv([["a", "b c", "d,e", 'f"g', "h\ni"]])).toBe(
      'a,b c,"d,e","f""g","h\ni"',
    );
  });

  it("行之间用 LF 分隔，末尾不带换行", () => {
    expect(
      serializeCsv([
        ["a", "b"],
        ["1", "2"],
      ]),
    ).toBe("a,b\n1,2");
  });

  it("解析后再生成与原文一致（往返）", () => {
    const rows = [
      ["名称", "金额", "备注"],
      ["苹果", "3,5", '他说 "好"'],
      ["", "", "多行\n内容"],
    ];
    expect(parseCsv(serializeCsv(rows))).toEqual(rows);
  });
});
