import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";

import type { QuickCommand, Skill } from "@openfox/api";

import { agent, device } from "../agents/fixtures";
import { json, renderSettings } from "../settings/testing";
import { ORG, chatBase, message, summary } from "./fixtures";
import { buildSlashEntries, filterSlashEntries } from "./slashMenu";

// 阶段 12f · E-05：对话输入框的斜杠菜单（技能 + 快捷指令，离线 / Runtime 不支持时置灰）。

afterEach(() => localStorage.clear());

const t = ((key: string, vars?: Record<string, string>) =>
  vars ? `${key}:${JSON.stringify(vars)}` : key) as never;

const skill = (id: string, name: string): Skill =>
  ({ id, name, description: `${name} 说明` }) as Skill;

function command(id: string, extra: Partial<QuickCommand> = {}): QuickCommand {
  return {
    id,
    name: `指令 ${id}`,
    prompt: "请澄清本任务",
    target: { kind: "AGENT", id: "a1", name: "A", available: true },
    ...extra,
  } as QuickCommand;
}

describe("buildSlashEntries", () => {
  it("技能在前、快捷指令在后；只列目标为当前 Agent 的指令", () => {
    const entries = buildSlashEntries({
      t,
      agentId: "a1",
      skills: [skill("s1", "代码审查")],
      commands: [
        command("c1"),
        command("c2", {
          target: { kind: "AGENT", id: "other", name: "B", available: true },
        } as Partial<QuickCommand>),
        command("c3", {
          target: { kind: "CREW", id: "a1", name: "队", available: true },
        } as Partial<QuickCommand>),
      ],
    });
    expect(entries.map((entry) => entry.id)).toEqual([
      "skill:s1",
      "command:c1",
    ]);
    expect(entries[1]!.insert).toBe("请澄清本任务");
  });

  it("阻塞原因让技能与可用快捷指令置灰；目标不可用的指令用自己的原因", () => {
    const entries = buildSlashEntries({
      t,
      agentId: "a1",
      skills: [skill("s1", "代码审查")],
      commands: [
        command("c1"),
        command("c2", {
          target: {
            kind: "AGENT",
            id: "a1",
            name: "A",
            available: false,
            unavailable_reason: "ARCHIVED",
          },
        } as Partial<QuickCommand>),
      ],
      blockReason: "电脑离线",
    });
    expect(entries.map((entry) => entry.disabledReason)).toEqual([
      "电脑离线",
      "电脑离线",
      "tasks.quickCommands.unavailable.ARCHIVED",
    ]);
  });

  it("空提示词的快捷指令回退到默认文案；按名称与说明过滤", () => {
    const entries = buildSlashEntries({
      t,
      agentId: "a1",
      skills: [skill("s1", "代码审查")],
      commands: [command("c1", { prompt: "  " })],
    });
    expect(entries[1]!.insert).toBe("tasks.quickCommands.defaultPrompt");
    expect(filterSlashEntries(entries, "审查").map((e) => e.id)).toEqual([
      "skill:s1",
    ]);
    expect(filterSlashEntries(entries, "")).toHaveLength(2);
  });
});

function open(online: boolean) {
  return renderSettings(
    "/o/o1/chat/c1",
    chatBase((call) => {
      if (call.method !== "GET") return undefined;
      if (call.path === `${ORG}/devices`)
        return json(200, {
          devices: [device("d1", { online })],
          next_cursor: null,
        });
      if (call.path === `${ORG}/spaces/s1/agents/a1`)
        return json(200, agent("a1"));
      if (call.path === `${ORG}/spaces/s1/agents/a1/skills`)
        return json(200, { skills: [skill("s1", "代码审查")] });
      if (call.path === `${ORG}/spaces/s1/quick-commands`)
        return json(200, { private: [command("c1")], shared: [] });
      if (call.path === `${ORG}/conversations/c1/messages`)
        return json(200, { messages: [message(1)], next_cursor: 0 });
      if (call.path === `${ORG}/conversations/c1`)
        return json(200, summary("c1"));
      if (call.path === `${ORG}/conversations`)
        return json(200, { conversations: [summary("c1")], next_cursor: null });
      return undefined;
    }),
  );
}

describe("对话斜杠菜单", () => {
  it("输入 / 列出技能与快捷指令，选择快捷指令只插入文字、不发送", async () => {
    const server = open(true);
    const user = userEvent.setup();
    const input = await screen.findByRole("textbox", { name: "输入消息" });
    await user.type(input, "/");
    const menu = await screen.findByRole("listbox", { name: "技能与快捷指令" });
    expect(menu).toBeInTheDocument();
    expect(
      await screen.findByRole("option", { name: /代码审查/ }),
    ).toBeInTheDocument();
    await user.click(await screen.findByRole("option", { name: /指令 c1/ }));
    await waitFor(() => expect(input).toHaveValue("请澄清本任务"));
    expect(
      screen.queryByRole("listbox", { name: "技能与快捷指令" }),
    ).toBeNull();
    // 选择只改输入框，不发送消息（打开会话时的「标记已读」POST 与此无关）
    expect(
      server.filter(
        (call) => call.method === "POST" && call.path.endsWith("/messages"),
      ),
    ).toEqual([]);
  });

  it("电脑离线：条目置灰并显示原因，点击不插入", async () => {
    open(false);
    const user = userEvent.setup();
    const input = await screen.findByRole("textbox", { name: "输入消息" });
    await user.type(input, "/");
    const option = await screen.findByRole("option", { name: /代码审查/ });
    expect(option).toHaveAttribute("aria-disabled", "true");
    expect(option).toHaveTextContent("电脑离线，暂时不能使用");
    await user.click(option);
    expect(input).toHaveValue("/");
  });

  it("路径中间的 / 不触发菜单（如 a/b）", async () => {
    open(true);
    const user = userEvent.setup();
    const input = await screen.findByRole("textbox", { name: "输入消息" });
    await user.type(input, "a/b");
    expect(
      screen.queryByRole("listbox", { name: "技能与快捷指令" }),
    ).toBeNull();
  });

  it("没有候选时 Enter 不被菜单吞掉，照常发送（如正文里的 /tmp 路径）", async () => {
    const server = open(true);
    const user = userEvent.setup();
    const input = await screen.findByRole("textbox", { name: "输入消息" });
    await user.type(input, "看一下 /tmp");
    await screen.findByRole("listbox", { name: "技能与快捷指令" });
    expect(screen.queryAllByRole("option")).toEqual([]);
    await user.keyboard("{Enter}");
    await waitFor(() =>
      expect(
        server.filter(
          (call) => call.method === "POST" && call.path.endsWith("/messages"),
        ),
      ).toHaveLength(1),
    );
  });
});
