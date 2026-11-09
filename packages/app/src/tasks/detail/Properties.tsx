import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useId, useRef, useState, type ReactNode } from "react";

import type {
  CustomField,
  Label,
  Task,
  TaskFieldValue,
  TaskStatus,
} from "@openfox/api";
import { useI18n, useT } from "@openfox/i18n";
import {
  Avatar,
  DateFieldPopover,
  Icon,
  IconButton,
  Switch,
  TextInput,
  Textarea,
  cn,
  useToast,
} from "@openfox/ui";

import { keys, useApi } from "../../context";
import { LabelDot, Picker, assigneeGroups } from "../pickers";
import { formatMonthDay } from "../primitives/dates";
import { PriorityIcon } from "../primitives/PriorityIcon";
import { StatusIcon } from "../primitives/StatusIcon";
import {
  personName,
  taskErrorMessage,
  useUpdateTask,
  type Person,
} from "../queries";
import {
  AssigneePicker,
  AssigneeContent,
  type AssigneeAgent,
  PriorityPicker,
  StatusPicker,
  TaskLabelPicker,
  TaskProjectPicker,
  useProjectsEnabled,
} from "./propertyPickers";
import { TaskSearchDialog } from "./TaskSearchDialog";
import { EstimateInput, useSpaceEstimation } from "../../spaces/EstimateField";
import { useIterationSettings, useIterations } from "../../iterations/queries";
import { ReadOnlyTip, useReadOnly } from "../../spaces/readonly";

const trigger =
  "flex min-h-8 w-full items-center gap-2 rounded-md px-2 text-left text-body hover:bg-surface-hover disabled:cursor-not-allowed disabled:hover:bg-transparent";

/** 属性行：用 group 关联属性名，读屏与测试都能区分各行里同为「未设置」的按钮。 */
function Row({
  label,
  children,
  compact = false,
}: {
  label: string;
  children: ReactNode;
  compact?: boolean;
}) {
  const id = useId();
  return (
    <div
      role="group"
      aria-labelledby={id}
      className={cn(
        compact
          ? "grid min-h-8 grid-cols-[96px_minmax(0,1fr)] items-center gap-2"
          : "flex flex-col gap-0.5",
      )}
    >
      <div
        id={id}
        className={cn(
          "px-2 text-fg-muted",
          compact ? "text-body-sm" : "text-caption",
        )}
      >
        {label}
      </div>
      <ReadOnlyTip block>
        <div className="flex min-w-0 flex-1 flex-col">{children}</div>
      </ReadOnlyTip>
    </div>
  );
}

/** 详情右栏：处理人、执行 Agent、状态、优先级、标签、日期、父任务、更多属性、元信息。 */
export function Properties({
  orgId,
  task,
  workflow,
  labels,
  people,
  agents,
  onOpenTask,
  additionalOnly = false,
  hideDueDate = false,
}: {
  orgId: string;
  task: Task;
  workflow: TaskStatus[];
  labels: Label[];
  people: Person[];
  agents: AssigneeAgent[];
  onOpenTask: (displayId: string) => void;
  /** 窄布局已在按钮行显示基础属性，浮层只展示剩余属性。 */
  additionalOnly?: boolean;
  hideDueDate?: boolean;
}) {
  const t = useT();
  const { locale } = useI18n();
  const update = useUpdateTask(orgId);
  // 估时只在启用估时的空间显示。
  const estimation = useSpaceEstimation(orgId, task.space_id);
  const iterationSettings = useIterationSettings(orgId, task.space_id);
  const iterations = useIterations(orgId, task.space_id, {
    status: "planned,active",
  });
  const projectsEnabled = useProjectsEnabled(orgId, task.space_id);
  const readOnly = useReadOnly();
  const [dateOpen, setDateOpen] = useState<null | "start" | "due">(null);
  const [parentOpen, setParentOpen] = useState(false);
  const startAnchor = useRef<HTMLButtonElement | null>(null);
  const dueAnchor = useRef<HTMLButtonElement | null>(null);
  const chosenLabels = task.label_items;
  return (
    // 已停用空间：fieldset 一次禁用右栏所有控件，每行外层 ReadOnlyTip 提示原因。
    <fieldset
      disabled={readOnly}
      aria-label={t("tasks.detail.properties")}
      className={cn(
        "flex min-w-0 flex-col",
        additionalOnly ? "gap-1" : "gap-3",
      )}
    >
      {!additionalOnly && (
        <>
          <Row label={t("tasks.fields.assignee")}>
            <AssigneePicker
              orgId={orgId}
              task={task}
              people={people}
              agents={agents}
              trigger={({ ref, toggle, open }) => (
                <button
                  ref={ref}
                  type="button"
                  aria-expanded={open}
                  onClick={toggle}
                  aria-label={t("tasks.fields.assignee")}
                  className={trigger}
                >
                  <AssigneeContent
                    task={task}
                    people={people}
                    agents={agents}
                  />
                </button>
              )}
            />
          </Row>
          <Row label={t("tasks.fields.status")}>
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
          </Row>
          <Row label={t("tasks.fields.priority")}>
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
          </Row>
        </>
      )}
      {!additionalOnly && (
        <Row label={t("tasks.fields.label")}>
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
                className={cn(
                  trigger,
                  "flex-wrap py-1",
                  !chosenLabels.length && "text-fg-muted",
                )}
              >
                {chosenLabels.length === 0 && (
                  <>
                    <Icon name="tag" />
                    {t("tasks.noLabels")}
                  </>
                )}
                {chosenLabels.map((l) => (
                  <span
                    key={l.id}
                    className="inline-flex h-5 items-center gap-1 rounded-full border border-line-default px-1.5 text-caption"
                  >
                    <LabelDot color={l.color} />
                    {l.name}
                  </span>
                ))}
              </button>
            )}
          />
        </Row>
      )}
      <Row label={t("tasks.fields.startDate")} compact={additionalOnly}>
        <button
          ref={startAnchor}
          type="button"
          aria-expanded={dateOpen === "start"}
          onClick={() => setDateOpen("start")}
          className={cn(trigger, !task.start_date && "text-fg-muted")}
        >
          <Icon name="calendar" />
          {task.start_date
            ? formatMonthDay(task.start_date, locale)
            : t("tasks.detail.none")}
        </button>
        <DateFieldPopover
          open={dateOpen === "start"}
          onOpenChange={(o) => setDateOpen(o ? "start" : null)}
          anchorRef={startAnchor}
          value={task.start_date}
          onChange={(value) =>
            update.mutate({ task, patch: { start_date: value } })
          }
        />
      </Row>
      {!hideDueDate && (
        <Row label={t("tasks.fields.dueDate")} compact={additionalOnly}>
          <button
            ref={dueAnchor}
            type="button"
            aria-expanded={dateOpen === "due"}
            onClick={() => setDateOpen("due")}
            className={cn(trigger, !task.due_date && "text-fg-muted")}
          >
            <Icon name="calendar" />
            {task.due_date
              ? formatMonthDay(task.due_date, locale)
              : t("tasks.detail.none")}
          </button>
          <DateFieldPopover
            open={dateOpen === "due"}
            onOpenChange={(o) => setDateOpen(o ? "due" : null)}
            anchorRef={dueAnchor}
            value={task.due_date}
            onChange={(value) =>
              update.mutate({ task, patch: { due_date: value } })
            }
          />
        </Row>
      )}
      {estimation && (
        <Row label={t("spaces.task.estimate")} compact={additionalOnly}>
          <EstimateInput
            estimation={estimation}
            value={task.estimate}
            className="h-7 w-full text-body-sm"
            onChange={(estimate) =>
              update.mutate({
                task,
                patch: { estimate },
                optimistic: { estimate },
              })
            }
          />
        </Row>
      )}
      {iterationSettings.data?.enabled && (
        <Row label={t("spaces.sections.iterations")} compact={additionalOnly}>
          <Picker
            label={t("spaces.sections.iterations")}
            groups={[
              {
                options: (iterations.data?.iterations ?? []).map((item) => ({
                  value: item.id,
                  label: item.display_name,
                })),
              },
            ]}
            selected={task.iteration_id ? [task.iteration_id] : []}
            onSelect={(iteration_id) =>
              update.mutate({ task, patch: { iteration_id } })
            }
            trigger={({ ref, toggle, open }) => (
              <button
                ref={ref}
                type="button"
                aria-expanded={open}
                onClick={toggle}
                className={cn(trigger, !task.iteration_id && "text-fg-muted")}
              >
                <Icon name="repeat" />
                <span className="truncate">
                  {(iterations.data?.iterations ?? []).find(
                    (item) => item.id === task.iteration_id,
                  )?.display_name ?? t("tasks.detail.none")}
                </span>
              </button>
            )}
          />
        </Row>
      )}
      {!additionalOnly && projectsEnabled && (
        <Row label={t("projects.title")}>
          <TaskProjectPicker
            orgId={orgId}
            task={task}
            trigger={(name, { ref, toggle, open }) => (
              <button
                ref={ref}
                type="button"
                aria-expanded={open}
                onClick={toggle}
                className={cn(trigger, !task.project_id && "text-fg-muted")}
              >
                <Icon name="folder" />
                <span className="truncate">
                  {name ?? t("tasks.detail.none")}
                </span>
              </button>
            )}
          />
        </Row>
      )}
      {task.duplicate_of && (
        <Row label={t("tasks.detail.duplicateOf")} compact={additionalOnly}>
          <button
            type="button"
            className={cn(trigger, "text-accent")}
            onClick={() => onOpenTask(task.duplicate_of!.display_id)}
          >
            <Icon name="copy" />
            <span className="truncate">
              {task.duplicate_of.display_id.toUpperCase()}{" "}
              {task.duplicate_of.title}
            </span>
          </button>
        </Row>
      )}
      <Row label={t("tasks.fields.parent")} compact={additionalOnly}>
        <div className="flex items-center">
          <button
            type="button"
            onClick={() =>
              task.parent
                ? onOpenTask(task.parent.display_id)
                : setParentOpen(true)
            }
            className={cn(trigger, !task.parent && "text-fg-muted")}
          >
            <Icon name="git-fork" />
            <span className="truncate">
              {task.parent
                ? `${task.parent.display_id.toUpperCase()} ${task.parent.title}`
                : t("tasks.detail.setParent")}
            </span>
          </button>
          {task.parent && (
            <>
              <IconButton
                label={t("tasks.detail.setParent")}
                icon={<Icon name="pencil" />}
                size="28"
                onClick={() => setParentOpen(true)}
              />
              <IconButton
                label={t("tasks.detail.clearParent")}
                icon={<Icon name="x" />}
                size="28"
                onClick={() =>
                  update.mutate({
                    task,
                    patch: { parent_task_id: null },
                    optimistic: { parent: null, parent_task_id: null },
                  })
                }
              />
            </>
          )}
        </div>
        {parentOpen && (
          <TaskSearchDialog
            orgId={orgId}
            spaceId={task.space_id}
            title={t("tasks.detail.setParent")}
            excludeIds={[task.id]}
            onClose={() => setParentOpen(false)}
            onPick={(parent) => {
              setParentOpen(false);
              update.mutate({
                task,
                patch: { parent_task_id: parent.id },
                optimistic: {
                  parent_task_id: parent.id,
                  parent: {
                    id: parent.id,
                    display_id: parent.display_id,
                    title: parent.title,
                  },
                },
              });
            }}
          />
        )}
      </Row>

      <CustomFields
        orgId={orgId}
        task={task}
        people={people}
        agents={agents}
        compact={additionalOnly}
      />
    </fieldset>
  );
}

// ---------- 自定义字段 ----------

function CustomFields({
  orgId,
  task,
  people,
  agents,
  compact = false,
}: {
  compact?: boolean;
  orgId: string;
  task: Task;
  people: Person[];
  agents: { id: string; name: string }[];
}) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const queryClient = useQueryClient();
  const queryKey = keys.taskSub(orgId, task.id, "fields");
  const fields = useQuery({
    // 类型变更后绑定的字段也会变
    queryKey: [...queryKey, task.type_id],
    queryFn: () => api.tasks.fields(orgId, task.space_id, task.id),
  });
  const save = useMutation({
    mutationFn: async ({
      field,
      value,
    }: {
      field: CustomField;
      value: unknown;
    }) =>
      void (await (value === null ||
      (Array.isArray(value) && value.length === 0) ||
      value === ""
        ? api.tasks.clearField(orgId, task.space_id, task.id, field.id)
        : api.tasks.setField(orgId, task.space_id, task.id, field.id, value))),
    onError: (error) => toast(taskErrorMessage(t, error), { tone: "error" }),
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey });
      void queryClient.invalidateQueries({
        queryKey: keys.taskSub(orgId, task.id, "timeline"),
      });
    },
  });
  const list = fields.data ?? [];
  if (compact && list.length === 0 && !fields.error) return null;
  return (
    <div
      className={cn(
        "flex flex-col border-t border-line-default pt-3",
        compact ? "mt-2 gap-1" : "gap-3",
      )}
    >
      {!compact && (
        <div className="px-2 text-caption text-fg-muted">
          {t("tasks.detail.moreProperties")}
        </div>
      )}
      {fields.data && list.length === 0 && (
        <p className="px-2 text-body-sm text-fg-muted">
          {t("tasks.detail.noFields")}
        </p>
      )}
      {list.map((item) => (
        <Row
          key={item.field.id}
          compact={compact}
          label={
            item.bound
              ? item.field.name
              : `${item.field.name} · ${t("tasks.detail.unbound")}`
          }
        >
          <FieldEditor
            item={item}
            people={people}
            agents={agents}
            onSave={(value) => save.mutate({ field: item.field, value })}
          />
        </Row>
      ))}
    </div>
  );
}

export function FieldEditor({
  item,
  people,
  agents,
  onSave,
}: {
  item: TaskFieldValue;
  people: Person[];
  agents: { id: string; name: string }[];
  onSave: (value: unknown) => void;
}) {
  const t = useT();
  const { locale } = useI18n();
  const { field, value, bound } = item;
  const [dateOpen, setDateOpen] = useState(false);
  const anchor = useRef<HTMLButtonElement | null>(null);
  const disabled = !bound;
  const textValue =
    typeof value === "string"
      ? value
      : typeof value === "number"
        ? String(value)
        : "";
  const [draft, setDraft] = useState(textValue);
  const commitText = (raw: string) => {
    if (raw === textValue) return;
    if (field.kind === "NUMBER") {
      if (raw.trim() === "") return onSave(null);
      const n = Number(raw);
      if (Number.isFinite(n)) onSave(n);
      else setDraft(textValue);
      return;
    }
    onSave(raw.trim() === "" ? null : raw);
  };

  switch (field.kind) {
    case "TEXT":
    case "URL":
    case "NUMBER":
      return (
        <TextInput
          aria-label={field.name}
          type={
            field.kind === "NUMBER"
              ? "number"
              : field.kind === "URL"
                ? "url"
                : "text"
          }
          disabled={disabled}
          value={draft}
          maxLength={field.kind === "URL" ? 2048 : 256}
          onChange={(event) => setDraft(event.target.value)}
          onBlur={() => commitText(draft)}
          onKeyDown={(event) => {
            if (event.key === "Enter") event.currentTarget.blur();
          }}
        />
      );
    case "RICH_TEXT":
      return (
        <Textarea
          aria-label={field.name}
          disabled={disabled}
          value={draft}
          rows={3}
          maxLength={65536}
          onChange={(event) => setDraft(event.target.value)}
          onBlur={() => commitText(draft)}
        />
      );
    case "BOOLEAN":
      return (
        <div className="px-2">
          <Switch
            label={field.name}
            disabled={disabled}
            checked={value === true}
            onCheckedChange={(checked) => onSave(checked)}
          />
        </div>
      );
    case "DATE":
      return (
        <>
          <button
            ref={anchor}
            type="button"
            disabled={disabled}
            onClick={() => setDateOpen(true)}
            className={cn(trigger, !textValue && "text-fg-muted")}
          >
            <Icon name="calendar" />
            {textValue
              ? formatMonthDay(textValue, locale)
              : t("tasks.detail.none")}
          </button>
          <DateFieldPopover
            open={dateOpen}
            onOpenChange={setDateOpen}
            anchorRef={anchor}
            value={textValue || null}
            onChange={(v) => onSave(v)}
          />
        </>
      );
    case "SELECT":
    case "MULTI_SELECT": {
      const multiple = field.kind === "MULTI_SELECT";
      const selected = multiple
        ? Array.isArray(value)
          ? (value as string[])
          : []
        : typeof value === "string"
          ? [value]
          : [];
      const names = field.options.filter((o) => selected.includes(o.id));
      return (
        <Picker
          label={field.name}
          multiple={multiple}
          groups={[
            {
              options: field.options.map((o) => ({
                value: o.id,
                label: o.label,
                icon: <LabelDot color={o.color} />,
              })),
            },
          ]}
          selected={selected}
          onSelect={(id) => {
            if (!multiple) return onSave(selected[0] === id ? null : id);
            onSave(
              selected.includes(id)
                ? selected.filter((x) => x !== id)
                : [...selected, id],
            );
          }}
          trigger={({ ref, toggle, open }) => (
            <button
              ref={ref}
              type="button"
              disabled={disabled}
              aria-expanded={open}
              onClick={toggle}
              className={cn(
                trigger,
                "flex-wrap py-1",
                !names.length && "text-fg-muted",
              )}
            >
              {names.length === 0
                ? t("tasks.detail.none")
                : names.map((o) => (
                    <span
                      key={o.id}
                      className="inline-flex h-5 items-center gap-1 rounded-full border border-line-default px-1.5 text-caption"
                    >
                      <LabelDot color={o.color} />
                      {o.label}
                    </span>
                  ))}
            </button>
          )}
        />
      );
    }
    case "MEMBER": {
      const member = value as { kind?: string; id?: string } | null;
      const current = member?.id
        ? `${member.kind === "AGENT" ? "agent" : "user"}:${member.id}`
        : "none";
      const scope = field.member_scope ?? "ALL";
      const memberPerson = people.find((p) => p.id === member?.id);
      const name =
        member?.kind === "AGENT"
          ? agents.find((a) => a.id === member.id)?.name
          : memberPerson && personName(memberPerson);
      return (
        <Picker
          label={field.name}
          groups={assigneeGroups(t, people, agents, {
            humans: scope !== "AGENTS",
            agents: scope !== "HUMANS",
          })}
          selected={[current]}
          onSelect={(v) => {
            if (v === "none") return onSave(null);
            const [kind, id] = v.split(":");
            onSave({ kind: kind === "agent" ? "AGENT" : "HUMAN", id });
          }}
          trigger={({ ref, toggle, open }) => (
            <button
              ref={ref}
              type="button"
              disabled={disabled}
              aria-expanded={open}
              onClick={toggle}
              className={cn(trigger, !name && "text-fg-muted")}
            >
              {name ? (
                <Avatar
                  name={name}
                  kind={member?.kind === "AGENT" ? "agent" : "user"}
                  size={20}
                />
              ) : (
                <Icon name="user" />
              )}
              <span className="truncate">{name ?? t("tasks.detail.none")}</span>
            </button>
          )}
        />
      );
    }
    default:
      return null;
  }
}
