import { useMemo, useState, type ReactNode } from "react";

import type { TaskStatus } from "@openfox/api";
import { useI18n, useT } from "@openfox/i18n";
import { Button, Icon, Spinner, Switch, cn } from "@openfox/ui";

import { STATUS_LIFECYCLE_COLORS } from "@openfox/ui";
import { formatMonthDay } from "../primitives/dates";
import { useTaskStats } from "../queries";
import { TaskState } from "../TaskWorkspace";
import type { TaskViewProps } from "../views";
import {
  areaPath,
  barRatio,
  completionRate,
  donutArcs,
  linePath,
  niceTicks,
  trendPoints,
} from "./model";

const CLOSED = new Set(["DONE", "CANCELLED", "DUPLICATE"]);
// 图表强调色没有对应的设计令牌，这里固定取值。
const ACCENT = "#2f6fdb";
const ACCENT_SOFT = "#bcd0f2";

/** 统计视图：指标卡 + 完成趋势 + 状态分布 + 完成分布，另有优先级与处理人分布。 */
export function TaskStatsView({
  orgId,
  source,
  params,
  columns,
  people,
  agents,
}: TaskViewProps) {
  const t = useT();
  const stats = useTaskStats(orgId, source, params);
  const [openOnly, setOpenOnly] = useState(true);
  const data = stats.data;

  const statusRows = useMemo(() => {
    const byId = new Map(columns.map((status) => [status.id, status]));
    return (data?.by_status ?? [])
      .map((group) => ({ group, status: byId.get(group.key) }))
      .filter(
        (row): row is { group: typeof row.group; status: TaskStatus } =>
          Boolean(row.status) &&
          (!openOnly || !CLOSED.has(row.status!.category)),
      );
  }, [data?.by_status, columns, openOnly]);
  // 「仅未完成」时也列出没有任务的未完成状态（计数 0）。
  const shownStatuses = useMemo(() => {
    const present = new Set(statusRows.map((row) => row.status.id));
    const empty = openOnly
      ? columns
          .filter((s) => !CLOSED.has(s.category) && !present.has(s.id))
          .map((status) => ({ group: { key: status.id, count: 0 }, status }))
      : [];
    return [...statusRows, ...empty].sort(
      (a, b) => columns.indexOf(a.status) - columns.indexOf(b.status),
    );
  }, [statusRows, columns, openOnly]);

  if (stats.isLoading)
    return (
      <TaskState busy icon={<Spinner />} title={t("tasks.empty.loading")} />
    );
  if (stats.error || !data)
    return (
      <TaskState
        icon={<Icon name="circle-alert" size={24} className="text-danger" />}
        title={t("tasks.stats.loadFailed")}
        actions={
          <Button variant="outline" onClick={() => void stats.refetch()}>
            {t("common.retry")}
          </Button>
        }
      />
    );

  const rate = completionRate(data.completed, data.total);
  const cards: Array<{
    key: string;
    label: string;
    value: string;
    accent?: ReactNode;
  }> = [
    { key: "total", label: t("tasks.stats.total"), value: String(data.total) },
    {
      key: "completed",
      label: t("tasks.stats.completed"),
      value: String(data.completed),
    },
    { key: "open", label: t("tasks.stats.open"), value: String(data.open) },
    {
      key: "overdue",
      label: t("tasks.stats.overdue"),
      value: String(data.overdue),
      accent:
        data.overdue > 0 ? (
          <span className="h-2 w-2 rounded-full bg-danger" aria-hidden />
        ) : undefined,
    },
    { key: "rate", label: t("tasks.stats.completionRate"), value: `${rate}%` },
  ];
  const personName = (kind: string, id: string) => {
    if (kind === "NONE") return t("tasks.stats.unassigned");
    if (kind === "HUMAN") {
      // 与其他界面一致：有全名显示全名，没有就显示邮箱；不在成员列表里时才退回 ID 前缀
      const person = people.find((p) => p.id === id);
      return person?.name || person?.email || id.slice(0, 8);
    }
    if (kind === "AGENT")
      return agents.find((a) => a.id === id)?.name ?? id.slice(0, 8);
    return id.slice(0, 8);
  };
  const priorityRows = data.by_priority.map((group) => ({
    key: group.key,
    label: t(`tasks.priority.${group.key as "NONE"}`),
    count: group.count,
    color: ACCENT,
  }));
  const assigneeRows = data.by_assignee.map((group) => ({
    key: `${group.kind}:${group.id}`,
    label: personName(group.kind, group.id),
    count: group.count,
    color: ACCENT,
  }));

  return (
    <div
      data-testid="task-stats"
      className="flex min-h-0 flex-1 flex-col gap-4 overflow-auto px-4 pb-6"
    >
      <h2 className="sr-only">{t("tasks.stats.title")}</h2>
      <div className="grid grid-cols-2 gap-3 md:grid-cols-5">
        {cards.map((card) => (
          <div
            key={card.key}
            data-testid={`stat-${card.key}`}
            className="rounded-xl border border-line-default bg-surface-card p-4"
          >
            <div className="flex items-center justify-between text-body-sm text-fg-muted">
              {card.label}
              {card.accent}
            </div>
            <div className="mt-2 text-[28px] leading-8 font-semibold">
              {card.value}
            </div>
          </div>
        ))}
      </div>

      <Panel
        title={t("tasks.stats.trend")}
        aside={t("tasks.stats.trendRange", { days: data.trend.length })}
      >
        <TrendChart
          trend={data.trend}
          label={t("tasks.stats.trend")}
          pointLabel={(date, count) =>
            t("tasks.stats.trendPoint", { date, count })
          }
        />
      </Panel>

      <div className="grid gap-4 lg:grid-cols-[1fr_320px]">
        <Panel
          title={t("tasks.stats.statusDistribution")}
          aside={
            <label className="flex items-center gap-2 text-body-sm text-fg-muted">
              {t("tasks.stats.openOnly")}
              <Switch
                label={t("tasks.stats.openOnly")}
                checked={openOnly}
                onCheckedChange={setOpenOnly}
              />
            </label>
          }
        >
          <BarList
            testId="stats-status"
            rows={shownStatuses.map(({ group, status }) => ({
              key: status.id,
              label: status.name,
              count: group.count,
              color:
                status.color ??
                STATUS_LIFECYCLE_COLORS[
                  status.category as keyof typeof STATUS_LIFECYCLE_COLORS
                ],
            }))}
            empty={t("tasks.stats.noData")}
          />
        </Panel>
        <Panel title={t("tasks.stats.completionDistribution")}>
          <Donut
            completed={data.completed}
            total={data.total}
            unit={t("tasks.stats.taskUnit")}
            legend={[
              {
                label: t("tasks.stats.completed"),
                count: data.completed,
                color: ACCENT,
              },
              {
                label: t("tasks.stats.open"),
                count: data.open,
                color: ACCENT_SOFT,
              },
              // 已取消 / 重复的任务既不算完成也不算未完成，单列一项让三项之和等于总数
              ...(data.total - data.completed - data.open > 0
                ? [
                    {
                      label: t("tasks.stats.closedOther"),
                      count: data.total - data.completed - data.open,
                      color: "#d4d7dd",
                    },
                  ]
                : []),
            ]}
          />
        </Panel>
      </div>

      <div className="grid gap-4 lg:grid-cols-2">
        <Panel title={t("tasks.stats.priorityDistribution")}>
          <BarList
            testId="stats-priority"
            rows={priorityRows}
            empty={t("tasks.stats.noData")}
          />
        </Panel>
        <Panel title={t("tasks.stats.assigneeDistribution")}>
          <BarList
            testId="stats-assignee"
            rows={assigneeRows}
            empty={t("tasks.stats.noData")}
          />
        </Panel>
      </div>
    </div>
  );
}

function Panel({
  title,
  aside,
  children,
}: {
  title: string;
  aside?: ReactNode;
  children: ReactNode;
}) {
  return (
    <section className="rounded-xl border border-line-default bg-surface-card p-4">
      <header className="mb-3 flex items-center justify-between">
        <h3 className="text-body font-medium">{title}</h3>
        {typeof aside === "string" ? (
          <span className="text-body-sm text-fg-muted">{aside}</span>
        ) : (
          aside
        )}
      </header>
      {children}
    </section>
  );
}

function BarList({
  rows,
  empty,
  testId,
}: {
  rows: Array<{ key: string; label: string; count: number; color: string }>;
  empty: string;
  testId: string;
}) {
  const max = Math.max(0, ...rows.map((r) => r.count));
  if (rows.length === 0)
    return (
      <p className="py-6 text-center text-body-sm text-fg-muted">{empty}</p>
    );
  return (
    <ul data-testid={testId} className="flex flex-col gap-2.5">
      {rows.map((row) => (
        <li key={row.key} className="flex items-center gap-3 text-body-sm">
          <span
            className="w-24 shrink-0 truncate text-fg-secondary"
            title={row.label}
          >
            {row.label}
          </span>
          <span className="h-5 min-w-0 flex-1 rounded bg-surface-muted">
            <span
              className="block h-full rounded"
              style={{
                width: `${barRatio(row.count, max) * 100}%`,
                backgroundColor: row.color,
              }}
            />
          </span>
          <span className="w-8 shrink-0 text-right tabular-nums">
            {row.count}
          </span>
        </li>
      ))}
    </ul>
  );
}

function TrendChart({
  trend,
  label,
  pointLabel,
}: {
  trend: Array<{ date: string; completed: number }>;
  label: string;
  pointLabel: (date: string, count: number) => string;
}) {
  const { locale } = useI18n();
  const [hover, setHover] = useState<number | null>(null);
  const W = 800;
  const H = 160;
  const values = trend.map((p) => p.completed);
  const ticks = niceTicks(Math.max(0, ...values));
  const yMax = ticks[ticks.length - 1]!;
  const points = trendPoints(values, W, H, yMax);
  const every = Math.max(1, Math.ceil(trend.length / 8));
  const active = hover === null ? null : points[hover];
  return (
    <div className="relative">
      <svg
        role="img"
        aria-label={`${label}: ${trend.reduce((sum, p) => sum + p.completed, 0)}`}
        viewBox={`-32 -8 ${W + 40} ${H + 36}`}
        className="h-[220px] w-full"
        onMouseLeave={() => setHover(null)}
      >
        {ticks.map((tick) => {
          const y = H - (tick / yMax) * H;
          return (
            <g key={tick}>
              <line
                x1={0}
                x2={W}
                y1={y}
                y2={y}
                stroke="currentColor"
                className="text-line-default"
                strokeDasharray="3 3"
              />
              <text
                x={-8}
                y={y + 4}
                textAnchor="end"
                className="fill-current text-fg-muted"
                fontSize={11}
              >
                {tick}
              </text>
            </g>
          );
        })}
        <path d={areaPath(points, H)} fill={ACCENT} opacity={0.12} />
        <path
          d={linePath(points)}
          fill="none"
          stroke={ACCENT}
          strokeWidth={2}
          strokeLinejoin="round"
        />
        {trend.map((p, i) =>
          i % every === 0 || i === trend.length - 1 ? (
            <text
              key={p.date}
              x={points[i]!.x}
              y={H + 20}
              textAnchor="middle"
              className="fill-current text-fg-muted"
              fontSize={11}
            >
              {formatMonthDay(p.date, locale)}
            </text>
          ) : null,
        )}
        {points.map((point, i) => (
          <circle
            key={trend[i]!.date}
            cx={point.x}
            cy={point.y}
            r={hover === i ? 5 : 3}
            fill={
              hover === i || trend[i]!.completed > 0 ? ACCENT : "transparent"
            }
            stroke="transparent"
            strokeWidth={10}
            onMouseEnter={() => setHover(i)}
          >
            <title>{pointLabel(trend[i]!.date, trend[i]!.completed)}</title>
          </circle>
        ))}
      </svg>
      {active && hover !== null && (
        <div
          role="status"
          className={cn(
            "pointer-events-none absolute top-2 rounded-lg border border-line-default bg-surface-card px-3 py-1.5 text-body-sm shadow-md",
          )}
          style={{
            left: `${Math.min(80, Math.max(2, (hover / Math.max(1, trend.length - 1)) * 100))}%`,
          }}
        >
          {pointLabel(
            formatMonthDay(trend[hover]!.date, locale),
            trend[hover]!.completed,
          )}
        </div>
      )}
    </div>
  );
}

function Donut({
  completed,
  total,
  unit,
  legend,
}: {
  completed: number;
  total: number;
  unit: string;
  legend: Array<{ label: string; count: number; color: string }>;
}) {
  const R = 52;
  const { circumference, completedLength } = donutArcs(completed, total, R);
  return (
    <div className="flex flex-col items-center gap-4">
      <svg
        viewBox="0 0 140 140"
        className="h-[140px] w-[140px]"
        role="img"
        aria-label={`${completed}/${total}`}
      >
        <circle
          cx={70}
          cy={70}
          r={R}
          fill="none"
          stroke={ACCENT_SOFT}
          strokeWidth={16}
        />
        <circle
          cx={70}
          cy={70}
          r={R}
          fill="none"
          stroke={ACCENT}
          strokeWidth={16}
          strokeDasharray={`${completedLength} ${circumference}`}
          transform="rotate(-90 70 70)"
        />
        <text
          x={70}
          y={72}
          textAnchor="middle"
          className="fill-current"
          fontSize={26}
          fontWeight={600}
        >
          {total}
        </text>
        <text
          x={70}
          y={90}
          textAnchor="middle"
          className="fill-current text-fg-muted"
          fontSize={11}
        >
          {unit}
        </text>
      </svg>
      <ul className="flex w-full flex-col gap-1.5 text-body-sm">
        {legend.map((item) => (
          <li key={item.label} className="flex items-center gap-2">
            <span
              className="h-2.5 w-2.5 rounded-sm"
              style={{ backgroundColor: item.color }}
            />
            <span className="flex-1 text-fg-secondary">{item.label}</span>
            <span className="tabular-nums">{item.count}</span>
            <span className="w-10 text-right text-fg-muted tabular-nums">
              {completionRate(item.count, total)}%
            </span>
          </li>
        ))}
      </ul>
    </div>
  );
}
