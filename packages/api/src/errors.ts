/** 接口错误：status 为 HTTP 状态码（0 表示网络中断或超时），code 为后端稳定错误码。 */
export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly requestId?: string;
  readonly details?: Record<string, unknown>;
  /** 来自 Retry-After 的秒数（429 时有值） */
  readonly retryAfter?: number;

  constructor(init: {
    status: number;
    code: string;
    message: string;
    requestId?: string;
    details?: Record<string, unknown>;
    retryAfter?: number;
  }) {
    super(init.message);
    this.name = "ApiError";
    this.status = init.status;
    this.code = init.code;
    this.requestId = init.requestId;
    this.details = init.details;
    this.retryAfter = init.retryAfter;
  }

  /** 网络中断或超时：写请求可能已到达服务端，界面应提示刷新确认，而不是直接重试。 */
  get uncertain(): boolean {
    return this.status === 0;
  }
}

export function isApiError(value: unknown, code?: string): value is ApiError {
  return (
    value instanceof ApiError && (code === undefined || value.code === code)
  );
}
