import { useViewState, usePreviewScroll } from "../viewState";
import {
  useCallback,
  useEffectEvent,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import type {
  ArtifactAnnotation,
  ArtifactReviewAnchor,
  OfficeView,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Icon, IconButton, cn } from "@openfox/ui";
import type { SelectionRect } from "../selection";
import { utf8Length } from "../anchors";
import { regionFromPoints } from "../regions";
import {
  address,
  cellAt,
  moveCell,
  rangeName,
  selected,
  tracks,
  type Cell,
  type Range,
} from "./geometry";
import type { SheetFrame, SheetReply, SheetRequest } from "./protocol";
const LEFT = 40,
  TOP = 24;
const SHEET_ZOOM_STEPS = [0.5, 0.75, 1, 1.25, 1.5] as const;
const segmentBtn = (active: boolean) =>
  cn(
    "rounded-md px-2 py-0.5 text-caption transition-colors disabled:opacity-50",
    active
      ? "border border-line-subtle bg-surface-card font-medium text-fg-default"
      : "border border-transparent text-fg-secondary hover:text-fg-default",
  );
export function SpreadsheetReview({
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
  onSelect?: (a: ArtifactReviewAnchor, r: SelectionRect) => void;
  onFailure: () => void;
  located?: ArtifactReviewAnchor | null;
  annotations?: ArtifactAnnotation[];
}) {
  const t = useT(),
    sheets = view.spreadsheet!.sheets;
  const [index, setIndex] = useViewState(
    "sheet",
    sheets.find((s) => !s.hidden)?.index ?? 0,
  );
  const indexRef = useRef(index);
  indexRef.current = index;
  const [frame, setFrame] = useState<SheetFrame>();
  const image = useRef<ImageBitmap | undefined>(undefined);
  const [zoom, setZoom] = useViewState<number | "fit">("sheet.zoom", 1);
  const [size, setSize] = useState({ width: 600, height: 480 });
  const scale =
    zoom === "fit"
      ? Math.min(1, Math.max(1, size.width - LEFT) / (frame?.width ?? 640))
      : zoom;
  const [offset, setOffset] = useState({ x: 0, y: 0 });
  const [mode, setMode] = useState<"cells" | "objects">("cells");
  const [selection, setSelection] = useState<Range>();
  const active = useRef<Range | undefined>(undefined);
  const keyboard = useRef<{ anchor: Cell; focus: Cell } | undefined>(undefined);
  const [object, setObject] = useState<ArtifactReviewAnchor>();
  const [error, setError] = useState("");
  const scroll = useRef<HTMLDivElement>(null),
    canvas = useRef<HTMLCanvasElement>(null);
  usePreviewScroll(scroll, Boolean(frame), String(index));
  const request = useRef<(r: SheetRequest) => void>(() => {}),
    failure = useRef(onFailure);
  failure.current = onFailure;
  const down = useRef<{ cell: Cell; axis?: "row" | "col" } | undefined>(
    undefined,
  );
  const cells = useMemo(
    () =>
      new Map(
        view.units
          .filter((u) => u.cell_source?.sheet === index)
          .map((u) => [`${u.cell_source!.row}:${u.cell_source!.column}`, u]),
      ),
    [view, index],
  );
  const byID = useMemo(() => new Map(view.units.map((u) => [u.id, u])), [view]);
  const rowTracks = useMemo(
      () => (frame ? tracks(frame.grid, "row") : []),
      [frame],
    ),
    colTracks = useMemo(
      () => (frame ? tracks(frame.grid, "col") : []),
      [frame],
    );
  const used = useMemo(
    () => ({
      row: Math.max(0, ...[...cells.values()].map((u) => u.cell_source!.row)),
      col: Math.max(
        0,
        ...[...cells.values()].map((u) => u.cell_source!.column),
      ),
    }),
    [cells],
  );
  const selectRange = (r: Range | undefined) => {
    keyboard.current = undefined;
    active.current = r;
    setSelection(r);
  };
  useEffect(() => {
    const el = scroll.current;
    if (!el) return;
    const resize = () =>
      setSize({ width: el.clientWidth, height: el.clientHeight });
    resize();
    const observer = new ResizeObserver(resize);
    observer.observe(el);
    return () => observer.disconnect();
  }, []);
  useEffect(() => {
    let closed = false,
      timer: ReturnType<typeof setTimeout>;
    const worker = new Worker(new URL("./sheet.worker.ts", import.meta.url), {
      type: "module",
    });
    const fail = () => {
      if (closed) return;
      closed = true;
      clearTimeout(timer);
      worker.terminate();
      image.current?.close();
      image.current = undefined;
      failure.current();
    };
    request.current = (r) => {
      if (closed) return;
      clearTimeout(timer);
      timer = setTimeout(fail, 30000);
      worker.postMessage(r, r.kind === "open" ? [r.bytes] : []);
    };
    worker.onerror = fail;
    worker.onmessage = (e: MessageEvent<SheetReply>) => {
      const r = e.data;
      clearTimeout(timer);
      if (closed) {
        if (r.kind === "sheet") r.bitmap.close();
        return;
      }
      if (r.kind === "error") {
        fail();
        return;
      }
      if (r.kind === "ready") {
        request.current({ kind: "sheet", index: indexRef.current });
        return;
      }
      if (r.index !== indexRef.current) {
        r.bitmap.close();
        return;
      }
      image.current?.close();
      image.current = r.bitmap;
      setFrame(r);
    };
    void blob.arrayBuffer().then((bytes) => {
      if (!closed) request.current({ kind: "open", bytes, view });
    }, fail);
    return () => {
      closed = true;
      clearTimeout(timer);
      worker.terminate();
      image.current?.close();
      image.current = undefined;
    };
  }, [blob, view]);
  useEffect(() => {
    down.current = undefined;
    selectRange(undefined);
    setObject(undefined);
    setError("");
  }, [editable, mode]);
  const changeSheet = (next: number) => {
    if (next === index || sheets[next]?.hidden) return;
    indexRef.current = next;
    setIndex(next);
    setFrame(undefined);
    selectRange(undefined);
    setObject(undefined);
    setError("");
    scroll.current?.scrollTo(0, 0);
    setOffset({ x: 0, y: 0 });
    request.current({ kind: "sheet", index: next });
  };
  const rangeBox = useCallback(
    (r: Range) => {
      const rows = rowTracks.filter((v) => v.index >= r.r1 && v.index <= r.r2),
        cols = colTracks.filter((v) => v.index >= r.c1 && v.index <= r.c2);
      if (!rows.length || !cols.length) return null;
      return {
        x: cols[0]!.start,
        y: rows[0]!.start,
        w: cols.at(-1)!.end - cols[0]!.start,
        h: rows.at(-1)!.end - rows[0]!.start,
      };
    },
    [rowTracks, colTracks],
  );
  const anchorRange = useCallback(
    (a: ArtifactReviewAnchor): Range | undefined => {
      if (a.kind === "office") {
        const u = byID.get(a.target ?? "")?.cell_source;
        if (u?.sheet === index)
          return { r1: u.row, r2: u.row, c1: u.column, c2: u.column };
      }
      if (a.kind === "office_range" && a.target === sheets[index]!.part) {
        const match = /^([A-Z]+)([1-9][0-9]*)(?::([A-Z]+)([1-9][0-9]*))?$/.exec(
          a.range ?? "",
        );
        if (match) {
          const col = (s: string) =>
            [...s].reduce((n, c) => n * 26 + c.charCodeAt(0) - 64, 0) - 1;
          return {
            r1: Number(match[2]) - 1,
            c1: col(match[1]!),
            r2: Number(match[4] ?? match[2]) - 1,
            c2: col(match[3] ?? match[1]!),
          };
        }
      }
      return undefined;
    },
    [byID, index, sheets],
  );
  const locateSelection = useEffectEvent(() => {
    if (!located) return;
    const target =
      located.kind === "region"
        ? (located.page ?? 1) - 1
        : located.kind === "office"
          ? byID.get(located.target ?? "")?.cell_source?.sheet
          : sheets.find((s) => s.part === located.target)?.index;
    if (target !== undefined && target !== index) {
      changeSheet(target);
      return;
    }
    if (!frame) return;
    const r = anchorRange(located);
    if (r) {
      selectRange(r);
      const b = rangeBox(r);
      if (b)
        scroll.current?.scrollTo(
          Math.max(0, b.x - frame.frozenWidth) * scale,
          Math.max(0, b.y - frame.frozenHeight) * scale,
        );
    } else if (located.kind === "region") setObject(located);
  });
  useEffect(() => {
    locateSelection();
  }, [located, frame, index, byID, scale]);
  const bands = useMemo(() => {
    if (!frame) return [];
    const vw = Math.max(0, (size.width - LEFT) / scale),
      vh = Math.max(0, (size.height - TOP) / scale),
      fw = Math.min(frame.frozenWidth, vw),
      fh = Math.min(frame.frozenHeight, vh);
    const xs = [
        { src: 0, dst: 0, len: fw },
        { src: frame.frozenWidth + offset.x, dst: fw, len: vw - fw },
      ],
      ys = [
        { src: 0, dst: 0, len: fh },
        { src: frame.frozenHeight + offset.y, dst: fh, len: vh - fh },
      ];
    return xs
      .flatMap((x) =>
        ys.map((y) => ({
          sx: x.src,
          sy: y.src,
          dx: LEFT + x.dst * scale,
          dy: TOP + y.dst * scale,
          w: Math.max(0, Math.min(x.len, frame.width - x.src)),
          h: Math.max(0, Math.min(y.len, frame.height - y.src)),
        })),
      )
      .filter((p) => p.w > 0 && p.h > 0);
  }, [frame, size.width, size.height, scale, offset.x, offset.y]);
  useLayoutEffect(() => {
    const c = canvas.current;
    if (!c) return;
    const dpr = Math.min(2, window.devicePixelRatio || 1);
    c.width = Math.ceil(size.width * dpr);
    c.height = Math.ceil(size.height * dpr);
    const ctx = c.getContext("2d")!;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.fillStyle = "#fff";
    ctx.fillRect(0, 0, size.width, size.height);
    if (!frame || !image.current) return;
    for (const p of bands)
      ctx.drawImage(
        image.current,
        p.sx * 2,
        p.sy * 2,
        p.w * 2,
        p.h * 2,
        p.dx,
        p.dy,
        p.w * scale,
        p.h * scale,
      );
    if (editable || located) {
      const highlights = [
        ...(editable ? annotations.map((a) => a.anchor) : []),
        ...(located ? [located] : []),
        ...(object ? [object] : []),
      ].flatMap((a) => {
        if (a.kind === "region" && a.page === index + 1)
          return [
            {
              x: (a.x ?? 0) * frame.width,
              y: (a.y ?? 0) * frame.height,
              w: (a.width ?? 0) * frame.width,
              h: (a.height ?? 0) * frame.height,
            },
          ];
        const r = anchorRange(a),
          b = r && rangeBox(r);
        return b ? [b] : [];
      });
      const chosen = selection && rangeBox(selection);
      if (chosen) highlights.push(chosen);
      for (const b of highlights)
        for (const p of bands) {
          ctx.save();
          ctx.beginPath();
          ctx.rect(p.dx, p.dy, p.w * scale, p.h * scale);
          ctx.clip();
          ctx.fillStyle = "rgba(37,99,235,.13)";
          ctx.strokeStyle = "#2563eb";
          ctx.lineWidth = 1.5;
          ctx.fillRect(
            p.dx + (b.x - p.sx) * scale,
            p.dy + (b.y - p.sy) * scale,
            b.w * scale,
            b.h * scale,
          );
          ctx.strokeRect(
            p.dx + (b.x - p.sx) * scale,
            p.dy + (b.y - p.sy) * scale,
            b.w * scale,
            b.h * scale,
          );
          ctx.restore();
        }
    }
    ctx.fillStyle = "#f1f5f9";
    ctx.fillRect(0, 0, size.width, TOP);
    ctx.fillRect(0, 0, LEFT, size.height);
    ctx.fillStyle = "#475569";
    ctx.font = "11px sans-serif";
    ctx.textAlign = "center";
    ctx.textBaseline = "middle";
    for (const [values, frozen, shift, horizontal] of [
      [colTracks, frame.frozenWidth, offset.x, true],
      [rowTracks, frame.frozenHeight, offset.y, false],
    ] as const) {
      const fontSize = horizontal ? 11 : Math.min(11, Math.max(8, 11 * scale));
      ctx.font = `${fontSize}px sans-serif`;
      let labelEnd = -Infinity;
      for (const v of values) {
        const fixed = v.start < frozen;
        const start = (v.start - (fixed ? 0 : shift)) * scale;
        const end = (v.end - (fixed ? 0 : shift)) * scale;
        const min = fixed ? 0 : frozen * scale,
          max = horizontal ? size.width - LEFT : size.height - TOP;
        if (end <= min || start >= max) continue;
        const center = (start + end) / 2;
        // At fit-to-width, row heights can be smaller than legible labels.
        // Keep hit areas intact while avoiding overlapping row numbers.
        if (!horizontal && center - fontSize / 2 < labelEnd) continue;
        labelEnd = center + fontSize / 2 + 1;
        ctx.save();
        ctx.beginPath();
        ctx.rect(
          horizontal ? LEFT + min : 0,
          horizontal ? 0 : TOP + min,
          horizontal ? max - min : LEFT,
          horizontal ? TOP : max - min,
        );
        ctx.clip();
        ctx.fillText(
          horizontal
            ? address(0, v.index).replace(/1$/, "")
            : String(v.index + 1),
          horizontal ? LEFT + (start + end) / 2 : LEFT / 2,
          horizontal ? TOP / 2 : TOP + (start + end) / 2,
        );
        ctx.restore();
      }
    }
    ctx.strokeStyle = "#94a3b8";
    ctx.lineWidth = 1;
    if (frame.frozenWidth) {
      const x = LEFT + Math.min(frame.frozenWidth * scale, size.width - LEFT);
      ctx.beginPath();
      ctx.moveTo(x, TOP);
      ctx.lineTo(x, size.height);
      ctx.stroke();
    }
    if (frame.frozenHeight) {
      const y = TOP + Math.min(frame.frozenHeight * scale, size.height - TOP);
      ctx.beginPath();
      ctx.moveTo(LEFT, y);
      ctx.lineTo(size.width, y);
      ctx.stroke();
    }
  }, [
    frame,
    size,
    offset,
    scale,
    editable,
    selection,
    object,
    annotations,
    located,
    index,
    bands,
    anchorRange,
    rangeBox,
    rowTracks,
    colTracks,
  ]);
  const point = (e: { clientX: number; clientY: number }) => {
    const b = canvas.current!.getBoundingClientRect();
    const x = (e.clientX - b.left - LEFT) / scale,
      y = (e.clientY - b.top - TOP) / scale;
    return {
      x:
        x +
        (x >= frame!.frozenWidth
          ? (scroll.current?.scrollLeft ?? 0) / scale
          : 0),
      y:
        y +
        (y >= frame!.frozenHeight
          ? (scroll.current?.scrollTop ?? 0) / scale
          : 0),
      headerX: x < 0,
      headerY: y < 0,
    };
  };
  const comment = (r: Range) => {
    if (!editable || !frame) return;
    const count = (r.r2 - r.r1 + 1) * (r.c2 - r.c1 + 1);
    if (count > 256) {
      setError(t("artifacts.review.selectionLimit"));
      return;
    }
    const rows: string[] = [];
    for (let row = r.r1; row <= r.r2; row++) {
      const values: string[] = [];
      for (let col = r.c1; col <= r.c2; col++)
        values.push(cells.get(`${row}:${col}`)?.text ?? "");
      rows.push(values.join("\t"));
    }
    const quote = rows.join("\n");
    if (utf8Length(quote) > 8192) {
      setError(t("artifacts.review.selectionLimit"));
      return;
    }
    const unit = count === 1 ? cells.get(`${r.r1}:${r.c1}`) : undefined;
    const anchor: ArtifactReviewAnchor = unit?.editable
      ? { kind: "office", target: unit.id, start: 0, end: 0, quote }
      : {
          kind: "office_range",
          target: sheets[index]!.part,
          range: rangeName(r),
          start: 0,
          end: 0,
          quote,
        };
    const b = rangeBox(r),
      screen = canvas.current!.getBoundingClientRect();
    if (!b) return;
    const left =
        screen.left +
        LEFT +
        (b.x - (b.x < frame.frozenWidth ? 0 : offset.x)) * scale,
      top =
        screen.top +
        TOP +
        (b.y - (b.y < frame.frozenHeight ? 0 : offset.y)) * scale;
    onSelect?.(anchor, {
      left: Math.max(screen.left + LEFT, Math.min(screen.right - 20, left)),
      top: Math.max(screen.top + TOP, Math.min(screen.bottom - 20, top)),
      width: Math.min(b.w * scale, size.width - LEFT),
      height: Math.min(b.h * scale, size.height - TOP),
    });
    setError("");
  };
  const pointerRange = (e: React.PointerEvent) => {
    if (!down.current || !frame) return;
    const p = point(e);
    const hit = cellAt(frame.grid, Math.max(0, p.x), Math.max(0, p.y));
    if (!hit) return;
    let a = down.current.cell,
      b = hit;
    if (down.current.axis === "row") {
      a = { row: a.row, col: 0 };
      b = { row: b.row, col: used.col };
    }
    if (down.current.axis === "col") {
      a = { row: 0, col: a.col };
      b = { row: used.row, col: b.col };
    }
    selectRange(selected(a, b, frame.merges));
  };
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="order-last flex shrink-0 flex-wrap items-center gap-2 border-t border-line-subtle bg-surface-card px-3 py-1.5 text-caption text-fg-muted">
        <div className="inline-flex items-center gap-0.5 rounded-full border border-line-subtle bg-surface-muted p-0.5">
          <IconButton
            label={t("artifacts.review.zoomOut")}
            icon={<Icon name="minus" size={14} />}
            size="28"
            className="size-6 rounded-full!"
            disabled={zoom === SHEET_ZOOM_STEPS[0]}
            onClick={() => {
              const current = zoom === "fit" ? scale : zoom;
              const prev = [...SHEET_ZOOM_STEPS]
                .reverse()
                .find((v) => v < current - 0.01);
              setZoom(prev ?? SHEET_ZOOM_STEPS[0]);
              scroll.current?.scrollTo(0, 0);
              setOffset({ x: 0, y: 0 });
            }}
          />
          <select
            aria-label={t("artifacts.review.zoom")}
            className="h-6 cursor-pointer rounded-full bg-transparent px-1.5 text-caption font-medium text-fg-default outline-none"
            value={zoom}
            onChange={(e) => {
              setZoom(
                e.target.value === "fit" ? "fit" : Number(e.target.value),
              );
              scroll.current?.scrollTo(0, 0);
              setOffset({ x: 0, y: 0 });
            }}
          >
            <option value="fit">{t("artifacts.review.fitWidth")}</option>
            {SHEET_ZOOM_STEPS.map((v) => (
              <option key={v} value={v}>
                {v * 100}%
              </option>
            ))}
          </select>
          <IconButton
            label={t("artifacts.review.zoomIn")}
            icon={<Icon name="plus" size={14} />}
            size="28"
            className="size-6 rounded-full!"
            disabled={zoom === SHEET_ZOOM_STEPS[SHEET_ZOOM_STEPS.length - 1]}
            onClick={() => {
              const current = zoom === "fit" ? scale : zoom;
              const next = SHEET_ZOOM_STEPS.find((v) => v > current + 0.01);
              setZoom(next ?? SHEET_ZOOM_STEPS[SHEET_ZOOM_STEPS.length - 1]!);
              scroll.current?.scrollTo(0, 0);
              setOffset({ x: 0, y: 0 });
            }}
          />
        </div>
        <div className="inline-flex items-center gap-0.5 rounded-lg border border-line-subtle bg-surface-muted p-0.5">
          <button
            type="button"
            className={segmentBtn(mode === "cells")}
            disabled={!editable || !frame}
            aria-pressed={mode === "cells"}
            onClick={() => setMode("cells")}
          >
            {t("artifacts.review.sheetCells")}
          </button>
          <button
            type="button"
            className={segmentBtn(mode === "objects")}
            disabled={!editable || !frame}
            aria-pressed={mode === "objects"}
            onClick={() => setMode("objects")}
          >
            {t("artifacts.review.officeSelectObject")}
          </button>
        </div>
        <output
          aria-live="polite"
          className="font-mono text-caption text-fg-muted"
        >
          {selection ? rangeName(selection) : ""}
        </output>
      </div>
      {error && (
        <p role="alert" className="text-body-sm">
          {error}
        </p>
      )}
      {!frame && <p role="status">{t("artifacts.review.loading")}</p>}
      <div
        ref={scroll}
        className="relative min-h-0 flex-1 overflow-auto"
        onScroll={(e) => {
          const el = e.currentTarget;
          setOffset({ x: el.scrollLeft / scale, y: el.scrollTop / scale });
        }}
      >
        <div
          style={{
            width: Math.max(size.width, LEFT + (frame?.width ?? 640) * scale),
            height: Math.max(size.height, TOP + (frame?.height ?? 400) * scale),
          }}
        >
          <canvas
            ref={canvas}
            role="grid"
            aria-label={`${name} · ${sheets[index]?.name}${selection ? ` · ${rangeName(selection)}` : ""}`}
            aria-readonly="true"
            tabIndex={editable ? 0 : -1}
            data-sheet-index={index}
            style={{
              position: "sticky",
              top: 0,
              left: 0,
              display: "block",
              width: size.width,
              height: size.height,
              touchAction: editable ? "none" : "auto",
              cursor: editable
                ? mode === "cells"
                  ? "cell"
                  : "crosshair"
                : "default",
            }}
            onPointerDown={(e) => {
              if (!editable || !frame || e.button !== 0) return;
              e.preventDefault();
              e.currentTarget.focus();
              setError("");
              const p = point(e);
              if (mode === "objects") {
                const hit = [...frame.objects]
                  .reverse()
                  .find(
                    (o) =>
                      p.x >= o.x &&
                      p.x <= o.x + o.w &&
                      p.y >= o.y &&
                      p.y <= o.y + o.h,
                  );
                if (!hit) return;
                const a = regionFromPoints(
                  { x: hit.x / frame.width, y: hit.y / frame.height },
                  {
                    x: (hit.x + hit.w) / frame.width,
                    y: (hit.y + hit.h) / frame.height,
                  },
                  index + 1,
                  0,
                );
                if (a) {
                  setObject(a);
                  const b = e.currentTarget.getBoundingClientRect();
                  onSelect?.(a, {
                    left: e.clientX,
                    top: e.clientY,
                    width: Math.min(hit.w * scale, b.width),
                    height: Math.min(hit.h * scale, b.height),
                  });
                }
                return;
              }
              const hit = cellAt(
                frame.grid,
                Math.max(0, p.x),
                Math.max(0, p.y),
              );
              if (!hit || (p.headerX && p.headerY)) return;
              down.current = {
                cell: hit,
                axis: p.headerX ? "row" : p.headerY ? "col" : undefined,
              };
              pointerRange(e);
              e.currentTarget.setPointerCapture(e.pointerId);
            }}
            onPointerMove={pointerRange}
            onPointerCancel={() => {
              down.current = undefined;
              selectRange(undefined);
            }}
            onPointerUp={(e) => {
              if (!down.current) return;
              pointerRange(e);
              down.current = undefined;
              e.currentTarget.releasePointerCapture(e.pointerId);
              if (active.current) comment(active.current);
            }}
            onKeyDown={(e) => {
              if (!editable || !frame || mode !== "cells") return;
              const r = active.current ?? { r1: 0, c1: 0, r2: 0, c2: 0 };
              if (e.key === "Enter") {
                e.preventDefault();
                comment(r);
                return;
              }
              const delta: { [key: string]: [number, number] } = {
                ArrowDown: [1, 0],
                ArrowUp: [-1, 0],
                ArrowLeft: [0, -1],
                ArrowRight: [0, 1],
              };
              const d = delta[e.key];
              if (!d) return;
              e.preventDefault();
              const current = keyboard.current ?? {
                anchor: { row: r.r1, col: r.c1 },
                focus: {
                  row: d[0] > 0 ? r.r2 : r.r1,
                  col: d[1] > 0 ? r.c2 : r.c1,
                },
              };
              const focus = moveCell(
                current.focus,
                d,
                frame.grid,
                frame.merges,
              );
              const anchor = e.shiftKey ? current.anchor : focus;
              selectRange(selected(anchor, focus, frame.merges));
              keyboard.current = { anchor, focus };
              const box = rangeBox(selected(focus, focus, frame.merges));
              if (box && scroll.current) {
                const reveal = (
                  start: number,
                  length: number,
                  frozen: number,
                  shift: number,
                  viewport: number,
                ) => {
                  if (start < frozen) return shift;
                  if (start < frozen + shift)
                    return Math.max(0, start - frozen);
                  return start + length > shift + viewport
                    ? Math.max(0, start + length - viewport)
                    : shift;
                };
                scroll.current.scrollTo(
                  reveal(
                    box.x,
                    box.w,
                    frame.frozenWidth,
                    offset.x,
                    (size.width - LEFT) / scale,
                  ) * scale,
                  reveal(
                    box.y,
                    box.h,
                    frame.frozenHeight,
                    offset.y,
                    (size.height - TOP) / scale,
                  ) * scale,
                );
              }
            }}
          />
        </div>
      </div>
      <div
        role="tablist"
        aria-label={t("artifacts.review.sheetTabs")}
        className="flex shrink-0 gap-1 overflow-x-auto border-t border-line-subtle bg-surface-muted/60 px-2 py-1"
      >
        {sheets
          .filter((s) => !s.hidden)
          .map((s) => (
            <button
              key={s.index}
              type="button"
              role="tab"
              aria-selected={s.index === index}
              className={segmentBtn(s.index === index)}
              disabled={!frame && s.index !== index}
              onClick={() => changeSheet(s.index)}
            >
              {s.name}
            </button>
          ))}
      </div>
    </div>
  );
}
