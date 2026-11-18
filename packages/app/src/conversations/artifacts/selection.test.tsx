import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { I18nProvider } from "@openfox/i18n";
import { ChatMarkdown } from "../ChatMarkdown";
import { domTextAnchor, sourceOffsets } from "./selection";
import { htmlPreviewDocument, htmlTextSelection } from "./htmlDocument";

describe("rendered selection mapping", () => {
  it("maps entities, escapes and CRLF without allowing half an entity", () => {
    expect(sourceOffsets("a&amp;🙂\r\nb", "a&🙂\nb")).toEqual([
      0, 1, 6, 7, 8, 10, 11,
    ]);
    expect(sourceOffsets("\\*a", "*a", true)).toEqual([0, 2, 3]);
    expect(sourceOffsets("&NotEqualTilde;", "≂̸")).toEqual([0, -1, 15]);
    expect(sourceOffsets("different", "shown")).toBeNull();
  });
  it("maps Markdown paragraphs with soft breaks and keeps the second repeated range", () => {
    const source = "原文\n\n前🙂原文\n后文";
    const { container } = render(
      <I18nProvider initialLocale="zh-CN">
        <ChatMarkdown source={source} selectable />
      </I18nProvider>,
    );
    const node = screen.getByText(/前🙂原文/).firstChild!;
    const range = document.createRange();
    range.setStart(node, 3);
    range.setEnd(node, 5);
    expect(domTextAnchor(container, source, range)).toEqual({
      kind: "text",
      quote: "原文",
      start: 15,
      end: 21,
    });
  });
  it("keeps literal source entities, CRLF and syntax-highlight spans exact", () => {
    const source = 'const a = "原文 &amp; 🙂";\r\n';
    const { container } = render(
      <div data-comment-plain="true">
        <pre>
          <code>
            <span>const a = </span>
            <span>{'"原文 &amp; 🙂";'}</span>
            {"\r\n"}
          </code>
        </pre>
      </div>,
    );
    const node = container.querySelectorAll("code span")[1]!.firstChild!;
    const range = document.createRange();
    range.setStart(node, 1);
    range.setEnd(node, 12);
    const root = container.firstChild as HTMLElement;
    expect(domTextAnchor(root, source, range)?.quote).toBe("原文 &amp; 🙂");
  });
  it("binds HTML text endpoints to this frame, session and exact original source", () => {
    const source = "<!doctype html><p>原文</p><p>前&amp;🙂后</p>";
    const session = "01234567-89ab-4def-8012-3456789abcde";
    const preview = htmlPreviewDocument(source, session);
    const target = preview.textTargets.find((t) =>
      source.slice(t.start).startsWith("前&amp;"),
    )!;
    const event = {
      source: window,
      origin: "null",
      data: {
        type: "openfox-html-text",
        session,
        start: { path: target.path, offset: 1 },
        end: { path: target.path, offset: 4 },
      },
    } as unknown as MessageEvent;
    const quote = "&amp;🙂",
      at = source.indexOf(quote);
    expect(
      htmlTextSelection(event, window, session, preview.textTargets, source),
    ).toEqual({
      kind: "text",
      quote,
      start: new TextEncoder().encode(source.slice(0, at)).length,
      end: new TextEncoder().encode(source.slice(0, at + quote.length)).length,
    });
    for (const invalid of [
      { ...event, origin: "https://other.test" },
      { ...event, source: null },
      { ...event, data: { ...event.data, session: "old" } },
      {
        ...event,
        data: { ...event.data, end: { path: target.path, offset: 9999 } },
      },
    ])
      expect(
        htmlTextSelection(
          invalid as MessageEvent,
          window,
          session,
          preview.textTargets,
          source,
        ),
      ).toBeNull();
  });
});
