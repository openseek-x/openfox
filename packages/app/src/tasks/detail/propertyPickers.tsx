import { useState, type ReactNode } from "react";

import type { Label, SpaceAgent, Task, TaskStatus } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Icon, cn, type SearchMenuGroup } from "@openfox/ui";

import { AgentAvatar } from "../../agents/AgentAvatar";
import { useReadOnly } from "../../spaces/readonly";
import { usePriorityLevels } from "../../organization/queries";
import { ProjectPicker } from "../../projects/ProjectPicker";
import { useSpaceDetail } from "../../spaces/queries";
import {
  Picker,
  assigneeGroups,
  assigneeValue,
  refFromValue,
  labelGroups,
  priorityGroups,
  statusGroups,
  toggleLabel,
  type PickerTriggerProps,
} from "../pickers";
import type { Priority } from "../primitives/PriorityIcon";
import { useAgentDirectory, useUpdateTask, type Person } from "../queries";
import { statusFields } from "../TaskMenu";
import {
  TaskAssigneeAvatar,
  isPersonAssignee,
  useInvitationAssignees,
} from "../invitationAssignees";
import { useSetTaskAssignee } from "./assign";

// 处理人 / 执行 Agent / 状态 / 优先级 / 专项选择器：从 Properties 抽出，
// 详情右栏的属性行与窄宽度下的属性按钮行共用同一份 groups、selected、onSelect 与乐观更新。

type Trigger = (props: PickerTriggerProps) => ReactNode;

export type AssigneeAgent = Pick<SpaceAgent, "id" | "name"> &
  Partial<
    Pick<SpaceAgent, "avatar" | "allow_assign" | "archived_at" | "builtin_kind">
  >;

interface AssigneeNames {
  people: Person[];
  agents: AssigneeAgent[];
}

/** 人员、Agent、小队共用入口；选择或清除时只修改对应的类型。 */
export function AssigneePicker({
  orgId,
  task,
  people,
  agents,
  trigger,
}: { orgId: string; task: Task; trigger: Trigger } & AssigneeNames) {
  const t = useT();
  const readOnly = useReadOnly();
  const writeAssignee = useSetTaskAssignee(orgId);
  const crews = (useAgentDirectory(orgId).data?.entries ?? []).filter(
    (entry) =>
      entry.type === "crew" &&
      entry.space_id === task.space_id &&
      !entry.archived_at,
  );
  const [open, setOpen] = useState(false);
  const { candidates: invitations } = useInvitationAssignees(orgId, open, [
    task.space_id,
  ]);
  const selected = task.assignees.map(assigneeValue);
  if (!task.assignees.some((a) => isPersonAssignee(a.kind)))
    selected.push("none");
  if (!task.assignees.some((a) => a.kind === "AGENT"))
    selected.push("no-agent");
  const groups: SearchMenuGroup[] = [
    ...assigneeGroups(t, [], [], { agents: false, humans: false }),
    ...(crews.length
      ? [
          {
            id: "crews",
            label: t("agents.crewBadge"),
            options: crews.map((crew) => ({
              value: `crew:${crew.id}`,
              label: crew.name,
              icon: <Icon name="users" />,
            })),
          },
        ]
      : []),
    {
      id: "agents",
      label: t("tasks.filters.agents"),
      options: [
        {
          value: "no-agent",
          label: t("tasks.detail.noAgent"),
          icon: <Icon name="bot" />,
        },
        ...agents
          .filter(
            (agent) =>
              !agent.archived_at &&
              !agent.builtin_kind &&
              agent.allow_assign !== false,
          )
          .map((agent) => ({
            value: `agent:${agent.id}`,
            label: agent.name,
            icon: (
              <AgentAvatar name={agent.name} avatar={agent.avatar} size={20} />
            ),
          })),
      ],
    },
    ...assigneeGroups(t, people, [], {
      none: false,
      agents: false,
      invitations,
    }),
  ];
  return (
    <Picker
      label={t("tasks.fields.assignee")}
      open={open}
      onOpenChange={setOpen}
      placeholder={t("tasks.detail.assignTo")}
      multiple
      closeOnSelect
      groups={groups}
      selected={selected}
      onSelect={(value) => {
        if (readOnly || selected.includes(value)) return;
        const kind =
          value === "no-agent"
            ? "AGENT"
            : (refFromValue(value)?.kind ?? "HUMAN");
        writeAssignee(task, value === "no-agent" ? "none" : value, kind, {
          people,
          agents: [...agents, ...crews],
          invitations,
        });
      }}
      trigger={trigger}
    />
  );
}

/** 同一个按钮里显示人员 → 执行 Agent，宽窄详情共享。 */
export function AssigneeContent({
  task,
  people,
  agents,
  stacked = false,
}: { task: Task; stacked?: boolean } & AssigneeNames) {
  const t = useT();
  const humans = task.assignees.filter((a) => isPersonAssignee(a.kind));
  const agent = task.assignees.find((a) => a.kind === "AGENT");
  const crew = task.assignees.find((a) => a.kind === "CREW");
  return (
    <span
      className={cn(
        "flex min-w-0",
        stacked ? "flex-col gap-2" : "items-center gap-2",
      )}
    >
      {humans.length ? (
        humans.map((human) => {
          const person =
            human.kind === "HUMAN"
              ? people.find((item) => item.id === human.id)
              : undefined;
          const name = person?.name || human.name;
          return (
            <span
              key={assigneeValue(human)}
              className="flex min-w-0 items-center gap-2"
            >
              <TaskAssigneeAvatar
                kind={human.kind}
                name={name}
                src={person?.avatar_url}
                size={20}
              />
              <span className="truncate">{name}</span>
              {human.kind === "INVITATION" && (
                <span className="shrink-0 text-caption text-fg-muted">
                  {t("tasks.invitedBadge")}
                </span>
              )}
            </span>
          );
        })
      ) : task.external_assignee ? (
        <span data-testid="external-assignee" className="truncate">
          {t("imports.externalAssignee", { name: task.external_assignee.name })}
          {task.external_assignee.pending_invitation &&
            ` · ${t("imports.externalPending")}`}
        </span>
      ) : (
        <span className="flex min-w-0 items-center gap-2">
          <Icon name="user" />
          <span className="truncate">{t("tasks.noAssignee")}</span>
        </span>
      )}
      {crew && (
        <span className="flex min-w-0 items-center gap-2">
          <Icon name="users" />
          <span className="truncate">{crew.name}</span>
        </span>
      )}
      {agent && (
        <span
          className={cn("flex min-w-0 items-center gap-2", stacked && "pl-2")}
        >
          <Icon
            name="arrow-right"
            size={12}
            className="shrink-0 text-fg-muted"
          />
          <AgentAvatar
            name={agent.name}
            avatar={agents.find((item) => item.id === agent.id)?.avatar}
            size={20}
          />
          <span className="truncate">{agent.name}</span>
        </span>
      )}
    </span>
  );
}

/** 状态：只列任务类型工作流里的状态。 */
export function StatusPicker({
  orgId,
  task,
  workflow,
  trigger,
}: {
  orgId: string;
  task: Task;
  workflow: TaskStatus[];
  trigger: Trigger;
}) {
  const t = useT();
  const update = useUpdateTask(orgId);
  return (
    <Picker
      label={t("tasks.fields.status")}
      groups={statusGroups(workflow)}
      selected={[task.status_id]}
      onSelect={(id) => {
        const status = workflow.find((s) => s.id === id);
        if (status && id !== task.status_id)
          update.mutate({
            task,
            patch: { status_id: id },
            optimistic: statusFields(status),
          });
      }}
      trigger={trigger}
    />
  );
}

/** 优先级：组织配置的优先级档位。 */
export function PriorityPicker({
  orgId,
  task,
  trigger,
}: {
  orgId: string;
  task: Task;
  trigger: Trigger;
}) {
  const t = useT();
  const priorityLevels = usePriorityLevels();
  const update = useUpdateTask(orgId);
  return (
    <Picker
      label={t("tasks.fields.priority")}
      groups={priorityGroups(t, priorityLevels)}
      selected={[task.priority]}
      onSelect={(v) => {
        if (v !== task.priority)
          update.mutate({ task, patch: { priority: v as Priority } });
      }}
      trigger={trigger}
    />
  );
}

/** 标签：宽属性栏与窄属性按钮共用分组互斥规则和乐观更新。 */
export function TaskLabelPicker({
  orgId,
  task,
  labels,
  trigger,
}: {
  orgId: string;
  task: Task;
  labels: Label[];
  trigger: Trigger;
}) {
  const t = useT();
  const update = useUpdateTask(orgId);
  const chosen = task.label_items;
  return (
    <Picker
      label={t("tasks.fields.label")}
      multiple
      groups={labelGroups(labels)}
      selected={chosen.map((label) => label.id)}
      onSelect={(id) => {
        const ids = toggleLabel(
          chosen.map((label) => label.id),
          id,
          labels,
        );
        const items = ids
          .map(
            (id) =>
              labels.find((label) => label.id === id) ??
              chosen.find((label) => label.id === id),
          )
          .filter((label): label is NonNullable<typeof label> => Boolean(label))
          .map(({ id, name, color, group_id, space_id }) => ({
            id,
            name,
            color,
            group_id,
            space_id,
          }));
        update.mutate({
          task,
          patch: { label_ids: ids },
          optimistic: { label_items: items },
        });
      }}
      trigger={trigger}
    />
  );
}

/** 空间是否启用专项（专项选择器只在启用时显示）。 */
export function useProjectsEnabled(orgId: string, spaceId: string) {
  return Boolean(useSpaceDetail(orgId, spaceId).data?.features.projects);
}

/** 专项：trigger 额外拿到当前专项名称。 */
export function TaskProjectPicker({
  orgId,
  task,
  trigger,
}: {
  orgId: string;
  task: Task;
  trigger: (name: string | undefined, props: PickerTriggerProps) => ReactNode;
}) {
  const update = useUpdateTask(orgId);
  return (
    <ProjectPicker
      orgId={orgId}
      spaceId={task.space_id}
      value={task.project_id}
      onSelect={(project_id) => update.mutate({ task, patch: { project_id } })}
      trigger={trigger}
    />
  );
}
