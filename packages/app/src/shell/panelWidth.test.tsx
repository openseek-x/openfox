import { act, fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { PanelResizeHandle } from "./PanelResizeHandle";
import { usePanelWidth } from "./panelWidth";

let available = 1000;
let resize = () => {};
const disconnect = vi.fn();

beforeEach(() => {
  available = 1000;
  localStorage.clear();
  vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockImplementation(
    () => available,
  );
  vi.stubGlobal(
    "ResizeObserver",
    class {
      constructor(callback: () => void) {
        resize = callback;
      }
      observe() {}
      disconnect = disconnect;
    },
  );
});
afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  localStorage.clear();
});

function Panel({
  side = "left",
  onCollapse = vi.fn(),
  collapsible = false,
}: {
  side?: "left" | "right";
  onCollapse?: () => void;
  collapsible?: boolean;
}) {
  const [collapsed, setCollapsed] = useState(false);
  const sizing = usePanelWidth({
    storageKey: "test:panel-width",
    initial: 260,
    min: 220,
    max: 480,
    remaining: 400,
  });
  return (
    <div ref={sizing.containerRef}>
      {!collapsed && <nav id="test-panel" />}
      <PanelResizeHandle
        {...sizing}
        label="Resize"
        controls="test-panel"
        side={side}
        collapsed={collapsed}
        onExpand={collapsible ? () => setCollapsed(false) : undefined}
        onCollapse={() => {
          onCollapse();
          if (collapsible) setCollapsed(true);
        }}
        className=""
      />
    </div>
  );
}

function pointerFrames(handle: HTMLElement) {
  Object.defineProperties(handle, {
    setPointerCapture: { value: vi.fn() },
    releasePointerCapture: { value: vi.fn() },
  });
  const pending = new Map<number, FrameRequestCallback>();
  let nextFrame = 0;
  const request = vi.fn((callback: FrameRequestCallback) => {
    pending.set(++nextFrame, callback);
    return nextFrame;
  });
  const cancel = vi.fn((frameId: number) => pending.delete(frameId));
  vi.stubGlobal("requestAnimationFrame", request);
  vi.stubGlobal("cancelAnimationFrame", cancel);
  const flush = () =>
    act(() => {
      const callbacks = [...pending.values()];
      pending.clear();
      callbacks.forEach((callback) => callback(0));
    });
  return { request, cancel, flush };
}

describe("panel resizing", () => {
  it("starts a new drag from the displayed width during a settling animation", () => {
    render(<Panel collapsible />);
    vi.spyOn(
      screen.getByRole("navigation"),
      "getBoundingClientRect",
    ).mockReturnValue({ width: 120 } as DOMRect);
    const handle = screen.getByRole("separator");
    const frames = pointerFrames(handle);
    fireEvent.pointerDown(handle, { pointerId: 1, button: 0, clientX: 300 });
    expect(handle).toHaveAttribute("aria-valuenow", "120");
    fireEvent.pointerMove(handle, { pointerId: 1, clientX: 280 });
    frames.flush();
    expect(handle).toHaveAttribute("aria-valuenow", "100");
    fireEvent.pointerUp(handle, { pointerId: 1, clientX: 280 });
    expect(handle).toHaveAttribute("aria-valuenow", "0");
    fireEvent.keyDown(handle, { key: "Enter" });
    expect(handle).toHaveAttribute("aria-valuenow", "260");
  });

  it("follows the pointer through the minimum to zero and back without saving previews", () => {
    render(<Panel collapsible />);
    const handle = screen.getByRole("separator");
    const frames = pointerFrames(handle);
    const save = vi.spyOn(Storage.prototype, "setItem");
    fireEvent.pointerDown(handle, { pointerId: 1, button: 0, clientX: 300 });
    for (const width of [
      240, 220, 200, 160, 110, 100, 40, 0, 40, 100, 160, 220, 260,
    ]) {
      fireEvent.pointerMove(handle, { pointerId: 1, clientX: width + 40 });
      frames.flush();
      expect(handle).toHaveAttribute("aria-valuenow", String(width));
    }
    expect(save).not.toHaveBeenCalled();
    fireEvent.pointerUp(handle, { pointerId: 1, clientX: 300 });
    expect(save).toHaveBeenCalledExactlyOnceWith("test:panel-width", "260");
  });

  it("coalesces pointer moves into one frame and flushes the final pointer position", () => {
    render(<Panel collapsible />);
    const handle = screen.getByRole("separator");
    const frames = pointerFrames(handle);
    fireEvent.pointerDown(handle, { pointerId: 1, button: 0, clientX: 300 });
    for (const clientX of [320, 340, 360])
      fireEvent.pointerMove(handle, { pointerId: 1, clientX });
    expect(frames.request).toHaveBeenCalledOnce();
    expect(handle).toHaveAttribute("aria-valuenow", "260");
    frames.flush();
    expect(handle).toHaveAttribute("aria-valuenow", "320");
    fireEvent.pointerMove(handle, { pointerId: 1, clientX: 380 });
    fireEvent.pointerUp(handle, { pointerId: 1, clientX: 400 });
    expect(handle).toHaveAttribute("aria-valuenow", "360");
    expect(localStorage.getItem("test:panel-width")).toBe("360");
    frames.flush();
    expect(handle).toHaveAttribute("aria-valuenow", "360");
  });

  it.each([40, 160])("settles a %spx preview only on release", (width) => {
    localStorage.setItem("test:panel-width", "340");
    render(<Panel collapsible />);
    const handle = screen.getByRole("separator");
    const frames = pointerFrames(handle);
    fireEvent.pointerDown(handle, { pointerId: 1, button: 0, clientX: 340 });
    fireEvent.pointerMove(handle, { pointerId: 1, clientX: width });
    frames.flush();
    expect(handle).toHaveAttribute("aria-valuenow", String(width));
    expect(localStorage.getItem("test:panel-width")).toBe("340");
    fireEvent.pointerUp(handle, { pointerId: 1, clientX: width });
    expect(handle).toHaveAttribute("aria-valuenow", width < 110 ? "0" : "220");
    expect(localStorage.getItem("test:panel-width")).toBe(
      width < 110 ? "340" : "220",
    );
    if (width < 110) {
      fireEvent.keyDown(handle, { key: "Enter" });
      expect(handle).toHaveAttribute("aria-valuenow", "340");
    }
  });

  it.each(["pointerCancel", "lostPointerCapture"] as const)(
    "%s restores the original collapsed state and width",
    (eventName) => {
      render(<Panel collapsible />);
      const handle = screen.getByRole("separator");
      const frames = pointerFrames(handle);
      fireEvent.keyDown(handle, { key: "Home" });
      fireEvent.pointerDown(handle, { pointerId: 1, button: 0, clientX: 0 });
      fireEvent.pointerMove(handle, { pointerId: 1, clientX: 40 });
      frames.flush();
      expect(handle).toHaveAttribute("aria-valuenow", "40");
      fireEvent.pointerMove(handle, { pointerId: 1, clientX: 100 });
      fireEvent[eventName](handle, { pointerId: 1 });
      frames.flush();
      expect(handle).toHaveAttribute("aria-valuenow", "0");
      expect(localStorage.getItem("test:panel-width")).toBeNull();
      fireEvent.keyDown(handle, { key: "Enter" });
      expect(handle).toHaveAttribute("aria-valuenow", "260");
    },
  );

  it("cancels scheduled resize work on unmount", () => {
    const mounted = render(<Panel collapsible />);
    const handle = screen.getByRole("separator");
    const frames = pointerFrames(handle);
    fireEvent.pointerDown(handle, { pointerId: 1, button: 0, clientX: 300 });
    fireEvent.pointerMove(handle, { pointerId: 1, clientX: 320 });
    mounted.unmount();
    expect(frames.cancel).toHaveBeenCalledOnce();
    frames.flush();
    expect(localStorage.getItem("test:panel-width")).toBeNull();
  });

  it("collapses fully and restores its width through the keyboard", () => {
    render(<Panel collapsible />);
    const handle = screen.getByRole("separator");
    fireEvent.keyDown(handle, { key: "End" });
    fireEvent.keyDown(handle, { key: "Home" });
    expect(handle).toHaveAttribute("aria-valuemin", "0");
    expect(handle).toHaveAttribute("aria-valuenow", "0");
    expect(screen.queryByRole("navigation")).not.toBeInTheDocument();
    expect(localStorage.getItem("test:panel-width")).toBe("480");
    fireEvent.keyDown(handle, { key: "ArrowLeft" });
    expect(handle).toHaveAttribute("aria-valuenow", "0");
    fireEvent.keyDown(handle, { key: "Enter" });
    expect(handle).toHaveAttribute("aria-valuenow", "480");
    expect(screen.getByRole("navigation")).toBeInTheDocument();
    fireEvent.keyDown(handle, { key: "Home" });
    fireEvent.keyDown(handle, { key: "ArrowRight" });
    expect(handle).toHaveAttribute("aria-valuenow", "480");
    fireEvent.keyDown(handle, { key: "Home" });
    fireEvent.keyDown(handle, { key: "End" });
    expect(handle).toHaveAttribute("aria-valuenow", "480");
  });

  it("collapses below the minimum without losing the previous width", () => {
    localStorage.setItem("test:panel-width", "220");
    render(<Panel collapsible />);
    const handle = screen.getByRole("separator");
    fireEvent.keyDown(handle, { key: "ArrowLeft" });
    expect(handle).toHaveAttribute("aria-valuenow", "0");
    expect(localStorage.getItem("test:panel-width")).toBe("220");
    fireEvent.keyDown(handle, { key: "ArrowRight" });
    expect(handle).toHaveAttribute("aria-valuenow", "220");
  });

  it.each(["left", "right"] as const)(
    "%s panel keyboard direction, bounds, collapse and persistence",
    (side) => {
      const collapse = vi.fn();
      const mounted = render(<Panel side={side} onCollapse={collapse} />);
      const handle = screen.getByRole("separator", { name: "Resize" });
      expect(handle).toHaveAttribute("aria-controls", "test-panel");
      fireEvent.keyDown(handle, { key: "ArrowRight" });
      expect(handle).toHaveAttribute(
        "aria-valuenow",
        side === "left" ? "276" : "244",
      );
      fireEvent.keyDown(handle, { key: "Home" });
      expect(handle).toHaveAttribute("aria-valuenow", "220");
      fireEvent.keyDown(handle, { key: "End" });
      expect(handle).toHaveAttribute("aria-valuenow", "480");
      expect(localStorage.getItem("test:panel-width")).toBe("480");
      fireEvent.keyDown(handle, { key: "Enter" });
      expect(collapse).toHaveBeenCalledOnce();
      mounted.unmount();
      expect(disconnect).toHaveBeenCalled();
      render(<Panel side={side} />);
      expect(screen.getByRole("separator")).toHaveAttribute(
        "aria-valuenow",
        "480",
      );
    },
  );

  it("shrinks with its container without overwriting the preferred width", () => {
    localStorage.setItem("test:panel-width", "480");
    render(<Panel />);
    available = 720;
    act(() => resize());
    expect(screen.getByRole("separator")).toHaveAttribute(
      "aria-valuenow",
      "320",
    );
    expect(screen.getByRole("separator")).toHaveAttribute(
      "aria-valuemax",
      "320",
    );
    expect(localStorage.getItem("test:panel-width")).toBe("480");
    available = 1000;
    act(() => resize());
    expect(screen.getByRole("separator")).toHaveAttribute(
      "aria-valuenow",
      "480",
    );
  });

  it.each(["NaN", "999999", "-20", ""])(
    "ignores invalid persisted width %s",
    (value) => {
      localStorage.setItem("test:panel-width", value);
      render(<Panel />);
      expect(screen.getByRole("separator")).toHaveAttribute(
        "aria-valuenow",
        "260",
      );
    },
  );

  it("remains usable when local storage is unavailable", () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    render(<Panel />);
    fireEvent.keyDown(screen.getByRole("separator"), { key: "ArrowRight" });
    expect(screen.getByRole("separator")).toHaveAttribute(
      "aria-valuenow",
      "276",
    );
  });
});
