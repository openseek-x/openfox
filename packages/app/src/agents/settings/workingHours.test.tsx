import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { I18nProvider } from "@openfox/i18n";
import { agent } from "../fixtures";
import {
  defaultWorkingHours,
  validWorkingHours,
  WorkingHoursSection,
} from "./WorkingHoursSection";
import { RunChoiceDialog } from "../../tasks/detail/RunChoiceDialog";

function setup() {
  const saveConfig = vi.fn().mockResolvedValue(undefined),
    setDirty = vi.fn();
  const item = agent("a1");
  render(
    <I18nProvider initialLocale="en-US">
      <WorkingHoursSection
        orgId="o1"
        agent={item}
        writes={{
          saveConfig,
          patchProfile: vi.fn(),
          configPending: false,
          profilePending: false,
        }}
        setDirty={setDirty}
      />
    </I18nProvider>,
  );
  return { saveConfig, setDirty };
}
describe("working-hours editor", () => {
  it("starts disabled with the approved periods and explicit trigger defaults", () => {
    const cfg = defaultWorkingHours();
    expect(cfg.enabled).toBe(false);
    expect(cfg.days[0]).toEqual([
      { start: "00:00", end: "08:00" },
      { start: "20:00", end: "24:00" },
    ]);
    expect(cfg.days[6]).toEqual([{ start: "00:00", end: "24:00" }]);
    expect(cfg.triggers).toEqual({
      assignment: true,
      comment_mention: true,
      direct_chat: false,
    });
    expect(validWorkingHours(cfg)).toBe(true);
  });
  it("rejects overlaps and invalid timezone but permits a closed week", () => {
    const cfg = defaultWorkingHours();
    cfg.days = Array.from({ length: 7 }, () => []);
    expect(validWorkingHours(cfg)).toBe(true);
    cfg.days[0] = [
      { start: "01:00", end: "09:00" },
      { start: "08:00", end: "12:00" },
    ];
    expect(validWorkingHours(cfg)).toBe(false);
    cfg.days[0] = [];
    cfg.timezone = "Local";
    expect(validWorkingHours(cfg)).toBe(false);
  });
  it("submits one complete schedule and reports unsaved changes", async () => {
    const user = userEvent.setup();
    const { saveConfig, setDirty } = setup();
    await user.click(screen.getByRole("switch"));
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(saveConfig).toHaveBeenCalledOnce();
    expect(saveConfig.mock.calls[0]![0].working_hours).toMatchObject({
      enabled: true,
      triggers: { assignment: true, comment_mention: true, direct_chat: false },
    });
    expect(setDirty).toHaveBeenCalledWith("working-hours", true);
  });
  it("disables save for invalid periods instead of sending partial config", async () => {
    const { saveConfig } = setup();
    fireEvent.change(screen.getByLabelText("Monday period 1 end"), {
      target: { value: "23:00" },
    });
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
    expect(saveConfig).not.toHaveBeenCalled();
  });
  it("can close all days without clearing trigger choices", async () => {
    const user = userEvent.setup();
    const { saveConfig } = setup();
    await user.click(screen.getByRole("switch"));
    for (const day of [
      "Monday",
      "Tuesday",
      "Wednesday",
      "Thursday",
      "Friday",
      "Saturday",
      "Sunday",
    ]) {
      const group = screen.getByRole("group", { name: day });
      while (within(group).queryAllByRole("button", { name: /Remove/ }).length)
        await user.click(
          within(group).getAllByRole("button", { name: /Remove/ })[0]!,
        );
    }
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(saveConfig.mock.calls[0]![0].working_hours.days).toEqual([
      [],
      [],
      [],
      [],
      [],
      [],
      [],
    ]);
    expect(
      screen.getByText(/No working periods this week/),
    ).toBeInTheDocument();
  });
  it("converts a once-only local datetime to an absolute timestamp", async () => {
    const onConfirm = vi.fn();
    render(
      <I18nProvider initialLocale="en-US">
        <RunChoiceDialog
          open
          agentName="Agent"
          conversationTitle={null}
          hasConversation={false}
          onConfirm={onConfirm}
          onClose={vi.fn()}
        />
      </I18nProvider>,
    );
    fireEvent.change(screen.getByLabelText("Earliest start (local timezone)"), {
      target: { value: "2026-11-20T21:30" },
    });
    await userEvent.click(screen.getByRole("button", { name: "Start run" }));
    await waitFor(() =>
      expect(onConfirm).toHaveBeenCalledWith(
        "reuse",
        new Date("2026-11-20T21:30").toISOString(),
      ),
    );
  });
});
