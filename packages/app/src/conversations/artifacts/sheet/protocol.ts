import type { OfficeView } from "@openfox/api";
import type { GridMeta, MergedRange, Rect } from "@betteroffice/xlsx";
export type SheetRequest =
  | { kind: "open"; bytes: ArrayBuffer; view: OfficeView }
  | { kind: "sheet"; index: number };
export type SheetFrame = {
  kind: "sheet";
  index: number;
  bitmap: ImageBitmap;
  width: number;
  height: number;
  grid: GridMeta;
  merges: MergedRange[];
  frozenWidth: number;
  frozenHeight: number;
  objects: (Rect & { name: string })[];
};
export type SheetReply = { kind: "ready" } | SheetFrame | { kind: "error" };
