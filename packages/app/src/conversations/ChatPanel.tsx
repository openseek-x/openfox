import { DirectChatList } from "./DirectChat";
import { useCallback, useRef, useState, type ReactNode } from "react";
import { Link, useMatch, useNavigate, useSearchParams } from "react-router-dom";

import type { ConversationSummary } from "@openfox/api";
import { useI18n, useT, type MessageKey } from "@openfox/i18n";
import {
  Button,
  Icon,
  IconButton,
  Popover,
  Skeleton,
  cn,
  type IconName,
} from "@openfox/ui";

import { AgentAvatar } from "../agents/AgentAvatar";
import { useFavoriteEntry } from "../agents/actions";
import { useAgentSettingsParam } from "../agents/params";
import { useDirectory, useMe } from "../agents/queries";
import { localKeys, readLocal, writeLocal } from "../storage";
import { SidebarCollapseButton } from "../shell/SidebarCollapseButton";
import { LoadMore } from "../tasks/parts";
import { formatRelativeTime } from "../tasks/primitives/dates";
import {
  ConversationItem,
  InlineRename,
  RowMenu,
  type RowMenuItem,
} from "./ConversationItem";
import {
  CHAT_MODES,
  DIRECT_VISIBLE,
  SESSION_GROUP_INITIAL,
  SESSION_GROUP_STEP,
  activeRunId,
  conversationTitle,
  directRows,
  groupByAgent,
  isChatMode,
  parseIdSet,
  splitPinned,
  type ChatMode,
  type DirectRow,
} from "./model";
import { NewChatButton, SmallCrewIcon, newChatPath } from "./NewChatPopover";
import { useConversationActions, useConversations } from "./queries";
import { conversationDestination } from "./chatNavigation";

const MODE_ICONS: Record<ChatMode, IconName> = {
  recents: "message-circle",
  organized: "hash",
  sessions: "list",
};

export function readChatMode(): ChatMode {
  const value = readLocal(localKeys.chatMode);
  return isChatMode(value) ? value : "recents";
}

/** 侧栏在嵌套路由之外渲染，从地址里取当前会话 id */
export function useCurrentConversationId(): string | undefined {
  const chat = useMatch("/o/:orgId/chat/:conversationId");
  const task = useMatch("/o/:orgId/tasks/:displayId");
  const [params] = useSearchParams();
  return (
    chat?.params.conversationId ??
    (task && params.get("view") === "chat"
      ? (params.get("conversationId") ?? undefined)
      : undefined)
  );
}

export const conversationPath = (orgId: string, id: string) =>
  `/o/${orgId}/chat/${encodeURIComponent(id)}`;

/** 打开 Agent 设置全屏的「运行历史」页签（?agentSettings=&agentTab=runs） */
export function useOpenRunHistory() {
  const settings = useAgentSettingsParam();
  return useCallback(
    (agentId: string, spaceId?: string) =>
      settings.open(agentId, spaceId, "runs"),
    [settings],
  );
}

/**
 * 「对话」页签的侧栏面板：顶部「对话」+ 新建；
 * 三种导航模式（微信 / Slack / 多会话），底部 ModeFooter 切换，选择存本机。
 */
export function ChatPanel({
  orgId,
  onCollapse,
}: {
  orgId: string;
  onCollapse: () => void;
}) {
  const t = useT();
  const [mode, setMode] = useState<ChatMode>(readChatMode);
  const change = (next: ChatMode) => {
    writeLocal(localKeys.chatMode, next);
    setMode(next);
  };
  return (
    <nav
      id="chat-sidebar"
      aria-label={t("chat.title")}
      className="group/panel flex min-w-0 flex-1 flex-col px-1.5 pt-1.5"
    >
      <div className="flex h-11 shrink-0 items-center gap-1 pr-1 pl-2.5">
        <h2 className="min-w-0 flex-1 truncate text-title font-bold">
          {t("chat.title")}
        </h2>
        <SidebarCollapseButton onClick={onCollapse} />
        <NewChatButton orgId={orgId} />
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto pb-2">
        <DirectChatList orgId={orgId} />
        {mode === "recents" && <RecentsMode orgId={orgId} />}
        {mode === "organized" && <OrganizedMode orgId={orgId} />}
        {mode === "sessions" && <SessionsMode orgId={orgId} />}
      </div>
      <div
        role="radiogroup"
        aria-label={t("chat.modes.label")}
        className="flex h-11 shrink-0 items-center justify-center gap-2 border-t border-line-default"
      >
        {CHAT_MODES.map((item) => (
          <button
            key={item}
            type="button"
            role="radio"
            aria-checked={mode === item}
            aria-label={t(`chat.modes.${item}` as MessageKey)}
            title={t(`chat.modes.${item}` as MessageKey)}
            onClick={() => change(item)}
            className={cn(
              "flex size-7 items-center justify-center rounded-md hover:bg-surface-muted",
              mode === item ? "text-fg-default" : "text-fg-subtle",
            )}
          >
            <Icon name={MODE_ICONS[item]} size={16} />
          </button>
        ))}
      </div>
    </nav>
  );
}

// ---------- 公共小件 ----------

function GroupLabel({
  label,
  open,
  onToggle,
  trailing,
}: {
  label: string;
  open: boolean;
  onToggle: () => void;
  trailing?: ReactNode;
}) {
  return (
    <div className="group mt-2 flex h-7 items-center pr-1">
      <button
        type="button"
        aria-expanded={open}
        onClick={onToggle}
        className="flex flex-1 items-center gap-1 px-2 text-body-sm text-fg-muted hover:text-fg-default"
      >
        <span>{label}</span>
        <Icon
          name="chevron-down"
          size={12}
          className={cn("transition-transform", !open && "-rotate-90")}
        />
      </button>
      {trailing}
    </div>
  );
}

export function RowsSkeleton({ rows = 4 }: { rows?: number }) {
  return (
    <div aria-busy="true" className="flex flex-col gap-2 px-2 py-2">
      {Array.from({ length: rows }, (_, index) => (
        <div key={index} className="flex flex-col gap-1.5">
          <Skeleton className="h-3.5 w-3/4" />
          <Skeleton className="h-3 w-1/2" />
        </div>
      ))}
    </div>
  );
}

/** 初次加载失败（Row/LoadError）或刷新失败（保留旧列表，顶部提示） */
function LoadState({
  query,
  children,
  empty,
  skeleton,
}: {
  query: {
    isLoading: boolean;
    isError: boolean;
    isFetching: boolean;
    data?: unknown;
    refetch: () => unknown;
  };
  children: ReactNode;
  empty: boolean;
  skeleton?: ReactNode;
}) {
  const t = useT();
  if (query.isLoading) return <>{skeleton ?? <RowsSkeleton />}</>;
  if (query.isError && !query.data)
    return (
      <div
        role="alert"
        className="mx-1 flex items-center gap-2 rounded-lg px-2 py-2 text-body-sm text-fg-muted"
      >
        <Icon name="openfox-alert-circle" size={14} className="text-danger" />
        <span className="flex-1">{t("chat.list.loadFailed")}</span>
        <Button variant="ghost" size="28" onClick={() => void query.refetch()}>
          {t("chat.list.retry")}
        </Button>
      </div>
    );
  return (
    <>
      {query.isError && (
        <div
          role="status"
          className="mx-1 mb-1 flex items-center gap-2 rounded-lg bg-warning-soft px-2 py-1.5 text-caption text-warning"
        >
          <span className="flex-1">{t("chat.list.refreshFailed")}</span>
          <button
            type="button"
            className="font-medium underline-offset-2 hover:underline"
            onClick={() => void query.refetch()}
          >
            {t("chat.list.retry")}
          </button>
        </div>
      )}
      {empty ? (
        <p className="px-2.5 py-2 text-body-sm text-fg-muted">
          {t("chat.list.noAgentConversations")}
        </p>
      ) : (
        children
      )}
    </>
  );
}

/** 会话行菜单项（微信模式）：任务会话不能重命名；只有所有者能重命名、完成 */
function useRowMenu(orgId: string) {
  const t = useT();
  const me = useMe();
  const actions = useConversationActions(orgId);
  return (
    item: ConversationSummary,
    startRename: () => void,
  ): { menu: RowMenuItem[]; onComplete?: () => void } => {
    const owner = item.owner_user_id === me.data?.id;
    const canComplete =
      owner && item.lifecycle === "OPEN" && !activeRunId(item);
    const complete = () =>
      actions.complete.mutate({ id: item.id, version: item.version });
    const menu: RowMenuItem[] = [];
    if (owner && !item.task_id)
      menu.push({
        key: "rename",
        label: t("chat.row.rename"),
        icon: <Icon name="pencil" />,
        onSelect: startRename,
      });
    menu.push({
      key: "pin",
      label: t(item.pinned ? "chat.row.unpin" : "chat.row.pin"),
      icon: <Icon name={item.pinned ? "pin-off" : "pin"} />,
      onSelect: () => actions.pin.mutate({ id: item.id, pinned: !item.pinned }),
    });
    if (canComplete)
      menu.push({
        key: "complete",
        label: t("chat.row.complete"),
        icon: <Icon name="check" />,
        onSelect: complete,
      });
    return {
      menu,
      onComplete: canComplete ? complete : undefined,
    };
  };
}

// ---------- 微信模式 ----------

function RecentsMode({ orgId }: { orgId: string }) {
  const t = useT();
  const conversationId = useCurrentConversationId();
  const query = useConversations(orgId);
  const actions = useConversationActions(orgId);
  const rowMenu = useRowMenu(orgId);
  const [renaming, setRenaming] = useState<string | null>(null);
  const [pinnedOpen, setPinnedOpen] = useState(true);
  const [recentOpen, setRecentOpen] = useState(true);
  const { pinned, recent } = splitPinned(query.list);

  const row = (item: ConversationSummary) => {
    const { menu, onComplete } = rowMenu(item, () => setRenaming(item.id));
    return (
      <ConversationItem
        key={item.id}
        to={conversationDestination(orgId, item)}
        conversation={item}
        selected={item.id === conversationId}
        renaming={renaming === item.id}
        onRename={(title) => {
          setRenaming(null);
          actions.rename.mutate({ id: item.id, title });
        }}
        onRenameEnd={() => setRenaming(null)}
        onComplete={onComplete}
        menuItems={menu}
      />
    );
  };

  return (
    <LoadState query={query} empty={query.list.length === 0}>
      {pinned.length > 0 ? (
        <>
          <GroupLabel
            label={t("chat.list.pinned")}
            open={pinnedOpen}
            onToggle={() => setPinnedOpen((v) => !v)}
          />
          {pinnedOpen && <div className="flex flex-col">{pinned.map(row)}</div>}
          <GroupLabel
            label={t("chat.list.recent")}
            open={recentOpen}
            onToggle={() => setRecentOpen((v) => !v)}
          />
          {recentOpen && <div className="flex flex-col">{recent.map(row)}</div>}
        </>
      ) : (
        <div className="mt-1 flex flex-col">{recent.map(row)}</div>
      )}
      <LoadMore
        hasMore={Boolean(query.hasNextPage)}
        loading={query.isFetchingNextPage}
        onLoad={() => void query.fetchNextPage()}
      />
    </LoadState>
  );
}

// ---------- Slack 模式 ----------

function readHidden(orgId: string) {
  return parseIdSet(readLocal(localKeys.chatHiddenAgents(orgId)));
}

function OrganizedMode({ orgId }: { orgId: string }) {
  const t = useT();
  const navigate = useNavigate();
  const directory = useDirectory(orgId);
  const query = useConversations(orgId);
  const favorite = useFavoriteEntry(orgId);
  const openRuns = useOpenRunHistory();
  const [hidden, setHidden] = useState(() => readHidden(orgId));
  const [favOpen, setFavOpen] = useState(true);
  const [directOpen, setDirectOpen] = useState(true);
  const [moreOpen, setMoreOpen] = useState(false);
  const moreRef = useRef<HTMLButtonElement | null>(null);
  const [searchParams] = useSearchParams();
  const conversationId = useCurrentConversationId();

  const entries = (directory.data?.entries ?? []).filter((e) => !e.archived_at);
  const favorites = entries.filter((entry) => entry.favorite);
  const rows = directRows(entries, query.list, hidden);
  const visible = rows.slice(0, DIRECT_VISIBLE);
  const overflow = rows.slice(DIRECT_VISIBLE);
  const currentAgent =
    searchParams.get("agent") ??
    query.list.find((item) => item.id === conversationId)?.agent_id ??
    null;

  const open = (row: DirectRow) => {
    if (row.entry.type === "crew") navigate(newChatPath(orgId, row.entry));
    else if (row.latest) navigate(conversationDestination(orgId, row.latest));
    else navigate(newChatPath(orgId, row.entry));
  };
  const hide = (agentId: string) => {
    const next = new Set(hidden);
    next.add(agentId);
    writeLocal(localKeys.chatHiddenAgents(orgId), JSON.stringify([...next]));
    setHidden(next);
  };
  const latestOf = (agentId: string) =>
    rows.find((row) => row.entry.id === agentId)?.latest ??
    query.list.find((item) => item.agent_id === agentId) ??
    null;

  const agentRow = (row: DirectRow, menu: RowMenuItem[]) => (
    <AgentNavRow
      key={`${row.entry.type}:${row.entry.id}`}
      row={row}
      selected={row.entry.id === currentAgent}
      onOpen={() => open(row)}
      menu={menu}
    />
  );
  const directMenu = (row: DirectRow): RowMenuItem[] => [
    {
      key: "runs",
      label: t("chat.row.runHistory"),
      icon: <Icon name="history" />,
      onSelect: () => openRuns(row.entry.id, row.entry.space_id),
    },
    {
      key: "remove",
      label: t("chat.row.remove"),
      icon: <Icon name="x" />,
      onSelect: () => hide(row.entry.id),
    },
  ];

  if (directory.isLoading) return <RowsSkeleton />;
  if (directory.isError && !directory.data)
    return (
      <LoadState query={directory} empty={false}>
        {null}
      </LoadState>
    );
  return (
    <>
      {favorites.length > 0 && (
        <>
          <GroupLabel
            label={t("chat.list.favorites")}
            open={favOpen}
            onToggle={() => setFavOpen((v) => !v)}
          />
          {favOpen &&
            favorites.map((entry) =>
              agentRow(
                {
                  entry,
                  latest: entry.type === "agent" ? latestOf(entry.id) : null,
                },
                [
                  ...(entry.type === "agent"
                    ? [
                        {
                          key: "runs",
                          label: t("chat.row.runHistory"),
                          icon: <Icon name="history" />,
                          onSelect: () => openRuns(entry.id, entry.space_id),
                        },
                      ]
                    : []),
                  {
                    key: "unfavorite",
                    label: t("chat.row.unfavorite"),
                    icon: <Icon name="star" />,
                    onSelect: () =>
                      favorite.mutate({
                        target: { type: entry.type, id: entry.id },
                        favorite: false,
                      }),
                  },
                ],
              ),
            )}
        </>
      )}
      <GroupLabel
        label={t("chat.list.direct")}
        open={directOpen}
        onToggle={() => setDirectOpen((v) => !v)}
        trailing={<NewChatButton orgId={orgId} className="size-6" />}
      />
      {directOpen &&
        (rows.length === 0 ? (
          <p className="px-2.5 py-2 text-body-sm text-fg-muted">
            {t("chat.list.noContacts")}
          </p>
        ) : (
          <>
            {visible.map((row) => agentRow(row, directMenu(row)))}
            {overflow.length > 0 && (
              <>
                <button
                  ref={moreRef}
                  type="button"
                  aria-expanded={moreOpen}
                  onClick={() => setMoreOpen((v) => !v)}
                  className="flex h-[30px] w-full items-center rounded-lg px-2.5 text-body-sm text-fg-muted hover:bg-surface-hover"
                >
                  {t("chat.list.more", { count: overflow.length })}
                </button>
                <Popover
                  open={moreOpen}
                  onOpenChange={setMoreOpen}
                  anchorRef={moreRef}
                  placement="right-start"
                  aria-label={t("chat.list.direct")}
                  className="max-h-[320px] w-[260px] overflow-y-auto"
                >
                  {overflow.map((row) => agentRow(row, directMenu(row)))}
                </Popover>
              </>
            )}
          </>
        ))}
    </>
  );
}

function AgentNavRow({
  row,
  selected,
  onOpen,
  menu,
}: {
  row: DirectRow;
  selected: boolean;
  onOpen: () => void;
  menu: RowMenuItem[];
}) {
  const t = useT();
  const { entry } = row;
  return (
    <div
      className={cn(
        "group flex h-[30px] items-center rounded-lg pr-1",
        selected ? "bg-surface-selected" : "hover:bg-surface-hover",
      )}
    >
      <button
        type="button"
        onClick={onOpen}
        aria-current={selected ? "page" : undefined}
        className="flex min-w-0 flex-1 items-center gap-2 pl-2 text-left text-body"
      >
        {entry.type === "crew" ? (
          <SmallCrewIcon />
        ) : (
          <AgentAvatar
            name={entry.name}
            avatar={entry.avatar}
            size={16}
            offline={!entry.online}
          />
        )}
        <span className="truncate">{entry.name}</span>
        <span className="min-w-0 flex-1 truncate text-caption text-fg-muted">
          {entry.type === "crew" ? t("chat.newChatPopover.crew") : entry.title}
        </span>
        {row.latest?.unread && (
          <span
            aria-label={t("chat.list.unread")}
            className="size-1.5 shrink-0 rounded-full bg-danger"
          />
        )}
      </button>
      <RowMenu label={t("chat.row.actions")} items={menu} />
    </div>
  );
}

// ---------- 多会话模式 ----------

function SessionsMode({ orgId }: { orgId: string }) {
  const t = useT();
  const { locale } = useI18n();
  const navigate = useNavigate();
  const conversationId = useCurrentConversationId();
  const query = useConversations(orgId);
  const actions = useConversationActions(orgId);
  const openRuns = useOpenRunHistory();
  const me = useMe();
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());
  const [limits, setLimits] = useState<Record<string, number>>({});
  const [renaming, setRenaming] = useState<string | null>(null);
  const groups = groupByAgent(query.list);

  return (
    <LoadState
      query={query}
      empty={groups.length === 0}
      skeleton={<RowsSkeleton rows={3} />}
    >
      {groups.map((group) => {
        const agentId = group.agent.id;
        const isCollapsed = collapsed.has(agentId);
        const limit = limits[agentId] ?? SESSION_GROUP_INITIAL;
        const shown = group.conversations.slice(0, limit);
        const remaining = group.conversations.length - shown.length;
        const spaceId = group.conversations[0]?.space_id;
        return (
          <div key={agentId} className="mt-1">
            <div className="group flex h-[30px] items-center rounded-lg pr-1 hover:bg-surface-hover">
              <button
                type="button"
                aria-expanded={!isCollapsed}
                onClick={() =>
                  setCollapsed((prev) => {
                    const next = new Set(prev);
                    if (next.has(agentId)) next.delete(agentId);
                    else next.add(agentId);
                    return next;
                  })
                }
                className="flex min-w-0 flex-1 items-center gap-2 pl-2 text-left text-body"
              >
                <AgentAvatar
                  name={group.agent.name}
                  avatar={group.agent.avatar}
                  size={16}
                  offline={!group.agent.online}
                />
                <span className="truncate">{group.agent.name}</span>
                <span className="min-w-0 flex-1 truncate text-caption text-fg-muted">
                  {group.agent.title}
                </span>
                <span className="shrink-0 text-caption text-fg-muted group-hover:hidden">
                  {formatRelativeTime(group.updatedAt, locale)}
                </span>
              </button>
              <span className="hidden items-center group-hover:flex">
                <RowMenu
                  label={t("chat.row.actions")}
                  items={[
                    {
                      key: "runs",
                      label: t("chat.row.runHistory"),
                      icon: <Icon name="history" />,
                      onSelect: () => openRuns(agentId, spaceId),
                    },
                  ]}
                />
                <IconButton
                  label={t("chat.row.newSession")}
                  icon={<Icon name="plus" size={14} />}
                  size="28"
                  variant="ghost"
                  className="size-6"
                  onClick={() =>
                    navigate(newChatPath(orgId, { type: "agent", id: agentId }))
                  }
                />
              </span>
            </div>
            {!isCollapsed && (
              <div className="flex flex-col">
                {shown.map((item) => (
                  <SessionRow
                    key={item.id}
                    orgId={orgId}
                    item={item}
                    selected={item.id === conversationId}
                    renaming={renaming === item.id}
                    canEdit={item.owner_user_id === me.data?.id}
                    onStartRename={() => setRenaming(item.id)}
                    onRename={(title) => {
                      setRenaming(null);
                      if (title !== null)
                        actions.rename.mutate({ id: item.id, title });
                    }}
                    onArchive={() => actions.archive.mutate(item.id)}
                    onRuns={() => openRuns(agentId, item.space_id)}
                  />
                ))}
                {remaining > 0 && (
                  <button
                    type="button"
                    onClick={() =>
                      setLimits((prev) => ({
                        ...prev,
                        [agentId]: limit + SESSION_GROUP_STEP,
                      }))
                    }
                    className="flex h-7 items-center rounded-lg pl-8 text-body-sm text-fg-muted hover:bg-surface-hover"
                  >
                    {t("chat.list.showMore", {
                      count: Math.min(remaining, SESSION_GROUP_STEP),
                    })}
                  </button>
                )}
                {remaining <= 0 && limit > SESSION_GROUP_INITIAL && (
                  <button
                    type="button"
                    onClick={() =>
                      setLimits((prev) => ({
                        ...prev,
                        [agentId]: SESSION_GROUP_INITIAL,
                      }))
                    }
                    className="flex h-7 items-center rounded-lg pl-8 text-body-sm text-fg-muted hover:bg-surface-hover"
                  >
                    {t("chat.list.showLess")}
                  </button>
                )}
              </div>
            )}
          </div>
        );
      })}
      <LoadMore
        hasMore={Boolean(query.hasNextPage)}
        loading={query.isFetchingNextPage}
        onLoad={() => void query.fetchNextPage()}
      />
    </LoadState>
  );
}

function SessionRow({
  orgId,
  item,
  selected,
  renaming,
  canEdit,
  onStartRename,
  onRename,
  onArchive,
  onRuns,
}: {
  orgId: string;
  item: ConversationSummary;
  selected: boolean;
  renaming: boolean;
  canEdit: boolean;
  onStartRename: () => void;
  onRename: (title: string | null) => void;
  onArchive: () => void;
  onRuns: () => void;
}) {
  const t = useT();
  const { locale } = useI18n();
  const title = conversationTitle(item);
  if (renaming)
    return (
      <div className="py-0.5 pr-1 pl-8">
        <InlineRename
          initial={title ?? ""}
          onSave={onRename}
          onCancel={() => onRename(null)}
        />
      </div>
    );
  return (
    <div
      className={cn(
        "group flex h-[31px] items-center rounded-lg pr-1",
        selected ? "bg-surface-selected" : "hover:bg-surface-hover",
      )}
    >
      <Link
        to={conversationDestination(orgId, item)}
        aria-current={selected ? "page" : undefined}
        className="flex min-w-0 flex-1 items-center gap-1.5 pl-8 text-body"
      >
        {item.unread && (
          <span
            aria-label={t("chat.list.unread")}
            className="size-1.5 shrink-0 rounded-full bg-danger"
          />
        )}
        <span className="min-w-0 flex-1 truncate">
          {title ?? t("chat.untitled")}
        </span>
        <span className="shrink-0 text-caption text-fg-muted group-hover:hidden">
          {formatRelativeTime(item.updated_at, locale)}
        </span>
      </Link>
      <span className="hidden items-center group-hover:flex group-focus-within:flex">
        <IconButton
          label={t("chat.row.runHistory")}
          icon={<Icon name="clock" size={14} />}
          size="28"
          variant="ghost"
          className="size-6"
          onClick={onRuns}
        />
        {canEdit && !item.task_id && (
          <IconButton
            label={t("chat.row.rename")}
            icon={<Icon name="pencil" size={14} />}
            size="28"
            variant="ghost"
            className="size-6"
            onClick={onStartRename}
          />
        )}
        {canEdit && (
          <IconButton
            label={t("chat.row.archive")}
            icon={<Icon name="archive-box" size={14} />}
            size="28"
            variant="ghost"
            className="size-6"
            onClick={onArchive}
          />
        )}
      </span>
    </div>
  );
}
