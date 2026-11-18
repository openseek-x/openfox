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
  let source: EventSource | undefined;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let stopped = false;
  let attempts = 0;
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

  const detach = () => {
    if (!source) return;
    source.onopen = null;
    source.onerror = null;
    for (const type of types) source.removeEventListener(type, listener);
    source.close();
  };
  const retry = () => {
    if (stopped || timer !== undefined) return;
    const wait = Math.min(30_000, 1000 * 2 ** Math.min(attempts++, 5));
    timer = setTimeout(
      () => {
        timer = undefined;
        connect();
      },
      wait * (0.8 + Math.random() * 0.4),
    );
  };
  const disconnect = () => {
    if (!disconnected) handlers.onDisconnect?.();
    disconnected = true;
  };
  const connect = () => {
    if (stopped) return;
    try {
      source = new EventSource(url, { withCredentials: true });
    } catch {
      disconnect();
      retry();
      return;
    }
    for (const type of types) source.addEventListener(type, listener);
    source.onopen = () => {
      if (stopped) return;
      if (disconnected) handlers.onReconnect?.();
      else if (!opened) handlers.onFirstOpen?.();
      opened = true;
      disconnected = false;
      attempts = 0;
    };
    source.onerror = () => {
      if (stopped) return;
      disconnect();
      if (source?.readyState === EventSource.CLOSED) {
        detach();
        retry();
      }
    };
  };
  connect();
  return () => {
    stopped = true;
    clearTimeout(timer);
    detach();
  };
}
