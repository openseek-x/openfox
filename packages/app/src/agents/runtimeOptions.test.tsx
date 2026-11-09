import { describe, expect, it } from "vitest";

import { runtimeOptions } from "./CreateAgentDialog";
import { runtimeChoices } from "./model";
import { device } from "./fixtures";

const t = ((key: string) => key) as Parameters<typeof runtimeOptions>[1];

describe("Runtime 下拉", () => {
  it("四种 Runtime 都能出现在选项里，未登录的置灰", () => {
    const base = device("d1").runtimes[0]!;
    const choices = runtimeChoices(
      [
        device("d1", {
          name: "mac",
          runtimes: [
            base,
            { ...base, provider: "claude-code" },
            { ...base, provider: "opencode" },
            {
              ...base,
              provider: "pi",
              runnable: false,
              status: "NEEDS_LOGIN",
              reason: null,
            },
          ],
        }),
      ],
      "s1",
      "u1",
    );
    const options = runtimeOptions(choices, t);
    expect(options.map((option) => [option.label, option.disabled])).toEqual([
      ["Codex · mac", false],
      ["Claude Code · mac", false],
      ["OpenCode · mac", false],
      ["pi · mac", true],
    ]);
    expect(options[3]!.description).toBe("agents.create.blocks.login");
  });
});
