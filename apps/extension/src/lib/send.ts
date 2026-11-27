// 发送消息：同一条待发消息的 client_message_id 与正文在重试时保持不变，
// 服务端据此识别重放，失败重试不会产生重复消息。
import {
  isApiError,
  type Api,
  type ConversationMessage,
  type ConversationSummary,
  type MessageCreateInput,
} from "@openfox/api";

export interface PendingSend {
  clientMessageId: string;
  body: string;
}

export function newPendingSend(body: string): PendingSend {
  return { clientMessageId: crypto.randomUUID(), body };
}

/** 发送目标：只用到会话 ID、绑定 Agent 与小队标记。 */
export type SendTarget = Pick<
  ConversationSummary,
  "id" | "agent_id" | "crew_id"
>;

/**
 * 侧栏里的每条消息都是发给 Agent 的提问。服务端只在消息 @ 了会话绑定的 Agent 时才安排运行
 * （enqueueAgentMention），所以普通会话固定 @ 绑定 Agent，与网页端输入框的自动 @ 一致；
 * 小队会话不带 @，由服务端默认派给队长。结果只取决于会话，重试同一条消息时保持不变。
 */
export function runMentions(
  target: SendTarget,
): NonNullable<MessageCreateInput["mentions"]> {
  return target.crew_id ? [] : [{ agent_id: target.agent_id }];
}

/**
 * 提交一条待发消息。网络中断或超时时写入可能已经生效，先按 client_message_id 查回执：
 * 已写入就直接返回原消息；查不到或查询失败则抛出原始错误，由调用方用同一个 PendingSend 重试。
 */
export async function submitPending(
  api: Api,
  orgId: string,
  target: SendTarget,
  pending: PendingSend,
): Promise<ConversationMessage> {
  const conversationId = target.id;
  const mentions = runMentions(target);
  try {
    return await api.conversations.postMessage(orgId, conversationId, {
      body: pending.body,
      client_message_id: pending.clientMessageId,
      ...(mentions.length > 0 && { mentions }),
    });
  } catch (error) {
    if (isApiError(error) && error.uncertain) {
      try {
        const receipt = await api.conversations.messageReceipt(
          orgId,
          conversationId,
          pending.clientMessageId,
        );
        if (receipt.message) return receipt.message;
      } catch {
        // 回执也查不到时保留原始错误。
      }
    }
    throw error;
  }
}
