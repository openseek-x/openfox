import { LocalComputerCard } from "./computers/LocalComputerCard";
import { DirectChatPage } from "./conversations/DirectChat";
import {
  QueryClient,
  QueryClientProvider,
  useQuery,
} from "@tanstack/react-query";
import { useLayoutEffect, useState, type ReactNode } from "react";
import { Navigate, Route, Routes, useLocation } from "react-router-dom";

import { isApiError, type Api } from "@openfox/api";
import { I18nProvider, type Locale } from "@openfox/i18n";
import { ToastProvider } from "@openfox/ui";

import {
  AgentsSettingsPage,
  CrewsSettingsPage,
} from "./agents/AgentSettingsPages";
import { AgentsPage } from "./agents/AgentsPage";
import { AutomationDetailPage } from "./automations/AutomationDetailPage";
import { AutomationsPage } from "./automations/AutomationsPage";
import { DocsPage } from "./docs/DocsPage";
import { SkillsPage } from "./skills/SkillsPage";
import { MarketPage } from "./market/MarketPage";
import { MembersPage } from "./agents/MembersPage";
import { SpaceAgentsPage } from "./agents/SpaceAgentsPage";
import { LoginPage } from "./auth/LoginPage";
import { InboxPage } from "./inbox/InboxPage";
import { MyTasksPage } from "./my-tasks/MyTasksPage";
import { ComputersPage } from "./computers/ComputersPage";
import { AssistantPage } from "./conversations/AssistantPage";
import { ChatPage } from "./conversations/ChatPage";
import { ApiProvider, keys, useApi } from "./context";
import { InviteLinkPage } from "./onboarding/InviteLinkPage";
import { BetaFeaturesPage } from "./me/BetaFeaturesPage";
import { GetStartedPage } from "./me/GetStartedPage";
import { NotificationsPage } from "./me/NotificationsPage";
import { PreferencesPage } from "./me/PreferencesPage";
import { PreferencesSync } from "./me/PreferencesSync";
import { ProfilePage } from "./me/ProfilePage";
import { ShortcutsPage } from "./me/ShortcutsPage";
import { AccountBindingPage } from "./integrations/AccountBindingPage";
import { TokensPage } from "./me/TokensPage";
import { applyCachedTheme } from "./me/theme";
import { ImportExportPage } from "./imports/ImportExportPage";
import { IntegrationDetailPage } from "./integrations/IntegrationDetailPage";
import { IntegrationsPage } from "./integrations/IntegrationsPage";
import { OAuthCompletePage } from "./integrations/OAuthCompletePage";
import { MembersSettingsPage } from "./organization/MembersSettingsPage";
import { OrganizationSettingsPage } from "./organization/OrganizationSettingsPage";
import { JoinPage } from "./onboarding/JoinPage";
import { WelcomePage } from "./onboarding/WelcomePage";
import { NotFoundPage, UnavailablePage } from "./pages/StatePages";
import { CustomFieldsPage } from "./settings/custom-fields/CustomFieldsPage";
import { LabelsPage } from "./settings/labels/LabelsPage";
import { RelationTypesPage } from "./settings/relation-types/RelationTypesPage";
import { SettingsLayout } from "./settings/SettingsLayout";
import { GitSettingsPage } from "./settings/GitSettingsPage";
import { SpaceSettingsPage } from "./spaces/settings/SpaceSettingsPage";
import { SpacesListPage } from "./spaces/settings/SpacesListPage";
import { StatusesPage } from "./settings/statuses/StatusesPage";
import { OrgLayout } from "./shell/OrgLayout";
import { localKeys, readLocal } from "./storage";
import { SpaceTasksPage } from "./tasks/SpaceTasksPage";
import { TaskPage } from "./tasks/TaskPage";
import { IterationDetailPage } from "./iterations/IterationDetailPage";
import { IterationsPage } from "./iterations/IterationsPage";
import { TriagePage } from "./triage/TriagePage";
import { TrashPage } from "./tasks/trash/TrashPage";
import { ProjectDetailPage } from "./projects/ProjectDetailPage";
import { ProjectsPage } from "./projects/ProjectsPage";
import { ViewEditorPage } from "./views/ViewEditorPage";
import { ViewPage } from "./views/ViewPage";
import { ViewsPage } from "./views/ViewsPage";

export function createQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        staleTime: 30_000,
        refetchOnWindowFocus: false,
        // 401/403/404 重试没有意义，网络类错误最多重试两次
        retry: (count, error) =>
          !(isApiError(error) && [401, 403, 404].includes(error.status)) &&
          count < 2,
      },
    },
  });
}

/** 需要登录的路由：未登录时带上原地址跳到登录页。 */
function RequireAuth({ children }: { children: ReactNode }) {
  const api = useApi();
  const location = useLocation();
  const me = useQuery({ queryKey: keys.me, queryFn: api.me.get, retry: false });
  if (me.isLoading)
    return <div className="h-screen bg-surface-app" aria-busy="true" />;
  if (me.error && isApiError(me.error) && me.error.status === 401) {
    const next = location.pathname + location.search;
    return (
      <Navigate
        to={`/login${next === "/" ? "" : `?next=${encodeURIComponent(next)}`}`}
        replace
      />
    );
  }
  // 已登录后同步账号的主题与语言
  return (
    <>
      {children}
      <PreferencesSync />
    </>
  );
}

/** 根路径：进入上次使用的组织，没有组织时进入首登。 */
function Home() {
  const api = useApi();
  const organizations = useQuery({
    queryKey: keys.organizations,
    queryFn: api.me.organizations,
  });
  if (organizations.isLoading)
    return <div className="h-screen bg-surface-app" aria-busy="true" />;
  const list = organizations.data ?? [];
  if (list.length === 0) return <Navigate to="/welcome" replace />;
  const last = readLocal(localKeys.lastOrg);
  const target = list.find((item) => item.id === last) ?? list[0]!;
  return <Navigate to={`/o/${target.id}`} replace />;
}

export function AppRoutes() {
  const auth = (node: ReactNode) => <RequireAuth>{node}</RequireAuth>;
  return (
    <Routes>
      <Route path="/login" element={<LoginPage />} />
      <Route path="/welcome" element={auth(<WelcomePage />)} />
      <Route path="/join" element={auth(<JoinPage />)} />
      <Route path="/invite/:token" element={auth(<InviteLinkPage />)} />
      <Route
        path="/integrations/oauth/complete"
        element={auth(<OAuthCompletePage />)}
      />
      <Route path="/" element={auth(<Home />)} />
      <Route path="/o/:orgId" element={auth(<OrgLayout />)}>
        <Route index element={<Navigate to="inbox" replace />} />
        <Route path="inbox" element={<InboxPage />} />
        <Route path="my-tasks" element={<MyTasksPage />} />
        {/* 任务全页（?view=chat&conversationId= 打开 AI 面板） */}
        <Route path="tasks/:displayId" element={<TaskPage />} />
        <Route path="spaces/:spaceId/tasks" element={<SpaceTasksPage />} />
        <Route path="spaces/:spaceId/triage" element={<TriagePage />} />
        {/* 任务回收站「最近删除」 */}
        <Route path="spaces/:spaceId/trash" element={<TrashPage />} />
        <Route path="spaces/:spaceId/iterations" element={<IterationsPage />} />
        <Route
          path="spaces/:spaceId/iterations/:iterationId"
          element={<IterationDetailPage />}
        />
        {/* 专项与视图 */}
        <Route path="spaces/:spaceId/projects" element={<ProjectsPage />} />
        <Route
          path="spaces/:spaceId/projects/:projectId"
          element={<ProjectDetailPage />}
        />
        <Route path="spaces/:spaceId/views" element={<ViewsPage />} />
        <Route path="spaces/:spaceId/views/new" element={<ViewEditorPage />} />
        <Route
          path="spaces/:spaceId/views/:viewId/edit"
          element={<ViewEditorPage />}
        />
        <Route path="spaces/:spaceId/views/:viewId" element={<ViewPage />} />
        {/* 对话页签、对话页、小助手 */}
        <Route path="chat" element={<ChatPage />} />
        <Route path="chat/direct/:directChatId" element={<DirectChatPage />} />
        <Route path="chat/:conversationId" element={<ChatPage />} />
        <Route path="assistant" element={<AssistantPage />} />
        {/* Agent 自动化（需放在空间兜底路由之前） */}
        <Route
          path="spaces/:spaceId/automations"
          element={<AutomationsPage />}
        />
        <Route
          path="spaces/:spaceId/automations/:automationId"
          element={<AutomationDetailPage />}
        />
        {/* 文档库（需放在空间兜底路由之前） */}
        <Route path="spaces/:spaceId/docs" element={<DocsPage />} />
        <Route path="spaces/:spaceId/docs/:nodeId" element={<DocsPage />} />
        {/* 技能（详情用 ?skill=<id> 的 Dialog；需放在空间兜底路由之前） */}
        <Route path="spaces/:spaceId/skills" element={<SkillsPage />} />
        {/* 空间 Agents 页（复用全局目录，只列本空间；需放在空间兜底路由之前） */}
        <Route path="spaces/:spaceId/agents" element={<SpaceAgentsPage />} />
        {/* 市场（详情用 ?skill= / ?template= 的 Dialog） */}
        <Route path="market" element={<MarketPage />} />
        {["spaces/:spaceId/*"].map((path) => (
          <Route key={path} path={path} element={<UnavailablePage />} />
        ))}
        {/* Agent 与小队、成员页签 */}
        <Route path="agents" element={<AgentsPage />} />
        <Route path="members" element={<MembersPage />} />
        {/* 旧的侧栏入口地址：电脑管理在设置页 */}
        <Route
          path="computers"
          element={<Navigate to="../settings/computers" replace />}
        />
        <Route path="settings" element={<SettingsLayout />}>
          <Route index element={<Navigate to="labels" replace />} />
          {/* 组织设置与成员 */}
          <Route path="organization" element={<OrganizationSettingsPage />} />
          <Route path="git" element={<GitSettingsPage />} />
          <Route path="members" element={<MembersSettingsPage />} />
          {/* 外部应用集成总览与详情 */}
          <Route path="integrations" element={<IntegrationsPage />} />
          <Route
            path="integrations/:provider"
            element={<IntegrationDetailPage />}
          />
          {/* 导入与导出（向导在页内展开） */}
          <Route path="import-export" element={<ImportExportPage />} />
          {/* 个人资料、偏好设置与快捷键 */}
          <Route
            path="daemon"
            element={
              <div className="mx-auto max-w-3xl p-6">
                <LocalComputerCard />
              </div>
            }
          />
          <Route path="profile" element={<ProfilePage />} />
          <Route path="preferences" element={<PreferencesPage />} />
          <Route path="shortcuts" element={<ShortcutsPage />} />
          {/* 开始使用、通知、个人访问令牌与内测功能 */}
          <Route path="get-started" element={<GetStartedPage />} />
          <Route path="notifications" element={<NotificationsPage />} />
          <Route path="tokens" element={<TokensPage />} />
          <Route path="account-binding" element={<AccountBindingPage />} />
          <Route path="beta-features" element={<BetaFeaturesPage />} />
          <Route path="labels" element={<LabelsPage />} />
          <Route path="statuses" element={<StatusesPage />} />
          <Route path="relation-types" element={<RelationTypesPage />} />
          <Route path="custom-fields" element={<CustomFieldsPage />} />
          <Route path="computers" element={<ComputersPage />} />
          <Route path="agents" element={<AgentsSettingsPage />} />
          <Route path="crews" element={<CrewsSettingsPage />} />
          {/* 空间设置 */}
          <Route path="spaces" element={<SpacesListPage />} />
          <Route
            path="spaces/:spaceId"
            element={<Navigate to="general" replace />}
          />
          <Route
            path="spaces/:spaceId/:section"
            element={<SpaceSettingsPage />}
          />
          <Route
            path="spaces/:spaceId/task-types/:typeId"
            element={<SpaceSettingsPage />}
          />
          <Route path="*" element={<UnavailablePage />} />
        </Route>
        <Route path="*" element={<NotFoundPage />} />
      </Route>
      <Route path="*" element={<NotFoundPage />} />
    </Routes>
  );
}

/** 应用根组件。测试可注入 api / queryClient / 语言。 */
export function App({
  api,
  queryClient,
  locale,
}: {
  api?: Api;
  queryClient?: QueryClient;
  locale?: Locale;
}) {
  const [client] = useState(() => queryClient ?? createQueryClient());
  // 首屏前按本机缓存的主题上色，避免偏好加载前闪烁
  useLayoutEffect(() => applyCachedTheme(), []);
  return (
    <I18nProvider initialLocale={locale}>
      <QueryClientProvider client={client}>
        <ApiProvider api={api}>
          <ToastProvider>
            <AppRoutes />
          </ToastProvider>
        </ApiProvider>
      </QueryClientProvider>
    </I18nProvider>
  );
}
