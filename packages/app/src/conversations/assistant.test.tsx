import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";

import { agent, device } from "../agents/fixtures";
import { findCall, json, renderSettings } from "../settings/testing";
import { ORG, TS, chatBase, message, run, summary } from "./fixtures";

afterEach(() => localStorage.clear());

const assistant = agent("as1", {
  name: "小助手",
  builtin_kind: "ASSISTANT",
  visibility: "PRIVATE",
});

const session = (id: string, extra: Record<string, unknown> = {}) => ({
  id,
  space_id: "s1",
  agent_id: "as1",
  title: `会话 ${id}`,
  lifecycle: "OPEN",
  version: 1,
  created_at: TS,
  updated_at: TS,
  ...extra,
});

describe("小助手", () => {
  it.each([
    ["创建任务", "create_task", "描述你想创建的任务…"],
    ["创建 Agent", "create_agent", "描述你想创建的 Agent…"],
  ] as const)(
    "%s：选择、移除、失败重试和历史标签",
    async (label, action, placeholder) => {
      const path = `${ORG}/conversations/c1/messages`;
      const history: ReturnType<typeof message>[] = [];
      let attempted = false;
      const calls = renderSettings(
        "/o/o1/assistant",
        chatBase((call) => {
          if (call.path === `${ORG}/assistant`) return json(200, { assistant });
          if (call.path === `${ORG}/assistant/sessions`)
            return json(200, { sessions: [session("c1")] });
          if (call.path === `${ORG}/conversations/c1`)
            return json(200, summary("c1", { agent_id: "as1" }));
          if (call.path === path && call.method === "POST") {
            if (!attempted) {
              attempted = true;
              return json(503, { error: { code: "UNAVAILABLE" } });
            }
            const sent = message(1, {
              body: "整理每周进展",
              assistant_action: action,
            });
            history.push(sent);
            return json(201, sent);
          }
          if (call.path === path)
            return json(200, { messages: history, next_cursor: 0 });
          if (call.path === `${ORG}/conversations`)
            return json(200, { conversations: [], next_cursor: null });
          return undefined;
        }),
      );
      const user = userEvent.setup();
      const card = () =>
        screen.findByRole("button", { name: new RegExp("^" + label) });
      await user.click(await card());
      const box = screen.getByPlaceholderText(placeholder);
      expect(box).toHaveFocus();
      const send = screen.getByRole("button", { name: "发送" });
      await user.click(send);
      expect(findCall(calls, "POST", path)).toBeUndefined();
      await user.type(box, "整理每周进展");
      await user.keyboard("{Home}{Backspace}");
      expect(box).toHaveValue("整理每周进展");
      await user.click(await card());
      await user.click(screen.getByRole("button", { name: "移除" + label }));
      expect(box).toHaveFocus();
      await user.click(await card());
      await user.keyboard("{Enter}");
      await waitFor(() =>
        expect(findCall(calls, "POST", path)?.body).toMatchObject({
          body: "整理每周进展",
          assistant_action: action,
        }),
      );
      expect(screen.getByPlaceholderText(placeholder)).toHaveValue(
        "整理每周进展",
      );
      await user.click(send);
      await screen.findByText("整理每周进展", { selector: "div" });
      expect(
        await screen.findByText(label, { selector: "span" }),
      ).toBeInTheDocument();
      const attempts = calls.filter(
        (c) => c.method === "POST" && c.path === path,
      );
      expect(attempts).toHaveLength(2);
      expect(attempts[1]!.body).toEqual(attempts[0]!.body);
      expect(screen.queryByRole("button", { name: "移除" + label })).toBeNull();
    },
  );

  it("重试较早的失败时只请求那次运行，不重发最后一条用户消息", async () => {
    const base = `${ORG}/conversations/c1`;
    const calls = renderSettings(
      "/o/o1/assistant",
      chatBase((call) => {
        if (call.path === `${ORG}/assistant`) return json(200, { assistant });
        if (call.path === `${ORG}/assistant/sessions`)
          return json(200, { sessions: [session("c1")] });
        if (call.path === base)
          return json(200, summary("c1", { agent_id: "as1" }));
        if (call.path === `${base}/messages`)
          return json(200, {
            messages: [
              message(1, { assistant_action: "create_task", run_id: "r1" }),
              message(2, {
                author_kind: "SYSTEM",
                body: "运行失败",
                run_id: "r1",
              }),
              message(3, { assistant_action: "create_agent" }),
            ],
            next_cursor: 0,
          });
        if (call.path === `${ORG}/runs/r1`)
          return json(
            200,
            run("r1", {
              agent_id: "as1",
              state: "FAILED",
              failure_code: "RATE_LIMITED",
              finished_at: TS,
            }),
          );
        if (call.path === `${base}/runs/r1/retry`) return json(204);
        return undefined;
      }),
    );
    await userEvent.click(await screen.findByRole("button", { name: /重试/ }));
    await waitFor(() =>
      expect(findCall(calls, "POST", `${base}/runs/r1/retry`)).toBeTruthy(),
    );
    expect(findCall(calls, "POST", `${base}/messages`)).toBeUndefined();
  });

  it("小助手不存在时先 PUT /assistant（选第一台可用电脑），成功后进入会话页", async () => {
    let created = false;
    const calls = renderSettings(
      "/o/o1/assistant",
      chatBase((call) => {
        if (call.path === `${ORG}/assistant` && call.method === "PUT") {
          created = true;
          return json(200, { assistant });
        }
        if (call.path === `${ORG}/assistant`)
          return json(200, { assistant: created ? assistant : null });
        if (call.path === `${ORG}/devices`)
          return json(200, {
            devices: [device("d0", { online: false }), device("d1")],
            next_cursor: null,
          });
        if (call.path === `${ORG}/assistant/sessions`)
          return json(200, { sessions: [] });
        if (call.path === `${ORG}/conversations`)
          return json(200, { conversations: [], next_cursor: null });
        return undefined;
      }),
    );
    await waitFor(() =>
      expect(findCall(calls, "PUT", `${ORG}/assistant`)?.body).toEqual({
        computer_id: "d1",
        grant_id: "g-d1",
        runtime: "codex",
      }),
    );
    // 能力卡
    expect(
      await screen.findByRole("button", { name: /创建 Agent/ }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /创建任务/ }),
    ).toBeInTheDocument();
    expect(calls.filter((c) => c.method === "PUT")).toHaveLength(1);
  });

  it("没有可用电脑时显示 Callout 并引导去电脑页，不调用 PUT", async () => {
    const calls = renderSettings(
      "/o/o1/assistant",
      chatBase((call) => {
        if (call.path === `${ORG}/assistant`)
          return json(200, { assistant: null });
        if (call.path === `${ORG}/devices`)
          return json(200, { devices: [], next_cursor: null });
        return undefined;
      }),
    );
    expect(await screen.findByText("需要一台在线电脑")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "去连接电脑" })).toHaveAttribute(
      "href",
      "/o/o1/settings/computers",
    );
    expect(findCall(calls, "PUT", `${ORG}/assistant`)).toBeUndefined();
  });

  function openProposal(action: "create_agent" | "create_task") {
    let state = "pending";
    let name = "整理周报";
    const base = `${ORG}/conversations/c1/messages`;
    const calls = renderSettings(
      "/o/o1/assistant",
      chatBase((call) => {
        if (call.path === `${ORG}/assistant`) return json(200, { assistant });
        if (call.path === `${ORG}/assistant/sessions`)
          return json(200, { sessions: [session("c1")] });
        if (call.path === `${ORG}/devices`)
          return json(200, {
            devices: [device("d1"), device("d2", { online: false })],
            next_cursor: null,
          });
        if (call.path === `${ORG}/spaces/s1/agents`)
          return json(200, { agents: [agent("a2", { name: "小周" })] });
        if (call.path === `${ORG}/members`)
          return json(200, {
            members: [
              {
                user_id: "u2",
                email: "zhou@example.test",
                full_name: "小周同学",
                status: "ACTIVE",
              },
            ],
            next_cursor: null,
          });
        if (call.path === base + "/p1/confirm") {
          state = "confirmed";
          const body = call.body as { name?: string; title?: string };
          name = body.name ?? body.title ?? name;
          return json(200, { proposal: {}, executed: {} });
        }
        if (call.path === base + "/p1/proposal") {
          state = "cancelled";
          return json(200, { proposal: {} });
        }
        if (call.path === base)
          return json(200, {
            messages: [
              message(1),
              message(2, {
                id: "p1",
                kind: "ACTION_PROPOSAL",
                body: "已生成：整理周报",
                payload: {
                  action,
                  status: state,
                  ...(action === "create_agent"
                    ? {
                        agent: {
                          name,
                          instructions: "整理每周进展",
                          description: "仅在旧版草稿里提供的说明",
                        },
                      }
                    : { task: { title: name, description: "本周进展" } }),
                },
              }),
              ...(state === "confirmed"
                ? [
                    message(3, {
                      author_kind: "AGENT",
                      kind: "ACTION_EXECUTED",
                      payload: {
                        action,
                        name,
                        space_id: "s1",
                        ...(action === "create_agent"
                          ? { agent_id: "a3" }
                          : { task_id: "t1", identifier: "DEF-1" }),
                      },
                    }),
                  ]
                : []),
            ],
            next_cursor: 0,
          });
        if (call.path === `${ORG}/conversations/c1`)
          return json(200, summary("c1", { agent_id: "as1" }));
        if (call.path === `${ORG}/conversations`)
          return json(200, { conversations: [], next_cursor: null });
        return undefined;
      }),
    );
    return { calls, base };
  }

  it("任务草稿先选空间再选 Agent，保留编辑和处理人，确认后可打开所建任务", async () => {
    const { calls, base } = openProposal("create_task");
    const user = userEvent.setup();
    const card = within(
      await screen.findByRole("region", { name: "创建任务草稿" }),
    );
    const title = card.getByRole("textbox", { name: "标题" });
    const confirm = card.getByRole("button", { name: "创建任务" });
    expect(card.getByRole("combobox", { name: "处理人" })).toHaveValue("u1");
    expect(card.getByRole("combobox", { name: "Agent" })).toBeDisabled();
    await user.clear(title);
    expect(confirm).toBeDisabled();
    await user.type(title, "整理第 44 周周报");
    await user.click(card.getByRole("button", { name: "空间" }));
    await user.type(
      await screen.findByRole("combobox", { name: "搜索空间" }),
      "DEF",
    );
    await user.click(await screen.findByRole("option", { name: "默认空间" }));
    await user.selectOptions(
      card.getByRole("combobox", { name: "处理人" }),
      "u2",
    );
    await user.selectOptions(
      card.getByRole("combobox", { name: "Agent" }),
      await card.findByRole("option", { name: "小周" }),
    );
    await user.click(confirm);
    expect(
      await screen.findByRole("link", {
        name: /任务已创建 DEF-1 整理第 44 周周报/,
      }),
    ).toHaveAttribute("href", "/o/o1/tasks/DEF-1");
    expect(findCall(calls, "POST", base + "/p1/confirm")?.body).toEqual({
      space_id: "s1",
      title: "整理第 44 周周报",
      description: "本周进展",
      assignee_user_id: "u2",
      executor_agent_id: "a2",
      start_working: false,
    });
    expect(
      screen.getByText("整理第 44 周周报", { selector: "span.font-medium" }),
    ).toBeInTheDocument();
  });

  it("Agent 草稿预选小助手的 Runtime，可修改电脑、指令和可见性，生成两处入口", async () => {
    const { calls, base } = openProposal("create_agent"),
      user = userEvent.setup();
    const card = within(
      await screen.findByRole("region", { name: "创建 Agent 草稿" }),
    );
    const picker = card.getByRole("combobox", { name: "电脑与 Runtime" });
    await waitFor(() => expect(picker).toHaveValue("d1:codex"));
    const confirm = card.getByRole("button", { name: "创建 Agent" });
    await user.selectOptions(picker, "");
    expect(confirm).toBeDisabled();
    await user.selectOptions(picker, "d2:codex");
    await user.selectOptions(
      card.getByRole("combobox", { name: "可见性" }),
      "SHARED",
    );
    const instructions = card.getByRole("textbox", { name: "指令" });
    await user.clear(instructions);
    await user.type(instructions, "每周二汇总");
    await user.click(confirm);
    expect(
      await screen.findByRole("link", { name: "查看 Agent" }),
    ).toHaveAttribute("href", "/o/o1/members?contact=agent:a3");
    expect(screen.getByRole("link", { name: "去设置" })).toHaveAttribute(
      "href",
      "/o/o1/members?contact=agent:a3&agentSettings=a3",
    );
    expect(findCall(calls, "POST", base + "/p1/confirm")?.body).toEqual({
      space_id: "s1",
      name: "整理周报",
      instructions: "每周二汇总",
      visibility: "SHARED",
      computer_id: "d2",
      grant_id: "g-d2",
      runtime: "codex",
    });
  });

  it("取消待确认草稿后只保留取消状态", async () => {
    const { calls, base } = openProposal("create_agent");
    const card = within(
      await screen.findByRole("region", { name: "创建 Agent 草稿" }),
    );
    await userEvent.click(card.getByRole("button", { name: "取消" }));
    expect(await screen.findByText("草稿已取消")).toBeInTheDocument();
    expect(
      screen.queryByRole("region", { name: "创建 Agent 草稿" }),
    ).toBeNull();
    expect(findCall(calls, "DELETE", base + "/p1/proposal")).toBeTruthy();
    expect(findCall(calls, "POST", base + "/p1/confirm")).toBeUndefined();
  });

  it("会话下拉：搜索、切换会话、已归档入口", async () => {
    renderSettings(
      "/o/o1/assistant",
      chatBase((call) => {
        if (call.path === `${ORG}/assistant`) return json(200, { assistant });
        if (call.path === `${ORG}/assistant/sessions`)
          return json(200, {
            sessions: [
              session("c1", {
                title: "周报整理",
                updated_at: "2026-10-29T09:00:00Z",
              }),
              session("c2", { title: "竞品调研" }),
              session("c3", { title: "旧的会话", archived_at: TS }),
            ],
          });
        const detail = /\/conversations\/(c\d)$/.exec(call.path);
        if (detail)
          return json(
            200,
            summary(detail[1]!, {
              agent_id: "as1",
              title: detail[1] === "c2" ? "竞品调研" : "周报整理",
            }),
          );
        if (/\/conversations\/c\d\/messages$/.test(call.path))
          return json(200, { messages: [], next_cursor: 0 });
        if (call.path === `${ORG}/conversations`)
          return json(200, { conversations: [], next_cursor: null });
        return undefined;
      }),
    );
    const picker = await screen.findByRole("button", { name: "会话记录" });
    await waitFor(() => expect(picker).toHaveTextContent("周报整理"));
    await userEvent.click(picker);
    const dialog = await screen.findByRole("dialog", { name: "会话记录" });
    await userEvent.type(
      within(dialog).getByRole("textbox", { name: "搜索会话记录" }),
      "竞品",
    );
    expect(within(dialog).queryByText("周报整理")).not.toBeInTheDocument();
    await userEvent.click(
      within(dialog).getByRole("button", { name: "竞品调研" }),
    );
    await waitFor(() => expect(picker).toHaveTextContent("竞品调研"));
    await userEvent.click(picker);
    const reopened = await screen.findByRole("dialog", { name: "会话记录" });
    await userEvent.click(
      within(reopened).getByRole("button", { name: "已归档 1" }),
    );
    expect(within(reopened).getByText("旧的会话")).toBeInTheDocument();
  });

  it("显示 Runtime 原生会话包导出及保留说明", async () => {
    renderSettings(
      "/o/o1/assistant?session=c1",
      chatBase((call) => {
        if (call.path === `${ORG}/assistant`) return json(200, { assistant });
        if (call.path === `${ORG}/assistant/sessions`)
          return json(200, { sessions: [session("c1")] });
        if (call.path === `${ORG}/conversations/c1`)
          return json(200, summary("c1", { agent_id: "as1" }));
        if (call.path === `${ORG}/conversations/c1/messages`)
          return json(200, { messages: [], next_cursor: 0 });
        if (call.path === `${ORG}/conversations`)
          return json(200, { conversations: [], next_cursor: null });
        return undefined;
      }),
    );

    await userEvent.click(
      await screen.findByRole("button", { name: "更多操作" }),
    );
    await userEvent.click(
      await screen.findByRole("menuitem", {
        name: "导出 Runtime 原生会话包",
      }),
    );
    const dialog = await screen.findByRole("dialog", {
      name: "导出 Runtime 原生会话包",
    });
    expect(within(dialog).getByText(/最长 24 小时/)).toBeInTheDocument();
    expect(within(dialog).getByText(/下载的 ZIP 未加密/)).toBeInTheDocument();
    expect(
      within(dialog).getByRole("button", { name: "开始导出" }),
    ).toBeEnabled();
  });
});
