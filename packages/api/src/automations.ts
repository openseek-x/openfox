import type { HttpClient } from "./http";
import type { components, operations } from "./schema.gen";

// 阶段 5a · Agent 自动化、运行记录与 Webhook 投递。所有公开类型取自生成契约。
type S = components["schemas"];
type JsonBody<K extends keyof operations> =
  NonNullable<operations[K]["requestBody"]> extends {
    content: { "application/json": infer B };
  }
    ? B
    : never;

export type Automation = S["Automation"];
export type AutomationInput = S["AutomationInput"];
export type AutomationSaveResult = S["AutomationSaveResult"];
export type AutomationTrigger = S["AutomationTrigger"];
export type AutomationTriggerInput = S["AutomationTriggerInput"];
export type AutomationTriggerKind = S["AutomationTriggerKind"];
export type AutomationTriggerConfig = S["AutomationTriggerConfig"];
export type AutomationScheduleConfig = S["AutomationScheduleConfig"];
export type AutomationTaskFilter = S["AutomationTaskFilter"];
export type AutomationRun = S["AutomationRun"];
export type AutomationRunDetail = S["AutomationRunDetail"];
export type AutomationRunStatus = S["AutomationRunStatus"];
export type AutomationWebhook = S["AutomationWebhook"];
export type WebhookDelivery = S["WebhookDelivery"];
export type WebhookDeliveryDetail = S["WebhookDeliveryDetail"];
export type AutomationUpdateInput = JsonBody<"updateAutomation">;

const spacePath = (orgId: string, spaceId: string) =>
  `/v1/organizations/${encodeURIComponent(orgId)}/spaces/${encodeURIComponent(spaceId)}`;
const automationPath = (orgId: string, spaceId: string, automationId: string) =>
  `${spacePath(orgId, spaceId)}/automations/${encodeURIComponent(automationId)}`;

export function createAutomationsApi(http: HttpClient) {
  return {
    automations: {
      list: async (orgId: string, spaceId: string) =>
        (
          await http.request<{ automations: Automation[] }>(
            `${spacePath(orgId, spaceId)}/automations`,
          )
        ).automations,
      create: (orgId: string, spaceId: string, input: AutomationInput) =>
        http.request<AutomationSaveResult>(
          `${spacePath(orgId, spaceId)}/automations`,
          { method: "POST", body: input },
        ),
      reorder: (orgId: string, spaceId: string, automationIds: string[]) =>
        http.request<void>(`${spacePath(orgId, spaceId)}/automations/reorder`, {
          method: "PUT",
          body: { automation_ids: automationIds },
        }),
      get: (orgId: string, spaceId: string, automationId: string) =>
        http.request<Automation>(automationPath(orgId, spaceId, automationId)),
      update: (
        orgId: string,
        spaceId: string,
        automationId: string,
        input: AutomationUpdateInput,
      ) =>
        http.request<AutomationSaveResult>(
          automationPath(orgId, spaceId, automationId),
          { method: "PATCH", body: input },
        ),
      remove: (
        orgId: string,
        spaceId: string,
        automation: Pick<Automation, "id" | "version">,
      ) =>
        http.request<void>(automationPath(orgId, spaceId, automation.id), {
          method: "DELETE",
          query: { version: automation.version },
        }),
      run: (orgId: string, spaceId: string, automationId: string) =>
        http.request<AutomationRun>(
          `${automationPath(orgId, spaceId, automationId)}/run`,
          { method: "POST" },
        ),
      runs: (
        orgId: string,
        spaceId: string,
        automationId: string,
        cursor?: string,
      ) =>
        http.request<{ runs: AutomationRun[]; next_cursor: string }>(
          `${automationPath(orgId, spaceId, automationId)}/runs`,
          { query: { cursor } },
        ),
      runDetail: (
        orgId: string,
        spaceId: string,
        automationId: string,
        runId: string,
      ) =>
        http.request<AutomationRunDetail>(
          `${automationPath(orgId, spaceId, automationId)}/runs/${encodeURIComponent(runId)}`,
        ),
      regenerateWebhookURL: (
        orgId: string,
        spaceId: string,
        automationId: string,
        triggerId: string,
      ) =>
        http.request<{ url: string }>(
          `${automationPath(orgId, spaceId, automationId)}/triggers/${encodeURIComponent(triggerId)}/webhook-url`,
          { method: "POST" },
        ),
      signingSecret: (
        orgId: string,
        spaceId: string,
        automationId: string,
        triggerId: string,
      ) =>
        http.request<{ secret: string }>(
          `${automationPath(orgId, spaceId, automationId)}/triggers/${encodeURIComponent(triggerId)}/signing-secret`,
        ),
      resetSigningSecret: (
        orgId: string,
        spaceId: string,
        automationId: string,
        triggerId: string,
      ) =>
        http.request<{ secret: string }>(
          `${automationPath(orgId, spaceId, automationId)}/triggers/${encodeURIComponent(triggerId)}/signing-secret`,
          { method: "POST" },
        ),
      deliveries: (
        orgId: string,
        spaceId: string,
        automationId: string,
        cursor?: string,
      ) =>
        http.request<{ deliveries: WebhookDelivery[]; next_cursor: string }>(
          `${automationPath(orgId, spaceId, automationId)}/deliveries`,
          { query: { cursor } },
        ),
      delivery: (
        orgId: string,
        spaceId: string,
        automationId: string,
        deliveryId: string,
      ) =>
        http.request<WebhookDeliveryDetail>(
          `${automationPath(orgId, spaceId, automationId)}/deliveries/${encodeURIComponent(deliveryId)}`,
        ),
      replayDelivery: (
        orgId: string,
        spaceId: string,
        automationId: string,
        deliveryId: string,
      ) =>
        http.request<WebhookDelivery>(
          `${automationPath(orgId, spaceId, automationId)}/deliveries/${encodeURIComponent(deliveryId)}/replay`,
          { method: "POST" },
        ),
    },
  };
}
