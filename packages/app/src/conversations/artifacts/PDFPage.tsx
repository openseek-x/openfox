import { useEffect, useRef, useState } from "react";
import {
  AnnotationMode,
  TextLayer,
  GlobalWorkerOptions,
  getDocument,
  type PDFDocumentProxy,
} from "pdfjs-dist";
import { useT } from "@openfox/i18n";
import type { ArtifactReviewAnchor, ArtifactAnnotation } from "@openfox/api";
import { pdfTextAnchor, pdfQuadPoints, type PDFViewport } from "./pdfGeometry";
import type { SelectionRect } from "./selection";
import "./pdfTextLayer.css";

// Rspack emits the worker as a separate local asset; no CDN or document URLs.
GlobalWorkerOptions.workerSrc = new URL(
  "pdfjs-dist/build/pdf.worker.min.mjs",
  import.meta.url,
).toString();

export default function PDFPage({
  blob,
  page,
  rotation,
  onPages,
  onRotation,
  onReady,
  editable = false,
  textMode = true,
  onSelect,
  annotations = [],
  located,
}: {
  editable?: boolean;
  textMode?: boolean;
  onSelect?: (a: ArtifactReviewAnchor, rect: SelectionRect) => void;
  annotations?: ArtifactAnnotation[];
  located?: ArtifactReviewAnchor | null;
  blob: Blob;
  page: number;
  rotation: number;
  onPages: (n: number) => void;
  onRotation: (r: number) => void;
  onReady: (ready: boolean) => void;
}) {
  const t = useT();
  const host = useRef<HTMLDivElement>(null);
  const [pdf, setPDF] = useState<PDFDocumentProxy | null>(null);
  const [error, setError] = useState(false);
  const [geometry, setGeometry] = useState<PDFViewport | null>(null);
  const [notes, setNotes] = useState<
    { id: string; body: string; rect: number[] }[]
  >([]);
  const [note, setNote] = useState("");
  const [textUnavailable, setTextUnavailable] = useState(false);
  const textHost = useRef<HTMLDivElement | null>(null);
  const loading = useRef<ReturnType<typeof getDocument> | null>(null);
  // Callback refs prevent loading/rendering again when the annotation list changes.
  const callbacks = useRef({ onPages, onRotation, onReady });
  callbacks.current = { onPages, onRotation, onReady };
  useEffect(() => {
    let disposed = false;
    let task: ReturnType<typeof getDocument> | undefined;
    setError(false);
    setPDF(null);
    callbacks.current.onReady(false);
    const deadline = setTimeout(() => {
      if (!disposed) {
        setError(true);
        void task?.destroy();
      }
    }, 30_000);
    void blob
      .arrayBuffer()
      .then(async (data) => {
        if (disposed) return;
        task = getDocument({
          data: new Uint8Array(data),
          enableXfa: false,
          useWorkerFetch: false,
          maxImageSize: 16_000_000,
          canvasMaxAreaInBytes: 32_000_000,
          disableAutoFetch: true,
          disableStream: true,
          stopAtErrors: true,
          cMapUrl: "/static/pdfjs/cmaps/",
          cMapPacked: true,
          standardFontDataUrl: "/static/pdfjs/standard_fonts/",
          wasmUrl: "/static/pdfjs/wasm/",
        });
        loading.current = task;
        // Password-protected files are explicitly unsupported; never leave a pending prompt.
        task.onPassword = () => {
          if (!disposed) setError(true);
          void task?.destroy();
        };
        const doc = await task.promise;
        if (disposed) return;
        if (doc.numPages > 1000) throw new Error("PDF page limit");
        callbacks.current.onPages(doc.numPages);
        setPDF(doc);
      })
      .catch(() => {
        if (!disposed) setError(true);
        void task?.destroy();
      })
      .finally(() => clearTimeout(deadline));
    return () => {
      disposed = true;
      clearTimeout(deadline);
      void task?.destroy();
    };
  }, [blob]);
  useEffect(() => {
    if (!pdf || !host.current) return;
    let canceled = false;
    let layer: TextLayer | undefined;
    let observer: ResizeObserver | undefined;
    setGeometry(null);
    setNotes([]);
    setNote("");
    setTextUnavailable(false);
    let renderTask:
      | ReturnType<Awaited<ReturnType<PDFDocumentProxy["getPage"]>>["render"]>
      | undefined;
    let timeout: ReturnType<typeof setTimeout>;
    const canvas = document.createElement("canvas");
    canvas.style.width = "100%";
    canvas.style.display = "block";
    const text = document.createElement("div");
    text.className = "openfox-pdf-text";
    textHost.current = text;
    host.current.replaceChildren(canvas, text);
    callbacks.current.onReady(false);
    setError(false);
    timeout = setTimeout(() => {
      canceled = true;
      renderTask?.cancel();
      setError(true);
      void loading.current?.destroy();
    }, 30_000);
    void pdf
      .getPage(page)
      .then(async (p) => {
        if (canceled) {
          p.cleanup();
          return;
        }
        const angle = (p.rotate + rotation) % 360;
        callbacks.current.onRotation(angle);
        const original = p.getViewport({ scale: 1, rotation: angle });
        const scale = Math.min(
          2,
          2048 / Math.max(original.width, original.height),
          Math.sqrt(4_000_000 / (original.width * original.height)),
        );
        const viewport = p.getViewport({ scale, rotation: angle });
        if (
          !Number.isFinite(viewport.width) ||
          viewport.width < 1 ||
          viewport.height < 1
        )
          throw new Error("Invalid page dimensions");
        canvas.width = Math.ceil(viewport.width);
        canvas.height = Math.ceil(viewport.height);
        renderTask = p.render({
          canvas,
          viewport,
          annotationMode: AnnotationMode.ENABLE,
        });
        try {
          await renderTask.promise;
          if (canceled) return;
          setGeometry(viewport);
          // Appearance streams are painted by PDF.js; text notes are plain,
          // read-only UI. Never activate embedded JS, links, attachments or forms.
          const native = await p.getAnnotations({ intent: "display" });
          if (canceled) return;
          setNotes(
            native
              .slice(0, 500)
              .filter((a) => a.contentsObj?.str && Array.isArray(a.rect))
              .map((a) => ({
                id: String(a.id),
                body: String(a.contentsObj.str).slice(0, 8192),
                rect: a.rect,
              })),
          );
          try {
            const content = await p.getTextContent();
            if (canceled) return;
            if (
              content.items.length > 20000 ||
              content.items.reduce(
                (n, item) => n + ("str" in item ? item.str.length : 0),
                0,
              ) > 1_000_000
            )
              throw new Error("PDF text limit");
            text.style.setProperty(
              "--total-scale-factor",
              String(viewport.scale * viewport.userUnit),
            );
            layer = new TextLayer({
              textContentSource: content,
              container: text,
              viewport,
            });
            await layer.render();
            if (canceled) {
              layer.cancel();
              return;
            }
            const resize = () => {
              if (host.current)
                text.style.transform = `scale(${host.current.clientWidth / viewport.width}) ${({ 0: "", 90: "rotate(90deg) translateY(-100%)", 180: "rotate(180deg) translate(-100%, -100%)", 270: "rotate(270deg) translateX(-100%)" } as Record<number, string>)[angle]}`;
            };
            observer = new ResizeObserver(resize);
            observer.observe(host.current!);
            resize();
            if (!content.items.length) setTextUnavailable(true);
          } catch {
            if (!canceled) {
              text.replaceChildren();
              setTextUnavailable(true);
            }
          }
          if (!canceled) callbacks.current.onReady(true);
        } finally {
          p.cleanup();
        }
      })
      .catch(() => {
        if (!canceled) setError(true);
      })
      .finally(() => clearTimeout(timeout));
    return () => {
      canceled = true;
      clearTimeout(timeout);
      renderTask?.cancel();
      layer?.cancel();
      TextLayer.cleanup();
      observer?.disconnect();
      text.remove();
      canvas.remove();
      textHost.current = null;
    };
  }, [pdf, page, rotation]);
  const select = () => {
    const selection = window.getSelection(),
      root = textHost.current;
    if (
      !editable ||
      !textMode ||
      !geometry ||
      !root ||
      !selection ||
      selection.isCollapsed ||
      selection.rangeCount !== 1
    )
      return;
    const range = selection.getRangeAt(0);
    if (
      !root.contains(range.startContainer) ||
      !root.contains(range.endContainer)
    )
      return;
    // Text-node rects avoid duplicate container rects and spanning empty lines.
    const rects: DOMRect[] = [];
    const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
    let node: Node | null;
    while ((node = walker.nextNode())) {
      if (!range.intersectsNode(node)) continue;
      const part = document.createRange();
      part.selectNodeContents(node);
      if (node === range.startContainer) part.setStart(node, range.startOffset);
      if (node === range.endContainer) part.setEnd(node, range.endOffset);
      rects.push(...part.getClientRects());
      if (rects.length > 256) break;
    }
    const anchor = pdfTextAnchor(
      selection.toString(),
      rects,
      host.current!.getBoundingClientRect(),
      geometry,
      page,
    );
    if (anchor) onSelect?.(anchor, range.getBoundingClientRect());
  };
  const marks = [
    ...annotations.map((a) => a.anchor),
    ...(located ? [located] : []),
  ].filter(
    (a, index, all) =>
      a.kind === "pdf_text" &&
      a.page === page &&
      all.findIndex((other) => JSON.stringify(other) === JSON.stringify(a)) ===
        index,
  );
  return (
    <div className="relative">
      {error && <p role="alert">{t("artifacts.review.pdfFailed")}</p>}
      {editable && textMode && textUnavailable && (
        <p className="absolute bottom-14 left-4 z-10 rounded bg-surface-card px-2 py-1 text-caption text-fg-muted">
          {t("artifacts.review.pdfNoText")}
        </p>
      )}
      <div className="relative" style={{ isolation: "isolate" }}>
        <div
          ref={host}
          data-pdf-page="true"
          onMouseUp={select}
          className={error ? "hidden" : "relative min-h-24"}
          style={{ pointerEvents: textMode ? "auto" : "none" }}
        />
        {geometry && !error && (
          <>
            <svg
              aria-hidden="true"
              viewBox="0 0 100 100"
              preserveAspectRatio="none"
              className="pointer-events-none absolute inset-0 h-full w-full"
            >
              {marks.flatMap((a, i) =>
                (a.quads ?? []).map((q, j) => (
                  <polygon
                    key={`${i}:${j}`}
                    points={pdfQuadPoints(q, geometry)}
                    fill="rgba(59,130,246,0.22)"
                    stroke="#3b82f6"
                    strokeWidth="0.15"
                  />
                )),
              )}
            </svg>
            {notes.map((n) => {
              const [x, y] = geometry.convertToViewportPoint(
                n.rect[0]!,
                n.rect[3]!,
              );
              return (
                <button
                  key={n.id}
                  type="button"
                  title={n.body}
                  aria-label={t("artifacts.review.pdfExistingNote")}
                  className="absolute rounded bg-amber-200 px-1 text-xs text-black shadow"
                  style={{
                    left: `${Math.min(96, Math.max(0, (x! / geometry.width) * 100))}%`,
                    top: `${Math.min(96, Math.max(0, (y! / geometry.height) * 100))}%`,
                  }}
                  onPointerDown={(e) => e.stopPropagation()}
                  onClick={() => setNote(note === n.body ? "" : n.body)}
                >
                  ▤
                </button>
              );
            })}
          </>
        )}
      </div>
      {note && (
        <div
          role="note"
          className="absolute left-2 right-2 top-2 z-20 max-h-40 overflow-auto whitespace-pre-wrap break-words rounded border border-line-default bg-surface-card p-2 text-body-sm shadow-lg"
          onPointerDown={(e) => e.stopPropagation()}
        >
          {note}
          <button type="button" className="ml-2" onClick={() => setNote("")}>
            {t("common.close")}
          </button>
        </div>
      )}
    </div>
  );
}
