import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import {
  apiError,
  json,
  renderSettings,
  type Call,
} from "../../settings/testing";
import { agent, crew, entry, TS } from "../fixtures";

const org = "/v1/organizations/o1";
const path = `${org}/spaces/s1/agents/a1`;

function setup({ conflict = false, member = false } = {}) {
  const initial = agent("a1");
  let current = {
    ...initial,
    config: {
      ...initial.config!,
      instructions: "current",
      model: "current-model",
    },
  };
  const history = [
    current.config,
    {
      ...current.config,
      config_version: 1,
      instructions: "# old 中文",
      model: "old-model",
      computer_id: "old-computer",
    },
  ];
  const writes: Call[] = [];
  const calls = renderSettings(
    "/o/o1/agents?agentSettings=a1",
    (call) => {
      if (call.path === `${org}/spaces`)
        return json(200, {
          spaces: [
            {
              id: "s1",
              name: "space",
              prefix: "SP",
              visibility: "shared",
              created_at: TS,
              features: { views: false },
              my_role: member ? "VIEWER" : "OWNER",
              can_manage: !member,
            },
          ],
        });
      if (call.path === `${org}/agent-directory`)
        return json(200, { entries: [entry("a1")], archived_count: 0 });
      if (call.path === path) return json(200, current);
      if (call.path === `${path}/config-versions`) {
        if (call.method === "GET")
          return json(200, {
            config_versions: history,
            next_before_version: null,
          });
        writes.push(call);
        if (conflict) return apiError(409, "VERSION_CONFLICT");
        const body = call.body as { instructions: string };
        current = {
          ...current,
          version: current.version + 1,
          current_config_version: current.current_config_version + 1,
          config: {
            ...current.config,
            instructions: body.instructions,
            config_version: current.config.config_version + 1,
          },
        };
        history.unshift(current.config);
        return json(201, current);
      }
      if (call.path === `${path}/stats`)
        return json(200, {
          task_count: 0,
          run_count: 0,
          input_tokens: 0,
          output_tokens: 0,
        });
      return undefined;
    },
    member ? "MEMBER" : "OWNER",
  );
  return { calls, writes, initial };
}

describe("instruction history integration with existing client writes", () => {
  it("reads paged history and restores only instructions using current full config", async () => {
    const user = userEvent.setup();
    const { calls, writes, initial } = setup();
    await user.click(
      await screen.findByRole("button", { name: /^Agent 指令/ }),
    );
    await user.click(screen.getByRole("button", { name: "指令历史" }));
    await user.click(await screen.findByRole("button", { name: /^版本 1 / }));
    expect(screen.getByLabelText("指令差异")).toHaveTextContent("old 中文");
    await user.click(screen.getByRole("button", { name: "恢复此版本" }));
    await waitFor(() => expect(writes).toHaveLength(1));
    expect(writes[0]!.body).toMatchObject({
      instructions: "# old 中文",
      model: "current-model",
      computer_id: initial.config!.computer_id,
      grant_id: initial.config!.grant_id,
      expected_agent_version: initial.version,
    });
    expect(
      calls
        .find(
          (call) =>
            call.method === "GET" && call.path.endsWith("config-versions"),
        )
        ?.query.get("limit"),
    ).toBe("20");
    expect(screen.getByRole("textbox", { name: "Agent 指令" })).toHaveValue(
      "# old 中文",
    );
  });

  it("refreshes already open history after an ordinary instruction save", async () => {
    const user = userEvent.setup();
    const { calls } = setup();
    await user.click(
      await screen.findByRole("button", { name: /^Agent 指令/ }),
    );
    await user.click(screen.getByRole("button", { name: "指令历史" }));
    await screen.findByRole("button", { name: /^版本 2 / });
    fireEvent.change(screen.getByRole("textbox", { name: "Agent 指令" }), {
      target: { value: "ordinary save" },
    });
    await user.click(screen.getByRole("button", { name: "保存" }));
    expect(
      await screen.findByRole("button", { name: /^版本 3 / }),
    ).toBeVisible();
    expect(
      calls.filter(
        (call) =>
          call.method === "GET" && call.path.endsWith("config-versions"),
      ).length,
    ).toBeGreaterThan(1);
  });

  it("409 refreshes the Agent but preserves unsaved text and leaving asks for confirmation", async () => {
    const user = userEvent.setup();
    const { calls, writes } = setup({ conflict: true });
    await user.click(
      await screen.findByRole("button", { name: /^Agent 指令/ }),
    );
    const input = screen.getByRole("textbox", { name: "Agent 指令" });
    fireEvent.change(input, { target: { value: "keep this draft" } });
    await user.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() => expect(writes).toHaveLength(1));
    await waitFor(() =>
      expect(calls.filter((call) => call.path === path)).toHaveLength(2),
    );
    expect(input).toHaveValue("keep this draft");
    await user.click(screen.getByRole("button", { name: /^Agent 指令/ }));
    expect(await screen.findByRole("dialog", { name: /未保存/ })).toBeVisible();
    await user.click(screen.getByRole("button", { name: "继续编辑" }));
    expect(input).toHaveValue("keep this draft");
  });

  it("a readonly member can browse visible history but cannot edit or restore", async () => {
    const user = userEvent.setup();
    setup({ member: true });
    await user.click(
      await screen.findByRole("button", { name: /^Agent 指令/ }),
    );
    expect(screen.getByRole("textbox", { name: "Agent 指令" })).toHaveAttribute(
      "readonly",
    );
    await user.click(screen.getByRole("button", { name: "指令历史" }));
    await user.click(await screen.findByRole("button", { name: /^版本 1 / }));
    expect(screen.getByRole("button", { name: "恢复此版本" })).toBeDisabled();
  });

  it("crew save failure retains its draft and Escape from the drawer requires confirmation", async () => {
    const user = userEvent.setup();
    const value = crew("c1");
    renderSettings("/o/o1/agents?crewSettings=c1", (call) => {
      if (call.path === `${org}/spaces`)
        return json(200, {
          spaces: [
            {
              id: "s1",
              name: "space",
              prefix: "SP",
              visibility: "shared",
              created_at: TS,
              features: { views: false },
              my_role: "OWNER",
              can_manage: true,
            },
          ],
        });
      if (call.path === `${org}/agent-directory`)
        return json(200, {
          entries: [entry("c1", { type: "crew" })],
          archived_count: 0,
        });
      if (call.path === `${org}/spaces/s1/crews/c1`)
        return call.method === "GET"
          ? json(200, value)
          : apiError(500, "INTERNAL");
      return undefined;
    });
    const drawer = await screen.findByRole("complementary", { name: /c1/ });
    await user.click(within(drawer).getByRole("button", { name: /^小队指令/ }));
    const input = screen.getByRole("textbox", { name: "小队指令" });
    fireEvent.change(input, { target: { value: "crew draft" } });
    await user.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "保存" })).toBeEnabled(),
    );
    expect(input).toHaveValue("crew draft");
    await user.keyboard("{Escape}");
    expect(await screen.findByRole("dialog", { name: /未保存/ })).toBeVisible();
  });
});
