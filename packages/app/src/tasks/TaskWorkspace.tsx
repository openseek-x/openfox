import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";

import type {
  SpaceEstimation,
  Task,
  TaskListQuery,
  TaskStatus,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Icon, IconButton, Spinner, TextInput } from "@openfox/ui";

import { EstimationContext } from "../spaces/EstimateField";
import { readLocal } from "../storage";
import { Board } from "./board/Board";
import { Gantt } from "./gantt/Gantt";
import { DisplayMenu } from "./display/DisplayMenu";
import {
  FilterButton,
  FilterChips,
  type FilterOptions,
} from "./filters/Filters";
import { withoutIterationGrouping } from "./grouping";
import { useLayoutParam } from "./layoutParam";
import { TaskList } from "./list/TaskList";
import { TaskStatsView } from "./stats/TaskStats";
import {
  conditionActive,
  defaultDisplay,
  displayQuery,
  displayStorageKey,
  filtersToQuery,
  loadDisplay,
  saveDisplay,
  type DisplaySettings,
  type FilterCondition,
  type FilterField,
  type FilterQuery,
} from "./model";
import { useTaskCounts, type Person, type TaskSource } from "./queries";
import { TaskTable } from "./table/TaskTable";

/** 空态 / 加载 / 失败的居中提示（Figma 空态模板） */
export function TaskState({
  icon,
  title,
  desc,
  actions,
  busy,
}: {
  icon?: ReactNode;
  title: string;
  desc?: string;
  actions?: ReactNode;
  busy?: boolean;
}) {
  return (
    <div
      role={busy ? "status" : undefined}
      aria-busy={busy || undefined}
      className="flex flex-1 flex-col items-center justify-center gap-3 p-10 text-center"
    >
      {icon}
      <h2 className="text-title font-semibold tracking-[-0.01em] text-fg-default">
        {title}
      </h2>
      {desc && <p className="max-w-md text-body-sm text-fg-muted">{desc}</p>}
      {actions && <div className="mt-1 flex items-center gap-2">{actions}</div>}
    </div>
  );
}

/**
 * 空间任务与我的任务共用的工作区：工具栏（查找、筛选、显示设置）+ 筛选胶囊 + 看板 / 列表 / 表格，
 * 以及加载、失败、筛选无结果和页面自定义的空态。
 */
export function TaskWorkspace({
  orgId,
  source,
  pageKey,
  kind,
  columns,
  baseParams,
  filterFields,
  filterOptions,
  initialConditions = [],
  leading,
  empty,
  onOpen,
  onCreate,
  people,
  agents,
  estimation = null,
  iterationEnabled = false,
  projectEnabled = false,
  refNames,
  initialDisplay,
  fallbackDisplay,
  displayControl,
  onQueryError,
  onChange,
}: {
  orgId: string;
  source: TaskSource;
  /** 显示设置在本机的存储键（按组织 + 页面区分） */
  pageKey: string;
  kind: "space" | "my";
  columns: TaskStatus[];
  baseParams: TaskListQuery;
  filterFields: FilterField[];
  filterOptions: FilterOptions;
  initialConditions?: FilterCondition[];
  /** 工具栏左侧（页签、空间选择、范围胶囊） */
  leading?: ReactNode;
  /** 没有任何任务、也没有筛选时的空态 */
  empty: ReactNode;
  onOpen: (task: Task) => void;
  onCreate?: (statusId: string) => void;
  people: Person[];
  agents: { id: string; name: string }[];
  /** 阶段 3a · 空间估时设置；未启用或我的任务为 null（不显示估时列、排序与筛选） */
  estimation?: SpaceEstimation | null;
  /** 阶段 3b · 空间已启用迭代时显示迭代筛选与显示属性 */
  iterationEnabled?: boolean;
  /** 阶段 3c · 空间已启用专项时显示专项筛选与显示属性 */
  projectEnabled?: boolean;
  /** 迭代 / 专项 id → 名称，列表与表格显示用 */
  refNames?: ReadonlyMap<string, string>;
  /** 阶段 3c · 视图的显示设置：给了就以它为初值，且不写本机存储 */
  initialDisplay?: DisplaySettings;
  /** 阶段 9d-2 · 本机没有保存过显示设置时的默认值（以及「重置」的目标）；不传用 defaultDisplay(kind) */
  fallbackDisplay?: DisplaySettings;
  /** Space defaults are live; other workspaces keep their existing local behavior. */
  displayControl?: {
    value: DisplaySettings;
    onChange: (value: DisplaySettings) => void;
    onReset: () => void;
  };
  onQueryError?: (error: unknown) => void;
  /** 筛选或显示设置变化（视图编辑取值、打开视图时记本人展示方式） */
  onChange?: (state: {
    filters: FilterQuery;
    display: DisplaySettings;
  }) => void;
}) {
  const t = useT();
  const [conditions, setConditions] = useState(initialConditions);
  const [find, setFind] = useState("");
  const [finding, setFinding] = useState(false);
  // 阶段 9e · 地址里的 ?view= 优先于本机设置与视图默认
  const layoutParam = useLayoutParam();
  const [savedDisplay, setDisplayState] = useState<DisplaySettings>(() => {
    const base =
      initialDisplay ??
      (fallbackDisplay && !readLocal(displayStorageKey(orgId, pageKey))
        ? fallbackDisplay
        : loadDisplay(orgId, pageKey, kind));
    return layoutParam.layout ? { ...base, layout: layoutParam.layout } : base;
  });
  // 空间关闭估时后，本机保存的估时排序 / 属性不再生效；未启用迭代时迭代分组回落默认
  const display = useMemo(
    () =>
      withoutIterationGrouping(
        withoutEstimate(
          displayControl?.value ?? savedDisplay,
          Boolean(estimation),
          kind,
        ),
        iterationEnabled,
      ),
    [savedDisplay, displayControl?.value, estimation, kind, iterationEnabled],
  );
  // 阶段 7e · 统计视图：工具栏上的独立开关，不属于布局，也不写本机存储
  const [statsOpen, setStatsOpen] = useState(false);
  const [boardToolbar, setBoardToolbar] = useState<HTMLSpanElement | null>(
    null,
  );
  const findRef = useRef<HTMLInputElement | null>(null);
  const setDisplay = (next: DisplaySettings) => {
    if (displayControl) displayControl.onChange(next);
    else {
      setDisplayState(next);
      if (!initialDisplay) saveDisplay(orgId, pageKey, next);
    }
    if (next.layout !== display.layout) layoutParam.setLayout(next.layout);
  };

  const filters = useMemo(() => filtersToQuery(conditions), [conditions]);
  const onChangeRef = useRef(onChange);
  onChangeRef.current = onChange;
  useEffect(() => {
    onChangeRef.current?.({ filters, display });
  }, [filters, display]);

  const params = useMemo<TaskListQuery>(() => {
    // 「查找」与「内容」筛选都映射到 q：查找优先
    const q = find.trim() || ((filters.q as string | undefined) ?? "");
    return {
      ...baseParams,
      ...(filters as TaskListQuery),
      q: q || undefined,
      ...displayQuery(display),
    };
  }, [baseParams, filters, find, display]);

  const counts = useTaskCounts(orgId, source, params);
  const queryErrorHandler = useRef(onQueryError);
  queryErrorHandler.current = onQueryError;
  useEffect(() => {
    if (counts.error) queryErrorHandler.current?.(counts.error);
  }, [counts.error]);
  // Counts already gates the layout. Reuse its server anchor across every
  // column, page and stats request; never persist the anchor in a saved view.
  const rangedParams = useMemo<TaskListQuery>(
    () => ({
      ...params,
      ...(counts.data?.completed_as_of
        ? { completed_as_of: counts.data.completed_as_of }
        : {}),
    }),
    [params, counts.data?.completed_as_of],
  );
  const countMap = useMemo(
    () =>
      new Map(
        (counts.data?.by_status ?? []).map((s) => [s.status_id, s.count]),
      ),
    [counts.data],
  );
  const filtering = conditions.some(conditionActive) || find.trim() !== "";

  const viewProps = {
    orgId,
    source,
    params: rangedParams,
    columns,
    counts: countMap,
    display,
    onOpen,
    onCreate,
    people,
    agents,
    refNames,
  };

  const showStatusColumns =
    !statsOpen &&
    display.layout === "board" &&
    display.boardColumns === "status" &&
    columns.length > 0;
  let body: ReactNode;
  if (counts.isLoading)
    body = (
      <TaskState busy icon={<Spinner />} title={t("tasks.empty.loading")} />
    );
  else if (counts.error)
    body = (
      <TaskState
        icon={<Icon name="circle-alert" size={24} className="text-danger" />}
        title={t("tasks.empty.errorTitle")}
        desc={t("tasks.empty.errorDesc")}
        actions={
          <Button variant="outline" onClick={() => void counts.refetch()}>
            {t("common.retry")}
          </Button>
        }
      />
    );
  else if ((counts.data?.total ?? 0) === 0 && !showStatusColumns)
    body = filtering ? (
      <TaskState
        icon={<Icon name="list-filter" size={24} className="text-fg-muted" />}
        title={t("tasks.empty.filteredTitle")}
        desc={t("tasks.empty.filteredDesc")}
        actions={
          <Button
            variant="outline"
            onClick={() => {
              setConditions([]);
              setFind("");
              setFinding(false);
            }}
          >
            {t("tasks.filters.clear")}
          </Button>
        }
      />
    ) : (
      empty
    );
  else if (statsOpen) body = <TaskStatsView {...viewProps} />;
  else if (display.layout === "gantt") body = <Gantt {...viewProps} />;
  else if (display.layout === "list") body = <TaskList {...viewProps} />;
  else if (display.layout === "table") body = <TaskTable {...viewProps} />;
  else body = <Board {...viewProps} toolbar={boardToolbar} />;

  return (
    <EstimationContext.Provider value={estimation}>
      <div className="flex min-h-0 flex-1 flex-col">
        <div
          role="toolbar"
          aria-label={t("tasks.toolbar")}
          className="@container flex min-h-12 items-center gap-2 px-4"
        >
          <div className="flex min-w-0 flex-1 items-center gap-2">
            {leading}
          </div>
          <span ref={setBoardToolbar} className="contents" />
          {finding || find ? (
            <TextInput
              ref={findRef}
              autoFocus
              aria-label={t("tasks.find")}
              placeholder={t("tasks.findPlaceholder")}
              leading={<Icon name="search" />}
              value={find}
              maxLength={100}
              className="w-[220px] min-w-0 shrink rounded-full"
              onChange={(event) => setFind(event.target.value)}
              onBlur={() => {
                if (!find) setFinding(false);
              }}
              onKeyDown={(event) => {
                if (event.key === "Escape") {
                  setFind("");
                  setFinding(false);
                }
              }}
            />
          ) : (
            <IconButton
              label={t("tasks.find")}
              icon={<Icon name="search" />}
              variant="round"
              onClick={() => setFinding(true)}
            />
          )}
          <FilterButton
            fields={filterFields.filter(
              (field) =>
                (iterationEnabled || field !== "iteration") &&
                (projectEnabled || field !== "project"),
            )}
            conditions={conditions}
            onChange={setConditions}
            options={filterOptions}
          />
          <DisplayMenu
            value={display}
            onChange={setDisplay}
            manualSort={source.manualSort}
            estimation={Boolean(estimation)}
            iterationEnabled={iterationEnabled}
            projectEnabled={projectEnabled}
            onReset={() => {
              if (displayControl) displayControl.onReset();
              else {
                setDisplayState(fallbackDisplay ?? defaultDisplay(kind));
                saveDisplay(orgId, pageKey, null);
              }
              layoutParam.setLayout(null);
            }}
          />
          <IconButton
            label={t(
              kind === "my" ? "myTasks.dashboard" : "tasks.stats.toggle",
            )}
            icon={<Icon name="bar-chart" />}
            variant="round"
            active={statsOpen}
            aria-pressed={statsOpen}
            onClick={() => setStatsOpen((v) => !v)}
          />
        </div>
        <FilterChips
          conditions={conditions}
          onChange={setConditions}
          options={filterOptions}
          matchCount={counts.data?.total}
        />
        {body}
      </div>
    </EstimationContext.Provider>
  );
}

/** 未启用估时时去掉估时排序与估时属性。 */
export function withoutEstimate(
  display: DisplaySettings,
  enabled: boolean,
  kind: "space" | "my",
): DisplaySettings {
  if (enabled) return display;
  if (display.sort !== "estimate" && !display.properties.includes("estimate"))
    return display;
  return {
    ...display,
    sort:
      display.sort === "estimate" ? defaultDisplay(kind).sort : display.sort,
    properties: display.properties.filter((p) => p !== "estimate"),
  };
}
