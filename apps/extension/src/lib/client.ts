// 扩展使用的 API 客户端：复用 @openfox/api 的 createApi 与契约类型，只替换传输层。
// 传输层把同源路径补成服务器地址并带上个人访问令牌；令牌只出现在请求头里，不进入 URL、日志或错误文案。
import { ApiError, HttpClient, createApi, type Api } from "@openfox/api";

import type { ConnectionSettings } from "./config";

/** 只允许访问服务器的 /api/v1/ 接口，避免传输层被用来请求任意地址。 */
const API_PREFIX = "/api/v1/";

export function createTokenTransport(
  settings: ConnectionSettings,
  transport: typeof fetch = (...args) => globalThis.fetch(...args),
): typeof fetch {
  return (input, init) => {
    if (typeof input !== "string" || !input.startsWith(API_PREFIX)) {
      return Promise.reject(new TypeError("只允许访问 /api/v1/ 接口"));
    }
    const headers = new Headers(init?.headers);
    headers.set("Authorization", `Bearer ${settings.token}`);
    return transport(`${settings.serverOrigin}${input}`, {
      ...init,
      headers,
      // 令牌认证不依赖 cookie，也不需要 CSRF 头；显式不携带凭据，避免混入浏览器里的登录态。
      credentials: "omit",
      redirect: "error",
    });
  };
}

export function createExtensionApi(
  settings: ConnectionSettings,
  transport?: typeof fetch,
): Api {
  return createApi(
    new HttpClient("/api", createTokenTransport(settings, transport)),
  );
}

/** 把接口错误转成界面文案；使用本地文案，避免服务端回显请求信息。 */
export function describeApiError(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.status === 401) {
      return "令牌无效、已过期或已被吊销，请在 OpenFox 里重新生成";
    }
    if (error.status === 403) return "没有权限执行该操作";
    if (error.status === 429) {
      return error.retryAfter
        ? `请求过于频繁，请 ${error.retryAfter} 秒后再试`
        : "请求过于频繁，请稍后再试";
    }
    if (error.code === "CONVERSATION_CLOSED") return "会话已关闭";
    if (error.code === "TIMEOUT") return "请求超时，请重试";
    if (error.code === "NETWORK_ERROR")
      return "无法连接服务器，请检查地址和网络后重试";
    if (error.status === 404) return "找不到该资源，或当前账号无法访问";
    return `请求失败（${error.status}），请重试`;
  }
  return "出现未知错误";
}
