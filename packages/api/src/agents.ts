import type { HttpClient } from "./http";
import type { components, operations } from "./schema.gen";

// 阶段 2a · Agent 与小队：Agent 资料 / 配置版本 / 环境变量 / Git 仓库 / 统计 / 运行历史、
// 目录置顶与收藏、小队、成员页签联系人、邀请。类型全部来自 schema.gen.ts，不手写。

type S = components["schemas"];
type JsonBody<K extends keyof operations> =
  NonNullable<operations[K]["requestBody"]> extends {
    content: { "application/json": infer B };
  }
    ? B
    : never;

export type AgentKind = S["AgentKind"];
export type AgentVisibility = S["AgentVisibility"];
export type AgentAvatarValue = S["AgentAvatar"];
export type AgentDirectoryMode = S["AgentDirectoryMode"];
export type AgentConfigInput = S["AgentConfigInput"];
export type AgentConfigVersion = S["AgentConfigVersion"];
/** 阶段 9a-2：Agent 协作模式（FOLLOW_RUNTIME / DEFAULT / PLAN） */
export type CollaborationMode = S["CollaborationMode"];
export type AgentCreateInput = S["AgentCreateRequest"];
export type AgentConfigCreateInput = S["AgentConfigCreateRequest"];
export type AgentUpdateInput = S["AgentUpdateRequest"];
export type AgentProfilePatch = S["AgentProfilePatch"];
export type AgentEnvVar = S["AgentEnvVar"];
export type AgentEnvVarList = S["AgentEnvVarList"];
export type AgentRepo = S["AgentRepo"];
export type AgentRepoCreateInput = S["AgentRepoCreateRequest"];
export type AgentRepoPatch = S["AgentRepoPatch"];
export type AgentStats = S["AgentStats"];
export type AgentRunSummary = S["AgentRunSummary"];
export type AgentFavoriteTarget = S["AgentFavoriteTarget"];
export type AgentLeader = S["AgentLeader"];
export type AgentDirectoryMember = S["AgentDirectoryMember"];
export type Crew = S["Crew"];
export type CrewMember = S["CrewMember"];
export type CrewCreateInput = S["CrewCreateRequest"];
export type CrewPatch = S["CrewPatch"];
export type CrewMembersInput = S["CrewMembersRequest"];
export type ContactItem = S["ContactItem"];
export type ContactGroup = S["ContactGroup"];
export type Contacts = S["Contacts"];
export type Invitation = S["Invitation"];
export type CreateInvitationInput = JsonBody<"createInvitation">;
export type DirectoryPinInput = JsonBody<"setAgentDirectoryPin">;

/** 一页运行历史；next_cursor 为 null 表示没有下一页。 */
export interface AgentRunPage {
  runs: AgentRunSummary[];
  next_cursor: string | null;
}

const org = (id: string) => `/v1/organizations/${encodeURIComponent(id)}`;
const seg = encodeURIComponent;
const agentPath = (orgId: string, spaceId: string, agentId: string) =>
  `${org(orgId)}/spaces/${seg(spaceId)}/agents/${seg(agentId)}`;
const crewPath = (orgId: string, spaceId: string, crewId: string) =>
  `${org(orgId)}/spaces/${seg(spaceId)}/crews/${seg(crewId)}`;

/** Agent 与小队方法组，由 createApi 展开到顶层。 */
export function createAgentsApi(http: HttpClient) {
  return {
    agents: {
      list: async (orgId: string, spaceId: string, includeArchived = false) =>
        (
          await http.request<{ agents: S["SpaceAgent"][] }>(
            `${org(orgId)}/spaces/${seg(spaceId)}/agents`,
            { query: { include_archived: includeArchived || undefined } },
          )
        ).agents,
      get: (orgId: string, spaceId: string, agentId: string) =>
        http.request<S["SpaceAgent"]>(agentPath(orgId, spaceId, agentId)),
      /** 配置为版本 1；默认可见性由调用方决定（设置页 SHARED，其他入口 PRIVATE） */
      create: (
        orgId: string,
        spaceId: string,
        input: AgentCreateInput,
        idempotencyKey?: string,
      ) =>
        http.request<S["SpaceAgent"]>(
          `${org(orgId)}/spaces/${seg(spaceId)}/agents`,
          { method: "POST", body: input, idempotencyKey },
        ),
      /** 旧接口：名称、说明、类型、可见性、归档；乐观锁 version */
      update: (
        orgId: string,
        spaceId: string,
        agentId: string,
        input: AgentUpdateInput,
      ) =>
        http.request<S["SpaceAgent"]>(agentPath(orgId, spaceId, agentId), {
          method: "PATCH",
          body: input,
        }),
      /** 资料、触发开关与访问权限；乐观锁 profile_version */
      updateProfile: (
        orgId: string,
        spaceId: string,
        agentId: string,
        input: AgentProfilePatch,
      ) =>
        http.request<S["SpaceAgent"]>(
          `${agentPath(orgId, spaceId, agentId)}/profile`,
          { method: "PATCH", body: input },
        ),
      /** 新建配置版本（需传完整配置；expected_agent_version = Agent 的 version） */
      createConfigVersion: (
        orgId: string,
        spaceId: string,
        agentId: string,
        input: AgentConfigCreateInput,
      ) =>
        http.request<S["SpaceAgent"]>(
          `${agentPath(orgId, spaceId, agentId)}/config-versions`,
          { method: "POST", body: input },
        ),
      copy: (
        orgId: string,
        spaceId: string,
        agentId: string,
        idempotencyKey: string,
        name?: string,
      ) =>
        http.request<S["SpaceAgent"]>(
          `${agentPath(orgId, spaceId, agentId)}/copy`,
          { method: "POST", body: name ? { name } : {}, idempotencyKey },
        ),
      stats: (orgId: string, spaceId: string, agentId: string) =>
        http.request<AgentStats>(`${agentPath(orgId, spaceId, agentId)}/stats`),
      runs: (
        orgId: string,
        spaceId: string,
        agentId: string,
        cursor?: string | null,
        limit = 20,
      ) =>
        http.request<AgentRunPage>(
          `${agentPath(orgId, spaceId, agentId)}/runs`,
          { query: { limit, cursor } },
        ),
      envVars: {
        list: (orgId: string, spaceId: string, agentId: string) =>
          http.request<AgentEnvVarList>(
            `${agentPath(orgId, spaceId, agentId)}/env-vars`,
          ),
        /** 不带 version = 新建；带 version = 修改（乐观锁） */
        put: (
          orgId: string,
          spaceId: string,
          agentId: string,
          name: string,
          value: string,
          version?: number,
        ) =>
          http.request<AgentEnvVar>(
            `${agentPath(orgId, spaceId, agentId)}/env-vars/${seg(name)}`,
            {
              method: "PUT",
              body: version === undefined ? { value } : { value, version },
            },
          ),
        remove: (
          orgId: string,
          spaceId: string,
          agentId: string,
          name: string,
        ) =>
          http.request<void>(
            `${agentPath(orgId, spaceId, agentId)}/env-vars/${seg(name)}`,
            { method: "DELETE" },
          ),
      },
      repos: {
        list: async (orgId: string, spaceId: string, agentId: string) =>
          (
            await http.request<{ repos: AgentRepo[] }>(
              `${agentPath(orgId, spaceId, agentId)}/repos`,
            )
          ).repos,
        create: (
          orgId: string,
          spaceId: string,
          agentId: string,
          input: AgentRepoCreateInput,
          idempotencyKey: string,
        ) =>
          http.request<AgentRepo>(
            `${agentPath(orgId, spaceId, agentId)}/repos`,
            { method: "POST", body: input, idempotencyKey },
          ),
        update: (
          orgId: string,
          spaceId: string,
          agentId: string,
          repoId: string,
          input: AgentRepoPatch,
        ) =>
          http.request<AgentRepo>(
            `${agentPath(orgId, spaceId, agentId)}/repos/${seg(repoId)}`,
            { method: "PATCH", body: input },
          ),
        remove: (
          orgId: string,
          spaceId: string,
          agentId: string,
          repoId: string,
        ) =>
          http.request<void>(
            `${agentPath(orgId, spaceId, agentId)}/repos/${seg(repoId)}`,
            { method: "DELETE" },
          ),
      },
      /** 组织级目录；archived=true 只看已归档 */
      directory: (orgId: string, archived = false) =>
        http.request<S["AgentDirectory"]>(`${org(orgId)}/agent-directory`, {
          query: { archived: archived || undefined },
        }),
      /** 目录置顶（个人偏好，幂等） */
      setPin: (orgId: string, input: DirectoryPinInput) =>
        http.request<{ pinned: boolean }>(
          `${org(orgId)}/agent-directory/pins`,
          { method: "PUT", body: input },
        ),
      /** 收藏到侧栏（幂等）；agent_id 与 crew_id 只能传一个 */
      favorite: (orgId: string, target: AgentFavoriteTarget) =>
        http.request<void>(`${org(orgId)}/agent-favorites`, {
          method: "PUT",
          body: target,
        }),
      unfavorite: (orgId: string, target: AgentFavoriteTarget) =>
        http.request<void>(`${org(orgId)}/agent-favorites`, {
          method: "DELETE",
          query: { agent_id: target.agent_id, crew_id: target.crew_id },
        }),
    },
    crews: {
      get: (orgId: string, spaceId: string, crewId: string) =>
        http.request<Crew>(crewPath(orgId, spaceId, crewId)),
      create: (orgId: string, spaceId: string, input: CrewCreateInput) =>
        http.request<Crew>(`${org(orgId)}/spaces/${seg(spaceId)}/crews`, {
          method: "POST",
          body: input,
        }),
      update: (
        orgId: string,
        spaceId: string,
        crewId: string,
        input: CrewPatch,
      ) =>
        http.request<Crew>(crewPath(orgId, spaceId, crewId), {
          method: "PATCH",
          body: input,
        }),
      addMembers: (
        orgId: string,
        spaceId: string,
        crewId: string,
        input: CrewMembersInput,
      ) =>
        http.request<Crew>(`${crewPath(orgId, spaceId, crewId)}/members`, {
          method: "POST",
          body: input,
        }),
      /** 专属成员；冲突 409 AGENT_CREW_MEMBERSHIP_CONFLICT */
      setExclusive: (
        orgId: string,
        spaceId: string,
        crewId: string,
        agentId: string,
        exclusive: boolean,
      ) =>
        http.request<Crew>(
          `${crewPath(orgId, spaceId, crewId)}/members/agents/${seg(agentId)}`,
          { method: "PATCH", body: { exclusive } },
        ),
      /** 队长 409 CREW_LEADER_REQUIRED */
      removeMember: (
        orgId: string,
        spaceId: string,
        crewId: string,
        kind: "agent" | "human",
        memberId: string,
      ) =>
        http.request<void>(
          `${crewPath(orgId, spaceId, crewId)}/members/${kind}/${seg(memberId)}`,
          { method: "DELETE" },
        ),
    },
    contacts: {
      list: (orgId: string) => http.request<Contacts>(`${org(orgId)}/contacts`),
    },
    invitations: {
      /** 邀请邮件在响应前发送，结果见 delivery_status；该邮箱已是成员时 409 ALREADY_MEMBER */
      create: (orgId: string, input: CreateInvitationInput) =>
        http.request<Invitation>(`${org(orgId)}/invites`, {
          method: "POST",
          body: input,
        }),
      list: async (orgId: string) =>
        (
          await http.request<{ invitations: Invitation[] }>(
            `${org(orgId)}/invites`,
          )
        ).invitations,
      revoke: (orgId: string, inviteId: string) =>
        http.request<Invitation>(
          `${org(orgId)}/invites/${seg(inviteId)}/revoke`,
          { method: "POST" },
        ),
      /** 1 分钟内重复 429 RESEND_COOLDOWN */
      resend: (orgId: string, inviteId: string) =>
        http.request<Invitation>(
          `${org(orgId)}/invites/${seg(inviteId)}/resend`,
          { method: "POST" },
        ),
    },
  };
}
