import { useState } from "react";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it } from "vitest";
import { Dialog } from "./Dialog";

function NestedDialogs() {
  const [open, setOpen] = useState(false);
  const [child, setChild] = useState(false);
  return (
    <>
      <button onClick={() => setOpen(true)}>Open</button>
      {open && (
        <Dialog open title="Parent" onClose={() => setOpen(false)}>
          <button onClick={() => setChild(true)}>Child</button>
          {child && (
            <Dialog open title="Child dialog" onClose={() => setChild(false)}>
              <button>Last control</button>
              <button onClick={() => setOpen(false)}>Close all</button>
            </Dialog>
          )}
        </Dialog>
      )}
    </>
  );
}

it("Escape and Tab belong only to the top dialog, and closing restores focus", async () => {
  const user = userEvent.setup();
  render(<NestedDialogs />);
  await user.click(screen.getByRole("button", { name: "Open" }));
  const trigger = screen.getByRole("button", { name: "Child" });
  await user.click(trigger);
  const child = screen.getByRole("dialog", { name: "Child dialog" });
  const buttons = within(child).getAllByRole("button");
  buttons.at(-1)!.focus();
  await user.tab();
  expect(buttons[0]).toHaveFocus();
  await user.keyboard("{Escape}");
  expect(screen.queryByRole("dialog", { name: "Child dialog" })).toBeNull();
  expect(screen.getByRole("dialog", { name: "Parent" })).toBeVisible();
  expect(trigger).toHaveFocus();
  expect(document.body.style.overflow).toBe("hidden");
  await user.keyboard("{Escape}");
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(screen.getByRole("button", { name: "Open" })).toHaveFocus();
  expect(document.body.style.overflow).toBe("");
});

it("restores scrolling when a parent and child unmount together", async () => {
  document.body.style.overflow = "auto";
  const user = userEvent.setup();
  render(<NestedDialogs />);
  await user.click(screen.getByRole("button", { name: "Open" }));
  await user.click(screen.getByRole("button", { name: "Child" }));
  await user.click(screen.getByRole("button", { name: "Close all" }));
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(document.body.style.overflow).toBe("auto");
  document.body.style.overflow = "";
});
