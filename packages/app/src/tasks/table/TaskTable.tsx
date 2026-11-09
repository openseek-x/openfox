import { memo, useMemo, useRef, useState } from "react";

import type { Task } from "@openfox/api";
import { useI18n, useT } from "@openfox/i18n";
import {
  Checkbox,
  DateFieldPopover,
  Spinner,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeaderCell,
  TableRow,
  cn,
} from "@openfox/ui";

import { BulkActionBar } from "../bulk/BulkActionBar";
import { SelectBox } from "../bulk/SelectBox";
import { useTaskSelection } from "../bulk/selection";
import { LoadMore, displayIdOf } from "../parts";
import {
  Picker,
  assigneeGroups,
  assigneeValue,
  priorityGroups,
  replaceAssignee,
  statusGroups,
  toRefs,
} from "../pickers";
import { formatMonthDay, isOverdue } from "../primitives/dates";
import { PriorityIcon, type Priority } from "../primitives/PriorityIcon";
import { StatusIcon } from "../primitives/StatusIcon";
import { useTaskPages, useUpdateTask } from "../queries";
import { statusFields } from "../TaskMenu";
import type { TaskViewProps } from "../views";
import { useEstimation } from "../../spaces/EstimateField";
import { formatEstimate } from "../../spaces/estimation";
import { useTaskReadOnlyReason } from "../../spaces/readonly";
import {
  TaskAssigneeAvatar,
  isPersonAssignee,
  useInvitationAssignees,
} from "../invitationAssignees";
import { usePriorityLevels } from "../../organization/queries";

/** 表格：单元格内直接改状态、优先级、处理人、截止日期；勾选后底部出现批量操作栏。 */
export function TaskTable(props: TaskViewProps) {
  const t = useT();
  const { orgId, source, params, display } = props;
  const pages = useTaskPages(orgId, source, params, null);
  const tasks = useMemo(
    () => pages.data?.pages.flatMap((p) => p.tasks) ?? [],
    [pages.data],
  );
  const selection = useTaskSelection();
  const order = useMemo(() => tasks.map((x) => x.id), [tasks]);
  // 空间启用估时时多一列估时（只读，修改在任务详情里）。
  const estimation = useEstimation();
  const all = tasks.length > 0 && tasks.every((x) => selection.has(x.id));
  return (
    <div className="min-h-0 flex-1 overflow-auto px-4 pb-4">
      <Table aria-label={t("tasks.table.label")}>
        <TableHead>
          <TableRow>
            <TableHeaderCell className="w-9">
              <Checkbox
                aria-label={t("tasks.table.selectAll")}
                checked={all}
                onChange={() => selection.setMany(order, !all)}
              />
            </TableHeaderCell>
            <TableHeaderCell className="w-24">
              {t("tasks.fields.id")}
            </TableHeaderCell>
            <TableHeaderCell>{t("tasks.fields.task")}</TableHeaderCell>
            <TableHeaderCell className="w-36">
              {t("tasks.fields.status")}
            </TableHeaderCell>
            <TableHeaderCell className="w-28">
              {t("tasks.fields.priority")}
            </TableHeaderCell>
            <TableHeaderCell className="w-44">
              {t("tasks.fields.assignee")}
            </TableHeaderCell>
            <TableHeaderCell className="w-32">
              {t("tasks.fields.dueDate")}
            </TableHeaderCell>
            {estimation && (
              <TableHeaderCell className="w-28">
                {t("spaces.task.estimate")}
              </TableHeaderCell>
            )}
            {display.properties.includes("iteration") && (
              <TableHeaderCell className="w-28">
                {t("spaces.sections.iterations")}
              </TableHeaderCell>
            )}
            {display.properties.includes("project") && (
              <TableHeaderCell className="w-28">
                {t("projects.title")}
              </TableHeaderCell>
            )}
          </TableRow>
        </TableHead>
        <TableBody>
          {tasks.map((task) => (
            <Row
              key={task.id}
              task={task}
              {...props}
              selected={selection.has(task.id)}
              onSelect={(on, range) =>
                selection.toggle(task.id, on, { range, order })
              }
            />
          ))}
        </TableBody>
      </Table>
      {pages.isLoading && <Spinner className="mx-auto my-4" />}
      <LoadMore
        hasMore={Boolean(pages.hasNextPage)}
        loading={pages.isFetchingNextPage}
        onLoad={() => void pages.fetchNextPage()}
      />
      <BulkActionBar
        orgId={orgId}
        selection={selection}
        people={props.people}
      />
    </div>
  );
}

const cellButton =
  "flex h-7 w-full items-center gap-1.5 rounded-md px-1.5 text-left text-body-sm hover:bg-surface-active disabled:cursor-not-allowed disabled:hover:bg-transparent";

const Row = memo(function Row({
  task,
  orgId,
  columns,
  people,
  agents,
  display,
  refNames,
  onOpen,
  selected,
  onSelect,
}: TaskViewProps & {
  task: Task;
  selected: boolean;
  onSelect: (on: boolean, range: boolean) => void;
}) {
  const t = useT();
  const priorityLevels = usePriorityLevels();
  const { locale } = useI18n();
  const update = useUpdateTask(orgId);
  const estimation = useEstimation();
  // 已停用空间的任务：单元格只读。
  const readOnlyReason = useTaskReadOnlyReason(orgId, task.space_id);
  const readOnly = Boolean(readOnlyReason);
  const [dateOpen, setDateOpen] = useState(false);
  const dateAnchor = useRef<HTMLButtonElement | null>(null);
  const [assignOpen, setAssignOpen] = useState(false);
  const { candidates: invitations } = useInvitationAssignees(
    orgId,
    assignOpen,
    [task.space_id],
  );
  const human = task.assignees.find((a) => isPersonAssignee(a.kind));
  const agent = task.assignees.find((a) => a.kind === "AGENT");
  const shown = human ?? agent;
  return (
    <TableRow selected={selected} title={readOnlyReason}>
      <TableCell>
        <SelectBox
          label={t("tasks.table.select", { task: displayIdOf(task) })}
          checked={selected}
          onToggle={onSelect}
        />
      </TableCell>
      <TableCell className="max-w-[140px] truncate whitespace-nowrap text-caption text-fg-muted">
        {displayIdOf(task)}
      </TableCell>
      <TableCell>
        <button
          type="button"
          onClick={() => onOpen(task)}
          className="max-w-full truncate text-left hover:underline"
        >
          {task.title}
        </button>
      </TableCell>
      <TableCell>
        <Picker
          label={t("tasks.fields.status")}
          groups={statusGroups(columns)}
          selected={[task.status_id]}
          onSelect={(id) => {
            const status = columns.find((c) => c.id === id);
            if (status && id !== task.status_id)
              update.mutate({
                task,
                patch: { status_id: id },
                optimistic: statusFields(status),
              });
          }}
          trigger={({ ref, toggle, open }) => (
            <button
              ref={ref}
              type="button"
              aria-expanded={open}
              aria-label={`${t("tasks.fields.status")}: ${task.status_name}`}
              disabled={readOnly}
              onClick={toggle}
              className={cellButton}
            >
              <StatusIcon
                category={task.status_category}
                color={task.status_color}
              />
              <span className="truncate">{task.status_name}</span>
            </button>
          )}
        />
      </TableCell>
      <TableCell>
        <Picker
          label={t("tasks.fields.priority")}
          groups={priorityGroups(t, priorityLevels)}
          selected={[task.priority]}
          onSelect={(value) => {
            if (value !== task.priority)
              update.mutate({ task, patch: { priority: value as Priority } });
          }}
          trigger={({ ref, toggle, open }) => (
            <button
              ref={ref}
              type="button"
              aria-expanded={open}
              aria-label={`${t("tasks.fields.priority")}: ${t(`tasks.priority.${task.priority}`)}`}
              disabled={readOnly}
              onClick={toggle}
              className={cellButton}
            >
              <PriorityIcon priority={task.priority} />
              {t(`tasks.priority.${task.priority}`)}
            </button>
          )}
        />
      </TableCell>
      <TableCell>
        <Picker
          label={t("tasks.fields.assignee")}
          groups={assigneeGroups(t, people, agents, { invitations })}
          open={assignOpen}
          onOpenChange={setAssignOpen}
          selected={shown ? [assigneeValue(shown)] : ["none"]}
          onSelect={(value) => {
            const next = replaceAssignee(task.assignees, value, {
              invitations,
              people,
              agents,
            });
            update.mutate({
              task,
              patch: { assignees: toRefs(next) },
              optimistic: { assignees: next },
            });
          }}
          trigger={({ ref, toggle, open }) => (
            <button
              ref={ref}
              type="button"
              aria-expanded={open}
              aria-label={`${t("tasks.fields.assignee")}: ${shown?.name ?? t("tasks.noAssignee")}`}
              disabled={readOnly}
              onClick={toggle}
              className={cn(cellButton, !shown && "text-fg-muted")}
            >
              {shown && (
                <TaskAssigneeAvatar
                  name={shown.name}
                  kind={shown.kind}
                  size={20}
                />
              )}
              <span className="truncate">
                {shown?.name ?? t("tasks.noAssignee")}
              </span>
            </button>
          )}
        />
      </TableCell>
      <TableCell>
        <button
          ref={dateAnchor}
          type="button"
          aria-expanded={dateOpen}
          aria-label={`${t("tasks.fields.dueDate")}: ${task.due_date ?? t("tasks.detail.none")}`}
          disabled={readOnly}
          onClick={() => setDateOpen((v) => !v)}
          className={cn(
            cellButton,
            !task.due_date && "text-fg-muted",
            task.due_date && isOverdue(task.due_date) && "text-danger",
          )}
        >
          {task.due_date ? formatMonthDay(task.due_date, locale) : "—"}
        </button>
        <DateFieldPopover
          open={dateOpen}
          onOpenChange={setDateOpen}
          anchorRef={dateAnchor}
          value={task.due_date}
          onChange={(value) =>
            update.mutate({ task, patch: { due_date: value } })
          }
        />
      </TableCell>
      {estimation && (
        <TableCell className="text-body-sm text-fg-secondary">
          {formatEstimate(task.estimate, estimation) || "—"}
        </TableCell>
      )}
      {display.properties.includes("iteration") && (
        <TableCell className="text-body-sm text-fg-secondary">
          {(task.iteration_id && refNames?.get(task.iteration_id)) || "—"}
        </TableCell>
      )}
      {display.properties.includes("project") && (
        <TableCell className="text-body-sm text-fg-secondary">
          {(task.project_id && refNames?.get(task.project_id)) || "—"}
        </TableCell>
      )}
    </TableRow>
  );
});
