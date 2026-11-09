import { useQuery } from "@tanstack/react-query";
import {
  createContext,
  useContext,
  useEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { useLocation, useNavigate, useSearchParams } from "react-router-dom";

import {
  isApiError,
  type Task,
  type TaskDetail,
  type TaskLink,
  type TimelineItem,
} from "@openfox/api";
import { useT, type Translate } from "@openfox/i18n";
import {
  Button,
  ConfirmDialog,
  Drawer,
  Icon,
  IconButton,
  type IconName,
  Menu,
  MenuItem,
  MenuLabel,
  MenuSeparator,
  Spinner,
  Tabs,
  useToast,
  cn,
} from "@openfox/ui";

import { keys, useApi } from "../../context";
import { useMe } from "../../agents/queries";
import { recordTaskVisit } from "../recentVisits";
import { useRejectNavLocation, withoutParam } from "../../shell/navMemory";
import { useCreateTaskDialog } from "../create/CreateTaskProvider";
import { branchUsername, gitBranchName } from "../model";
import { useIntegrationSettings } from "../../integrations/queries";
import { useOpenTaskPage } from "../navigation";
import {
  displayIdOf,
  taskCommitMessage,
  taskTitleLink,
  taskUrl,
  useCopy,
} from "../parts";
import { priorityGroups } from "../pickers";
import { PriorityIcon, type Priority } from "../primitives/PriorityIcon";
import { StatusIcon } from "../primitives/StatusIcon";
import { TaskTypeIcon } from "../primitives/TaskTypeIcon";
import {
  taskErrorMessage,
  useDeleteTask,
  usePeople,
  usePickerLabels,
  useRelationTypes,
  useSpaceAgents,
  useSpaceTypes,
  useStatuses,
  useTaskByDisplayId,
  useToggleFavorite,
  useUpdateTask,
} from "../queries";
import { statusFields } from "../TaskMenu";
import { AttachmentsSection } from "./AttachmentsSection";
import { ExecutionSection } from "./ExecutionSection";
import { ConversationTabs, PropertyBar } from "./NarrowDetail";
import {
  InvokeAgentButton,
  TranscriptList,
  useTaskConversations,
} from "./TaskConversations";
import { useIsNarrow } from "./useIsNarrow";
import { GitLinksSection } from "./GitLinksSection";
import { WideProperties } from "./WideProperties";
import { Followers } from "./Followers";
import { Reactions } from "./Reactions";
import { LinksSection, RelationsSection, SubtasksSection } from "./Sections";
import { useMoveTaskEntry } from "./moveToSpace";
import { CommentBox } from "./CommentBox";
import { Timeline, useTimeline, type NameBook } from "./Timeline";
import {
  ReadOnlyContext,
  ReadOnlyTip,
  useReadOnly,
  useTaskReadOnlyReason,
  useReadOnlyReason,
} from "../../spaces/readonly";
import { RetiredBanner } from "../../spaces/settings/parts";
import {
  canManageSpace,
  useOrgRole,
  useSpaceDetail,
} from "../../spaces/queries";
import { spaceSettingsPath } from "../../spaces/SpacesNav";
import {
  TaskTriageActions,
  type WorkbenchActions,
} from "../../triage/TaskTriageActions";
import { usePriorityLevels } from "../../organization/queries";

/**
 * 旧编号重定向：按旧标识符打开的任务，把地址里的 ?task= 原地替换为当前编号（不新增历史记录）。
 */
export function useCanonicalDisplayId(
  displayId: string,
  canonical: string | undefined,
  /** 全页用：地址里的编号不在 ?task= 里，由调用方 replace 到规范编号的地址 */
  redirect?: (canonical: string) => void,
  /** 任务当前所在空间：在别的空间页面按旧编号打开时，跳到这个空间的任务页（2026-11-04 用户决定） */
  spaceId?: string,
) {
  const [, setParams] = useSearchParams();
  const location = useLocation();
  const navigate = useNavigate();
  useEffect(() => {
    if (!canonical || canonical.toUpperCase() === displayId.toUpperCase())
      return;
    if (redirect) {
      redirect(canonical.toUpperCase());
      return;
    }
    const inSpace = /^\/o\/([^/]+)\/spaces\/([^/]+)\//.exec(location.pathname);
    if (inSpace && spaceId && inSpace[2] !== spaceId) {
      // 只带 task=：源空间页面的筛选等参数对目标空间没有意义
      const task = encodeURIComponent(canonical.toUpperCase());
      navigate(`/o/${inSpace[1]}/spaces/${spaceId}/tasks?task=${task}`, {
        replace: true,
      });
      return;
    }
    setParams(
      (prev) => {
        const next = new URLSearchParams(prev);
        next.set("task", canonical.toUpperCase());
        return next;
      },
      { replace: true },
    );
    // 只在编号变化时触发；location 读的是触发时的地址
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [canonical, displayId, setParams, redirect, spaceId]);
}

/** 详情外框（抽屉或全页）需要的三块内容。 */
export interface TaskDetailFrameProps {
  title: ReactNode;
  actions: ReactNode;
  children: ReactNode;
}

/**
 * 任务详情视图（抽屉与全页共用）：加载任务、规范编号、只读态、顶栏操作、正文与各种状态。
 * 外框由 frame 决定；frame 以函数调用（不是组件），外框元素类型稳定，切换任务不会重挂载。
 */
export function TaskDetailView({
  orgId,
  displayId,
  onOpenTask,
  onDeleted,
  onExpand,
  expandLabel,
  expandIcon,
  rejectReplacement,
  redirectCanonical,
  triageWorkbench,
  notFoundActions,
  frame,
}: {
  orgId: string;
  displayId: string;
  onOpenTask: (displayId: string) => void;
  onDeleted: () => void;
  /** 抽屉：展开到全页；全页：还原抽屉 */
  onExpand: (displayId: string) => void;
  expandLabel: string;
  expandIcon: IconName;
  /** 任务不存在时主导航记忆改记的地址 */
  rejectReplacement: string;
  triageWorkbench?: WorkbenchActions;
  redirectCanonical?: (canonical: string) => void;
  notFoundActions?: ReactNode;
  frame: (props: TaskDetailFrameProps) => ReactNode;
}) {
  const t = useT();
  const copyId = useCopy();
  const detail = useTaskByDisplayId(orgId, displayId);
  const [linking, setLinking] = useState(false);
  const [linkEditing, setLinkEditing] = useState<TaskLink | "new" | null>(null);
  const task = detail.data;
  const userId = useMe().data?.id;
  useEffect(() => {
    if (task?.id && userId) recordTaskVisit(orgId, userId, task.id);
  }, [orgId, userId, task?.id]);
  useCanonicalDisplayId(
    displayId,
    task?.canonical_display_id,
    redirectCanonical,
    task?.space_id,
  );
  // 任务不存在（404）时，主导航记忆改记失效参数去掉后的地址，避免下次又回到失效任务
  const taskMissing = isApiError(detail.error) && detail.error.status === 404;
  useRejectNavLocation(taskMissing, taskMissing ? rejectReplacement : null);
  // 已停用空间的任务：详情里的写操作全部禁用。
  const readOnlyReason = useTaskReadOnlyReason(orgId, task?.space_id);
  return (
    <ReadOnlyContext.Provider value={readOnlyReason ?? false}>
      <DetailUiContext.Provider
        value={{ linking, setLinking, linkEditing, setLinkEditing }}
      >
        {frame({
          title: task ? (
            <span className="flex min-w-0 items-center gap-1.5">
              <button
                type="button"
                className="shrink-0 rounded px-1 text-fg-muted hover:bg-surface-hover"
                title={t("tasks.detail.copyId")}
                onClick={() =>
                  void copyId(displayIdOf(task), t("tasks.copied.id"))
                }
              >
                {displayIdOf(task)}
              </button>
              <span aria-hidden className="text-fg-subtle">
                ·
              </span>
              <span className="truncate">{task.title}</span>
            </span>
          ) : (
            displayId
          ),
          actions: task ? (
            <>
              {" "}
              {triageWorkbench && (
                <TaskTriageActions
                  orgId={orgId}
                  task={task}
                  workbench={triageWorkbench}
                />
              )}
              <HeaderActions
                orgId={orgId}
                task={task}
                expandLabel={expandLabel}
                expandIcon={expandIcon}
                onExpand={() => onExpand(displayIdOf(task))}
                onDeleted={onDeleted}
              />
            </>
          ) : undefined,
          children: (
            <>
              {detail.isLoading && (
                <div className="flex h-full items-center justify-center">
                  <Spinner />
                </div>
              )}
              {detail.error && (
                <div className="flex h-full flex-col items-center justify-center gap-3 p-10 text-center">
                  <p className="text-body text-fg-muted">
                    {taskMissing
                      ? t("tasks.detail.notFound")
                      : t("tasks.empty.errorDesc")}
                  </p>
                  {taskMissing ? (
                    notFoundActions
                  ) : (
                    <Button
                      variant="outline"
                      onClick={() => void detail.refetch()}
                    >
                      {t("common.retry")}
                    </Button>
                  )}
                </div>
              )}
              {task && (
                <DetailBody
                  triageInHeader={!!triageWorkbench}
                  orgId={orgId}
                  task={task}
                  onOpenTask={onOpenTask}
                />
              )}
            </>
          ),
        })}
      </DetailUiContext.Provider>
    </ReadOnlyContext.Provider>
  );
}

/**
 * 任务详情抽屉：地址栏 ?task=<DISPLAY_ID> 打开，刷新后保持。
 * 非模态：没有遮罩，主导航、侧栏和列表仍可操作；展开按钮跳到任务全页。
 */
export function TaskDetailDrawer({
  orgId,
  displayId,
  onClose,
  onOpenTask,
}: {
  orgId: string;
  displayId: string;
  onClose: () => void;
  onOpenTask: (displayId: string) => void;
}) {
  const t = useT();
  const openPage = useOpenTaskPage();
  const { pathname, search } = useLocation();
  return (
    <TaskDetailView
      orgId={orgId}
      displayId={displayId}
      onOpenTask={onOpenTask}
      onDeleted={onClose}
      onExpand={(id) => openPage(id)}
      expandLabel={t("tasks.detail.expand")}
      expandIcon="maximize-2"
      rejectReplacement={withoutParam(pathname, search, "task")}
      notFoundActions={
        <Button variant="outline" onClick={onClose}>
          {t("common.close")}
        </Button>
      }
      frame={({ title, actions, children }) => (
        <Drawer
          open
          modal={false}
          onClose={onClose}
          title={title}
          actions={actions}
        >
          {children}
        </Drawer>
      )}
    />
  );
}

function HeaderActions({
  orgId,
  task,
  expandLabel,
  expandIcon,
  onExpand,
  onDeleted,
}: {
  orgId: string;
  task: TaskDetail;
  expandLabel: string;
  expandIcon: IconName;
  onExpand: () => void;
  onDeleted: () => void;
}) {
  const t = useT();
  const priorityLevels = usePriorityLevels();
  const copy = useCopy();
  const update = useUpdateTask(orgId);
  const remove = useDeleteTask(orgId);
  const favorite = useToggleFavorite(orgId);
  const statuses = useStatuses(orgId);
  const types = useSpaceTypes(orgId, task.space_id);
  const toast = useToast();
  // 分支名格式取自 GitHub 集成设置，拿不到时用默认格式。
  const api = useApi();
  const me = useQuery({ queryKey: keys.me, queryFn: api.me.get });
  const gitSettings = useIntegrationSettings(orgId, "github");
  const { openCreate } = useCreateTaskDialog();
  const [level, setLevel] = useState<"root" | "status" | "priority" | "move">(
    "root",
  );
  // 移动到空间：入口、空间列表、对话框与快捷键都在 moveToSpace.tsx。
  const moveEntry = useMoveTaskEntry(orgId, task, () => setLevel("move"));
  const [confirm, setConfirm] = useState(false);
  const bus = useDetailBus();
  const workflow = workflowOf(task, types.data, statuses.data);
  const readOnly = useReadOnly();
  const retiredReason = useReadOnlyReason();

  const copyWithComments = async () => {
    try {
      const items: TimelineItem[] = [];
      let cursor: string | null = null;
      do {
        const page = await api.tasks.timeline(
          orgId,
          task.space_id,
          task.id,
          cursor,
        );
        items.push(...page.items);
        cursor = page.next_cursor;
      } while (cursor);
      await copy(taskPrompt(t, task, items), t("tasks.copied.prompt"));
    } catch (error) {
      toast(taskErrorMessage(t, error), { tone: "error" });
    }
  };
  const sub = (to: "status" | "priority") => (
    <button
      type="button"
      role="menuitem"
      aria-haspopup="menu"
      aria-disabled={readOnly || undefined}
      title={readOnly ? retiredReason : undefined}
      onClick={(event) => {
        event.stopPropagation();
        if (!readOnly) setLevel(to);
      }}
      className={cn(
        "flex min-h-[34px] w-full items-center gap-2.5 rounded-lg py-1.5 pr-2 pl-2.5 text-left text-body outline-none hover:bg-surface-active focus-visible:bg-surface-active",
        readOnly && "cursor-not-allowed opacity-50",
      )}
    >
      <span className="flex text-fg-muted">
        {to === "status" ? (
          <StatusIcon
            category={task.status_category}
            color={task.status_color}
          />
        ) : (
          <PriorityIcon priority={task.priority} />
        )}
      </span>
      <span className="flex-1">
        {to === "status"
          ? t("tasks.fields.status")
          : t("tasks.fields.priority")}
      </span>
      <Icon name="chevron-right" className="text-fg-muted" />
    </button>
  );

  const favorited = task.favorited === true;
  return (
    <>
      <IconButton
        label={
          favorited ? t("tasks.detail.unfavorite") : t("tasks.detail.favorite")
        }
        icon={
          <Icon
            name="star"
            className={favorited ? "fill-current text-warning" : undefined}
          />
        }
        size="28"
        aria-pressed={favorited}
        // 收藏是个人偏好，只读（已归档）空间里同样可以操作
        onClick={() => favorite.mutate({ task, favorite: !favorited })}
      />
      <IconButton
        label={t("tasks.detail.copyLink")}
        icon={<Icon name="link" />}
        size="28"
        onClick={() => void copy(taskUrl(orgId, task), t("tasks.copied.link"))}
      />
      <IconButton
        label={t("tasks.detail.copyId")}
        icon={<Icon name="copy" />}
        size="28"
        onClick={() => void copy(displayIdOf(task), t("tasks.copied.id"))}
      />
      <Menu
        label={t("tasks.detail.more")}
        placement="bottom-end"
        className="min-w-[240px]"
        trigger={({ ref, toggle, open }) => (
          <IconButton
            ref={ref}
            label={t("tasks.detail.more")}
            icon={<Icon name="ellipsis" />}
            size="28"
            active={open}
            onClick={() => {
              setLevel("root");
              toggle();
            }}
          />
        )}
      >
        {level === "root" && (
          <>
            <MenuItem
              icon={<Icon name="link-2" />}
              disabled={readOnly}
              disabledReason={retiredReason}
              onSelect={() => bus.setLinkEditing("new")}
            >
              {t("tasks.detail.addLink")}
            </MenuItem>
            <MenuItem
              icon={<Icon name="copy-plus" />}
              disabled={readOnly}
              disabledReason={retiredReason}
              onSelect={() =>
                openCreate({
                  spaceId: task.space_id,
                  relateTo: { spaceId: task.space_id, taskId: task.id },
                })
              }
            >
              {t("tasks.detail.createRelated")}
            </MenuItem>
            <MenuItem
              icon={<Icon name="waypoints" />}
              disabled={readOnly}
              disabledReason={retiredReason}
              onSelect={() => bus.setLinking(true)}
            >
              {t("tasks.detail.linkExisting")}
            </MenuItem>
            <MenuSeparator />
            <MenuItem
              icon={<Icon name="link" />}
              onSelect={() =>
                void copy(taskUrl(orgId, task), t("tasks.copied.link"))
              }
            >
              {t("tasks.detail.copyLink")}
            </MenuItem>
            <MenuItem
              icon={<Icon name="copy" />}
              onSelect={() =>
                void copy(
                  taskTitleLink(orgId, task),
                  t("tasks.copied.titleLink"),
                )
              }
            >
              {t("tasks.detail.copyTitleLink")}
            </MenuItem>
            <MenuItem
              icon={<Icon name="git-branch" />}
              onSelect={() =>
                void copy(
                  gitBranchName(task.display_id, task.title, {
                    format:
                      gitSettings.data?.settings.branch_format ??
                      "username/identifier-title",
                    username: branchUsername(me.data?.email),
                  }),
                  t("tasks.copied.branch"),
                )
              }
            >
              {t("tasks.detail.copyBranch")}
            </MenuItem>
            <MenuItem
              icon={<Icon name="circle-dot" />}
              onSelect={() =>
                void copy(taskCommitMessage(task), t("tasks.copied.commit"))
              }
            >
              {t("tasks.detail.copyCommit")}
            </MenuItem>
            <MenuLabel>{t("tasks.detail.copyPrompt")}</MenuLabel>
            <MenuItem
              icon={<Icon name="file-text" />}
              onSelect={() =>
                void copy(taskPrompt(t, task, []), t("tasks.copied.prompt"))
              }
            >
              {t("tasks.detail.copyPromptPlain")}
            </MenuItem>
            <MenuItem
              icon={<Icon name="messages" />}
              onSelect={() => void copyWithComments()}
            >
              {t("tasks.detail.copyPromptComments")}
            </MenuItem>
            <MenuSeparator />
            {moveEntry.trigger}
            {sub("status")}
            {sub("priority")}
            <MenuSeparator />
            <MenuItem
              icon={<Icon name="trash-2" />}
              danger
              disabled={readOnly}
              disabledReason={retiredReason}
              onSelect={() => setConfirm(true)}
            >
              {t("tasks.detail.delete")}
            </MenuItem>
          </>
        )}
        {level === "move" && moveEntry.options}
        {level === "status" &&
          workflow.map((status) => (
            <MenuItem
              key={status.id}
              icon={
                <StatusIcon category={status.category} color={status.color} />
              }
              selected={status.id === task.status_id}
              onSelect={() => {
                if (status.id !== task.status_id)
                  update.mutate({
                    task,
                    patch: { status_id: status.id },
                    optimistic: statusFields(status),
                  });
              }}
            >
              {status.name}
            </MenuItem>
          ))}
        {level === "priority" &&
          priorityGroups(t, priorityLevels)[0]!.options.map((option) => (
            <MenuItem
              key={option.value}
              icon={option.icon}
              selected={option.value === task.priority}
              onSelect={() => {
                if (option.value !== task.priority)
                  update.mutate({
                    task,
                    patch: { priority: option.value as Priority },
                  });
              }}
            >
              {option.label}
            </MenuItem>
          ))}
      </Menu>
      <IconButton
        label={expandLabel}
        icon={<Icon name={expandIcon} />}
        size="28"
        onClick={onExpand}
      />
      {moveEntry.dialog}
      <ConfirmDialog
        open={confirm}
        title={t("tasks.detail.deleteTitle")}
        description={t("tasks.detail.deleteDesc")}
        confirmLabel={t("tasks.detail.delete")}
        danger
        loading={remove.isPending}
        onCancel={() => setConfirm(false)}
        onConfirm={() =>
          remove.mutate(task, {
            onSuccess: () => {
              setConfirm(false);
              onDeleted();
            },
            onError: () => setConfirm(false),
          })
        }
      />
    </>
  );
}

// 顶栏菜单需要打开正文里的「添加链接」「关联已有任务」弹窗，状态放在抽屉级上下文里
interface DetailUi {
  linking: boolean;
  setLinking: (v: boolean) => void;
  linkEditing: TaskLink | "new" | null;
  setLinkEditing: (v: TaskLink | "new" | null) => void;
}
const DetailUiContext = createContext<DetailUi | null>(null);
function useDetailBus(): DetailUi {
  const value = useContext(DetailUiContext);
  if (!value) throw new Error("useDetailBus 必须在 TaskDetailDrawer 内使用");
  return value;
}

function workflowOf(
  task: Task,
  types: ReturnType<typeof useSpaceTypes>["data"],
  statuses: ReturnType<typeof useStatuses>["data"],
) {
  const type = types?.find((x) => x.id === task.type_id);
  const ids = new Set(type?.statuses.map((s) => s.status_id) ?? []);
  return (statuses ?? []).filter(
    (s) => (ids.size === 0 || ids.has(s.id)) && !s.archived_at,
  );
}

/** 复制为 Prompt：Markdown 格式的任务摘要，可选附带评论。 */
export function taskPrompt(
  t: Translate,
  task: Task,
  items: TimelineItem[],
): string {
  const lines = [`# ${displayIdOf(task)} ${task.title}`, ""];
  lines.push(`- ${t("tasks.fields.type")}: ${task.type_name}`);
  lines.push(`- ${t("tasks.fields.status")}: ${task.status_name}`);
  lines.push(
    `- ${t("tasks.fields.priority")}: ${t(`tasks.priority.${task.priority}`)}`,
  );
  if (task.assignees.length)
    lines.push(
      `- ${t("tasks.fields.assignee")}: ${task.assignees.map((a) => a.name).join(", ")}`,
    );
  if (task.label_items.length)
    lines.push(
      `- ${t("tasks.fields.label")}: ${task.label_items.map((l) => l.name).join(", ")}`,
    );
  if (task.start_date)
    lines.push(`- ${t("tasks.fields.startDate")}: ${task.start_date}`);
  if (task.due_date)
    lines.push(`- ${t("tasks.fields.dueDate")}: ${task.due_date}`);
  if (task.description.trim()) lines.push("", task.description.trim());
  const comments = items.flatMap((i) =>
    i.type === "comment" && i.comment ? [i.comment] : [],
  );
  if (comments.length) {
    lines.push("", "## Comments");
    for (const c of comments)
      lines.push("", `**${c.author_name}** (${c.created_at})`, "", c.body);
  }
  return lines.join("\n") + "\n";
}

function DetailBody({
  triageInHeader,
  orgId,
  task,
  onOpenTask,
}: {
  orgId: string;
  task: TaskDetail;
  triageInHeader: boolean;
  onOpenTask: (displayId: string) => void;
}) {
  const t = useT();
  const api = useApi();
  const update = useUpdateTask(orgId);
  const statuses = useStatuses(orgId);
  const types = useSpaceTypes(orgId, task.space_id);
  const spaces = useQuery({
    queryKey: keys.spaces(orgId),
    queryFn: () => api.spaces.list(orgId),
  });
  const space = spaces.data?.find((s) => s.id === task.space_id);
  const people = usePeople(orgId, space);
  const agents = (useSpaceAgents(orgId, task.space_id).data ?? []).filter(
    (a) => !a.archived_at,
  );
  const labels = usePickerLabels(orgId, task.space_id).data ?? [];
  const relationTypes = useRelationTypes(orgId).data ?? [];
  const bus = useDetailBus();
  const [tab, setTab] = useState("activity");
  const workflow = workflowOf(task, types.data, statuses.data);
  const book: NameBook = {
    statuses: statuses.data ?? [],
    labels,
    people,
    agents,
  };
  const typeList = types.data ?? [];
  const readOnly = useReadOnly();
  const readOnlyReason = useReadOnlyReason();
  // 9a · 类型菜单底部的「管理任务类型」：与空间设置页同一个权限判断，没有权限时不显示
  const navigate = useNavigate();
  const spaceDetail = useSpaceDetail(orgId, task.space_id);
  const orgRole = useOrgRole(orgId);
  const canManageTypes = canManageSpace(spaceDetail.data, orgRole);
  const conversations = useTaskConversations(orgId, task);
  const taskStats = conversations.data?.pages[0]?.task_stats;
  const activityCount = useTimeline(orgId, task).data?.pages[0]?.total;
  const transcriptCount =
    taskStats?.conversation_count ??
    (conversations.data && !conversations.hasNextPage
      ? conversations.list.length
      : undefined);
  // 量到容器窄于 720px 时走窄布局（不渲染右栏）；量不到时按宽，宽布局保持原样。
  const container = useRef<HTMLDivElement | null>(null);
  const narrow = useIsNarrow(container, 720);

  return (
    // 容器查询保留：ResizeObserver 首次回调前（或环境不支持时）窄容器仍把属性栏移到正文下方，避免正文挤出左栏
    <div ref={container} className="@container h-full">
      <div className="grid h-full grid-cols-1 grid-rows-[minmax(0,1fr)_auto] @[720px]:grid-cols-[minmax(0,1fr)_300px]">
        <div className="flex min-w-0 flex-col gap-5 overflow-y-auto px-6 py-5 *:shrink-0">
          {readOnly && (
            <RetiredBanner
              message={
                readOnlyReason === t("spaces.errors.retired")
                  ? t("spaces.task.retiredBanner")
                  : readOnlyReason!
              }
            />
          )}
          {task.triage && !triageInHeader && (
            <TaskTriageActions orgId={orgId} task={task} />
          )}
          {narrow && (!readOnly || conversations.list.length > 0) && (
            <div className="sticky top-0 z-10 -my-1 flex min-w-0 items-center gap-2 bg-surface-card py-1">
              <InvokeAgentButton
                orgId={orgId}
                task={task}
                className="shrink-0"
              />
              <ConversationTabs orgId={orgId} task={task} />
            </div>
          )}
          <div className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-2">
            <ReadOnlyTip>
              <Menu
                label={t("tasks.detail.changeType")}
                trigger={({ ref, toggle, open }) => (
                  <button
                    ref={ref}
                    type="button"
                    aria-expanded={open}
                    aria-label={`${t("tasks.detail.changeType")}: ${task.type_name}`}
                    disabled={readOnly}
                    onClick={toggle}
                    className="inline-flex h-6 max-w-full shrink-0 items-center gap-2 rounded-md bg-surface-muted px-2 text-body-sm"
                  >
                    <TaskTypeIcon icon={task.type_icon} size={14} />
                    <span className="truncate">{task.type_name}</span>
                  </button>
                )}
              >
                {typeList.map((type) => (
                  <MenuItem
                    key={type.id}
                    icon={<TaskTypeIcon icon={type.icon} />}
                    selected={type.id === task.type_id}
                    onSelect={() => {
                      if (type.id !== task.type_id)
                        update.mutate({
                          task,
                          patch: { type_id: type.id },
                          optimistic: {
                            type_id: type.id,
                            type_name: type.name,
                            type_icon: type.icon ?? null,
                          },
                        });
                    }}
                  >
                    {type.name}
                  </MenuItem>
                ))}
                {canManageTypes && (
                  <>
                    <MenuSeparator />
                    <MenuItem
                      icon={<Icon name="settings" />}
                      onSelect={() =>
                        navigate(
                          spaceSettingsPath(orgId, task.space_id, "task-types"),
                        )
                      }
                    >
                      {t("tasks.detail.manageTypes")}
                    </MenuItem>
                  </>
                )}
              </Menu>
            </ReadOnlyTip>
            <div className="min-w-0 flex-1 basis-48">
              <ReadOnlyTip block>
                <InlineText
                  key={`title:${task.id}:${task.version}`}
                  readOnly={readOnly}
                  label={t("tasks.detail.titleLabel")}
                  value={task.title}
                  className="text-[24px] leading-8 font-semibold"
                  required
                  maxLength={500}
                  onSave={(title) =>
                    update.mutateAsync({ task, patch: { title } })
                  }
                />
              </ReadOnlyTip>
            </div>
          </div>
          {narrow && (
            <PropertyBar
              orgId={orgId}
              task={task}
              workflow={workflow}
              labels={labels}
              people={people}
              agents={agents}
              onOpenTask={onOpenTask}
              readOnly={readOnly}
            />
          )}
          <ReadOnlyTip block>
            <InlineText
              key={`desc:${task.id}:${task.version}`}
              readOnly={readOnly}
              label={t("tasks.detail.descLabel")}
              value={task.description}
              placeholder={t("tasks.detail.descPlaceholder")}
              multiline
              maxLength={10000}
              className="text-body"
              onSave={(description) =>
                update.mutateAsync({ task, patch: { description } })
              }
            />
          </ReadOnlyTip>
          <AttachmentsSection
            orgId={orgId}
            task={task}
            renderActions={(uploadButton) => (
              <DescriptionActions
                orgId={orgId}
                task={task}
                uploadButton={uploadButton}
                people={people}
                agents={agents}
              />
            )}
          />
          <SubtasksSection orgId={orgId} task={task} onOpenTask={onOpenTask} />
          <RelationsSection
            orgId={orgId}
            task={task}
            relationTypes={relationTypes}
            onOpenTask={onOpenTask}
            linking={bus.linking}
            onLinkingChange={bus.setLinking}
          />
          <LinksSection
            orgId={orgId}
            task={task}
            editing={bus.linkEditing}
            onEditingChange={bus.setLinkEditing}
          />
          <GitLinksSection
            orgId={orgId}
            spaceId={task.space_id}
            taskId={task.id}
            people={people}
          />
          <div className="mt-5 flex flex-col gap-4">
            <Tabs
              label={t("tasks.detail.activity")}
              value={tab}
              onChange={setTab}
              tabs={[
                {
                  id: "activity",
                  label: t("tasks.detail.activity"),
                  count: activityCount,
                },
                {
                  id: "transcript",
                  label: t("tasks.detail.transcript"),
                  count: transcriptCount,
                },
              ]}
            />
            {tab === "transcript" ? (
              <TranscriptList orgId={orgId} task={task} people={people} />
            ) : (
              <Timeline orgId={orgId} task={task} book={book} />
            )}
          </div>
        </div>
        {tab === "activity" && (
          <div className="row-start-2 px-6 pt-2 pb-4">
            <CommentBox orgId={orgId} task={task} people={people} />
          </div>
        )}
        {!narrow && (
          <aside
            aria-label={t("tasks.detail.properties")}
            className="min-h-0 overflow-y-auto border-t border-line-default px-3 py-5 @[720px]:col-start-2 @[720px]:row-span-2 @[720px]:row-start-1 @[720px]:border-t-0 @[720px]:border-l"
          >
            <ExecutionSection orgId={orgId} task={task} people={people} />
            <WideProperties
              stats={taskStats}
              orgId={orgId}
              task={task}
              workflow={workflow}
              readOnly={readOnly}
              labels={labels}
              people={people}
              agents={agents}
              onOpenTask={onOpenTask}
            />
          </aside>
        )}
      </div>
    </div>
  );
}

/** 行内编辑：失焦或回车（单行）保存；Esc 放弃。 */
function InlineText({
  label,
  value,
  onSave,
  multiline = false,
  required = false,
  placeholder,
  maxLength,
  className,
  readOnly = false,
}: {
  label: string;
  value: string;
  onSave: (value: string) => Promise<unknown>;
  /** 已停用空间只读 */
  readOnly?: boolean;
  multiline?: boolean;
  required?: boolean;
  placeholder?: string;
  maxLength: number;
  className?: string;
}) {
  const [draft, setDraft] = useState(value);
  useEffect(() => {
    if (readOnly) setDraft(value);
  }, [readOnly, value]);
  const commit = async () => {
    if (readOnly) return;
    const next = multiline ? draft : draft.trim();
    if (next === value) return;
    if (required && !next) {
      setDraft(value);
      return;
    }
    try {
      await onSave(next);
    } catch {
      // The mutation reports the error; do not leave a rejected edit looking saved.
      setDraft((current) => (current === next ? value : current));
    }
  };
  const common = {
    "aria-label": label,
    value: draft,
    placeholder,
    maxLength,
    readOnly,
    onBlur: commit,
    className: cn(
      "w-full resize-none rounded-md bg-transparent px-1 -mx-1 outline-none placeholder:text-fg-subtle hover:bg-surface-hover focus:bg-transparent focus-visible:shadow-none",
      className,
    ),
  };
  if (multiline)
    return (
      <textarea
        {...common}
        rows={Math.min(20, Math.max(3, draft.split("\n").length))}
        onChange={(event) => setDraft(event.target.value)}
        onKeyDown={(event) => {
          if (event.key === "Escape") {
            event.stopPropagation();
            setDraft(value);
            event.currentTarget.blur();
          }
        }}
      />
    );
  return (
    <input
      {...common}
      onChange={(event) => setDraft(event.target.value)}
      onKeyDown={(event) => {
        if (event.key === "Enter") event.currentTarget.blur();
        if (event.key === "Escape") {
          event.stopPropagation();
          setDraft(value);
          requestAnimationFrame(() => event.currentTarget?.blur());
        }
      }}
    />
  );
}

function DescriptionActions({
  orgId,
  task,
  uploadButton,
  people,
  agents,
}: {
  orgId: string;
  task: TaskDetail;
  uploadButton: ReactNode;
  people: import("../queries").Person[];
  agents: import("./propertyPickers").AssigneeAgent[];
}) {
  const t = useT();
  const readOnly = useReadOnly();
  const reason = useReadOnlyReason();
  const { openCreate } = useCreateTaskDialog();
  const bus = useDetailBus();
  const moreLabel = `${t("tasks.detail.descLabel")}：${t("tasks.detail.more")}`;
  return (
    <div className="flex flex-wrap items-center gap-2">
      <Reactions
        orgId={orgId}
        task={task}
        reactions={task.description_reactions}
      />
      {uploadButton}
      <ReadOnlyTip>
        <Button
          variant="ghost-muted"
          size="28"
          leading={<Icon name="plus" />}
          aria-label={t("tasks.detail.addSubtask")}
          disabled={readOnly}
          onClick={() =>
            openCreate({
              spaceId: task.space_id,
              parentTaskId: task.id,
              parentDisplayId: displayIdOf(task),
            })
          }
        >
          {t("tasks.detail.subtasks")}
        </Button>
      </ReadOnlyTip>
      <Menu
        label={moreLabel}
        trigger={({ ref, toggle, open }) => (
          <IconButton
            ref={ref}
            label={moreLabel}
            icon={<Icon name="ellipsis" />}
            size="28"
            active={open}
            onClick={toggle}
          />
        )}
      >
        <MenuItem
          icon={<Icon name="link-2" />}
          disabled={readOnly}
          disabledReason={reason}
          onSelect={() => bus.setLinkEditing("new")}
        >
          {t("tasks.detail.addLink")}
        </MenuItem>
        <MenuItem
          icon={<Icon name="waypoints" />}
          disabled={readOnly}
          disabledReason={reason}
          onSelect={() => bus.setLinking(true)}
        >
          {t("tasks.detail.linkExisting")}
        </MenuItem>
      </Menu>
      <div className="ml-auto">
        <Followers orgId={orgId} task={task} people={people} agents={agents} />
      </div>
    </div>
  );
}
