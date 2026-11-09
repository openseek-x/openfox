import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
  type PointerEvent,
} from "react";

import type { Task } from "@openfox/api";
import { useI18n, useT } from "@openfox/i18n";
import { Button, Icon, Spinner, cn } from "@openfox/ui";

import { useTaskReadOnly } from "../../spaces/readonly";
import { displayIdOf } from "../parts";
import { formatMonthDay, todayYmd } from "../primitives/dates";
import { StatusIcon } from "../primitives/StatusIcon";
import { useTaskPages, useUpdateTask } from "../queries";
import type { TaskViewProps } from "../views";
import {
  DAY_WIDTH,
  MAX_GANTT_TASKS,
  applyDrag,
  barRange,
  dayOfMonth,
  dragPatch,
  fromDay,
  monthCells,
  sortByStart,
  tickCells,
  timelineRange,
  toDay,
  type BarRange,
  type DragMode,
  type GanttScale,
} from "./model";

const ROW_HEIGHT = "var(--density-gantt-row,36px)";
const LEFT_WIDTH = 240;
const HEADER_HEIGHT = 56;
const HANDLE = 6;
const CLOSED = new Set(["DONE", "CANCELLED", "DUPLICATE"]);
const SCALES: GanttScale[] = ["day", "week", "month"];

interface DragState {
  task: Task;
  mode: DragMode;
  startX: number;
  delta: number;
  moved: boolean;
}

/**
 * 甘特图：左侧任务列，右侧按日 / 周 / 月刻度的时间轴；
 * 拖动条整体平移、拖动两端改开始 / 截止日期，提交走与其他视图相同的 updateTask（带 version 乐观锁）。
 */
export function Gantt({
  orgId,
  source,
  params,
  display,
  onOpen,
}: TaskViewProps) {
  const t = useT();
  const { locale } = useI18n();
  const [scale, setScale] = useState<GanttScale>("week");
  const [byStart, setByStart] = useState(true);
  const [showCompleted, setShowCompleted] = useState(
    display.completed !== "hide",
  );
  const ganttParams = useMemo(
    () => ({
      ...params,
      completed: showCompleted ? undefined : ("hide" as const),
    }),
    [params, showCompleted],
  );
  const pages = useTaskPages(orgId, source, ganttParams, null);
  const update = useUpdateTask(orgId);
  const loaded = useMemo(
    () => pages.data?.pages.flatMap((p) => p.tasks) ?? [],
    [pages.data],
  );
  const { hasNextPage, isFetchingNextPage, fetchNextPage } = pages;
  // 一直取到上限；超过上限时提示缩小筛选范围
  useEffect(() => {
    if (hasNextPage && !isFetchingNextPage && loaded.length < MAX_GANTT_TASKS)
      void fetchNextPage();
  }, [hasNextPage, isFetchingNextPage, loaded.length, fetchNextPage]);
  const truncated = Boolean(hasNextPage) && loaded.length >= MAX_GANTT_TASKS;

  const tasks = useMemo(() => {
    const list = loaded.slice(0, MAX_GANTT_TASKS);
    return byStart ? sortByStart(list) : list;
  }, [loaded, byStart]);

  const today = toDay(todayYmd())!;
  const ranges = useMemo(() => tasks.map((task) => barRange(task)), [tasks]);
  const range = useMemo(
    () =>
      timelineRange(
        ranges.filter((r): r is BarRange => r !== null),
        today,
        scale,
      ),
    [ranges, today, scale],
  );
  const dayWidth = DAY_WIDTH[scale];
  const days = range.end - range.start + 1;
  const width = days * dayWidth;

  const scroller = useRef<HTMLDivElement | null>(null);
  const goToday = useCallback(() => {
    const el = scroller.current;
    if (!el) return;
    el.scrollLeft = Math.max(
      0,
      (today - range.start) * dayWidth - (el.clientWidth - LEFT_WIDTH) / 2,
    );
  }, [today, range.start, dayWidth]);
  // 首次出现数据和切换刻度时把今天放到中间
  const centered = useRef<string>("");
  useEffect(() => {
    if (loaded.length === 0 && pages.isLoading) return;
    if (centered.current === scale) return;
    centered.current = scale;
    goToday();
  }, [scale, loaded.length, pages.isLoading, goToday]);

  const [drag, setDrag] = useState<DragState | null>(null);
  const readOnly = useTaskReadOnly(orgId, tasks[0]?.space_id);
  const dragRef = useRef<DragState | null>(null);

  const commit = (task: Task, mode: DragMode, delta: number) => {
    const base = barRange(task);
    if (!base || delta === 0) return;
    const patch = dragPatch(task, applyDrag(base, mode, delta), mode);
    if (patch) update.mutate({ task, patch });
  };

  const onPointerDown = (
    event: PointerEvent<HTMLDivElement>,
    task: Task,
    rangeOfTask: BarRange,
  ) => {
    if (readOnly || event.button !== 0) return;
    const rect = event.currentTarget.getBoundingClientRect();
    const barWidth = (rangeOfTask.end - rangeOfTask.start + 1) * dayWidth;
    const edge = Math.min(HANDLE, barWidth / 3);
    const x = event.clientX - rect.left;
    const mode: DragMode =
      x <= edge ? "start" : x >= rect.width - edge ? "end" : "move";
    event.currentTarget.setPointerCapture?.(event.pointerId);
    const next = { task, mode, startX: event.clientX, delta: 0, moved: false };
    dragRef.current = next;
    setDrag(next);
  };
  /**
   * 取消拖拽：预览立即消失、不提交。凡是没有收到条形上的 pointerup 就结束的拖拽
   * （丢失指针捕获、窗口失焦、按 Esc、松手事件落在别处）都走这里。
   */
  const cancelDrag = useCallback(() => {
    if (!dragRef.current) return;
    dragRef.current = null;
    setDrag(null);
  }, []);
  const dragging = drag !== null;
  useEffect(() => {
    if (!dragging) return;
    const onKey = (event: globalThis.KeyboardEvent) => {
      if (event.key === "Escape") cancelDrag();
    };
    // 条形上的 onPointerUp 先于 window 执行并已清空 dragRef；走到这里说明松手没有送达条形
    const onRelease = () => cancelDrag();
    window.addEventListener("keydown", onKey);
    window.addEventListener("blur", cancelDrag);
    window.addEventListener("pointerup", onRelease);
    window.addEventListener("pointercancel", onRelease);
    return () => {
      window.removeEventListener("keydown", onKey);
      window.removeEventListener("blur", cancelDrag);
      window.removeEventListener("pointerup", onRelease);
      window.removeEventListener("pointercancel", onRelease);
    };
  }, [dragging, cancelDrag]);

  const onPointerMove = (event: PointerEvent<HTMLDivElement>) => {
    const current = dragRef.current;
    if (!current) return;
    // 鼠标已经没有按键按下却还在拖：松手事件丢了，按取消处理
    if (event.pointerType === "mouse" && event.buttons === 0) {
      cancelDrag();
      return;
    }
    const dx = event.clientX - current.startX;
    const next = {
      ...current,
      delta: Math.round(dx / dayWidth),
      moved: current.moved || Math.abs(dx) > 3,
    };
    // React can batch the last pointermove with pointerup. Commit must read
    // the latest pointer position even before the preview has rendered.
    dragRef.current = next;
    setDrag(next);
  };
  const onPointerUp = (event: PointerEvent<HTMLDivElement>) => {
    const current = dragRef.current;
    if (!current) return;
    // 先清空 ref：随后到达的 lostpointercapture 与 window 上的 pointerup 不再当作取消
    dragRef.current = null;
    event.currentTarget.releasePointerCapture?.(event.pointerId);
    setDrag(null);
    if (!current.moved) onOpen(current.task);
    else commit(current.task, current.mode, current.delta);
  };
  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>, task: Task) => {
    if (event.key === "Enter") {
      event.preventDefault();
      onOpen(task);
      return;
    }
    if (readOnly) return;
    const step =
      event.key === "ArrowLeft" ? -1 : event.key === "ArrowRight" ? 1 : 0;
    if (!step) return;
    event.preventDefault();
    commit(task, event.shiftKey ? "end" : "move", step);
  };

  const months = useMemo(() => monthCells(range), [range]);
  const ticks = useMemo(() => tickCells(range, scale), [range, scale]);
  const monthLabel = (day: number) =>
    new Intl.DateTimeFormat(locale, {
      year: "numeric",
      month: "long",
      timeZone: "UTC",
    }).format(new Date(day * 86_400_000));
  const scheduled = ranges.filter(Boolean).length;
  const todayLeft = (today - range.start) * dayWidth + dayWidth / 2;

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div
        role="toolbar"
        aria-label={t("tasks.gantt.label")}
        className="flex items-center gap-2 px-4 pb-2"
      >
        <div
          role="radiogroup"
          aria-label={t("tasks.gantt.scale")}
          className="inline-flex items-center gap-0.5"
        >
          {SCALES.map((item) => (
            <Button
              key={item}
              role="radio"
              aria-checked={scale === item}
              size="28"
              variant={scale === item ? "secondary" : "ghost"}
              onClick={() => setScale(item)}
            >
              {t(`tasks.gantt.${item}`)}
            </Button>
          ))}
        </div>
        <Button
          size="28"
          variant={byStart ? "secondary" : "ghost"}
          aria-pressed={byStart}
          leading={<Icon name="arrow-up-down" />}
          onClick={() => setByStart((v) => !v)}
        >
          {t("tasks.gantt.sortByStart")}
        </Button>
        <span className="flex-1" />
        <Button size="28" variant="ghost" onClick={goToday}>
          {t("tasks.gantt.goToday")}
        </Button>
        <Button
          size="28"
          variant="outline"
          role="switch"
          aria-checked={showCompleted}
          leading={<Icon name={showCompleted ? "check" : "circle"} />}
          onClick={() => setShowCompleted((v) => !v)}
        >
          {t("tasks.gantt.showCompleted")}
        </Button>
      </div>
      {truncated && (
        <p role="status" className="px-4 pb-2 text-body-sm text-fg-muted">
          {t("tasks.gantt.truncated", { count: MAX_GANTT_TASKS })}
        </p>
      )}
      {pages.isLoading ? (
        <div
          role="status"
          aria-busy
          className="flex flex-1 items-center justify-center"
        >
          <Spinner />
        </div>
      ) : (
        <div
          ref={scroller}
          data-testid="gantt-scroller"
          className="min-h-0 flex-1 overflow-auto border-t border-line-default"
        >
          <div className="relative" style={{ width: LEFT_WIDTH + width }}>
            <div
              className="sticky top-0 z-20 flex border-b border-line-default bg-surface-card"
              style={{ height: HEADER_HEIGHT }}
            >
              <div
                className="sticky left-0 z-30 flex shrink-0 items-end border-r border-line-default bg-surface-card px-4 pb-2 text-caption text-fg-muted"
                style={{ width: LEFT_WIDTH }}
              >
                {t("tasks.gantt.task")}
              </div>
              <div className="relative" style={{ width }}>
                {months.map((cell) => (
                  <div
                    key={cell.day}
                    className="absolute top-0 truncate border-l border-line-default px-2 pt-1.5 text-caption text-fg-secondary"
                    style={{
                      left: cell.offset * dayWidth,
                      width: cell.span * dayWidth,
                      height: HEADER_HEIGHT / 2,
                    }}
                  >
                    {monthLabel(cell.day)}
                  </div>
                ))}
                {ticks.map((cell) => (
                  <div
                    key={cell.day}
                    className="absolute truncate px-1 pt-1.5 text-center text-caption text-fg-muted"
                    style={{
                      left: cell.offset * dayWidth,
                      width: cell.span * dayWidth,
                      top: HEADER_HEIGHT / 2,
                    }}
                  >
                    {scale === "month" && dayOfMonth(cell.day) > 7
                      ? ""
                      : dayOfMonth(cell.day)}
                  </div>
                ))}
              </div>
            </div>
            <div className="relative">
              <div
                aria-hidden
                data-testid="gantt-today"
                className="pointer-events-none absolute z-10 w-px bg-danger/60"
                style={{
                  left: LEFT_WIDTH + todayLeft,
                  top: 0,
                  bottom: 0,
                }}
                title={t("tasks.gantt.today")}
              />
              {tasks.map((task, index) => {
                const base = ranges[index] ?? null;
                const active = drag?.task.id === task.id ? drag : null;
                const shown =
                  base && active
                    ? applyDrag(base, active.mode, active.delta)
                    : base;
                const closed = CLOSED.has(task.status_category);
                return (
                  <div
                    key={task.id}
                    role="row"
                    data-testid={`gantt-row-${task.id}`}
                    className="flex border-b border-line-default/60"
                    style={{ height: ROW_HEIGHT }}
                  >
                    <button
                      type="button"
                      onClick={() => onOpen(task)}
                      className="sticky left-0 z-10 flex shrink-0 items-center gap-2 border-r border-line-default bg-surface-card px-4 text-left hover:bg-surface-hover"
                      style={{ width: LEFT_WIDTH }}
                    >
                      <StatusIcon
                        category={task.status_category}
                        color={task.status_color}
                      />
                      <span className="shrink-0 text-caption text-fg-muted">
                        {displayIdOf(task)}
                      </span>
                      <span
                        className={cn(
                          "truncate text-body",
                          closed && "text-fg-muted line-through",
                        )}
                      >
                        {task.title}
                      </span>
                    </button>
                    <div className="relative" style={{ width }}>
                      {shown && base && (
                        <div
                          role="button"
                          tabIndex={0}
                          data-testid={`gantt-bar-${task.id}`}
                          aria-label={t("tasks.gantt.barLabel", {
                            title: task.title,
                            start: formatMonthDay(fromDay(shown.start), locale),
                            end: formatMonthDay(fromDay(shown.end), locale),
                          })}
                          aria-describedby="gantt-move-hint"
                          className={cn(
                            "absolute top-1.5 flex items-center overflow-hidden rounded-md px-2 text-caption font-medium text-fg-inverse select-none focus-visible:outline focus-visible:outline-2 focus-visible:outline-line-focus",
                            readOnly ? "cursor-default" : "cursor-grab",
                            active && "cursor-grabbing opacity-90 shadow-md",
                            closed ? "bg-fg-muted" : "bg-warning",
                          )}
                          style={{
                            left: (shown.start - range.start) * dayWidth,
                            width: (shown.end - shown.start + 1) * dayWidth,
                            height: `calc(${ROW_HEIGHT} - 12px)`,
                            ...(task.status_color && !closed
                              ? { backgroundColor: task.status_color }
                              : {}),
                            touchAction: "none",
                          }}
                          onPointerDown={(event) =>
                            onPointerDown(event, task, base)
                          }
                          onPointerMove={onPointerMove}
                          onPointerUp={onPointerUp}
                          onPointerCancel={cancelDrag}
                          onLostPointerCapture={cancelDrag}
                          onKeyDown={(event) => onKeyDown(event, task)}
                        >
                          <span className="truncate">{task.title}</span>
                        </div>
                      )}
                      {!base && (
                        <span className="absolute top-2 left-3 text-caption text-fg-muted">
                          {t("tasks.gantt.noDates")}
                        </span>
                      )}
                    </div>
                  </div>
                );
              })}
            </div>
          </div>
        </div>
      )}
      <p id="gantt-move-hint" className="sr-only">
        {t("tasks.gantt.moveHint")}
      </p>
      {!pages.isLoading && tasks.length > 0 && scheduled === 0 && (
        <p className="px-4 py-2 text-body-sm text-fg-muted">
          {t("tasks.gantt.noDatesHint")}
        </p>
      )}
    </div>
  );
}
