import { DirectChatList } from "../conversations/DirectChat";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import {
  Link,
  NavLink,
  Navigate,
  Outlet,
  useLocation,
  useNavigate,
  useParams,
} from "react-router-dom";

import type { Organization } from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Avatar,
  Badge,
  Icon,
  IconButton,
  Menu,
  MenuItem,
  MenuLabel,
  MenuSeparator,
  cn,
  useToast,
  type IconName,
} from "@openfox/ui";

import { ProxyDialog } from "../settings/ProxyDialog";
import { NotificationWatcher } from "../me/NotificationWatcher";
import { useMyPreferences } from "../me/queries";
import { outlineStyle, readCachedTheme, useResolvedTheme } from "../me/theme";
import { OrgIcon } from "../organization/OrgIcon";
import { MembersNavigation } from "../agents/MembersPage";
import { AgentSettingsHost } from "../agents/settings/AgentSettingsDrawer";
import { CrewSettingsHost } from "../agents/settings/CrewSettingsDrawer";
import { ComputersRailButton } from "../computers/ComputersRail";
import {
  ChatPanel,
  RowsSkeleton,
  useCurrentConversationId,
} from "../conversations/ChatPanel";
import { conversationDestination } from "../conversations/chatNavigation";
import { ConversationItem } from "../conversations/ConversationItem";
import { useConversations } from "../conversations/queries";
import { keys, useApi } from "../context";
import { useInboxStats } from "../inbox/queries";
import { OnboardingWizard } from "../onboarding/OnboardingWizard";
import { onboardingPending } from "../onboarding/state";
import { useOrgEvents } from "../realtime/useOrgEvents";
import { localKeys, readLocal, writeLocal } from "../storage";
import { SettingsNavigation } from "../settings/SettingsLayout";
import { CreateSpaceHost } from "../spaces/create/CreateSpaceDialog";
import { joinedSpaces, useDeletedSpaceRedirect } from "../spaces/queries";
import { SpaceIcon } from "../spaces/SpaceIcon";
import { JoinOrCreateSpace } from "../spaces/SpacesNav";
import { useAutoRegisterLocal } from "../computers/localRegistration";
import {
  desktopBridge,
  type DesktopAccount,
  isDesktop,
  isMacDesktop,
} from "../desktop";
import { SearchLauncher } from "../search/SearchLauncher";
import { DesktopNav } from "./DesktopNav";
import { PanelResizeHandle } from "./PanelResizeHandle";
import { SidebarCollapseButton } from "./SidebarCollapseButton";
import { usePanelWidth } from "./panelWidth";
import {
  NavRejectContext,
  clearNavMemory,
  moduleOf,
  navTarget,
  recordNav,
  type NavReject,
} from "./navMemory";
import { AdvancedGuide } from "./AdvancedGuide";
import { LayoutModeButton, railItemsFor, useLayoutMode } from "./layoutMode";
import { SpaceMoreMenu } from "./SpaceMoreMenu";
import { SpaceTaskNav } from "./SpaceTaskNav";
import {
  CreateTaskProvider,
  NewTaskButton,
} from "../tasks/create/CreateTaskProvider";

/** 组织内的应用外壳：标题栏 + 图标栏 + 导航面板 + 主区域。 */
export function OrgLayout() {
  const t = useT();
  const { orgId = "" } = useParams();
  const api = useApi();
  const organizations = useQuery({
    queryKey: keys.organizations,
    queryFn: api.me.organizations,
  });
  const [collapsed, setCollapsed] = useState(
    () => readLocal(localKeys.sidebarCollapsed) === "1",
  );
  const [wizard, setWizard] = useState(() => onboardingPending(orgId));
  const sidebar = usePanelWidth({
    storageKey: localKeys.sidebarWidth,
    initial: 260,
    min: 220,
    max: 480,
    remaining: 439,
  });
  const location = useLocation();
  const inSettings = location.pathname.startsWith(`/o/${orgId}/settings`);
  const inMembers = location.pathname.startsWith(`/o/${orgId}/members`);
  // 首页内打开对话仍保留首页侧栏，与主导航高亮使用同一规则。
  const inChat = moduleOf(orgId, location.pathname, location.search) === "chat";
  // 主导航记忆：每次路由变化记录当前模块的最近地址（只在本次打开期间）
  const rejectRef = useRef<NavReject | null>(null);
  useEffect(() => {
    const current = location.pathname + location.search;
    const rejected =
      rejectRef.current?.key === location.key ? rejectRef.current : null;
    // 组织根（会被重定向）和被页面判定为失效的地址不记；失效时改记去掉失效参数的地址
    if (rejected) {
      if (rejected.replacement) recordNav(orgId, rejected.replacement);
      return;
    }
    if (current === `/o/${orgId}` || current === `/o/${orgId}/`) return;
    recordNav(orgId, current);
  }, [orgId, location.pathname, location.search, location.key]);
  // 每个组织一条实时事件连接，按事件失效任务、收件箱、会话缓存
  useOrgEvents(orgId);
  // 正在查看的空间被删除时跳回收件箱
  useDeletedSpaceRedirect(orgId);
  // 桌面端：本机还没注册时自动注册到当前组织。
  useAutoRegisterLocal(orgId);

  useEffect(() => {
    writeLocal(localKeys.lastOrg, orgId);
    setWizard(onboardingPending(orgId));
  }, [orgId]);
  // 外壳底色按账号的外边框色覆盖（主题决定深浅色的取值）
  const prefs = useMyPreferences();
  const theme = useResolvedTheme(prefs.data?.theme ?? readCachedTheme());

  if (organizations.isLoading)
    return <div className="h-screen bg-surface-app" aria-busy="true" />;
  const org = organizations.data?.find((item) => item.id === orgId);
  if (organizations.data && !org)
    return (
      <Navigate
        to={
          organizations.data[0] ? `/o/${organizations.data[0].id}` : "/welcome"
        }
        replace
      />
    );
  if (!org) return null;

  const changeCollapsed = (next: boolean) => {
    writeLocal(localKeys.sidebarCollapsed, next ? "1" : null);
    setCollapsed(next);
  };
  const toggle = () => changeCollapsed(!collapsed);

  return (
    <NavRejectContext.Provider value={rejectRef}>
      <CreateTaskProvider orgId={orgId}>
        <div
          className="flex h-screen flex-col bg-surface-app"
          style={outlineStyle(prefs.data?.outline_color ?? 0, theme)}
        >
          <TitleBar org={org} organizations={organizations.data ?? []} />
          <div
            ref={sidebar.containerRef}
            className="flex min-h-0 flex-1 gap-2 px-2 pb-2"
          >
            <div className="relative flex shrink-0">
              <aside
                aria-label={t("shell.a11y.sidebar")}
                className="flex shrink-0 overflow-hidden rounded-xl bg-surface-card"
              >
                <Rail orgId={orgId} collapsed={collapsed} onToggle={toggle} />
                <div
                  id="org-sidebar-panel"
                  style={{ width: collapsed ? 0 : sidebar.width }}
                  aria-hidden={collapsed || sidebar.width === 0}
                  inert={collapsed || sidebar.width === 0}
                  className={cn(
                    "flex min-w-0 shrink-0 overflow-hidden",
                    !sidebar.resizing &&
                      "transition-[width] duration-150 ease-out motion-reduce:transition-none",
                  )}
                >
                  <div
                    style={{ width: Math.max(sidebar.min, sidebar.width) }}
                    className="flex min-w-0 shrink-0"
                  >
                    {inSettings ? (
                      <SettingsNavigation orgId={orgId} onCollapse={toggle} />
                    ) : inMembers ? (
                      <MembersNavigation orgId={orgId} onCollapse={toggle} />
                    ) : inChat ? (
                      <ChatPanel orgId={orgId} onCollapse={toggle} />
                    ) : (
                      // 按组织重新挂载，侧栏的本机折叠状态随组织切换
                      <Navigation
                        key={orgId}
                        orgId={orgId}
                        onCollapse={toggle}
                      />
                    )}
                  </div>
                </div>
              </aside>
              <PanelResizeHandle
                {...sidebar}
                label={t("shell.a11y.resizeSidebar")}
                controls="org-sidebar-panel"
                side="left"
                collapsed={collapsed}
                onCollapse={() => changeCollapsed(true)}
                onExpand={() => changeCollapsed(false)}
                className="absolute inset-y-0 -right-2 w-2"
              />
            </div>
            <main
              data-org-main
              className="min-w-0 flex-1 overflow-auto rounded-xl bg-surface-card"
            >
              <Outlet />
            </main>
          </div>
          {/* Agent / 小队设置抽屉（?agentSettings= / ?crewSettings=，任何页面可打开） */}
          <AgentSettingsHost orgId={orgId} />
          <CrewSettingsHost orgId={orgId} />
          {/* 新建空间向导（?createSpace=1，任何页面可打开） */}
          <CreateSpaceHost orgId={orgId} />
          {/* 新通知弹出（不渲染内容） */}
          <NotificationWatcher orgId={orgId} />
          {wizard && (
            <OnboardingWizard orgId={orgId} onFinish={() => setWizard(false)} />
          )}
        </div>
      </CreateTaskProvider>
    </NavRejectContext.Provider>
  );
}

function TitleBar({
  org,
  organizations,
}: {
  org: Organization;
  organizations: Organization[];
}) {
  const t = useT();
  const api = useApi();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const me = useQuery({ queryKey: keys.me, queryFn: api.me.get });
  const desktop = isDesktop();
  const macDesktop = isMacDesktop();
  const bridge = desktopBridge();
  const [proxyOpen, setProxyOpen] = useState(false);
  const [accountMenu, setAccountMenu] = useState(false);
  const [accounts, setAccounts] = useState<DesktopAccount[]>([]);
  const name = me.data?.full_name || me.data?.email;
  useEffect(() => {
    if (name) void bridge?.nameAccount?.(name).catch(() => {});
  }, [name, bridge]);
  const toast = useToast();
  const logout = async () => {
    try {
      await api.auth.logout();
    } catch {
      toast(t("connect.remote.requestFailed"), { tone: "error" });
      return;
    }
    await bridge?.nameAccount?.("").catch(() => {});
    queryClient.clear();
    clearNavMemory();
    navigate("/login", { replace: true });
  };
  return (
    <header
      className={cn(
        "flex h-10 shrink-0 items-center gap-2 px-3",
        // 桌面端：标题栏可拖动窗口，按钮区域除外；macOS 左侧给红黄绿按钮留位
        desktop &&
          "[-webkit-app-region:drag] [&_button]:[-webkit-app-region:no-drag]",
        macDesktop && "pl-[92px]",
      )}
    >
      <Menu
        label={t("shell.switchOrg")}
        trigger={({ ref, toggle, open }) => (
          <button
            ref={ref}
            type="button"
            aria-expanded={open}
            onClick={toggle}
            className="flex h-7 items-center gap-2 rounded-lg py-1 pr-2 pl-1 text-body font-medium hover:bg-surface-muted"
          >
            <OrgIcon org={org} size={20} />
            <span className="max-w-[160px] truncate">{org.name}</span>
            <Icon name="chevron-down" size={14} className="text-fg-muted" />
          </button>
        )}
      >
        <MenuLabel>{t("shell.switchOrg")}</MenuLabel>
        {organizations.map((item) => (
          <MenuItem
            key={item.id}
            icon={<OrgIcon org={item} size={20} />}
            selected={item.id === org.id}
            onSelect={() => navigate(`/o/${item.id}`)}
          >
            {item.name}
          </MenuItem>
        ))}
        <MenuSeparator />
        <MenuItem
          icon={<Icon name="plus" />}
          onSelect={() => navigate("/welcome?new=1")}
        >
          {t("shell.createOrg")}
        </MenuItem>
      </Menu>
      <div className="flex flex-1 items-center justify-center gap-2">
        {desktop && <DesktopNav />}
        <SearchLauncher orgId={org.id} />
      </div>
      <Menu
        label={me.data?.email ?? ""}
        placement="bottom-end"
        trigger={({ ref, toggle }) => (
          <button
            ref={ref}
            type="button"
            onClick={() => {
              setAccountMenu(false);
              toggle();
            }}
            className="rounded-full"
            aria-label={me.data?.email}
          >
            <Avatar
              name={me.data?.full_name || me.data?.email || "?"}
              src={me.data?.avatar_url}
              size={24}
            />
          </button>
        )}
      >
        {accountMenu ? (
          <>
            <button
              type="button"
              onClick={() => setAccountMenu(false)}
              className="flex w-full items-center gap-2 px-2 py-1.5"
            >
              <Icon name="chevron-left" />
              {t("shell.accounts")}
            </button>
            {accounts
              .filter((a) => a.name || a.current)
              .map((a) => (
                <MenuItem
                  key={a.id}
                  selected={a.current}
                  disabled={a.current}
                  onSelect={() => void bridge?.selectAccount?.(a.id)}
                >
                  {a.name || name}
                </MenuItem>
              ))}
            <MenuSeparator />
            <MenuItem
              icon={<Icon name="plus" />}
              onSelect={() => void bridge?.selectAccount?.(null)}
            >
              {t("shell.addAccount")}
            </MenuItem>
          </>
        ) : (
          <>
            {name && <MenuLabel>{name}</MenuLabel>}
            {bridge?.accounts && (
              <button
                type="button"
                onClick={async () => {
                  setAccounts(await bridge.accounts!());
                  setAccountMenu(true);
                }}
                className="flex w-full items-center justify-between rounded-lg px-2.5 py-1.5 text-body hover:bg-surface-active"
              >
                {t("shell.switchAccount")}
                <Icon name="chevron-right" />
              </button>
            )}
            <MenuSeparator />
            <MenuLabel>{t("shell.rail.settings")}</MenuLabel>
            {(
              ["profile", "preferences", "notifications", "tokens"] as const
            ).map((path, i) => (
              <MenuItem
                key={path}
                icon={
                  <Icon
                    name={
                      (["user", "settings-2", "bell", "key-round"] as const)[i]!
                    }
                  />
                }
                onSelect={() => navigate(`/o/${org.id}/settings/${path}`)}
              >
                {t(`settings.items.${path}`)}
              </MenuItem>
            ))}
            {desktop && (
              <MenuItem
                icon={<Icon name="monitor" />}
                onSelect={() => navigate(`/o/${org.id}/settings/daemon`)}
              >
                {t("shell.localService")}
              </MenuItem>
            )}
            {bridge?.getProxy && (
              <MenuItem
                icon={<Icon name="globe" />}
                onSelect={() => setProxyOpen(true)}
              >
                {t("shell.proxyTitle")}
              </MenuItem>
            )}
            <MenuSeparator />
            <MenuItem
              icon={<Icon name="log-out" />}
              onSelect={() => void logout()}
            >
              {t("common.logout")}
            </MenuItem>
          </>
        )}
      </Menu>
      {proxyOpen && <ProxyDialog onClose={() => setProxyOpen(false)} />}
    </header>
  );
}

function Rail({
  orgId,
  collapsed,
  onToggle,
}: {
  orgId: string;
  collapsed: boolean;
  onToggle: () => void;
}) {
  const t = useT();
  const location = useLocation();
  const current = location.pathname + location.search;
  const active = moduleOf(orgId, location.pathname, location.search);
  // 布局模式只改顺序、名称和图标，module 不变，主导航记忆照旧
  const railItems = railItemsFor(useLayoutMode());
  return (
    <nav
      aria-label={t("shell.a11y.rail")}
      className="flex w-[55px] shrink-0 flex-col items-center gap-1 border-r border-line-default py-3"
    >
      {collapsed ? (
        <IconButton
          label={t("shell.expand")}
          icon={<Icon name="chevrons-left" size={18} className="rotate-180" />}
          size="36"
          onClick={onToggle}
          className="mb-1"
        />
      ) : (
        <Icon
          name="brand-logo"
          size={24}
          className="mb-3 h-6 w-6"
          label="OpenFox"
        />
      )}
      {railItems.map((item) => (
        <Link
          key={item.label}
          to={navTarget(
            orgId,
            item.module,
            current,
            `/o/${orgId}/${item.fallback}`,
          )}
          aria-label={t(item.label)}
          aria-current={active === item.module ? "page" : undefined}
          title={t(item.label)}
          className={cn(
            "flex size-9 items-center justify-center rounded-lg text-fg-secondary hover:bg-surface-muted",
            active === item.module && "bg-surface-active text-fg-default",
          )}
        >
          <Icon name={item.icon} size={19} />
        </Link>
      ))}
      <div className="mt-auto flex flex-col items-center gap-1">
        <LayoutModeButton />
        <ComputersRailButton orgId={orgId} />
      </div>
    </nav>
  );
}

function NavRow({
  to,
  icon,
  label,
  trailing,
  end,
}: {
  to: string;
  icon: IconName;
  label: string;
  trailing?: React.ReactNode;
  end?: boolean;
}) {
  return (
    <NavLink
      to={to}
      end={end}
      className={({ isActive }) =>
        cn(
          "flex h-[var(--density-nav-row,30px)] items-center gap-2 rounded-lg pr-1.5 pl-2 text-body hover:bg-surface-hover",
          isActive ? "bg-surface-active font-medium" : "text-fg-default",
        )
      }
    >
      <Icon name={icon} className="text-fg-secondary" />
      <span className="min-w-0 flex-1 truncate">{label}</span>
      {trailing}
    </NavLink>
  );
}

/** 侧栏里折叠的空间 id；记录缺失或损坏时全部展开。 */
function readCollapsedSpaces(orgId: string): string[] {
  try {
    const parsed: unknown = JSON.parse(
      readLocal(localKeys.spacesCollapsed(orgId)) ?? "[]",
    );
    return Array.isArray(parsed)
      ? parsed.filter((id): id is string => typeof id === "string")
      : [];
  } catch {
    return [];
  }
}

function Navigation({
  orgId,
  onCollapse,
}: {
  orgId: string;
  onCollapse: () => void;
}) {
  const t = useT();
  const conversationId = useCurrentConversationId();
  const api = useApi();
  const base = `/o/${orgId}`;
  // 与收件箱页共用同一个查询键，操作后一起失效
  const inbox = useInboxStats(orgId);
  const spaces = useQuery({
    queryKey: keys.spaces(orgId),
    queryFn: () => api.spaces.list(orgId),
  });
  // 与对话页签共用同一份列表缓存（最近优先，取前 5 条）
  const conversations = useConversations(orgId);
  const [spacesOpen, setSpacesOpen] = useState(true);
  const [collapsedSpaces, setCollapsedSpaces] = useState<string[]>(() =>
    readCollapsedSpaces(orgId),
  );
  const toggleSpace = (spaceId: string) => {
    const next = collapsedSpaces.includes(spaceId)
      ? collapsedSpaces.filter((id) => id !== spaceId)
      : [...collapsedSpaces, spaceId];
    setCollapsedSpaces(next);
    writeLocal(localKeys.spacesCollapsed(orgId), JSON.stringify(next));
  };
  const unread = inbox.data?.inbox_unread ?? 0;
  const spaceRef = useRef<HTMLDivElement | null>(null);

  return (
    <nav
      aria-label={t("shell.a11y.navigation")}
      className="group/panel flex min-w-0 flex-1 flex-col overflow-y-auto px-1.5 pt-1.5 pb-3"
    >
      <div className="flex h-11 shrink-0 items-center gap-1 pr-1 pl-2.5">
        <h2 className="min-w-0 flex-1 truncate text-title font-bold">
          {t("shell.home")}
        </h2>
        <SidebarCollapseButton onClick={onCollapse} />
        <NewTaskButton orgId={orgId} />
      </div>
      <div className="flex flex-col gap-0.5">
        <NavRow
          to={`${base}/inbox`}
          icon="inbox"
          label={t("shell.inbox")}
          trailing={
            unread > 0 ? (
              <Badge
                tone={inbox.data?.inbox_important_unread ? "danger" : "neutral"}
              >
                {unread}
              </Badge>
            ) : undefined
          }
        />
        <NavRow
          to={`${base}/my-tasks`}
          icon="user-check"
          label={t("shell.myTasks")}
        />
        <NavRow
          to={`${base}/assistant`}
          icon="bot-face"
          label={t("shell.assistant")}
        />
        <NavRow
          to={`${base}/agents`}
          icon="briefcase"
          label={t("shell.allAgents")}
          trailing={
            <span className="text-caption text-fg-muted">
              {t("shell.allAgentsHint")}
            </span>
          }
        />
        <NavRow
          to={`${base}/market`}
          icon="blocks"
          label={t("shell.market")}
          trailing={
            <span className="truncate text-caption text-fg-muted">
              {t("shell.marketHint")}
            </span>
          }
        />
      </div>

      {spaces.data?.some((space) => space.triage_favorited) && (
        <>
          <SectionLabel label={t("chat.list.favorites")} />
          {spaces.data
            .filter((space) => space.triage_favorited)
            .map((space) => (
              <NavRow
                key={space.id}
                to={`${base}/spaces/${space.id}/triage`}
                icon="star"
                label={`${t("spaces.triage.title")} · ${space.name}`}
              />
            ))}
        </>
      )}
      <SectionLabel label={t("shell.chat")} />
      <DirectChatList orgId={orgId} compact rail="home" />
      {conversations.isLoading ? (
        <RowsSkeleton rows={2} />
      ) : conversations.list.length === 0 ? (
        <p className="px-2.5 py-1 text-body-sm text-fg-muted">
          {conversations.isError
            ? t("chat.list.loadFailed")
            : t("chat.list.noAgentConversations")}
        </p>
      ) : (
        conversations.list
          .slice(0, 5)
          .map((item) => (
            <ConversationItem
              key={item.id}
              to={conversationDestination(orgId, item, "home")}
              conversation={item}
              selected={conversationId === item.id}
            />
          ))
      )}

      <div ref={spaceRef} className="group/spaces flex items-center pr-1">
        <SectionLabel
          label={t("shell.mySpaces")}
          open={spacesOpen}
          onToggle={() => setSpacesOpen((v) => !v)}
        />
        {/* 「+」平时隐藏，悬停分组行时出现 */}
        <span className="mt-3 ml-auto opacity-0 group-hover/spaces:opacity-100 focus-within:opacity-100">
          <JoinOrCreateSpace orgId={orgId} variant="icon" />
        </span>
      </div>
      {spacesOpen &&
        // 只列已加入的空间，按个人顺序（服务端已排序）
        joinedSpaces(spaces.data).map((space) => (
          <div key={space.id} className="flex flex-col gap-0.5">
            {/* 点空间名折叠 / 展开它的子项，状态按组织存在本机 */}
            <button
              type="button"
              aria-expanded={!collapsedSpaces.includes(space.id)}
              onClick={() => toggleSpace(space.id)}
              className="group/space flex h-[30px] items-center gap-2 rounded-lg pr-1.5 pl-2 text-left text-body font-medium hover:bg-surface-hover"
            >
              <SpaceIcon icon={space.icon} />
              <span className="truncate">{space.name}</span>
              {space.visibility === "private" && (
                <Icon
                  name="lock"
                  size={12}
                  className="text-fg-muted"
                  label={t("spaces.access.private")}
                />
              )}
              <Icon
                name="chevron-down"
                size={12}
                className={cn(
                  "shrink-0 text-fg-muted transition-transform",
                  collapsedSpaces.includes(space.id)
                    ? "-rotate-90"
                    : "opacity-0 group-hover/space:opacity-100 group-focus-visible/space:opacity-100",
                )}
              />
            </button>
            {!collapsedSpaces.includes(space.id) && (
              <div className="ml-[15px] flex flex-col gap-0.5 border-l border-line-default pl-2">
                <NavRow
                  to={`${base}/spaces/${space.id}/tasks`}
                  icon="square-check"
                  label={t("shell.space.tasks")}
                />
                {/* 任务的下一级：迭代 / 分拣台 / 视图 / 专项 */}
                <SpaceTaskNav
                  orgId={orgId}
                  spaceId={space.id}
                  features={space.features}
                />
                <NavRow
                  to={`${base}/spaces/${space.id}/agents`}
                  icon="bot"
                  label={t("shell.space.agents")}
                />
                {/* 侧栏入口：Agents / Agent 自动化 / 文档库 / 技能 */}
                <NavRow
                  to={`${base}/spaces/${space.id}/automations`}
                  icon="zap"
                  label={t("automations.navLabel")}
                  trailing={
                    <span className="text-caption text-fg-muted">
                      {t("automations.navSubtitle")}
                    </span>
                  }
                />
                <NavRow
                  to={`${base}/spaces/${space.id}/docs`}
                  icon="book-open"
                  label={t("shell.space.docs")}
                />
                <NavRow
                  to={`${base}/spaces/${space.id}/skills`}
                  icon="sparkles"
                  label={t("shell.space.skills")}
                />
                {/* 空间「更多」：空间设置入口 */}
                <SpaceMoreMenu orgId={orgId} spaceId={space.id} />
              </div>
            )}
          </div>
        ))}

      <SectionLabel label={t("shell.organization")} />
      <NavRow
        to={`${base}/settings/integrations`}
        icon="puzzle"
        label={t("shell.integrations")}
      />
      <NavRow
        to={`${base}/settings`}
        icon="settings"
        label={t("shell.moreSettings")}
        end
      />

      <div className="mt-auto flex flex-col gap-1 border-t border-line-default pt-2">
        {/* 进阶指引入口可在开始使用页隐藏（只影响本机），也可在侧栏就地展开 */}
        <AdvancedGuide
          orgId={orgId}
          firstSpaceId={joinedSpaces(spaces.data)[0]?.id}
        />
        <span className="flex h-9 items-center gap-2 px-2.5 text-body-sm text-fg-muted">
          <Icon name="book-open" />
          {t("shell.community")}
        </span>
      </div>
    </nav>
  );
}

function SectionLabel({
  label,
  open,
  onToggle,
}: {
  label: string;
  open?: boolean;
  onToggle?: () => void;
}) {
  const content = (
    <>
      <span>{label}</span>
      {onToggle && (
        <Icon
          name="chevron-down"
          size={12}
          className={cn("transition-transform", !open && "-rotate-90")}
        />
      )}
    </>
  );
  return onToggle ? (
    <button
      type="button"
      aria-expanded={open}
      onClick={onToggle}
      className="mt-3 flex h-7 items-center gap-1 px-2 text-body-sm text-fg-muted hover:text-fg-default"
    >
      {content}
    </button>
  ) : (
    <div className="mt-3 flex h-7 items-center gap-1 px-2 text-body-sm text-fg-muted">
      {content}
    </div>
  );
}
