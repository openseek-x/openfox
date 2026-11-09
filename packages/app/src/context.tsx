import { createContext, useContext, type ReactNode } from "react";

import { createApi, type Api } from "@openfox/api";

const ApiContext = createContext<Api | null>(null);

export function ApiProvider({
  api,
  children,
}: {
  api?: Api;
  children: ReactNode;
}) {
  return (
    <ApiContext.Provider value={api ?? defaultApi()}>
      {children}
    </ApiContext.Provider>
  );
}

let cached: Api | undefined;
function defaultApi(): Api {
  cached ??= createApi();
  return cached;
}

export function useApi(): Api {
  const api = useContext(ApiContext);
  if (!api) throw new Error("useApi 必须在 ApiProvider 内使用");
  return api;
}

/** 查询键按「资源 / 组织 / 参数」分层，便于按组织整体失效。 */
export const keys = {
  me: ["me"] as const,
  // 账号偏好（挂在 me 前缀下：失效当前用户时一并刷新）
  preferences: ["me", "preferences"] as const,
  // 个人访问令牌、通知渠道、内测功能（都挂在 me 前缀下）
  tokens: ["me", "tokens"] as const,
  notificationChannels: ["me", "notification-channels"] as const,
  betaFeatures: ["me", "beta-features"] as const,
  organizations: ["organizations"] as const,
  spaces: (orgId: string) => ["org", orgId, "spaces"] as const,
  devices: (orgId: string) => ["org", orgId, "devices"] as const,
  conversationOffers: (orgId: string) =>
    ["org", orgId, "conversation-offers"] as const,
  inboxStats: (orgId: string) => ["org", orgId, "inbox-stats"] as const,
  // 开始使用进度（设置导航计数与页面共用）
  getStarted: (orgId: string) => ["org", orgId, "get-started"] as const,
  conversations: (orgId: string) => ["org", orgId, "conversations"] as const,
  // 全局配置
  labelGroups: (orgId: string) => ["org", orgId, "label-groups"] as const,
  labels: (orgId: string, scope: string) =>
    ["org", orgId, "labels", scope] as const,
  taskStatuses: (orgId: string) => ["org", orgId, "task-statuses"] as const,
  relationTypes: (orgId: string) => ["org", orgId, "relation-types"] as const,
  customFields: (orgId: string) => ["org", orgId, "custom-fields"] as const,
  orgTaskTypes: (orgId: string) => ["org", orgId, "task-types"] as const,
  // 任务、我的任务、收件箱
  /** 所有任务查询（列表、计数、详情、子资源）的公共前缀 */
  tasks: (orgId: string) => ["org", orgId, "tasks"] as const,
  taskLists: (orgId: string) => ["org", orgId, "tasks", "list"] as const,
  /** scope：space:<id> 或 my；column：看板列 / 列表分组的状态 id，平铺列表为 null */
  taskList: (
    orgId: string,
    scope: string,
    params: Record<string, unknown>,
    column: string | null,
  ) => ["org", orgId, "tasks", "list", scope, params, column] as const,
  taskCounts: (orgId: string, scope: string, params: Record<string, unknown>) =>
    ["org", orgId, "tasks", "counts", scope, params] as const,
  /** 统计视图聚合；tz 是调用方时区 */
  taskStats: (
    orgId: string,
    scope: string,
    params: Record<string, unknown>,
    tz: string,
  ) => ["org", orgId, "tasks", "stats", scope, params, tz] as const,
  taskDetails: (orgId: string) => ["org", orgId, "tasks", "detail"] as const,
  taskDetail: (orgId: string, displayId: string) =>
    ["org", orgId, "tasks", "detail", displayId.toUpperCase()] as const,
  taskSub: (orgId: string, taskId: string, kind: string) =>
    ["org", orgId, "tasks", "sub", taskId, kind] as const,
  spaceTaskTypes: (orgId: string, spaceId: string) =>
    ["org", orgId, "space-task-types", spaceId] as const,
  spaceMembers: (orgId: string, spaceId: string) =>
    ["org", orgId, "space-members", spaceId] as const,
  spaceAgents: (orgId: string, spaceId: string) =>
    ["org", orgId, "space-agents", spaceId] as const,
  members: (orgId: string) => ["org", orgId, "members"] as const,
  // 组织设置与成员（成员详情挂在 members 前缀下，一起失效）
  memberDetails: (orgId: string) =>
    ["org", orgId, "members", "details"] as const,
  organizationSettings: (orgId: string) =>
    ["org", orgId, "organization-settings"] as const,
  invitations: (orgId: string) => ["org", orgId, "invitations"] as const,
  inviteLink: (orgId: string) => ["org", orgId, "invite-link"] as const,
  agentDirectory: (orgId: string) => ["org", orgId, "agent-directory"] as const,
  inbox: (orgId: string) => ["org", orgId, "inbox"] as const,
  inboxList: (orgId: string, params: Record<string, unknown>) =>
    ["org", orgId, "inbox", params] as const,
  // 电脑（列表沿用 devices；详情与子资源挂在其下，失效列表时一并刷新）
  device: (orgId: string, deviceId: string) =>
    ["org", orgId, "devices", deviceId] as const,
  deviceRemovalBlockers: (orgId: string, deviceId: string) =>
    ["org", orgId, "devices", deviceId, "removal-blockers"] as const,
  // Agent 与小队（详情按 ID 缓存，不含空间：换空间后缓存仍然有效）
  agentDirectoryArchived: (orgId: string) =>
    ["org", orgId, "agent-directory", "archived"] as const,
  agents: (orgId: string) => ["org", orgId, "agents"] as const,
  agent: (orgId: string, agentId: string) =>
    ["org", orgId, "agents", agentId] as const,
  /** kind：stats / env-vars / repos / runs */
  agentSub: (orgId: string, agentId: string, kind: string) =>
    ["org", orgId, "agents", agentId, kind] as const,
  // 快捷回复
  quickReplies: (orgId: string) => ["org", orgId, "quick-replies"] as const,
  // 任务快捷指令（按空间）
  quickCommands: (orgId: string, spaceId: string) =>
    ["org", orgId, "quick-commands", spaceId] as const,
  crews: (orgId: string) => ["org", orgId, "crews"] as const,
  crew: (orgId: string, crewId: string) =>
    ["org", orgId, "crews", crewId] as const,
  contacts: (orgId: string) => ["org", orgId, "contacts"] as const,
  // Run 执行过程（详情与子资源挂在同一前缀下）
  runs: (orgId: string) => ["org", orgId, "runs"] as const,
  run: (orgId: string, runId: string) => ["org", orgId, "runs", runId] as const,
  /** kind：events / result / artifacts */
  runSub: (
    orgId: string,
    runId: string,
    kind: "events" | "result" | "artifacts",
  ) => ["org", orgId, "runs", runId, kind] as const,
  // 对话（都挂在 conversations 前缀下：整体失效会话时一并刷新）
  conversationLists: (orgId: string) =>
    ["org", orgId, "conversations", "list"] as const,
  conversationList: (orgId: string, params: Record<string, unknown>) =>
    ["org", orgId, "conversations", "list", params] as const,
  conversation: (orgId: string, conversationId: string) =>
    ["org", orgId, "conversations", "detail", conversationId] as const,
  conversationMessages: (orgId: string, conversationId: string) =>
    ["org", orgId, "conversations", "messages", conversationId] as const,
  /** 会话产物（挂在会话详情下：会话详情失效时一并刷新，例如 Run 结束） */
  conversationArtifacts: (orgId: string, conversationId: string) =>
    [
      "org",
      orgId,
      "conversations",
      "detail",
      conversationId,
      "artifacts",
    ] as const,
  // 小助手
  assistant: (orgId: string) => ["org", orgId, "assistant"] as const,
  assistantSessions: (orgId: string) =>
    ["org", orgId, "assistant", "sessions"] as const,
  // 电脑远程命令（挂在电脑详情下；命令只在发起它的组件里轮询）
  deviceCommand: (orgId: string, deviceId: string, commandId: string) =>
    ["org", orgId, "devices", deviceId, "commands", commandId] as const,
  // 空间设置（都挂在 spaces 前缀下：失效空间列表时一并刷新详情与子资源）
  spacesByStatus: (orgId: string, status: "retired" | "all") =>
    ["org", orgId, "spaces", "status", status] as const,
  space: (orgId: string, spaceId: string) =>
    ["org", orgId, "spaces", "detail", spaceId] as const,
  spaceRepos: (orgId: string, spaceId: string) =>
    ["org", orgId, "spaces", "repos", spaceId] as const,
  spaceImpact: (orgId: string, spaceId: string) =>
    ["org", orgId, "spaces", "impact", spaceId] as const,
  estimationPreview: (orgId: string, spaceId: string, scale: string) =>
    ["org", orgId, "spaces", "estimation-preview", spaceId, scale] as const,
  /** 空间成员详情（设置页用；挂在 spaceMembers 前缀下，与指派人员列表一起失效） */
  spaceMemberDetails: (orgId: string, spaceId: string) =>
    ["org", orgId, "space-members", spaceId, "detail"] as const,
  taskTypeTemplates: (orgId: string) =>
    ["org", orgId, "task-type-templates"] as const,
  /** 空间标签分组（挂在 label-groups 前缀下，失效组织分组时一并刷新） */
  spaceLabelGroups: (orgId: string, spaceId: string) =>
    ["org", orgId, "label-groups", "space", spaceId] as const,
  /** 组织成员及组织角色（空间成员页的添加弹层用；挂在 members 前缀下） */
  orgMemberRoles: (orgId: string) =>
    ["org", orgId, "members", "roles"] as const,
  // 迭代设置、列表与详情
  iterationSettings: (orgId: string, spaceId: string) =>
    ["org", orgId, "spaces", spaceId, "iteration-settings"] as const,
  iterations: (
    orgId: string,
    spaceId: string,
    params: Record<string, unknown> = {},
  ) => ["org", orgId, "spaces", spaceId, "iterations", params] as const,
  iteration: (orgId: string, spaceId: string, iterationId: string) =>
    ["org", orgId, "spaces", spaceId, "iterations", iterationId] as const,
  // 专项与视图（失效专项 / 视图事件时按前缀整体刷新）
  projects: (orgId: string) => ["org", orgId, "projects"] as const,
  projectList: (orgId: string, spaceId: string, status?: string) =>
    ["org", orgId, "projects", "list", spaceId, status ?? ""] as const,
  project: (orgId: string, projectId: string) =>
    ["org", orgId, "projects", "detail", projectId] as const,
  views: (orgId: string) => ["org", orgId, "views"] as const,
  viewList: (orgId: string, spaceId: string) =>
    ["org", orgId, "views", "list", spaceId] as const,
  view: (orgId: string, viewId: string) =>
    ["org", orgId, "views", "detail", viewId] as const,
  // 分拣台（队列、设置与规则统一挂在 triage 前缀下）
  triage: (orgId: string, spaceId: string) =>
    ["org", orgId, "spaces", spaceId, "triage"] as const,
  triageList: (
    orgId: string,
    spaceId: string,
    params: Record<string, unknown>,
  ) => ["org", orgId, "spaces", spaceId, "triage", "list", params] as const,
  triageCount: (orgId: string, spaceId: string) =>
    ["org", orgId, "spaces", spaceId, "triage", "count"] as const,
  triageSettings: (orgId: string, spaceId: string) =>
    ["org", orgId, "spaces", spaceId, "triage", "settings"] as const,
  triageRules: (orgId: string, spaceId: string) =>
    ["org", orgId, "spaces", spaceId, "triage", "rules"] as const,
  // Agent 自动化（按空间分组；详情与运行、投递都挂在自动化前缀下）
  automations: (orgId: string, spaceId: string) =>
    ["org", orgId, "automations", spaceId] as const,
  automation: (orgId: string, spaceId: string, automationId: string) =>
    ["org", orgId, "automations", spaceId, automationId] as const,
  automationRuns: (orgId: string, spaceId: string, automationId: string) =>
    ["org", orgId, "automations", spaceId, automationId, "runs"] as const,
  automationDeliveries: (
    orgId: string,
    spaceId: string,
    automationId: string,
  ) =>
    ["org", orgId, "automations", spaceId, automationId, "deliveries"] as const,
  webhookSecret: (orgId: string, automationId: string, triggerId: string) =>
    ["org", orgId, "automations", "secret", automationId, triggerId] as const,
  // 文档库（同一空间的文档集、节点、详情与搜索都挂在 docs 前缀下，文档变化按空间整体失效）
  docs: (orgId: string, spaceId: string) =>
    ["org", orgId, "docs", spaceId] as const,
  docSets: (orgId: string, spaceId: string) =>
    ["org", orgId, "docs", spaceId, "sets"] as const,
  /** 子节点列表：setId 仅文档集需要，personal 容器用空串；parentId 为空串表示容器根 */
  docNodes: (
    orgId: string,
    spaceId: string,
    container: string,
    setId: string,
    parentId: string,
  ) =>
    [
      "org",
      orgId,
      "docs",
      spaceId,
      "nodes",
      container,
      setId,
      parentId,
    ] as const,
  docNode: (orgId: string, spaceId: string, nodeId: string) =>
    ["org", orgId, "docs", spaceId, "node", nodeId] as const,
  docSearch: (orgId: string, spaceId: string, q: string) =>
    ["org", orgId, "docs", spaceId, "search", q] as const,
  // 技能（列表、详情、文件与 Agent 装配都挂在 skills 前缀下，skill.changed 按空间整体失效）
  skills: (orgId: string, spaceId: string) =>
    ["org", orgId, "skills", spaceId] as const,
  skillList: (orgId: string, spaceId: string, filters: unknown) =>
    ["org", orgId, "skills", spaceId, "list", filters] as const,
  skill: (orgId: string, spaceId: string, skillId: string) =>
    ["org", orgId, "skills", spaceId, "detail", skillId] as const,
  skillFile: (orgId: string, spaceId: string, skillId: string, path: string) =>
    ["org", orgId, "skills", spaceId, "file", skillId, path] as const,
  agentSkills: (orgId: string, spaceId: string, agentId: string) =>
    ["org", orgId, "skills", spaceId, "agent", agentId] as const,
  // 市场（全平台共用的只读目录，按组织入口缓存）
  // 外部应用集成（总览与详情共用前缀，断开后整体失效）
  integrations: (orgId: string) => ["org", orgId, "integrations"] as const,
  // 第三方账号绑定
  accountBindings: (orgId: string) =>
    ["org", orgId, "account-bindings"] as const,
  integrationOverview: (orgId: string) =>
    ["org", orgId, "integrations", "overview"] as const,
  integrationDetail: (orgId: string, provider: string) =>
    ["org", orgId, "integrations", "detail", provider] as const,
  integrationSettings: (orgId: string, provider: string) =>
    ["org", orgId, "integrations", "settings", provider] as const,
  integrationWebhook: (orgId: string, connectionId: string) =>
    ["org", orgId, "integrations", "webhook", connectionId] as const,
  integrationRepositories: (orgId: string, connectionId: string) =>
    ["org", orgId, "integrations", "repositories", connectionId] as const,
  // Lark 等飞书家族服务商带 provider 后缀；飞书保持原键
  feishuApp: (orgId: string, provider = "feishu") =>
    provider === "feishu"
      ? (["org", orgId, "integrations", "feishu-app"] as const)
      : (["org", orgId, "integrations", "feishu-app", provider] as const),
  feishuBots: (orgId: string, provider = "feishu") =>
    provider === "feishu"
      ? (["org", orgId, "integrations", "feishu-bots"] as const)
      : (["org", orgId, "integrations", "feishu-bots", provider] as const),
  // 微信绑定列表
  wechatBindings: (orgId: string) =>
    ["org", orgId, "integrations", "wechat-bindings"] as const,
  integrationOAuth: (orgId: string, sessionId: string) =>
    ["org", orgId, "integrations", "oauth", sessionId] as const,
  // 导入（记录列表与单条详情共用前缀）
  imports: (orgId: string) => ["org", orgId, "imports"] as const,
  importList: (orgId: string) => ["org", orgId, "imports", "list"] as const,
  import: (orgId: string, importId: string) =>
    ["org", orgId, "imports", "one", importId] as const,
  importRollback: (orgId: string, importId: string) =>
    ["org", orgId, "imports", "rollback", importId] as const,
  // 克隆空间（任务挂在目标组织下）
  cloneTargets: (orgId: string, spaceId: string) =>
    ["org", orgId, "spaces", "clone-targets", spaceId] as const,
  cloneJob: (targetOrgId: string, jobId: string) =>
    ["org", targetOrgId, "clone-jobs", jobId] as const,
  // 导出任务数据
  exports: (orgId: string) => ["org", orgId, "exports"] as const,
  exportList: (orgId: string) => ["org", orgId, "exports", "list"] as const,
  exportSpaces: (orgId: string) => ["org", orgId, "exports", "spaces"] as const,
  // 成员页外部账号
  externalAccounts: (orgId: string) =>
    ["org", orgId, "external-accounts"] as const,
  externalAccountList: (orgId: string, filters: unknown) =>
    ["org", orgId, "external-accounts", "list", filters] as const,
  market: (orgId: string) => ["org", orgId, "market"] as const,
  marketCatalog: (orgId: string) =>
    ["org", orgId, "market", "catalog"] as const,
  marketSkills: (orgId: string, filters: unknown) =>
    ["org", orgId, "market", "skills", filters] as const,
  marketTemplates: (orgId: string, filters: unknown) =>
    ["org", orgId, "market", "templates", filters] as const,
  marketSkill: (orgId: string, id: string) =>
    ["org", orgId, "market", "skill", id] as const,
  marketSkillFile: (orgId: string, id: string, path: string) =>
    ["org", orgId, "market", "file", id, path] as const,
  marketSkillBlob: (orgId: string, id: string, path: string) =>
    ["org", orgId, "market", "blob", id, path] as const,
  marketTemplate: (orgId: string, id: string) =>
    ["org", orgId, "market", "template", id] as const,
};
