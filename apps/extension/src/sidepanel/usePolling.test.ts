import { act, renderHook } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import type { Api } from "@openfox/api";
import { usePolling } from "./usePolling";
import { POLL_MIN_MS } from "../lib/poll";

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

it("隐藏时暂停，恢复时复用在途请求，卸载后停止读取", async () => {
  vi.useFakeTimers();
  const hidden = vi.spyOn(document, "hidden", "get").mockReturnValue(false);
  let finish!: (value: unknown) => void;
  const messageWindow = vi.fn(
    () =>
      new Promise((resolve) => {
        finish = resolve;
      }),
  );
  const api = { conversations: { messageWindow } } as unknown as Api;
  const options = {
    api,
    orgId: "o",
    conversationId: "c",
    cursorRef: { current: 0 },
    onMessages: vi.fn(),
    onError: vi.fn(),
  };
  const { unmount } = renderHook(() => usePolling(options));
  const visibility = (value: boolean) => {
    hidden.mockReturnValue(value);
    document.dispatchEvent(new Event("visibilitychange"));
  };
  await act(() => vi.advanceTimersByTimeAsync(POLL_MIN_MS));
  expect(messageWindow).toHaveBeenCalledTimes(1);
  visibility(true);
  visibility(false);
  await act(() => vi.advanceTimersByTimeAsync(50));
  expect(messageWindow).toHaveBeenCalledTimes(1);
  visibility(true);
  await act(async () => {
    finish({ messages: [{ id: "m", seq: 2 }], next_cursor: 0 });
  });
  await act(() => vi.advanceTimersByTimeAsync(30000));
  expect(messageWindow).toHaveBeenCalledTimes(1);
  visibility(false);
  await act(() => vi.advanceTimersByTimeAsync(1));
  expect(messageWindow).toHaveBeenLastCalledWith("o", "c", { after: 2 });
  unmount();
  await act(async () => {
    finish({ messages: [{ id: "old", seq: 3 }], next_cursor: 0 });
  });
  await vi.advanceTimersByTimeAsync(30000);
  expect(options.onMessages).toHaveBeenCalledTimes(1);
  expect(messageWindow).toHaveBeenCalledTimes(2);
});
