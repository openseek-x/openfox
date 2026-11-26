// 发送消息：同一条待发消息的 client_message_id 与正文在重试时保持不变，
// 服务端据此识别重放，失败重试不会产生重复消息。
import { isApiError, type Api, type ConversationMessage } from "@openfox/api";

export interface PendingSend {
  clientMessageId: string;
  body: string;
}

export function newPendingSend(body: string): PendingSend {
  return { clientMessageId: crypto.randomUUID(), body };
}

/**
 * 提交一条待发消息。网络中断或超时时写入可能已经生效，先按 client_message_id 查回执：
 * 已写入就直接返回原消息；查不到或查询失败则抛出原始错误，由调用方用同一个 PendingSend 重试。
 */
export async function submitPending(
  api: Api,
  orgId: string,
  conversationId: string,
  pending: PendingSend,
): Promise<ConversationMessage> {
  try {
    return await api.conversations.postMessage(orgId, conversationId, {
      body: pending.body,
      client_message_id: pending.clientMessageId,
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
