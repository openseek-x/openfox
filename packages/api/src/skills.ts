import type { HttpClient } from "./http";
import type { components, operations } from "./schema.gen";
import type { MarketTemplateApplyResult } from "./market";

// 阶段 5c · 技能：空间技能库、上传 / URL / Runtime 导入、文件读写、Agent 装配。所有公开类型取自生成契约。
type S = components["schemas"];
type JsonBody<K extends keyof operations> =
  NonNullable<operations[K]["requestBody"]> extends {
    content: { "application/json": infer B };
  }
    ? B
    : never;

export type Skill = S["Skill"];
export type SkillDetail = S["SkillDetail"];
export type SkillSourceKind = S["SkillSourceKind"];
export type SkillAgentRef = S["SkillAgentRef"];
export type SkillFileEntry = S["SkillFileEntry"];
export type SkillFileContent = S["SkillFileContent"];
export type SkillCreateInput = S["SkillCreateInput"];
export type SkillImportItem = S["SkillImportItem"];
export type SkillRuntimeImport = S["SkillRuntimeImport"];
export type DeviceSkillScanResult = S["DeviceSkillScanResult"];
export type SkillSaveInput = JsonBody<"saveSkillFiles">;
export type SkillRuntimeImportInput = JsonBody<"createSkillRuntimeImport">;

/** 技能列表的查询条件。 */
export type SkillListQuery = {
  q?: string;
  sort?: "usage" | "updated" | "created";
  created_by?: string;
};

/** 上传一项：path 仅文件夹上传需要（相对路径，每个 file 之后跟一个 file_path）。 */
export type SkillUploadFile = { file: File; path?: string };

const spacePath = (orgId: string, spaceId: string) =>
  `/v1/organizations/${encodeURIComponent(orgId)}/spaces/${encodeURIComponent(spaceId)}`;
const skillsPath = (orgId: string, spaceId: string) =>
  `${spacePath(orgId, spaceId)}/skills`;
const skillPath = (orgId: string, spaceId: string, skillId: string) =>
  `${skillsPath(orgId, spaceId)}/${encodeURIComponent(skillId)}`;
const agentSkillsPath = (orgId: string, spaceId: string, agentId: string) =>
  `${spacePath(orgId, spaceId)}/agents/${encodeURIComponent(agentId)}/skills`;
const importPath = (orgId: string, spaceId: string, commandId: string) =>
  `${skillsPath(orgId, spaceId)}/runtime-imports/${encodeURIComponent(commandId)}`;

export function createSkillsApi(http: HttpClient) {
  return {
    skills: {
      list: (orgId: string, spaceId: string, query: SkillListQuery = {}) =>
        http.request<{ skills: Skill[]; can_create: boolean }>(
          skillsPath(orgId, spaceId),
          { query },
        ),
      create: (orgId: string, spaceId: string, input: SkillCreateInput) =>
        http.request<SkillDetail>(skillsPath(orgId, spaceId), {
          method: "POST",
          body: input,
        }),
      /** 上传 .md、.zip 或文件夹的多个文件。 */
      upload: (orgId: string, spaceId: string, files: SkillUploadFile[]) => {
        const form = new FormData();
        for (const item of files) {
          form.append("file", item.file);
          if (item.path !== undefined) form.append("file_path", item.path);
        }
        return http.request<SkillDetail>(
          `${skillsPath(orgId, spaceId)}/upload`,
          {
            method: "POST",
            body: form,
          },
        );
      },
      importFromUrl: (orgId: string, spaceId: string, url: string) =>
        http.request<SkillDetail>(`${skillsPath(orgId, spaceId)}/url-imports`, {
          method: "POST",
          body: { url },
        }),
      createRuntimeImport: (
        orgId: string,
        spaceId: string,
        input: SkillRuntimeImportInput,
      ) =>
        http.request<SkillRuntimeImport>(
          `${skillsPath(orgId, spaceId)}/runtime-imports`,
          { method: "POST", body: input },
        ),
      getRuntimeImport: (orgId: string, spaceId: string, commandId: string) =>
        http.request<SkillRuntimeImport>(importPath(orgId, spaceId, commandId)),
      cancelRuntimeImport: (
        orgId: string,
        spaceId: string,
        commandId: string,
      ) =>
        http.request<SkillRuntimeImport>(
          `${importPath(orgId, spaceId, commandId)}/cancel`,
          { method: "POST" },
        ),
      get: (orgId: string, spaceId: string, skillId: string) =>
        http.request<SkillDetail>(skillPath(orgId, spaceId, skillId)),
      remove: (
        orgId: string,
        spaceId: string,
        skill: Pick<Skill, "id" | "version">,
      ) =>
        http.request<void>(skillPath(orgId, spaceId, skill.id), {
          method: "DELETE",
          query: { version: skill.version },
        }),
      getFile: (
        orgId: string,
        spaceId: string,
        skillId: string,
        path: string,
      ) =>
        http.request<SkillFileContent>(
          `${skillPath(orgId, spaceId, skillId)}/files`,
          { query: { path } },
        ),
      /** 阶段 5d：把市场技能复制到空间。 */
      addFromMarket: (orgId: string, spaceId: string, marketSkillId: string) =>
        http.request<SkillDetail>(`${skillsPath(orgId, spaceId)}/market`, {
          method: "POST",
          body: { market_skill_id: marketSkillId },
        }),
      /** 阶段 5d：更新到市场当前版本；version 是空间技能的乐观锁版本。 */
      updateFromMarket: (
        orgId: string,
        spaceId: string,
        skillId: string,
        version: number,
      ) =>
        http.request<SkillDetail>(
          `${skillPath(orgId, spaceId, skillId)}/market-update`,
          { method: "POST", body: { version } },
        ),
      saveFiles: (
        orgId: string,
        spaceId: string,
        skillId: string,
        input: SkillSaveInput,
      ) =>
        http.request<SkillDetail>(
          `${skillPath(orgId, spaceId, skillId)}/files`,
          {
            method: "PUT",
            body: input,
          },
        ),
    },
    agentSkills: {
      installMarketSkill: (
        orgId: string,
        spaceId: string,
        agentId: string,
        marketId: string,
      ) =>
        http.request<void>(
          `${spacePath(orgId, spaceId)}/agents/${encodeURIComponent(agentId)}/market-skills/${encodeURIComponent(marketId)}`,
          { method: "PUT" },
        ),
      /** 阶段 5d：把模板引用的技能装进空间并装配给 Agent。 */
      applyMarketTemplate: (
        orgId: string,
        spaceId: string,
        agentId: string,
        marketTemplateId: string,
      ) =>
        http.request<MarketTemplateApplyResult>(
          `${spacePath(orgId, spaceId)}/agents/${encodeURIComponent(agentId)}/market-template`,
          { method: "POST", body: { market_template_id: marketTemplateId } },
        ),
      list: async (orgId: string, spaceId: string, agentId: string) =>
        (
          await http.request<{ skills: Skill[] }>(
            agentSkillsPath(orgId, spaceId, agentId),
          )
        ).skills,
      set: async (
        orgId: string,
        spaceId: string,
        agentId: string,
        skillIds: string[],
      ) =>
        (
          await http.request<{ skills: Skill[] }>(
            agentSkillsPath(orgId, spaceId, agentId),
            { method: "PUT", body: { skill_ids: skillIds } },
          )
        ).skills,
    },
  };
}
