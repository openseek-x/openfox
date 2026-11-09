import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  useRef,
  useState,
  type ReactNode,
  type SelectHTMLAttributes,
} from "react";
import { Link } from "react-router-dom";

import type {
  AgentVisibility,
  ConversationMessage,
  SpaceDetail,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Button,
  Icon,
  SearchMenu,
  TextInput,
  Textarea,
  useToast,
} from "@openfox/ui";

import { deviceScopeBlock, isSupportedRuntime } from "../agents/model";
import {
  invalidateAgentLists,
  useDevices,
  useMe,
  useOrgRole,
  useSpaces,
} from "../agents/queries";
import { keys, useApi } from "../context";
import { SpaceIcon } from "../spaces/SpaceIcon";
import { canManageSpace, isRetired } from "../spaces/queries";
import { canWriteTasksIn } from "../spaces/readonly";
import { AssigneeStack, DueChip, LabelPills } from "../tasks/parts";
import { StatusIcon } from "../tasks/primitives/StatusIcon";
import { TaskTypeIcon } from "../tasks/primitives/TaskTypeIcon";
import {
  personName,
  usePeople,
  useSpaceAgents,
  useTaskByDisplayId,
} from "../tasks/queries";
import { chatErrorMessage } from "./queries";

// 小助手草稿卡（messages.kind = ACTION_PROPOSAL / ACTION_EXECUTED）。
// 草稿来自模型输出，属于不可信数据：只读白名单字段并按纯文本显示；确认时由用户编辑后提交。

interface ProposalPayload {
  action: "create_agent" | "create_task" | string;
  status: "pending" | "confirmed" | "cancelled" | string;
  agent?: { name?: string; description?: string; instructions?: string };
  task?: { title?: string; description?: string };
}

interface ExecutedPayload {
  action: string;
  space_id?: string;
  agent_id?: string;
  task_id?: string;
  identifier?: string;
  name?: string;
}

const str = (value: unknown) => (typeof value === "string" ? value : "");

export function readProposal(payload: unknown): ProposalPayload | null {
  if (typeof payload !== "object" || payload === null) return null;
  const raw = payload as Record<string, unknown>;
  const agent = raw.agent as Record<string, unknown> | undefined;
  const task = raw.task as Record<string, unknown> | undefined;
  return {
    action: str(raw.action),
    status: str(raw.status) || "pending",
    agent:
      agent && typeof agent === "object"
        ? {
            name: str(agent.name),
            description: str(agent.description),
            instructions: str(agent.instructions),
          }
        : undefined,
    task:
      task && typeof task === "object"
        ? { title: str(task.title), description: str(task.description) }
        : undefined,
  };
}

function readExecuted(payload: unknown): ExecutedPayload | null {
  if (typeof payload !== "object" || payload === null) return null;
  const raw = payload as Record<string, unknown>;
  return {
    action: str(raw.action),
    space_id: str(raw.space_id) || undefined,
    agent_id: str(raw.agent_id) || undefined,
    task_id: str(raw.task_id) || undefined,
    identifier: str(raw.identifier) || undefined,
    name: str(raw.name) || undefined,
  };
}

export function ExecutedCard({ message }: { message: ConversationMessage }) {
  const t = useT();
  const created = readExecuted(message.payload);
  const orgId = message.organization_id;
  const taskKey =
    created?.action === "create_task" ? created.identifier : undefined;
  const task = useTaskByDisplayId(orgId, taskKey ?? null).data;
  if (!created) return null;
  if (taskKey) {
    return (
      <Link
        to={`/o/${orgId}/tasks/${encodeURIComponent(taskKey)}`}
        aria-label={`${t("chat.proposal.taskCreated")} ${taskKey} ${created.name ?? ""}`}
        className="relative block w-full max-w-[332px] rounded-[10px] border border-line-default/80 bg-surface-card px-3 py-2.5 shadow-card hover:bg-surface-hover"
      >
        <div className="flex items-center justify-between gap-2">
          <span className="text-body-sm font-medium text-fg-muted">
            {taskKey}
          </span>
          {task && <AssigneeStack task={task} size={20} />}
        </div>
        <div className="mt-2 flex items-start gap-2 text-body-sm font-medium text-fg-default/80">
          {task && (
            <>
              <StatusIcon
                category={task.status_category}
                color={task.status_color}
                size={14}
              />
              <TaskTypeIcon icon={task.type_icon} size={14} />
            </>
          )}
          <span className="line-clamp-2 break-words">{created.name}</span>
        </div>
        {task &&
          (task.estimate != null ||
            task.label_items.length > 0 ||
            task.start_date ||
            task.due_date) && (
            <div className="mt-2 flex flex-wrap items-center gap-1">
              {task.estimate != null && (
                <span className="text-caption text-fg-muted">
                  {task.estimate}
                </span>
              )}
              <LabelPills task={task} max={task.label_items.length} />
              <DueChip date={task.start_date} />
              <DueChip date={task.due_date} />
            </div>
          )}
      </Link>
    );
  }
  if (created.action !== "create_agent" || !created.agent_id) return null;
  const url = `/o/${orgId}/members?contact=agent:${encodeURIComponent(created.agent_id)}`;
  const linkClass =
    "rounded-lg border border-line-default px-3 py-1.5 text-body-sm hover:bg-surface-hover";
  return (
    <div className="flex w-full flex-wrap items-center gap-2 rounded-[8px] bg-surface-card px-5 py-3 shadow-sm">
      <span className="text-body font-medium">
        {t("chat.proposal.agentReady")}
      </span>
      <span className="flex-1 text-body-sm text-fg-muted">{created.name}</span>
      <Link to={url} className={linkClass}>
        {t("chat.proposal.openAgent")}
      </Link>
      <Link
        to={`${url}&agentSettings=${encodeURIComponent(created.agent_id)}`}
        className={linkClass}
      >
        {t("chat.proposal.openSettings")}
      </Link>
    </div>
  );
}

function DraftField({
  label,
  children,
}: {
  label: string;
  children: ReactNode;
}) {
  return (
    <label className="block">
      <span className="mb-1 block text-caption text-fg-muted">{label}</span>
      {children}
    </label>
  );
}

function DraftSelect(props: SelectHTMLAttributes<HTMLSelectElement>) {
  return (
    <span className="relative block">
      <select
        {...props}
        className="h-9 w-full min-w-0 appearance-none rounded-lg border border-line-default bg-transparent pl-3 pr-8 text-body outline-none focus-visible:border-line-focus disabled:opacity-50"
      />
      <Icon
        name="chevron-down"
        size={14}
        className="pointer-events-none absolute top-1/2 right-2.5 -translate-y-1/2 text-fg-muted"
      />
    </span>
  );
}

function SpaceControl({
  spaces,
  value,
  onChange,
  placeholder,
}: {
  spaces: SpaceDetail[];
  value: string;
  onChange: (id: string) => void;
  placeholder: string;
}) {
  const t = useT();
  const anchor = useRef<HTMLButtonElement | null>(null);
  const [open, setOpen] = useState(false);
  const selected = spaces.find((s) => s.id === value);
  return (
    <div>
      <div className="mb-1 text-caption text-fg-muted">
        {t("chat.proposal.space")}
      </div>
      <button
        type="button"
        ref={anchor}
        aria-label={t("chat.proposal.space")}
        aria-haspopup="listbox"
        aria-expanded={open}
        className="flex h-9 w-full items-center gap-2 rounded-lg border border-line-default bg-surface-card px-3 text-body hover:bg-surface-hover"
        onClick={() => setOpen(!open)}
      >
        {selected && <SpaceIcon icon={selected.icon} />}
        <span className="min-w-0 flex-1 truncate text-left">
          {selected?.name ?? placeholder}
        </span>
        <Icon name="chevron-down" size={14} className="text-fg-muted" />
      </button>
      <SearchMenu
        open={open}
        onOpenChange={setOpen}
        anchorRef={anchor}
        label={t("chat.proposal.space")}
        placeholder={t("chat.proposal.searchSpace")}
        groups={[
          {
            options: spaces.map((s) => ({
              value: s.id,
              label: s.name,
              keywords: s.prefix,
              icon: <SpaceIcon icon={s.icon} />,
            })),
          },
        ]}
        selected={value ? [value] : []}
        onSelect={onChange}
      />
    </div>
  );
}

export function ProposalCard({
  orgId,
  message,
  defaultSpaceId,
}: {
  orgId: string;
  message: ConversationMessage;
  defaultSpaceId: string;
}) {
  const t = useT();
  const proposal = readProposal(message.payload);
  if (!proposal || !["create_agent", "create_task"].includes(proposal.action))
    return null;
  if (proposal.status !== "pending") {
    return (
      <div className="w-full rounded-lg border border-line-default px-4 py-3 text-body-sm text-fg-muted">
        {proposal.status === "confirmed" ? (
          <>
            {t("chat.proposal.drafted")}
            <span className="ml-1 font-medium text-fg-default">
              {proposal.agent?.name ?? proposal.task?.title}
            </span>
          </>
        ) : (
          t("chat.proposal.cancelled")
        )}
      </div>
    );
  }
  return <PendingProposal {...{ orgId, message, defaultSpaceId, proposal }} />;
}

function PendingProposal({
  orgId,
  message,
  defaultSpaceId,
  proposal,
}: {
  orgId: string;
  message: ConversationMessage;
  defaultSpaceId: string;
  proposal: ProposalPayload;
}) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const queryClient = useQueryClient();
  const isAgent = proposal.action === "create_agent";
  const spaces = useSpaces(orgId);
  const me = useMe();
  const role = useOrgRole(orgId);
  const eligible = (spaces.data ?? []).filter(
    (s) =>
      !isRetired(s) &&
      (isAgent ? canManageSpace(s, role) : canWriteTasksIn(s, role)),
  );
  const [pickedSpace, setSpace] = useState<string | null>(null);
  const newest =
    isAgent && pickedSpace === null
      ? [...eligible].sort((a, b) =>
          b.created_at.localeCompare(a.created_at),
        )[0]
      : undefined;
  const visibleSpace = pickedSpace ?? newest?.id ?? "";
  const effectiveSpace = eligible.find(
    (s) => s.id === (visibleSpace || defaultSpaceId),
  );
  const [name, setName] = useState(
    (isAgent ? proposal.agent?.name : proposal.task?.title) ?? "",
  );
  const [description, setDescription] = useState(
    proposal.task?.description ?? "",
  );
  const [instructions, setInstructions] = useState(
    proposal.agent?.instructions ?? "",
  );
  const [visibility, setVisibility] = useState<AgentVisibility>("PRIVATE");

  const devices = useDevices(orgId, isAgent);
  const assistant = useQuery({
    queryKey: keys.assistant(orgId),
    queryFn: () => api.assistant.get(orgId),
    enabled: isAgent,
  });
  const myId = me.data?.id;
  const choices =
    isAgent && myId
      ? (devices.data ?? [])
          .filter((d) => d.status === "ACTIVE" && d.grant_status === "ACTIVE")
          .flatMap((device) =>
            device.runtimes
              .filter((r) => isSupportedRuntime(r.provider))
              .map((r) => ({
                value: `${device.id}:${r.provider}`,
                provider: r.provider,
                deviceId: device.id,
                grantId: device.grant_id,
                block: deviceScopeBlock(device, visibleSpace, myId),
              })),
          )
      : [];
  const [pickedRuntime, setRuntime] = useState<string | null>(null);
  const assistantConfig = assistant.data?.config;
  const runtime = choices.find(
    (c) =>
      c.value ===
      (pickedRuntime ??
        `${assistantConfig?.computer_id}:${assistantConfig?.runtime}`),
  );
  const people = usePeople(orgId, !isAgent ? effectiveSpace : undefined);
  const [pickedPerson, setPerson] = useState<string | null>(null);
  const assignee =
    pickedPerson && people.some((person) => person.id === pickedPerson)
      ? pickedPerson
      : (myId ?? "");
  const [agentId, setAgent] = useState("");
  const agents = (
    useSpaceAgents(orgId, !isAgent && visibleSpace ? visibleSpace : undefined)
      .data ?? []
  ).filter((a) => !a.archived_at && !a.builtin_kind && a.allow_assign);
  const refresh = () =>
    void queryClient.invalidateQueries({
      queryKey: keys.conversationMessages(orgId, message.conversation_id),
    });
  const onError = (error: unknown) => {
    toast(chatErrorMessage(t, error), { tone: "error" });
    refresh();
  };
  const confirm = useMutation({
    mutationFn: () =>
      api.assistant.confirmProposal(
        orgId,
        message.conversation_id,
        message.id,
        isAgent
          ? {
              space_id: visibleSpace,
              name: name.trim(),
              instructions: instructions.trim(),
              visibility,
              computer_id: runtime!.deviceId,
              grant_id: runtime!.grantId,
              runtime: runtime!.provider,
            }
          : {
              space_id: effectiveSpace!.id,
              title: name.trim(),
              description: description.trim(),
              assignee_user_id: assignee,
              ...(agentId && { executor_agent_id: agentId }),
              start_working: false,
            },
      ),
    onSuccess: () => {
      refresh();
      if (isAgent) invalidateAgentLists(queryClient, orgId);
      else void queryClient.invalidateQueries({ queryKey: keys.tasks(orgId) });
    },
    onError,
  });
  const cancel = useMutation({
    mutationFn: () =>
      api.assistant.cancelProposal(orgId, message.conversation_id, message.id),
    onSuccess: refresh,
    onError,
  });
  const busy =
    confirm.isPending ||
    confirm.isSuccess ||
    cancel.isPending ||
    cancel.isSuccess;
  const invalid =
    !name.trim() ||
    !effectiveSpace ||
    (isAgent
      ? !runtime || !!runtime.block
      : !assignee || (!!agentId && !agents.some((a) => a.id === agentId)));
  const spaceControl = (
    <SpaceControl
      spaces={eligible}
      value={visibleSpace}
      placeholder={t(
        isAgent ? "chat.proposal.selectSpace" : "chat.proposal.defaultSpace",
      )}
      onChange={(id) => {
        setSpace(id);
        setAgent("");
      }}
    />
  );
  return (
    <section
      aria-label={t(
        isAgent ? "chat.proposal.createAgent" : "chat.proposal.createTask",
      )}
      className="w-full space-y-3 rounded-lg border border-line-default bg-surface-card p-4"
    >
      <DraftField
        label={t(isAgent ? "chat.proposal.name" : "chat.proposal.title")}
      >
        <TextInput
          value={name}
          maxLength={isAgent ? 255 : 500}
          placeholder={t(
            isAgent
              ? "chat.proposal.namePlaceholder"
              : "chat.proposal.titlePlaceholder",
          )}
          onChange={(e) => setName(e.target.value)}
        />
      </DraftField>
      {isAgent ? (
        <>
          <DraftField label={t("chat.proposal.instructions")}>
            <Textarea
              rows={4}
              value={instructions}
              maxLength={20000}
              onChange={(e) => setInstructions(e.target.value)}
            />
          </DraftField>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <DraftField label={t("chat.proposal.runtime")}>
              <DraftSelect
                value={runtime?.value ?? ""}
                onChange={(e) => setRuntime(e.target.value)}
              >
                <option value="">{t("chat.proposal.pickRuntime")}</option>
                {choices.map((c) => (
                  <option key={c.value} value={c.value} disabled={!!c.block}>
                    {c.provider}
                  </option>
                ))}
              </DraftSelect>
              {!choices.length && devices.isSuccess && (
                <span className="mt-1 block text-caption text-fg-muted">
                  {t("chat.proposal.runtimeEmpty")}
                </span>
              )}
            </DraftField>
            <DraftField label={t("chat.proposal.visibility")}>
              <DraftSelect
                value={visibility}
                onChange={(e) =>
                  setVisibility(e.target.value as AgentVisibility)
                }
              >
                <option value="PRIVATE">{t("chat.proposal.personal")}</option>
                <option value="SHARED">{t("chat.proposal.space")}</option>
              </DraftSelect>
            </DraftField>
          </div>
          {spaceControl}
        </>
      ) : (
        <>
          <DraftField label={t("chat.proposal.description")}>
            <Textarea
              rows={4}
              value={description}
              maxLength={10000}
              onChange={(e) => setDescription(e.target.value)}
            />
          </DraftField>
          {spaceControl}
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <DraftField label={t("chat.proposal.assignee")}>
              <DraftSelect
                value={assignee}
                onChange={(e) => setPerson(e.target.value)}
              >
                {me.data && !people.some((p) => p.id === me.data.id) && (
                  <option value={me.data.id}>{me.data.email}</option>
                )}
                {people.map((p) => (
                  <option key={p.id} value={p.id}>
                    {personName(p)}
                  </option>
                ))}
              </DraftSelect>
            </DraftField>
            <DraftField label="Agent">
              <DraftSelect
                value={agentId}
                disabled={!visibleSpace}
                onChange={(e) => setAgent(e.target.value)}
              >
                <option value="">
                  {t(
                    visibleSpace
                      ? "chat.proposal.noAgent"
                      : "chat.proposal.spaceFirst",
                  )}
                </option>
                {agents.map((a) => (
                  <option key={a.id} value={a.id}>
                    {a.name}
                  </option>
                ))}
              </DraftSelect>
            </DraftField>
          </div>
        </>
      )}
      <div className="flex items-center justify-end gap-2 pt-1">
        <Button
          variant="ghost"
          size="28"
          disabled={busy}
          onClick={() => cancel.mutate()}
        >
          {t("chat.proposal.cancel")}
        </Button>
        <Button disabled={busy || invalid} onClick={() => confirm.mutate()}>
          {t(
            isAgent
              ? "chat.proposal.confirmAgent"
              : "chat.proposal.confirmTask",
          )}
        </Button>
      </div>
    </section>
  );
}
