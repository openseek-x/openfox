import type { HttpClient } from "./http";
import type { components, operations } from "./schema.gen";

// 阶段 4a · 组织设置、成员管理、邀请与邀请链接。所有公开类型取自生成契约。
type S = components["schemas"];
type JsonBody<K extends keyof operations> =
  NonNullable<operations[K]["requestBody"]> extends {
    content: { "application/json": infer B };
  }
    ? B
    : never;

export type OrganizationSettings = S["OrganizationSettings"];
export type GitSettings = S["GitSettings"];
export type GitSettingsInput = S["GitSettingsInput"];
export type OrganizationSettingsPatch = S["OrganizationSettingsPatch"];
export type InviteLink = S["InviteLink"];
export type InviteLinkInput = JsonBody<"putInviteLink">;
export type InviteLinkPreview = S["InviteLinkPreview"];
export type MemberSpace = S["MemberSpace"];
export type MemberChange = JsonBody<"changeOrganizationMember">;
export type GetStartedProgress = S["GetStartedProgress"];
export type GetStartedCategory = S["GetStartedCategory"];
export type GetStartedStep = S["GetStartedStep"];

const org = (id: string) => `/v1/organizations/${encodeURIComponent(id)}`;

export function createOrganizationApi(http: HttpClient) {
  const gitPath = (orgId: string, spaceId?: string) =>
    `${org(orgId)}${spaceId ? `/spaces/${encodeURIComponent(spaceId)}` : ""}/git-settings`;
  return {
    gitSettings: {
      get: (orgId: string, spaceId?: string) =>
        http.request<GitSettings>(gitPath(orgId, spaceId)),
      put: (orgId: string, input: GitSettingsInput, spaceId?: string) =>
        http.request<GitSettings>(gitPath(orgId, spaceId), {
          method: "PUT",
          body: input,
        }),
    },
    organizationSettings: {
      get: (orgId: string) =>
        http.request<OrganizationSettings>(`${org(orgId)}/settings`),
      update: (orgId: string, patch: OrganizationSettingsPatch) =>
        http.request<OrganizationSettings>(`${org(orgId)}/settings`, {
          method: "PATCH",
          body: patch,
        }),
      /** 切换为 4 级前确认用的「最低」任务数 */
      lowestPriorityCount: async (orgId: string) =>
        (
          await http.request<{ count: number }>(
            `${org(orgId)}/settings/lowest-priority-count`,
          )
        ).count,
      /** 唯一所有者 409 OWNER_REQUIRED；只剩自己 409 SOLE_MEMBER */
      leave: (orgId: string) =>
        http.request<void>(`${org(orgId)}/leave`, { method: "POST" }),
      /** confirm_name 必须与组织名完全一致（400 CONFIRM_MISMATCH） */
      remove: (orgId: string, confirmName: string) =>
        http.request<void>(org(orgId), {
          method: "DELETE",
          body: { confirm_name: confirmName },
        }),
    },
    memberships: {
      change: (orgId: string, userId: string, input: MemberChange) =>
        http.request<S["OrganizationMember"]>(
          `${org(orgId)}/members/${encodeURIComponent(userId)}`,
          { method: "PATCH", body: input },
        ),
    },
    inviteLink: {
      /** 没有密钥配置时 503 SECRETS_UNAVAILABLE */
      get: (orgId: string) =>
        http.request<InviteLink>(`${org(orgId)}/invite-link`),
      put: (orgId: string, input: InviteLinkInput) =>
        http.request<InviteLink>(`${org(orgId)}/invite-link`, {
          method: "PUT",
          body: input,
        }),
      reset: (orgId: string, version: number) =>
        http.request<InviteLink>(`${org(orgId)}/invite-link/reset`, {
          method: "POST",
          body: { version },
        }),
      preview: (token: string) =>
        http.request<InviteLinkPreview>("/v1/invite-links/preview", {
          method: "POST",
          body: { token },
        }),
      join: (token: string) =>
        http.request<{
          organization_id: string;
          role: string;
          status: "JOINED" | "ALREADY_MEMBER";
        }>("/v1/invite-links/join", { method: "POST", body: { token } }),
    },
    // 阶段 4c · 开始使用进度（所有成员可读，按组织计算）
    getStarted: {
      progress: (orgId: string) =>
        http.request<GetStartedProgress>(`${org(orgId)}/get-started`),
    },
  };
}
