import { useViewState, usePreviewScroll } from "./viewState";
import { Button, Icon, IconButton, cn } from "@openfox/ui";
import { PreviewNavigation } from "./PreviewNavigation";
import {
  useEffectEvent,
  lazy,
  Suspense,
  useEffect,
  useRef,
  useState,
} from "react";
import type { ArtifactAnnotation, ArtifactReviewAnchor } from "@openfox/api";
import type { SelectionRect } from "./selection";
import { useT } from "@openfox/i18n";
import { displayedRegion, regionFromPoints, type Point } from "./regions";
const PDFPage = lazy(() => import("./PDFPage"));
const ZOOM_STEPS = [0.5, 1, 1.5, 2] as const;

export function VisualReview({
  blob,
  name,
  pdf = false,
  editable = false,
  annotations = [],
  located,
  onSelect,
  controls,
  allowTextSelection = true,
}: {
  allowTextSelection?: boolean;
  controls?: React.ReactNode;
  blob: Blob;
  name: string;
  pdf?: boolean;
  editable?: boolean;
  annotations?: ArtifactAnnotation[];
  located?: ArtifactReviewAnchor | null;
  onSelect?: (a: ArtifactReviewAnchor, rect: SelectionRect) => void;
}) {
  const t = useT();
  const [url, setURL] = useState("");
  const [textMode, setTextMode] = useState(allowTextSelection);
  const [page, setPage] = useViewState("visual.pageNumber", 1);
  const [pages, setPages] = useState(1);
  const [rotation, setRotation] = useViewState("visual.rotation", 0);
  const [angle, setAngle] = useState(0);
  const [zoom, setZoom] = useViewState("visual.zoom", 1);
  const [ready, setReady] = useState(false);
  const [failed, setFailed] = useState(false);
  const [draft, setDraft] = useState<ArtifactReviewAnchor | null>(null);
  const start = useRef<Point | null>(null);
  const stage = useRef<HTMLDivElement>(null);
  const scroll = useRef<HTMLDivElement>(null);
  usePreviewScroll(scroll, ready, String(page));
  useEffect(() => {
    if (pdf) return;
    const u = URL.createObjectURL(blob);
    setURL(u);
    return () => URL.revokeObjectURL(u);
  }, [blob, pdf]);
  useEffect(() => {
    setDraft(null);
    start.current = null;
  }, [page, rotation, editable]);
  const locatePage = useEffectEvent(() => {
    if (located?.kind !== "region" && located?.kind !== "pdf_text") return;
    if ((located.page ?? 1) !== page) setReady(false);
    setPage(located.page ?? 1);
    setDraft(located);
    stage.current?.scrollIntoView?.({ block: "nearest" });
  });
  useEffect(() => {
    locatePage();
  }, [located]);
  const point = (e: React.PointerEvent<HTMLDivElement>) => {
    const b = e.currentTarget.getBoundingClientRect();
    return {
      x: Math.max(0, Math.min(1, (e.clientX - b.left) / b.width)),
      y: Math.max(0, Math.min(1, (e.clientY - b.top) / b.height)),
    };
  };
  const totalAngle = pdf ? angle : 0;
  const highlighted = editable ? draft : located;
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      {pdf && (
        <PreviewNavigation>
          <IconButton
            size="28"
            className="rounded-full!"
            disabled={page <= 1}
            onClick={() => {
              setReady(false);
              setPage(page - 1);
            }}
            label={t("artifacts.review.previousPage")}
            icon={<Icon name="chevron-left" size={15} />}
          />
          <span>
            {page} / {pages}
          </span>
          <IconButton
            size="28"
            className="rounded-full!"
            disabled={page >= pages}
            onClick={() => {
              setReady(false);
              setPage(page + 1);
            }}
            label={t("artifacts.review.nextPage")}
            icon={<Icon name="chevron-right" size={15} />}
          />
        </PreviewNavigation>
      )}
      <div className="order-last flex shrink-0 flex-wrap items-center gap-2 border-t border-line-subtle bg-surface-card px-3 py-1.5 text-caption text-fg-muted">
        {controls}
        {pdf && editable && allowTextSelection && (
          <Button
            size="28"
            variant="outline"
            onClick={() => setTextMode(!textMode)}
          >
            {t(
              textMode
                ? "artifacts.review.pdfRegionMode"
                : "artifacts.review.pdfTextMode",
            )}
          </Button>
        )}
        {pdf && (
          <Button
            size="28"
            variant="outline"
            onClick={() => {
              setReady(false);
              setRotation((rotation + 90) % 360);
            }}
          >
            <Icon name="rotate-cw" size={14} />
            {t("artifacts.review.rotate")}
          </Button>
        )}
        <div className="inline-flex items-center gap-0.5 rounded-full border border-line-subtle bg-surface-muted p-0.5">
          <IconButton
            label={t("artifacts.review.zoomOut")}
            icon={<Icon name="minus" size={14} />}
            size="28"
            className="size-6 rounded-full!"
            disabled={zoom <= ZOOM_STEPS[0]}
            onClick={() => {
              const prev = [...ZOOM_STEPS].reverse().find((z) => z < zoom);
              if (prev) setZoom(prev);
            }}
          />
          <select
            aria-label={t("artifacts.review.zoom")}
            value={zoom}
            onChange={(e) => setZoom(Number(e.target.value))}
            className="h-6 cursor-pointer rounded-full bg-transparent px-1.5 text-caption font-medium text-fg-default outline-none"
          >
            {ZOOM_STEPS.map((z) => (
              <option key={z} value={z}>
                {z * 100}%
              </option>
            ))}
          </select>
          <IconButton
            label={t("artifacts.review.zoomIn")}
            icon={<Icon name="plus" size={14} />}
            size="28"
            className="size-6 rounded-full!"
            disabled={zoom >= ZOOM_STEPS[ZOOM_STEPS.length - 1]!}
            onClick={() => {
              const next = ZOOM_STEPS.find((z) => z > zoom);
              if (next) setZoom(next);
            }}
          />
        </div>
        {onSelect && (
          <Button
            size="28"
            variant="outline"
            disabled={!editable || !ready}
            className={cn(!editable && "opacity-50")}
            onClick={(e) => {
              const a = regionFromPoints(
                { x: 0, y: 0 },
                { x: 1, y: 1 },
                page,
                0,
              )!;
              setDraft(a);
              onSelect?.(a, e.currentTarget.getBoundingClientRect());
            }}
          >
            {t("artifacts.review.wholePage")}
          </Button>
        )}
      </div>
      {failed && <p role="alert">{t("artifacts.review.unavailable")}</p>}
      <div ref={scroll} className="min-h-0 flex-1 overflow-auto p-4">
        <div
          className="mx-auto"
          style={{
            width: `${zoom * 100}%`,
            maxWidth: pdf ? `${816 * zoom}px` : undefined,
          }}
        >
          <div
            ref={stage}
            className="relative select-none"
            style={{ touchAction: editable ? "none" : "auto" }}
            onPointerDown={(e) => {
              if (!editable || !ready || e.button !== 0 || (pdf && textMode))
                return;
              e.preventDefault();
              e.currentTarget.setPointerCapture(e.pointerId);
              start.current = point(e);
              setDraft(null);
            }}
            onPointerMove={(e) => {
              if (start.current)
                setDraft(
                  regionFromPoints(start.current, point(e), page, totalAngle),
                );
            }}
            onPointerUp={(e) => {
              if (!editable || !start.current) return;
              const a = regionFromPoints(
                start.current,
                point(e),
                page,
                totalAngle,
              );
              start.current = null;
              e.currentTarget.releasePointerCapture(e.pointerId);
              setDraft(a);
              if (a)
                onSelect?.(a, {
                  left: e.clientX,
                  top: e.clientY,
                  width: 0,
                  height: 0,
                });
            }}
            onPointerCancel={() => {
              start.current = null;
              setDraft(null);
            }}
          >
            {pdf ? (
              <Suspense fallback={<p>{t("artifacts.review.loading")}</p>}>
                <PDFPage
                  editable={editable}
                  textMode={textMode}
                  onSelect={(a, rect) => {
                    setDraft(a);
                    onSelect?.(a, rect);
                  }}
                  annotations={editable ? annotations : []}
                  located={
                    editable && draft?.kind === "pdf_text" ? draft : located
                  }
                  blob={blob}
                  page={page}
                  rotation={rotation}
                  onPages={setPages}
                  onRotation={setAngle}
                  onReady={setReady}
                />
              </Suspense>
            ) : (
              url && (
                <img
                  src={url}
                  alt={name}
                  draggable={false}
                  className="block w-full"
                  style={{ imageOrientation: "from-image" }}
                  onLoad={(e) => {
                    const image = e.currentTarget;
                    const ok =
                      image.naturalWidth * image.naturalHeight <= 40_000_000;
                    setReady(ok);
                    setFailed(!ok);
                  }}
                  onError={() => {
                    setReady(false);
                    setFailed(true);
                  }}
                />
              )
            )}
            {ready &&
              editable &&
              annotations
                .filter(
                  (a) =>
                    a.anchor.kind === "region" && (a.anchor.page ?? 1) === page,
                )
                .map((a, i) => (
                  <div
                    key={a.id}
                    aria-label={a.body}
                    className={`pointer-events-none absolute border-2 border-amber-500`}
                    style={displayedRegion(a.anchor, totalAngle)}
                  >
                    <span className="bg-amber-500 px-1 text-xs text-black">
                      {i + 1}
                    </span>
                  </div>
                ))}
            {ready &&
              highlighted?.kind === "region" &&
              (highlighted.page ?? 1) === page && (
                <div
                  className="pointer-events-none absolute border-2 border-blue-500 bg-blue-500/15"
                  data-located-region={Boolean(!editable && located)}
                  style={displayedRegion(highlighted, totalAngle)}
                />
              )}
          </div>
        </div>
      </div>
    </div>
  );
}
