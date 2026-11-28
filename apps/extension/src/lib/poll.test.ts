import { describe, expect, it } from "vitest";
import type { Api, ConversationMessage } from "@openfox/api";

import {
  POLL_MAX_MS,
  POLL_MIN_MS,
  mergeMessages,
  nextPollDelay,
  pollOnce,
} from "./poll";

const msg = (id: string, seq: number, body = "") =>
  ({ id, seq, body }) as unknown as ConversationMessage;

function fakeApi(page: {
  messages: ConversationMessage[];
  next_cursor: number;
}) {
  const cursors: unknown[] = [];
  const api = {
    conversations: {
      messageWindow: async (
        _orgId: string,
        _conversationId: string,
        cursor: unknown,
      ) => {
        cursors.push(cursor);
        return page;
      },
    },
  } as unknown as Api;
  return { api, cursors };
}

describe("nextPollDelay", () => {
  it("有新消息时回到最短间隔", () => {
    expect(nextPollDelay(9000, "new")).toBe(POLL_MIN_MS);
  });

  it("空闲时缓慢放大，出错时放大得更快", () => {
    expect(nextPollDelay(1500, "idle")).toBe(2250);
    expect(nextPollDelay(1500, "error")).toBe(3000);
  });

  it("不超过上限", () => {
    expect(nextPollDelay(10000, "error")).toBe(POLL_MAX_MS);
    expect(nextPollDelay(POLL_MAX_MS, "idle")).toBe(POLL_MAX_MS);
  });
});

describe("pollOnce", () => {
  it("以 after 游标请求，只接受 seq 大于游标的消息并推进游标", async () => {
    const { api, cursors } = fakeApi({
      messages: [msg("old", 2), msg("a", 3), msg("b", 5)],
      next_cursor: 0,
    });
    const result = await pollOnce(api, "o1", "c1", 2);
    expect(cursors).toEqual([{ after: 2 }]);
    expect(result.messages.map((m) => m.id)).toEqual(["a", "b"]);
    expect(result.cursor).toBe(5);
    expect(result.more).toBe(false);
  });

  it("服务端还有下一页时标记 more", async () => {
    const { api } = fakeApi({
      messages: [msg("a", 3)],
      next_cursor: 9,
    });
    const result = await pollOnce(api, "o1", "c1", 2);
    expect(result.more).toBe(true);
  });

  it("没有新消息时游标不动，也不会要求立刻再拉（避免死循环）", async () => {
    const { api } = fakeApi({ messages: [msg("old", 2)], next_cursor: 9 });
    const result = await pollOnce(api, "o1", "c1", 2);
    expect(result.messages).toEqual([]);
    expect(result.cursor).toBe(2);
    expect(result.more).toBe(false);
  });
});

describe("mergeMessages", () => {
  it("按 id 去重并按 seq 升序排列", () => {
    const merged = mergeMessages(
      [msg("b", 2), msg("a", 1)],
      [msg("c", 3), msg("b", 2)],
    );
    expect(merged.map((m) => m.id)).toEqual(["a", "b", "c"]);
  });

  it("重复到达的消息以新值为准", () => {
    const merged = mergeMessages([msg("a", 1, "旧")], [msg("a", 1, "新")]);
    expect(merged).toHaveLength(1);
    expect(merged[0]?.body).toBe("新");
  });

  it("不修改传入的数组", () => {
    const existing = [msg("a", 1)];
    mergeMessages(existing, [msg("b", 2)]);
    expect(existing).toHaveLength(1);
  });
});
