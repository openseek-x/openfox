import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
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
  Kbd,
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
import { readLocal, writeLocal } from "../../storage";
import { workflowStatuses } from "../model";
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
import { withoutDuplicateStatuses } from "../detail/MarkDuplicateDialog";
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
import { useDirectory } from "../../agents/queries";
import { ProjectPicker } from "../../projects/ProjectPicker";
import { EstimateInput, useSpaceEstimation } from "../../spaces/EstimateField";
import { ComposeFields, ComposeShell } from "./ComposeShell";
import { TaskCreatorPicker } from "./TaskCreatorPicker";
import { isMacPlatform } from "../../me/shortcuts";
import { usePriorityLevels } from "../../organization/queries";

export interface CreateTaskInit {
  spaceId?: string;
  manual?: boolean;
  priority?: Priority;
  dueDate?: string;
  typeId?: string;
  statusId?: string;
  parentTaskId?: string;
  /** 阶段 9c-2 · 父任务编号（如 OPEN-1），只用于在面包屑里显示「父任务：OPEN-1」 */
  parentDisplayId?: string;
  /** 「创建关联任务」：创建成功后与该任务建立「相关」关联 */
  relateTo?: { spaceId: string; taskId: string };
  /** 预填处理人（成员页签、Agent 设置的「新建任务」）：value 为 user:/agent:/crew:<id> */
  assignee?: { value: string; name: string };
  /** Agent 工作台新建成功后打开详情；继续创建时仍保留表单。 */
  openAfterCreate?: boolean;
  submitToTriage?: boolean;
  /** 预填描述（阶段 5b「添加到任务」写入文档链接） */
  description?: string;
  title?: string;
  projectId?: string;
  iterationId?: string;
  sourceComment?: CreateTaskInput["source_comment"];
}

/**
 * 新建任务弹窗（Figma 页 10 区 1）：宽 672，展开后 896 × 83%。
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
  const modeKey = `openfox:task-create-mode:${orgId}`;
  const canUseAgent =
    !init.relateTo &&
    !init.sourceComment &&
    (!init.assignee || /^(agent|crew):/.test(init.assignee.value));
  const [agentMode, setAgentMode] = useState(
    () => canUseAgent && !init.manual && readLocal(modeKey) === "agent",
  );
  const directory = useDirectory(orgId, agentMode);
  const creators = (directory.data?.entries ?? []).filter(
    (entry) =>
      entry.space_id === spaceId &&
      !entry.archived_at &&
      (entry.type === "crew"
        ? !!entry.leader
        : entry.allow_assign && entry.allow_direct_chat),
  );
  const [creatorValue, setCreatorValue] = useState(init.assignee?.value ?? "");
  const creator = creatorValue
    ? creators.find((entry) => `${entry.type}:${entry.id}` === creatorValue)
    : creators[0];
  const [brief, setBrief] = useState(
    [init.title, init.description].filter(Boolean).join("\n\n"),
  );
  const [priorityOpen, setPriorityOpen] = useState(false);
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
  // 阶段 12f · 验收要求（B-01），与描述同样限长 10000
  const [acceptance, setAcceptance] = useState("");
  const [priority, setPriority] = useState<Priority>(init.priority ?? "NONE");
  // 阶段 12f · 处理人值列表（user:/agent:/crew:/invitation:），人员是否多选由空间开关决定
  const [assignees, setAssignees] = useState<string[]>(
    init.assignee ? [init.assignee.value] : [],
  );
  const multiAssignee = spaceDetail.data?.multi_assignee ?? false;
  const [labelIds, setLabelIds] = useState<string[]>([]);
  const [startDate, setStartDate] = useState<string | null>(null);
  const [dueDate, setDueDate] = useState<string | null>(init.dueDate ?? null);
  // 阶段 3a · 估时（只在启用估时的空间显示）
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
  const workflow = useMemo(
    () => (type ? workflowStatuses(statuses.data ?? [], type) : []),
    [type, statuses.data],
  );
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
  // OPE-17 · 新任务还没有可选的重复目标，候选里不提供重复类别
  const creatable = withoutDuplicateStatuses(workflow);
  const choices = triageEnabled
    ? creatable
    : creatable.filter((item) => item.category !== "TRIAGE");
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
    setAssignees([]);
    setEstimate(null);
    setProjectId(null);
    setIterationId(null);
  };

  const dirty =
    (agentMode && brief.trim() !== "") ||
    title.trim() !== "" ||
    description.trim() !== "" ||
    acceptance.trim() !== "";
  const requestClose = () => {
    if (dirty) setConfirmDiscard(true);
    else onClose();
  };
  useModalBehavior(!confirmDiscard, panel, requestClose);

  const agentList = (agents.data ?? []).filter((a) => !a.archived_at);
  const labelList: Label[] = labels.data ?? [];

  const changeMode = (next: boolean) => {
    if (next === agentMode) return;
    if (next)
      setBrief([title.trim(), description.trim()].filter(Boolean).join("\n\n"));
    else if (title.trim()) {
      const boundary = brief.indexOf("\n\n");
      const first = boundary < 0 ? brief : brief.slice(0, boundary);
      if (first.length <= 500) {
        setTitle(first);
        setDescription(boundary < 0 ? "" : brief.slice(boundary + 2));
      } else {
        setTitle("");
        setDescription(brief);
      }
    } else setDescription(brief);
    setAgentMode(next);
    writeLocal(modeKey, next ? "agent" : "manual");
    requestAnimationFrame(() =>
      panel.current?.querySelector<HTMLElement>("[data-autofocus]")?.focus(),
    );
  };
  // Reuse the key after an uncertain delivery, until the user changes the request.
  const previousBriefRequest = useRef<{ payload: string; key: string } | null>(
    null,
  );
  const submitBrief = useMutation({
    mutationFn: () => {
      if (!creator) throw new Error("Missing creator");
      const input = {
        brief: brief.trim(),
        ...(creator.type === "crew"
          ? { crew_id: creator.id }
          : { agent_id: creator.id }),
        priority,
        ...(projectId && { project_id: projectId }),
        ...(dueDate && { due_date: dueDate }),
        ...(type && { type_id: type.id }),
        ...(effectiveStatus && { status_id: effectiveStatus.id }),
        ...(submitToTriage && { submit_to_triage: true }),
        ...(iterationId && { iteration_id: iterationId }),
        ...(init.parentTaskId && { parent_task_id: init.parentTaskId }),
      };
      const payload = JSON.stringify([spaceId, input]);
      if (previousBriefRequest.current?.payload !== payload)
        previousBriefRequest.current = { payload, key: newIdempotencyKey() };
      return api.tasks.createWithAgent(
        orgId,
        spaceId,
        input,
        previousBriefRequest.current.key,
      );
    },
    onSuccess: () => {
      toast(t("tasks.create.queued"));
      if (!keepCreating) {
        onClose();
        return;
      }
      previousBriefRequest.current = null;
      setCreatedCount((n) => n + 1);
      setTitle("");
      setDescription("");
      setBrief("");
    },
    onError: (error) => {
      toast(taskErrorMessage(t, error), { tone: "error" });
    },
  });
  const submit = () => {
    if (agentMode) {
      if (
        space &&
        creator &&
        brief.trim() &&
        brief.length <= 10000 &&
        !submitBrief.isPending
      )
        submitBrief.mutate();
      return;
    }
    if (!space || !type || !title.trim() || create.isPending) return;
    const assigneeRefs = assignees.flatMap((value) => {
      const ref = refFromValue(value);
      return ref ? [ref] : [];
    });
    const input: CreateTaskInput = {
      type_id: type.id,
      ...(submitToTriage ? { submit_to_triage: true } : {}),
      ...(init.sourceComment ? { source_comment: init.sourceComment } : {}),
      title: title.trim(),
      priority,
      ...(effectiveStatus ? { status_id: effectiveStatus.id } : {}),
      ...(description.trim() ? { description } : {}),
      ...(acceptance.trim() ? { acceptance } : {}),
      ...(assigneeRefs.length ? { assignees: assigneeRefs } : {}),
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

  const assigneeLabel = (value: string) =>
    value === init.assignee?.value
      ? init.assignee.name
      : value.startsWith("agent:")
        ? agentList.find((a) => `agent:${a.id}` === value)?.name
        : value.startsWith("invitation:")
          ? invitations.find((invite) => `invitation:${invite.id}` === value)
              ?.email
          : people.find((p) => `user:${p.id}` === value)?.email;
  const assigneeName =
    assignees.map(assigneeLabel).filter(Boolean).join("、") || null;
  // 点击已选项取消；Agent 单选替换；人员按空间开关多选或替换
  const toggleAssignees = (value: string) => {
    if (value === "none") return setAssignees([]);
    if (assignees.includes(value))
      return setAssignees(assignees.filter((v) => v !== value));
    const isPerson = (v: string) =>
      v.startsWith("user:") || v.startsWith("invitation:");
    const kindOf = (v: string) => (isPerson(v) ? "person" : v.split(":")[0]);
    setAssignees([
      ...assignees.filter((v) =>
        isPerson(value) && multiAssignee ? true : kindOf(v) !== kindOf(value),
      ),
      value,
    ]);
  };

  const heading = agentMode
    ? t("tasks.create.withAgent")
    : type
      ? t("tasks.create.newOf", { type: type.name })
      : t("tasks.create.dialog");

  const projectPicker = spaceDetail.data?.features.projects && (
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
  );

  return (
    <ComposeShell
      panelRef={panel}
      label={heading}
      onBackdrop={requestClose}
      style={
        expanded
          ? { width: 896, height: "83%" }
          : { width: agentMode ? 576 : 672 }
      }
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
                // 阶段 9c-2 · 建子任务时后端要求与父任务同空间，锁定空间
                disabled={spaceLocked}
                title={
                  spaceLocked
                    ? t("tasks.detail.subtaskFullFormSpaceLocked")
                    : undefined
                }
                onClick={toggle}
                className={
                  agentMode
                    ? "flex h-6 min-w-0 items-center gap-1 rounded-sm text-fg-muted hover:text-fg-default"
                    : "flex h-6 items-center gap-1.5 rounded-full border border-line-default px-2 hover:bg-surface-hover disabled:cursor-not-allowed disabled:hover:bg-transparent"
                }
              >
                {!agentMode && (
                  <Avatar name={space?.name ?? "?"} kind="agent" size={16} />
                )}
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
          <span className="truncate font-medium text-fg-default">
            {heading}
          </span>
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
      {agentMode ? (
        <div
          className={`flex min-h-0 flex-col gap-2 px-5 ${expanded ? "flex-1" : ""}`}
        >
          <TaskCreatorPicker
            creators={creators}
            creator={creator}
            loading={directory.isPending}
            onSelect={setCreatorValue}
          />
          <textarea
            data-autofocus
            aria-label={t("tasks.create.brief")}
            placeholder={t("tasks.create.briefExample")}
            value={brief}
            maxLength={10000}
            rows={6}
            onChange={(event) => setBrief(event.target.value)}
            onKeyDown={(event) => {
              if (
                !event.nativeEvent.isComposing &&
                event.key === "Enter" &&
                (event.metaKey || event.ctrlKey)
              ) {
                event.preventDefault();
                submit();
              }
            }}
            className="min-h-12 w-full flex-auto resize-none rounded-sm bg-transparent text-body leading-6 text-fg-default outline-none placeholder:text-fg-muted focus-visible:shadow-none"
          />
        </div>
      ) : (
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
                  <Icon
                    name="chevron-down"
                    size={12}
                    className="text-fg-muted"
                  />
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
      )}

      <div hidden={agentMode} className="px-5">
        <textarea
          aria-label={t("tasks.create.acceptancePlaceholder")}
          placeholder={t("tasks.create.acceptancePlaceholder")}
          value={acceptance}
          maxLength={10000}
          rows={2}
          onChange={(event) => setAcceptance(event.target.value)}
          className="w-full resize-none bg-transparent text-body-sm outline-none placeholder:text-fg-subtle focus-visible:shadow-none"
        />
      </div>

      <div
        className={
          agentMode
            ? "flex flex-wrap items-center gap-1.5 px-4 pt-2 pb-3"
            : "flex flex-wrap items-center gap-1.5 px-5 py-3"
        }
      >
        {!agentMode && (
          <>
            <Picker
              label={t("tasks.fields.assignee")}
              groups={assigneeGroups(t, people, agentList, { invitations })}
              open={assignOpen}
              onOpenChange={setAssignOpen}
              multiple
              selected={assignees.length ? assignees : ["none"]}
              onSelect={toggleAssignees}
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
                        id={refFromValue(assignees[0] ?? "")?.id}
                        kind={refFromValue(assignees[0] ?? "")?.kind ?? "HUMAN"}
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
          </>
        )}
        {agentMode && projectPicker}
        {(!agentMode || priorityOpen || priority !== "NONE") && (
          <Picker
            label={t("tasks.fields.priority")}
            open={priorityOpen}
            onOpenChange={setPriorityOpen}
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
        )}
        {!agentMode && (
          <>
            <Picker
              label={t("tasks.fields.label")}
              multiple
              groups={labelGroups(labelList)}
              selected={labelIds}
              onSelect={(id) =>
                setLabelIds((prev) => toggleLabel(prev, id, labelList))
              }
              footer={{
                label: (query) =>
                  t("tasks.create.createLabel", { name: query }),
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
          </>
        )}
        {!agentMode && (showDates.start || startDate) && (
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
        {(dateOpen === "due" || showDates.due || dueDate) && (
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
        {!agentMode && estimation && (
          <EstimateInput
            estimation={estimation}
            value={estimate}
            onChange={setEstimate}
            className="h-7 w-[120px] text-body-sm"
          />
        )}
        {!agentMode && spaceDetail.data?.features.iterations && (
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
        {!agentMode && projectPicker}
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
          {agentMode ? (
            <MenuItem
              icon={<PriorityIcon priority="NONE" size={14} />}
              onSelect={() => setPriorityOpen(true)}
            >
              {t("tasks.fields.priority")}
            </MenuItem>
          ) : (
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
          )}
          <MenuItem
            icon={<Icon name="calendar" />}
            onSelect={() => {
              if (!agentMode) setShowDates((d) => ({ ...d, due: true }));
              setDateOpen("due");
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

      <div className="flex shrink-0 flex-wrap items-center justify-end gap-x-3 gap-y-2 border-t border-line-default px-4 py-3">
        {createdCount > 0 && (
          <span className="mr-auto text-body-sm text-fg-muted">
            {t("tasks.create.createdCount", { count: createdCount })}
          </span>
        )}
        {canUseAgent && (
          <button
            type="button"
            disabled={create.isPending || submitBrief.isPending}
            onClick={() => changeMode(!agentMode)}
            className="inline-flex min-h-7 items-center gap-1.5 rounded-md px-1.5 text-body-sm text-fg-muted hover:bg-surface-hover hover:text-fg-default disabled:opacity-50"
          >
            <Icon name="arrow-left-right" size={14} />
            {t(
              agentMode
                ? "tasks.create.switchToManual"
                : "tasks.create.switchToAgent",
            )}
          </button>
        )}
        <label className="flex cursor-pointer items-center gap-2 text-body-sm text-fg-muted">
          <Switch
            label={t("tasks.create.keepCreating")}
            checked={keepCreating}
            tone="success"
            onCheckedChange={setKeepCreating}
          />
          {t("tasks.create.keepCreating")}
        </label>
        <Button
          trailing={
            agentMode && (
              <span aria-hidden="true" className="hidden sm:inline-flex">
                <Kbd className="h-4 border-current/25 bg-transparent text-current">
                  {isMacPlatform() ? "⌘" : "Ctrl"} ↵
                </Kbd>
              </span>
            )
          }
          onClick={submit}
          disabled={
            !space ||
            (agentMode
              ? !creator || !brief.trim() || brief.length > 10000
              : !title.trim() || !type)
          }
          loading={create.isPending || submitBrief.isPending}
        >
          {t(agentMode ? "tasks.create.submitBrief" : "tasks.create.submit")}
        </Button>
      </div>
    </ComposeShell>
  );
}
