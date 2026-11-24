import {
  defaultTreeAdapter as tree,
  parse,
  parseFragment,
  serializeOuter,
  type DefaultTreeAdapterMap,
} from "parse5";
import type { ArtifactReviewAnchor } from "@openfox/api";
import { sourceOffsets } from "./selection";
import { textAnchor } from "./anchors";

// Reuse the static HTML allowlist from the artifact-preview work. Never mount
// serialized artifact markup in the workbench DOM. The iframe remains opaque.
const TAGS = new Set(
  "html head body title a abbr address article aside b bdi bdo blockquote br button caption cite code col colgroup dd del details dfn div dl dt em fieldset figcaption figure footer form h1 h2 h3 h4 h5 h6 header hgroup hr i img input ins kbd label legend li main mark nav ol optgroup option p pre q rp rt ruby s samp section select small span strong style sub summary sup table tbody td textarea tfoot th thead time tr u ul var wbr".split(
    " ",
  ),
);
const ATTRIBUTES = new Set(
  "alt checked class colspan datetime dir disabled height hidden id lang max maxlength min multiple name open placeholder readonly required reversed role rows rowspan selected size span start step style title type value width wrap".split(
    " ",
  ),
);
const VOID = new Set("br col hr img input wbr".split(" "));
const UNSELECTABLE = new Set("html head body title style".split(" "));
const RASTER_DATA =
  /^data:image\/(?:png|jpeg|gif|webp);base64,[a-z0-9+/=\s]+$/i;
const MARKER = "data-openfox-review";
const KEYBOARD_TARGETS = new Set(
  "p h1 h2 h3 h4 h5 h6 li pre td th img figcaption".split(" "),
);
const HTML_NS = "http://www.w3.org/1999/xhtml";
export const HTML_MAX_BYTES = 512 * 1024;
export const HTML_MAX_NODES = 10000;
export const HTML_MAX_DEPTH = 128;
type Node = DefaultTreeAdapterMap["childNode"];
export type HtmlTextTarget = { path: string; start: number; offsets: number[] };

export type HtmlTarget = {
  id: string;
  tag: string;
  anchor: ArtifactReviewAnchor;
};

// parse5 offsets index the original JavaScript string. Convert once, preserving
// BOM, CRLF, entities and astral characters in the actual UTF-8 patch coordinates.
function byteOffsets(source: string) {
  const offsets = new Uint32Array(source.length + 1);
  let bytes = 0;
  for (let i = 0; i < source.length; i++) {
    offsets[i] = bytes;
    const code = source.codePointAt(i)!;
    if (code > 0xffff) offsets[++i] = bytes;
    bytes += code > 0xffff ? 4 : code > 0x7ff ? 3 : code > 0x7f ? 2 : 1;
  }
  offsets[source.length] = bytes;
  return offsets;
}

// An external local asset also satisfies the parent page's script-src 'self'.
// A srcdoc inline nonce alone cannot relax the inherited parent CSP.
const bridgeURL = new URL(
  "./htmlSelectionBridge.js",
  import.meta.url,
).toString();

export function htmlPreviewDocument(
  source: string,
  session?: string,
): { document: string; targets: HtmlTarget[]; textTargets: HtmlTextTarget[] } {
  if (
    source.length > HTML_MAX_BYTES ||
    new TextEncoder().encode(source).length > HTML_MAX_BYTES
  )
    throw new Error("HTML preview size limit");
  if (session && !/^[a-zA-Z0-9_-]{16,80}$/.test(session))
    throw new Error("Invalid preview session");
  let malformed = false;
  const bom = source.startsWith("\ufeff") ? 1 : 0;
  const doc = parse(source.slice(bom), {
    sourceCodeLocationInfo: true,
    onParseError: (error) => {
      if (error.code !== "missing-doctype") malformed = true;
    },
  });
  const offsets = byteOffsets(source);
  const targets: HtmlTarget[] = [];
  const spans = new Map<string, number>();
  let count = 0;
  const clean = (nodes: Node[], depth: number): Node[] => {
    if (depth > HTML_MAX_DEPTH) throw new Error("HTML preview depth limit");
    return nodes.filter((node) => {
      if (++count > HTML_MAX_NODES) throw new Error("HTML preview node limit");
      if (node.nodeName === "#text") return true;
      if (
        !tree.isElementNode(node) ||
        node.namespaceURI !== HTML_NS ||
        !TAGS.has(node.tagName)
      )
        return false;
      node.attrs = node.attrs.filter(
        (attr) =>
          !attr.namespace &&
          ((node.tagName === "img" &&
            attr.name === "src" &&
            RASTER_DATA.test(attr.value)) ||
            ATTRIBUTES.has(attr.name) ||
            attr.name.startsWith("aria-") ||
            (attr.name.startsWith("data-") &&
              !attr.name.startsWith("data-openfox-"))),
      );
      if (["button", "input", "select", "textarea"].includes(node.tagName))
        node.attrs.push({ name: "disabled", value: "" });
      const loc = node.sourceCodeLocation;
      if (
        !malformed &&
        !UNSELECTABLE.has(node.tagName) &&
        loc?.startTag &&
        (loc.endTag || VOID.has(node.tagName))
      ) {
        const start = offsets[loc.startOffset + bom]!,
          end = offsets[loc.endOffset + bom]!;
        if (end > start && end - start <= 8192) {
          const id = `e${targets.length}`;
          targets.push({
            id,
            tag: node.tagName,
            anchor: {
              kind: "text",
              start,
              end,
              quote: source.slice(loc.startOffset + bom, loc.endOffset + bom),
            },
          });
          const span = `${start}:${end}`;
          spans.set(span, (spans.get(span) ?? 0) + 1);
          node.attrs.push({ name: MARKER, value: id });
          if (session && KEYBOARD_TARGETS.has(node.tagName))
            node.attrs.push({ name: "tabindex", value: "0" });
        }
      }
      node.childNodes = clean(node.childNodes, depth + 1);
      return true;
    });
  };
  doc.childNodes = clean(doc.childNodes, 0);
  const root = doc.childNodes.find(tree.isElementNode)!;
  const head = root.childNodes.find(
    (n) => tree.isElementNode(n) && n.tagName === "head",
  ) as DefaultTreeAdapterMap["element"];
  const policy = `default-src 'none'; script-src ${session ? `'nonce-${session}'` : "'none'"}; script-src-attr 'none'; style-src 'unsafe-inline'; img-src data:; font-src 'none'; connect-src 'none'; frame-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'`;
  head.childNodes.unshift(
    ...parseFragment(
      `<meta charset="utf-8"><meta http-equiv="Content-Security-Policy" content="${policy}"><meta name="referrer" content="no-referrer"><meta name="viewport" content="width=device-width,initial-scale=1"><style>*{scrollbar-width:thin}html{color-scheme:light}body{margin:16px;color:#171717;background:#fff}img{max-width:100%;height:auto}</style>`,
    ).childNodes,
  );
  if (session) {
    const script = tree.createElement("script", root.namespaceURI, [
      { name: "nonce", value: session },
      { name: "src", value: bridgeURL },
    ]);
    tree.appendChild(root, script);
  }
  const textTargets: HtmlTextTarget[] = [];
  const collect = (node: Node, path: number[]) => {
    if (tree.isTextNode(node) && node.sourceCodeLocation && !malformed) {
      const start = node.sourceCodeLocation.startOffset + bom;
      const end = node.sourceCodeLocation.endOffset + bom;
      const offsets = sourceOffsets(source.slice(start, end), node.value);
      if (offsets) textTargets.push({ path: path.join("."), start, offsets });
    }
    if (
      tree.isElementNode(node) &&
      !["head", "script", "style", "textarea"].includes(node.tagName)
    )
      node.childNodes.forEach((child, i) => collect(child, [...path, i]));
  };
  collect(root, []);
  return {
    textTargets,
    document: `<!doctype html>${serializeOuter(root)}`,
    // Parser repairs can clone formatting nodes. Ambiguous ranges are never
    // returned to the host, even if an inert marker remains in the preview.
    targets: targets.filter(
      ({ anchor: a }) => spans.get(`${a.start}:${a.end}`) === 1,
    ),
  };
}

export function htmlSelection(
  event: MessageEvent,
  frame: Window | null,
  session: string,
  targets: HtmlTarget[],
) {
  const message: unknown = event.data;
  if (
    !frame ||
    event.source !== frame ||
    event.origin !== "null" ||
    typeof message !== "object" ||
    message === null
  )
    return undefined;
  const m = message as Record<string, unknown>;
  if (
    m.type !== "openfox-html-select" ||
    m.session !== session ||
    typeof m.id !== "string" ||
    m.id.length > 6 ||
    Object.keys(m).some((k) => !["type", "session", "id", "rect"].includes(k))
  )
    return undefined;
  return targets.find((target) => target.id === m.id)?.anchor;
}

export function htmlTextSelection(
  event: MessageEvent,
  frame: Window | null,
  session: string,
  targets: HtmlTextTarget[],
  source: string,
) {
  if (!frame || event.source !== frame || event.origin !== "null") return null;
  const m = event.data;
  if (!m || m.type !== "openfox-html-text" || m.session !== session)
    return null;
  const endpoint = (value: unknown) => {
    if (!value || typeof value !== "object") return null;
    const { path, offset } = value as { path?: unknown; offset?: unknown };
    if (
      typeof path !== "string" ||
      path.length > 1024 ||
      !Number.isInteger(offset) ||
      typeof offset !== "number" ||
      offset < 0
    )
      return null;
    const target = targets.find((t) => t.path === path);
    const raw = target?.offsets[offset];
    return target && raw !== undefined && raw >= 0 ? target.start + raw : null;
  };
  const start = endpoint(m.start),
    end = endpoint(m.end);
  return start === null || end === null ? null : textAnchor(source, start, end);
}
