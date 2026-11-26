import { describe, expect, it } from "vitest";
import { ApiError, isApiError } from "@openfox/api";

import {
  createExtensionApi,
  createTokenTransport,
  describeApiError,
} from "./client";

const settings = {
  serverOrigin: "https://openfox.example.com",
  token: "ofx_pat_TESTTOKEN0000000000000000000000000000000",
};

interface Call {
  url: string;
  init?: RequestInit;
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function recordingTransport(response: () => Response) {
  const calls: Call[] = [];
  const transport: typeof fetch = async (input, init) => {
    calls.push({ url: String(input), init });
    return response();
  };
  return { calls, transport };
}

function firstCall(calls: Call[]): Call {
  const call = calls[0];
  if (!call) throw new Error("没有记录到请求");
  return call;
}

describe("createTokenTransport", () => {
  it("补全服务器地址并带上 Bearer 令牌，不携带 cookie", async () => {
    const { calls, transport } = recordingTransport(() => jsonResponse({}));
    await createTokenTransport(settings, transport)("/api/v1/me", {
      method: "GET",
    });
    expect(calls).toHaveLength(1);
    expect(firstCall(calls).url).toBe("https://openfox.example.com/api/v1/me");
    const headers = firstCall(calls).init?.headers as Headers;
    expect(headers.get("Authorization")).toBe(`Bearer ${settings.token}`);
    expect(firstCall(calls).init?.credentials).toBe("omit");
  });

  it("令牌不会出现在请求地址里", async () => {
    const { calls, transport } = recordingTransport(() => jsonResponse({}));
    await createTokenTransport(settings, transport)("/api/v1/me?limit=5");
    expect(firstCall(calls).url).not.toContain(settings.token);
  });

  it("拒绝 /api/v1/ 之外的路径与绝对地址，且不发出请求", async () => {
    const { calls, transport } = recordingTransport(() => jsonResponse({}));
    const guarded = createTokenTransport(settings, transport);
    await expect(guarded("/other")).rejects.toThrow();
    await expect(
      guarded("https://evil.example.com/api/v1/me"),
    ).rejects.toThrow();
    expect(calls).toHaveLength(0);
  });
});

describe("createExtensionApi", () => {
  it("通过令牌传输层请求当前用户", async () => {
    const me = { id: "u1", email: "a@example.com", full_name: "A" };
    const { calls, transport } = recordingTransport(() => jsonResponse(me));
    const api = createExtensionApi(settings, transport);
    const result = await api.me.get();
    expect(result).toMatchObject({ id: "u1" });
    expect(firstCall(calls).url).toBe("https://openfox.example.com/api/v1/me");
  });

  it("令牌无效时抛出 401 错误，界面文案不含令牌", async () => {
    const { transport } = recordingTransport(() =>
      jsonResponse({ error: { code: "UNAUTHORIZED", message: "denied" } }, 401),
    );
    const api = createExtensionApi(settings, transport);
    const error = await api.me.get().catch((e: unknown) => e);
    expect(isApiError(error)).toBe(true);
    expect((error as ApiError).status).toBe(401);
    const text = describeApiError(error);
    expect(text).toContain("令牌无效");
    expect(text).not.toContain(settings.token);
  });

  it("网络中断时错误文案不含令牌", async () => {
    const transport: typeof fetch = async () => {
      throw new Error(`连接失败 ${settings.token}`);
    };
    const api = createExtensionApi(settings, transport);
    const error = await api.me.get().catch((e: unknown) => e);
    expect(isApiError(error)).toBe(true);
    expect((error as ApiError).uncertain).toBe(true);
    expect(describeApiError(error)).not.toContain(settings.token);
    expect((error as ApiError).message).not.toContain(settings.token);
  });
});

describe("describeApiError", () => {
  const make = (status: number, retryAfter?: number, message = "服务端信息") =>
    new ApiError({ status, code: "X", message, retryAfter });

  it("403 提示没有权限", () => {
    expect(describeApiError(make(403))).toBe("没有权限执行该操作");
  });

  it("429 带等待秒数时写出秒数", () => {
    expect(describeApiError(make(429, 7))).toContain("7 秒");
    expect(describeApiError(make(429))).toContain("稍后再试");
  });

  it("其他状态使用本地文案，服务端回显的凭据不会显示", () => {
    expect(
      describeApiError(make(400, undefined, settings.token)),
    ).not.toContain(settings.token);
  });

  it("非接口错误返回通用文案，不透传原始内容", () => {
    expect(describeApiError(new Error(settings.token))).toBe("出现未知错误");
  });
});
