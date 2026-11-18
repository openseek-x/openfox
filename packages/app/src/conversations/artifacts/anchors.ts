import type { ArtifactReviewAnchor } from "@openfox/api";

const encoder = new TextEncoder();
export const utf8Length = (s: string) => encoder.encode(s).length;

/** Textarea selection offsets are UTF-16 code units; persisted offsets are UTF-8 bytes. */
export function textAnchor(
  text: string,
  start: number,
  end: number,
): ArtifactReviewAnchor | null {
  if (start < 0 || end <= start || end > text.length) return null;
  const boundary = (n: number) =>
    n === 0 ||
    n === text.length ||
    !(
      text.charCodeAt(n - 1) >= 0xd800 &&
      text.charCodeAt(n - 1) <= 0xdbff &&
      text.charCodeAt(n) >= 0xdc00 &&
      text.charCodeAt(n) <= 0xdfff
    );
  const quote = text.slice(start, end);
  if (!boundary(start) || !boundary(end) || utf8Length(quote) > 8192)
    return null;
  return {
    kind: "text",
    start: utf8Length(text.slice(0, start)),
    end: utf8Length(text.slice(0, end)),
    quote,
  };
}

/** Browsers normalize textarea CRLF and CR to LF; translate back before persisting. */
export function textareaAnchor(text: string, start: number, end: number) {
  let raw = 0,
    shown = 0,
    rawStart = -1;
  while (raw <= text.length) {
    if (shown === start) rawStart = raw;
    if (shown === end)
      return rawStart < 0 ? null : textAnchor(text, rawStart, raw);
    if (raw === text.length) break;
    raw += text[raw] === "\r" && text[raw + 1] === "\n" ? 2 : 1;
    shown++;
  }
  return null;
}

export interface ReviewCell {
  value: string;
  anchor: ArtifactReviewAnchor;
}
/** Strict comma CSV, with raw source spans. Display indices never become source offsets. */
export function reviewCSV(text: string): ReviewCell[][] {
  const rows: ReviewCell[][] = [];
  let at = text.startsWith("\ufeff") ? 1 : 0,
    bytes = at ? 3 : 0,
    count = 0;
  while (at < text.length) {
    if (
      text[at] === "\n" ||
      (text[at] === "\r" && (text[at + 1] === "\n" || at + 1 === text.length))
    ) {
      const n = text[at] === "\r" ? 2 : 1;
      at += n;
      bytes += n;
      continue;
    }
    const row: ReviewCell[] = [];
    for (;;) {
      if (++count > 100000) throw new Error("CSV_CELL_LIMIT");
      const start = at,
        startByte = bytes;
      let value = "";
      if (text[at] === '"') {
        at++;
        let closed = false;
        while (at < text.length) {
          if (text[at] === '"') {
            at++;
            if (text[at] === '"') {
              value += '"';
              at++;
            } else {
              closed = true;
              break;
            }
          } else if (text[at] === "\r" && text[at + 1] === "\n") {
            value += "\n";
            at += 2;
          } else {
            value += text[at];
            at++;
          }
        }
        if (!closed) throw new Error("INVALID_CSV");
      } else {
        while (
          at < text.length &&
          text[at] !== "," &&
          text[at] !== "\n" &&
          !(
            text[at] === "\r" &&
            (text[at + 1] === "\n" || at + 1 === text.length)
          )
        ) {
          if (text[at] === '"') throw new Error("INVALID_CSV");
          value += text[at];
          at++;
        }
      }
      const quote = text.slice(start, at);
      bytes += utf8Length(quote);
      row.push({
        value,
        anchor: {
          kind: "cell",
          start: startByte,
          end: bytes,
          quote,
          row: rows.length,
          column: row.length,
        },
      });
      if (text[at] === ",") {
        at++;
        bytes++;
        continue;
      }
      if (
        text[at] === "\r" &&
        (text[at + 1] === "\n" || at + 1 === text.length)
      ) {
        at++;
        bytes++;
      }
      if (at === text.length) break;
      if (text[at] !== "\n") throw new Error("INVALID_CSV");
      at++;
      bytes++;
      break;
    }
    rows.push(row);
  }
  return rows;
}
