import { ApiError } from "./errors";

const MAX_RESPONSE_BYTES = 2 * 1024 * 1024;
const CSRF_COOKIE = "openfox_csrf";

/** 数组取值展开为重复参数（status_id=a&status_id=b）。 */
export type QueryValue =
  string | number | boolean | undefined | null | ReadonlyArray<string | number>;

export interface RequestOptions {
  method?: "GET" | "POST" | "PUT" | "PATCH" | "DELETE";
  body?: unknown;
  query?: Record<string, QueryValue>;
  /** 创建类接口的幂等键 */
  idempotencyKey?: string;
  signal?: AbortSignal;
  timeoutMs?: number;
}

function readCookie(name: string): string | undefined {
  if (typeof document === "undefined") return undefined;
  const prefix = `${name}=`;
  const item = document.cookie
    .split(";")
    .map((v) => v.trim())
    .find((v) => v.startsWith(prefix));
  if (!item) return undefined;
  try {
    return decodeURIComponent(item.slice(prefix.length));
  } catch {
    return undefined;
  }
}

/** Go 以 int64 输出游标，超过 JS 安全整数会丢精度；解析前把 next_cursor 的数字转成字符串。 */
export function parseJson(raw: string): unknown {
  return JSON.parse(
    raw.replace(
      /"next_cursor"\s*:\s*(-?\d+)(?=\s*[,}])|"(?:\\.|[^"\\])*"/g,
      (token, cursor: string | undefined) =>
        cursor === undefined ? token : `"next_cursor":"${cursor}"`,
    ),
  ) as unknown;
}

/** 生成幂等键（32 位十六进制，满足后端 8–128 位字母数字的要求）。 */
export function newIdempotencyKey(): string {
  return crypto.randomUUID().replaceAll("-", "");
}

async function readBody(response: Response): Promise<string> {
  const reader = response.body?.getReader();
  if (!reader) return "";
  const decoder = new TextDecoder();
  let raw = "";
  let bytes = 0;
  try {
    for (;;) {
      const chunk = await reader.read();
      if (chunk.done) break;
      bytes += chunk.value.byteLength;
      if (bytes > MAX_RESPONSE_BYTES) {
        await reader.cancel();
        throw new ApiError({
          status: 502,
          code: "INVALID_RESPONSE",
          message: "响应超过客户端上限",
        });
      }
      raw += decoder.decode(chunk.value, { stream: true });
    }
    return raw + decoder.decode();
  } finally {
    reader.releaseLock();
  }
}

/**
 * 同源 API 客户端：带会话 cookie，写请求自动附加 CSRF 头，统一错误结构，带超时与响应体大小上限。
 * 只允许访问 /v1/ 路径，避免被拼接成任意地址。
 */
export class HttpClient {
  constructor(
    private readonly baseUrl = "/api",
    private readonly transport: typeof fetch = (...args) =>
      globalThis.fetch(...args),
    private readonly defaultTimeoutMs = 15000,
  ) {
    if (!/^\/(?!\/)[a-zA-Z0-9/_-]*$/.test(baseUrl))
      throw new Error("API 地址必须是同源路径");
  }

  url(path: string, query?: Record<string, QueryValue>): string {
    if (!path.startsWith("/v1/")) throw new Error("只允许访问 /v1/ 接口");
    const search = new URLSearchParams();
    for (const [key, value] of Object.entries(query ?? {})) {
      if (Array.isArray(value)) {
        for (const item of value as ReadonlyArray<string | number>)
          if (item !== "") search.append(key, String(item));
      } else if (value !== undefined && value !== null && value !== "")
        search.set(key, String(value));
    }
    const qs = search.toString();
    return `${this.baseUrl}${path}${qs ? `?${qs}` : ""}`;
  }

  /** 取二进制响应（图片预览用）：同样走同源、超时与大小上限；失败抛 ApiError。 */
  async requestBlob(
    path: string,
    options: {
      query?: Record<string, QueryValue>;
      maxBytes: number;
      signal?: AbortSignal;
    },
  ): Promise<Blob> {
    const timeout = new AbortController();
    const timer = setTimeout(() => timeout.abort(), this.defaultTimeoutMs * 2);
    const signal = options.signal
      ? AbortSignal.any([timeout.signal, options.signal])
      : timeout.signal;
    try {
      const response = await this.transport(this.url(path, options.query), {
        method: "GET",
        headers: new Headers({ "X-Request-ID": crypto.randomUUID() }),
        credentials: "same-origin",
        redirect: "error",
        signal,
      });
      if (!response.ok)
        throw new ApiError({
          status: response.status,
          code: "HTTP_ERROR",
          message: `请求失败（${response.status}）`,
        });
      const blob = await response.blob();
      if (blob.size > options.maxBytes)
        throw new ApiError({
          status: 502,
          code: "INVALID_RESPONSE",
          message: "文件超过预览上限",
        });
      return blob;
    } catch (error) {
      if (error instanceof ApiError) throw error;
      if (options.signal?.aborted) throw error;
      throw new ApiError({
        status: 0,
        code: timeout.signal.aborted ? "TIMEOUT" : "NETWORK_ERROR",
        message: "网络连接失败",
      });
    } finally {
      clearTimeout(timer);
    }
  }

  async request<T>(path: string, options: RequestOptions = {}): Promise<T> {
    const method = options.method ?? "GET";
    const timeout = new AbortController();
    const timer = setTimeout(
      () => timeout.abort(),
      options.timeoutMs ?? this.defaultTimeoutMs,
    );
    const signal = options.signal
      ? AbortSignal.any([timeout.signal, options.signal])
      : timeout.signal;
    const headers = new Headers({
      Accept: "application/json",
      "X-Request-ID": crypto.randomUUID(),
    });
    // FormData 由浏览器带 boundary 设置 Content-Type
    const form = options.body instanceof FormData;
    if (options.body !== undefined && !form)
      headers.set("Content-Type", "application/json");
    if (method !== "GET") {
      const csrf = readCookie(CSRF_COOKIE);
      if (csrf) headers.set("X-CSRF-Token", csrf);
    }
    if (options.idempotencyKey)
      headers.set("Idempotency-Key", options.idempotencyKey);

    try {
      const response = await this.transport(this.url(path, options.query), {
        method,
        headers,
        body:
          options.body === undefined
            ? undefined
            : form
              ? (options.body as FormData)
              : JSON.stringify(options.body),
        credentials: "same-origin",
        redirect: "error",
        signal,
      });
      if (response.status === 204) return undefined as T;
      const raw = await readBody(response);
      let payload: unknown;
      try {
        payload = raw ? parseJson(raw) : undefined;
      } catch {
        payload = undefined;
      }
      if (!response.ok) {
        const err = (
          payload as
            | {
                error?: {
                  code?: string;
                  message?: string;
                  request_id?: string;
                  details?: Record<string, unknown>;
                };
              }
            | undefined
        )?.error;
        const retry = Number(response.headers.get("Retry-After"));
        throw new ApiError({
          status: response.status,
          code: err?.code ?? "HTTP_ERROR",
          message: err?.message ?? `请求失败（${response.status}）`,
          requestId:
            err?.request_id ??
            response.headers.get("X-Request-ID") ??
            undefined,
          details: err?.details,
          retryAfter: Number.isFinite(retry) && retry > 0 ? retry : undefined,
        });
      }
      if (
        payload === undefined ||
        payload === null ||
        typeof payload !== "object"
      ) {
        throw new ApiError({
          status: 502,
          code: "INVALID_RESPONSE",
          message: "接口返回了无效的 JSON",
        });
      }
      return payload as T;
    } catch (error) {
      if (options.signal?.aborted) throw error;
      if (error instanceof ApiError) throw error;
      if (timeout.signal.aborted)
        throw new ApiError({
          status: 0,
          code: "TIMEOUT",
          message: "请求超时，写入可能已经生效",
        });
      throw new ApiError({
        status: 0,
        code: "NETWORK_ERROR",
        message: "网络连接失败，写入可能已经生效",
      });
    } finally {
      clearTimeout(timer);
    }
  }
}
