import type { HttpClient } from "./http";
import type { components, operations } from "./schema.gen";

// 阶段 2b · 对话与小助手：会话列表 / 详情 / 重命名归档 / 置顶 / 已读 / 绑定任务 / 转任务、
// 消息（向上分页）、完成 / 重新打开；小助手本体、会话选择器、草稿确认与撤销。
// 类型全部来自 schema.gen.ts，不手写。

type S = components["schemas"];
type JsonBody<K extends keyof operations> =
  NonNullable<operations[K]["requestBody"]> extends {
    content: { "application/json": infer B };
  }
    ? B
    : never;

export type ConversationWorkspaceSelection =
  S["ConversationWorkspaceSelection"];
export type ConversationWorkspaceOptions = S["ConversationWorkspaceOptions"];
export type Conversation = S["Conversation"];
export type ConversationSummary = S["ConversationSummary"];
export type TaskConversationStats = S["TaskConversationStats"];
export type ConversationStatus = S["ConversationStatus"];
export type ConversationAgent = S["ConversationAgent"];
export type ConversationLastMessage = S["ConversationLastMessage"];
export type ConversationRunRef = S["ConversationRun"];
export type ConversationMessage = S["Message"];
export type MessageMention = S["MessageMention"];
export type MessageCreateInput = S["MessageCreateRequest"];
export type ConversationCreateInput = S["ConversationCreateRequest"];
export type ConversationUpdateInput = S["ConversationUpdateRequest"];
export type ApprovalMode = S["ApprovalMode"];
export type ReasoningEffort = S["ReasoningEffort"];
export type ConversationOverrides = S["ConversationOverrides"];
export type ConversationEffectiveConfig = S["ConversationEffectiveConfig"];
export type ConversationOverrideOptions = S["ConversationOverrideOptions"];
export type ConversationOverridesInput = S["ConversationOverridesRequest"];
export type ConversationListQuery = NonNullable<
  operations["listConversations"]["parameters"]["query"]
>;
export type AssistantSession = S["AssistantSession"];
export type AssistantProposalConfirmInput =
  S["AssistantProposalConfirmRequest"];
export type EnsureAssistantInput = JsonBody<"ensureAssistant">;

/** 一页会话；next_cursor 为 null 表示没有下一页。 */
export interface ConversationPage {
  conversations: ConversationSummary[];
  task_stats?: TaskConversationStats;
  next_cursor: string | null;
}

/** 一页消息（页内 seq 升序）；next_cursor 为 0 表示没有下一页。 */
export interface MessagePage {
  messages: ConversationMessage[];
  next_cursor: number;
}

const org = (id: string) => `/v1/organizations/${encodeURIComponent(id)}`;
const seg = encodeURIComponent;
const conv = (orgId: string, conversationId: string) =>
  `${org(orgId)}/conversations/${seg(conversationId)}`;

/** HttpClient 会把数字游标转成字符串（防 int64 丢精度），消息 seq 远小于安全整数，这里转回数字。 */
function toSeq(value: unknown): number {
  const n = Number(value);
  return Number.isFinite(n) && n > 0 ? n : 0;
}

/** 对话方法组，由 createApi 展开到顶层。 */
export function createConversationsApi(http: HttpClient) {
  return {
    conversations: {
      workspaceOptions: (
        orgId: string,
        agentId: string,
        sourceConversationId?: string,
      ) =>
        http.request<ConversationWorkspaceOptions>(
          `${org(orgId)}/conversation-workspace-options`,
          {
            query: {
              agent_id: agentId,
              source_conversation_id: sourceConversationId,
            },
          },
        ),
      removeRecentDirectory: (
        orgId: string,
        agentId: string,
        deviceId: string,
        directory: string,
      ) =>
        http.request<void>(`${org(orgId)}/conversation-workspace-recents`, {
          method: "DELETE",
          query: { agent_id: agentId, device_id: deviceId },
          body: { directory },
        }),
      offerCandidates: (
        orgId: string,
        id: string,
        kind: "INVITE" | "TRANSFER",
        q: string,
      ) =>
        http.request<S["ConversationOfferPeer"][]>(
          `${org(orgId)}/conversations/${seg(id)}/offer-candidates`,
          { query: { kind, q } },
        ),
      offer: (
        orgId: string,
        id: string,
        input: S["ConversationOfferInput"],
        key: string,
      ) =>
        http.request<void>(`${org(orgId)}/conversations/${seg(id)}/offers`, {
          method: "POST",
          body: input,
          idempotencyKey: key,
        }),
      pendingOffers: (orgId: string) =>
        http.request<S["PendingConversationOffer"][]>(
          `${org(orgId)}/conversation-offers`,
        ),
      decideOffer: (orgId: string, id: string, accept: boolean) =>
        http.request<void>(`${org(orgId)}/conversation-offers/${seg(id)}`, {
          method: "PUT",
          body: { accept },
        }),

      /** 一页会话（updated_at desc）；旧代码直接取数组的写法改用 page */
      page: async (
        orgId: string,
        query: ConversationListQuery = {},
      ): Promise<ConversationPage> => {
        const result = await http.request<Partial<ConversationPage>>(
          `${org(orgId)}/conversations`,
          { query },
        );
        return {
          conversations: result.conversations ?? [],
          next_cursor: result.next_cursor || null,
          ...(result.task_stats && { task_stats: result.task_stats }),
        };
      },
      get: (orgId: string, conversationId: string) =>
        http.request<ConversationSummary>(conv(orgId, conversationId)),
      create: (orgId: string, input: ConversationCreateInput) =>
        http.request<Conversation>(`${org(orgId)}/conversations`, {
          method: "POST",
          body: input,
        }),
      /** 重命名（title: null / 空串清除）、归档与恢复 */
      update: (
        orgId: string,
        conversationId: string,
        input: ConversationUpdateInput,
      ) =>
        http.request<ConversationSummary>(conv(orgId, conversationId), {
          method: "PATCH",
          body: input,
        }),
      /** 修改对话的 Runtime / 模型 / 权限模式覆盖（下一次运行生效）；字段缺省 = 不改，null = 清除覆盖 */
      updateOverrides: (
        orgId: string,
        conversationId: string,
        input: ConversationOverridesInput,
      ) =>
        http.request<ConversationSummary>(
          `${conv(orgId, conversationId)}/overrides`,
          { method: "PATCH", body: input },
        ),
      /** 输入框里 Runtime / 模型 / 权限模式的可选项（Agent 绑定电脑最近一次心跳；只读，鉴权同修改覆盖） */
      overrideOptions: (orgId: string, conversationId: string) =>
        http.request<ConversationOverrideOptions>(
          `${conv(orgId, conversationId)}/override-options`,
        ),
      pin: (orgId: string, conversationId: string, pinned: boolean) =>
        http.request<void>(`${conv(orgId, conversationId)}/pin`, {
          method: pinned ? "PUT" : "DELETE",
        }),
      /** 已读位置只增不减 */
      markRead: (orgId: string, conversationId: string, seq: number) =>
        http.request<void>(`${conv(orgId, conversationId)}/read`, {
          method: "POST",
          body: { seq },
        }),
      bindTask: (orgId: string, conversationId: string, taskId: string) =>
        http.request<ConversationSummary>(
          `${conv(orgId, conversationId)}/task`,
          {
            method: "POST",
            body: { task_id: taskId },
          },
        ),
      convertToTask: (
        orgId: string,
        conversationId: string,
        idempotencyKey: string,
        input: { title?: string; type_id?: string } = {},
      ) =>
        http.request<ConversationSummary>(
          `${conv(orgId, conversationId)}/convert-to-task`,
          { method: "POST", body: input, idempotencyKey },
        ),
      complete: (orgId: string, conversationId: string, version: number) =>
        http.request<Conversation>(`${conv(orgId, conversationId)}/complete`, {
          method: "POST",
          body: { version },
        }),
      reopen: (orgId: string, conversationId: string, version: number) =>
        http.request<Conversation>(`${conv(orgId, conversationId)}/reopen`, {
          method: "POST",
          body: { version },
        }),
      messageWindow: async (
        orgId: string,
        id: string,
        cursor: { around: string } | { after: number },
      ): Promise<MessagePage> => {
        const page = await http.request<MessagePage>(
          `${conv(orgId, id)}/messages`,
          {
            query: {
              ...cursor,
              order: "after" in cursor ? "asc" : "desc",
              limit: 50,
            },
          },
        );
        return {
          messages: page.messages ?? [],
          next_cursor: toSeq(page.next_cursor),
        };
      },
      /** 最新一页（before 省略）或 before 之前的一页；页内按 seq 升序 */
      messagesBefore: async (
        orgId: string,
        conversationId: string,
        before?: number,
        limit = 50,
      ): Promise<MessagePage> => {
        const result = await http.request<MessagePage>(
          `${conv(orgId, conversationId)}/messages`,
          { query: { order: "desc", before: before || undefined, limit } },
        );
        return {
          messages: result.messages ?? [],
          next_cursor: toSeq(result.next_cursor),
        };
      },
      /** 同会话同 client_message_id 同内容重放返回原消息 */
      retryAssistantRun: (
        orgId: string,
        conversationId: string,
        runId: string,
      ) =>
        http.request<void>(
          `${conv(orgId, conversationId)}/runs/${seg(runId)}/retry`,
          { method: "POST" },
        ),
      postMessage: (
        orgId: string,
        conversationId: string,
        input: MessageCreateInput,
      ) =>
        http.request<ConversationMessage>(
          `${conv(orgId, conversationId)}/messages`,
          { method: "POST", body: input },
        ),
      // 停止运行用 runs.cancel（runs.ts，带 Idempotency-Key）
    },
    assistant: {
      directorySettings: (orgId: string) =>
        http.request<S["AssistantDirectorySettings"]>(
          `${org(orgId)}/assistant/work-directory`,
        ),
      setDirectory: (orgId: string, input: JsonBody<"setAssistantDirectory">) =>
        http.request<S["AssistantDirectorySettings"]>(
          `${org(orgId)}/assistant/work-directory`,
          { method: "PUT", body: input },
        ),
      /** 还没创建时为 null */
      get: async (orgId: string) =>
        (
          await http.request<{ assistant: S["SpaceAgent"] | null }>(
            `${org(orgId)}/assistant`,
          )
        ).assistant,
      /** 幂等：确保小助手存在并运行在指定电脑上；无可放置空间 409 NO_ASSISTANT_SPACE */
      ensure: async (orgId: string, input: EnsureAssistantInput) =>
        (
          await http.request<{ assistant: S["SpaceAgent"] }>(
            `${org(orgId)}/assistant`,
            { method: "PUT", body: input },
          )
        ).assistant,
      /** 会话选择器（含已归档） */
      sessions: async (orgId: string) =>
        (
          await http.request<{ sessions: AssistantSession[] }>(
            `${org(orgId)}/assistant/sessions`,
          )
        ).sessions,
      /** 确认草稿；重复确认 409 PROPOSAL_RESOLVED */
      confirmProposal: (
        orgId: string,
        conversationId: string,
        messageId: string,
        input: AssistantProposalConfirmInput,
      ) =>
        http.request<{
          proposal: Record<string, unknown>;
          executed: Record<string, unknown>;
        }>(
          `${conv(orgId, conversationId)}/messages/${seg(messageId)}/confirm`,
          {
            method: "POST",
            body: input,
          },
        ),
      cancelProposal: (
        orgId: string,
        conversationId: string,
        messageId: string,
      ) =>
        http.request<{ proposal: Record<string, unknown> }>(
          `${conv(orgId, conversationId)}/messages/${seg(messageId)}/proposal`,
          { method: "DELETE" },
        ),
    },
  };
}
