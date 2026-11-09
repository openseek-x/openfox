import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";

import { apiError, findCall, json, renderSettings } from "../settings/testing";
import { ORG, TS, chatBase, message, run, summary } from "./fixtures";

afterEach(() => localStorage.clear());

const list = (
  items: ReturnType<typeof summary>[],
  next: string | null = null,
) => json(200, { conversations: items, next_cursor: next });

const sidebar = () => screen.getByRole("navigation", { name: "对话" });

describe("对话页签 · 三种导航模式", () => {
  it("微信模式：有置顶时分「已置顶 / 最近」两组，可折叠；无置顶时平铺", async () => {
    renderSettings(
      "/o/o1/chat",
      chatBase((call) => {
        if (call.path === `${ORG}/conversations`)
          return list([
            summary("c1", { title: "置顶的会话", pinned: true }),
            summary("c2", { title: "普通会话", status: "WAITING" }),
          ]);
        return undefined;
      }),
    );
    const nav = await waitFor(sidebar);
    expect(await within(nav).findByText("置顶的会话")).toBeInTheDocument();
    expect(
      within(nav).getByRole("button", { name: /已置顶/ }),
    ).toBeInTheDocument();
    expect(within(nav).getByText("等你回复")).toBeInTheDocument();
    await userEvent.click(within(nav).getByRole("button", { name: /已置顶/ }));
    expect(within(nav).queryByText("置顶的会话")).not.toBeInTheDocument();
    expect(within(nav).getByText("普通会话")).toBeInTheDocument();
  });

  it("底部切换到 Slack 模式与多会话模式，选择存本机", async () => {
    renderSettings(
      "/o/o1/chat",
      chatBase((call) => {
        if (call.path === `${ORG}/conversations`)
          return list([
            summary("c1", { title: "熊猫的会话" }),
            summary("c2", {
              title: "阿虎的会话",
              agent_id: "a2",
              agent: {
                id: "a2",
                name: "阿虎",
                title: "后端",
                avatar: null,
                online: true,
              },
            }),
          ]);
        return undefined;
      }),
    );
    const nav = await waitFor(sidebar);
    await within(nav).findByText("熊猫的会话");
    await userEvent.click(
      within(nav).getByRole("radio", { name: "Slack 模式" }),
    );
    expect(
      await within(nav).findByRole("button", { name: /直接对话/ }),
    ).toBeInTheDocument();
    expect(within(nav).getByText("熊猫")).toBeInTheDocument();
    expect(within(nav).getByText("阿虎")).toBeInTheDocument();
    expect(localStorage.getItem("openfox:chat-mode")).toBe("organized");

    await userEvent.click(
      within(nav).getByRole("radio", { name: "多会话模式" }),
    );
    // 按 Agent 分组：组头可折叠
    const group = await within(nav).findByRole("button", { name: /阿虎/ });
    expect(within(nav).getByText("阿虎的会话")).toBeInTheDocument();
    await userEvent.click(group);
    expect(within(nav).queryByText("阿虎的会话")).not.toBeInTheDocument();
    expect(localStorage.getItem("openfox:chat-mode")).toBe("sessions");
  });

  it("多会话模式：组内先显示 5 条，「展开显示 n」后可「收起显示」", async () => {
    localStorage.setItem("openfox:chat-mode", "sessions");
    const items = Array.from({ length: 7 }, (_, i) =>
      summary(`c${i}`, {
        title: `第 ${i} 个`,
        updated_at: `2026-10-29T08:0${9 - i}:00Z`,
      }),
    );
    renderSettings(
      "/o/o1/chat",
      chatBase((call) => {
        if (call.path === `${ORG}/conversations`) return list(items);
        return undefined;
      }),
    );
    const nav = await waitFor(sidebar);
    await within(nav).findByText("第 0 个");
    expect(within(nav).queryByText("第 5 个")).not.toBeInTheDocument();
    await userEvent.click(
      within(nav).getByRole("button", { name: "展开显示 2" }),
    );
    expect(within(nav).getByText("第 6 个")).toBeInTheDocument();
    await userEvent.click(
      within(nav).getByRole("button", { name: "收起显示" }),
    );
    expect(within(nav).queryByText("第 6 个")).not.toBeInTheDocument();
  });

  it("微信模式：··· 菜单行内重命名，Enter 保存发 PATCH title", async () => {
    const calls = renderSettings(
      "/o/o1/chat",
      chatBase((call) => {
        if (call.method === "PATCH" && call.path === `${ORG}/conversations/c1`)
          return json(200, summary("c1", { title: "新名字" }));
        if (call.path === `${ORG}/conversations`)
          return list([summary("c1", { title: "旧名字" })]);
        return undefined;
      }),
    );
    const nav = await waitFor(sidebar);
    await within(nav).findByText("旧名字");
    await userEvent.click(
      within(nav).getByRole("button", { name: "对话操作" }),
    );
    await userEvent.click(
      await screen.findByRole("menuitem", { name: "重命名" }),
    );
    const input = within(nav).getByRole("textbox", { name: "对话标题" });
    await userEvent.clear(input);
    await userEvent.type(input, "新名字{Enter}");
    await waitFor(() =>
      expect(findCall(calls, "PATCH", `${ORG}/conversations/c1`)?.body).toEqual(
        {
          title: "新名字",
        },
      ),
    );
    expect(await within(nav).findByText("新名字")).toBeInTheDocument();
  });

  it("任务会话的菜单里没有重命名；置顶走 PUT pin 并乐观移到「已置顶」", async () => {
    const calls = renderSettings(
      "/o/o1/chat",
      chatBase((call) => {
        if (
          call.method === "PUT" &&
          call.path === `${ORG}/conversations/c1/pin`
        )
          return json(204);
        if (call.path === `${ORG}/conversations`)
          return list([
            summary("c1", {
              title: "任务会话",
              updated_at: "2026-10-29T09:00:00Z",
              task_id: "t1",
              task: { id: "t1", display_id: "DEF-1", title: "任务会话" },
            }),
            summary("c2", { title: "另一个" }),
          ]);
        return undefined;
      }),
    );
    const nav = await waitFor(sidebar);
    await within(nav).findByText("任务会话");
    await userEvent.click(
      within(nav).getAllByRole("button", { name: "对话操作" })[0]!,
    );
    expect(
      screen.queryByRole("menuitem", { name: "重命名" }),
    ).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("menuitem", { name: "置顶" }));
    expect(
      await within(nav).findByRole("button", { name: /已置顶/ }),
    ).toBeInTheDocument();
    expect(findCall(calls, "PUT", `${ORG}/conversations/c1/pin`)).toBeTruthy();
  });

  it("多会话模式归档后 toast「已归档的聊天｜撤销」，撤销发 archived:false", async () => {
    localStorage.setItem("openfox:chat-mode", "sessions");
    const calls = renderSettings(
      "/o/o1/chat",
      chatBase((call) => {
        if (
          call.method === "PATCH" &&
          call.path === `${ORG}/conversations/c1`
        ) {
          const archived = (call.body as { archived: boolean }).archived;
          return json(
            200,
            summary("c1", {
              title: "要归档的",
              archived_at: archived ? "2026-10-29T09:00:00Z" : null,
            }),
          );
        }
        if (call.path === `${ORG}/conversations`)
          return list([summary("c1", { title: "要归档的" })]);
        return undefined;
      }),
    );
    const nav = await waitFor(sidebar);
    await within(nav).findByText("要归档的");
    await userEvent.click(within(nav).getByRole("button", { name: "归档" }));
    expect(await screen.findByText("已归档的聊天")).toBeInTheDocument();
    expect(within(nav).queryByText("要归档的")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "撤销" }));
    await waitFor(() =>
      expect(
        calls.filter((c) => c.method === "PATCH").map((c) => c.body),
      ).toEqual([{ archived: true }, { archived: false }]),
    );
    expect(await within(nav).findByText("要归档的")).toBeInTheDocument();
  });

  it("初次加载失败显示「加载对话失败｜重试」；刷新失败时保留旧列表", async () => {
    let fail = true;
    renderSettings(
      "/o/o1/chat",
      chatBase((call) => {
        if (call.path === `${ORG}/conversations`)
          return fail
            ? apiError(500, "INTERNAL_ERROR")
            : list([summary("c1", { title: "旧列表" })]);
        return undefined;
      }),
    );
    const nav = await waitFor(sidebar);
    expect(await within(nav).findByText("加载对话失败")).toBeInTheDocument();
    fail = false;
    await userEvent.click(within(nav).getByRole("button", { name: "重试" }));
    expect(await within(nav).findByText("旧列表")).toBeInTheDocument();
    // 再次刷新失败：旧列表保留，顶部提示
    fail = true;
    await userEvent.click(
      within(nav).getByRole("radio", { name: "多会话模式" }),
    );
    await userEvent.click(within(nav).getByRole("radio", { name: "微信模式" }));
    expect(within(nav).getByText("旧列表")).toBeInTheDocument();
  });
});

describe("对话页", () => {
  it("进入会话：拉最新一页（order=desc），向上翻页带 before，已读上报最大 seq", async () => {
    const calls = renderSettings(
      "/o/o1/chat/c1",
      chatBase((call) => {
        if (call.path === `${ORG}/conversations/c1/messages`) {
          const before = call.query.get("before");
          return before
            ? json(200, {
                messages: [message(1, { body: "最早的消息" })],
                next_cursor: 0,
              })
            : json(200, {
                messages: [message(2), message(3, { body: "最新的问题" })],
                next_cursor: 2,
              });
        }
        if (call.path === `${ORG}/conversations/c1/read`) return json(204);
        if (call.path === `${ORG}/conversations/c1`)
          return json(200, summary("c1", { unread: true, last_read_seq: 1 }));
        if (call.path === `${ORG}/conversations`) return list([summary("c1")]);
        return undefined;
      }),
    );
    expect(await screen.findByText("最新的问题")).toBeInTheDocument();
    const first = findCall(calls, "GET", `${ORG}/conversations/c1/messages`)!;
    expect(first.query.get("order")).toBe("desc");
    expect(first.query.get("before")).toBeNull();
    await waitFor(() =>
      expect(
        findCall(calls, "POST", `${ORG}/conversations/c1/read`)?.body,
      ).toEqual({
        seq: 3,
      }),
    );
    await userEvent.click(
      screen.getByRole("button", { name: "加载更早的消息" }),
    );
    expect(await screen.findByText("最早的消息")).toBeInTheDocument();
    const older = calls.filter(
      (c) =>
        c.path === `${ORG}/conversations/c1/messages` && c.query.get("before"),
    );
    expect(older[0]?.query.get("before")).toBe("2");
    expect(
      screen.queryByRole("button", { name: "加载更早的消息" }),
    ).not.toBeInTheDocument();
  });

  it("发送：Enter 发送并自动 @ Agent；失败后重试复用同一个 client_message_id，成功后换新的", async () => {
    let attempts = 0;
    const calls = renderSettings(
      "/o/o1/chat/c1",
      chatBase((call) => {
        if (
          call.method === "POST" &&
          call.path === `${ORG}/conversations/c1/messages`
        ) {
          attempts += 1;
          if (attempts === 1) return apiError(503, "UNAVAILABLE");
          const body = call.body as { body: string; client_message_id: string };
          return json(
            201,
            message(10, {
              body: body.body,
              client_message_id: body.client_message_id,
            }),
          );
        }
        if (call.path === `${ORG}/conversations/c1/messages`)
          return json(200, { messages: [], next_cursor: 0 });
        if (call.path === `${ORG}/conversations/c1`)
          return json(200, summary("c1"));
        if (call.path === `${ORG}/conversations`) return list([summary("c1")]);
        return undefined;
      }),
    );
    const box = await screen.findByRole("textbox", { name: "输入消息" });
    await userEvent.type(box, "帮我整理周报{Enter}");
    expect(await screen.findByText("发送失败，请重试")).toBeInTheDocument();
    expect(box).toHaveValue("帮我整理周报");
    await userEvent.type(box, "{Enter}");
    await waitFor(() => expect(box).toHaveValue(""));
    const posts = calls.filter(
      (c) =>
        c.method === "POST" && c.path === `${ORG}/conversations/c1/messages`,
    );
    const bodies = posts.map(
      (c) => c.body as { client_message_id: string; mentions: unknown },
    );
    expect(bodies).toHaveLength(2);
    expect(bodies[0]!.client_message_id).toBe(bodies[1]!.client_message_id);
    expect(bodies[1]!.mentions).toEqual([{ agent_id: "a1" }]);
    // 下一条消息用新的 client_message_id
    await userEvent.type(box, "第二条{Enter}");
    await waitFor(() =>
      expect(
        calls.filter(
          (c) =>
            c.method === "POST" &&
            c.path === `${ORG}/conversations/c1/messages`,
        ),
      ).toHaveLength(3),
    );
    const third = calls.filter(
      (c) =>
        c.method === "POST" && c.path === `${ORG}/conversations/c1/messages`,
    )[2]!.body as { client_message_id: string };
    expect(third.client_message_id).not.toBe(bodies[0]!.client_message_id);
  });

  it.each(["PRIVATE", "SHARED"] as const)(
    "%s 会话：仅共享会话常驻提及按钮，两种会话均可插入 Agent 提及",
    async (visibility) => {
      renderSettings(
        "/o/o1/chat/c1",
        chatBase((call) => {
          if (call.path === `${ORG}/members`)
            return json(200, {
              members: [
                { user_id: "u1", email: "lin@openfox.dev", status: "ACTIVE" },
              ],
              next_cursor: null,
            });
          if (call.path === `${ORG}/conversations/c1/messages`)
            return json(200, { messages: [], next_cursor: 0 });
          if (call.path === `${ORG}/conversations/c1`)
            return json(200, summary("c1", { visibility }));
          if (call.path === `${ORG}/conversations`)
            return list([summary("c1", { visibility })]);
          return undefined;
        }),
      );
      const input = await screen.findByRole("textbox", { name: "输入消息" });
      if (visibility === "PRIVATE") {
        expect(
          screen.queryByRole("button", { name: "提及成员" }),
        ).not.toBeInTheDocument();
        await userEvent.type(input, "@");
      } else {
        await userEvent.click(
          await screen.findByRole("button", { name: "提及成员" }),
        );
      }
      const menu = await screen.findByRole("listbox");
      await userEvent.click(within(menu).getByText("熊猫"));
      expect(input).toHaveValue("@熊猫 ");
      await waitFor(() => expect(input).toHaveFocus());
    },
  );

  it("Shift+Enter 换行不发送", async () => {
    const calls = renderSettings(
      "/o/o1/chat/c1",
      chatBase((call) => {
        if (call.path === `${ORG}/conversations/c1/messages`)
          return json(200, { messages: [], next_cursor: 0 });
        if (call.path === `${ORG}/conversations/c1`)
          return json(200, summary("c1"));
        if (call.path === `${ORG}/conversations`) return list([summary("c1")]);
        return undefined;
      }),
    );
    const box = await screen.findByRole("textbox", { name: "输入消息" });
    await userEvent.type(box, "第一行{Shift>}{Enter}{/Shift}第二行");
    expect(box).toHaveValue("第一行\n第二行");
    expect(
      calls.some((c) => c.method === "POST" && c.path.endsWith("/messages")),
    ).toBe(false);
  });

  it("提及保留输入焦点与后文，邮件不触发，空候选和中文合成回车不发送", async () => {
    const calls = renderSettings(
      "/o/o1/chat/c1",
      chatBase((call) => {
        if (call.path === `${ORG}/conversations/c1/messages`)
          return json(200, { messages: [], next_cursor: 0 });
        if (call.path === `${ORG}/conversations/c1`)
          return json(200, summary("c1"));
        return undefined;
      }),
    );
    const box = (await screen.findByRole("textbox", {
      name: "输入消息",
    })) as HTMLTextAreaElement;
    await userEvent.type(box, "qa@example.test");
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
      await userEvent.clear(box);
      await userEvent.type(box, "请检查@熊");
      expect(await screen.findByRole("option", { name: "熊猫" })).toBeVisible();
      await userEvent.keyboard("{Escape}");
    await userEvent.clear(box);
    await userEvent.type(box, "前文 @熊 后文");
    box.setSelectionRange(5, 5);
    fireEvent.select(box);
    expect(await screen.findByRole("option", { name: "熊猫" })).toBeVisible();
    expect(box).toHaveFocus();
    await userEvent.keyboard("{Enter}");
    expect(box).toHaveValue("前文 @熊猫  后文");
    expect(box.selectionStart).toBe(7);
    await userEvent.clear(box);
    await userEvent.type(box, "@nobody{Enter}");
    expect(box).toHaveValue("@nobody");
    fireEvent.compositionStart(box);
    fireEvent.keyDown(box, { key: "Enter", keyCode: 229, isComposing: true });
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
    fireEvent.compositionEnd(box);
    await userEvent.keyboard("{Escape}");
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
    expect(calls.some((call) => call.method === "POST")).toBe(false);
  });

  it("运行中显示「停止」：POST /runs/{run}/cancel 带 Idempotency-Key，重复点击用同一个键", async () => {
    const running = summary("c1", {
      status: "WORKING",
      run: {
        id: "r1",
        state: "RUNNING",
        wait_reason: "",
        started_at: "2026-10-29T08:00:00Z",
        finished_at: null,
      },
    });
    let cancels = 0;
    const calls = renderSettings(
      "/o/o1/chat/c1",
      chatBase((call) => {
        if (call.method === "POST" && call.path === `${ORG}/runs/r1/cancel`) {
          cancels += 1;
          return cancels === 1
            ? apiError(503, "UNAVAILABLE")
            : json(200, {
                run: { id: "r1", state: "CANCEL_REQUESTED" },
                replayed: false,
              });
        }
        if (call.path === `${ORG}/conversations/c1/messages`)
          return json(200, {
            messages: [message(1, { run_id: "r1" })],
            next_cursor: 0,
          });
        if (call.path === `${ORG}/conversations/c1`) return json(200, running);
        if (call.path === `${ORG}/conversations`) return list([running]);
        return undefined;
      }),
    );
    expect(await screen.findByText(/工作中 ·/)).toBeInTheDocument();
    const stop = await screen.findByRole("button", { name: "停止" });
    await userEvent.click(stop);
    await waitFor(() => expect(cancels).toBe(1));
    await userEvent.click(await screen.findByRole("button", { name: "停止" }));
    await waitFor(() => expect(cancels).toBe(2));
    const keysUsed = calls
      .filter((c) => c.path === `${ORG}/runs/r1/cancel`)
      .map((c) => c.headers.get("Idempotency-Key"));
    expect(keysUsed[0]).toBeTruthy();
    expect(keysUsed[1]).toBe(keysUsed[0]);
  });

  it("Run 失败只写了 SYSTEM 消息时，消息上方仍挂该 Run 的时间线与 ModelError 卡片", async () => {
    const failed = summary("c1", { status: "ERROR" });
    renderSettings(
      "/o/o1/chat/c1",
      chatBase((call) => {
        if (call.method === "GET" && call.path === `${ORG}/runs/r9`)
          return json(
            200,
            run("r9", {
              state: "FAILED",
              failure_code: "RATE_LIMITED",
              finished_at: TS,
            }),
          );
        if (call.path === `${ORG}/conversations/c1/messages`)
          return json(200, {
            messages: [
              message(1),
              message(2, {
                author_kind: "SYSTEM",
                body: "The Agent run failed: RATE_LIMITED",
                run_id: "r9",
              }),
            ],
            next_cursor: 0,
          });
        if (call.path === `${ORG}/conversations/c1`) return json(200, failed);
        if (call.path === `${ORG}/conversations`) return list([failed]);
        return undefined;
      }),
    );
    expect(await screen.findByText("请求过于频繁")).toBeInTheDocument();
    expect(
      screen.getByText("The Agent run failed: RATE_LIMITED"),
    ).toBeInTheDocument();
  });

  it("新会话：?agent= 显示「开始和 {name} 对话」，第一条消息先建会话再发送", async () => {
    const calls = renderSettings(
      "/o/o1/chat?agent=a2",
      chatBase((call) => {
        if (call.method === "POST" && call.path === `${ORG}/conversations`)
          return json(201, {
            ...summary("new"),
            id: (call.body as { id: string }).id,
          });
        const posted = /\/conversations\/([^/]+)\/messages$/.exec(call.path);
        if (call.method === "POST" && posted)
          return json(
            201,
            message(1, { conversation_id: posted[1]!, body: "你好" }),
          );
        if (posted) return json(200, { messages: [], next_cursor: 0 });
        const detail = /\/conversations\/([^/]+)$/.exec(call.path);
        if (detail && call.path !== `${ORG}/conversations`)
          return json(200, summary(detail[1]!, { agent_id: "a2" }));
        if (call.path === `${ORG}/conversations`) return list([]);
        return undefined;
      }),
    );
    expect(await screen.findByText("开始和 阿虎 对话")).toBeInTheDocument();
    await userEvent.type(
      screen.getByRole("textbox", { name: "输入消息" }),
      "你好{Enter}",
    );
    await waitFor(() =>
      expect(findCall(calls, "POST", `${ORG}/conversations`)).toBeTruthy(),
    );
    const created = findCall(calls, "POST", `${ORG}/conversations`)!.body as {
      id: string;
      agent_id: string;
      space_id: string;
    };
    expect(created).toMatchObject({ agent_id: "a2", space_id: "s1" });
    await waitFor(() =>
      expect(
        findCall(calls, "POST", `${ORG}/conversations/${created.id}/messages`),
      ).toBeTruthy(),
    );
  });

  it("Agent 消息用安全 Markdown 渲染：链接只放行 http(s) 并带 rel，javascript: 不生成链接", async () => {
    renderSettings(
      "/o/o1/chat/c1",
      chatBase((call) => {
        if (call.path === `${ORG}/conversations/c1/messages`)
          return json(200, {
            messages: [
              message(2, {
                body: "**完成**\n\n- [文档](https://example.com/doc)\n- [坏链接](javascript:alert(1))\n\n<img src=x onerror=alert(1)>",
              }),
            ],
            next_cursor: 0,
          });
        if (call.path === `${ORG}/conversations/c1`)
          return json(200, summary("c1"));
        if (call.path === `${ORG}/conversations`) return list([summary("c1")]);
        return undefined;
      }),
    );
    const link = await screen.findByRole("link", { name: "文档" });
    expect(link).toHaveAttribute("href", "https://example.com/doc");
    expect(link).toHaveAttribute("rel", "noopener noreferrer");
    expect(
      screen.queryByRole("link", { name: "坏链接" }),
    ).not.toBeInTheDocument();
    expect(screen.getByText("完成").tagName).toBe("STRONG");
    expect(document.querySelector("img")).toBeNull();
  });

  it("首页侧栏「对话」分组用新列表显示前 5 条与状态字", async () => {
    renderSettings(
      "/o/o1/inbox",
      chatBase((call) => {
        if (call.path === `${ORG}/conversations`)
          return list(
            Array.from({ length: 6 }, (_, i) =>
              summary(`c${i}`, {
                title: `首页会话 ${i}`,
                status: i === 0 ? "ERROR" : "IDLE",
                updated_at: `2026-10-29T08:0${9 - i}:00Z`,
              }),
            ),
          );
        return undefined;
      }),
    );
    expect(await screen.findByText("首页会话 0")).toBeInTheDocument();
    expect(screen.getByText("执行异常")).toBeInTheDocument();
    expect(screen.queryByText("首页会话 5")).not.toBeInTheDocument();
  });
});
