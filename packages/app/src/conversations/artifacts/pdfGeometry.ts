import type { ArtifactReviewAnchor } from "@openfox/api";
import type { SelectionRect } from "./selection";

export type PDFViewport = {
  width: number;
  height: number;
  viewBox: number[];
  convertToPdfPoint(x: number, y: number): number[];
  convertToViewportPoint(x: number, y: number): number[];
};
export function pdfTextAnchor(
  quote: string,
  rects: SelectionRect[],
  bounds: SelectionRect,
  viewport: PDFViewport,
  page: number,
): ArtifactReviewAnchor | null {
  if (
    !quote.trim() ||
    new TextEncoder().encode(quote).length > 8192 ||
    bounds.width <= 0 ||
    bounds.height <= 0
  )
    return null;
  const seen = new Set<string>();
  const quads: number[][] = [];
  for (const r of rects) {
    const x = Math.max(bounds.left, r.left),
      y = Math.max(bounds.top, r.top),
      right = Math.min(bounds.left + bounds.width, r.left + r.width),
      bottom = Math.min(bounds.top + bounds.height, r.top + r.height);
    if (right - x < 0.2 || bottom - y < 0.2) continue;
    const key = [x, y, right, bottom].map((n) => n.toFixed(2)).join(":");
    if (seen.has(key)) continue;
    seen.add(key);
    const point = (a: number, b: number) =>
      viewport.convertToPdfPoint(
        ((a - bounds.left) * viewport.width) / bounds.width,
        ((b - bounds.top) * viewport.height) / bounds.height,
      );
    quads.push([
      ...point(x, y),
      ...point(right, y),
      ...point(right, bottom),
      ...point(x, bottom),
    ]);
  }
  if (!quads.length || quads.length > 128) return null;
  return {
    kind: "pdf_text",
    start: 0,
    end: 0,
    quote,
    page,
    quads,
    view_box: [...viewport.viewBox],
  };
}
export function pdfQuadPoints(quad: number[], viewport: PDFViewport) {
  const points: string[] = [];
  for (let i = 0; i < 8; i += 2) {
    const [x, y] = viewport.convertToViewportPoint(quad[i]!, quad[i + 1]!);
    points.push(
      `${(x! / viewport.width) * 100},${(y! / viewport.height) * 100}`,
    );
  }
  return points.join(" ");
}
