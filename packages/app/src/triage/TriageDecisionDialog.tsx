import { useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useNavigate } from "react-router-dom";
import type { Task, TriageAcceptInput } from "@openfox/api";
import { useI18n, useT } from "@openfox/i18n";
import {
  Button,
  DateFieldPopover,
  Dialog,
  Icon,
  IconButton,
  Menu,
  MenuItem,
  Switch,
  useToast,
} from "@openfox/ui";
import { keys, useApi } from "../context";
import { ProjectPicker } from "../projects/ProjectPicker";
import { useSpaceDetail } from "../spaces/queries";
import { useSpaceEstimation, EstimateInput } from "../spaces/EstimateField";
import { usePriorityLevels } from "../organization/queries";
import { displayIdOf } from "../tasks/parts";
import { isPersonAssignee, useInvitationAssignees } from "../tasks/invitationAssignees";
import {
  Picker,
  PropertyChip,
  assigneeGroups,
  refFromValue,
  assigneeValue,
  labelGroups,
  toggleLabel,
  priorityGroups,
  type PickerTriggerProps,
} from "../tasks/pickers";
import { PriorityIcon } from "../tasks/primitives/PriorityIcon";
import { StatusIcon } from "../tasks/primitives/StatusIcon";
import { formatMonthDay } from "../tasks/primitives/dates";
import {
  useAgentDirectory,
  usePeople,
  usePickerLabels,
  useSpaceAgents,
  useSpaceTypes,
  useStatuses,
} from "../tasks/queries";
import { FieldEditor } from "../tasks/detail/Properties";
import {
  useTriageAccept,
  useTriageDecline,
  useTriageSettings,
  triageErrorMessage,
} from "./queries";

export function TriageDecisionDialog({
  orgId,
  task,
  action,
  onClose,
  onProcessed,
}: {
  orgId: string;
  task: Task;
  action: "accept" | "decline";
  onClose: () => void;
  onProcessed?: () => void;
}) {
  const t = useT();
  const toast = useToast();
  const [comment, setComment] = useState("");
  const [subscribe, setSubscribe] = useState(true);
  const [draft, setDraft] = useState<Partial<TriageAcceptInput>>({});
  const accept = useTriageAccept(orgId, task.space_id);
  const decline = useTriageDecline(orgId, task.space_id);
  const settings = useTriageSettings(orgId, task.space_id);
  const types = useSpaceTypes(orgId, task.space_id);
  const flow =
    types.data?.find((type) => type.id === task.type_id)?.statuses ?? [];
  const after = flow.findIndex((status) => status.status_id === task.status_id);
  const defaultStatus =
    flow.slice(after + 1).find((s) => s.category !== "TRIAGE") ??
    flow.find((s) => s.default && s.category !== "TRIAGE");
  const chosenStatus = draft.status_id ?? defaultStatus?.status_id;
  const [priorityOpen, setPriorityOpen] = useState(false);
  const needsPriority =
    !!settings.data?.require_priority &&
    (draft.priority ?? task.priority) === "NONE";
  useEffect(() => {
    if (action === "accept" && needsPriority) setPriorityOpen(true);
  }, [action, needsPriority]);
  const onSuccess = () => {
    onProcessed?.();
    toast(
      t(
        action === "accept"
          ? "spaces.triage.accepted"
          : "spaces.triage.declined",
      ),
    );
    onClose();
  };
  const pending = accept.isPending || decline.isPending;
  const submit = () => {
    if (pending) return;
    const common = {
      version: task.version,
      subscribe,
      ...(comment.trim() ? { comment: comment.trim() } : {}),
    };
    if (action === "decline") {
      decline.mutate({ taskId: task.id, input: common }, { onSuccess });
      return;
    }
    if (needsPriority) {
      setPriorityOpen(true);
      return;
    }
    accept.mutate(
      {
        taskId: task.id,
        input: { ...common, ...draft, status_id: chosenStatus },
      },
      {
        onSuccess,
        onError: (error) => {
          if (
            typeof error === "object" &&
            error &&
            "code" in error &&
            error.code === "PRIORITY_REQUIRED"
          )
            setPriorityOpen(true);
        },
      },
    );
  };
  return (
    <Dialog
      open
      onClose={pending ? undefined : onClose}
      width={672}
      className="gap-0! rounded-[20px]! px-5! pt-5! pb-4! [&_h2]:text-base"
      title={
        t(`spaces.triage.${action}`) +
        "：" +
        displayIdOf(task) +
        " " +
        task.title
      }
      footer={
        <div className="flex w-full items-center justify-between pt-2">
          <label className="flex items-center gap-2 text-caption text-fg-muted">
            <Switch
              className="h-3.5! w-6! [&>span]:top-px [&>span]:size-3 [&>span]:left-px aria-checked:[&>span]:left-[11px]"
              label={t("spaces.triage.subscribe")}
              tone="success"
              checked={subscribe}
              onCheckedChange={setSubscribe}
            />
            {t("spaces.triage.subscribe")}
          </label>
          <Button
            size="28"
            className="min-w-[72px] rounded-full!"
            loading={pending}
            disabled={action === "accept" && !chosenStatus}
            onClick={submit}
          >
            {t(`spaces.triage.${action}`)}
          </Button>
        </div>
      }
    >
      <textarea
        data-autofocus
        aria-label={t("spaces.triage.comment")}
        placeholder={t(
          action === "accept"
            ? "spaces.triage.commentPlaceholder"
            : "spaces.triage.declinePlaceholder",
        )}
        value={comment}
        onChange={(e) => setComment(e.target.value)}
        maxLength={10000}
        className="mt-3 block min-h-20 max-h-48 w-full resize-none [field-sizing:content] bg-transparent py-2 text-body outline-none focus-visible:shadow-none placeholder:text-fg-subtle"
      />
      {action === "accept" && (
        <AcceptFields
          orgId={orgId}
          task={task}
          draft={draft}
          patch={(fields) => setDraft((prev) => ({ ...prev, ...fields }))}
          flow={flow}
          chosenStatus={chosenStatus}
          priorityOpen={priorityOpen}
          setPriorityOpen={setPriorityOpen}
          priorityRequired={!!settings.data?.require_priority}
        />
      )}
      {(accept.error || decline.error) && (
        <p
          role="alert"
          className="mt-2 rounded-lg bg-danger/5 px-3 py-2 text-caption text-danger"
        >
          {triageErrorMessage(t, accept.error ?? decline.error)}
        </p>
      )}
    </Dialog>
  );
}

function AcceptFields({
  orgId,
  task,
  draft,
  patch,
  flow,
  chosenStatus,
  priorityOpen,
  setPriorityOpen,
  priorityRequired,
}: {
  orgId: string;
  task: Task;
  draft: Partial<TriageAcceptInput>;
  patch: (v: Partial<TriageAcceptInput>) => void;
  flow: { status_id: string; category: string }[];
  chosenStatus?: string;
  priorityOpen: boolean;
  setPriorityOpen: (v: boolean) => void;
  priorityRequired: boolean;
}) {
  const t = useT(),
    { locale } = useI18n(),
    api = useApi(),
    navigate = useNavigate();
  const space = useSpaceDetail(orgId, task.space_id).data,
    people = usePeople(orgId, space);
  const agents = (useSpaceAgents(orgId, task.space_id).data ?? []).filter(
    (a) => !a.archived_at && a.allow_assign !== false && !a.builtin_kind,
  );
  const crews = (useAgentDirectory(orgId).data?.entries ?? []).filter(
    (a) => a.type === "crew" && a.space_id === task.space_id && !a.archived_at,
  );
  const assignees = draft.assignees ?? task.assignees,
    assigned = assignees.map(assigneeValue);
  const [assignOpen, setAssignOpen] = useState(false);
  const { candidates: invitations } = useInvitationAssignees(
    orgId,
    assignOpen,
    [task.space_id],
  );
  const groups = assigneeGroups(t, people, agents, {
    none: false,
    invitations,
  });
  if (crews.length)
    groups.push({
      id: "crew",
      label: t("agents.crewBadge"),
      options: crews.map((a) => ({ value: "crew:" + a.id, label: a.name })),
    });
  const getChip =
    (name: string, icon: React.ReactNode, label = name) =>
    (p: PickerTriggerProps) => (
      <PropertyChip
        ref={p.ref}
        active={p.open}
        muted={label === name}
        onClick={p.toggle}
        icon={icon}
        label={label}
      >
        {name}
      </PropertyChip>
    );
  const statuses = useStatuses(orgId).data ?? [],
    statusMap = new Map(statuses.map((s) => [s.id, s])),
    status = statusMap.get(chosenStatus ?? "");
  const labels = usePickerLabels(orgId, task.space_id).data ?? [],
    labelIds = draft.label_ids ?? task.label_items.map((l) => l.id);
  const priority = draft.priority ?? task.priority,
    priorities = priorityGroups(t, usePriorityLevels());
  if (priorityRequired)
    for (const group of priorities)
      group.options = group.options.filter((o) => o.value !== "NONE");
  const project =
    draft.project_id !== undefined ? draft.project_id : task.project_id;
  const iteration =
    draft.iteration_id !== undefined ? draft.iteration_id : task.iteration_id;
  const iterations =
    useQuery({
      queryKey: keys.iterations(orgId, task.space_id, {
        status: "planned,active",
      }),
      queryFn: () =>
        api.iterations.list(orgId, task.space_id, { status: "planned,active" }),
      enabled: !!space?.features.iterations,
    }).data?.iterations ?? [];
  const estimation = useSpaceEstimation(orgId, task.space_id);
  const fields =
    useQuery({
      queryKey: [...keys.taskSub(orgId, task.id, "fields"), task.type_id],
      queryFn: () => api.tasks.fields(orgId, task.space_id, task.id),
    }).data?.filter((item) => item.bound) ?? [];
  const [dateOpen, setDateOpen] = useState<"start_date" | "due_date" | null>(
    null,
  );
  const [shown, setShown] = useState<string[]>([]);
  const startAnchor = useRef<HTMLElement | null>(null),
    dueAnchor = useRef<HTMLElement | null>(null);
  const dateLabel = (key: "start_date" | "due_date") =>
    t(key === "start_date" ? "tasks.fields.startDate" : "tasks.fields.dueDate");
  return (
    <div className="mt-2 mb-2 flex flex-wrap items-center gap-1.5 [&_button[aria-expanded]]:h-[26px] [&_button[aria-expanded]]:text-caption">
      <Picker
        label={t("tasks.fields.assignee")}
        open={assignOpen}
        onOpenChange={setAssignOpen}
        groups={groups}
        multiple
        closeOnSelect
        selected={assigned}
        onSelect={(v) => {
          const ref = refFromValue(v);
          if (!ref) return;
          const keep = assignees.filter(
            (a) => !(a.id === ref.id && a.kind === ref.kind),
          );
          const other = keep.filter((a) =>
            isPersonAssignee(ref.kind)
              ? space?.multi_assignee || !isPersonAssignee(a.kind)
              : isPersonAssignee(a.kind),
          );
          patch({
            assignees: (assigned.includes(v) ? keep : [...other, ref]).map(
              ({ kind, id }) => ({ kind, id }),
            ),
          });
        }}
        trigger={getChip(
          groups
            .flatMap((g) => g.options)
            .filter((o) => assigned.includes(o.value))
            .map((o) => o.label)
            .join("、") || t("tasks.fields.assignee"),
          <Icon name="users" size={14} />,
          t("tasks.fields.assignee"),
        )}
      />
      <Picker
        label={t("tasks.fields.status")}
        groups={[
          {
            options: flow.flatMap((w) => {
              const s = statusMap.get(w.status_id);
              return !s || s.archived_at
                ? []
                : [
                    {
                      value: s.id,
                      label: s.name,
                      disabled: s.category === "TRIAGE",
                      icon: (
                        <StatusIcon category={s.category} color={s.color} />
                      ),
                    },
                  ];
            }),
          },
        ]}
        selected={chosenStatus ? [chosenStatus] : []}
        onSelect={(id) => patch({ status_id: id })}
        moreOptions={{
          label: t("spaces.triage.manageStatuses"),
          onClick: () => navigate(`/o/${orgId}/settings/statuses`),
        }}
        trigger={getChip(
          status?.name ?? t("tasks.fields.status"),
          status && (
            <StatusIcon
              category={status.category}
              color={status.color}
              size={14}
            />
          ),
          t("tasks.fields.status"),
        )}
      />
      <Picker
        label={t("tasks.fields.priority")}
        groups={priorities}
        open={priorityOpen}
        onOpenChange={setPriorityOpen}
        selected={[priority]}
        onSelect={(v) => patch({ priority: v as Task["priority"] })}
        trigger={getChip(
          priority === "NONE"
            ? t("tasks.fields.priority")
            : t(`tasks.priority.${priority}`),
          <PriorityIcon priority={priority} />,
          t("tasks.fields.priority"),
        )}
      />
      {space?.features.projects && (
        <ProjectPicker
          orgId={orgId}
          spaceId={task.space_id}
          value={project ?? null}
          onSelect={(v) => patch({ project_id: v })}
          trigger={(name, p) =>
            getChip(
              name ?? t("projects.title"),
              <Icon name="folder" size={14} />,
            )(p)
          }
        />
      )}
      {space?.features.iterations && (
        <Picker
          label={t("spaces.sections.iterations")}
          groups={[
            {
              options: [
                { value: "none", label: t("tasks.detail.none") },
                ...iterations.map((it) => ({
                  value: it.id,
                  label: it.display_name,
                })),
              ],
            },
          ]}
          selected={[iteration ?? "none"]}
          onSelect={(v) => patch({ iteration_id: v === "none" ? null : v })}
          trigger={getChip(
            iterations.find((it) => it.id === iteration)?.display_name ??
              t("spaces.sections.iterations"),
            <Icon name="repeat" size={14} />,
          )}
        />
      )}
      {estimation && (
        <EstimateInput
          estimation={estimation}
          value={draft.estimate !== undefined ? draft.estimate : task.estimate}
          onChange={(v) => patch({ estimate: v })}
          className="h-7 max-w-32 rounded-full text-caption"
        />
      )}
      <Picker
        label={t("tasks.fields.label")}
        groups={labelGroups(labels)}
        multiple
        selected={labelIds}
        onSelect={(id) =>
          patch({ label_ids: toggleLabel(labelIds, id, labels) })
        }
        trigger={getChip(
          labels
            .filter((l) => labelIds.includes(l.id))
            .map((l) => l.name)
            .join("、") || t("tasks.fields.label"),
          <Icon name="tag" size={14} />,
          t("tasks.fields.label"),
        )}
      />
      {(["start_date", "due_date"] as const).map((key) => {
        const value = draft[key] !== undefined ? draft[key] : task[key];
        const anchor = key === "start_date" ? startAnchor : dueAnchor;
        return (
          (shown.includes(key) || !!value) && (
            <div key={key}>
              <PropertyChip
                ref={(n) => {
                  anchor.current = n;
                }}
                onClick={() => setDateOpen(key)}
                icon={<Icon name="calendar" size={14} />}
                label={dateLabel(key)}
              >
                {value ? formatMonthDay(value, locale) : dateLabel(key)}
              </PropertyChip>
              <DateFieldPopover
                open={dateOpen === key}
                onOpenChange={(open) => setDateOpen(open ? key : null)}
                anchorRef={anchor}
                value={value ?? null}
                onChange={(v) => patch({ [key]: v })}
              />
            </div>
          )
        );
      })}
      {fields
        .filter((f) => shown.includes(f.field.id) || f.value != null)
        .map((f) => (
          <label
            key={f.field.id}
            className="flex items-center gap-1 text-caption"
          >
            {f.field.name}
            <FieldEditor
              item={{
                ...f,
                value:
                  draft.field_values && f.field.id in draft.field_values
                    ? draft.field_values[f.field.id]
                    : f.value,
              }}
              people={people}
              agents={agents}
              onSave={(v) =>
                patch({
                  field_values: { ...draft.field_values, [f.field.id]: v },
                })
              }
            />
          </label>
        ))}
      <Menu
        label={t("tasks.detail.moreProperties")}
        trigger={({ ref, toggle, open }) => (
          <IconButton
            ref={ref}
            active={open}
            icon={<Icon name="ellipsis" />}
            label={t("tasks.detail.moreProperties")}
            size="28"
            variant="round"
            onClick={toggle}
          />
        )}
      >
        {(["start_date", "due_date"] as const).map((key) => (
          <MenuItem
            key={key}
            onSelect={() => {
              setShown((v) => [...v, key]);
              setDateOpen(key);
            }}
          >
            {dateLabel(key)}
          </MenuItem>
        ))}
        {fields.map((f) => (
          <MenuItem
            key={f.field.id}
            onSelect={() => setShown((v) => [...v, f.field.id])}
          >
            {f.field.name}
          </MenuItem>
        ))}
      </Menu>
    </div>
  );
}
