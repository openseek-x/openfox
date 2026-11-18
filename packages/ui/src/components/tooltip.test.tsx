import { act, fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

import { Tooltip } from "./Tooltip";

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

function advance() {
  act(() => vi.advanceTimersByTime(300));
}

it("hides a visible tooltip when its menu opens and preserves the trigger handler", () => {
  const onClick = vi.fn();
  function Harness() {
    const [open, setOpen] = useState(false);
    return (
      <Tooltip content="Permission mode">
        <button
          aria-expanded={open}
          onClick={() => {
            onClick();
            setOpen(!open);
          }}
        >
          Permissions
        </button>
      </Tooltip>
    );
  }
  render(<Harness />);
  const trigger = screen.getByRole("button");
  fireEvent.mouseEnter(trigger);
  advance();
  expect(screen.getByRole("tooltip")).toHaveTextContent("Permission mode");
  fireEvent.click(trigger);
  expect(onClick).toHaveBeenCalledOnce();
  expect(trigger).toHaveAttribute("aria-expanded", "true");
  expect(trigger).not.toHaveAttribute("aria-describedby");
  expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();
  fireEvent.mouseLeave(trigger);
  fireEvent.mouseEnter(trigger);
  fireEvent.focus(trigger);
  advance();
  expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();
  fireEvent.click(trigger);
  fireEvent.blur(trigger);
  fireEvent.focus(trigger);
  advance();
  expect(screen.getByRole("tooltip")).toBeVisible();
});

it("cancels a pending focus tooltip when a keyboard action expands the trigger", () => {
  const content = (expanded: boolean | "true") => (
    <Tooltip content="Models">
      <button aria-expanded={expanded}>Select model</button>
    </Tooltip>
  );
  const mounted = render(content(false));
  fireEvent.focus(screen.getByRole("button"));
  mounted.rerender(content("true"));
  advance();
  expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();
  expect(vi.getTimerCount()).toBe(0);
});

it("dismisses ordinary action hints on click and clears timers on unmount", () => {
  const onClick = vi.fn();
  const mounted = render(
    <Tooltip content="Copy text">
      <button onClick={onClick}>Copy</button>
    </Tooltip>,
  );
  const trigger = screen.getByRole("button");
  fireEvent.mouseEnter(trigger);
  advance();
  expect(screen.getByRole("tooltip")).toBeVisible();
  fireEvent.click(trigger);
  expect(onClick).toHaveBeenCalledOnce();
  expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();
  fireEvent.mouseEnter(trigger);
  mounted.unmount();
  expect(vi.getTimerCount()).toBe(0);
});
