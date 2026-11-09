import { useRef, useState, type ReactNode } from "react";

import type {
  Label,
  Invitation,
  SpaceAgent,
  TaskAssignee,
  TaskAssigneeRef,
  TaskStatus,
} from "@openfox/api";
import type { Translate } from "@openfox/i18n";
import {
  Avatar,
  Icon,
  SearchMenu,
  type Placement,
  type SearchMenuGroup,
  type SearchMenuProps,
} from "@openfox/ui";

import { PRIORITIES, PriorityIcon } from "./primitives/PriorityIcon";
import { StatusIcon } from "./primitives/StatusIcon";
import { TaskAssigneeAvatar, isPersonAssignee } from "./invitationAssignees";
import { personName, type Person } from "./queries";

export interface PickerTriggerProps {
  ref: (node: HTMLElement | null) => void;
  open: boolean;
  toggle: () => void;
}

/** 触发按钮 + 带搜索的选择菜单。单选选中后自动关闭。 */
export function Picker({
  trigger,
  placement,
  open: controlledOpen,
  onOpenChange,
  ...menu
}: Omit<SearchMenuProps, "open" | "onOpenChange" | "anchorRef"> & {
  trigger: (props: PickerTriggerProps) => ReactNode;
  placement?: Placement;
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
}) {
  const [localOpen, setLocalOpen] = useState(false);
  const open = controlledOpen ?? localOpen;
  const setOpen = onOpenChange ?? setLocalOpen;
  const anchor = useRef<HTMLElement | null>(null);
  return (
    <>
      {trigger({
        ref: (node) => {
          anchor.current = node;
        },
        open,
        toggle: () => setOpen(!open),
      })}
      <SearchMenu
        {...menu}
        open={open}
        onOpenChange={setOpen}
        anchorRef={anchor}
        placement={placement}
      />
    </>
  );
}

export function statusGroups(statuses: TaskStatus[]): SearchMenuGroup[] {
  return [
    {
      options: statuses.map((s) => ({
        value: s.id,
        label: s.name,
        icon: <StatusIcon category={s.category} color={s.color} />,
      })),
    },
  ];
}

/** 优先级候选；组织为 4 级时不含「最低」（levels 来自 usePriorityLevels）。 */
export function priorityGroups(
  t: Translate,
  levels: 4 | 5 = 4,
): SearchMenuGroup[] {
  return [
    {
      options: [...PRIORITIES]
        .reverse()
        .filter((p) => levels === 5 || p !== "LOWEST")
        .map((p) => ({
          value: p,
          label:
            p === "NONE" ? t("tasks.priorityNone") : t(`tasks.priority.${p}`),
          icon: <PriorityIcon priority={p} />,
        })),
    },
  ];
}

/** 处理人选项值：user:<id> / agent:<id> / none */
export function assigneeValue(a: Pick<TaskAssignee, "kind" | "id">): string {
  return `${a.kind === "HUMAN" ? "user" : a.kind.toLowerCase()}:${a.id}`;
}

export function refFromValue(value: string): TaskAssigneeRef | null {
  const [kind, id] = value.split(":");
  if (!id) return null;
  if (kind === "user") return { kind: "HUMAN", id };
  if (kind === "invitation") return { kind: "INVITATION", id };
  if (kind === "agent") return { kind: "AGENT", id };
  if (kind === "crew") return { kind: "CREW", id };
  return null;
}

export function assigneeGroups(
  t: Translate,
  people: Person[],
  agents: Pick<SpaceAgent, "id" | "name">[],
  options: {
    none?: boolean;
    humans?: boolean;
    agents?: boolean;
    invitations?: Invitation[];
  } = {},
): SearchMenuGroup[] {
  const { none = true, humans = true } = options;
  const withAgents = options.agents ?? true;
  const groups: SearchMenuGroup[] = [];
  if (none)
    groups.push({
      id: "none",
      options: [
        {
          value: "none",
          label: t("tasks.noAssignee"),
          icon: <Icon name="user-x" />,
        },
      ],
    });
  if (withAgents && agents.length)
    groups.push({
      id: "agents",
      label: t("tasks.filters.agents"),
      options: agents.map((a) => ({
        value: `agent:${a.id}`,
        label: a.name,
        icon: <Avatar name={a.name} kind="agent" size={20} />,
      })),
    });
  if (humans && people.length)
    groups.push({
      id: "people",
      label: t("tasks.filters.people"),
      options: people.map((p) => ({
        value: `user:${p.id}`,
        label: personName(p),
        // 有全名时邮箱作为说明
        description: p.name ? p.email : undefined,
        icon: <Avatar name={personName(p)} src={p.avatar_url} size={20} />,
      })),
    });
  if (humans && options.invitations?.length)
    groups.push({
      id: "invitations",
      label: t("tasks.invitedGroup"),
      options: options.invitations.map((invite) => ({
        value: `invitation:${invite.id}`,
        label: invite.email,
        trailing: t("tasks.invitedBadge"),
        icon: (
          <TaskAssigneeAvatar kind="INVITATION" name={invite.email} size={20} />
        ),
      })),
    });
  return groups;
}

export function labelGroups(labels: Label[]): SearchMenuGroup[] {
  return [
    {
      options: labels
        .filter((l) => !l.archived_at)
        .map((l) => ({
          value: l.id,
          label: l.name,
          icon: <LabelDot color={l.color} />,
        })),
    },
  ];
}

/** 切换标签选择；选中单选分组里的标签时，替换同组已选的其他标签。 */
export function toggleLabel(
  selected: string[],
  labelId: string,
  labels: Pick<Label, "id" | "group_id">[],
): string[] {
  if (selected.includes(labelId))
    return selected.filter((id) => id !== labelId);
  const group = labels.find((l) => l.id === labelId)?.group_id;
  const kept = group
    ? selected.filter(
        (id) => labels.find((l) => l.id === id)?.group_id !== group,
      )
    : selected;
  return [...kept, labelId];
}

export function LabelDot({ color }: { color?: string | null }) {
  return (
    <span
      aria-hidden
      className="inline-block size-2 shrink-0 rounded-full"
      style={{ background: color || "var(--color-fg-subtle, #9ca3af)" }}
    />
  );
}

/** 属性胶囊按钮（新建任务弹窗、详情右栏共用的外观） */
export function PropertyChip({
  ref,
  icon,
  children,
  active,
  onClick,
  label,
  muted,
}: {
  ref?: (node: HTMLElement | null) => void;
  icon?: ReactNode;
  children: ReactNode;
  active?: boolean;
  onClick?: () => void;
  /** 无障碍名称（属性名） */
  label?: string;
  muted?: boolean;
}) {
  return (
    <button
      ref={ref}
      type="button"
      aria-label={
        label
          ? `${label}: ${typeof children === "string" ? children : ""}`.trim()
          : undefined
      }
      aria-expanded={active}
      onClick={onClick}
      className={
        "inline-flex h-7 max-w-[220px] items-center gap-1.5 rounded-full border border-line-default bg-surface-card px-2.5 text-body-sm hover:bg-surface-hover " +
        (muted ? "text-fg-muted" : "text-fg-default")
      }
    >
      {icon}
      <span className="truncate">{children}</span>
    </button>
  );
}

/**
 * 单选处理人：选人员替换已有人员、保留 Agent / 小队；选 Agent 替换已有 Agent；
 * none 移除人员。返回新的处理人列表（带名称，用于乐观显示）。
 */
export function replaceAssignee(
  current: TaskAssignee[],
  value: string,
  names: {
    people: Person[];
    agents: Pick<SpaceAgent, "id" | "name">[];
    invitations?: Invitation[];
  },
): TaskAssignee[] {
  const ref = refFromValue(value);
  if (!ref) return current.filter((a) => !isPersonAssignee(a.kind));
  const name =
    ref.kind === "HUMAN"
      ? (names.people.find((p) => p.id === ref.id)?.email ?? "")
      : ref.kind === "INVITATION"
        ? (names.invitations?.find((invite) => invite.id === ref.id)?.email ??
          "")
        : (names.agents.find((a) => a.id === ref.id)?.name ?? "");
  const kept = current.filter((a) =>
    isPersonAssignee(ref.kind)
      ? !isPersonAssignee(a.kind)
      : a.kind !== ref.kind,
  );
  const next = [...kept, { ...ref, name }];
  const order = { HUMAN: 0, INVITATION: 0, AGENT: 1, CREW: 2 } as const;
  return next.sort((a, b) => order[a.kind] - order[b.kind]);
}

export function toRefs(list: TaskAssignee[]): TaskAssigneeRef[] {
  return list.map(({ kind, id }) => ({ kind, id }));
}
