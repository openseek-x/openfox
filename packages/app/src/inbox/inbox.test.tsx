import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { InboxGroup } from "@openfox/api";

import { App, createQueryClient } from "../App";
import { backend, json, ORG } from "../tasks/test-backend";

const groups: InboxGroup[] = [
  {
    id: "g1",
    notification_id: "n1",
    kind: "MENTION",
    task_id: "task-1",
    task_display_id: "open-1",
    task_title: "修复登录",
    task_status_category: "TODO",
    task_type_icon: null,
    space_id: "s1",
    space_name: "默认空间",
    actor_kind: "USER",
    actor_name: "wang@openfox.dev",
    safe_summary: "请看一下",
    count: 2,
    unread: true,
    latest_at: "2026-10-28T00:00:00Z",
  },
  {
    id: "g2",
    notification_id: "n2",
    kind: "TASK_COMMENT",
    task_id: "task-2",
    task_display_id: "open-2",
    task_title: "写文档",
    task_status_category: "DONE",
    task_type_icon: null,
    safe_summary: "新评论",
    count: 1,
    unread: false,
    latest_at: "2026-10-27T00:00:00Z",
  },
];
const counts = {
  inbox: 2,
  inbox_unread: 1,
  inbox_important_unread: 1,
  inbox_other_unread: 0,
  later: 0,
  cleared: 0,
};

function renderInbox(notifications = groups) {
  const server = backend((call) => {
    if (call.path.endsWith("/inbox") && call.method === "GET") {
      const section = call.query.get("section") ?? "inbox";
      return json(200, {
        groups: section === "inbox" ? notifications : [],
        counts,
      });
    }
    if (call.path.endsWith("/inbox/stats")) return json(200, { counts });
    if (
      call.path.endsWith("/inbox/actions") ||
      call.path.endsWith("/inbox/bulk")
    )
      return json(200, { affected: 1 });
    return undefined;
  });
  const queryClient = createQueryClient();
  queryClient.setDefaultOptions({ queries: { retry: false } });
  render(
    <MemoryRouter initialEntries={[`/o/${ORG}/inbox`]}>
      <App api={server.api} queryClient={queryClient} locale="zh-CN" />
    </MemoryRouter>,
  );
  return server;
}

beforeEach(() => {
  localStorage.setItem(`openfox:onboarding:${ORG}`, JSON.stringify({}));
});
afterEach(() => localStorage.clear());

describe("收件箱", () => {
  it("显示分区计数与事件文字；切换「重要」请求带 importance=primary", async () => {
    const user = userEvent.setup();
    const server = renderInbox();
    const list = await screen.findByRole("list", { name: "通知列表" });
    expect(within(list).getByText("修复登录")).toBeInTheDocument();
    expect(within(list).getByText("@了你")).toBeInTheDocument();
    expect(within(list).getByText("+1")).toBeInTheDocument();
    expect(
      screen.getByRole("tab", { name: /收件箱\s*1 条未读/ }),
    ).toBeInTheDocument();
    await user.click(screen.getByRole("radio", { name: "重要 1" }));
    await waitFor(() =>
      expect(
        server.calls.some(
          (c) =>
            c.path.endsWith("/inbox") &&
            c.query.get("importance") === "primary",
        ),
      ).toBe(true),
    );
  });

  it("隐藏零计数；离开收件箱后收起并重置重要程度筛选", async () => {
    const user = userEvent.setup();
    const server = renderInbox();
    await screen.findByRole("list", { name: "通知列表" });
    expect(screen.getByRole("tab", { name: "稍后" })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "已清理" })).toBeInTheDocument();
    await user.click(screen.getByRole("radio", { name: "重要 1" }));
    await user.click(screen.getByRole("tab", { name: "稍后" }));
    expect(await screen.findByText("没有稍后处理的通知")).toBeInTheDocument();
    expect(
      screen.queryByRole("radiogroup", { name: "重要程度" }),
    ).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "全部清理" })).toBeDisabled();
    expect(
      server.calls.some(
        (c) =>
          c.path.endsWith("/inbox") &&
          c.query.get("section") === "later" &&
          !c.query.has("importance"),
      ),
    ).toBe(true);
    await user.click(screen.getByRole("tab", { name: /收件箱/ }));
    expect(screen.getByRole("radio", { name: "全部 1" })).toHaveAttribute(
      "aria-checked",
      "true",
    );
  });

  it("会话通知显示来源，替换固定英文占位但保留真实摘要", async () => {
    renderInbox([
      {
        id: "reply-1",
        notification_id: "n3",
        kind: "REPLY",
        actor_kind: "AGENT",
        actor_name: "文档助手",
        task_type_icon: null,
        safe_summary: "An Agent replied in a conversation.",
        count: 1,
        unread: true,
        latest_at: groups[0]!.latest_at,
      },
      {
        id: "reply-2",
        notification_id: "n4",
        kind: "REPLY",
        actor_kind: "AGENT",
        actor_name: "代码助手",
        task_type_icon: null,
        safe_summary: "已完成登录修复，验证通过",
        count: 1,
        unread: false,
        latest_at: groups[0]!.latest_at,
      },
    ]);
    const list = await screen.findByRole("list", { name: "通知列表" });
    expect(
      within(list).getByRole("button", { name: "文档助手" }),
    ).toBeInTheDocument();
    expect(within(list).getAllByText("回复了对话")).toHaveLength(2);
    expect(
      within(list).queryByText("An Agent replied in a conversation."),
    ).not.toBeInTheDocument();
    expect(
      within(list).getByText("已完成登录修复，验证通过"),
    ).toBeInTheDocument();
  });

  it("任务完成状态不会覆盖通知的未读标记", async () => {
    renderInbox([{ ...groups[1]!, unread: true }]);
    const list = await screen.findByRole("list", { name: "通知列表" });
    expect(within(list).getByRole("img", { name: "未读" })).toBeInTheDocument();
    expect(
      within(list).getByRole("img", { name: "已完成" }),
    ).toBeInTheDocument();
  });

  it("稍后：选预设后发送 snooze 请求并把该组移出列表", async () => {
    const user = userEvent.setup();
    const server = renderInbox();
    const list = await screen.findByRole("list", { name: "通知列表" });
    const row = within(list).getByText("修复登录").closest("li")!;
    await user.hover(row);
    await user.click(within(row).getByRole("button", { name: "稍后" }));
    await user.click(
      await screen.findByRole("menuitem", { name: /明天 08:00/ }),
    );
    await waitFor(() =>
      expect(server.find("POST", "/inbox/actions")).toHaveLength(1),
    );
    const body = server.find("POST", "/inbox/actions")[0]!.body as {
      action: string;
      section: string;
      group_ids: string[];
      snoozed_until: string;
    };
    expect(body.action).toBe("snooze");
    expect(body.section).toBe("inbox");
    expect(body.group_ids).toEqual(["g1"]);
    const at = new Date(body.snoozed_until);
    expect(at.getHours()).toBe(8);
    expect(at.getTime()).toBeGreaterThan(Date.now());
  });

  it("稍后输入无法识别的时间时提示错误", async () => {
    const user = userEvent.setup();
    renderInbox();
    const list = await screen.findByRole("list", { name: "通知列表" });
    const row = within(list).getByText("修复登录").closest("li")!;
    await user.click(within(row).getByRole("button", { name: "稍后" }));
    await user.type(
      await screen.findByRole("textbox", { name: "稍后处理" }),
      "随便哪天{Enter}",
    );
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "无法识别这个时间",
    );
  });

  it("全部清理需要确认，确认后按当前筛选调用 bulk clear_all", async () => {
    const user = userEvent.setup();
    const server = renderInbox();
    await screen.findByRole("list", { name: "通知列表" });
    await user.click(screen.getByRole("button", { name: "全部清理" }));
    const dialog = await screen.findByRole("dialog", { name: "全部清理？" });
    expect(dialog).toHaveTextContent("收件箱中的 2 条通知会移到「已清理」");
    expect(server.find("POST", "/inbox/bulk")).toHaveLength(0);
    await user.click(within(dialog).getByRole("button", { name: "全部清理" }));
    await waitFor(() =>
      expect(server.find("POST", "/inbox/bulk")).toHaveLength(1),
    );
    expect(server.find("POST", "/inbox/bulk")[0]!.body).toEqual({
      operation: "clear_all",
      section: "inbox",
    });
  });
});
