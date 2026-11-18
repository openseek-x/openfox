import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { expect, it, vi } from "vitest";

import { Dialog } from "./Dialog";
import { Select } from "./Select";

it("keeps modal select options accessible and Escape closes only the list", async () => {
  const onClose = vi.fn();
  function Harness() {
    const [value, setValue] = useState<string | null>(null);
    return (
      <Dialog open title="同步" onClose={onClose}>
        <Select
          aria-label="目标空间"
          value={value}
          onChange={setValue}
          options={[{ value: "space", label: "测试空间" }]}
        />
      </Dialog>
    );
  }
  render(<Harness />);
  const trigger = screen.getByRole("button", { name: "目标空间" });
  await userEvent.click(trigger);
  const modal = within(screen.getByRole("dialog", { name: "同步" }));
  await userEvent.click(modal.getByRole("option", { name: "测试空间" }));
  expect(trigger).toHaveTextContent("测试空间");
  expect(trigger).toHaveFocus();
  await userEvent.keyboard("{ArrowDown}");
  expect(modal.getByRole("listbox")).toBeInTheDocument();
  await userEvent.keyboard("{Escape}");
  expect(modal.queryByRole("listbox")).not.toBeInTheDocument();
  expect(onClose).not.toHaveBeenCalled();
  expect(trigger).toHaveFocus();
});
