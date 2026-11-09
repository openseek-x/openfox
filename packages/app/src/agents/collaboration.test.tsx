import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";

import type { CollaborationMode, Device } from "@openfox/api";

import { ORG, chatBase } from "../conversations/fixtures";
import { json, renderSettings, type Call } from "../settings/testing";
import {
  collaborationModeOf,
  supportedCollaborationModes,
} from "./collaboration";
import { agent, device, entry } from "./fixtures";

// Agent 设置页「协作模式默认」下拉。

afterEach(() => localStorage.clear());

const LABEL = "协作模式默认";

function codexDevice(modes?: CollaborationMode[]): Device {
  const base = device("d1");
  return device("d1", {
    runtimes: [
      modes === undefined
        ? base.runtimes[0]!
        : { ...base.runtimes[0]!, collaboration_modes: modes },
    ],
  });
}

function open(options: { devices: Device[]; collaboration?: string }) {
  const record: Call[] = [];
  const base = agent("a1", { name: "熊孩子" });
  const current =
    options.collaboration === undefined
      ? base
      : {
          ...base,
          config: {
            ...base.config!,
            collaboration_mode: options.collaboration,
          },
        };
  renderSettings(
    "/o/o1/agents?agentSettings=a1",
    chatBase((call) => {
      if (call.method === "GET" && call.path === `${ORG}/agent-directory`)
        return json(200, {
          entries: [entry("a1", { name: "熊孩子" })],
          archived_count: 0,
          truncated: false,
        });
      if (call.method === "GET" && call.path === `${ORG}/devices`)
        return json(200, { devices: options.devices, next_cursor: null });
      if (call.method === "GET" && call.path === `${ORG}/spaces/s1/agents/a1`)
        return json(200, current);
      if (call.method === "GET" && call.path.endsWith("/a1/env-vars"))
        return json(200, {
          env_vars: [],
          can_view_values: true,
          secrets_available: true,
        });
      if (call.method === "GET" && call.path.endsWith("/a1/repos"))
        return json(200, { repos: [] });
      if (call.method === "POST" && call.path.endsWith("/a1/config-versions")) {
        record.push(call);
        return json(201, agent("a1", { name: "熊孩子", version: 6 }));
      }
      return undefined;
    }),
  );
  return record;
}

async function openRuntimeSection() {
  const user = userEvent.setup();
  const drawer = await screen.findByRole("complementary", {
    name: "熊孩子 的设置",
  });
  await user.click(
    within(drawer).getByRole("button", { name: /电脑 & Runtime/ }),
  );
  const trigger = await within(drawer).findByRole("button", { name: LABEL });
  return { user, drawer, trigger };
}

describe("Agent 设置页的协作模式默认", () => {
  it("历史值 FOLLOW_AGENT 显示为「跟随 Runtime」；Codex 不支持的模式置灰", async () => {
    open({ devices: [codexDevice(["FOLLOW_RUNTIME"])] });
    const { user, trigger } = await openRuntimeSection();
    expect(trigger).toHaveTextContent("跟随 Runtime");
    await user.click(trigger);
    const list = await screen.findByRole("listbox", { name: LABEL });
    const options = within(list).getAllByRole("option");
    expect(options.map((item) => item.getAttribute("aria-disabled"))).toEqual([
      null,
      "true",
      "true",
    ]);
    expect(options[2]).toHaveTextContent("这台电脑上的 Codex 不支持");
  });

  it("支持时选择计划模式，保存带 collaboration_mode: PLAN", async () => {
    const record = open({
      devices: [codexDevice(["FOLLOW_RUNTIME", "DEFAULT", "PLAN"])],
    });
    const { user, trigger } = await openRuntimeSection();
    await user.click(trigger);
    const list = await screen.findByRole("listbox", { name: LABEL });
    expect(
      within(list)
        .getAllByRole("option")
        .every((item) => item.getAttribute("aria-disabled") === null),
    ).toBe(true);
    await user.click(within(list).getByRole("option", { name: /计划模式/ }));
    await waitFor(() => expect(record).toHaveLength(1));
    expect(record[0]!.body).toMatchObject({ collaboration_mode: "PLAN" });
  });

  it("保存的模式在这台电脑上不支持时给出警告；旧版心跳只有跟随 Runtime", async () => {
    open({ devices: [codexDevice()], collaboration: "PLAN" });
    const { drawer, trigger } = await openRuntimeSection();
    expect(trigger).toHaveTextContent("计划模式");
    expect(
      await within(drawer).findByText(
        /这台电脑上的 Codex 不支持当前的协作模式「计划模式」/,
      ),
    ).toBeInTheDocument();
  });

  it("拿不到电脑信息时不置灰任何选项", async () => {
    open({ devices: [] });
    const { user, drawer, trigger } = await openRuntimeSection();
    await user.click(trigger);
    const list = await screen.findByRole("listbox", { name: LABEL });
    expect(
      within(list)
        .getAllByRole("option")
        .every((item) => item.getAttribute("aria-disabled") === null),
    ).toBe(true);
    expect(within(drawer).queryByText(/不支持当前的协作模式/)).toBeNull();
  });
});

describe("协作模式工具函数", () => {
  it("collaborationModeOf 归一历史值、拒绝未知值", () => {
    expect(collaborationModeOf("")).toBe("FOLLOW_RUNTIME");
    expect(collaborationModeOf(undefined)).toBe("FOLLOW_RUNTIME");
    expect(collaborationModeOf("FOLLOW_AGENT")).toBe("FOLLOW_RUNTIME");
    expect(collaborationModeOf("PLAN")).toBe("PLAN");
    expect(collaborationModeOf("AUTO")).toBeNull();
  });

  it("supportedCollaborationModes 缺省按只有 FOLLOW_RUNTIME，找不到返回 undefined", () => {
    expect(supportedCollaborationModes([codexDevice()], "d1", "codex")).toEqual(
      ["FOLLOW_RUNTIME"],
    );
    expect(
      supportedCollaborationModes(
        [codexDevice(["FOLLOW_RUNTIME", "PLAN"])],
        "d1",
        "codex",
      ),
    ).toEqual(["FOLLOW_RUNTIME", "PLAN"]);
    expect(
      supportedCollaborationModes([codexDevice()], "d2", "codex"),
    ).toBeUndefined();
    expect(
      supportedCollaborationModes([codexDevice()], "d1", "pi"),
    ).toBeUndefined();
    expect(
      supportedCollaborationModes(undefined, "d1", "codex"),
    ).toBeUndefined();
  });
});
