import type { ArtifactReviewAnchor } from "@openfox/api";
export type Point = { x: number; y: number };
export function rotatePoint(p: Point, rotation: number): Point {
  switch ((rotation + 360) % 360) {
    case 90:
      return { x: 1 - p.y, y: p.x };
    case 180:
      return { x: 1 - p.x, y: 1 - p.y };
    case 270:
      return { x: p.y, y: 1 - p.x };
    default:
      return p;
  }
}
export function regionFromPoints(
  a: Point,
  b: Point,
  page: number,
  rotation: number,
): ArtifactReviewAnchor | null {
  a = rotatePoint(a, 360 - rotation);
  b = rotatePoint(b, 360 - rotation);
  const x = Math.max(0, Math.min(a.x, b.x)),
    y = Math.max(0, Math.min(a.y, b.y));
  const width = Math.min(1, Math.max(a.x, b.x)) - x,
    height = Math.min(1, Math.max(a.y, b.y)) - y;
  if (width < 0.003 || height < 0.003) return null;
  return {
    kind: "region",
    start: 0,
    end: 0,
    quote: "",
    page,
    x,
    y,
    width,
    height,
  };
}
export function displayedRegion(a: ArtifactReviewAnchor, rotation: number) {
  const p = rotatePoint({ x: a.x ?? 0, y: a.y ?? 0 }, rotation);
  const q = rotatePoint(
    { x: (a.x ?? 0) + (a.width ?? 0), y: (a.y ?? 0) + (a.height ?? 0) },
    rotation,
  );
  return {
    left: `${Math.min(p.x, q.x) * 100}%`,
    top: `${Math.min(p.y, q.y) * 100}%`,
    width: `${Math.abs(p.x - q.x) * 100}%`,
    height: `${Math.abs(p.y - q.y) * 100}%`,
  };
}
