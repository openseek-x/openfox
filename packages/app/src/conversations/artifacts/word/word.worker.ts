// Retained-layout API sequence adapted from OpenCode (MIT): see NOTICE.md.
import {
  createYrsSession,
  createYrsPositionProjection,
} from "@betteroffice/docx/yrs";
import {
  buildResidentRegionLayoutRequest,
  getLayoutKernelInputs,
  workerLayoutComputation,
} from "@betteroffice/docx/editor";
import { createRustMeasureSource } from "@betteroffice/docx/layout";
import {
  applyFrameDelta,
  decodeFrameDelta,
  encodeDisplayListFrameExtras,
  glyphRunRect,
  rasterizeDisplayPageToBackBuffer,
  type DisplayPage,
} from "@betteroffice/docx/layout/render";
import type { OfficeView } from "@openfox/api";
import { wordFonts } from "./fonts";
import type {
  OfficePageReply,
  OfficePageRequest,
  OfficeTextSpan,
} from "../officePageProtocol";

let session: Awaited<ReturnType<typeof createYrsSession>> | undefined;
let pages: DisplayPage[] = [];
let spans: OfficeTextSpan[][] = [];
const images = new Map<string, ImageBitmap>();
const fonts = wordFonts();
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
  session = await createYrsSession();
  const document = session.openDocx(new Uint8Array(bytes), true, {
    mediaTokens: true,
  }).document;
  const engine = session;
  const measure = createRustMeasureSource({
    engine: {
      registerFont: (bytes) =>
        fonts.registered(engine.registerFont(bytes), bytes),
      registerSubstituteFont: (id, family) =>
        fonts.substitute(engine.registerSubstituteFont(id, family), id),
      clearFonts: () => engine.clearFonts(),
    },
    bundled: fonts.provider,
  });
  measure.setCompat(document.package.settings?.compatibilityFlags);
  const request = buildResidentRegionLayoutRequest(document, 0, {
    themeColors: Object.fromEntries(
      Object.entries(document.package.theme?.colorScheme ?? {}).filter(
        (entry): entry is [string, string] => typeof entry[1] === "string",
      ),
    ),
    defaultTabStopTwips: document.package.settings?.defaultTabStop ?? null,
    numericIds: {},
    mediaTokens: true,
  });
  const requirements = JSON.parse(
    session.layoutFontRequirementsJson(JSON.stringify(request)),
  );
  await measure.prepareFontRequirements(requirements);
  const measurement = measure.measurementConfigForRequirements(requirements);
  if (!measurement) throw Error("Missing metrics");
  const layout = workerLayoutComputation(
    session.layoutDocumentWithRegionsRetainedJson(
      JSON.stringify({ ...request, measurement }),
    ),
  ).layout;
  const extras = encodeDisplayListFrameExtras({
    measured: [],
    options: undefined,
    layout,
    fontChains: measurement.fontChains,
    headersFooters: getLayoutKernelInputs(layout)?.headersFooters,
    resolvedCommentIds: [],
  });
  const frame = applyFrameDelta(
    null,
    decodeFrameDelta(session.buildDisplayListFrame(extras, 0)),
  );
  pages = frame.displayList.pages;
  if (!pages.length || pages.length > 1000) throw Error("Page limit");
  const identities = session.paragraphIdentities();
  if (identities.packageSha256 !== digest) throw Error("Source mismatch");
  const projection = createYrsPositionProjection(session, "body");
  const sources = new Map(
    identities.paragraphs.flatMap((p) =>
      p.session && p.source ? [[p.session.paraId, p] as const] : [],
    ),
  );
  const units = new Map<string, OfficeView["units"]>();
  for (const unit of view.units) {
    if (!unit.editable || !unit.source) continue;
    const key = `${unit.source.part}:${unit.source.paragraph}`;
    const group = units.get(key) ?? [];
    group.push(unit);
    units.set(key, group);
  }
  const targets: Record<string, number> = {};
  spans = pages.map((page, index) =>
    page.primitives.flatMap((primitive) => {
      if (
        primitive.kind !== "glyphRun" ||
        primitive.hidden ||
        primitive.rotationDeg ||
        primitive.horizontalScale ||
        !primitive.blockKey
      )
        return [];
      const identity = sources.get(primitive.blockKey);
      if (!identity?.session || !identity.source || !projection) return [];
      const base = projection.positionForLoc({
        story: identity.session.story,
        paraId: identity.session.paraId,
        offset: 0,
      });
      if (
        base === null ||
        primitive.docStart === undefined ||
        primitive.docEnd === undefined
      )
        return [];
      const offset = primitive.docStart - base;
      const group = units.get(
        `${identity.source.partUri.replace(/^\//, "")}:${identity.source.paragraphOrdinal}`,
      );
      const unit = group?.find(
        (u) =>
          offset >= u.source!.offset &&
          primitive.docEnd! - base <= u.source!.offset + u.text.length,
      );
      if (
        !unit ||
        unit.text.slice(
          offset - unit.source!.offset,
          primitive.docEnd - base - unit.source!.offset,
        ) !== primitive.text
      )
        return [];
      const box = glyphRunRect(primitive);
      if (!box.w || !box.h) return [];
      targets[unit.id] ??= index;
      return [
        {
          target: unit.id,
          offset: offset - unit.source!.offset,
          text: primitive.text,
          x: box.x,
          y: box.y,
          width: box.w,
          height: box.h,
          size: primitive.size,
        },
      ];
    }),
  );
  send({ kind: "ready", pages: pages.length, targets });
}

async function page(index: number) {
  const value = pages[index];
  if (!session || !value || value.width * value.height > 8_000_000)
    throw Error("Invalid page");
  const canvas = new OffscreenCanvas(1, 1);
  // Browser paint uses the same registered bytes as measurement and synthesizes
  // styles absent from CJK Regular. Outline paint would silently lose those styles.
  await rasterizeDisplayPageToBackBuffer(
    canvas,
    fonts.paint(value),
    {
      resolveImage: async (token) => {
        if (!images.has(token)) {
          const source = session!.mediaSource(token);
          if (!source) return null;
          images.set(
            token,
            await createImageBitmap(
              new Blob([source.bytes as Uint8Array<ArrayBuffer>], {
                type: source.mimeType,
              }),
            ),
          );
        }
        return images.get(token)!;
      },
    },
    2,
    1,
  );
  const bitmap = canvas.transferToImageBitmap();
  send(
    {
      kind: "page",
      page: index,
      width: value.width,
      height: value.height,
      bitmap,
      spans: spans[index]!,
    },
    [bitmap],
  );
  for (const image of images.values()) image.close();
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
      session?.destroy();
      session = undefined;
      for (const image of images.values()) image.close();
      images.clear();
      send({ kind: "error" });
    });
};
