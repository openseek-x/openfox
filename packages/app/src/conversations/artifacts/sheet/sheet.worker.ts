import {
  initWasm,
  openWorkbook,
  paintDisplayList,
  type WorkbookHandle,
  type Rect,
} from "@betteroffice/xlsx";
import {
  loadBundledFontBytes,
  resolveBundledFamilyFace,
  resolveMetricCompatFace,
  resolveLastResortFace,
  resolveScriptFallbackFace,
} from "@betteroffice/fonts";
import type { OfficeView } from "@openfox/api";
import { officeFontFamily } from "../word/fonts";
import type { SheetRequest, SheetReply } from "./protocol";
let book: WorkbookHandle | undefined;
let view: OfficeView;
const loaded = new Map<string, string>();
const images = new Set<ImageBitmap>();
const send = (reply: SheetReply, transfer: Transferable[] = []) =>
  self.postMessage(reply, { transfer });
async function open(bytes: ArrayBuffer, source: OfficeView) {
  const digest = [
    ...new Uint8Array(await crypto.subtle.digest("SHA-256", bytes)),
  ]
    .map((b) => b.toString(16).padStart(2, "0"))
    .join("");
  if (
    bytes.byteLength > 10 * 1024 * 1024 ||
    !source.spreadsheet ||
    digest !== source.spreadsheet.sha256
  )
    throw Error("Source mismatch");
  await initWasm();
  book = openWorkbook(new Uint8Array(bytes));
  view = source;
  const mapped = book.exportStructured();
  if (
    !mapped.ok ||
    book.sheetCount() !== source.spreadsheet.sheets.length ||
    book.sheetCount() > 1000
  )
    throw Error("Sheet mismatch");
  for (const sheet of source.spreadsheet.sheets) {
    const origin = mapped.content.sheets.find(
      (s) => s.anchor.kind === "sheet" && s.anchor.sheet.index === sheet.index,
    );
    if (origin?.source?.part !== sheet.part) throw Error("Source mismatch");
  }
  send({ kind: "ready" });
}
async function sheet(index: number) {
  const meta = view.spreadsheet?.sheets[index];
  if (!book || !meta || meta.hidden) throw Error("Invalid sheet");
  book.setActiveSheet(index);
  const info = book.sheetInfo();
  const point = (p: { row: number; column: number; x: number; y: number }) => {
    const r = book!.cellRect(index, p.row, p.column);
    return { x: r.x + p.x, y: r.y + p.y };
  };
  const objects = meta.drawings.map((d) => {
    const a = point(d.from),
      b = d.to ? point(d.to) : { x: a.x + d.width, y: a.y + d.height };
    return { ...a, w: b.x - a.x, h: b.y - a.y, name: d.name, drawing: d };
  });
  // The engine paints charts; images are added afterward. If that would
  // reverse an overlapping object's source stacking order, use PDF instead.
  if (
    objects.some(
      (a, i) =>
        a.drawing.kind === "image" &&
        objects
          .slice(i + 1)
          .some(
            (b) =>
              b.drawing.kind === "chart" &&
              a.x < b.x + b.w &&
              b.x < a.x + a.w &&
              a.y < b.y + b.h &&
              b.y < a.y + a.h,
          ),
    )
  )
    throw Error("Unsupported drawing order");
  let width = Math.ceil(
    Math.max(640, info.contentWidth, ...objects.map((o) => o.x + o.w)),
  );
  let height = Math.ceil(
    Math.max(400, info.contentHeight, ...objects.map((o) => o.y + o.h)),
  );
  if (
    !Number.isFinite(width * height) ||
    width > 16000 ||
    height > 16000 ||
    width * height > 8_000_000 ||
    objects.some((o) => o.w <= 0 || o.h <= 0)
  )
    throw Error("Sheet bitmap limit");
  const frame = book.displayList({ x: 0, y: 0, width, height });
  if (
    frame.truncated ||
    !frame.grid ||
    frame.charts?.some((c) => c.placeholder)
  )
    throw Error("Incomplete sheet");
  const fontSet = (self as unknown as { fonts: { add(f: FontFace): void } })
    .fonts;
  const register = async (family: string, bold: boolean, italic: boolean) => {
    const normalized = officeFontFamily(family);
    const face =
      resolveBundledFamilyFace(normalized, bold, italic) ??
      resolveMetricCompatFace(normalized, bold, italic) ??
      resolveBundledFamilyFace(normalized, false, false) ??
      resolveLastResortFace(normalized, bold, italic, "powerpoint");
    const key = face.file;
    if (!loaded.has(key)) {
      const alias = `OpenFox Sheet ${key}`,
        font = new FontFace(alias, await loadBundledFontBytes(face), {
          weight: String(face.weight),
          style: face.style,
        });
      await font.load();
      fontSet.add(font);
      loaded.set(key, alias);
    }
    return loaded.get(key)!;
  };
  const cjk = resolveScriptFallbackFace("cjk-sc", false, false)!;
  const fallback = await register(cjk.family, false, false);
  for (const c of frame.commands) {
    if (c.op === "text") {
      const alias = await register(
        c.fontFamily ?? "Calibri",
        !!c.bold,
        !!c.italic,
      );
      c.fontFamily = `"${alias}", "${fallback}"`;
    }
  }
  const canvas = new OffscreenCanvas(width * 2, height * 2);
  const ctx = canvas.getContext("2d")!;
  ctx.fillStyle = "white";
  ctx.fillRect(0, 0, canvas.width, canvas.height);
  paintDisplayList(ctx as unknown as CanvasRenderingContext2D, frame, 2);
  ctx.setTransform(2, 0, 0, 2, 0, 0);
  for (const object of objects) {
    if (object.drawing.kind !== "image") continue;
    const raw = Uint8Array.from(atob(object.drawing.data!), (c) =>
      c.charCodeAt(0),
    );
    const image = await createImageBitmap(new Blob([raw]));
    images.add(image);
    ctx.drawImage(image, object.x, object.y, object.w, object.h);
  }
  const merges = book.mergedRanges(index, "A1:XFD1048576");
  if (merges.length > 10000) throw Error("Merge limit");
  const frozen = book.cellRect(index, meta.frozen_rows, meta.frozen_columns);
  const bitmap = canvas.transferToImageBitmap();
  send(
    {
      kind: "sheet",
      index,
      bitmap,
      width,
      height,
      grid: frame.grid,
      merges,
      frozenWidth: meta.frozen_columns ? frozen.x : 0,
      frozenHeight: meta.frozen_rows ? frozen.y : 0,
      objects: objects.map(({ x, y, w, h, name }): Rect & { name: string } => ({
        x,
        y,
        w,
        h,
        name,
      })),
    },
    [bitmap],
  );
  for (const image of images) image.close();
  images.clear();
}
let chain = Promise.resolve();
self.onmessage = (e: MessageEvent<SheetRequest>) => {
  const request = e.data;
  chain = chain
    .then(() =>
      request.kind === "open"
        ? open(request.bytes, request.view)
        : sheet(request.index),
    )
    .catch(() => {
      book?.dispose();
      book = undefined;
      for (const image of images) image.close();
      images.clear();
      send({ kind: "error" });
    });
};
