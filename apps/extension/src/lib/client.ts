import { ApiError, HttpClient, createApi } from "@openfox/api";

/** 仅从已配置工作台读取普通 CSRF 标记。HttpOnly 登录凭据始终由 Chrome 管理。 */
export async function readCsrf(origin: string): Promise<string | null> {
  const cookie = await chrome.cookies.get({
    url: origin + "/",
    name: "openfox_csrf",
  });
  return cookie?.value || null;
}

export function createSessionApi(
  origin: string,
  csrf: string,
  onInvalid: () => void,
) {
  async function requireSameLogin() {
    if ((await readCsrf(origin)) !== csrf) {
      onInvalid();
      throw new ApiError({
        status: 401,
        code: "UNAUTHENTICATED",
        message: "登录状态已更改",
      });
    }
  }
  const transport: typeof fetch = async (input, init) => {
    if (typeof input !== "string" || !input.startsWith("/api/v1/"))
      throw new Error("请求必须指向工作台 API");
    const url = new URL(input, origin);
    if (url.origin !== origin || !url.pathname.startsWith("/api/v1/"))
      throw new Error("请求超出工作台 API 范围");
    // 旧草稿与重试使用原账号的标记；换号后取消请求，不能向新账号提交。
    await requireSameLogin();
    const headers = new Headers(init?.headers);
    if ((init?.method ?? "GET") !== "GET") headers.set("X-CSRF-Token", csrf);
    const response = await fetch(url.href, {
      ...init,
      headers,
      credentials: "include",
      redirect: "error",
      cache: "no-store",
    });
    if (response.status === 401) onInvalid();
    else await requireSameLogin();
    return response;
  };
  return createApi(new HttpClient("/api", transport));
}

/** 把接口错误转成界面文案；使用本地文案，避免服务端回显请求信息。 */
export function describeApiError(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.status === 401) {
      return "登录已失效，请在当前浏览器重新登录 OpenFox";
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
      return "无法连接服务器，请检查网络后重试";
    if (error.status === 404) return "找不到该资源，或当前账号无法访问";
    return `请求失败（${error.status}），请重试`;
  }
  return "出现未知错误";
}
