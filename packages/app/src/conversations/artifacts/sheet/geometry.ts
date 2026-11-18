import type { GridMeta, MergedRange } from "@betteroffice/xlsx";
export type Cell = { row: number; col: number };
export type Range = { r1: number; c1: number; r2: number; c2: number };
export function address(row: number, col: number) {
  let label = "";
  for (let v = col + 1; v; v = Math.floor((v - 1) / 26))
    label = String.fromCharCode(65 + ((v - 1) % 26)) + label;
  return label + (row + 1);
}
export function rangeName(r: Range) {
  const a = address(r.r1, r.c1),
    b = address(r.r2, r.c2);
  return a === b ? a : `${a}:${b}`;
}
export function selected(a: Cell, b: Cell, merges: MergedRange[]): Range {
  const r = {
    r1: Math.min(a.row, b.row),
    c1: Math.min(a.col, b.col),
    r2: Math.max(a.row, b.row),
    c2: Math.max(a.col, b.col),
  };
  let changed = true;
  while (changed) {
    changed = false;
    for (const m of merges) {
      if (
        r.r1 <= m.end.row &&
        m.start.row <= r.r2 &&
        r.c1 <= m.end.col &&
        m.start.col <= r.c2
      ) {
        const next = {
          r1: Math.min(r.r1, m.start.row),
          c1: Math.min(r.c1, m.start.col),
          r2: Math.max(r.r2, m.end.row),
          c2: Math.max(r.c2, m.end.col),
        };
        if (JSON.stringify(next) !== JSON.stringify(r)) {
          Object.assign(r, next);
          changed = true;
        }
      }
    }
  }
  return r;
}
export function tracks(grid: GridMeta, axis: "row" | "col") {
  const offsets = axis === "row" ? grid.rowOffsets : grid.colOffsets;
  const indices = axis === "row" ? grid.rowIndices : grid.colIndices;
  const start = axis === "row" ? grid.startRow : grid.startCol;
  return offsets
    .slice(0, -1)
    .map((at, i) => ({
      index: indices?.[i] ?? start + i,
      start: at,
      end: offsets[i + 1]!,
    }))
    .filter((v) => v.end > v.start);
}
export function moveCell(
  cell: Cell,
  delta: [number, number],
  grid: GridMeta,
  merges: MergedRange[],
): Cell {
  const r = selected(cell, cell, merges);
  const step = (
    axis: "row" | "col",
    direction: number,
    current: number,
    min: number,
    max: number,
  ) => {
    if (!direction) return current;
    const visible = tracks(grid, axis);
    return direction > 0
      ? (visible.find((v) => v.index > max)?.index ?? current)
      : (visible.reverse().find((v) => v.index < min)?.index ?? current);
  };
  return {
    row: step("row", delta[0], cell.row, r.r1, r.r2),
    col: step("col", delta[1], cell.col, r.c1, r.c2),
  };
}
export function cellAt(grid: GridMeta, x: number, y: number): Cell | null {
  const row = tracks(grid, "row").find((v) => y >= v.start && y < v.end),
    col = tracks(grid, "col").find((v) => x >= v.start && x < v.end);
  return row && col ? { row: row.index, col: col.index } : null;
}
