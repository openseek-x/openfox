import { lazy, Suspense, useEffect, useMemo, useRef, useState } from "react";
import type {
  ArtifactAnnotation,
  ArtifactReviewAnchor,
  OfficeView,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Popover } from "@openfox/ui";
import type { SelectionRect } from "./selection";
import { VisualReview } from "./VisualReview";
const OfficePages = lazy(() =>
  import("./OfficePages").then((m) => ({ default: m.OfficePages })),
);
const SpreadsheetReview = lazy(() =>
  import("./sheet/SpreadsheetReview").then((m) => ({
    default: m.SpreadsheetReview,
  })),
);

type OfficeReviewProps = {
  view: OfficeView;
  blob: Blob;
  name: string;
  editable?: boolean;
  onSelect?: (anchor: ArtifactReviewAnchor, rect: SelectionRect) => void;
  located?: ArtifactReviewAnchor | null;
  annotations?: ArtifactAnnotation[];
  loadFallback: (signal: AbortSignal) => Promise<Blob>;
};

export function OfficeReview({ loadFallback, ...props }: OfficeReviewProps) {
  const t = useT();
  const [fallback, setFallback] = useState<Blob>();
  const [failed, setFailed] = useState(false);
  const [loadingFallback, setLoadingFallback] = useState(false);
  const live = useRef(true);
  const fallbackRequest = useRef<AbortController | null>(null);
  useEffect(() => {
    live.current = true;
    return () => {
      live.current = false;
      fallbackRequest.current?.abort();
    };
  }, []);
  if (props.view.kind === "xlsx" && !props.view.spreadsheet)
    return <OfficePDFReview {...props} />;
  if (fallback)
    return (
      <VisualReview {...props} blob={fallback} pdf allowTextSelection={false} />
    );
  if (failed) return <p role="alert">{t("artifacts.review.unavailable")}</p>;
  if (loadingFallback)
    return <p role="status">{t("artifacts.review.loading")}</p>;
  const NativeReview =
    props.view.kind === "xlsx" ? SpreadsheetReview : OfficePages;
  return (
    <Suspense fallback={<p>{t("artifacts.review.loading")}</p>}>
      <NativeReview
        {...props}
        onFailure={() => {
          if (fallbackRequest.current) return;
          const request = new AbortController();
          fallbackRequest.current = request;
          setLoadingFallback(true);
          void loadFallback(request.signal).then(
            (value) => {
              if (live.current) setFallback(value);
            },
            () => {
              if (live.current) setFailed(true);
            },
          );
        }}
      />
    </Suspense>
  );
}

// 原文件锚点由用户明确选择；不能用渲染后的同名文字推测其 OOXML 位置。
// 浮层独立于 PDF，开启批注和选择文字都不会替换底层版式。
function OfficePDFReview({
  view,
  editable = false,
  onSelect,
  ...rest
}: {
  view: OfficeView;
  blob: Blob;
  name: string;
  editable?: boolean;
  onSelect?: (anchor: ArtifactReviewAnchor, rect: SelectionRect) => void;
  located?: ArtifactReviewAnchor | null;
  annotations?: ArtifactAnnotation[];
}) {
  const t = useT();
  const sections = useMemo(
    () => [...new Set(view.units.map((u) => u.section))],
    [view],
  );
  const [open, setOpen] = useState(false);
  const [section, setSection] = useState(sections[0] ?? "");
  const [page, setPage] = useState(0);
  const anchor = useRef<HTMLButtonElement>(null);
  const units = view.units.filter((u) => u.section === section);
  const button =
    "rounded border border-line-default px-2 py-1 text-body-sm disabled:opacity-50";
  useEffect(() => {
    if (!editable) setOpen(false);
  }, [editable]);
  return (
    <VisualReview
      allowTextSelection={false}
      {...rest}
      pdf
      editable={editable}
      onSelect={onSelect}
      controls={
        onSelect && (
          <>
            <button
              ref={anchor}
              type="button"
              className={button}
              disabled={!editable}
              aria-expanded={open}
              onClick={() => setOpen((value) => !value)}
            >
              {t("artifacts.review.officeSelectText")}
            </button>
            <Popover
              open={open && editable}
              onOpenChange={setOpen}
              anchorRef={anchor}
              placement="bottom-start"
              role="dialog"
              aria-label={t("artifacts.review.officeContent")}
              className="flex max-h-[60vh] w-80 max-w-[90vw] flex-col gap-2 p-3"
            >
              <p className="text-caption text-fg-muted">
                {t("artifacts.review.officeSelectionHint")}
              </p>
              {sections.length > 1 && (
                <select
                  className="rounded border border-line-default bg-surface-default p-1 text-body-sm"
                  aria-label={t("artifacts.review.officeSection")}
                  value={section}
                  onChange={(e) => {
                    setSection(e.target.value);
                    setPage(0);
                  }}
                >
                  {sections.map((s) => (
                    <option key={s} value={s}>
                      {s}
                    </option>
                  ))}
                </select>
              )}
              <div className="min-h-0 overflow-auto">
                {units.slice(page * 50, (page + 1) * 50).map((unit) => (
                  <button
                    key={unit.id}
                    type="button"
                    data-office-target={unit.id}
                    disabled={!unit.editable}
                    className="flex w-full flex-col gap-1 border-b border-line-default p-2 text-left text-body-sm hover:bg-surface-hover disabled:opacity-50"
                    onClick={() => {
                      if (!editable) return;
                      const rect = anchor.current?.getBoundingClientRect();
                      if (!rect) return;
                      setOpen(false);
                      onSelect?.(
                        {
                          kind: "office",
                          target: unit.id,
                          start: 0,
                          end: 0,
                          quote: unit.text,
                        },
                        rect,
                      );
                    }}
                  >
                    <span className="text-caption text-fg-muted">
                      {unit.section} · {unit.label}
                      {!unit.editable &&
                        ` · ${t("artifacts.review.officeReadOnly")}`}
                    </span>
                    <span className="whitespace-pre-wrap break-words">
                      {unit.text || t("artifacts.review.officeBlank")}
                    </span>
                  </button>
                ))}
                {!units.length && (
                  <p className="text-body-sm">
                    {t("artifacts.review.officeEmpty")}
                  </p>
                )}
              </div>
              {units.length > 50 && (
                <div className="flex items-center gap-2">
                  <button
                    className={button}
                    disabled={page === 0}
                    onClick={() => setPage((value) => value - 1)}
                  >
                    {t("artifacts.review.previous")}
                  </button>
                  <span>
                    {page + 1} / {Math.ceil(units.length / 50)}
                  </span>
                  <button
                    className={button}
                    disabled={(page + 1) * 50 >= units.length}
                    onClick={() => setPage((value) => value + 1)}
                  >
                    {t("artifacts.review.next")}
                  </button>
                </div>
              )}
            </Popover>
          </>
        )
      }
    />
  );
}
