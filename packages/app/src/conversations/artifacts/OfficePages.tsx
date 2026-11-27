import { useViewState, usePreviewScroll } from "./viewState";
import { Icon, IconButton } from "@openfox/ui";
import { PreviewNavigation } from "./PreviewNavigation";
import { useEffect, useMemo, useRef, useState } from "react";
import type {
  ArtifactAnnotation,
  ArtifactReviewAnchor,
  OfficeView,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import type { SelectionRect } from "./selection";
import type {
  OfficePageReply,
  OfficePageRequest,
  OfficeTextSpan,
  OfficeObject,
} from "./officePageProtocol";
import { textAnchor, utf8Length } from "./anchors";
import { displayedRegion, regionFromPoints } from "./regions";

const button =
  "rounded border border-line-default px-2 py-1 text-body-sm disabled:opacity-50";
export function OfficePages({
  blob,
  view,
  name,
  editable = false,
  onSelect,
  onFailure,
  located,
  annotations = [],
}: {
  blob: Blob;
  view: OfficeView;
  name: string;
  editable?: boolean;
  onSelect?: (anchor: ArtifactReviewAnchor, rect: SelectionRect) => void;
  onFailure: () => void;
  located?: ArtifactReviewAnchor | null;
  annotations?: ArtifactAnnotation[];
}) {
  const t = useT();
  const units = useMemo(
    () => new Map(view.units.map((unit) => [unit.id, unit])),
    [view],
  );
  const canvas = useRef<HTMLCanvasElement>(null);
  const stage = useRef<HTMLDivElement>(null);
  const scroll = useRef<HTMLDivElement>(null);
  const worker = useRef<Worker | null>(null);
  const failure = useRef(onFailure);
  failure.current = onFailure;
  const targets = useRef<Record<string, number>>({});
  const request = useRef<(request: OfficePageRequest) => void>(() => {});
  const requested = useRef(0);
  const [count, setCount] = useState(0);
  const [index, setIndex] = useViewState("office.pageIndex", 0);
  const [frame, setFrame] = useState<{
    width: number;
    height: number;
    spans: OfficeTextSpan[];
    objects?: OfficeObject[];
  }>();
  const [ready, setReady] = useState(false);
  const [zoom, setZoom] = useViewState<number | "fit">("office.zoom", "fit");
  usePreviewScroll(scroll, ready, String(index));
  const [available, setAvailable] = useState(612);
  const scale =
    zoom === "fit" ? Math.min(1, available / (frame?.width ?? 816)) : zoom;
  const [region, setRegion] = useState(false);
  const [objects, setObjects] = useState(false);
  const [draftRegion, setDraftRegion] = useState<ArtifactReviewAnchor | null>(
    null,
  );
  const [error, setError] = useState("");
  const down = useRef<{ x: number; y: number } | null>(null);
  useEffect(() => {
    const element = scroll.current;
    if (!element) return;
    const resize = () => setAvailable(Math.max(1, element.clientWidth - 32));
    resize();
    const observer = new ResizeObserver(resize);
    observer.observe(element);
    return () => observer.disconnect();
  }, []);
  useEffect(() => {
    let closed = false;
    requested.current = 0;
    targets.current = {};
    setCount(0);
    setReady(false);
    let timer: ReturnType<typeof setTimeout>;
    const current =
      view.kind === "pptx"
        ? new Worker(new URL("./ppt/ppt.worker.ts", import.meta.url), {
            type: "module",
          })
        : new Worker(new URL("./word/word.worker.ts", import.meta.url), {
            type: "module",
          });
    worker.current = current;
    const fail = () => {
      if (closed) return;
      closed = true;
      clearTimeout(timer);
      current.terminate();
      failure.current();
    };
    request.current = (message) => {
      if (closed) return;
      clearTimeout(timer);
      timer = setTimeout(fail, 30_000);
      current.postMessage(
        message,
        message.kind === "open" ? [message.bytes] : [],
      );
    };
    current.onerror = fail;
    current.onmessage = (event: MessageEvent<OfficePageReply>) => {
      const reply = event.data;
      clearTimeout(timer);
      if (closed) {
        if (reply.kind === "page") reply.bitmap.close();
        return;
      }
      if (reply.kind === "error") {
        fail();
        return;
      }
      if (reply.kind === "ready") {
        setCount(reply.pages);
        targets.current = reply.targets;
        const restored = Math.max(0, Math.min(index, reply.pages - 1));
        setIndex(restored);
        requested.current = restored;
        request.current({ kind: "page", page: restored });
        return;
      }
      if (reply.page !== requested.current || !canvas.current) {
        reply.bitmap.close();
        return;
      }
      canvas.current.width = reply.bitmap.width;
      canvas.current.height = reply.bitmap.height;
      canvas.current.getContext("2d")!.drawImage(reply.bitmap, 0, 0);
      reply.bitmap.close();
      setFrame({
        width: reply.width,
        height: reply.height,
        spans: reply.spans,
        objects: reply.objects,
      });
      setReady(true);
    };
    void blob.arrayBuffer().then((bytes) => {
      if (!closed) request.current({ kind: "open", bytes, view });
    }, fail);
    return () => {
      closed = true;
      clearTimeout(timer);
      current.terminate();
      worker.current = null;
    };
  }, [blob, view]);
  const go = (page: number) => {
    if (page < 0 || page >= count || page === requested.current) return;
    requested.current = page;
    setIndex(page);
    setReady(false);
    setError("");
    setDraftRegion(null);
    window.getSelection()?.removeAllRanges();
    request.current({ kind: "page", page });
  };
  useEffect(() => {
    down.current = null;
    setError("");
    setDraftRegion(null);
    if (!editable) window.getSelection()?.removeAllRanges();
  }, [editable, region, objects]);
  useEffect(() => {
    if (!located || !count) return;
    const page =
      located.kind === "office"
        ? targets.current[located.target ?? ""]
        : (located.page ?? 1) - 1;
    if (page !== undefined) go(page);
    stage.current?.scrollIntoView({ block: "nearest" });
  }, [located, count]);
  const choose = (
    target: string,
    start: number,
    end: number,
    rect: SelectionRect,
  ) => {
    if (!editable || !ready) return;
    const unit = units.get(target);
    if (!unit?.editable || start < 0 || end <= start || end > unit.text.length)
      return;
    const anchor = textAnchor(unit.text, start, end);
    if (!anchor) {
      setError(t("artifacts.review.selectionLimit"));
      return;
    }
    onSelect?.({ ...anchor, kind: "office", target }, rect);
    setError("");
  };
  const select = (event: React.MouseEvent<HTMLDivElement>) => {
    if (!editable || !ready || region || objects) return;
    const selection = window.getSelection();
    if (!selection || selection.isCollapsed || selection.rangeCount !== 1)
      return;
    const range = selection.getRangeAt(0);
    if (!event.currentTarget.contains(range.commonAncestorContainer)) return;
    // Chromium may express an exclusive end as offset 0 of the NEXT span.
    // Read only nonempty selected pieces, so that boundary does not move the
    // annotation to the next OOXML run (or the next repeated paragraph).
    const parts = [
      ...event.currentTarget.querySelectorAll<HTMLElement>(
        "[data-office-target]",
      ),
    ].flatMap((element) => {
      const node = element.firstChild;
      if (!node || !range.intersectsNode(node)) return [];
      const text = node.textContent ?? "";
      let start = -1,
        end = -1;
      for (let at = 0; at <= text.length; at++)
        if (range.isPointInRange(node, at)) {
          if (start < 0) start = at;
          end = at;
        }
      if (start < 0 || end <= start) return [];
      const offset = Number(element.dataset.officeOffset);
      return [
        {
          target: element.dataset.officeTarget!,
          start: offset + start,
          end: offset + end,
          text: text.slice(start, end),
        },
      ];
    });
    const first = parts[0],
      last = parts.at(-1);
    if (
      !first ||
      !last ||
      parts.some(
        (p, i) =>
          p.target !== first.target || (i > 0 && p.start !== parts[i - 1]!.end),
      ) ||
      parts.map((p) => p.text).join("") !== range.toString()
    ) {
      setError(t("artifacts.review.officeSelectionUnmapped"));
      return;
    }
    choose(first.target, first.start, last.end, range.getBoundingClientRect());
  };
  const bounds = (e: React.PointerEvent) => {
    const box = stage.current!.getBoundingClientRect();
    return {
      x: Math.max(0, Math.min(1, (e.clientX - box.left) / box.width)),
      y: Math.max(0, Math.min(1, (e.clientY - box.top) / box.height)),
    };
  };
  const seenTargets = new Set<string>();
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PreviewNavigation>
        <IconButton
          size="28"
          className="rounded-full!"
          disabled={!ready || index === 0}
          onClick={() => go(index - 1)}
          label={t("artifacts.review.previousPage")}
          icon={<Icon name="chevron-left" size={15} />}
        />
        <span>
          {count ? index + 1 : 0} / {count}
        </span>
        <IconButton
          size="28"
          className="rounded-full!"
          disabled={!ready || index + 1 >= count}
          onClick={() => go(index + 1)}
          label={t("artifacts.review.nextPage")}
          icon={<Icon name="chevron-right" size={15} />}
        />
      </PreviewNavigation>
      <div className="order-last flex shrink-0 flex-wrap items-center gap-2 border-t border-line-default px-3 py-1.5 text-caption text-fg-muted">
        <select
          className={button}
          aria-label={t("artifacts.review.zoom")}
          value={zoom}
          onChange={(e) =>
            setZoom(e.target.value === "fit" ? "fit" : Number(e.target.value))
          }
        >
          <option value="fit">{t("artifacts.review.fitWidth")}</option>
          {[0.5, 0.75, 1, 1.25, 1.5].map((v) => (
            <option key={v} value={v}>
              {v * 100}%
            </option>
          ))}
        </select>
        <button
          className={button}
          disabled={!editable || !ready}
          aria-pressed={!region && !objects}
          onClick={() => {
            setRegion(false);
            setObjects(false);
          }}
        >
          {t("artifacts.review.officeSelectText")}
        </button>
        {view.kind === "pptx" && (
          <button
            className={button}
            disabled={!editable || !ready}
            aria-pressed={objects}
            onClick={() => {
              setObjects(true);
              setRegion(false);
            }}
          >
            {t("artifacts.review.officeSelectObject")}
          </button>
        )}
        <button
          className={button}
          disabled={!editable || !ready}
          aria-pressed={region}
          onClick={() => {
            setRegion(true);
            setObjects(false);
          }}
        >
          {t("artifacts.browser.region")}
        </button>
      </div>
      {error && (
        <p role="alert" className="text-body-sm">
          {error}
        </p>
      )}
      {!ready && <p role="status">{t("artifacts.review.loading")}</p>}
      <div ref={scroll} className="min-h-0 flex-1 overflow-auto p-4">
        <div
          ref={stage}
          className="relative mx-auto bg-white shadow-card"
          style={{
            width: (frame?.width ?? 816) * scale,
            height: (frame?.height ?? 1056) * scale,
            touchAction: editable && region ? "none" : "auto",
          }}
          onMouseUp={select}
          onPointerDown={(e) => {
            if (editable && ready && region && e.button === 0) {
              e.preventDefault();
              down.current = bounds(e);
              setDraftRegion(null);
              e.currentTarget.setPointerCapture(e.pointerId);
            }
          }}
          onPointerMove={(e) => {
            if (down.current)
              setDraftRegion(
                regionFromPoints(down.current, bounds(e), index + 1, 0),
              );
          }}
          onPointerCancel={() => {
            down.current = null;
            setDraftRegion(null);
          }}
          onPointerUp={(e) => {
            const start = down.current;
            down.current = null;
            if (!editable || !ready || !region || !start) return;
            const anchor = regionFromPoints(start, bounds(e), index + 1, 0);
            setDraftRegion(anchor);
            e.currentTarget.releasePointerCapture(e.pointerId);
            if (!anchor) return;
            const { x = 0, y = 0, width = 0, height = 0 } = anchor;
            const box = e.currentTarget.getBoundingClientRect();
            onSelect?.(anchor, {
              left: box.left + x * box.width,
              top: box.top + y * box.height,
              width: width * box.width,
              height: height * box.height,
            });
          }}
        >
          <canvas
            ref={canvas}
            role="img"
            aria-label={name}
            style={{ display: "block", width: "100%", height: "100%" }}
          />
          {(editable || located?.kind === "office") &&
            ready &&
            !region &&
            !objects &&
            frame?.spans.map((span, i) => {
              const first = !seenTargets.has(span.target);
              seenTargets.add(span.target);
              const unit = units.get(span.target)!;
              const start = utf8Length(unit.text.slice(0, span.offset));
              const end = start + utf8Length(span.text);
              const highlighted = (a: ArtifactReviewAnchor) =>
                a.kind === "office" &&
                a.target === span.target &&
                ((a.start === 0 && a.end === 0) ||
                  (a.start < end && start < a.end));
              return (
                <span
                  key={i}
                  tabIndex={editable && first ? 0 : undefined}
                  role={editable && first ? "button" : undefined}
                  aria-label={
                    first
                      ? `${t("artifacts.review.annotate")} ${unit.text}`
                      : undefined
                  }
                  onKeyDown={(e) => {
                    if (e.key === "Enter" || e.key === " ") {
                      e.preventDefault();
                      choose(
                        unit.id,
                        0,
                        unit.text.length,
                        e.currentTarget.getBoundingClientRect(),
                      );
                    }
                  }}
                  data-office-target={span.target}
                  data-office-offset={span.offset}
                  style={{
                    position: "absolute",
                    left: span.x * scale,
                    top: span.y * scale,
                    width: span.width * scale,
                    height: span.height * scale,
                    fontSize: span.size * scale,
                    lineHeight: `${span.height * scale}px`,
                    color: "transparent",
                    whiteSpace: "pre",
                    userSelect: editable ? "text" : "none",
                    pointerEvents: editable ? "auto" : "none",
                    cursor: editable ? "text" : "default",
                    background:
                      (located && highlighted(located)) ||
                      (editable &&
                        annotations.some((a) => highlighted(a.anchor)))
                        ? "rgba(255,214,64,.18)"
                        : undefined,
                  }}
                >
                  {span.text}
                </span>
              );
            })}
          {editable &&
            ready &&
            objects &&
            frame?.objects?.map((object, i) => (
              <button
                key={i}
                type="button"
                data-office-object={i}
                aria-label={`${t("artifacts.review.annotate")} ${object.name}`}
                className="absolute hover:outline hover:outline-2 hover:outline-blue-500 focus-visible:outline-2 focus-visible:outline-blue-500"
                style={{
                  left: object.x * scale,
                  top: object.y * scale,
                  width: object.width * scale,
                  height: object.height * scale,
                }}
                onClick={(e) => {
                  const anchor = regionFromPoints(
                    { x: object.x / frame.width, y: object.y / frame.height },
                    {
                      x: (object.x + object.width) / frame.width,
                      y: (object.y + object.height) / frame.height,
                    },
                    index + 1,
                    0,
                  );
                  if (anchor) {
                    setDraftRegion(anchor);
                    onSelect?.(anchor, e.currentTarget.getBoundingClientRect());
                  }
                }}
              />
            ))}
          {editable &&
            [
              ...annotations.map((a) => a.anchor),
              ...(draftRegion ? [draftRegion] : []),
              ...(located?.kind === "region" ? [located] : []),
            ]
              .filter((a) => a.kind === "region" && a.page === index + 1)
              .map((anchor, i) => (
                <div
                  key={i}
                  className="pointer-events-none absolute border-2 border-blue-500 bg-blue-500/10"
                  style={displayedRegion(anchor, 0)}
                />
              ))}
          <div className="sr-only">
            {view.units.map((u) => u.text).join("\n")}
          </div>
        </div>
      </div>
    </div>
  );
}
