import { afterEach, expect, it, vi } from "vitest";
import { subscribeEvents } from "./events";
class Stream {
  static CLOSED = 2;
  static all: Stream[] = [];
  readyState = 0;
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  addEventListener = vi.fn();
  removeEventListener = vi.fn();
  close = vi.fn();
  constructor() {
    Stream.all.push(this);
  }
}
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  Stream.all = [];
});
it("CLOSED 后退避重建，成功通知恢复，取消后停止重建", async () => {
  vi.useFakeTimers();
  vi.stubGlobal("EventSource", Stream);
  vi.spyOn(Math, "random").mockReturnValue(0.5);
  const reconnect = vi.fn(),
    disconnect = vi.fn();
  const close = subscribeEvents("/events", ["changed"], {
    onEvent: vi.fn(),
    onReconnect: reconnect,
    onDisconnect: disconnect,
  });
  Stream.all[0]!.onerror?.();
  Stream.all[0]!.readyState = 2;
  Stream.all[0]!.onerror?.();
  await vi.advanceTimersByTimeAsync(2000);
  expect(Stream.all).toHaveLength(2);
  Stream.all[1]!.onopen?.();
  expect(reconnect).toHaveBeenCalledOnce();
  expect(disconnect).toHaveBeenCalledOnce();
  Stream.all[1]!.readyState = 2;
  Stream.all[1]!.onerror?.();
  close();
  await vi.advanceTimersByTimeAsync(60000);
  expect(Stream.all).toHaveLength(2);
});
