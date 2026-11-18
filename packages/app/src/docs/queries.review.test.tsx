import { act, renderHook } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { ApiError } from "@openfox/api";
import { useDebouncedSave } from "./queries";
afterEach(() => vi.useRealTimers());
it("未保存或正在保存时阻止关闭，pagehide 发起 flush", async () => {
  vi.useFakeTimers();
  let done!: () => void;
  const save = vi.fn(
    () =>
      new Promise<void>((resolve) => {
        done = resolve;
      }),
  );
  const h = renderHook(() => useDebouncedSave<{ body: string }>(save));
  act(() => h.result.current.schedule({ body: "最后一段" }));
  const unload = new Event("beforeunload", { cancelable: true });
  window.dispatchEvent(unload);
  expect(unload.defaultPrevented).toBe(true);
  await act(async () => window.dispatchEvent(new Event("pagehide")));
  expect(save).toHaveBeenCalledOnce();
  const busy = new Event("beforeunload", { cancelable: true });
  window.dispatchEvent(busy);
  expect(busy.defaultPrevented).toBe(true);
  await act(async () => done());
  const clean = new Event("beforeunload", { cancelable: true });
  window.dispatchEvent(clean);
  expect(clean.defaultPrevented).toBe(false);
  h.unmount();
});
it("暂时失败自动有限重试，冲突不自动重试", async () => {
  vi.useFakeTimers();
  const save = vi.fn().mockRejectedValue(new Error("network"));
  const h = renderHook(() => useDebouncedSave<{ body: string }>(save));
  act(() => h.result.current.schedule({ body: "draft" }));
  await act(async () => vi.advanceTimersByTimeAsync(30000));
  expect(save).toHaveBeenCalledTimes(4);
  await act(async () => vi.advanceTimersByTimeAsync(30000));
  expect(save).toHaveBeenCalledTimes(4);
  save.mockRejectedValue(
    new ApiError({
      status: 409,
      code: "VERSION_CONFLICT",
      message: "conflict",
    }),
  );
  act(() => h.result.current.schedule({ body: "next" }));
  await act(async () => vi.advanceTimersByTimeAsync(30000));
  expect(save).toHaveBeenCalledTimes(5);
  h.unmount();
});
