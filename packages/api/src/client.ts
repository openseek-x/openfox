import { createDirectChatApi } from "./directChat";
import { createAgentsApi } from "./agents";
import { createAutomationsApi } from "./automations";
import { createConfigApi } from "./config";
import { createConversationsApi } from "./conversations";
import { createDevicesApi } from "./devices";
import { createAttachmentsApi } from "./attachments";
import { createQuickCommandsApi } from "./quickCommands";
import { createQuickRepliesApi } from "./quickReplies";
import { createDocsApi } from "./docs";
import { createSearchApi } from "./search";
import { createSkillsApi } from "./skills";
import { createCloneApi } from "./clone";
import { createExportsApi } from "./exports";
import { createImportsApi } from "./imports";
import { createIntegrationsApi } from "./integrations";
import { createMarketApi } from "./market";
import { HttpClient } from "./http";
import { createRunsApi } from "./runs";
import { createRuntimeSessionExportsApi } from "./runtimeSessionExports";
import { createSpacesApi } from "./spaces";
import { createIterationsApi } from "./iterations";
import { createMeApi } from "./me";
import { createProjectsApi } from "./projects";
import { createOrganizationApi } from "./organization";
import { createTriageApi } from "./triage";
import { createViewsApi } from "./views";
import type { components } from "./schema.gen";
import { createInboxMethods, createTasksApi, type InboxCounts } from "./tasks";

type S = components["schemas"];
export type LoginSession = S["LoginSession"];
export type Me = S["Me"];
export type Organization = S["Organization"];
export type OrganizationRole = S["OrganizationRole"];
export type Space = S["Space"];

// 以下类型对应已有接口，契约尚未补进 contracts/openapi.yaml（阶段 1 补齐）。
export interface TaskType {
  id: string;
  name: string;
  is_default: boolean;
  enabled: boolean;
  version: number;
}

const org = (id: string) => `/v1/organizations/${encodeURIComponent(id)}`;
const space = (orgId: string, spaceId: string) =>
  `${org(orgId)}/spaces/${encodeURIComponent(spaceId)}`;

/** 按契约划分的接口方法。新接口先写进 contracts/openapi.yaml，运行 pnpm gen:api，再在这里补方法。 */
export function createApi(http = new HttpClient()) {
  return {
    http,
    // 阶段 1 · 全局配置：labelGroups / labels / taskStatuses / relationTypes / customFields / orgTaskTypes / typeFields
    ...createConfigApi(http),
    // 阶段 1 · 任务：tasks / comments / myTasks / members / spaceMembers / spaceAgents / agentDirectory
    ...createTasksApi(http),
    // 阶段 2a · 电脑与事件：devices / events
    ...createDevicesApi(http),
    // 阶段 2a · Agent 与小队：agents / crews / contacts / invitations
    ...createAgentsApi(http),
    // 阶段 2b · Run 执行过程：runs
    ...createRunsApi(http),
    ...createRuntimeSessionExportsApi(http),
    // 阶段 2b · 对话与小助手：conversations / assistant
    ...createConversationsApi(http),
    ...createDirectChatApi(http),
    // 阶段 3a · 空间设置与生命周期：spaces / spaceRepos / spaceMembership / taskTypeTemplates
    ...createSpacesApi(http),
    // 阶段 3b · 迭代设置、列表、详情与任务规划
    ...createIterationsApi(http),
    // 阶段 3c · 专项与视图
    ...createProjectsApi(http),
    ...createViewsApi(http),
    // 阶段 3d · 分拣台
    ...createTriageApi(http),
    // 阶段 5a · Agent 自动化、运行记录与 Webhook 投递
    ...createAutomationsApi(http),
    // 阶段 5b · 文档库：文档集、文档节点、移动、上传与附件
    ...createDocsApi(http),
    // 阶段 5c · 技能：空间技能库、导入、文件读写与 Agent 装配
    ...createSkillsApi(http),
    // 阶段 5d · 市场：技能与 Agent 模板目录、文件预览
    ...createMarketApi(http),
    // 阶段 6a · 外部应用集成
    ...createIntegrationsApi(http),
    // 阶段 6c · 导入（Jira CSV）
    ...createImportsApi(http),
    // 阶段 7a · 克隆空间到另一个组织
    ...createCloneApi(http),
    ...createExportsApi(http),
    // 阶段 7c · 任务附件与对话附件
    ...createAttachmentsApi(http),
    ...createQuickRepliesApi(http),
    // 阶段 9f · 任务快捷指令
    ...createQuickCommandsApi(http),
    // 阶段 8e · 全局搜索（命令面板）
    ...createSearchApi(http),
    // 阶段 4a · 组织设置、成员管理、邀请链接
    ...createOrganizationApi(http),
    auth: {
      requestEmailCode: (email: string) =>
        http.request<{ expires_at: string }>("/v1/auth/email/request", {
          method: "POST",
          body: { email },
        }),
      verifyEmailCode: (
        email: string,
        code: string,
        clientKind: "web" | "desktop" = "web",
      ) =>
        http.request<LoginSession>("/v1/auth/email/verify", {
          method: "POST",
          body: { email, code, client_kind: clientKind },
        }),
      logout: () => http.request<void>("/v1/auth/logout", { method: "POST" }),
    },
    me: {
      get: () => http.request<Me>("/v1/me"),
      organizations: async () =>
        (
          await http.request<{ organizations: Organization[] }>(
            "/v1/me/organizations",
          )
        ).organizations,
      // 阶段 4b · 个人资料、偏好与修改邮箱
      ...createMeApi(http),
    },
    organizations: {
      create: (input: { name: string; slug: string }, idempotencyKey: string) =>
        http.request<Organization>("/v1/organizations", {
          method: "POST",
          body: input,
          idempotencyKey,
        }),
    },
    invites: {
      accept: (inviteId: string, token: string) =>
        http.request<{ organization_id: string; role: string }>(
          `/v1/invites/${encodeURIComponent(inviteId)}/accept`,
          {
            method: "POST",
            body: { token },
          },
        ),
    },
    taskTypes: {
      list: async (orgId: string, spaceId: string) =>
        (
          await http.request<{ task_types: TaskType[] }>(
            `${space(orgId, spaceId)}/task-types`,
          )
        ).task_types,
      create: (orgId: string, spaceId: string, name: string) =>
        http.request<TaskType>(`${space(orgId, spaceId)}/task-types`, {
          method: "POST",
          body: { name },
        }),
      rename: (
        orgId: string,
        spaceId: string,
        typeId: string,
        name: string,
        version: number,
      ) =>
        http.request<TaskType>(
          `${space(orgId, spaceId)}/task-types/${encodeURIComponent(typeId)}`,
          {
            method: "PATCH",
            body: { name, version },
          },
        ),
    },
    inbox: {
      stats: async (orgId: string) =>
        (
          await http.request<{ counts: InboxCounts }>(
            `${org(orgId)}/inbox/stats`,
          )
        ).counts,
      ...createInboxMethods(http),
    },
  };
}

export type Api = ReturnType<typeof createApi>;
