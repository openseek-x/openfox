import type { HttpClient } from "./http";
import type { components, operations } from "./schema.gen";

// 阶段 2a · 电脑（设备）管理与统一实时事件流。
// 类型全部来自 schema.gen.ts，不手写。

type S = components["schemas"];
type JsonBody<K extends keyof operations> =
  NonNullable<operations[K]["requestBody"]> extends {
    content: { "application/json": infer B };
  }
    ? B
    : never;

export type Device = S["Device"];
export type DeviceDetail = S["DeviceDetail"];
export type DeviceRuntime = S["DeviceRuntime"];
export type DeviceRuntimeModel = S["DeviceRuntimeModel"];
export type DeviceGrantScope = S["DeviceGrantScope"];
export type DeviceBoundAgent = S["DeviceBoundAgent"];
export type DeviceRemovalBlockers = S["DeviceRemovalBlockers"];
export type DeviceRemovalResult = S["DeviceRemovalResult"];
// 阶段 8g · 重新连接后把旧电脑记录上的 Agent 迁到新记录
export type DeviceReplacementResult = S["DeviceReplacementResult"];
export type UpdateDeviceInput = JsonBody<"updateDevice">;
export type UpdateDeviceScopeInput = JsonBody<"updateDeviceScope">;
export type IssueRegistrationTokenInput =
  JsonBody<"issueDeviceRegistrationToken">;
export type RegistrationToken = S["DeviceRegistrationToken"];
// 阶段 2c · 连接电脑、存储与远程命令
export type DeviceRegistrationScope = S["DeviceRegistrationScope"];
export type DeviceStorage = S["DeviceStorage"];
export type DeviceStoragePolicy = S["DeviceStoragePolicy"];
export type DevicePreferences = S["DevicePreferences"];
export type DeviceCommand = S["DeviceCommand"];
export type DeviceCommandKind = S["DeviceCommandKind"];
export type DeviceCommandStatus = S["DeviceCommandStatus"];
export type CreateDeviceCommandInput = JsonBody<"createDeviceCommand">;
export type DeviceRuntimeProbeResult = S["DeviceRuntimeProbeResult"];
export type DeviceRuntimeTestResult = S["DeviceRuntimeTestResult"];
export type DeviceStorageCategory = S["DeviceStorageCategory"];
export type DeviceStorageCleanupResult = S["DeviceStorageCleanupResult"];
export type RunStreamEvent = S["RunStreamEvent"];
export type OrganizationEvent = S["OrganizationEvent"];
export type OrganizationEventType = S["OrganizationEventType"];

/** 统一事件流推送的组织事件类型（失效通知）。 */
export const ORGANIZATION_EVENT_TYPES: readonly OrganizationEventType[] = [
  "direct_chat.changed",
  "task.changed",
  "inbox.changed",
  "run.state",
  "conversation.updated",
  "conversation.message",
  // 阶段 3a · 空间设置变化（资料、开关、停用、删除）
  "space.changed",
  // 阶段 3b · 迭代与任务归属变化
  "iteration.changed",
  // 阶段 3c · 专项与视图变化
  "project.changed",
  "view.changed",
  // 阶段 3d · 分拣设置与责任成员、规则变化
  "triage.changed",
  // 阶段 4a · 组织设置、成员与邀请变化
  "organization.changed",
  "member.changed",
  // 阶段 5a · 自动化与触发器变化
  "automation.changed",
  // 阶段 5b · 文档库变化（只发给有权限的成员；我的文档只发给本人）
  "doc.changed",
  // 阶段 5c · 技能变化（带 space_id）
  "skill.changed",
  // 阶段 10d · Agent 发布了会话产物（带 conversation_id，payload.run_id）
  "run.artifact",
];

/** 列表分页上限：单页 200 台，最多翻 10 页，防止异常游标导致无限请求。 */
const DEVICE_PAGE_SIZE = 200;
const DEVICE_MAX_PAGES = 10;

const org = (id: string) => `/v1/organizations/${encodeURIComponent(id)}`;
const devicePath = (orgId: string, deviceId: string) =>
  `${org(orgId)}/devices/${encodeURIComponent(deviceId)}`;

/** 电脑与事件方法组，由 createApi 展开到顶层。 */
export function createDevicesApi(http: HttpClient) {
  return {
    devices: {
      /** 当前组织可见的全部电脑（自动翻页，按注册时间升序） */
      list: async (orgId: string): Promise<Device[]> => {
        const devices: Device[] = [];
        let cursor: string | undefined;
        for (let page = 0; page < DEVICE_MAX_PAGES; page++) {
          const result = await http.request<{
            devices: Device[];
            next_cursor: string | null;
          }>(`${org(orgId)}/devices`, {
            query: { limit: DEVICE_PAGE_SIZE, cursor },
          });
          devices.push(...result.devices);
          if (!result.next_cursor) break;
          cursor = result.next_cursor;
        }
        return devices;
      },
      /** 当前中心地址（与安装令牌里的一致），用于发现本机注册的地址已过期 */
      center: (orgId: string) =>
        http.request<{ center_url: string }>(`${org(orgId)}/devices/center`),
      get: (orgId: string, deviceId: string) =>
        http.request<DeviceDetail>(devicePath(orgId, deviceId)),
      /** 名称与并发上限；settings_version 乐观锁，冲突 409 VERSION_CONFLICT */
      update: (orgId: string, deviceId: string, input: UpdateDeviceInput) =>
        http.request<Device>(devicePath(orgId, deviceId), {
          method: "PATCH",
          body: input,
        }),
      /** 可见范围；version = permission_version */
      updateScope: (
        orgId: string,
        deviceId: string,
        input: UpdateDeviceScopeInput,
      ) =>
        http.request<Device>(`${devicePath(orgId, deviceId)}/scope`, {
          method: "POST",
          body: input,
        }),
      removalBlockers: (orgId: string, deviceId: string) =>
        http.request<DeviceRemovalBlockers>(
          `${devicePath(orgId, deviceId)}/removal-blockers`,
        ),
      /** 移除机器（仅所有者）；仍有绑定 Agent 时 409 DEVICE_HAS_ACTIVE_AGENTS */
      remove: (orgId: string, deviceId: string) =>
        http.request<void>(devicePath(orgId, deviceId), { method: "DELETE" }),
      removeFromOrganization: (orgId: string, deviceId: string) =>
        http.request<DeviceRemovalResult>(
          `${devicePath(orgId, deviceId)}/remove-from-organization`,
          { method: "POST" },
        ),
      /** agent_ids 缺省 = 全部绑定这台电脑的 Agent */
      unbindAgents: (orgId: string, deviceId: string, agentIds?: string[]) =>
        http.request<DeviceRemovalResult>(
          `${devicePath(orgId, deviceId)}/unbind-agents`,
          {
            method: "POST",
            body: agentIds?.length ? { agent_ids: agentIds } : {},
          },
        ),
      /** deviceId 是替换后的新记录；旧记录上的 Agent 迁到它，旧记录在本组织的授权被吊销 */
      replace: (orgId: string, deviceId: string, replacedDeviceId: string) =>
        http.request<DeviceReplacementResult>(
          `${devicePath(orgId, deviceId)}/replace`,
          { method: "POST", body: { replaced_device_id: replacedDeviceId } },
        ),
      issueRegistrationToken: (
        orgId: string,
        input: IssueRegistrationTokenInput,
      ) =>
        http.request<RegistrationToken>(
          `${org(orgId)}/devices/registration-tokens`,
          { method: "POST", body: input },
        ),
      /** 下发远程命令（仅电脑所有者）；已有同 kind 未完成命令时返回该命令（200），否则新建（202） */
      createCommand: async (
        orgId: string,
        deviceId: string,
        input: CreateDeviceCommandInput,
      ) =>
        (
          await http.request<{ command: DeviceCommand }>(
            `${devicePath(orgId, deviceId)}/commands`,
            { method: "POST", body: input },
          )
        ).command,
      /** 读取命令（轮询用）；服务端读取时会把过期未完成的标记为 EXPIRED */
      getCommand: async (orgId: string, deviceId: string, commandId: string) =>
        (
          await http.request<{ command: DeviceCommand }>(
            `${devicePath(orgId, deviceId)}/commands/${encodeURIComponent(commandId)}`,
          )
        ).command,
    },
    events: {
      /** 统一事件流地址（EventSource 用；重连时浏览器自动带 Last-Event-ID） */
      url: (orgId: string) => http.url(`${org(orgId)}/events`),
    },
  };
}
