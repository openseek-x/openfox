// Stage-six investigation, run only by run.cjs with pinned, isolated dependencies.
// Word retained-layout API sequence: OpenCode (MIT), see NOTICE.md.
import { createYrsSession } from "/deps/docx/dist/yrs/index.mjs";
import {
  buildResidentRegionLayoutRequest,
  getLayoutKernelInputs,
  workerLayoutComputation,
} from "/deps/docx/dist/editor/index.mjs";
import { createRustMeasureSource } from "/deps/docx/dist/layout/index.mjs";
import {
  applyFrameDelta,
  decodeFrameDelta,
  encodeDisplayListFrameExtras,
  rasterizeDisplayPageToBackBuffer,
} from "/deps/docx/dist/layout/render/index.mjs";
import * as pptx from "/deps/pptx/dist/index.js";
import * as xlsx from "/deps/xlsx/dist/index.js";
import {
  createFontProvider,
  loadBundledFontBytes,
  resolveMetricCompatFace,
  resolveBundledFamilyFace,
  resolveScriptFallbackFace,
  registerBundledFontFace,
} from "/deps/fonts/dist/index.js";

const bytes = async (url) =>
  new Uint8Array(await (await fetch(url)).arrayBuffer());
const clear = () => document.getElementById("pages").replaceChildren();
const canvas = (width, height) => {
  const c = document.createElement("canvas");
  c.width = Math.ceil(width);
  c.height = Math.ceil(height);
  c.style.display = "block";
  c.style.margin = "12px";
  document.getElementById("pages").append(c);
  return c;
};

async function word(raw) {
  const session = await createYrsSession();
  const images = new Map();
  try {
    const document = session.openDocx(raw, true, {
      mediaTokens: true,
    }).document;
    const provider = createFontProvider({ baseUrl: "/fonts/" });
    const fonts = new Map();
    const measure = createRustMeasureSource({
      engine: session,
      bundled: {
        ...provider,
        resolve: (family, bold, italic) => {
          const face =
            resolveBundledFamilyFace(family, bold, italic) ??
            resolveMetricCompatFace(family, bold, italic) ??
            resolveBundledFamilyFace(family, false, false);
          if (!face) return provider.resolve(family, bold, italic);
          fonts.set(`${family}:${face.file}`, { family, face });
          return () => loadBundledFontBytes(face, { baseUrl: "/fonts/" });
        },
      },
    });
    measure.setCompat(document.package.settings?.compatibilityFlags);
    const request = buildResidentRegionLayoutRequest(document, 0, {
      themeColors: document.package.theme?.colorScheme ?? {},
      defaultTabStopTwips: document.package.settings?.defaultTabStop ?? null,
      numericIds: {},
      mediaTokens: true,
    });
    const requirements = JSON.parse(
      session.layoutFontRequirementsJson(JSON.stringify(request)),
    );
    await measure.prepareFontRequirements(requirements);
    await Promise.all(
      [...fonts.values()].map(({ family, face }) =>
        registerBundledFontFace(face, family, { baseUrl: "/fonts/" }),
      ),
    );
    const measurement = measure.measurementConfigForRequirements(requirements);
    if (!measurement) throw Error("Missing actual font metrics");
    const input = JSON.stringify({ ...request, measurement });
    const layout = workerLayoutComputation(
      session.layoutDocumentWithRegionsRetainedJson(input),
    ).layout;
    const extras = encodeDisplayListFrameExtras({
      measured: [],
      layout,
      fontChains: measurement.fontChains,
      headersFooters: getLayoutKernelInputs(layout)?.headersFooters,
      resolvedCommentIds: [],
    });
    const retained = applyFrameDelta(
      null,
      decodeFrameDelta(session.buildDisplayListFrame(extras, 0)),
    );
    const resolveImage = async (token) => {
      if (!images.has(token)) {
        const source = session.mediaSource(token);
        if (!source) throw Error(`Missing image token: ${token}`);
        const blob = new Blob([source.bytes], { type: source.mimeType });
        images.set(token, await createImageBitmap(blob));
      }
      return images.get(token);
    };
    for (const page of retained.displayList.pages) {
      const surface = new OffscreenCanvas(1, 1);
      await rasterizeDisplayPageToBackBuffer(
        surface,
        page,
        { resolveImage },
        1,
        1,
      );
      canvas(surface.width, surface.height)
        .getContext("2d")
        .drawImage(surface, 0, 0);
    }
    return {
      pages: retained.displayList.pages,
      requirements,
      identities: session.paragraphIdentities(),
      mapped: session.exportStructuredWithPagesFor(
        {
          revisionView: "markup",
          stories: ["body", "headers", "footers"],
          includeGeometry: true,
        },
        input,
      ),
    };
  } finally {
    for (const image of images.values()) image.close();
    session.destroy();
  }
}

async function slides(raw) {
  await pptx.initWasm();
  const cjk = resolveScriptFallbackFace("cjk-sc", false, false);
  const latin = resolveMetricCompatFace("Arial", false, false);
  const [cjkBytes, latinBytes] = await Promise.all(
    [cjk, latin].map((face) =>
      loadBundledFontBytes(face, { baseUrl: "/fonts/" }),
    ),
  );
  // Keep the original family names while using documented open-font substitutes.
  const fonts = ["PingFang SC", "Noto Sans CJK SC", "Noto Sans SC"].map(
    (family) => ({ family, bytes: new Uint8Array(cjkBytes) }),
  );
  fonts.push({ family: "Arial", bytes: new Uint8Array(latinBytes) });
  for (const item of fonts) {
    const font = new FontFace(item.family, item.bytes);
    await font.load();
    document.fonts.add(font);
  }
  const deck = pptx.openPresentation(raw, { fonts, fallbackFonts: [fonts[0]] });
  const images = new Map();
  try {
    const model = deck.snapshot();
    const frames = [];
    const hits = [];
    for (let index = 0; index < model.slides.length; index++) {
      const frame = deck.layoutSlide(index);
      frames.push(frame);
      const target = canvas(frame.width, frame.height);
      await pptx.paintSlide(target.getContext("2d"), frame, 1, 1, {
        resolveImage: async (id) => {
          if (!images.has(id))
            images.set(
              id,
              await pptx.decodePresentationImage(
                deck.mediaBytes(id),
                `Image ${id} unavailable`,
              ),
            );
          return images.get(id);
        },
      });
      hits.push(deck.hitTest(frame.width / 2, frame.height / 2));
    }
    return {
      model,
      frames,
      hits,
      mapped: deck.exportStructured({ includeFormatting: true }),
    };
  } finally {
    for (const image of images.values()) image.close?.();
    deck.dispose();
  }
}

async function sheets(raw) {
  await xlsx.initWasm();
  const cjk = await bytes("/fonts/NotoSansSC-Regular.otf");
  const face = new FontFace("Noto Sans SC", cjk);
  await face.load();
  document.fonts.add(face);
  const workbook = xlsx.openWorkbook(raw);
  try {
    const frames = [],
      info = [],
      picks = [];
    for (let i = 0; i < workbook.sheetCount(); i++) {
      workbook.setActiveSheet(i);
      info.push(workbook.sheetInfo());
      const frame = workbook.displayList({
        x: 0,
        y: 0,
        width: 1280,
        height: 720,
      });
      frames.push(frame);
      xlsx.paintDisplayList(canvas(1280, 720).getContext("2d"), frame, 1);
      const rect = workbook.cellRect(i, 2, 3);
      picks.push({
        rect,
        hit: xlsx.cellAtPoint(
          frame.grid,
          rect.x + rect.w / 2,
          rect.y + rect.h / 2,
        ),
      });
    }
    return { info, frames, picks, mapped: workbook.exportStructured() };
  } finally {
    workbook.dispose();
  }
}

window.renderOffice = async (name, kind) => {
  clear();
  const started = performance.now();
  const raw = await bytes(`/fixtures/${name}`);
  const result = await { docx: word, pptx: slides, xlsx: sheets }[kind](raw);
  return {
    ...result,
    elapsedMs: performance.now() - started,
    inputBytes: raw.length,
  };
};
window.ready = true;
