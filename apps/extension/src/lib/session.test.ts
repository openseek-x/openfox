import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { watchBrowserSession } from "./session";

const origin = "https://work.example.com";
const get = vi.fn();
const fetchMock = vi.fn();
const publish = vi.fn();
let watcher: ReturnType<typeof watchBrowserSession> | undefined;
beforeEach(() => {
  vi.useFakeTimers();
  vi.resetAllMocks();
  vi.stubGlobal("chrome", { cookies: { get } });
  vi.stubGlobal("fetch", fetchMock);
  get.mockResolvedValue(null);
  fetchMock.mockImplementation(async () => Response.json({ id: "fixture" }));
});
afterEach(() => {
  watcher?.stop();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});
it("先打开面板，网页登录、换号和登出自动反映到侧栏", async () => {
  watcher = watchBrowserSession(origin, publish);
  await vi.advanceTimersByTimeAsync(0);
  expect(publish).toHaveBeenLastCalledWith({ kind: "signed-out" });
  expect(fetchMock).not.toHaveBeenCalled();
  get.mockResolvedValue({ value: "a" });
  await vi.advanceTimersByTimeAsync(2000);
  const first = publish.mock.lastCall?.[0];
  expect(first.kind).toBe("ready");
  get.mockResolvedValue({ value: "b" });
  await vi.advanceTimersByTimeAsync(2000);
  const second = publish.mock.lastCall?.[0];
  expect(second.kind).toBe("ready");
  expect(second.api).not.toBe(first.api);
  get.mockResolvedValue(null);
  await vi.advanceTimersByTimeAsync(2000);
  expect(publish).toHaveBeenLastCalledWith({ kind: "signed-out" });
});
it("已有会话立即认证，重新聚焦时同步服务端吊销", async () => {
  get.mockResolvedValue({ value: "a" });
  watcher = watchBrowserSession(origin, publish);
  await vi.advanceTimersByTimeAsync(0);
  expect(publish.mock.lastCall?.[0].kind).toBe("ready");
  fetchMock.mockImplementation(async () => Response.json({}, { status: 401 }));
  window.dispatchEvent(new Event("focus"));
  await vi.advanceTimersByTimeAsync(0);
  expect(publish).toHaveBeenLastCalledWith({ kind: "signed-out" });
});
it("卸载后到达的认证响应不会恢复账号", async () => {
  let finish!: (response: Response) => void;
  get.mockResolvedValue({ value: "a" });
  fetchMock.mockImplementation(
    () =>
      new Promise<Response>((resolve) => {
        finish = resolve;
      }),
  );
  watcher = watchBrowserSession(origin, publish);
  await vi.advanceTimersByTimeAsync(0);
  watcher.stop();
  finish(Response.json({ id: "late" }));
  await vi.advanceTimersByTimeAsync(0);
  expect(publish).not.toHaveBeenCalled();
});
