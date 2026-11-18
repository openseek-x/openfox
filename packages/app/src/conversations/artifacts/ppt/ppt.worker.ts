import {
  initWasm,
  openPresentation,
  paintSlide,
  decodePresentationImage,
  type PresentationHandle,
  type PptxExportShape,
  type SlidePrimitive,
} from "@betteroffice/pptx";
import {
  loadBundledFontBytes,
  resolveBundledFamilyFace,
  resolveMetricCompatFace,
  resolveLastResortFace,
  resolveScriptFallbackFace,
} from "@betteroffice/fonts";
import type { OfficeView } from "@openfox/api";
import type {
  OfficePageReply,
  OfficePageRequest,
  OfficeTextSpan,
  OfficeObject,
} from "../officePageProtocol";

import { officeFontFamily } from "../word/fonts";

let deck: PresentationHandle | undefined;
let frames: ReturnType<PresentationHandle["layoutSlide"]>[] = [];
let spans: OfficeTextSpan[][] = [];
let objects: OfficeObject[][] = [];
const images = new Map<
  string,
  Awaited<ReturnType<typeof decodePresentationImage>>
>();
const send = (reply: OfficePageReply, transfer: Transferable[] = []) =>
  self.postMessage(reply, { transfer });

async function open(bytes: ArrayBuffer, view: OfficeView) {
  const digest = [
    ...new Uint8Array(await crypto.subtle.digest("SHA-256", bytes)),
  ]
    .map((b) => b.toString(16).padStart(2, "0"))
    .join("");
  if (bytes.byteLength > 10 * 1024 * 1024 || digest !== view.sha256)
    throw Error("Source mismatch");
  await initWasm();
  deck = openPresentation(new Uint8Array(bytes));
  const model = deck.snapshot();
  if (!model.slides.length || model.slides.length > 1000)
    throw Error("Slide limit");
  // Resolve inherited/theme families from layout, then lay out again with the
  // same bundled faces used by Canvas paint. No system-font metrics guessing.
  const aliases = new Map<number, string>();
  const paintFonts = new Set<string>();
  const allPrimitives = (values: SlidePrimitive[]): SlidePrimitive[] =>
    values.flatMap((p) =>
      "primitives" in p ? [p, ...allPrimitives(p.primitives)] : [p],
    );
  const wanted = new Map<
    string,
    { family: string; bold: boolean; italic: boolean }
  >();
  for (let i = 0; i < model.slides.length; i++) {
    for (const p of allPrimitives(deck.layoutSlide(i).primitives)) {
      if (p.kind !== "textBox") continue;
      for (const line of p.lines ?? [])
        for (const run of line.runs) {
          const value = {
            family: run.fontFamily,
            bold: run.bold,
            italic: run.italic,
          };
          wanted.set(JSON.stringify(value), value);
        }
    }
  }
  if (wanted.size > 256) throw Error("Font limit");
  const register = async (
    family: string,
    bold: boolean,
    italic: boolean,
    fallback = false,
  ) => {
    const face = fallback
      ? resolveScriptFallbackFace("cjk-sc", false, false)
      : (resolveBundledFamilyFace(officeFontFamily(family), bold, italic) ??
        resolveMetricCompatFace(officeFontFamily(family), bold, italic) ??
        resolveBundledFamilyFace(officeFontFamily(family), false, false) ??
        resolveLastResortFace(family, bold, italic, "powerpoint"));
    if (!face) throw Error("Missing font");
    const raw = await loadBundledFontBytes(face);
    const alias = `OpenFox Office ${face.file}`;
    if (!paintFonts.has(alias)) {
      const font = new FontFace(alias, raw, {
        weight: String(face.weight),
        style: face.style,
      });
      await font.load();
      (self as unknown as { fonts: { add(font: FontFace): void } }).fonts.add(
        font,
      );
      paintFonts.add(alias);
    }
    const input = { family, bold, italic, bytes: new Uint8Array(raw) };
    const id = fallback
      ? deck!.registerFallbackFont(input)
      : deck!.registerFont(input);
    aliases.set(id, alias);
  };
  for (const { family, bold, italic } of wanted.values())
    await register(family, bold, italic);
  await register("OpenFox CJK", false, false, true);
  const mapped = deck.exportStructured();
  if (!mapped.ok) throw Error("Unmapped source");
  const sources = new Map<string, PptxExportShape>();
  const visit = (shape: PptxExportShape) => {
    if (!shape.hidden)
      for (const story of shape.stories)
        if (story.anchor.kind === "range")
          sources.set(story.anchor.storyId, shape);
    for (const child of shape.children) visit(child);
  };
  for (const slide of mapped.content.slides)
    for (const shape of slide.shapes) visit(shape);
  const units = new Map<string, OfficeView["units"]>();
  for (const unit of view.units) {
    if (!unit.editable || !unit.slide_source) continue;
    const key = `${unit.slide_source.part}:${unit.slide_source.shape}`;
    const group = units.get(key) ?? [];
    group.push(unit);
    units.set(key, group);
  }
  const targets: Record<string, number> = {};
  frames = model.slides.map((_, i) => deck!.layoutSlide(i));
  for (const frame of frames)
    for (const p of allPrimitives(frame.primitives)) {
      if (p.kind === "textBox")
        for (const line of p.lines)
          for (const run of line.runs) {
            const alias = aliases.get(run.fontId);
            if (!alias) throw Error("Unregistered paint font");
            run.fontFamily = alias;
          }
    }
  spans = frames.map((frame, index) =>
    frame.primitives.flatMap((p) => {
      if (
        p.kind !== "textBox" ||
        !p.storyId ||
        p.transform?.rotationDeg ||
        p.transform?.flipH ||
        p.transform?.flipV
      )
        return [];
      const source = sources.get(p.storyId);
      const origin = source?.provenance;
      if (!origin || origin.sourceId !== p.objectId) return [];
      const group = units.get(`${origin.part}:${origin.sourceId}`);
      if (!group) return [];
      return (p.lines ?? []).flatMap((line) =>
        line.runs.flatMap((run) =>
          group.flatMap((unit) => {
            const start = Math.max(run.start, unit.slide_source!.offset);
            const end = Math.min(
              run.end,
              unit.slide_source!.offset + unit.text.length,
            );
            if (end <= start) return [];
            const text = run.text.slice(start - run.start, end - run.start);
            if (
              text !==
              unit.text.slice(
                start - unit.slide_source!.offset,
                end - unit.slide_source!.offset,
              )
            )
              return [];
            // One transparent span per Unicode character gives browser selection
            // the actual engine caret geometry, independent of DOM font metrics.
            let at = start;
            return [...text].flatMap((char) => {
              const from = at;
              at += char.length;
              const left = line.caretStops.find((c) => c.position === from)?.x;
              const right = line.caretStops.find((c) => c.position === at)?.x;
              if (left === undefined || right === undefined || right <= left)
                return [];
              targets[unit.id] ??= index;
              return [
                {
                  target: unit.id,
                  offset: from - unit.slide_source!.offset,
                  text: char,
                  x: left,
                  y: line.y,
                  width: right - left,
                  height: line.height,
                  size: run.fontSizePx,
                },
              ];
            });
          }),
        ),
      );
    }),
  );
  objects = frames.map((frame) => {
    const seen = new Set<string>();
    return frame.primitives.flatMap((p) => {
      if (!p.shapeId || seen.has(p.shapeId) || p.w <= 0 || p.h <= 0) return [];
      seen.add(p.shapeId);
      const angle = ((p.transform?.rotationDeg ?? 0) * Math.PI) / 180;
      const w =
        Math.abs(p.w * Math.cos(angle)) + Math.abs(p.h * Math.sin(angle));
      const h =
        Math.abs(p.h * Math.cos(angle)) + Math.abs(p.w * Math.sin(angle));
      const x = Math.max(0, p.x + (p.w - w) / 2),
        y = Math.max(0, p.y + (p.h - h) / 2);
      const right = Math.min(frame.width, p.x + (p.w + w) / 2),
        bottom = Math.min(frame.height, p.y + (p.h + h) / 2);
      if (right <= x || bottom <= y) return [];
      return [
        {
          name: "name" in p ? p.name : `${seen.size}`,
          x,
          y,
          width: right - x,
          height: bottom - y,
        },
      ];
    });
  });
  send({ kind: "ready", pages: frames.length, targets });
}

async function page(index: number) {
  const frame = frames[index];
  if (!deck || !frame || frame.width * frame.height > 8_000_000)
    throw Error("Invalid slide");
  const canvas = new OffscreenCanvas(
    Math.ceil(frame.width * 2),
    Math.ceil(frame.height * 2),
  );
  await paintSlide(
    canvas.getContext("2d")! as unknown as CanvasRenderingContext2D,
    frame,
    1,
    2,
    {
      resolveImage: async (id) => {
        if (!images.has(id))
          images.set(
            id,
            await decodePresentationImage(
              deck!.mediaBytes(id),
              "Image unavailable",
            ),
          );
        return images.get(id)!;
      },
    },
  );
  const bitmap = canvas.transferToImageBitmap();
  send(
    {
      kind: "page",
      page: index,
      width: frame.width,
      height: frame.height,
      bitmap,
      spans: spans[index]!,
      objects: objects[index]!,
    },
    [bitmap],
  );
  for (const image of images.values()) if ("close" in image) image.close();
  images.clear();
}
let chain = Promise.resolve();
self.onmessage = (event: MessageEvent<OfficePageRequest>) => {
  const request = event.data;
  chain = chain
    .then(() =>
      request.kind === "open"
        ? open(request.bytes, request.view)
        : page(request.page),
    )
    .catch(() => {
      deck?.dispose();
      deck = undefined;
      for (const image of images.values()) if ("close" in image) image.close();
      images.clear();
      send({ kind: "error" });
    });
};
