import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it } from "vitest";
import { agent, device, entry } from "./fixtures";
import { runtimeChoices } from "./model";
import { runtimeOptions } from "./CreateAgentDialog";
import { ORG, chatBase } from "../conversations/fixtures";
import { json, renderSettings } from "../settings/testing";
import { optionsFromDevice } from "../conversations/overrides";
const base = device("d1").runtimes[0]!;
const capability = {
  ...base,
  provider: "codebuddy" as const,
  version: "2.184.0",
  runnable: false,
  status: "UNAVAILABLE" as const,
  login_status: "UNKNOWN" as const,
  protocol_ready: true,
  requires_run_configuration: true,
  approval_modes: ["READ_ONLY" as const],
  collaboration_modes: ["DEFAULT" as const, "PLAN" as const],
  models_error: "Explicit model required",
};
afterEach(() => localStorage.clear());
it("keeps protocol-ready CodeBuddy selectable without claiming login, and rejects missing readiness", () => {
  const t = ((key: string) => key) as Parameters<typeof runtimeOptions>[1];
  const ready = device("d1", { runtimes: [capability] });
  expect(
    runtimeOptions(runtimeChoices([ready], "s1", "u1"), t).find((v) =>
      v.value.endsWith(":codebuddy"),
    )?.disabled,
  ).toBe(false);
  expect(
    runtimeOptions(
      runtimeChoices(
        [
          device("d1", {
            runtimes: [{ ...capability, protocol_ready: false }],
          }),
        ],
        "s1",
        "u1",
      ),
      t,
    ).find((v) => v.value.endsWith(":codebuddy"))?.disabled,
  ).toBe(true);
  expect(optionsFromDevice(ready).runtimes[0]).toMatchObject({
    runnable: false,
    protocol_ready: true,
    requires_run_configuration: true,
  });
});
it("shows exact configuration requirements, disables writable modes and arguments, and retains PLAN", async () => {
  const original = agent("a1", { name: "CodeBuddy Agent" });
  const current = {
    ...original,
    config: {
      ...original.config!,
      runtime: "codebuddy",
      model: "default",
      approval_mode: "FOLLOW_RUNTIME" as const,
    },
  };
  renderSettings(
    "/o/o1/agents?agentSettings=a1",
    chatBase((call) => {
      if (call.path === `${ORG}/agent-directory`)
        return json(200, {
          entries: [entry("a1", { name: current.name, runtime: "codebuddy" })],
          archived_count: 0,
          truncated: false,
        });
      if (call.path === `${ORG}/devices`)
        return json(200, {
          devices: [device("d1", { runtimes: [capability] })],
          next_cursor: null,
        });
      if (call.path === `${ORG}/spaces/s1/agents/a1`) return json(200, current);
      if (call.path.endsWith("/a1/env-vars"))
        return json(200, {
          env_vars: [],
          can_view_values: true,
          secrets_available: true,
        });
      if (call.path.endsWith("/a1/repos")) return json(200, { repos: [] });
      return undefined;
    }),
  );
  const user = userEvent.setup();
  const drawer = await screen.findByRole("complementary", {
    name: "CodeBuddy Agent 的设置",
  });
  await user.click(
    within(drawer).getByRole("button", { name: /电脑 & Runtime/ }),
  );
  expect(
    await within(drawer).findByText(/仅支持 macOS arm64 \/ 2.184.0/),
  ).toHaveTextContent("CODEBUDDY_API_KEY");
  await user.click(
    within(drawer).getByRole("button", { name: "权限模式默认" }),
  );
  const modes = screen.getByRole("listbox", { name: "权限模式默认" });
  const options = within(modes).getAllByRole("option");
  expect(options.map((v) => v.getAttribute("aria-disabled"))).toEqual([
    null,
    null,
    "true",
    "true",
  ]);
  await user.keyboard("{Escape}");
  await user.click(
    within(drawer).getByRole("button", { name: "协作模式默认" }),
  );
  const collaboration = screen.getByRole("listbox", { name: "协作模式默认" });
  expect(
    within(collaboration)
      .getAllByRole("option")
      .every((v) => v.getAttribute("aria-disabled") !== "true"),
  ).toBe(true);
  await user.keyboard("{Escape}");
  expect(
    within(drawer)
      .getAllByRole("button", { name: /^添加$/ })
      .at(-1),
  ).toBeDisabled();
});
