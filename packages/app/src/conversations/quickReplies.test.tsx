import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";

import type { QuickReply } from "@openfox/api";

import { apiError, json, renderSettings } from "../settings/testing";
import { ORG, chatBase, summary } from "./fixtures";

// 快捷回复：输入框菜单（个人 + 组织）、插入不发送、管理对话框（新建 / 编辑 / 删除 / 排序 / 上限 / 权限）。

afterEach(() => localStorage.clear());

const QR = `${ORG}/quick-replies`;
const MESSAGES = `${ORG}/conversations/c1/messages`;

const reply = (id: string, extra: Partial<QuickReply> = {}): QuickReply => ({
  id,
  scope: "PERSONAL",
  owner_user_id: "u1",
  title: `标题 ${id}`,
  body: `内容 ${id}`,
  position: 0,
  version: 1,
  can_edit: true,
  created_at: "2026-11-01T00:00:00Z",
  updated_at: "2026-11-01T00:00:00Z",
  ...extra,
});

function open(
  initial: { personal: QuickReply[]; organization: QuickReply[] },
  route: Parameters<typeof renderSettings>[1] = () => undefined,
  role: "OWNER" | "ADMIN" | "MEMBER" = "OWNER",
) {
  let state = initial;
  const calls = renderSettings(
    "/o/o1/chat/c1",
    chatBase((call) => {
      const custom = route(call);
      if (custom) return custom;
      if (call.method === "GET" && call.path === QR) return json(200, state);
      if (call.method === "GET" && call.path === MESSAGES)
        return json(200, { messages: [], next_cursor: 0 });
      if (call.path === `${ORG}/conversations/c1`)
        return json(200, summary("c1"));
      return undefined;
    }),
    role,
  );
  return { calls, set: (next: typeof initial) => (state = next) };
}

describe("快捷回复", () => {
  it("紧凑菜单显示个人和组织标题、插入和添加入口，不重复正文与分组", async () => {
    open({
      personal: [reply("p1", { title: "收到", body: "收到，谢谢" })],
      organization: [
        reply("o1", {
          scope: "ORG",
          title: "欢迎",
          body: "欢迎加入",
          can_edit: false,
        }),
      ],
    });
    const button = await screen.findByRole("button", { name: "快捷回复" });
    expect(button).toBeEnabled();
    expect(
      screen.queryByRole("button", { name: /快捷回复.*即将开放/ }),
    ).not.toBeInTheDocument();
    await userEvent.click(button);
    const menu = await screen.findByRole("dialog", { name: "快捷回复" });
    expect(await within(menu).findByText("收到")).toBeInTheDocument();
    expect(within(menu).queryByText("我的快捷回复")).not.toBeInTheDocument();
    expect(within(menu).queryByText("组织快捷回复")).not.toBeInTheDocument();
    expect(within(menu).queryByRole("textbox")).not.toBeInTheDocument();
    expect(within(menu).queryByText("收到，谢谢")).not.toBeInTheDocument();
    expect(
      within(menu).getByRole("button", { name: "添加快捷回复" }),
    ).toBeEnabled();
    expect(within(menu).getByText("欢迎")).toBeInTheDocument();
  });

  it("选择后把正文插入光标处，不自动发送", async () => {
    const { calls } = open({
      personal: [reply("p1", { title: "收到", body: "收到，谢谢" })],
      organization: [],
    });
    const box = await screen.findByRole("textbox", { name: "输入消息" });
    await userEvent.type(box, "你好 ");
    await userEvent.click(screen.getByRole("button", { name: "快捷回复" }));
    await userEvent.click(await screen.findByText("收到"));
    await waitFor(() => expect(box).toHaveValue("你好 收到，谢谢"));
    expect(calls.some((c) => c.method === "POST")).toBe(false);
  });

  it("管理：新建个人快捷回复（客户端生成 id，重试幂等）", async () => {
    const { calls, set } = open({ personal: [], organization: [] }, (call) => {
      if (call.method === "POST" && call.path === QR) {
        const body = call.body as { id: string; title: string; body: string };
        set({ personal: [reply(body.id, body)], organization: [] });
        return json(201, reply(body.id, body));
      }
      return undefined;
    });
    await userEvent.click(
      await screen.findByRole("button", { name: "快捷回复" }),
    );
    await userEvent.click(
      await screen.findByRole("button", { name: "管理快捷回复…" }),
    );
    const dialog = await screen.findByRole("dialog");
    await userEvent.click(
      within(dialog).getByRole("button", { name: "新建快捷回复" }),
    );
    await userEvent.type(within(dialog).getByLabelText("标题"), "稍后");
    await userEvent.type(within(dialog).getByLabelText("内容"), "我稍后处理");
    await userEvent.click(within(dialog).getByRole("button", { name: "保存" }));
    await waitFor(() =>
      expect(
        calls.find((c) => c.method === "POST" && c.path === QR),
      ).toBeTruthy(),
    );
    const post = calls.find((c) => c.method === "POST" && c.path === QR)!;
    expect(post.body).toMatchObject({
      scope: "PERSONAL",
      title: "稍后",
      body: "我稍后处理",
    });
    expect((post.body as { id: string }).id).toMatch(/[0-9a-f-]{36}/);
    expect(await within(dialog).findByText("稍后")).toBeInTheDocument();
  });

  it("添加入口直接进入空表单，取消后再次打开仍为新建；行内插入不发送", async () => {
    const { calls } = open({
      personal: [reply("p1", { title: "收到", body: "收到，谢谢" })],
      organization: [],
    });
    const box = await screen.findByRole("textbox", { name: "输入消息" });
    const quick = screen.getByRole("button", { name: "快捷回复" });
    await userEvent.click(quick);
    await userEvent.click(
      await screen.findByRole("button", { name: "添加快捷回复" }),
    );
    const title = await screen.findByLabelText("标题");
    await userEvent.type(title, "不保存");
    await userEvent.keyboard("{Escape}");
    await userEvent.click(quick);
    await userEvent.click(
      await screen.findByRole("button", { name: "添加快捷回复" }),
    );
    expect(await screen.findByLabelText("标题")).toHaveValue("");
    await userEvent.keyboard("{Escape}");
    await userEvent.click(quick);
    await userEvent.click(
      await screen.findByRole("button", { name: "把「收到」插入到输入框" }),
    );
    expect(box).toHaveValue("收到，谢谢");
    await waitFor(() => expect(box).toHaveFocus());
    expect(calls.some((call) => call.method === "POST")).toBe(false);
  });

  it("管理：达到上限时提示，编辑带 version，删除与排序调用对应接口", async () => {
    const { calls } = open(
      {
        personal: [
          reply("p1", { title: "甲", version: 4 }),
          reply("p2", { title: "乙", position: 1 }),
        ],
        organization: [],
      },
      (call) => {
        if (call.method === "POST" && call.path === QR)
          return apiError(409, "QUICK_REPLY_LIMIT");
        if (call.method === "PATCH" && call.path === `${QR}/p1`)
          return json(200, reply("p1", { title: "甲改", version: 5 }));
        if (call.method === "DELETE" && call.path === `${QR}/p2`)
          return new Response(null, { status: 204 });
        if (call.method === "PUT" && call.path === `${QR}/order`)
          return json(200, {
            quick_replies: [
              reply("p2", { title: "乙", position: 0 }),
              reply("p1", { title: "甲改", position: 1, version: 5 }),
            ],
          });
        return undefined;
      },
    );
    await userEvent.click(
      await screen.findByRole("button", { name: "快捷回复" }),
    );
    await userEvent.click(
      await screen.findByRole("button", { name: "管理快捷回复…" }),
    );
    const dialog = await screen.findByRole("dialog");
    // 编辑
    await userEvent.click(
      await within(dialog).findByRole("button", { name: "编辑 甲" }),
    );
    const title = within(dialog).getByLabelText("标题");
    await userEvent.clear(title);
    await userEvent.type(title, "甲改");
    await userEvent.click(within(dialog).getByRole("button", { name: "保存" }));
    await waitFor(() =>
      expect(calls.find((c) => c.method === "PATCH")).toBeTruthy(),
    );
    expect(calls.find((c) => c.method === "PATCH")!.body).toMatchObject({
      version: 4,
      title: "甲改",
    });
    // 排序：乙上移
    await userEvent.click(
      within(dialog).getByRole("button", { name: "上移 乙" }),
    );
    await waitFor(() =>
      expect(calls.find((c) => c.method === "PUT")).toBeTruthy(),
    );
    expect(calls.find((c) => c.method === "PUT")!.body).toEqual({
      scope: "PERSONAL",
      ids: ["p2", "p1"],
    });
    // 删除
    await userEvent.click(
      within(dialog).getByRole("button", { name: "删除 乙" }),
    );
    await waitFor(() =>
      expect(calls.find((c) => c.method === "DELETE")).toBeTruthy(),
    );
    // 上限
    await userEvent.click(
      within(dialog).getByRole("button", { name: "新建快捷回复" }),
    );
    await userEvent.type(within(dialog).getByLabelText("标题"), "新");
    await userEvent.type(within(dialog).getByLabelText("内容"), "内容");
    await userEvent.click(within(dialog).getByRole("button", { name: "保存" }));
    expect(
      await screen.findByText("快捷回复已达 50 条上限，请先删除不用的"),
    ).toBeInTheDocument();
  });

  it("组织页签：普通成员只读，不显示新建与编辑按钮", async () => {
    open(
      {
        personal: [],
        organization: [
          reply("o1", { scope: "ORG", title: "欢迎", can_edit: false }),
        ],
      },
      () => undefined,
      "MEMBER",
    );
    await userEvent.click(
      await screen.findByRole("button", { name: "快捷回复" }),
    );
    await userEvent.click(
      await screen.findByRole("button", { name: "管理快捷回复…" }),
    );
    const dialog = await screen.findByRole("dialog");
    await userEvent.click(within(dialog).getByRole("tab", { name: "组织" }));
    expect(await within(dialog).findByText("欢迎")).toBeInTheDocument();
    expect(
      within(dialog).getByText("只有组织所有者和管理员可以维护组织快捷回复"),
    ).toBeInTheDocument();
    expect(
      within(dialog).queryByRole("button", { name: "新建快捷回复" }),
    ).not.toBeInTheDocument();
    expect(
      within(dialog).queryByRole("button", { name: "编辑 欢迎" }),
    ).not.toBeInTheDocument();
  });

  it("组织页签：管理员可以新建组织快捷回复（scope=ORG）", async () => {
    const { calls } = open(
      { personal: [], organization: [] },
      (call) =>
        call.method === "POST" && call.path === QR
          ? json(201, reply("o9", { scope: "ORG" }))
          : undefined,
      "ADMIN",
    );
    await userEvent.click(
      await screen.findByRole("button", { name: "快捷回复" }),
    );
    await userEvent.click(
      await screen.findByRole("button", { name: "管理快捷回复…" }),
    );
    const dialog = await screen.findByRole("dialog");
    await userEvent.click(within(dialog).getByRole("tab", { name: "组织" }));
    await userEvent.click(
      within(dialog).getByRole("button", { name: "新建快捷回复" }),
    );
    await userEvent.type(within(dialog).getByLabelText("标题"), "公告");
    await userEvent.type(within(dialog).getByLabelText("内容"), "请注意");
    await userEvent.click(within(dialog).getByRole("button", { name: "保存" }));
    await waitFor(() =>
      expect(
        calls.find((c) => c.method === "POST" && c.path === QR),
      ).toBeTruthy(),
    );
    expect(
      calls.find((c) => c.method === "POST" && c.path === QR)!.body,
    ).toMatchObject({
      scope: "ORG",
      title: "公告",
    });
  });
});
