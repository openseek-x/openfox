import { useId, useRef, useState, type ReactNode } from "react";

import type { TaskConversationStats } from "@openfox/api";
import { formatTokens } from "../../agents/settings/parts";
import { useI18n, useT } from "@openfox/i18n";
import { DateFieldPopover, Icon, cn } from "@openfox/ui";

import { LabelDot } from "../pickers";
import { formatMonthDay } from "../primitives/dates";
import { PriorityIcon } from "../primitives/PriorityIcon";
import { StatusIcon } from "../primitives/StatusIcon";
import { useUpdateTask } from "../queries";
import { MoreProperties, type PropertyBarProps } from "./NarrowDetail";
import {
  AssigneeContent,
  AssigneePicker,
  PriorityPicker,
  StatusPicker,
  TaskLabelPicker,
  TaskProjectPicker,
  useProjectsEnabled,
} from "./propertyPickers";

const trigger =
  "flex min-h-8 w-full min-w-0 items-center gap-2 rounded-md px-2 text-left text-body-sm hover:bg-surface-hover disabled:cursor-not-allowed disabled:hover:bg-transparent";

function PropertySection({
  title,
  children,
}: {
  title: string;
  children: ReactNode;
}) {
  const [open, setOpen] = useState(true);
  const id = useId();
  return (
    <section className="rounded-lg border border-line-default">
      <button
        type="button"
        aria-expanded={open}
        aria-controls={id}
        onClick={() => setOpen(!open)}
        className="flex h-10 w-full items-center justify-between rounded-lg px-3 text-body-sm font-medium text-fg-muted hover:bg-surface-hover"
      >
        {title}
        <Icon
          name={open ? "chevron-down" : "chevron-right"}
          size={14}
          className="text-fg-muted"
        />
      </button>
      <div id={id} hidden={!open} className="px-1 pb-2">
        {children}
      </div>
    </section>
  );
}

/** 宽栏保留常用字段；日期、父任务、自定义字段等仍由原编辑器保存。 */
export function WideProperties(
  props: PropertyBarProps & { stats?: TaskConversationStats },
) {
  const { orgId, task, people, agents, workflow, labels, readOnly, stats } =
    props;
  const t = useT();
  const { locale } = useI18n();
  const update = useUpdateTask(orgId);
  const projectsEnabled = useProjectsEnabled(orgId, task.space_id);
  const [dueOpen, setDueOpen] = useState(false);
  const dueAnchor = useRef<HTMLButtonElement | null>(null);
  return (
    <div className="flex flex-col gap-3">
      <PropertySection title={t("tasks.detail.properties")}>
        <fieldset
          disabled={readOnly}
          aria-label={t("tasks.detail.properties")}
          className="flex min-w-0 flex-col gap-1"
        >
          <div role="group" aria-label={t("tasks.fields.assignee")}>
            <AssigneePicker
              orgId={orgId}
              task={task}
              people={people}
              agents={agents}
              trigger={({ ref, toggle, open }) => (
                <button
                  ref={ref}
                  type="button"
                  aria-label={t("tasks.fields.assignee")}
                  aria-expanded={open}
                  onClick={toggle}
                  className={cn(trigger, "py-1")}
                >
                  <AssigneeContent
                    task={task}
                    people={people}
                    agents={agents}
                    stacked
                  />
                </button>
              )}
            />
          </div>
          <div role="group" aria-label={t("tasks.fields.status")}>
            <StatusPicker
              orgId={orgId}
              task={task}
              workflow={workflow}
              trigger={({ ref, toggle, open }) => (
                <button
                  ref={ref}
                  type="button"
                  aria-expanded={open}
                  onClick={toggle}
                  className={trigger}
                >
                  <StatusIcon
                    category={task.status_category}
                    color={task.status_color}
                  />
                  <span className="truncate">{task.status_name}</span>
                </button>
              )}
            />
          </div>
          <div role="group" aria-label={t("tasks.fields.priority")}>
            <PriorityPicker
              orgId={orgId}
              task={task}
              trigger={({ ref, toggle, open }) => (
                <button
                  ref={ref}
                  type="button"
                  aria-expanded={open}
                  onClick={toggle}
                  className={trigger}
                >
                  <PriorityIcon priority={task.priority} />
                  {task.priority === "NONE"
                    ? t("tasks.priorityNone")
                    : t(`tasks.priority.${task.priority}`)}
                </button>
              )}
            />
          </div>
          <div
            role="group"
            aria-label={t("tasks.fields.label")}
            className="mt-2 border-t border-line-default pt-2"
          >
            <TaskLabelPicker
              orgId={orgId}
              task={task}
              labels={labels}
              trigger={({ ref, toggle, open }) => (
                <button
                  ref={ref}
                  type="button"
                  aria-expanded={open}
                  onClick={toggle}
                  aria-label={t("tasks.fields.label")}
                  className={cn(trigger, "flex-wrap")}
                >
                  <span className="w-full text-fg-muted">
                    {t("tasks.fields.label")}
                  </span>
                  {task.label_items.map((label) => (
                    <span
                      key={label.id}
                      className="inline-flex h-5 max-w-full items-center gap-1 rounded-full border border-line-default px-1.5 text-caption"
                    >
                      <LabelDot color={label.color} />
                      <span className="truncate">{label.name}</span>
                    </span>
                  ))}
                  <span className="flex h-6 w-6 items-center justify-center rounded-full border border-line-default text-fg-muted">
                    <Icon name="plus" />
                  </span>
                </button>
              )}
            />
          </div>
          <div
            role="group"
            aria-label={t("tasks.fields.dueDate")}
            className="flex flex-col items-start gap-1 px-2 py-2"
          >
            <span className="text-body-sm text-fg-muted">
              {t("tasks.fields.dueDate")}
            </span>
            <button
              ref={dueAnchor}
              type="button"
              aria-expanded={dueOpen}
              onClick={() => setDueOpen(true)}
              className="flex h-7 items-center gap-1 rounded-md bg-surface-muted px-2 text-body-sm text-fg-muted hover:bg-surface-hover"
            >
              <Icon name="calendar" />
              <span>
                {task.due_date
                  ? formatMonthDay(task.due_date, locale)
                  : t("tasks.detail.none")}
              </span>
            </button>
            <DateFieldPopover
              open={dueOpen}
              onOpenChange={setDueOpen}
              anchorRef={dueAnchor}
              value={task.due_date}
              onChange={(value) =>
                update.mutate({ task, patch: { due_date: value } })
              }
            />
          </div>
        </fieldset>
        <MoreProperties {...props} sidebar />
      </PropertySection>
      {projectsEnabled && (
        <PropertySection title={t("projects.title")}>
          <TaskProjectPicker
            orgId={orgId}
            task={task}
            trigger={(name, { ref, toggle, open }) => (
              <button
                ref={ref}
                type="button"
                disabled={readOnly}
                aria-expanded={open}
                onClick={toggle}
                className={cn(trigger, !task.project_id && "text-fg-muted")}
              >
                <Icon name={task.project_id ? "folder" : "plus"} />
                <span className="truncate">
                  {name ?? t("tasks.detail.joinProject")}
                </span>
              </button>
            )}
          />
        </PropertySection>
      )}
      {stats && stats.run_count > 0 && <TaskTokenUsage stats={stats} />}
    </div>
  );
}

function TaskTokenUsage({ stats }: { stats: TaskConversationStats }) {
  const t = useT();
  const partial = stats.reported_runs < stats.run_count;
  const format = (value: number) =>
    stats.reported_runs === 0
      ? "—"
      : (partial ? "≥ " : "") + formatTokens(value).replace("K", "k");
  const rows = [
    [t("tasks.detail.tokenInput"), format(stats.input)],
    [t("tasks.detail.tokenOutput"), format(stats.output)],
    [
      t("tasks.detail.tokenCache"),
      stats.reported_runs === 0
        ? "—"
        : t("tasks.detail.cacheTotals", {
            read: format(stats.cache_read),
            write: format(stats.cache_write),
          }),
    ],
    [t("tasks.detail.runCount"), String(stats.run_count)],
  ];
  return (
    <PropertySection title={t("tasks.detail.tokenUsage")}>
      <dl className="grid grid-cols-[auto_1fr] items-baseline gap-x-3 gap-y-2 px-2 pb-1 text-body-sm">
        {rows.map(([label, value]) => (
          <div key={label} className="contents">
            <dt className="text-fg-muted">{label}</dt>
            <dd className="text-right tabular-nums">{value}</dd>
          </div>
        ))}
      </dl>
      {partial && (
        <p className="px-2 pt-1 text-caption text-fg-muted">
          {t("tasks.detail.partialUsage", {
            reported: stats.reported_runs,
            total: stats.run_count,
          })}
        </p>
      )}
    </PropertySection>
  );
}
