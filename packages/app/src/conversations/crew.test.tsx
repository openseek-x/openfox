import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";

import { crew as crewFixture, entry } from "../agents/fixtures";
import { json, renderSettings } from "../settings/testing";
import { ORG, chatBase, message, summary } from "./fixtures";

// 小队对话：?crew= 进入新的小队对话、@ 成员、按作者显示回复、小队不可用的空态。

afterEach(() => localStorage.clear());

const MESSAGES = `${ORG}/conversations/c1/messages`;
const entries = [
  entry("a1", { name: "队长" }),
  entry("a2", { name: "队员" }),
  entry("k1", {
    type: "crew",
    name: "产品小队",
    leader: { id: "a1", name: "队长", title: "" },
  }),
];

function open(
  path: string,
  route: Parameters<typeof renderSettings>[1] = () => undefined,
) {
  return renderSettings(
    path,
    chatBase(
      (call) => {
        const custom = route(call);
        if (custom) return custom;
        if (call.method === "GET" && call.path === `${ORG}/spaces/s1/crews/k1`)
          return json(200, crewFixture("k1", { name: "产品小队" }));
        return undefined;
      },
      { entries },
    ),
  );
}

describe("小队对话", () => {
  it("?crew= 不再显示「即将开放」，输入框提示由队长处理", async () => {
    open("/o/o1/chat?crew=k1");
    expect(
      await screen.findByPlaceholderText(/不 @ 成员时由队长 队长 处理/),
    ).toBeInTheDocument();
    expect(screen.queryByText(/即将开放/)).not.toBeInTheDocument();
    expect(screen.getByText("产品小队")).toBeInTheDocument();
    expect(screen.getByText("小队")).toBeInTheDocument();
    // 小队没有单一的 Runtime / 模型
    expect(screen.queryByText("默认模型")).not.toBeInTheDocument();
  });

  it("第一条消息以 crew_id 创建会话，没有 @ 时不带 Agent 提及（由队长处理）", async () => {
    const calls = open("/o/o1/chat?crew=k1", (call) => {
      if (call.method === "POST" && call.path === `${ORG}/conversations`)
        return json(201, summary("c9", { crew_id: "k1" }));
      if (call.method === "POST" && call.path.endsWith("/messages"))
        return json(201, message(1, { conversation_id: "c9" }));
      return undefined;
    });
    const box = await screen.findByRole("textbox", { name: "输入消息" });
    await userEvent.type(box, "大家好{Enter}");
    await waitFor(() =>
      expect(
        calls.find((c) => c.method === "POST" && c.path.endsWith("/messages")),
      ).toBeTruthy(),
    );
    const created = calls.find(
      (c) => c.method === "POST" && c.path === `${ORG}/conversations`,
    )!;
    expect(created.body).toMatchObject({ space_id: "s1", crew_id: "k1" });
    expect(created.body).not.toHaveProperty("agent_id");
    const post = calls.find(
      (c) => c.method === "POST" && c.path.endsWith("/messages"),
    )!;
    expect(post.body).toMatchObject({ body: "大家好" });
    expect(post.body).not.toHaveProperty("mentions");
  });

  it("@ 列表是小队成员，选择后只 @ 被选中的成员", async () => {
    const calls = open("/o/o1/chat?crew=k1", (call) => {
      if (call.method === "POST" && call.path === `${ORG}/conversations`)
        return json(201, summary("c9", { crew_id: "k1" }));
      if (call.method === "POST" && call.path.endsWith("/messages"))
        return json(201, message(1, { conversation_id: "c9" }));
      return undefined;
    });
    const box = await screen.findByRole("textbox", { name: "输入消息" });
    await userEvent.click(screen.getByRole("button", { name: "提及成员" }));
    const menu = await screen.findByRole("listbox");
    expect(within(menu).getByText("队长")).toBeInTheDocument();
    await userEvent.click(within(menu).getByText("队员"));
    await userEvent.type(box, "看一下{Enter}");
    await waitFor(() =>
      expect(
        calls.find((c) => c.method === "POST" && c.path.endsWith("/messages")),
      ).toBeTruthy(),
    );
    const post = calls.find(
      (c) => c.method === "POST" && c.path.endsWith("/messages"),
    )!;
    expect(post.body).toMatchObject({ mentions: [{ agent_id: "a2" }] });
  });

  it("已有小队对话：头部显示小队名，成员回复上方显示作者", async () => {
    open("/o/o1/chat/c1", (call) => {
      if (call.method === "GET" && call.path === MESSAGES)
        return json(200, {
          messages: [
            message(1, { body: "请 @队员 看下" }),
            message(2, { body: "好的，我来处理", author_agent_id: "a2" }),
          ],
          next_cursor: 0,
        });
      if (call.path === `${ORG}/conversations/c1`)
        return json(
          200,
          summary("c1", {
            crew_id: "k1",
            agent: {
              id: "a1",
              name: "队长",
              title: "",
              avatar: null,
              online: true,
            },
          }),
        );
      return undefined;
    });
    expect(await screen.findByText("好的，我来处理")).toBeInTheDocument();
    const reply = screen.getByText("好的，我来处理").closest("[data-seq]")!;
    expect(within(reply as HTMLElement).getByText("队员")).toBeInTheDocument();
    expect(screen.getAllByText("产品小队").length).toBeGreaterThan(0);
  });

  it("小队不存在或没有权限：显示空态，不创建会话", async () => {
    const calls = open("/o/o1/chat?crew=missing");
    expect(
      await screen.findByText("找不到这个小队，可能已归档或你没有访问权限"),
    ).toBeInTheDocument();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
  });
});
