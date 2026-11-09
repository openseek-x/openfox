import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";

import type { DeviceDetail, SpaceAgent } from "@openfox/api";

import { agent, crew as crewFixture, device, entry } from "../agents/fixtures";
import { json, renderSettings } from "../settings/testing";
import { computerNoticeState } from "./ComputerNotice";
import { ORG, TS, chatBase, message, summary } from "./fixtures";

// Agent 电脑离线 / 未绑定电脑时，输入框上方显示非阻塞提示。

afterEach(() => localStorage.clear());

const MESSAGES = `${ORG}/conversations/c1/messages`;

function open(
  options: {
    online?: boolean;
    agent?: Partial<SpaceAgent>;
    role?: "OWNER" | "ADMIN" | "MEMBER";
    path?: string;
    extra?: Parameters<typeof renderSettings>[1];
  } = {},
) {
  return renderSettings(
    options.path ?? "/o/o1/chat/c1",
    chatBase((call) => {
      const custom = options.extra?.(call);
      if (custom) return custom;
      if (call.method !== "GET") return undefined;
      if (call.path === `${ORG}/devices`)
        return json(200, {
          devices: [device("d1", { online: options.online ?? true })],
          next_cursor: null,
        });
      if (call.path === `${ORG}/spaces/s1/agents/a1`)
        return json(200, agent("a1", options.agent));
      if (call.path === MESSAGES)
        return json(200, { messages: [message(1)], next_cursor: 0 });
      if (call.path === `${ORG}/conversations/c1`)
        return json(200, summary("c1"));
      if (call.path === `${ORG}/conversations`)
        return json(200, { conversations: [summary("c1")], next_cursor: null });
      return undefined;
    }),
    options.role,
  );
}

const OFFLINE = /电脑 d1 离线，消息会排队/;
const NO_COMPUTER = /这个 Agent 还没有绑定电脑/;
const composer = () => screen.findByRole("textbox");

describe("对话输入框上方的电脑提示", () => {
  it("电脑在线：不显示提示", async () => {
    open();
    expect(await screen.findByText("消息 1")).toBeInTheDocument();
    await composer();
    expect(screen.queryByText(OFFLINE)).toBeNull();
    expect(screen.queryByText(NO_COMPUTER)).toBeNull();
  });

  it("电脑离线：显示提示、「查看电脑」指向电脑设置，输入框仍可用", async () => {
    open({ online: false });
    const notice = (await screen.findByText(OFFLINE)).closest(
      '[role="status"], [role="alert"], div',
    ) as HTMLElement;
    expect(notice).toBeTruthy();
    const link = screen.getByRole("link", { name: "查看电脑" });
    expect(link).toHaveAttribute(
      "href",
      "/o/o1/settings/computers?computer=d1",
    );
    expect(
      screen.getByRole("button", { name: "更换电脑" }),
    ).toBeInTheDocument();
    expect(await composer()).not.toBeDisabled();
  });

  it("没有绑定电脑：显示未绑定提示，没有「查看电脑」", async () => {
    // 服务端没有配置版本时不返回 config（omitempty）
    open({ agent: { config: undefined } });
    expect(await screen.findByText(NO_COMPUTER)).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "查看电脑" })).toBeNull();
  });

  it("小助手离线：去查看打开绑定电脑的详情，关闭后仍可继续写需求", async () => {
    open({
      path: "/o/o1/assistant",
      online: false,
      extra: (call) => {
        if (call.path === `${ORG}/assistant`) {
          return json(200, {
            assistant: agent("as1", {
              name: "小助手",
              builtin_kind: "ASSISTANT",
              visibility: "PRIVATE",
            }),
          });
        }
        if (call.path === `${ORG}/assistant/sessions`)
          return json(200, { sessions: [] });
        if (call.path === `${ORG}/devices/d1`)
          return json(200, {
            ...device("d1", { online: false }),
            bound_agents: [],
            created_at: TS,
            timezone: "Asia/Shanghai",
          } satisfies DeviceDetail);
        return undefined;
      },
    });
    const user = userEvent.setup();
    const input = await screen.findByRole("textbox", { name: "输入消息" });
    await user.type(input, "草稿");
    expect(
      await screen.findByText("Agent 已离线，暂时无法响应。"),
    ).toBeInTheDocument();
    const view = screen.getByRole("button", {
      name: "查看电脑 电脑 d1 的详情",
    });
    expect(view).toHaveTextContent("去查看");
    await user.click(view);
    const drawer = await screen.findByRole("dialog", { name: "电脑详情" });
    expect(await within(drawer).findByText("电脑 d1")).toBeInTheDocument();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog", { name: "电脑详情" })).toBeNull();
    expect(input).toHaveValue("草稿");
  });

  it("不能编辑这个 Agent 的普通成员看不到「更换电脑」", async () => {
    open({ online: false, role: "MEMBER", agent: { owner_user_id: "u2" } });
    expect(await screen.findByText(OFFLINE)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "查看电脑" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "更换电脑" })).toBeNull();
  });

  it("组织管理员可以更换别人的 Agent 的电脑，点击打开 Agent 设置", async () => {
    const user = userEvent.setup();
    open({ online: false, role: "ADMIN", agent: { owner_user_id: "u2" } });
    await user.click(await screen.findByRole("button", { name: "更换电脑" }));
    // 抽屉是非模态的 complementary 区域
    expect(
      await screen.findByRole("complementary", { name: "a1 的设置" }),
    ).toBeInTheDocument();
  });

  it("小队对话不显示电脑提示", async () => {
    renderSettings(
      "/o/o1/chat?crew=k1",
      chatBase(
        (call) => {
          if (call.method !== "GET") return undefined;
          if (call.path === `${ORG}/spaces/s1/crews/k1`)
            return json(200, crewFixture("k1", { name: "产品小队" }));
          if (call.path === `${ORG}/devices`)
            return json(200, {
              devices: [device("d1", { online: false })],
              next_cursor: null,
            });
          return undefined;
        },
        {
          entries: [
            entry("a1", { name: "队长" }),
            entry("k1", {
              type: "crew",
              name: "产品小队",
              leader: { id: "a1", name: "队长", title: "" },
            }),
          ],
        },
      ),
    );
    expect(
      await screen.findByPlaceholderText(/由队长 队长 处理/),
    ).toBeInTheDocument();
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(screen.queryByText(/离线，消息会排队/)).toBeNull();
    expect(screen.queryByText(NO_COMPUTER)).toBeNull();
  });
});

describe("computerNoticeState", () => {
  const base = agent("a1");
  it("电脑列表优先，看不到这台电脑时退回 Agent 的 online", () => {
    expect(computerNoticeState(base, [device("d1")])).toEqual({ kind: "none" });
    expect(
      computerNoticeState(base, [device("d1", { online: false, name: "M3" })]),
    ).toEqual({ kind: "offline", computerId: "d1", name: "M3" });
    expect(
      computerNoticeState({ ...base, online: false, computer_name: null }, []),
    ).toEqual({ kind: "offline", computerId: "d1", name: null });
    expect(computerNoticeState({ ...base, config: undefined }, [])).toEqual({
      kind: "noComputer",
    });
    expect(
      computerNoticeState(
        { ...base, config: { ...base.config!, computer_id: "" } },
        [],
      ),
    ).toEqual({ kind: "noComputer" });
  });
});
