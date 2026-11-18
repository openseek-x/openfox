import { describe, expect, it } from "vitest";
import { reviewCSV, textAnchor, textareaAnchor } from "./anchors";

describe("version-bound anchors", () => {
  it("converts UTF-16 selection to exact UTF-8 ranges without splitting emoji", () => {
    const text = "前🙂重复\r\n重复结束";
    expect(textAnchor(text, 3, 5)).toEqual({
      kind: "text",
      start: 7,
      end: 13,
      quote: "重复",
    });
    expect(textAnchor(text, 2, 3)).toBeNull();
    expect(textareaAnchor(text, 6, 8)).toEqual({
      kind: "text",
      start: 15,
      end: 21,
      quote: "重复",
    });
    expect(textAnchor("x".repeat(8193), 0, 8193)).toBeNull();
  });
  it("keeps BOM and raw line endings in source coordinates", () => {
    expect(textareaAnchor("\ufeffa\r\nb\rc", 0, 6)?.quote).toBe(
      "\ufeffa\r\nb\rc",
    );
  });
  it("maps CSV cells including commas, quoted newlines, escaped quotes and empty cells", () => {
    const text =
      '名称,说明,数量\r\n"上,海","多行\r\n""引号""",\r\n\n北京,完成,2\n';
    const rows = reviewCSV(text);
    expect(rows).toHaveLength(3);
    expect(rows[1]![1]!.value).toBe('多行\n"引号"');
    expect(rows[1]![2]!.anchor.row).toBe(1);
    expect(rows[1]![2]!.anchor.start).toBe(rows[1]![2]!.anchor.end);
    const bytes = new TextEncoder().encode(text);
    for (const row of rows)
      for (const cell of row) {
        expect(
          new TextDecoder().decode(
            bytes.slice(cell.anchor.start, cell.anchor.end),
          ),
        ).toBe(cell.anchor.quote);
      }
  });
  it("rejects malformed CSV instead of guessing positions", () => {
    for (const text of ['a,"unterminated', 'a,b"c', '"a"junk,b'])
      expect(() => reviewCSV(text)).toThrow();
  });
  it("keeps a CSV BOM outside the first cell", () => {
    const cell = reviewCSV('\ufeff"标题",值\r\n甲,1')[0]![0]!;
    expect(cell.value).toBe("标题");
    expect(cell.anchor).toMatchObject({ start: 3, quote: '"标题"' });
  });
});
