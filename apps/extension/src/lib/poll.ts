// 消息轮询：按 after 游标拉取新消息，退避与合并都是纯函数，定时器与面板可见性由界面层控制。
import type { Api, ConversationMessage } from "@openfox/api";

export const POLL_MIN_MS = 1500;
export const POLL_MAX_MS = 15000;

export type PollOutcome = "new" | "idle" | "error";

/** 有新消息时回到最短间隔；空闲时缓慢放大，出错时放大得更快，都不超过上限。 */
export function nextPollDelay(current: number, outcome: PollOutcome): number {
  if (outcome === "new") return POLL_MIN_MS;
  const factor = outcome === "error" ? 2 : 1.5;
  return Math.min(Math.round(current * factor), POLL_MAX_MS);
}

export interface PollResult {
  messages: ConversationMessage[];
  /** 已处理到的最大 seq，下一次以它作为 after。 */
  cursor: number;
  /** 这一页还没取完，应当立刻再拉一次。 */
  more: boolean;
}

/** 拉取 cursor 之后的一页消息；只接受 seq 大于游标的消息，游标只增不减。 */
export async function pollOnce(
  api: Api,
  orgId: string,
  conversationId: string,
  cursor: number,
): Promise<PollResult> {
  const page = await api.conversations.messageWindow(orgId, conversationId, {
    after: cursor,
  });
  const messages = page.messages.filter((message) => message.seq > cursor);
  const next = messages.reduce(
    (max, message) => Math.max(max, message.seq),
    cursor,
  );
  return {
    messages,
    cursor: next,
    more: page.next_cursor > 0 && next > cursor,
  };
}

/** 按 id 去重并按 seq 升序合并，重复到达的消息以新值为准。 */
export function mergeMessages(
  existing: ConversationMessage[],
  incoming: ConversationMessage[],
): ConversationMessage[] {
  const byId = new Map<string, ConversationMessage>();
  for (const message of existing) byId.set(message.id, message);
  for (const message of incoming) byId.set(message.id, message);
  return [...byId.values()].sort((a, b) => a.seq - b.seq);
}
