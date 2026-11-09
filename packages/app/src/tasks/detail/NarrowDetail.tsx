import { useRef, useState, type ReactNode } from "react";
import { useSearchParams } from "react-router-dom";

import type {
  ConversationSummary,
  Label,
  Task,
  TaskStatus,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Icon, Menu, MenuItem, Popover, cn, type BadgeTone } from "@openfox/ui";

import { AgentAvatar } from "../../agents/AgentAvatar";
import { displayStatus, statusMeta } from "../../conversations/model";
import { useConversation } from "../../conversations/queries";
import { ReadOnlyTip } from "../../spaces/readonly";
import { useOpenTaskPage } from "../navigation";
import { PriorityIcon } from "../primitives/PriorityIcon";
import { StatusIcon } from "../primitives/StatusIcon";
import type { Person } from "../queries";
import { Properties } from "./Properties";
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
import { useTaskConversations } from "./TaskConversations";

// 窄宽度（< 720px，例如任务全页旁开 AI 面板）的任务详情：
// 不渲染右栏，改为标题上方的「召唤 Agent + 会话标签」和标题下方的属性按钮行。

/** 会话标签最多直接显示几个，其余收进「+N」菜单。 */
const MAX_TABS = 2;

const toneText: Partial<Record<BadgeTone, string>> = {
  success: "text-accent",
  warning: "text-warning",
  danger: "text-danger",
  info: "text-info",
};

/** 会话状态的精简形式：只显示图标（颜色同 StatusBadge），文字给读屏与悬停提示。 */
function StatusDot({ conversation }: { conversation: ConversationSummary }) {
  const t = useT();
  const status = displayStatus(conversation);
  const meta = statusMeta(status);
  if (!meta || status === "ABORTED") return null;
  const label = t(meta.label);
  return (
    <span
      title={label}
      className={cn("flex shrink-0", toneText[meta.tone] ?? "text-fg-muted")}
    >
      <Icon name={meta.icon} size={12} aria-hidden />
      <span className="sr-only">{label}</span>
    </span>
  );
}

/** 标签内容：Agent 头像 + 名称 + · + 会话标题 + 状态。 */
function TabContent({ item }: { item: ConversationSummary }) {
  return (
    <>
      <AgentAvatar
        name={item.agent.name}
        avatar={item.agent.avatar}
        size={16}
      />
      <span className="min-w-0 truncate">
        {item.agent.name}
        {item.title && <span className="text-fg-muted"> · {item.title}</span>}
      </span>
      <StatusDot conversation={item} />
    </>
  );
}

/**
 * 会话标签行：该任务的会话逐个做成切换按钮，当前 AI 面板打开的那个为按下状态；
 * 点击与执行区块的会话行一样打开任务全页 + AI 面板。当前会话总在直接显示的标签里。
 */
export function ConversationTabs({
  orgId,
  task,
}: {
  orgId: string;
  task: Task;
}) {
  const t = useT();
  const openPage = useOpenTaskPage();
  const [params] = useSearchParams();
  const activeId = params.get("conversationId");
  const query = useTaskConversations(orgId, task);
  const { list } = query;
  const activeSummary = useConversation(
    orgId,
    activeId && !list.some((c) => c.id === activeId) ? activeId : null,
  ).data;
  if (list.length === 0) return null;
  let shown = list.slice(0, MAX_TABS);
  const active =
    list.find((item) => item.id === activeId) ??
    (activeSummary?.task_id === task.id && !activeSummary.archived_at
      ? activeSummary
      : undefined);
  if (active && !shown.includes(active))
    shown = [...shown.slice(0, MAX_TABS - 1), active];
  const rest = list.filter((item) => !shown.includes(item));
  const hiddenCount = Math.max(
    rest.length,
    (query.data?.pages[0]?.task_stats?.conversation_count ?? list.length) -
      shown.length,
  );
  const tab =
    "inline-flex h-[26px] max-w-[260px] min-w-0 items-center gap-1.5 rounded-md border border-line-default px-2 text-caption hover:bg-surface-hover";
  return (
    <div
      role="group"
      aria-label={t("tasks.detail.conversationTabs")}
      className="flex min-w-0 flex-1 items-center gap-1.5 border-l border-line-default pl-2"
    >
      {shown.map((item) => (
        <button
          key={item.id}
          type="button"
          aria-pressed={item.id === activeId}
          onClick={() => openPage(task.display_id, item.id)}
          title={[item.agent.name, item.title].filter(Boolean).join(" · ")}
          className={cn(
            tab,
            shown.length > 1 && "flex-1",
            item.id === activeId && "border-accent/40 bg-surface-active",
          )}
        >
          <TabContent item={item} />
        </button>
      ))}
      {rest.length > 0 && (
        <Menu
          label={t("tasks.detail.moreConversations", { count: hiddenCount })}
          placement="bottom-end"
          className="max-h-[320px] w-80 max-w-[90vw] overflow-y-auto"
          trigger={({ ref, toggle, open }) => (
            <button
              ref={ref}
              type="button"
              aria-label={t("tasks.detail.moreConversations", {
                count: hiddenCount,
              })}
              aria-haspopup="menu"
              aria-expanded={open}
              onClick={toggle}
              className="inline-flex h-7 shrink-0 items-center rounded-md border border-line-default px-2 text-body-sm text-fg-muted hover:bg-surface-hover"
            >
              {t("tasks.detail.moreConversations", { count: hiddenCount })}
            </button>
          )}
        >
          {rest.map((item) => (
            <MenuItem
              key={item.id}
              icon={
                <AgentAvatar
                  name={item.agent.name}
                  avatar={item.agent.avatar}
                  size={20}
                />
              }
              onSelect={() => openPage(task.display_id, item.id)}
            >
              {item.agent.name}
              {item.title && (
                <span className="text-fg-muted"> · {item.title}</span>
              )}
            </MenuItem>
          ))}
          {query.hasNextPage && (
            <MenuItem
              disabled={query.isFetchingNextPage}
              onSelect={() => void query.fetchNextPage()}
            >
              {t("tasks.detail.loadMoreConversations")}
            </MenuItem>
          )}
        </Menu>
      )}
    </div>
  );
}

const barButton =
  "inline-flex h-7 max-w-[200px] min-w-0 items-center gap-2 rounded-md border border-line-default px-2 text-body-sm hover:bg-surface-hover disabled:cursor-not-allowed disabled:opacity-50 disabled:hover:bg-transparent";

/** 属性按钮：图标 + 当前值；aria-label 为「属性名: 当前值」。只读时禁用并由 ReadOnlyTip 提示原因。 */
function BarButton({
  label,
  value,
  icon,
  muted,
  disabled,
  trigger: { ref, toggle, open },
}: {
  label: string;
  value: string;
  icon: ReactNode;
  muted?: boolean;
  disabled: boolean;
  trigger: {
    ref: (node: HTMLElement | null) => void;
    toggle: () => void;
    open: boolean;
  };
}) {
  return (
    <ReadOnlyTip>
      <button
        ref={ref}
        type="button"
        aria-label={`${label}: ${value}`}
        aria-expanded={open}
        disabled={disabled}
        onClick={toggle}
        className={cn(barButton, muted && "text-fg-muted")}
      >
        <span className="flex shrink-0">{icon}</span>
        <span className="truncate">{value}</span>
      </button>
    </ReadOnlyTip>
  );
}

export interface PropertyBarProps {
  orgId: string;
  task: Task;
  workflow: TaskStatus[];
  labels: Label[];
  people: Person[];
  agents: AssigneeAgent[];
  onOpenTask: (displayId: string) => void;
  readOnly: boolean;
}

/** 属性按钮行：处理人、执行 Agent、状态、优先级、专项（启用时）、更多属性；点开即对应选择器。 */
export function PropertyBar(props: PropertyBarProps) {
  const { orgId, task, people, agents, workflow, readOnly } = props;
  const t = useT();
  const projectsEnabled = useProjectsEnabled(orgId, task.space_id);
  const none = t("tasks.detail.none");
  return (
    <div
      role="group"
      aria-label={t("tasks.detail.properties")}
      className="flex flex-wrap gap-2"
    >
      <AssigneePicker
        orgId={orgId}
        task={task}
        people={people}
        agents={agents}
        trigger={({ ref, toggle, open }) => (
          <ReadOnlyTip>
            <button
              ref={ref}
              type="button"
              aria-label={`${t("tasks.fields.assignee")}: ${task.assignees.map((a) => a.name).join(" → ") || t("tasks.noAssignee")}`}
              aria-expanded={open}
              disabled={readOnly}
              onClick={toggle}
              className={cn(barButton, "max-w-full")}
            >
              <AssigneeContent task={task} people={people} agents={agents} />
            </button>
          </ReadOnlyTip>
        )}
      />
      <StatusPicker
        orgId={orgId}
        task={task}
        workflow={workflow}
        trigger={(trigger) => (
          <BarButton
            label={t("tasks.fields.status")}
            value={task.status_name}
            icon={
              <StatusIcon
                category={task.status_category}
                color={task.status_color}
              />
            }
            disabled={readOnly}
            trigger={trigger}
          />
        )}
      />
      <PriorityPicker
        orgId={orgId}
        task={task}
        trigger={(trigger) => (
          <BarButton
            label={t("tasks.fields.priority")}
            value={
              task.priority === "NONE"
                ? t("tasks.priorityNone")
                : t(`tasks.priority.${task.priority}`)
            }
            icon={<PriorityIcon priority={task.priority} />}
            disabled={readOnly}
            trigger={trigger}
          />
        )}
      />
      {projectsEnabled && (
        <TaskProjectPicker
          orgId={orgId}
          task={task}
          trigger={(name, trigger) => (
            <BarButton
              label={t("projects.title")}
              value={name ?? none}
              icon={<Icon name="folder" />}
              muted={!task.project_id}
              disabled={readOnly}
              trigger={trigger}
            />
          )}
        />
      )}
      <TaskLabelPicker
        orgId={orgId}
        task={task}
        labels={props.labels}
        trigger={(trigger) => (
          <BarButton
            label={t("tasks.fields.label")}
            value={
              task.label_items.map((label) => label.name).join("、") ||
              t("tasks.noLabels")
            }
            icon={<Icon name="tag" />}
            muted={task.label_items.length === 0}
            disabled={readOnly}
            trigger={trigger}
          />
        )}
      />
      <MoreProperties {...props} />
    </div>
  );
}

/**
 * 浮层里又打开了别的浮层（选择菜单、日期、父任务对话框）时，外层 Popover 收到的
 * 「点击外部 / Esc」其实属于里层，不能关闭，否则里层会跟着卸载。
 * 判断依据：body 下除本浮层与详情外框（抽屉）之外，还有打开的菜单 / 列表 / 对话框。
 */
function nestedFloatingOpen(
  panel: HTMLElement | null,
  anchor: HTMLElement | null,
): boolean {
  const nodes = document.querySelectorAll<HTMLElement>(
    'body > [role="dialog"], body > [role="menu"], body > div > [role="dialog"][aria-modal="true"]',
  );
  return Array.from(nodes).some(
    (node) =>
      node !== panel &&
      !node.contains(panel) &&
      !(anchor && node.contains(anchor)),
  );
}

/** 「更多属性」只展示按钮行未列出的属性；执行会话由标题上方的标签访问。 */
export function MoreProperties(
  props: PropertyBarProps & { sidebar?: boolean },
) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const anchor = useRef<HTMLButtonElement | null>(null);
  const panel = useRef<HTMLDivElement | null>(null);
  // 浮层高度按按钮上下两侧较大的可用空间取（最多 560px），避免放不下时被夹回视口而盖住按钮
  const [maxHeight, setMaxHeight] = useState(560);
  const label = t("tasks.detail.moreProperties");
  const toggle = () => {
    const rect = anchor.current?.getBoundingClientRect();
    if (rect && !open)
      setMaxHeight(
        Math.max(
          200,
          Math.min(
            560,
            Math.max(window.innerHeight - rect.bottom, rect.top) - 24,
          ),
        ),
      );
    setOpen((value) => !value);
  };
  return (
    <>
      <button
        ref={anchor}
        type="button"
        aria-label={label}
        aria-haspopup="dialog"
        aria-expanded={open}
        onClick={toggle}
        className={
          props.sidebar
            ? "flex h-8 w-full items-center justify-between rounded-md px-2 text-body-sm text-fg-muted hover:bg-surface-hover"
            : barButton
        }
      >
        {!props.sidebar && <Icon name="ellipsis" />}
        <span className="truncate">{label}</span>
        {props.sidebar && <Icon name="chevron-right" />}
      </button>
      <Popover
        open={open}
        onOpenChange={(next) => {
          if (!next && nestedFloatingOpen(panel.current, anchor.current))
            return;
          setOpen(next);
        }}
        anchorRef={anchor}
        placement="bottom-start"
        aria-label={label}
        className="w-[320px] p-0"
      >
        <div
          ref={panel}
          style={{ maxHeight }}
          className="flex flex-col overflow-y-auto p-2"
        >
          <Properties
            orgId={props.orgId}
            task={props.task}
            workflow={props.workflow}
            labels={props.labels}
            people={props.people}
            agents={props.agents}
            onOpenTask={props.onOpenTask}
            additionalOnly
            hideDueDate={props.sidebar}
          />
        </div>
      </Popover>
    </>
  );
}
