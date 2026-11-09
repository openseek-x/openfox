import { QueryClient } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { keys } from "../context";
import {
  EVENT_COALESCE_MS,
  createEventBatcher,
} from "../realtime/useOrgEvents";

const ORG = "o1";
const k = (key: readonly unknown[]) => JSON.stringify(key);

function seeded() {
  const client = new QueryClient();
  const seed = (key: readonly unknown[]) => client.setQueryData(key, {});
  seed(keys.conversationList(ORG, {}));
  seed(keys.conversation(ORG, "c1"));
  seed(keys.conversation(ORG, "c2"));
  seed(keys.conversationMessages(ORG, "c1"));
  seed(keys.conversationMessages(ORG, "c2"));
  seed(keys.assistantSessions(ORG));
  const stale = () =>
    client
      .getQueryCache()
      .getAll()
      .filter((query) => query.state.isInvalidated)
      .map((query) => k(query.queryKey))
      .sort();
  return { client, stale };
}

describe("会话实时事件 → 精确失效", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("conversation.message 只失效该会话的详情与消息，并刷新列表与小助手会话", () => {
    const { client, stale } = seeded();
    const batcher = createEventBatcher(client, ORG);
    batcher.push("conversation.message", {
      type: "conversation.message",
      conversation_id: "c1",
      payload: { seq: 4 },
    });
    vi.advanceTimersByTime(EVENT_COALESCE_MS);
    expect(stale()).toEqual(
      [
        k(keys.conversationList(ORG, {})),
        k(keys.conversation(ORG, "c1")),
        k(keys.conversationMessages(ORG, "c1")),
        k(keys.assistantSessions(ORG)),
      ].sort(),
    );
  });

  it("conversation.updated / run.state 只失效详情，不重拉消息；不带 conversation_id 时整体失效", () => {
    const { client, stale } = seeded();
    const batcher = createEventBatcher(client, ORG);
    batcher.push("conversation.updated", {
      type: "conversation.updated",
      conversation_id: "c2",
      payload: {},
    });
    vi.advanceTimersByTime(EVENT_COALESCE_MS);
    expect(stale()).not.toContain(k(keys.conversationMessages(ORG, "c2")));
    expect(stale()).toContain(k(keys.conversation(ORG, "c2")));
    expect(stale()).not.toContain(k(keys.conversation(ORG, "c1")));

    const second = seeded();
    const other = createEventBatcher(second.client, ORG);
    other.push("conversation.message", {
      type: "conversation.message",
      payload: {},
    });
    vi.advanceTimersByTime(EVENT_COALESCE_MS);
    expect(second.stale()).toContain(k(keys.conversationMessages(ORG, "c2")));
  });
});
