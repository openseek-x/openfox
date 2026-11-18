import type { Project, ProjectStatus, TaskPriority } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Avatar, Badge, Icon, type BadgeTone } from "@openfox/ui";

import { PropertyChip, Picker, priorityGroups } from "../tasks/pickers";
import { PriorityIcon } from "../tasks/primitives/PriorityIcon";
import type { Person } from "../tasks/queries";
import { usePriorityLevels } from "../organization/queries";

export const PROJECT_STATUSES: ProjectStatus[] = [
  "backlog",
  "planned",
  "in_progress",
  "completed",
  "canceled",
];

const TONE: Record<ProjectStatus, BadgeTone> = {
  backlog: "muted",
  planned: "neutral",
  in_progress: "info",
  completed: "success",
  canceled: "muted",
};

export function ProjectStatusBadge({ status }: { status: ProjectStatus }) {
  const t = useT();
  return <Badge tone={TONE[status]}>{t(`projects.status.${status}`)}</Badge>;
}

/** 专项图标：表情，没有时显示默认文件夹图标。 */
export function ProjectIcon({
  icon,
  size = 16,
}: {
  icon: string | null;
  size?: number;
}) {
  return icon ? (
    <span aria-hidden style={{ fontSize: size }} className="leading-none">
      {icon}
    </span>
  ) : (
    <Icon name="folder" size={size} className="text-fg-secondary" />
  );
}

export function ProgressBar({ progress }: { progress: Project["progress"] }) {
  const percent = progress.total
    ? Math.round((progress.done / progress.total) * 100)
    : 0;
  return (
    <div className="flex items-center gap-2">
      <div
        role="progressbar"
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={percent}
        className="h-1.5 w-24 overflow-hidden rounded-full bg-surface-muted"
      >
        <div className="h-full bg-accent" style={{ width: `${percent}%` }} />
      </div>
      <span className="text-body-sm text-fg-muted">
        {progress.done}/{progress.total}
      </span>
    </div>
  );
}

/** 属性胶囊（状态 / 优先级 / 负责人）：新建专项与专项详情共用，外观同新建任务的属性胶囊。 */
export function ProjectPropertyChips({
  status,
  priority,
  lead,
  people,
  disabled,
  onStatus,
  onPriority,
  onLead,
}: {
  status: ProjectStatus;
  priority: TaskPriority;
  /** 用户 id，空串表示没有负责人 */
  lead: string;
  people: Person[];
  disabled?: boolean;
  onStatus: (value: ProjectStatus) => void;
  onPriority: (value: TaskPriority) => void;
  onLead: (value: string) => void;
}) {
  const t = useT();
  const priorityLevels = usePriorityLevels();
  const leadName = people.find((person) => person.id === lead)?.email;
  const priorityLabel = (value: TaskPriority) =>
    value === "NONE" ? t("tasks.priorityNone") : t(`tasks.priority.${value}`);
  return (
    <>
      <Picker
        label={t("projects.statusLabel")}
        groups={[
          {
            options: PROJECT_STATUSES.map((value) => ({
              value,
              label: t(`projects.status.${value}`),
            })),
          },
        ]}
        selected={[status]}
        onSelect={(value) => onStatus(value as ProjectStatus)}
        trigger={({ ref, toggle, open }) => (
          <PropertyChip
            ref={ref}
            active={open}
            onClick={disabled ? undefined : toggle}
            label={t("projects.statusLabel")}
          >
            {t(`projects.status.${status}`)}
          </PropertyChip>
        )}
      />
      <Picker
        label={t("projects.priority")}
        groups={priorityGroups(t, priorityLevels)}
        selected={[priority]}
        onSelect={(value) => onPriority(value as TaskPriority)}
        trigger={({ ref, toggle, open }) => (
          <PropertyChip
            ref={ref}
            active={open}
            onClick={disabled ? undefined : toggle}
            label={t("projects.priority")}
            icon={<PriorityIcon priority={priority} />}
          >
            {priorityLabel(priority)}
          </PropertyChip>
        )}
      />
      <Picker
        label={t("projects.lead")}
        groups={[
          {
            options: [
              { value: "", label: t("projects.noLead") },
              ...people.map((person) => ({
                value: person.id,
                label: person.email,
              })),
            ],
          },
        ]}
        selected={[lead]}
        onSelect={onLead}
        trigger={({ ref, toggle, open }) => (
          <PropertyChip
            ref={ref}
            active={open}
            onClick={disabled ? undefined : toggle}
            label={t("projects.lead")}
            muted={!leadName}
            icon={
              leadName ? (
                <Avatar name={leadName} size={16} />
              ) : (
                <Icon name="user" size={14} />
              )
            }
          >
            {leadName ?? t("projects.lead")}
          </PropertyChip>
        )}
      />
    </>
  );
}
