import { describe, expect, it } from "vitest";
import {
  HTML_MAX_BYTES,
  htmlPreviewDocument,
  htmlSelection,
} from "./htmlDocument";
import { artifactPreviewKind } from "./model";

const token = "01234567-89ab-4def-8012-3456789abcde";
describe("HTML source mapping and isolation", () => {
  it("maps exact original UTF-8 outer HTML, retaining BOM, emoji, CRLF and entities", () => {
    const quote = '<p class="note">中文 &amp; 🙂\r\n<b>文字</b></p>';
    const source =
      "\ufeff<!doctype html><html><head><style>.note{color:red}</style></head><body>前🙂\r\n" +
      quote +
      "</body></html>";
    const result = htmlPreviewDocument(source, token);
    const target = result.targets.find((item) => item.tag === "p")!;
    expect(target.anchor).toEqual({
      kind: "text",
      quote,
      start: new TextEncoder().encode(source.slice(0, source.indexOf(quote)))
        .length,
      end: new TextEncoder().encode(
        source.slice(0, source.indexOf(quote) + quote.length),
      ).length,
    });
    expect(result.document).toContain('class="note"');
    expect(result.document).toContain(".note{color:red}");
    expect(result.targets.some((item) => item.tag === "style")).toBe(false);
  });
  it("does not invent ranges for implicit or malformed elements", () => {
    const result = htmlPreviewDocument(
      "<table><tr><td>值</td></tr></table>",
      token,
    );
    expect(result.targets.map((item) => item.tag)).toEqual([
      "table",
      "tr",
      "td",
    ]);
    expect(htmlPreviewDocument("<p>一<p>二", token).targets).toHaveLength(0);
    expect(
      htmlPreviewDocument('<p id="x" id="y">一</p>', token).targets,
    ).toHaveLength(0);
  });
  it("drops active content, URLs and forged markers, leaving only the trusted bridge", () => {
    const source = `<script>window.attack=true</script><meta http-equiv="refresh" content="0;url=https://bad.test"><base href="https://bad.test"><link rel="stylesheet" href="https://bad.test/a.css"><iframe srcdoc="bad"></iframe><svg onload="attack()"><foreignObject>bad</foreignObject></svg><object data="bad"></object><form action="https://bad.test"><input onfocus="attack()"></form><a href="javascript:attack()" data-openfox-review="e999">link</a><img src="https://bad.test/x" onerror="attack()"><img src="data:image/svg+xml;base64,PHN2Zz4=">`;
    const result = htmlPreviewDocument(source, token);
    const parsed = new DOMParser().parseFromString(
      result.document,
      "text/html",
    );
    expect(parsed.querySelectorAll("script")).toHaveLength(1);
    expect(parsed.querySelector("script")?.getAttribute("nonce")).toBe(token);
    expect(
      parsed.querySelector(
        "iframe,svg,object,base,link,[onfocus],[onerror],[href],[action]",
      ),
    ).toBeNull();
    expect(parsed.querySelectorAll("img[src]")).toHaveLength(0);
    expect(parsed.querySelector('[data-openfox-review="e999"]')).toBeNull();
    expect(parsed.querySelector("input")?.disabled).toBe(true);
    const policy = parsed
      .querySelector('meta[http-equiv="Content-Security-Policy"]')
      ?.getAttribute("content");
    expect(policy).toContain(`script-src 'nonce-${token}'`);
    expect(policy).toContain("connect-src 'none'");
    expect(htmlPreviewDocument(source).document).toContain("script-src 'none'");
  });
  it("bounds parsing and selection sizes", () => {
    expect(() => htmlPreviewDocument("x".repeat(HTML_MAX_BYTES + 1))).toThrow();
    expect(() =>
      htmlPreviewDocument("<div>".repeat(130) + "</div>".repeat(130)),
    ).toThrow();
    expect(() => htmlPreviewDocument("<br>".repeat(20001))).toThrow();
    expect(() => htmlPreviewDocument("<br>".repeat(19000))).not.toThrow();
    expect(
      htmlPreviewDocument("<p>" + "x".repeat(8192) + "</p>").targets,
    ).toHaveLength(0);
    expect(() => htmlPreviewDocument("<p>x</p>", 'bad"nonce')).toThrow();
  });
  it("renders a multi-megabyte report with embedded step screenshots", () => {
    // 测试报告：单文件 HTML，步骤截图以 data: 内嵌，体积超过 7,447,186 字节（A05 对照样本）。
    const png = "iVBORw0KGgo" + "A".repeat(180_000);
    const cases = Array.from(
      { length: 14 },
      (_, c) =>
        `<section><h2>TC-${c + 1}</h2>` +
        Array.from(
          { length: 3 },
          (_, s) =>
            `<figure><img alt="TC-${c + 1} 步骤 ${s + 1}" src="data:image/png;base64,${png}"><figcaption>步骤 ${s + 1}</figcaption></figure>`,
        ).join("") +
        "</section>",
    ).join("");
    const source = `<!doctype html><html><head><title>测试报告</title></head><body><h1>测试报告</h1>${cases}</body></html>`;
    expect(new TextEncoder().encode(source).length).toBeGreaterThan(7_447_186);
    const parsed = new DOMParser().parseFromString(
      htmlPreviewDocument(source).document,
      "text/html",
    );
    expect(parsed.querySelectorAll("img[src^='data:image/png']")).toHaveLength(
      42,
    );
    expect(parsed.querySelectorAll("h2")).toHaveLength(14);
  });
  it("accepts only the current frame, opaque origin, session and mapped element ID", () => {
    const result = htmlPreviewDocument("<p>原文</p>", token);
    const event = {
      source: window,
      origin: "null",
      data: {
        type: "openfox-html-select",
        session: token,
        id: result.targets[0]!.id,
      },
    } as unknown as MessageEvent;
    expect(htmlSelection(event, window, token, result.targets)).toEqual(
      result.targets[0]!.anchor,
    );
    for (const invalid of [
      { ...event, source: null },
      { ...event, origin: "https://bad.test" },
      { ...event, data: { ...event.data, session: "stale" } },
      { ...event, data: { ...event.data, id: "e999" } },
      { ...event, data: { ...event.data, anchor: { start: 0, end: 99999 } } },
    ])
      expect(
        htmlSelection(invalid as MessageEvent, window, token, result.targets),
      ).toBeUndefined();
  });
  it("only enables HTML rendering for verified text with an HTML filename", () => {
    expect(
      artifactPreviewKind({ mime_type: "text/plain", name: "PAGE.HTM" }),
    ).toBe("html");
    expect(
      artifactPreviewKind({
        mime_type: "application/octet-stream",
        name: "page.html",
      }),
    ).toBe("binary");
    expect(
      artifactPreviewKind({ mime_type: "text/plain", name: "log.txt" }),
    ).toBe("code");
  });
});
