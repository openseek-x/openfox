import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Link, useLocation } from "react-router-dom";
import { expect, it, vi } from "vitest";
import { useAgentSettingsParam } from "../params";
import { InstructionsEditor } from "./InstructionsEditor";

function Navigation() {
  const drawer = useAgentSettingsParam();
  const location = useLocation();
  return (
    <>
      <Link to="/tasks">任务</Link>
      <button onClick={() => drawer.open("other", "s1")}>切换 Agent</button>
      <output>{location.pathname + location.search}</output>
    </>
  );
}

it("prompts for SPA links, Agent switches and browser unload while a draft is dirty", async () => {
  const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
  const user = userEvent.setup();
  render(
    <MemoryRouter initialEntries={["/agents?agentSettings=original"]}>
      <Navigation />
      <InstructionsEditor
        saved="saved"
        label="指令"
        editable
        onSave={async (value) => value}
        onDirty={() => {}}
      />
    </MemoryRouter>,
  );
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "draft" } });
  await user.click(screen.getByRole("link", { name: "任务" }));
  expect(confirm).toHaveBeenCalledTimes(1);
  expect(screen.getByRole("status")).toHaveTextContent(
    "/agents?agentSettings=original",
  );
  await user.click(screen.getByRole("button", { name: "切换 Agent" }));
  expect(confirm).toHaveBeenCalledTimes(2);
  expect(screen.getByRole("status")).toHaveTextContent(
    "/agents?agentSettings=original",
  );
  const unload = new Event("beforeunload", { cancelable: true });
  window.dispatchEvent(unload);
  expect(unload.defaultPrevented).toBe(true);
  confirm.mockReturnValue(true);
  await user.click(screen.getByRole("button", { name: "切换 Agent" }));
  expect(screen.getByRole("status")).toHaveTextContent("agentSettings=other");
});
