import { describe, expect, it } from "vitest";
import { ApiError, type Api, type ConversationMessage } from "@openfox/api";

import {
  newPendingSend,
  runMentions,
  submitPending,
  type SendTarget,
} from "./send";

const target: SendTarget = { id: "c1", agent_id: "a1" };
const crewTarget: SendTarget = { id: "c1", agent_id: "a1", crew_id: "crew1" };

const saved = {
  id: "m1",
  seq: 1,
  body: "你好",
} as unknown as ConversationMessage;

function at<T>(items: T[], index: number): T {
  const item = items[index];
  if (item === undefined) throw new Error(`缺少第 ${index} 项`);
  return item;
}

interface PostCall {
  orgId: string;
  conversationId: string;
  input: {
    body: string;
    client_message_id: string;
    mentions?: Array<{ agent_id?: string; user_id?: string }>;
  };
}

function fakeApi(options: {
  post: (call: PostCall) => Promise<ConversationMessage>;
  receipt?: () => Promise<{ message: ConversationMessage | null }>;
}) {
  const posts: PostCall[] = [];
  let receiptCalls = 0;
  const api = {
    conversations: {
      postMessage: async (
        orgId: string,
        conversationId: string,
        input: PostCall["input"],
      ) => {
        const call = { orgId, conversationId, input };
        posts.push(call);
        return options.post(call);
      },
      messageReceipt: async () => {
        receiptCalls += 1;
        if (!options.receipt) throw new Error("不应查询回执");
        return options.receipt();
      },
    },
  } as unknown as Api;
  return { api, posts, receiptCalls: () => receiptCalls };
}

const uncertain = () =>
  new ApiError({ status: 0, code: "TIMEOUT", message: "请求超时" });

describe("newPendingSend", () => {
  it("每条待发消息有独立的 client_message_id，正文保持不变", () => {
    const a = newPendingSend("你好");
    const b = newPendingSend("你好");
    expect(a.body).toBe("你好");
    expect(a.clientMessageId).not.toBe(b.clientMessageId);
  });
});

describe("runMentions", () => {
  it("普通会话固定 @ 会话绑定的 Agent，服务端才会安排运行", () => {
    expect(runMentions(target)).toEqual([{ agent_id: "a1" }]);
  });

  it("小队会话不带 @", () => {
    expect(runMentions(crewTarget)).toEqual([]);
  });
});

describe("submitPending", () => {
  it("成功时返回服务端消息，并带上 client_message_id 与正文", async () => {
    const { api, posts } = fakeApi({ post: async () => saved });
    const pending = newPendingSend("你好");
    const result = await submitPending(api, "o1", target, pending);
    expect(result).toBe(saved);
    expect(at(posts, 0).orgId).toBe("o1");
    expect(at(posts, 0).conversationId).toBe("c1");
    expect(at(posts, 0).input).toEqual({
      body: "你好",
      client_message_id: pending.clientMessageId,
      mentions: [{ agent_id: "a1" }],
    });
  });

  it("小队会话不带 @，由服务端派给队长", async () => {
    const { api, posts } = fakeApi({ post: async () => saved });
    const pending = newPendingSend("你好");
    await submitPending(api, "o1", crewTarget, pending);
    expect(at(posts, 0).input).toEqual({
      body: "你好",
      client_message_id: pending.clientMessageId,
    });
  });

  it("网络不确定时先查回执，已写入就直接返回原消息", async () => {
    const { api, receiptCalls } = fakeApi({
      post: async () => {
        throw uncertain();
      },
      receipt: async () => ({ message: saved }),
    });
    const result = await submitPending(
      api,
      "o1",
      target,
      newPendingSend("你好"),
    );
    expect(result).toBe(saved);
    expect(receiptCalls()).toBe(1);
  });

  it("回执为空时抛出原始错误，由调用方用同一条待发消息重试", async () => {
    const { api } = fakeApi({
      post: async () => {
        throw uncertain();
      },
      receipt: async () => ({ message: null }),
    });
    await expect(
      submitPending(api, "o1", target, newPendingSend("你好")),
    ).rejects.toMatchObject({ code: "TIMEOUT" });
  });

  it("回执查询失败时保留原始错误", async () => {
    const { api } = fakeApi({
      post: async () => {
        throw uncertain();
      },
      receipt: async () => {
        throw new Error("回执接口失败");
      },
    });
    await expect(
      submitPending(api, "o1", target, newPendingSend("你好")),
    ).rejects.toMatchObject({ code: "TIMEOUT" });
  });

  it("明确的业务错误不查回执，原样抛出（例如会话已关闭）", async () => {
    const { api, receiptCalls } = fakeApi({
      post: async () => {
        throw new ApiError({
          status: 409,
          code: "CONVERSATION_CLOSED",
          message: "conversation is not open",
        });
      },
    });
    await expect(
      submitPending(api, "o1", target, newPendingSend("你好")),
    ).rejects.toMatchObject({ code: "CONVERSATION_CLOSED" });
    expect(receiptCalls()).toBe(0);
  });

  it("重试复用同一个 client_message_id 与正文，不会产生新的幂等键", async () => {
    let attempts = 0;
    const { api, posts } = fakeApi({
      post: async () => {
        attempts += 1;
        if (attempts === 1) throw uncertain();
        return saved;
      },
      receipt: async () => ({ message: null }),
    });
    const pending = newPendingSend("同一条消息");
    await expect(
      submitPending(api, "o1", target, pending),
    ).rejects.toBeDefined();
    await submitPending(api, "o1", target, pending);
    expect(posts).toHaveLength(2);
    expect(at(posts, 1).input).toEqual(at(posts, 0).input);
    expect(at(posts, 1).input.mentions).toEqual([{ agent_id: "a1" }]);
  });
});
