import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useRef } from "react";
import { afterEach, expect, it, vi } from "vitest";

import { Popover } from "./Popover";
import { Menu, MenuItem } from "./Menu";

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

it("keeps a nested menu open through pointerdown and closes only the child on Escape", async () => {
  const user = userEvent.setup();
  const select = vi.fn();
  render(
    <Menu
      label="Models"
      trigger={({ ref, toggle }) => (
        <button ref={ref} onClick={toggle}>
          Models
        </button>
      )}
    >
      <Menu
        label="Depth"
        trigger={({ ref, toggle }) => (
          <button ref={ref} onClick={toggle}>
            Depth
          </button>
        )}
      >
        <MenuItem onSelect={select}>High</MenuItem>
      </Menu>
    </Menu>,
  );
  await user.click(screen.getByRole("button", { name: "Models" }));
  const trigger = screen.getByRole("button", { name: "Depth" });
  await user.click(trigger);
  await user.keyboard("{Escape}");
  expect(screen.queryByRole("menu", { name: "Depth" })).not.toBeInTheDocument();
  expect(screen.getByRole("menu", { name: "Models" })).toBeVisible();
  expect(trigger).toHaveFocus();
  await user.click(trigger);
  await user.click(screen.getByRole("menuitem", { name: "High" }));
  expect(select).toHaveBeenCalledOnce();
  expect(screen.queryByRole("menu")).not.toBeInTheDocument();
});

it("repositions above its anchor after content resizes and disconnects observers", () => {
  let resize = () => {};
  let height = 180;
  const observe = vi.fn();
  const disconnect = vi.fn();
  vi.stubGlobal(
    "ResizeObserver",
    class {
      constructor(callback: () => void) {
        resize = callback;
      }
      observe = observe;
      disconnect = disconnect;
    },
  );
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(
    function (this: HTMLElement) {
      return this.tagName === "BUTTON"
        ? ({
            top: 400,
            bottom: 428,
            left: 100,
            right: 128,
            width: 28,
            height: 28,
          } as DOMRect)
        : ({ width: 260, height } as DOMRect);
    },
  );
  function Harness() {
    const anchor = useRef<HTMLButtonElement>(null);
    return (
      <>
        <button ref={anchor}>Open</button>
        <Popover
          open
          onOpenChange={() => {}}
          anchorRef={anchor}
          placement="top-start"
          aria-label="Choices"
        >
          Content
        </Popover>
      </>
    );
  }
  const mounted = render(<Harness />);
  expect(screen.getByRole("dialog")).toHaveStyle({ top: "216px" });
  expect(observe).toHaveBeenCalledTimes(2);
  height = 40;
  act(() => resize());
  expect(screen.getByRole("dialog")).toHaveStyle({ top: "356px" });
  mounted.unmount();
  expect(disconnect).toHaveBeenCalledOnce();
});
