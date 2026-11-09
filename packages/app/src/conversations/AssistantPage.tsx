import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useWorkDirectoryCopy } from "./useWorkDirectoryCopy";
import { useEffect, useMemo, useRef, useState } from "react";
import { Link, Navigate, useParams, useSearchParams } from "react-router-dom";

import {
  isApiError,
  type AssistantSession,
  type ConversationSummary,
  type ConversationWorkspaceSelection,
  type Device,
  type SpaceAgent,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Button,
  Callout,
  Icon,
  IconButton,
  Menu,
  MenuItem,
  MenuSeparator,
  Popover,
  Skeleton,
  Spinner,
  cn,
  useToast,
} from "@openfox/ui";

import { RUNTIME_PROVIDERS } from "../agents/model";
import { useAgentSettingsParam } from "../agents/params";
import { useDevices, useMe } from "../agents/queries";
import { keys, useApi } from "../context";
import { TaskSearchDialog } from "../tasks/detail/TaskSearchDialog";
import { ConversationOfferDialog } from "../tasks/detail/ConversationOfferDialog";
import { conversationPath } from "./ChatPanel";
import { copyText } from "../tasks/parts";
import { StatusBadge, useTaskLinkActions } from "./ConversationHeader";
import { InlineRename } from "./ConversationItem";
import { AssistantDirectorySettings } from "./AssistantDirectorySettings";
import { AssistantStorageDialog } from "./AssistantStorageDialog";
import { RuntimeSessionExportDialog } from "./RuntimeSessionExportDialog";
import { ConversationView } from "./ConversationView";
import { newDefaultsOf } from "./useOverrides";
import {
  chatErrorMessage,
  invalidateConversation,
  sameWorkspaceSelection,
  useConversation,
  useConversationActions,
} from "./queries";

// 小助手：会话选择器、更多菜单、能力卡，消息流与输入框复用对话页。
// 小助手不存在时先 PUT /assistant（选第一台可用电脑）；没有电脑时引导去电脑页。

/** 新会话的地址参数值 */
const NEW_SESSION = "new";

/** 小助手在这台电脑上用哪个 Runtime：第一个可运行的（Codex 优先，其次 Claude Code、OpenCode、pi）。 */
export function assistantRuntime(device: Device): string | undefined {
  return RUNTIME_PROVIDERS.find((provider) =>
    device.runtimes.some(
      (runtime) => runtime.provider === provider && runtime.runnable,
    ),
  );
}

/**
 * 小助手放到哪台电脑：在线、有可运行的 Runtime；优先组织授权，其次本人授权，最后空间授权
 * （空间授权的电脑只在小助手恰好放到该空间时可用，服务端会校验）。
 */
export function pickAssistantDevice(
  devices: Device[],
  meId: string | undefined,
): Device | undefined {
  const runnable = devices.filter(
    (device) => device.online && assistantRuntime(device) !== undefined,
  );
  const rank = (device: Device) =>
    device.grant_scope === "ORGANIZATION"
      ? 0
      : device.grant_scope === "PERSONAL" && device.grant_user_id === meId
        ? 1
        : device.grant_scope === "SPACE"
          ? 2
          : 3;
  return runnable
    .filter((device) => rank(device) < 3)
    .sort((a, b) => rank(a) - rank(b))[0];
}

function useAssistant(orgId: string) {
  const api = useApi();
  return useQuery({
    queryKey: keys.assistant(orgId),
    queryFn: () => api.assistant.get(orgId),
  });
}

function useAssistantSessions(orgId: string, enabled: boolean) {
  const api = useApi();
  return useQuery({
    queryKey: keys.assistantSessions(orgId),
    queryFn: () => api.assistant.sessions(orgId),
    enabled,
  });
}

const byUpdated = (a: AssistantSession, b: AssistantSession) =>
  a.updated_at < b.updated_at ? 1 : a.updated_at > b.updated_at ? -1 : 0;

export function AssistantPage() {
  const t = useT();
  const api = useApi();
  const queryClient = useQueryClient();
  const { orgId = "" } = useParams();
  const me = useMe();
  const assistant = useAssistant(orgId);
  const missing = assistant.isSuccess && assistant.data === null;
  const devices = useDevices(orgId, missing);
  const device = useMemo(
    () => pickAssistantDevice(devices.data ?? [], me.data?.id),
    [devices.data, me.data],
  );
  const ensure = useMutation({
    mutationFn: (target: Device) =>
      api.assistant.ensure(orgId, {
        computer_id: target.id,
        grant_id: target.grant_id,
        runtime: assistantRuntime(target) ?? RUNTIME_PROVIDERS[0],
      }),
    onSuccess: (agent) =>
      queryClient.setQueryData(keys.assistant(orgId), agent),
  });
  // 每个页面实例只自动创建一次；失败后由用户点「重试」
  const attempted = useRef(false);
  useEffect(() => {
    if (!missing || !device || attempted.current) return;
    attempted.current = true;
    ensure.mutate(device);
  }, [missing, device, ensure]);

  if (assistant.isLoading || (missing && (devices.isLoading || me.isLoading)))
    return <AssistantSkeleton />;
  if (assistant.isError)
    return (
      <ErrorState
        message={t("chat.assistant.loadFailed")}
        onRetry={() => void assistant.refetch()}
      />
    );
  if (missing && !device && devices.isSuccess)
    return (
      <div className="flex h-full items-center justify-center p-10">
        <Callout
          tone="warning"
          title={t("chat.assistant.noComputer")}
          className="max-w-md"
          action={
            <Link to={`/o/${orgId}/settings/computers`}>
              <Button size="28" variant="outline">
                {t("chat.assistant.goComputers")}
              </Button>
            </Link>
          }
        >
          {t("chat.assistant.noComputerDesc")}
        </Callout>
      </div>
    );
  if (missing && ensure.isError)
    return (
      <ErrorState
        message={
          isApiError(ensure.error, "NO_ASSISTANT_SPACE")
            ? chatErrorMessage(t, ensure.error)
            : t("chat.assistant.setupFailed")
        }
        onRetry={() => device && ensure.mutate(device)}
      />
    );
  if (!assistant.data)
    return (
      <div
        role="status"
        className="flex h-full items-center justify-center gap-2 text-body text-fg-muted"
      >
        <Spinner />
        {t("chat.assistant.setup")}
      </div>
    );
  return <AssistantWorkspace orgId={orgId} assistant={assistant.data} />;
}

function AssistantSkeleton() {
  return (
    <div aria-busy="true" className="flex h-full flex-col">
      <div className="flex h-11 items-center gap-2 px-4">
        <Skeleton className="size-5 rounded-full" />
        <Skeleton className="h-4 w-28" />
      </div>
      <div className="mt-auto px-6 pb-4">
        <Skeleton className="mx-auto h-24 max-w-[752px] rounded-2xl" />
      </div>
    </div>
  );
}

function ErrorState({
  message,
  onRetry,
}: {
  message: string;
  onRetry: () => void;
}) {
  const t = useT();
  return (
    <div
      role="alert"
      className="flex h-full flex-col items-center justify-center gap-3 text-body text-fg-muted"
    >
      <Icon name="openfox-alert-circle" size={28} className="text-danger" />
      {message}
      <Button size="28" variant="outline" onClick={onRetry}>
        {t("common.retry")}
      </Button>
    </div>
  );
}

function AssistantWorkspace({
  orgId,
  assistant,
}: {
  orgId: string;
  assistant: SpaceAgent;
}) {
  const t = useT();
  const api = useApi();
  const queryClient = useQueryClient();
  const [params, setParams] = useSearchParams();
  const sessions = useAssistantSessions(orgId, true);
  const [directorySettings, setDirectorySettings] = useState(false);
  const settings = useAgentSettingsParam();
  const active = useMemo(
    () =>
      (sessions.data ?? []).filter((item) => !item.archived_at).sort(byUpdated),
    [sessions.data],
  );
  const requested = params.get("session");
  const sessionId =
    requested === NEW_SESSION
      ? null
      : (requested ?? (sessions.isSuccess ? (active[0]?.id ?? null) : null));
  const conversation = useConversation(orgId, sessionId);
  // 新会话的 id 由前端生成，第一条消息重试时不会重复创建
  const pendingId = useRef<string>(crypto.randomUUID());
  const created = useRef(false);

  const select = (id: string) =>
    setParams((prev) => {
      const next = new URLSearchParams(prev);
      next.set("session", id);
      return next;
    });

  const ensureConversation = async (
    workspace?: ConversationWorkspaceSelection,
  ) => {
    const id = pendingId.current;
    if (!created.current) {
      try {
        await api.conversations.create(orgId, {
          id,
          space_id: assistant.space_id,
          agent_id: assistant.id,
          visibility: "PRIVATE",
          ...(workspace ? { workspace_selection: workspace } : {}),
        });
      } catch (error) {
        const exists = await api.conversations
          .get(orgId, id)
          .then((existing) =>
            sameWorkspaceSelection(workspace, existing.workspace_selection),
          )
          .catch(() => false);
        if (!exists) throw error;
      }
      created.current = true;
    }
    return id;
  };

  const startNew = () => {
    pendingId.current = crypto.randomUUID();
    created.current = false;
    select(NEW_SESSION);
  };

  const agent = {
    id: assistant.id,
    name: t("chat.assistant.title"),
    avatar: assistant.avatar,
    runtime: assistant.config?.runtime ?? null,
    model: assistant.config?.model ?? null,
  };
  const loadingSession =
    (sessionId !== null && conversation.isLoading) ||
    (requested === null && sessions.isLoading);

  if (conversation.data && conversation.data.agent_id !== assistant.id)
    return (
      <Navigate to={conversationPath(orgId, conversation.data.id)} replace />
    );

  return (
    <div className="flex h-full flex-col">
      <AssistantHeader
        orgId={orgId}
        deviceId={assistant.config?.computer_id}
        sessions={sessions.data ?? []}
        sessionsLoading={sessions.isLoading}
        currentId={sessionId}
        conversation={conversation.data}
        onSelect={select}
        onNew={startNew}
      />
      {loadingSession ? (
        <AssistantSkeleton />
      ) : sessionId !== null && conversation.isError ? (
        <ErrorState
          message={chatErrorMessage(t, conversation.error)}
          onRetry={() => void conversation.refetch()}
        />
      ) : (
        <ConversationView
          key={sessionId ?? `new:${pendingId.current}`}
          orgId={orgId}
          agent={agent}
          newDefaults={
            assistant.config ? newDefaultsOf(assistant.config) : null
          }
          conversation={conversation.data}
          ensureConversation={ensureConversation}
          onConversationCreated={(id) => {
            invalidateConversation(queryClient, orgId, id);
            select(id);
          }}
          spaceId={conversation.data?.space_id ?? assistant.space_id}
          agentDetail={assistant}
          onOpenDirectorySettings={() => setDirectorySettings(true)}
          onOpenSettings={() => settings.open(assistant.id, assistant.space_id)}
          empty={
            <div
              className={cn(
                "flex flex-col items-center text-fg-muted",
                sessionId ? "gap-3" : "gap-5",
              )}
            >
              <Icon
                name="openfox-bot"
                size={sessionId ? 32 : 56}
                className="opacity-30"
              />
              <p className="text-body">
                {sessionId
                  ? t("chat.page.readyEmpty", {
                      title: conversation.data?.title || t("chat.untitled"),
                    })
                  : t("chat.page.startWith", { name: agent.name })}
              </p>
            </div>
          }
        />
      )}
      {directorySettings && (
        <AssistantDirectorySettings
          orgId={orgId}
          agentId={assistant.id}
          onClose={() => setDirectorySettings(false)}
        />
      )}
    </div>
  );
}

/** Header/Assistant：头像 + 「小助手」 / 会话下拉 / 状态徽标 / 更多菜单 */
function AssistantHeader({
  orgId,
  deviceId,
  sessions,
  sessionsLoading,
  currentId,
  conversation,
  onSelect,
  onNew,
}: {
  orgId: string;
  deviceId?: string;
  sessions: AssistantSession[];
  sessionsLoading: boolean;
  currentId: string | null;
  conversation?: ConversationSummary;
  onSelect: (id: string) => void;
  onNew: () => void;
}) {
  const t = useT();
  const toast = useToast();
  const actions = useConversationActions(orgId);
  const { convert, bind } = useTaskLinkActions(orgId, currentId ?? undefined);
  const [renaming, setRenaming] = useState(false);
  const [binding, setBinding] = useState(false);
  const [storageOpen, setStorageOpen] = useState(false);
  const [runtimeExportOpen, setRuntimeExportOpen] = useState(false);
  const [transferring, setTransferring] = useState(false);
  const me = useMe();
  const directoryCopy = useWorkDirectoryCopy(orgId, currentId);
  const current = sessions.find((item) => item.id === currentId);
  const title =
    (conversation?.title ?? current?.title)?.trim() ||
    (currentId ? t("chat.untitled") : t("chat.newSession"));
  const hasTask = Boolean(conversation?.task_id);
  const running =
    conversation?.status === "WORKING" || conversation?.status === "QUEUED";

  const copyLink = async () => {
    if (!currentId) return;
    const url = `${window.location.origin}/o/${orgId}/assistant?session=${encodeURIComponent(currentId)}`;
    if (await copyText(url)) toast(t("chat.header.linkCopied"));
  };

  return (
    <header className="flex h-11 shrink-0 items-center gap-1 border-b border-line-soft px-4">
      <span className="flex size-5 items-center justify-center rounded-full bg-surface-muted">
        <Icon name="bot-face" size={12} />
      </span>
      <span className="text-body font-medium">{t("chat.assistant.title")}</span>
      <span className="text-fg-subtle">/</span>
      {renaming && currentId ? (
        <div className="w-56">
          <InlineRename
            initial={conversation?.title ?? current?.title ?? ""}
            onSave={(value) => {
              setRenaming(false);
              actions.rename.mutate({ id: currentId, title: value });
            }}
            onCancel={() => setRenaming(false)}
          />
        </div>
      ) : (
        <SessionPicker
          orgId={orgId}
          sessions={sessions}
          loading={sessionsLoading}
          currentId={currentId}
          label={title}
          onSelect={onSelect}
          onNew={onNew}
        />
      )}
      {conversation?.task && (
        <span className="ml-1 flex h-6 items-center gap-1 rounded-md px-1.5 text-caption text-fg-secondary">
          <Icon
            name="openfox-clipboard-task"
            size={12}
            className="text-accent"
          />
          {conversation.task.display_id.toUpperCase()}
        </span>
      )}
      <span className="flex-1" />
      {conversation && <StatusBadge conversation={conversation} />}
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
        <MenuItem icon={<Icon name="openfox-new-chat" />} onSelect={onNew}>
          {t("chat.assistant.more.newChat")}
        </MenuItem>
        <MenuItem
          icon={<Icon name="arrow-left-right" />}
          disabled={
            !conversation ||
            conversation.owner_user_id !== me.data?.id ||
            conversation.lifecycle !== "OPEN" ||
            !!conversation.archived_at
          }
          onSelect={() => setTransferring(true)}
        >
          {t("chat.assistant.more.transfer")}
        </MenuItem>
        <MenuItem
          icon={<Icon name="openfox-file-plus" />}
          disabled={!currentId || hasTask || running}
          onSelect={() => currentId && convert.mutate(currentId)}
        >
          {t("chat.assistant.more.convertToTask")}
        </MenuItem>
        <MenuItem
          icon={<Icon name="openfox-file-input" />}
          disabled={!currentId || hasTask || running}
          onSelect={() => setBinding(true)}
        >
          {t("chat.assistant.more.bindTask")}
        </MenuItem>
        <MenuSeparator />
        <MenuItem
          icon={<Icon name="link" />}
          disabled={!currentId}
          onSelect={() => void copyLink()}
        >
          {t("chat.assistant.more.copyLink")}
        </MenuItem>
        <MenuItem
          icon={<Icon name="pencil" />}
          disabled={!currentId}
          onSelect={() => setRenaming(true)}
        >
          {t("chat.assistant.more.rename")}
        </MenuItem>
        <MenuSeparator />
        <MenuItem
          icon={<Icon name="copy" />}
          disabled={directoryCopy.disabled}
          disabledReason={directoryCopy.reason}
          onSelect={() => void directoryCopy.copy()}
        >
          {t("chat.assistant.more.copyWorkdir")}
        </MenuItem>
        <MenuItem
          icon={<Icon name="hard-drive" />}
          disabled={!deviceId}
          disabledReason={t("chat.assistant.storageNoComputer")}
          onSelect={() => setStorageOpen(true)}
        >
          {t("chat.assistant.more.storage")}
        </MenuItem>
        <MenuItem
          icon={<Icon name="download" />}
          disabled={!currentId || !conversation || running}
          onSelect={() => setRuntimeExportOpen(true)}
        >
          {t("chat.assistant.more.exportRuntimeSession")}
        </MenuItem>
      </Menu>
      {runtimeExportOpen && currentId && conversation && (
        <RuntimeSessionExportDialog
          orgId={orgId}
          conversationId={currentId}
          onClose={() => setRuntimeExportOpen(false)}
        />
      )}
      {transferring && conversation && (
        <ConversationOfferDialog
          key={conversation.id}
          orgId={orgId}
          conversationId={conversation.id}
          kind="TRANSFER"
          canAssign={false}
          assistant
          close={() => setTransferring(false)}
        />
      )}
      {storageOpen && deviceId && (
        <AssistantStorageDialog
          key={deviceId}
          orgId={orgId}
          deviceId={deviceId}
          onClose={() => setStorageOpen(false)}
        />
      )}
      {binding && conversation && (
        <TaskSearchDialog
          orgId={orgId}
          spaceId={conversation.space_id}
          title={t("chat.assistant.more.bindTask")}
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

/** Popover/SessionPicker：搜索、新建会话、最近；悬停 重命名 / 归档；已归档入口切到已归档列表 */
function SessionPicker({
  orgId,
  sessions,
  loading,
  currentId,
  label,
  onSelect,
  onNew,
}: {
  orgId: string;
  sessions: AssistantSession[];
  loading: boolean;
  currentId: string | null;
  label: string;
  onSelect: (id: string) => void;
  onNew: () => void;
}) {
  const t = useT();
  const actions = useConversationActions(orgId);
  const ref = useRef<HTMLButtonElement | null>(null);
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [showArchived, setShowArchived] = useState(false);
  const [renaming, setRenaming] = useState<string | null>(null);
  const q = query.trim().toLowerCase();
  const archived = sessions.filter((item) => item.archived_at);
  const list = sessions
    .filter((item) => Boolean(item.archived_at) === showArchived)
    .filter((item) => !q || item.title.toLowerCase().includes(q))
    .sort(byUpdated);

  const close = () => {
    setOpen(false);
    setQuery("");
    setShowArchived(false);
    setRenaming(null);
  };

  return (
    <>
      <button
        ref={ref}
        type="button"
        aria-label={t("chat.assistant.sessions")}
        aria-expanded={open}
        onClick={() => (open ? close() : setOpen(true))}
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
        onOpenChange={(value) => (value ? setOpen(true) : close())}
        anchorRef={ref}
        aria-label={t("chat.assistant.sessions")}
        className="w-80 p-0"
      >
        <input
          autoFocus
          aria-label={t("chat.assistant.searchSessions")}
          placeholder={t("chat.assistant.searchSessions")}
          value={query}
          maxLength={100}
          onChange={(event) => setQuery(event.target.value)}
          className="h-10 w-full border-b border-line-default bg-transparent px-3 text-body outline-none placeholder:text-fg-subtle"
        />
        <div className="max-h-[320px] overflow-y-auto p-1">
          {showArchived ? (
            <button
              type="button"
              onClick={() => setShowArchived(false)}
              className="flex h-8 w-full items-center gap-1.5 rounded-lg px-2.5 text-body-sm text-fg-muted hover:bg-surface-active"
            >
              <Icon name="arrow-left" size={14} />
              {t("chat.assistant.archived", { count: archived.length })}
            </button>
          ) : (
            <>
              <MenuItem
                icon={<Icon name="openfox-new-chat" />}
                onSelect={() => {
                  close();
                  onNew();
                }}
              >
                {t("chat.assistant.more.newChat")}
              </MenuItem>
              <div className="px-2.5 pt-2 pb-1 text-caption text-fg-muted">
                {t("chat.assistant.recent")}
              </div>
            </>
          )}
          {loading ? (
            <div className="flex justify-center py-3">
              <Spinner />
            </div>
          ) : list.length === 0 ? (
            <p className="px-2.5 py-2 text-body-sm text-fg-muted">
              {q ? t("chat.assistant.noMatch") : t("chat.assistant.noSessions")}
            </p>
          ) : (
            list.map((item) =>
              renaming === item.id ? (
                <div key={item.id} className="px-1 py-0.5">
                  <InlineRename
                    initial={item.title}
                    onSave={(value) => {
                      setRenaming(null);
                      actions.rename.mutate({ id: item.id, title: value });
                    }}
                    onCancel={() => setRenaming(null)}
                  />
                </div>
              ) : (
                <div
                  key={item.id}
                  className={cn(
                    "group flex h-9 items-center rounded-lg pr-1 hover:bg-surface-active",
                    item.id === currentId && "bg-surface-selected",
                  )}
                >
                  <button
                    type="button"
                    onClick={() => {
                      close();
                      onSelect(item.id);
                    }}
                    className="min-w-0 flex-1 truncate px-2.5 text-left text-body"
                  >
                    {item.title.trim() || t("chat.untitled")}
                  </button>
                  <span className="hidden items-center group-hover:flex group-focus-within:flex">
                    <IconButton
                      label={t("chat.row.rename")}
                      icon={<Icon name="pencil" size={14} />}
                      size="28"
                      variant="ghost"
                      className="size-6"
                      onClick={() => setRenaming(item.id)}
                    />
                    <IconButton
                      label={t(
                        showArchived ? "chat.row.restore" : "chat.row.archive",
                      )}
                      icon={
                        <Icon
                          name={
                            showArchived ? "archive-restore" : "archive-box"
                          }
                          size={14}
                        />
                      }
                      size="28"
                      variant="ghost"
                      className="size-6"
                      onClick={() =>
                        showArchived
                          ? actions.restore.mutate(item.id)
                          : actions.archive.mutate(item.id)
                      }
                    />
                  </span>
                </div>
              ),
            )
          )}
          {!showArchived && archived.length > 0 && (
            <button
              type="button"
              onClick={() => setShowArchived(true)}
              className="mt-1 flex h-8 w-full items-center gap-1.5 rounded-lg px-2.5 text-body-sm text-fg-muted hover:bg-surface-active"
            >
              <Icon name="archive" size={14} />
              {t("chat.assistant.archived", { count: archived.length })}
            </button>
          )}
        </div>
      </Popover>
    </>
  );
}
