import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "@openfox/api";
import { createSessionApi, describeApiError } from "./client";

const origin = "https://work.example.com";
const get = vi.fn();
const request = vi.fn();
const invalid = vi.fn();

beforeEach(() => {
  vi.resetAllMocks();
  get.mockResolvedValue({ value: "fixture-csrf" });
  request.mockResolvedValue(Response.json({ id: "me" }));
  vi.stubGlobal("chrome", { cookies: { get } });
  vi.stubGlobal("fetch", request);
});
afterEach(() => vi.unstubAllGlobals());

describe("工作台会话传输", () => {
  it("访问固定工作台并交给浏览器附带会话", async () => {
    await createSessionApi(origin, "fixture-csrf", invalid).me.get();
    const [url, init] = request.mock.calls[0]!;
    expect(url).toBe(origin + "/api/v1/me");
    expect(init.credentials).toBe("include");
    expect(init.redirect).toBe("error");
    expect(init.headers.has("Authorization")).toBe(false);
    expect(init.headers.has("X-CSRF-Token")).toBe(false);
    expect(get).toHaveBeenCalledWith({
      url: origin + "/",
      name: "openfox_csrf",
    });
  });
  it("写请求带上已核验会话的 CSRF 标记", async () => {
    await createSessionApi(
      origin,
      "fixture-csrf",
      invalid,
    ).conversations.postMessage("org", "chat", {
      body: "你好",
      client_message_id: "m1",
    });
    expect(request.mock.calls[0]![1].headers.get("X-CSRF-Token")).toBe(
      "fixture-csrf",
    );
  });
  it.each([null, { value: "other" }])(
    "注销或切换账号时拒绝旧草稿",
    async (cookie) => {
      get.mockResolvedValue(cookie);
      await expect(
        createSessionApi(
          origin,
          "fixture-csrf",
          invalid,
        ).conversations.postMessage("org", "chat", {
          body: "旧草稿",
          client_message_id: "m1",
        }),
      ).rejects.toMatchObject({ status: 401 });
      expect(request).not.toHaveBeenCalled();
      expect(invalid).toHaveBeenCalledOnce();
    },
  );
  it("在途响应返回前换号时，丢弃旧视图的数据", async () => {
    get
      .mockResolvedValueOnce({ value: "fixture-csrf" })
      .mockResolvedValueOnce({ value: "other" });
    await expect(
      createSessionApi(origin, "fixture-csrf", invalid).me.get(),
    ).rejects.toMatchObject({ status: 401 });
    expect(request).toHaveBeenCalledOnce();
    expect(invalid).toHaveBeenCalledOnce();
  });
  it("服务端注销后清除面板身份", async () => {
    request.mockResolvedValue(Response.json({}, { status: 401 }));
    await expect(
      createSessionApi(origin, "fixture-csrf", invalid).me.get(),
    ).rejects.toMatchObject({ status: 401 });
    expect(invalid).toHaveBeenCalledOnce();
  });
  it("拒绝越出 API 的路径", async () => {
    await expect(
      createSessionApi(origin, "fixture-csrf", invalid).http.request(
        "/v1/../../other",
      ),
    ).rejects.toThrow();
    expect(request).not.toHaveBeenCalled();
  });
});

describe("本地错误提示", () => {
  const err = (status: number, retryAfter?: number) =>
    new ApiError({ status, code: "X", message: "fixture-private", retryAfter });
  it("失效与权限问题提供不同提示", () => {
    expect(describeApiError(err(401))).toContain("重新登录");
    expect(describeApiError(err(403))).toContain("没有权限");
  });
  it("限流展示等待时间，未知错误不回显服务端原文", () => {
    expect(describeApiError(err(429, 7))).toContain("7 秒");
    expect(describeApiError(err(400))).not.toContain("fixture-private");
    expect(describeApiError(new Error("fixture-private"))).not.toContain(
      "fixture-private",
    );
  });
});
