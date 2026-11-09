import { parseJson } from "./http";

export interface StreamHandlers<T> {
  onEvent: (type: string, data: T) => void;
  onFirstOpen?: () => void;
  /** 连接断开；EventSource 会自动重连，重连后调用方应按游标补齐或刷新缓存 */
  onDisconnect?: () => void;
  onReconnect?: () => void;
}

/**
 * 订阅 SSE 事件流，返回关闭函数。
 * 事件类型由服务端定义（如 run.event），调用方按类型更新或失效查询缓存。
 */
export function subscribeEvents<T = unknown>(
  url: string,
  types: readonly string[],
  handlers: StreamHandlers<T>,
): () => void {
  const source = new EventSource(url, { withCredentials: true });
  let disconnected = false;
  let opened = false;
  const listener = (event: Event) => {
    try {
      handlers.onEvent(
        event.type,
        parseJson((event as MessageEvent<string>).data) as T,
      );
    } catch {
      // 单条事件解析失败不影响后续事件
    }
  };
  for (const type of types) source.addEventListener(type, listener);
  source.onopen = () => {
    if (disconnected) handlers.onReconnect?.();
    else if (!opened) handlers.onFirstOpen?.();
    opened = true;
    disconnected = false;
  };
  source.onerror = () => {
    if (disconnected) return;
    disconnected = true;
    handlers.onDisconnect?.();
  };
  return () => source.close();
}
