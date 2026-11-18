import {
  createFontProvider,
  loadBundledFontBytes,
  resolveBundledFamilyFace,
  resolveMetricCompatFace,
  resolveLastResortFace,
  resolveScriptFallbackFace,
  resolveGlyphFallbackFace,
  type BundledFontFace,
  type BundledFontScript,
} from "@betteroffice/fonts";
import type {
  DisplayPage,
  DisplayPrimitive,
} from "@betteroffice/docx/layout/render";

// These authored CJK family names resolve to the same open substitute used
// throughout measurement and paint; they are not aliases in fonts 0.4.2.
export function officeFontFamily(family: string) {
  if (family === "PingFang SC" || family === "Noto Sans CJK SC")
    return "Noto Sans SC";
  if (family === "Noto Serif CJK SC") return "Noto Serif SC";
  return family;
}

// The resident measurement registry calls resolve, not resolveFamily. A bundled
// family must be a primary face, or Latin fallback metrics compress CJK rows.
export function wordFonts() {
  const provider = createFontProvider();
  const pending = new Map<string, Promise<void>>();
  const aliases = new WeakMap<ArrayBuffer, string>();
  const registered = new Map<number, string>();
  const loader = (face: BundledFontFace | undefined) =>
    face
      ? async () => {
          const bytes = await loadBundledFontBytes(face);
          const family = `OpenFox Office ${face.file}`;
          aliases.set(bytes, family);
          if (!pending.has(family)) {
            pending.set(
              family,
              (async () => {
                const font = new FontFace(family, bytes, {
                  weight: String(face.weight),
                  style: face.style,
                });
                await font.load();
                (
                  self as unknown as { fonts: { add(font: FontFace): void } }
                ).fonts.add(font);
              })(),
            );
          }
          await pending.get(family);
          return bytes;
        }
      : undefined;
  return {
    registered(id: number, bytes: Uint8Array) {
      const alias = aliases.get(bytes.buffer as ArrayBuffer);
      if (alias) registered.set(id, alias);
      return id;
    },
    substitute(id: number, original: number) {
      const alias = registered.get(original);
      if (alias) registered.set(id, alias);
      return id;
    },
    paint(page: DisplayPage): DisplayPage {
      const primitives = (values: DisplayPrimitive[]) =>
        values.map((p) => {
          if (p.kind !== "glyphRun") return p;
          const alias = registered.get(p.fontId);
          if (!alias) throw Error("Unregistered paint font");
          const prefix =
            p.fallbackFont?.match(/^.*?\d+(?:\.\d+)?px(?:\/\S+)?\s+/)?.[0] ??
            `${p.size}px `;
          return { ...p, fallbackFont: `${prefix}"${alias}"` };
        });
      return {
        ...page,
        primitives: primitives(page.primitives),
        header: page.header
          ? { ...page.header, primitives: primitives(page.header.primitives) }
          : undefined,
        footer: page.footer
          ? { ...page.footer, primitives: primitives(page.footer.primitives) }
          : undefined,
      };
    },
    provider: {
      ...provider,
      resolve(family: string, bold: boolean, italic: boolean) {
        family = officeFontFamily(family);
        const face =
          resolveBundledFamilyFace(family, bold, italic) ??
          resolveMetricCompatFace(family, bold, italic) ??
          resolveBundledFamilyFace(family, false, false) ??
          resolveLastResortFace(family, bold, italic, "word");
        return loader(face);
      },
      resolveLastResort: (family: string, bold: boolean, italic: boolean) =>
        loader(resolveLastResortFace(family, bold, italic, "word"))!,
      resolveScriptFallback: (
        script: BundledFontScript,
        bold: boolean,
        italic: boolean,
      ) => loader(resolveScriptFallbackFace(script, bold, italic)),
      resolveGlyphFallback: (family: string, bold: boolean, italic: boolean) =>
        loader(resolveGlyphFallbackFace(family, bold, italic, "word")),
    },
  };
}
