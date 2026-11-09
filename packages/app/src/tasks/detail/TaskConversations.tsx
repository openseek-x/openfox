import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useId, useState } from "react";
import { useSearchParams } from "react-router-dom";

import {
  newIdempotencyKey,
  type AssignmentConversation,
  type ConversationSummary,
  type Task,
} from "@openfox/api";
import { useI18n, useT } from "@openfox/i18n";
import {
  Avatar,
  Button,
  Icon,
  IconButton,
  Menu,
  MenuItem,
  MenuLabel,
  Tooltip,
  cn,
  useToast,
} from "@openfox/ui";

import { AgentAvatar } from "../../agents/AgentAvatar";
import { useApi } from "../../context";
import { ConversationStatusMenu } from "../../conversations/ConversationHeader";
import { useConversations } from "../../conversations/queries";
import {
  chatErrorMessage,
  invalidateConversation,
} from "../../conversations/queries";
import { conversationTitle, plainPreview } from "../../conversations/model";
import type { Person } from "../queries";
import { personName } from "../queries";
import { useReadOnly } from "../../spaces/readonly";
import { useOpenTaskPage } from "../navigation";
import { assigneeValue } from "../pickers";
import { formatRelativeTime } from "../primitives/dates";
import { useAgentDirectory, useSpaceAgents } from "../queries";
import { useRerunTaskAgent, useSetTaskAssignee } from "./assign";
import { findAssignmentConversation } from "./openSession";
import { ManageQuickCommandsDialog } from "./quickCommands/ManageQuickCommandsDialog";
import { QuickCommandIcon } from "./quickCommands/QuickCommandIcon";
import { unavailableText } from "./quickCommands/QuickCommandForm";
import { menuCommands, useQuickCommands } from "./quickCommands/queries";
import { useInsertQuickCommand } from "./quickCommands/insert";
import { useRunQuickCommand } from "./quickCommands/run";
import { RunChoiceDialog } from "./RunChoiceDialog";

/** 该任务绑定的会话（与执行区块共用同一份缓存）。 */
export function useTaskConversations(orgId: string, task: Pick<Task, "id">) {
  return useConversations(orgId, { task_id: task.id });
}

/** 动态、对话记录和宽栏共用真实会话摘要；点击始终打开原会话。 */
export function ConversationRow({
  task,
  item,
  compact = false,
  activity = false,
  people = [],
}: {
  task: Task;
  item: ConversationSummary;
  compact?: boolean;
  activity?: boolean;
  people?: Person[];
}) {
  const t = useT();
  const { locale } = useI18n();
  const openPage = useOpenTaskPage();
  const [params] = useSearchParams();
  const active = params.get("conversationId") === item.id;
  const previewId = useId();
  const readOnly = useReadOnly();
  const openDetails = () => openPage(task.display_id, item.id);
  const owner = people.find((person) => person.id === item.owner_user_id);
  // 老版本服务器只提供一条摘要；不额外逐卡请求整段消息，也不伪造第二条。
  const messages =
    item.recent_messages ?? (item.last_message ? [item.last_message] : []);
  const time = (
    <time
      dateTime={item.updated_at}
      className="shrink-0 text-caption text-fg-muted"
    >
      {formatRelativeTime(item.updated_at, locale)}
    </time>
  );
  return (
    <li
      className={cn(
        "relative flex w-full min-w-0 flex-col rounded-lg border border-line-default p-3 text-left text-body-sm hover:bg-surface-hover",
        compact ? "gap-1" : "gap-3",
        active && "bg-surface-active",
      )}
    >
      <div className="flex min-w-0 items-center gap-2">
        <span className="flex shrink-0 items-center -space-x-1">
          <AgentAvatar
            name={item.agent.name}
            avatar={item.agent.avatar}
            size={20}
          />
          {owner && (
            <span className="rounded-full ring-2 ring-surface-card">
              <Avatar
                name={personName(owner)}
                src={owner.avatar_url}
                size={16}
              />
            </span>
          )}
        </span>
        <button
          type="button"
          aria-label={t("tasks.detail.executionOpen", {
            agent: item.agent.name,
          })}
          aria-current={active || undefined}
          aria-describedby={
            compact || messages.length > 0 ? previewId : undefined
          }
          onClick={openDetails}
          className="min-w-0 flex-1 truncate text-left after:absolute after:inset-0 after:rounded-lg focus-visible:outline-none focus-visible:after:outline focus-visible:after:outline-2 focus-visible:after:outline-accent"
        >
          {activity
            ? t("tasks.detail.conversationWith", { agent: item.agent.name })
            : item.agent.name}
          <span className="text-fg-muted">
            {" "}
            ·{" "}
            {activity && owner
              ? t("tasks.detail.conversationStartedBy", {
                  name: personName(owner),
                })
              : (conversationTitle(item) ?? t("chat.untitled"))}
          </span>
        </button>
        <ConversationStatusMenu
          conversation={item}
          onDetails={openDetails}
          readOnly={readOnly}
        />
        {!compact && (
          <>
            {time}
            <Icon
              name="chevron-right"
              size={16}
              className="shrink-0 text-fg-muted"
            />
          </>
        )}
      </div>
      {compact ? (
        <span id={previewId} className="flex min-w-0 items-center gap-2 pl-7">
          <span className="min-w-0 flex-1 truncate text-fg-muted">
            {plainPreview(messages.at(-1)?.preview ?? "")}
          </span>
          {time}
        </span>
      ) : (
        messages.length > 0 && (
          <span
            id={previewId}
            className="flex min-w-0 flex-col gap-2 border-t border-line-default pt-3"
          >
            {messages.map((message) => (
              <span
                key={message.seq}
                className="line-clamp-2 min-w-0 break-words"
              >
                {"author_name" in message && Boolean(message.author_name) ? (
                  <span className="mr-2 font-medium">
                    {String(message.author_name)}
                  </span>
                ) : message.author_kind === "AGENT" ? (
                  <span className="mr-2 font-medium">{item.agent.name}</span>
                ) : null}
                <span className="text-fg-muted">
                  {plainPreview(message.preview)}
                </span>
              </span>
            ))}
          </span>
        )
      )}
    </li>
  );
}

export function ConversationListFeedback({
  query,
}: {
  query: ReturnType<typeof useTaskConversations>;
}) {
  const t = useT();
  return (
    <>
      {query.isError && (
        <div
          role="alert"
          aria-label={t("tasks.detail.transcript")}
          className="flex items-center gap-2 text-body-sm text-fg-muted"
        >
          <span>{t("chat.errors.generic")}</span>
          <Button
            variant="ghost-muted"
            size="28"
            disabled={query.isFetching}
            onClick={() =>
              void (query.isFetchNextPageError
                ? query.fetchNextPage()
                : query.refetch())
            }
          >
            {t("common.retry")}
          </Button>
        </div>
      )}
      {query.hasNextPage && !query.isFetchNextPageError && (
        <Button
          variant="ghost-muted"
          size="28"
          loading={query.isFetchingNextPage}
          onClick={() => void query.fetchNextPage()}
        >
          {t("tasks.detail.loadMoreConversations")}
        </Button>
      )}
    </>
  );
}

/** 「对话记录」包含摘要、分页和失败重试；空状态只在请求成功后显示。 */
export function TranscriptList({
  orgId,
  task,
  people = [],
}: {
  orgId: string;
  task: Task;
  people?: Person[];
}) {
  const t = useT();
  const conversations = useTaskConversations(orgId, task);
  if (conversations.isLoading) return null;
  if (conversations.isSuccess && conversations.list.length === 0)
    return (
      <div className="flex flex-col items-center gap-3 py-8 text-center">
        <p className="text-body-sm text-fg-muted">
          {t("tasks.detail.transcriptEmpty")}
        </p>
        <InvokeAgentButton orgId={orgId} task={task} />
      </div>
    );
  return (
    <div className="flex flex-col gap-3">
      <ul
        aria-label={t("tasks.detail.transcript")}
        className="flex flex-col gap-3"
      >
        {conversations.list.map((item) => (
          <ConversationRow
            key={item.id}
            task={task}
            item={item}
            people={people}
          />
        ))}
      </ul>
      <ConversationListFeedback query={conversations} />
    </div>
  );
}

/**
 * 召唤 Agent：选一个 Agent 新开挂在该任务上的会话，成功后打开 AI 面板。
 * 只读任务（空间已停用）不显示；进行中禁用，防止连发。
 */
export function InvokeAgentButton({
  orgId,
  task,
  className,
}: {
  orgId: string;
  task: Task;
  className?: string;
}) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const queryClient = useQueryClient();
  const readOnly = useReadOnly();
  const openPage = useOpenTaskPage();
  const writeAssignee = useSetTaskAssignee(orgId);
  const [level, setLevel] = useState<"root" | "assign" | "chat">("root");
  // 快捷指令：第一次打开菜单后才加载，避免每次打开任务详情都请求。
  const [armed, setArmed] = useState(false);
  const [managing, setManaging] = useState(false);
  const quickCommands = useQuickCommands(orgId, task.space_id, armed);
  const commands = menuCommands(quickCommands.data);
  const runCommand = useRunQuickCommand(orgId, task);
  const insertCommand = useInsertQuickCommand(orgId, task);
  // 召唤菜单指派：有可复用的指派会话时先选择运行对话；已指派的 Agent 再选一次就再运行一次。
  const taskConversations = useTaskConversations(orgId, task);
  const rerun = useRerunTaskAgent(orgId);
  const [choice, setChoice] = useState<{
    agentId: string;
    agentName: string;
    conversation: ConversationSummary;
  } | null>(null);
  const agents = (useSpaceAgents(orgId, task.space_id).data ?? []).filter(
    (agent) => !agent.archived_at && !agent.builtin_kind,
  );
  // 「把任务指派给 Agent」：不允许单独指派的 Agent 不列出；小队取自 Agent 目录里本空间未归档的项
  const assignableAgents = agents.filter((agent) => agent.allow_assign);
  const crews = (useAgentDirectory(orgId).data?.entries ?? []).filter(
    (e) => e.type === "crew" && e.space_id === task.space_id && !e.archived_at,
  );
  const assigned = new Set(task.assignees.map(assigneeValue));
  /** 让 Agent 运行：未指派时 PATCH 处理人，已指派时调再运行接口；成功后总是在任务全页打开运行对话 */
  const runAgent = (
    agentId: string,
    mode: AssignmentConversation,
    reuse: ConversationSummary | null,
  ) => {
    if (assigned.has(`agent:${agentId}`)) {
      if (!rerun.isPending)
        // 每次确认生成新的幂等键；mutation 的重试沿用同一组参数，也就复用同一个键
        rerun.mutate({
          task,
          agentId,
          mode,
          reuseConversationId: reuse?.id,
          idempotencyKey: newIdempotencyKey(),
        });
      return;
    }
    writeAssignee(
      task,
      `agent:${agentId}`,
      "AGENT",
      { people: [], agents: assignableAgents },
      {
        openAlways: true,
        assignmentConversation: reuse ? mode : undefined,
        reuseConversationId: reuse?.id,
      },
    );
  };
  const assign = (value: string, kind: "AGENT" | "CREW") => {
    // 小队维持原有逻辑：已指派时忽略，否则 PATCH 处理人（没有选择运行对话的弹窗）
    if (kind === "CREW") {
      if (assigned.has(value)) return;
      writeAssignee(task, value, kind, { people: [], agents: crews });
      return;
    }
    const agentId = value.slice("agent:".length);
    const reusable = findAssignmentConversation(
      taskConversations.list,
      agentId,
    );
    if (reusable) {
      const name =
        assignableAgents.find((agent) => agent.id === agentId)?.name ??
        reusable.agent.name;
      setChoice({ agentId, agentName: name, conversation: reusable });
      return;
    }
    // 没有可复用的指派会话：直接运行，服务端新建指派会话
    runAgent(agentId, "reuse", null);
  };
  // 进入子菜单不关闭外层菜单（Menu 点到可用菜单项时会自动关闭）
  const levelButton = (to: "root" | "assign" | "chat", label: string) => (
    <button
      type="button"
      role="menuitem"
      aria-haspopup={to === "root" ? undefined : "menu"}
      onClick={(event) => {
        event.stopPropagation();
        setLevel(to);
      }}
      className="flex min-h-[34px] w-full items-center gap-2.5 rounded-lg py-1.5 pr-2 pl-2.5 text-left text-body outline-none hover:bg-surface-active focus-visible:bg-surface-active"
    >
      {to === "root" && <Icon name="chevron-left" className="text-fg-muted" />}
      {to === "assign" && <Icon name="user-plus" className="text-fg-muted" />}
      {to === "chat" && <Icon name="message" className="text-fg-muted" />}
      <span className="flex-1">{label}</span>
      {to !== "root" && <Icon name="chevron-right" className="text-fg-muted" />}
    </button>
  );
  const invoke = useMutation({
    mutationFn: (agentId: string) =>
      api.conversations.create(orgId, {
        space_id: task.space_id,
        agent_id: agentId,
        task_id: task.id,
        // 任务会话与「指派即执行」一致设为 SHARED：任务可读的人都能在对话记录里看到
        visibility: "SHARED",
      }),
    onSuccess: (conversation) => {
      invalidateConversation(queryClient, orgId, conversation.id);
      openPage(task.display_id, conversation.id);
    },
    onError: (error) => toast(chatErrorMessage(t, error), { tone: "error" }),
  });
  if (readOnly) return null;
  return (
    <>
      <Menu
        label={t("tasks.detail.invokeAgent")}
        placement="bottom-start"
        className="max-h-[320px] min-w-[220px] overflow-y-auto"
        trigger={({ ref, toggle, open }) => (
          <Button
            ref={ref as React.Ref<HTMLButtonElement>}
            variant="outline"
            size="28"
            className={className}
            leading={<Icon name="bot" />}
            aria-haspopup="menu"
            aria-expanded={open}
            loading={
              invoke.isPending ||
              runCommand.isPending ||
              insertCommand.isPending ||
              rerun.isPending
            }
            disabled={
              invoke.isPending ||
              runCommand.isPending ||
              insertCommand.isPending ||
              rerun.isPending
            }
            onClick={() => {
              setLevel("root");
              setArmed(true);
              toggle();
            }}
          >
            {t("tasks.detail.invokeAgent")}
          </Button>
        )}
      >
        {(close) => (
          <>
            {/* 两级结构；根级两项之后是「快捷指令」分组 */}
            {level === "root" && (
              <>
                {levelButton("assign", t("tasks.detail.summonAssign"))}
                {levelButton("chat", t("tasks.detail.summonNewChat"))}
                <MenuLabel>{t("tasks.quickCommands.menuGroup")}</MenuLabel>
                {commands.length === 0 && (
                  <MenuItem disabled>
                    {t("tasks.quickCommands.menuEmpty")}
                  </MenuItem>
                )}
                {commands.map((command) => {
                  const reason = command.target.available
                    ? undefined
                    : unavailableText(t, command);
                  const busy = runCommand.isPending || insertCommand.isPending;
                  return (
                    <div key={command.id} className="flex items-center gap-0.5">
                      <div className="min-w-0 flex-1">
                        <MenuItem
                          icon={<QuickCommandIcon command={command} />}
                          disabled={!command.target.available || busy}
                          disabledReason={reason}
                          description={reason}
                          onSelect={() => {
                            if (!busy) runCommand.mutate(command);
                          }}
                        >
                          {command.name}
                        </MenuItem>
                      </div>
                      {/* 插入到输入框：独立按钮（不是菜单项，不触发主项的发送），Tab 可达 */}
                      <Tooltip content={t("tasks.quickCommands.insert")}>
                        <IconButton
                          label={t("tasks.quickCommands.insertLabel", {
                            name: command.name,
                          })}
                          icon={<Icon name="pencil-line" size={14} />}
                          size="28"
                          variant="ghost"
                          className="shrink-0"
                          disabled={!command.target.available || busy}
                          onClick={(event) => {
                            event.stopPropagation();
                            close();
                            insertCommand.mutate(command);
                          }}
                        />
                      </Tooltip>
                    </div>
                  );
                })}
                <MenuItem
                  icon={<Icon name="settings" />}
                  onSelect={() => setManaging(true)}
                >
                  {t("tasks.quickCommands.menuManage")}
                </MenuItem>
              </>
            )}
            {level === "assign" && (
              <>
                {levelButton("root", t("tasks.detail.summonBack"))}
                <MenuLabel>{t("tasks.detail.summonAssign")}</MenuLabel>
                {assignableAgents.length + crews.length === 0 && (
                  <MenuItem disabled>
                    {t("tasks.detail.summonAssignNone")}
                  </MenuItem>
                )}
                {assignableAgents.map((agent) => (
                  <MenuItem
                    key={agent.id}
                    icon={
                      <AgentAvatar
                        name={agent.name}
                        avatar={agent.avatar}
                        size={20}
                      />
                    }
                    selected={assigned.has(`agent:${agent.id}`)}
                    onSelect={() => assign(`agent:${agent.id}`, "AGENT")}
                  >
                    {agent.name}
                  </MenuItem>
                ))}
                {crews.length > 0 && (
                  <MenuLabel>{t("tasks.fields.crew")}</MenuLabel>
                )}
                {crews.map((crew) => (
                  <MenuItem
                    key={crew.id}
                    icon={<Avatar name={crew.name} kind="agent" size={20} />}
                    selected={assigned.has(`crew:${crew.id}`)}
                    onSelect={() => assign(`crew:${crew.id}`, "CREW")}
                  >
                    {crew.name}
                  </MenuItem>
                ))}
              </>
            )}
            {level === "chat" &&
              levelButton("root", t("tasks.detail.summonBack"))}
            {level === "chat" && (
              <MenuLabel>{t("tasks.detail.invokeAgentHint")}</MenuLabel>
            )}
            {level === "chat" && agents.length === 0 && (
              <MenuItem disabled>{t("tasks.detail.invokeAgentNone")}</MenuItem>
            )}
            {(level === "chat" ? agents : []).map((agent) => (
              <MenuItem
                key={agent.id}
                icon={
                  <AgentAvatar
                    name={agent.name}
                    avatar={agent.avatar}
                    size={20}
                  />
                }
                disabled={invoke.isPending}
                onSelect={() => {
                  if (!invoke.isPending) invoke.mutate(agent.id);
                }}
              >
                {agent.name}
              </MenuItem>
            ))}
          </>
        )}
      </Menu>
      <ManageQuickCommandsDialog
        orgId={orgId}
        spaceId={task.space_id}
        open={managing}
        onClose={() => setManaging(false)}
      />
      <RunChoiceDialog
        open={choice !== null}
        agentName={choice?.agentName ?? ""}
        conversationTitle={choice?.conversation.title ?? null}
        onClose={() => setChoice(null)}
        onConfirm={(mode) => {
          if (!choice) return;
          setChoice(null);
          runAgent(choice.agentId, mode, choice.conversation);
        }}
      />
    </>
  );
}
