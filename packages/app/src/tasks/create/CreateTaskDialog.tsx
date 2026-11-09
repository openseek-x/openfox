import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useMemo, useRef, useState } from "react";

import {
  isApiError,
  newIdempotencyKey,
  type CreateTaskInput,
  type Label,
  type Space,
} from "@openfox/api";
import { useI18n, useT } from "@openfox/i18n";
import {
  Avatar,
  Button,
  ConfirmDialog,
  DateFieldPopover,
  Icon,
  IconButton,
  Menu,
  MenuItem,
  Switch,
  useModalBehavior,
  useToast,
} from "@openfox/ui";

import {
  TaskAssigneeAvatar,
  useInvitationAssignees,
} from "../invitationAssignees";
import { keys, useApi } from "../../context";
import {
  LabelDot,
  Picker,
  PropertyChip,
  assigneeGroups,
  refFromValue,
  labelGroups,
  priorityGroups,
  statusGroups,
  toggleLabel,
} from "../pickers";
import { formatMonthDay } from "../primitives/dates";
import { PriorityIcon, type Priority } from "../primitives/PriorityIcon";
import { StatusIcon } from "../primitives/StatusIcon";
import { TaskTypeIcon } from "../primitives/TaskTypeIcon";
import {
  taskErrorMessage,
  useCreateTask,
  usePeople,
  usePickerLabels,
  useSpaceAgents,
  useSpaceTypes,
  useStatuses,
} from "../queries";
import { useOrgRole, useSpaceDetail } from "../../spaces/queries";
import { ProjectPicker } from "../../projects/ProjectPicker";
import { EstimateInput, useSpaceEstimation } from "../../spaces/EstimateField";
import { ComposeFields, ComposeShell } from "./ComposeShell";
import { usePriorityLevels } from "../../organization/queries";

export interface CreateTaskInit {
  spaceId?: string;
  typeId?: string;
  statusId?: string;
  parentTaskId?: string;
  /** 父任务编号（如 OPEN-1），只用于在面包屑里显示「父任务：OPEN-1」 */
  parentDisplayId?: string;
  /** 「创建关联任务」：创建成功后与该任务建立「相关」关联 */
  relateTo?: { spaceId: string; taskId: string };
  /** 预填处理人（成员页签、Agent 设置的「新建任务」）：value 为 user:/agent:/crew:<id> */
  assignee?: { value: string; name: string };
  /** Agent 工作台新建成功后打开详情；继续创建时仍保留表单。 */
  openAfterCreate?: boolean;
  submitToTriage?: boolean;
  /** 预填描述（「添加到任务」会写入文档链接） */
  description?: string;
  title?: string;
  projectId?: string;
  iterationId?: string;
  sourceComment?: CreateTaskInput["source_comment"];
}

/**
 * 新建任务弹窗：宽 672，展开后 896 × 83%。
 * 提交带 Idempotency-Key，每次打开与每次成功提交后重新生成。
 */
export function CreateTaskDialog({
  orgId,
  spaces,
  init,
  onClose,
  onCreated,
}: {
  orgId: string;
  spaces: Space[];
  init: CreateTaskInit;
  onClose: () => void;
  onCreated: (displayId: string) => void;
}) {
  const t = useT();
  const priorityLevels = usePriorityLevels();
  const { locale } = useI18n();
  const api = useApi();
  const toast = useToast();
  const queryClient = useQueryClient();
  const create = useCreateTask(orgId);
  const panel = useRef<HTMLDivElement | null>(null);

  const [spaceId, setSpaceId] = useState(init.spaceId ?? spaces[0]?.id ?? "");
  const space = spaces.find((s) => s.id === spaceId);
  const spaceLocked = Boolean(init.parentTaskId);
  const spaceDetail = useSpaceDetail(orgId, spaceId || undefined);
  const types = useSpaceTypes(orgId, spaceId || undefined);
  const statuses = useStatuses(orgId);
  const labels = usePickerLabels(orgId, spaceId || undefined);
  const agents = useSpaceAgents(orgId, spaceId || undefined);
  const people = usePeople(orgId, space);
  const [assignOpen, setAssignOpen] = useState(false);
  const { candidates: invitations } = useInvitationAssignees(
    orgId,
    assignOpen,
    space ? [space.id] : [],
  );

  const [typeId, setTypeId] = useState(init.typeId ?? "");
  const [statusId, setStatusId] = useState(init.statusId ?? "");
  const [title, setTitle] = useState(init.title ?? "");
  const [description, setDescription] = useState(init.description ?? "");
  const [priority, setPriority] = useState<Priority>("NONE");
  const [assignee, setAssignee] = useState(init.assignee?.value ?? "none");
  const [labelIds, setLabelIds] = useState<string[]>([]);
  const [startDate, setStartDate] = useState<string | null>(null);
  const [dueDate, setDueDate] = useState<string | null>(null);
  // 估时只在启用估时的空间显示。
  const estimation = useSpaceEstimation(orgId, spaceId || undefined);
  const [estimate, setEstimate] = useState<number | null>(null);
  const [iterationId, setIterationId] = useState<string | null>(
    init.iterationId ?? null,
  );
  const [projectId, setProjectId] = useState<string | null>(
    init.projectId ?? null,
  );
  const iterations = useQuery({
    queryKey: keys.iterations(orgId, spaceId, { status: "planned,active" }),
    queryFn: () =>
      api.iterations.list(orgId, spaceId, { status: "planned,active" }),
    enabled: Boolean(spaceDetail.data?.features.iterations),
  });
  const [showDates, setShowDates] = useState<{ start: boolean; due: boolean }>({
    start: false,
    due: false,
  });
  const [keepCreating, setKeepCreating] = useState(false);
  const [createdCount, setCreatedCount] = useState(0);
  const [expanded, setExpanded] = useState(false);
  const [confirmDiscard, setConfirmDiscard] = useState(false);
  const [idempotencyKey, setIdempotencyKey] = useState(newIdempotencyKey);
  const [dateOpen, setDateOpen] = useState<null | "start" | "due">(null);
  const startAnchor = useRef<HTMLElement | null>(null);
  const dueAnchor = useRef<HTMLElement | null>(null);

  const typeList = types.data ?? [];
  const type =
    typeList.find((x) => x.id === typeId) ??
    typeList.find((x) => x.is_default) ??
    typeList[0];
  // 工作流状态（按全局顺序）
  const workflow = useMemo(() => {
    const ids = new Set(type?.statuses.map((s) => s.status_id) ?? []);
    return (statuses.data ?? []).filter((s) => ids.has(s.id) && !s.archived_at);
  }, [type, statuses.data]);
  // 默认状态：组织默认在工作流里则用它，否则工作流默认
  const defaultStatusId =
    workflow.find((s) => s.is_default)?.id ??
    type?.statuses.find((s) => s.default)?.status_id ??
    workflow[0]?.id ??
    "";
  const orgRole = useOrgRole(orgId);
  const triageEnabled = spaceDetail.data?.features.triage;
  const guestTriage = triageEnabled && orgRole === "GUEST";
  const fromTriage =
    init.submitToTriage && init.spaceId === spaceId && triageEnabled !== false;
  const triageStatus = workflow.find((item) => item.category === "TRIAGE");
  const choices = triageEnabled
    ? workflow
    : workflow.filter((item) => item.category !== "TRIAGE");
  const selectedStatus = choices.find((item) => item.id === statusId);
  const submitToTriage =
    guestTriage ||
    (fromTriage && (!selectedStatus || selectedStatus.category === "TRIAGE"));
  const effectiveStatus = submitToTriage
    ? triageStatus
    : (selectedStatus ?? choices.find((item) => item.id === defaultStatusId));

  // 切换空间后类型、状态、标签、处理人都可能失效，重置
  const switchSpace = (id: string) => {
    if (id === spaceId) return;
    setSpaceId(id);
    setTypeId("");
    setStatusId("");
    setLabelIds([]);
    setAssignee("none");
    setEstimate(null);
    setProjectId(null);
    setIterationId(null);
  };

  const dirty = title.trim() !== "" || description.trim() !== "";
  const requestClose = () => {
    if (dirty) setConfirmDiscard(true);
    else onClose();
  };
  useModalBehavior(!confirmDiscard, panel, requestClose);

  const agentList = (agents.data ?? []).filter((a) => !a.archived_at);
  const labelList: Label[] = labels.data ?? [];

  const submit = () => {
    if (!space || !type || !title.trim() || create.isPending) return;
    const assigneeRef = refFromValue(assignee);
    const input: CreateTaskInput = {
      type_id: type.id,
      ...(submitToTriage ? { submit_to_triage: true } : {}),
      ...(init.sourceComment ? { source_comment: init.sourceComment } : {}),
      title: title.trim(),
      priority,
      ...(effectiveStatus ? { status_id: effectiveStatus.id } : {}),
      ...(description.trim() ? { description } : {}),
      ...(assigneeRef ? { assignees: [assigneeRef] } : {}),
      ...(labelIds.length ? { label_ids: labelIds } : {}),
      ...(startDate ? { start_date: startDate } : {}),
      ...(dueDate ? { due_date: dueDate } : {}),
      ...(estimation && estimate !== null ? { estimate } : {}),
      ...(iterationId ? { iteration_id: iterationId } : {}),
      ...(projectId ? { project_id: projectId } : {}),
      ...(init.parentTaskId ? { parent_task_id: init.parentTaskId } : {}),
    };
    create.mutate(
      { spaceId: space.id, input, idempotencyKey },
      {
        onSuccess: (task) => {
          setIdempotencyKey(newIdempotencyKey());
          if (init.sourceComment) {
            for (const sub of ["relations", "timeline", "subtasks"])
              void queryClient.invalidateQueries({
                queryKey: keys.taskSub(orgId, init.sourceComment.task_id, sub),
              });
          }
          if (init.relateTo && !init.sourceComment) {
            const source = init.relateTo;
            void api.tasks
              .createRelation(
                orgId,
                source.spaceId,
                source.taskId,
                { type_id: "builtin:RELATES", target_task_id: task.id },
                newIdempotencyKey(),
              )
              .then(() =>
                queryClient.invalidateQueries({
                  queryKey: keys.taskSub(orgId, source.taskId, "relations"),
                }),
              )
              .catch((error: unknown) =>
                toast(taskErrorMessage(t, error), { tone: "error" }),
              );
          }
          const displayId = task.display_id.toUpperCase();
          toast(t("tasks.create.created", { id: displayId }), {
            action: {
              label: t("tasks.create.view"),
              onClick: () => onCreated(displayId),
            },
            duration: 5000,
          });
          if (keepCreating) {
            setCreatedCount((n) => n + 1);
            setTitle("");
            setDescription("");
          } else {
            onClose();
            if (init.parentTaskId || init.openAfterCreate) onCreated(displayId);
          }
        },
        onError: (error) => {
          toast(taskErrorMessage(t, error), { tone: "error" });
          // 网络不确定时保留同一个幂等键，重试不会重复创建
          if (isApiError(error) && !error.uncertain && error.status !== 409)
            setIdempotencyKey(newIdempotencyKey());
        },
      },
    );
  };

  const createLabel = async (name: string) => {
    const trimmed = name.trim();
    if (!trimmed || !spaceId) return;
    try {
      const label = await api.labels.create(
        orgId,
        { name: trimmed, space_id: spaceId },
        newIdempotencyKey(),
      );
      await queryClient.invalidateQueries({
        queryKey: keys.labels(orgId, `picker:${spaceId}`),
      });
      setLabelIds((prev) => [...prev, label.id]);
      toast(t("tasks.create.labelCreated", { name: label.name }));
    } catch (error) {
      toast(taskErrorMessage(t, error), { tone: "error" });
    }
  };

  const assigneeName =
    assignee === "none"
      ? null
      : assignee === init.assignee?.value
        ? init.assignee.name
        : assignee.startsWith("agent:")
          ? agentList.find((a) => `agent:${a.id}` === assignee)?.name
          : assignee.startsWith("invitation:")
            ? invitations.find(
                (invite) => `invitation:${invite.id}` === assignee,
              )?.email
            : people.find((p) => `user:${p.id}` === assignee)?.email;

  const heading = type
    ? t("tasks.create.newOf", { type: type.name })
    : t("tasks.create.dialog");

  return (
    <ComposeShell
      panelRef={panel}
      label={heading}
      onBackdrop={requestClose}
      style={expanded ? { width: 896, height: "83%" } : { width: 672 }}
      breadcrumb={
        <>
          <Menu
            label={t("tasks.create.switchSpace")}
            trigger={({ ref, toggle, open }) => (
              <button
                ref={ref}
                type="button"
                aria-expanded={open}
                aria-label={`${t("tasks.create.switchSpace")}: ${space?.name ?? ""}`}
                // 建子任务时后端要求与父任务同空间，锁定空间。
                disabled={spaceLocked}
                title={
                  spaceLocked
                    ? t("tasks.detail.subtaskFullFormSpaceLocked")
                    : undefined
                }
                onClick={toggle}
                className="flex h-6 items-center gap-1.5 rounded-full border border-line-default px-2 hover:bg-surface-hover disabled:cursor-not-allowed disabled:hover:bg-transparent"
              >
                <Avatar name={space?.name ?? "?"} kind="agent" size={16} />
                <span className="max-w-[160px] truncate">
                  {space?.name ?? t("tasks.create.noSpace")}
                </span>
              </button>
            )}
          >
            {spaces.map((s) => (
              <MenuItem
                key={s.id}
                icon={<Avatar name={s.name} kind="agent" size={16} />}
                selected={s.id === spaceId}
                onSelect={() => switchSpace(s.id)}
              >
                {s.name}
              </MenuItem>
            ))}
          </Menu>
          {init.parentDisplayId && (
            <>
              <Icon name="chevron-right" size={12} className="text-fg-subtle" />
              <span
                data-testid="create-parent"
                className="shrink-0 text-fg-muted"
              >
                {t("tasks.detail.subtaskFullFormParent", {
                  id: init.parentDisplayId,
                })}
              </span>
            </>
          )}
          <Icon name="chevron-right" size={12} className="text-fg-subtle" />
          <span className="truncate text-fg-default">{heading}</span>
        </>
      }
      actions={
        <>
          <IconButton
            label={
              expanded ? t("tasks.create.collapse") : t("tasks.create.expand")
            }
            icon={<Icon name={expanded ? "minimize-2" : "maximize-2"} />}
            size="28"
            onClick={() => setExpanded((v) => !v)}
          />
          <IconButton
            label={t("common.close")}
            icon={<Icon name="x" />}
            size="28"
            onClick={requestClose}
          />
        </>
      }
      extra={
        <ConfirmDialog
          open={confirmDiscard}
          title={t("tasks.create.discardTitle")}
          description={t("tasks.create.discardDesc")}
          confirmLabel={t("tasks.create.discard")}
          cancelLabel={t("tasks.create.keepEditing")}
          danger
          onCancel={() => setConfirmDiscard(false)}
          onConfirm={onClose}
        />
      }
    >
      <ComposeFields
        fill={expanded}
        rows={expanded ? 12 : 3}
        title={title}
        onTitle={setTitle}
        titlePlaceholder={t("tasks.create.titlePlaceholder")}
        description={description}
        onDescription={setDescription}
        descPlaceholder={t("tasks.create.descPlaceholder")}
        onSubmit={submit}
      >
        <div>
          <Menu
            label={t("tasks.create.switchType")}
            trigger={({ ref, toggle, open }) => (
              <button
                ref={ref}
                type="button"
                aria-expanded={open}
                aria-label={`${t("tasks.fields.type")}: ${type?.name ?? ""}`}
                onClick={toggle}
                className="inline-flex h-6 items-center gap-1.5 rounded-md bg-surface-muted px-2 text-body-sm"
              >
                <TaskTypeIcon icon={type?.icon} size={14} />
                {type?.name ?? "—"}
                <Icon name="chevron-down" size={12} className="text-fg-muted" />
              </button>
            )}
          >
            {typeList.map((x) => (
              <MenuItem
                key={x.id}
                icon={<TaskTypeIcon icon={x.icon} />}
                selected={x.id === type?.id}
                onSelect={() => {
                  setTypeId(x.id);
                  setStatusId("");
                }}
              >
                {x.name}
              </MenuItem>
            ))}
          </Menu>
        </div>
      </ComposeFields>

      <div className="flex flex-wrap items-center gap-1.5 px-5 py-3">
        <Picker
          label={t("tasks.fields.assignee")}
          groups={assigneeGroups(t, people, agentList, { invitations })}
          open={assignOpen}
          onOpenChange={setAssignOpen}
          selected={[assignee]}
          onSelect={setAssignee}
          trigger={({ ref, toggle, open }) => (
            <PropertyChip
              ref={ref}
              active={open}
              onClick={toggle}
              label={t("tasks.fields.assignee")}
              muted={!assigneeName}
              icon={
                assigneeName ? (
                  <TaskAssigneeAvatar
                    name={assigneeName}
                    kind={refFromValue(assignee)?.kind ?? "HUMAN"}
                    size={16}
                  />
                ) : (
                  <Icon name="user" size={14} />
                )
              }
            >
              {assigneeName ?? t("tasks.fields.assignee")}
            </PropertyChip>
          )}
        />
        <Picker
          label={t("tasks.fields.status")}
          groups={statusGroups(choices)}
          selected={effectiveStatus ? [effectiveStatus.id] : []}
          onSelect={setStatusId}
          trigger={({ ref, toggle, open }) => (
            <PropertyChip
              ref={ref}
              active={open}
              onClick={guestTriage ? undefined : toggle}
              label={
                guestTriage
                  ? t("spaces.triage.enterHint")
                  : t("tasks.fields.status")
              }
              icon={
                effectiveStatus && (
                  <StatusIcon
                    category={effectiveStatus.category}
                    color={effectiveStatus.color}
                    size={14}
                  />
                )
              }
              muted={!effectiveStatus}
            >
              {effectiveStatus?.name ?? t("tasks.fields.status")}
            </PropertyChip>
          )}
        />
        <Picker
          label={t("tasks.fields.priority")}
          groups={priorityGroups(t, priorityLevels)}
          selected={[priority]}
          onSelect={(v) => setPriority(v as Priority)}
          trigger={({ ref, toggle, open }) => (
            <PropertyChip
              ref={ref}
              active={open}
              onClick={toggle}
              label={t("tasks.fields.priority")}
              muted={priority === "NONE"}
              icon={<PriorityIcon priority={priority} size={14} />}
            >
              {priority === "NONE"
                ? t("tasks.priorityNone")
                : t(`tasks.priority.${priority}`)}
            </PropertyChip>
          )}
        />
        <Picker
          label={t("tasks.fields.label")}
          multiple
          groups={labelGroups(labelList)}
          selected={labelIds}
          onSelect={(id) =>
            setLabelIds((prev) => toggleLabel(prev, id, labelList))
          }
          footer={{
            label: (query) => t("tasks.create.createLabel", { name: query }),
            requiresQuery: true,
            icon: <Icon name="plus" />,
            onSelect: (query) => void createLabel(query),
          }}
          trigger={({ ref, toggle, open }) => {
            const chosen = labelList.filter((l) => labelIds.includes(l.id));
            return (
              <PropertyChip
                ref={ref}
                active={open}
                onClick={toggle}
                label={t("tasks.fields.label")}
                muted={chosen.length === 0}
                icon={
                  chosen[0] ? (
                    <LabelDot color={chosen[0].color} />
                  ) : (
                    <Icon name="tag" size={14} />
                  )
                }
              >
                {chosen.length === 0
                  ? t("tasks.fields.label")
                  : chosen.length === 1
                    ? chosen[0]!.name
                    : t("tasks.filters.valuesCount", {
                        count: chosen.length,
                      })}
              </PropertyChip>
            );
          }}
        />
        {(showDates.start || startDate) && (
          <PropertyChip
            ref={(node) => {
              startAnchor.current = node;
            }}
            active={dateOpen === "start"}
            onClick={() => setDateOpen("start")}
            label={t("tasks.fields.startDate")}
            muted={!startDate}
            icon={<Icon name="calendar" size={14} />}
          >
            {startDate
              ? formatMonthDay(startDate, locale)
              : t("tasks.fields.startDate")}
          </PropertyChip>
        )}
        {(showDates.due || dueDate) && (
          <PropertyChip
            ref={(node) => {
              dueAnchor.current = node;
            }}
            active={dateOpen === "due"}
            onClick={() => setDateOpen("due")}
            label={t("tasks.fields.dueDate")}
            muted={!dueDate}
            icon={<Icon name="calendar" size={14} />}
          >
            {dueDate
              ? formatMonthDay(dueDate, locale)
              : t("tasks.fields.dueDate")}
          </PropertyChip>
        )}
        {estimation && (
          <EstimateInput
            estimation={estimation}
            value={estimate}
            onChange={setEstimate}
            className="h-7 w-[120px] text-body-sm"
          />
        )}
        {spaceDetail.data?.features.iterations && (
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
            selected={iterationId ? [iterationId] : []}
            onSelect={setIterationId}
            trigger={({ ref, toggle, open }) => (
              <PropertyChip
                ref={ref}
                active={open}
                onClick={toggle}
                label={t("spaces.sections.iterations")}
                muted={!iterationId}
                icon={<Icon name="repeat" size={14} />}
              >
                {iterations.data?.iterations.find(
                  (item) => item.id === iterationId,
                )?.display_name ?? t("spaces.sections.iterations")}
              </PropertyChip>
            )}
          />
        )}
        {spaceDetail.data?.features.projects && (
          <ProjectPicker
            orgId={orgId}
            spaceId={spaceId}
            value={projectId}
            onSelect={setProjectId}
            trigger={(name, { ref, toggle, open }) => (
              <PropertyChip
                ref={ref}
                active={open}
                onClick={toggle}
                label={t("projects.title")}
                muted={!projectId}
                icon={<Icon name="folder" size={14} />}
              >
                {name ?? t("projects.title")}
              </PropertyChip>
            )}
          />
        )}
        <Menu
          label={t("tasks.create.more")}
          trigger={({ ref, toggle, open }) => (
            <IconButton
              ref={ref}
              label={t("tasks.create.more")}
              icon={<Icon name="ellipsis" />}
              size="28"
              variant="round"
              active={open}
              onClick={toggle}
            />
          )}
        >
          <MenuItem
            icon={<Icon name="calendar" />}
            onSelect={() => {
              setShowDates((d) => ({ ...d, start: true }));
              // 等胶囊渲染出来再打开日期浮层
              requestAnimationFrame(() => setDateOpen("start"));
            }}
          >
            {t("tasks.fields.startDate")}
          </MenuItem>
          <MenuItem
            icon={<Icon name="calendar" />}
            onSelect={() => {
              setShowDates((d) => ({ ...d, due: true }));
              requestAnimationFrame(() => setDateOpen("due"));
            }}
          >
            {t("tasks.fields.dueDate")}
          </MenuItem>
        </Menu>
        <DateFieldPopover
          open={dateOpen === "start"}
          onOpenChange={(open) => setDateOpen(open ? "start" : null)}
          anchorRef={startAnchor}
          value={startDate}
          onChange={setStartDate}
        />
        <DateFieldPopover
          open={dateOpen === "due"}
          onOpenChange={(open) => setDateOpen(open ? "due" : null)}
          anchorRef={dueAnchor}
          value={dueDate}
          onChange={setDueDate}
        />
      </div>

      <div className="flex h-14 shrink-0 items-center gap-3 border-t border-line-default px-5">
        <span className="ml-auto flex items-center gap-2 text-body-sm">
          <Switch
            label={t("tasks.create.keepCreating")}
            checked={keepCreating}
            tone="success"
            onCheckedChange={setKeepCreating}
          />
          {t("tasks.create.keepCreating")}
          {createdCount > 0 && (
            <span className="text-accent">
              {t("tasks.create.createdCount", { count: createdCount })}
            </span>
          )}
        </span>
        <Button
          onClick={submit}
          disabled={!title.trim() || !type || !space}
          loading={create.isPending}
        >
          {t("tasks.create.submit")}
        </Button>
      </div>
    </ComposeShell>
  );
}
