import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, type Api, type ConversationSummary } from "@openfox/api";
import { useChat } from "./useChat";

const first = {
  id: "c1",
  space_id: "s1",
  lifecycle: "OPEN",
} as ConversationSummary;
const second = { ...first, id: "c2", space_id: "s2" };
const page = (id: string) => ({ messages: [{ id, seq: 1 }], next_cursor: 0 });

function setup() {
  const conversations = {
    page: vi
      .fn()
      .mockResolvedValue({ conversations: [first, second], next_cursor: null }),
    create: vi.fn().mockResolvedValue(first),
    get: vi.fn().mockResolvedValue(first),
    messagesBefore: vi.fn().mockResolvedValue(page("latest")),
    messageWindow: vi.fn().mockResolvedValue({ messages: [], next_cursor: 0 }),
    postMessage: vi.fn().mockResolvedValue({ id: "sent", seq: 2, body: "hi" }),
    messageReceipt: vi.fn().mockResolvedValue({ message: null }),
  };
  const api = {
    me: {
      get: async () => ({ id: "u1" }),
      organizations: async () => [{ id: "o1" }],
    },
    spaces: { list: async () => [{ id: "s1" }, { id: "s2" }] },
    spaceAgents: { list: async () => [{ id: "a", allow_direct_chat: true }] },
    conversations,
  } as unknown as Api;
  const { result } = renderHook(() => useChat(api));
  return { result, conversations };
}

describe("侧边栏会话边界", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("切换空间只显示该空间的会话，过期的历史请求不会写入新会话", async () => {
    const { result, conversations } = setup();
    await waitFor(() => expect(result.current.agentId).toBe("a"));
    expect(result.current.conversations).toEqual([first]);
    let finish!: (value: ReturnType<typeof page>) => void;
    conversations.messagesBefore.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        }),
    );
    act(() => {
      void result.current.loadHistory(first);
    });
    act(() => {
      result.current.selectSpace("s2");
    });
    expect(result.current.conversations).toEqual([second]);
    await act(() => result.current.loadHistory(second));
    await act(async () => {
      finish(page("stale"));
    });
    expect(result.current.messages.map((m) => m.id)).toEqual(["latest"]);
  });

  it("引用读取期间锁住目标与发送，重试复用已经采集的页面和消息 ID", async () => {
    const { result, conversations } = setup();
    await waitFor(() => expect(result.current.agentId).toBe("a"));
    let resolvePage!: (value: unknown) => void;
    const executeScript = vi.fn(
      () =>
        new Promise((resolve) => {
          resolvePage = resolve;
        }),
    );
    vi.stubGlobal("chrome", {
      tabs: { query: async () => [{ id: 7 }] },
      scripting: { executeScript },
    });
    conversations.postMessage.mockRejectedValueOnce(
      new ApiError({ status: 0, code: "TIMEOUT", message: "timeout" }),
    );
    let sending!: Promise<boolean>;
    await act(async () => {
      sending = result.current.send("整理", true);
      result.current.selectSpace("s2");
      expect(await result.current.send("整理", true)).toBe(false);
    });
    await act(async () => {
      resolvePage([
        {
          result: {
            url: "https://example.com/doc?private=yes",
            title: "记录",
            selection: "原始选区",
          },
        },
      ]);
      expect(await sending).toBe(false);
    });
    expect(result.current.spaceId).toBe("s1");
    await act(async () => {
      expect(await result.current.send("整理", true)).toBe(true);
    });
    expect(executeScript).toHaveBeenCalledTimes(1);
    expect(conversations.create).toHaveBeenCalledTimes(1);
    const [a, b] = conversations.postMessage.mock.calls;
    expect(a).toEqual(b);
    expect(a?.[2].body).toContain("原始选区");
    expect(a?.[2].body).not.toContain("private=yes");
  });

  it("无法读取显式引用时保留草稿，关闭的会话拒绝继续发送", async () => {
    const { result, conversations } = setup();
    await waitFor(() => expect(result.current.agentId).toBe("a"));
    vi.stubGlobal("chrome", { tabs: { query: async () => [] } });
    await act(async () => {
      expect(await result.current.send("hi", true)).toBe(false);
    });
    expect(result.current.error).toMatch(/无法读取/);
    expect(conversations.create).not.toHaveBeenCalled();
    await act(() => result.current.loadHistory(first));
    conversations.postMessage.mockRejectedValueOnce(
      new ApiError({
        status: 409,
        code: "CONVERSATION_CLOSED",
        message: "closed",
      }),
    );
    await act(async () => {
      expect(await result.current.send("hi", false)).toBe(false);
    });
    expect(result.current.active?.lifecycle).toBe("COMPLETED");
    await act(async () => {
      expect(await result.current.send("hi", false)).toBe(false);
    });
    expect(conversations.postMessage).toHaveBeenCalledTimes(1);
  });
});
