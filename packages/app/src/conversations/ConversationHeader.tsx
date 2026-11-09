import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";

import {
  newIdempotencyKey,
  type ConversationAgent,
  type ConversationSummary,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Badge,
  Icon,
  IconButton,
  Menu,
  MenuItem,
  MenuLabel,
  MenuSeparator,
  Popover,
  useToast,
  type IconName,
} from "@openfox/ui";

import { AgentAvatar } from "../agents/AgentAvatar";
import { useFavoriteEntry } from "../agents/actions";
import { useAgentSettingsParam } from "../agents/params";
import { useDirectory, useMe } from "../agents/queries";
import { keys, useApi } from "../context";
import { TaskSearchDialog } from "../tasks/detail/TaskSearchDialog";
import { copyText, useTaskParam } from "../tasks/parts";
import { useOpenArtifactList } from "./artifacts/params";
import { useConversationArtifacts } from "./artifacts/queries";
import { conversationPath } from "./ChatPanel";
import { conversationDestination, useChatNavigate } from "./chatNavigation";
import { InlineRename, StatusWord } from "./ConversationItem";
import {
  activeRunId,
  conversationTitle,
  displayStatus,
  statusMeta,
} from "./model";
import { newChatPath } from "./NewChatPopover";
import {
  chatErrorMessage,
  invalidateConversation,
  patchConversation,
  storeConversation,
  useConversationActions,
  useConversations,
} from "./queries";
import { elapsedMs, formatDuration } from "./steps";

/** 每秒刷新的当前时间（工作中计时） */
function useTicker(active: boolean) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!active) return;
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, [active]);
  return now;
}

/** 状态徽标：已完成 / 工作中 · 计时 / 执行异常 / 等你回复 / 等你审阅 / 排队中 */
export function StatusBadge({
  conversation,
}: {
  conversation: ConversationSummary;
}) {
  const t = useT();
  const status = displayStatus(conversation);
  const meta = statusMeta(status);
  const working = conversation.status === "WORKING";
  const now = useTicker(working);
  if (!meta || status === "ABORTED") return null;
  const elapsed = working
    ? elapsedMs(conversation.run?.started_at, new Date(now))
    : null;
  return (
    <Badge tone={meta.tone} className="gap-1">
      <Icon name={meta.icon} size={12} />
      {elapsed !== null
        ? t("chat.header.working", {
            status: t(meta.label),
            duration: formatDuration(elapsed, t),
          })
        : t(meta.label)}
    </Badge>
  );
}

/** The task card also offers a link to the original conversation. */
export function ConversationStatusMenu({
  conversation,
  onDetails,
  readOnly = false,
}: {
  conversation: ConversationSummary;
  onDetails?: () => void;
  readOnly?: boolean;
}) {
  const t = useT();
  const me = useMe();
  const api = useApi();
  const client = useQueryClient();
  const toast = useToast();
  const orgId = conversation.organization_id;
  const canMark =
    !readOnly &&
    conversation.status_mark !== undefined &&
    conversation.lifecycle === "OPEN" &&
    conversation.owner_user_id === me.data?.id;
  const mark = useMutation({
    mutationFn: (value: "DONE" | "DISCARDED" | "NONE") =>
      api.conversations.update(orgId, conversation.id, { status_mark: value }),
    onSuccess: (updated) => {
      patchConversation(client, orgId, updated.id, () => updated);
    },
    onError: (error) => {
      toast(chatErrorMessage(t, error), { tone: "error" });
      invalidateConversation(client, orgId, conversation.id);
    },
  });
  if (!canMark && !onDetails)
    return <StatusBadge conversation={conversation} />;
  const label = t(
    onDetails ? "tasks.detail.statusActions" : "tasks.detail.markConversation",
  );
  const status = displayStatus(conversation);
  return (
    <Menu
      label={label}
      placement="bottom-end"
      trigger={({ ref, toggle, open }) => (
        <button
          ref={ref}
          type="button"
          aria-label={label}
          title={label}
          aria-haspopup="menu"
          aria-expanded={open}
          disabled={mark.isPending}
          onClick={toggle}
          className="relative z-10 inline-flex min-h-6 min-w-6 shrink-0 items-center justify-center rounded-md hover:bg-surface-active"
        >
          {status === "IDLE" || status === "ABORTED" ? (
            <Icon name="ellipsis" size={16} className="text-fg-muted" />
          ) : (
            <StatusBadge conversation={conversation} />
          )}
        </button>
      )}
    >
      {onDetails && (
        <MenuItem onSelect={onDetails}>
          {t("tasks.detail.viewConversation")}
        </MenuItem>
      )}
      {canMark && (
        <>
          {onDetails && <MenuSeparator />}
          <MenuLabel>{t("tasks.detail.markConversation")}</MenuLabel>
          {conversation.status_mark !== "DONE" && (
            <MenuItem onSelect={() => mark.mutate("DONE")}>
              {t("tasks.detail.markDone")}
            </MenuItem>
          )}
          {conversation.status_mark !== "DISCARDED" && (
            <MenuItem onSelect={() => mark.mutate("DISCARDED")}>
              {t("tasks.detail.markDiscarded")}
            </MenuItem>
          )}
          {conversation.status_mark && (
            <MenuItem onSelect={() => mark.mutate("NONE")}>
              {t("tasks.detail.clearMark")}
            </MenuItem>
          )}
        </>
      )}
    </Menu>
  );
}

/** 标题下拉：切换该 Agent 的其他会话、新建会话 */
function SessionSwitcher({
  orgId,
  agentId,
  currentId,
  label,
}: {
  orgId: string;
  agentId: string;
  currentId: string | null;
  label: string;
}) {
  const t = useT();
  const navigate = useChatNavigate();
  const ref = useRef<HTMLButtonElement | null>(null);
  const [open, setOpen] = useState(false);
  const sessions = useConversations(orgId, { agent_id: agentId }, open);
  return (
    <>
      <button
        ref={ref}
        type="button"
        aria-label={t("chat.header.switchSession")}
        aria-expanded={open}
        onClick={() => setOpen((value) => !value)}
        className="flex h-7 min-w-0 items-center gap-1 rounded-md px-1.5 text-body font-medium hover:bg-surface-muted"
      >
        <span className="truncate">{label}</span>
        <Icon
          name="chevron-down"
          size={14}
          className="shrink-0 text-fg-muted"
        />
      </button>
      <Popover
        open={open}
        onOpenChange={setOpen}
        anchorRef={ref}
        role="menu"
        aria-label={t("chat.header.switchSession")}
        className="max-h-[360px] w-72 overflow-y-auto"
      >
        <MenuItem
          icon={<Icon name="openfox-new-chat" />}
          onSelect={() => {
            setOpen(false);
            navigate(newChatPath(orgId, { type: "agent", id: agentId }));
          }}
        >
          {t("chat.header.newSession")}
        </MenuItem>
        <MenuSeparator />
        {sessions.isLoading ? null : sessions.list.length === 0 ? (
          <p className="px-2.5 py-2 text-body-sm text-fg-muted">
            {t("chat.header.noSessions")}
          </p>
        ) : (
          sessions.list.map((item) => (
            <MenuItem
              key={item.id}
              selected={item.id === currentId}
              onSelect={() => {
                setOpen(false);
                navigate(conversationDestination(orgId, item));
              }}
              trailing={<StatusWord status={displayStatus(item)} />}
            >
              {conversationTitle(item) ?? t("chat.untitled")}
            </MenuItem>
          ))
        )}
      </Popover>
    </>
  );
}

/** 转为任务 / 绑定已有任务（对话页与小助手共用） */
export function useTaskLinkActions(orgId: string, conversationId?: string) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const queryClient = useQueryClient();
  const fail = (error: unknown) => {
    toast(chatErrorMessage(t, error), { tone: "error" });
    if (conversationId)
      invalidateConversation(queryClient, orgId, conversationId);
  };
  const convert = useMutation({
    mutationFn: (id: string) =>
      api.conversations.convertToTask(orgId, id, newIdempotencyKey()),
    onSuccess: (summary) => {
      storeConversation(queryClient, orgId, summary);
      void queryClient.invalidateQueries({
        queryKey: keys.assistantSessions(orgId),
      });
      toast(t("chat.toast.converted", { id: summary.task?.display_id ?? "" }));
    },
    onError: fail,
  });
  const bind = useMutation({
    mutationFn: ({ id, taskId }: { id: string; taskId: string }) =>
      api.conversations.bindTask(orgId, id, taskId),
    onSuccess: (summary) => {
      storeConversation(queryClient, orgId, summary);
      void queryClient.invalidateQueries({
        queryKey: keys.assistantSessions(orgId),
      });
      toast(t("chat.toast.bound", { id: summary.task?.display_id ?? "" }));
    },
    onError: fail,
  });
  return { convert, bind };
}

/**
 * 对话页头部：Agent 头像 + 名称 / 会话标题下拉 / 新建会话 / 状态徽标 /
 * 更多菜单 / 收藏 / 设置。绑定任务时标题下显示任务编号链接。新会话时 conversation 为空。
 */
export function ConversationHeader({
  orgId,
  agent,
  spaceId,
  conversation,
  crew,
}: {
  orgId: string;
  agent: Pick<ConversationAgent, "id" | "name" | "avatar" | "online">;
  spaceId: string;
  conversation?: ConversationSummary;
  /** 小队对话：标题显示小队名，头像仍用队长的 */
  crew?: { id: string; name: string };
}) {
  const t = useT();
  const toast = useToast();
  const navigate = useChatNavigate();
  const me = useMe();
  const directory = useDirectory(orgId);
  const favorite = useFavoriteEntry(orgId);
  const settings = useAgentSettingsParam();
  const taskParam = useTaskParam();
  const actions = useConversationActions(orgId);
  const [renaming, setRenaming] = useState(false);
  const [binding, setBinding] = useState(false);

  const entry = directory.data?.entries.find(
    (item) => item.type === "agent" && item.id === agent.id,
  );
  const isFavorite = entry?.favorite ?? false;
  const title = conversation
    ? (conversationTitle(conversation) ?? t("chat.untitled"))
    : t("chat.newSession");
  const owner = conversation?.owner_user_id === me.data?.id;
  const running = Boolean(activeRunId(conversation));
  const hasTask = Boolean(conversation?.task_id);

  const { convert, bind } = useTaskLinkActions(orgId, conversation?.id);
  // 会话有产物时「更多」里显示「产物」，打开右侧产物面板的列表。
  const artifacts = useConversationArtifacts(orgId, conversation?.id);
  const openArtifacts = useOpenArtifactList();
  const artifactCount = artifacts.data?.length ?? 0;

  const copyLink = async () => {
    if (!conversation) return;
    const url = `${window.location.origin}${conversationPath(orgId, conversation.id)}`;
    if (await copyText(url)) toast(t("chat.header.linkCopied"));
    else toast(t("chat.errors.generic"), { tone: "error" });
  };

  type Item = {
    key: string;
    icon: IconName;
    label: string;
    disabled?: boolean;
    danger?: boolean;
    onSelect: () => void;
  };
  const items: Item[] = conversation
    ? [
        conversation.lifecycle === "COMPLETED"
          ? {
              key: "reopen",
              icon: "rotate-ccw",
              label: t("chat.header.reopen"),
              disabled: !owner,
              onSelect: () =>
                actions.reopen.mutate({
                  id: conversation.id,
                  version: conversation.version,
                }),
            }
          : {
              key: "complete",
              icon: "openfox-circle-check",
              label: t("chat.header.complete"),
              disabled: !owner || running,
              onSelect: () =>
                actions.complete.mutate({
                  id: conversation.id,
                  version: conversation.version,
                }),
            },
        {
          key: "convert",
          icon: "openfox-file-plus",
          label: t("chat.header.convertToTask"),
          disabled: !owner || hasTask || running,
          onSelect: () => convert.mutate(conversation.id),
        },
        {
          key: "bind",
          icon: "openfox-file-input",
          label: t("chat.header.bindTask"),
          disabled: !owner || hasTask || running,
          onSelect: () => setBinding(true),
        },
        {
          key: "copy",
          icon: "link",
          label: t("chat.header.copyLink"),
          onSelect: () => void copyLink(),
        },
        ...(artifactCount > 0
          ? [
              {
                key: "artifacts",
                icon: "layers" as const,
                label: t("artifacts.menu"),
                onSelect: openArtifacts,
              },
            ]
          : []),
        {
          key: "rename",
          icon: "pencil",
          label: t("chat.header.rename"),
          disabled: !owner,
          onSelect: () => setRenaming(true),
        },
        {
          key: "archive",
          icon: "archive-box",
          label: t("chat.header.archive"),
          disabled: !owner,
          danger: true,
          onSelect: () => {
            actions.archive.mutate(conversation.id);
            navigate(`/o/${orgId}/chat`);
          },
        },
      ]
    : [];

  return (
    <header className="flex h-11 shrink-0 items-center gap-1 border-b border-line-soft px-4">
      <AgentAvatar
        name={agent.name}
        avatar={agent.avatar}
        size={20}
        offline={!agent.online}
      />
      <span className="truncate text-body font-medium">
        {crew?.name ?? agent.name}
      </span>
      {crew && (
        <span className="shrink-0 rounded bg-surface-muted px-1.5 text-caption text-fg-muted">
          {t("chat.crew.badge")}
        </span>
      )}
      <span className="text-fg-subtle">/</span>
      <div className="flex min-w-0 flex-col">
        {renaming && conversation ? (
          <div className="w-56">
            <InlineRename
              initial={conversationTitle(conversation) ?? ""}
              onSave={(value) => {
                setRenaming(false);
                actions.rename.mutate({ id: conversation.id, title: value });
              }}
              onCancel={() => setRenaming(false)}
            />
          </div>
        ) : (
          <SessionSwitcher
            orgId={orgId}
            agentId={agent.id}
            currentId={conversation?.id ?? null}
            label={title}
          />
        )}
      </div>
      {conversation?.task && (
        <button
          type="button"
          onClick={() => taskParam.open(conversation.task!.display_id)}
          className="ml-1 flex h-6 items-center gap-1 rounded-md px-1.5 text-caption text-fg-secondary hover:bg-surface-muted"
        >
          <Icon
            name="openfox-clipboard-task"
            size={12}
            className="text-accent"
          />
          {conversation.task.display_id.toUpperCase()}
        </button>
      )}
      <IconButton
        label={t("chat.header.newSession")}
        icon={<Icon name="openfox-new-chat" size={16} />}
        size="28"
        variant="ghost"
        onClick={() =>
          navigate(
            newChatPath(orgId, {
              type: crew ? "crew" : "agent",
              id: crew?.id ?? agent.id,
            }),
          )
        }
      />
      <span className="flex-1" />
      {conversation && <StatusBadge conversation={conversation} />}
      {conversation && (
        <Menu
          label={t("chat.header.more")}
          placement="bottom-end"
          trigger={({ ref, toggle, open }) => (
            <IconButton
              ref={ref}
              label={t("chat.header.more")}
              icon={<Icon name="ellipsis" size={16} />}
              size="28"
              variant="ghost"
              aria-expanded={open}
              onClick={toggle}
            />
          )}
        >
          {items.map((item, index) => (
            <div key={item.key}>
              {index === 3 && <MenuSeparator />}
              <MenuItem
                icon={<Icon name={item.icon} />}
                disabled={item.disabled}
                danger={item.danger}
                onSelect={item.onSelect}
              >
                {item.label}
              </MenuItem>
            </div>
          ))}
        </Menu>
      )}
      <IconButton
        label={t(
          isFavorite ? "chat.header.unfavorite" : "chat.header.favorite",
        )}
        icon={
          <Icon
            name="openfox-star"
            size={16}
            className={isFavorite ? "text-warning" : undefined}
          />
        }
        size="28"
        variant="ghost"
        aria-pressed={isFavorite}
        disabled={!entry}
        onClick={() =>
          favorite.mutate({
            target: { type: "agent", id: agent.id },
            favorite: !isFavorite,
          })
        }
      />
      <button
        type="button"
        onClick={() => settings.open(agent.id, spaceId)}
        className="flex h-7 items-center gap-1 rounded-md px-1.5 text-body-sm text-fg-secondary hover:bg-surface-muted"
      >
        <Icon name="openfox-settings-sliders" size={14} />
        {t("chat.header.settings")}
      </button>
      {binding && conversation && (
        <TaskSearchDialog
          orgId={orgId}
          spaceId={conversation.space_id}
          title={t("chat.header.bindTask")}
          excludeIds={[]}
          onClose={() => setBinding(false)}
          onPick={(task) => {
            setBinding(false);
            bind.mutate({ id: conversation.id, taskId: task.id });
          }}
        />
      )}
    </header>
  );
}
