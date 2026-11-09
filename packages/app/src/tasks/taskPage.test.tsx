import { QueryClient } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, useLocation, useNavigate } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { Drawer } from "@openfox/ui";

import { App, createQueryClient } from "../App";
import { summary } from "../conversations/fixtures";
import { backend, json, ORG, SPACE, type Override } from "./test-backend";

// 任务全页、非模态抽屉、对话记录页签、AI 面板、召唤 Agent、历史栈、通知定位会话。

const o = `/v1/organizations/${ORG}`;
const list = `/o/${ORG}/spaces/${SPACE}/tasks`;
const page = `/o/${ORG}/tasks/OPEN-1`;
const panel = `${page}?view=chat&conversationId=c9`;

function Probe() {
  const location = useLocation();
  const navigate = useNavigate();
  return (
    <>
      <output data-testid="where">{location.pathname}</output>
      <output data-testid="search">{location.search}</output>
      <button type="button" onClick={() => navigate(-1)}>
        history-back
      </button>
    </>
  );
}

function renderAt(
  entries: string[],
  override?: Override,
  client?: QueryClient,
) {
  const server = backend(override);
  const queryClient = client ?? createQueryClient();
  queryClient.setDefaultOptions({ queries: { retry: false } });
  render(
    <MemoryRouter initialEntries={entries} initialIndex={entries.length - 1}>
      <App api={server.api} queryClient={queryClient} locale="zh-CN" />
      <Probe />
    </MemoryRouter>,
  );
  return server;
}

const where = () =>
  screen.getByTestId("where").textContent! +
  screen.getByTestId("search").textContent!;

/** 任务 1 绑定的会话 c9（Agent 熊猫）；c8 属于别的任务。 */
function conversations(extra?: Override): Override {
  return (call) => {
    const custom = extra?.(call);
    if (custom) return custom;
    if (call.method === "GET" && call.path === `${o}/conversations`)
      return json(200, {
        conversations: call.query.get("task_id")
          ? [summary("c9", { task_id: "task-1", status: "REVIEW" })]
          : [],
        next_cursor: null,
      });
    if (call.method === "GET" && call.path === `${o}/conversations/c9`)
      return json(200, summary("c9", { task_id: "task-1", status: "REVIEW" }));
    if (call.method === "GET" && call.path === `${o}/conversations/c8`)
      return json(200, summary("c8", { task_id: "task-2" }));
    if (call.method === "GET" && call.path.endsWith("/messages"))
      return json(200, { messages: [], next_cursor: 0 });
    return undefined;
  };
}

beforeEach(() => {
  localStorage.setItem(`openfox:onboarding:${ORG}`, JSON.stringify({}));
});
afterEach(() => {
  vi.restoreAllMocks();
  localStorage.clear();
  sessionStorage.clear();
});

describe("任务全页", () => {
  it("AI 面板宽度独立持久化、受任务区最小宽度约束，关闭仍保留任务", async () => {
    vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(1000);
    localStorage.setItem("openfox:task-ai-panel-width", "520");
    const server = renderAt([panel], conversations());
    const handle = await screen.findByRole("separator", {
      name: "调整任务 AI 面板宽度",
    });
    expect(handle).toHaveAttribute("aria-valuenow", "520");
    expect(handle).toHaveAttribute("aria-valuemax", "600");
    const user = userEvent.setup();
    handle.focus();
    await user.keyboard("{ArrowLeft}");
    expect(handle).toHaveAttribute("aria-valuenow", "536");
    expect(localStorage.getItem("openfox:task-ai-panel-width")).toBe("536");
    await user.keyboard("{Home}");
    expect(handle).toHaveAttribute("aria-valuenow", "320");
    await user.keyboard("{End}");
    expect(handle).toHaveAttribute("aria-valuenow", "600");
    await user.keyboard("{Enter}");
    expect(
      screen.queryByRole("complementary", { name: "AI 面板" }),
    ).not.toBeInTheDocument();
    expect(
      await screen.findByRole("textbox", { name: "任务标题" }),
    ).toHaveValue("任务标题 1");
    expect(where()).toBe(page);
    expect(server.calls.some((call) => call.method === "PATCH")).toBe(false);
  });

  it("直接打开链接，显示任务；刷新（重新渲染同一地址）恢复", async () => {
    renderAt([page]);
    expect(
      await screen.findByRole("textbox", { name: "任务标题" }),
    ).toHaveValue("任务标题 1");
    expect(where()).toBe(page);
  });

  it("没有进入前的页面时，关闭回到所在空间任务页；还原回到空间任务页 ?task=", async () => {
    const user = userEvent.setup();
    renderAt([page]);
    await screen.findByRole("textbox", { name: "任务标题" });
    await user.click(screen.getByRole("button", { name: "还原为抽屉" }));
    await waitFor(() => expect(where()).toBe(`${list}?task=OPEN-1`));
  });

  it("没有 state 时关闭回到空间任务页", async () => {
    const user = userEvent.setup();
    renderAt([page]);
    await screen.findByRole("textbox", { name: "任务标题" });
    await user.click(screen.getByRole("button", { name: "关闭" }));
    await waitFor(() => expect(where()).toBe(list));
  });

  it("从抽屉展开后关闭：回到列表，后退不会再打开抽屉", async () => {
    const user = userEvent.setup();
    renderAt([`/o/${ORG}/inbox`, list]);
    await user.click(
      await screen.findByRole("button", { name: /OPEN-1 任务标题 1/ }),
    );
    await waitFor(() => expect(where()).toBe(`${list}?task=OPEN-1`));
    await user.click(await screen.findByRole("button", { name: "展开" }));
    await waitFor(() => expect(where()).toBe(page));
    await user.click(screen.getByRole("button", { name: "关闭" }));
    await waitFor(() => expect(where()).toBe(list));
    await user.click(screen.getByRole("button", { name: "history-back" }));
    await waitFor(() => expect(where()).toBe(`/o/${ORG}/inbox`));
  });

  it("从列表进入的抽屉展开后「还原为抽屉」回到带 ?task= 的列表", async () => {
    const user = userEvent.setup();
    renderAt([`/o/${ORG}/inbox`, list]);
    await user.click(
      await screen.findByRole("button", { name: /OPEN-1 任务标题 1/ }),
    );
    await user.click(await screen.findByRole("button", { name: "展开" }));
    await waitFor(() => expect(where()).toBe(page));
    await user.click(screen.getByRole("button", { name: "还原为抽屉" }));
    await waitFor(() => expect(where()).toBe(`${list}?task=OPEN-1`));
    // 此时关闭抽屉仍是后退一步，回到列表
    await user.click(screen.getByRole("button", { name: "关闭" }));
    await waitFor(() => expect(where()).toBe(list));
  });

  it("旧编号按规范编号 replace 跳转", async () => {
    renderAt([page.replace("OPEN-1", "OLD-1")], (call) =>
      call.path.endsWith("/by-display-id/OLD-1")
        ? json(200, {
            ...backendTask(),
            canonical_display_id: "OPEN-1",
          })
        : undefined,
    );
    await waitFor(() => expect(where()).toBe(page));
  });

  it("任务不存在：显示找不到并提供回收件箱入口", async () => {
    const user = userEvent.setup();
    renderAt([`/o/${ORG}/tasks/OPEN-99`]);
    expect(
      await screen.findByText("任务不存在或你没有访问权限"),
    ).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "返回收件箱" }));
    await waitFor(() => expect(where()).toBe(`/o/${ORG}/inbox`));
  });
});

function backendTask() {
  // 借用默认后端的任务 1
  return {
    organization_id: ORG,
    space_id: SPACE,
    id: "task-1",
    display_id: "open-1",
    sequence: 1,
    type_id: "t1",
    type_name: "任务",
    status_id: "st1",
    status_name: "待处理",
    status_category: "TODO",
    title: "任务标题 1",
    description: "",
    acceptance: "",
    priority: "NONE",
    version: 1,
    created_at: "2026-10-20T00:00:00Z",
    updated_at: "2026-10-20T00:00:00Z",
    labels: [],
    start_date: null,
    due_date: null,
    parent_task_id: null,
    parent: null,
    rank: "h1",
    assignees: [],
    label_items: [],
    subtask_count: 0,
    subtask_done_count: 0,
    created_by: "u1",
    type_icon: null,
    status_color: null,
    deleted: false,
    iteration_id: null,
    project_id: null,
    triage: null,
    duplicate_of: null,
    description_reactions: [],
  };
}

describe("抽屉历史栈", () => {
  it("从列表打开再关闭：后退到列表之前，不会重新打开抽屉", async () => {
    const user = userEvent.setup();
    renderAt([`/o/${ORG}/inbox`, list]);
    await user.click(
      await screen.findByRole("button", { name: /OPEN-1 任务标题 1/ }),
    );
    await waitFor(() => expect(where()).toBe(`${list}?task=OPEN-1`));
    await user.click(await screen.findByRole("button", { name: "关闭" }));
    await waitFor(() => expect(where()).toBe(list));
    await user.click(screen.getByRole("button", { name: "history-back" }));
    await waitFor(() => expect(where()).toBe(`/o/${ORG}/inbox`));
  });

  it("深链接进入的抽屉：关闭用 replace，后退直接离开页面", async () => {
    const user = userEvent.setup();
    renderAt([`/o/${ORG}/inbox`, `${list}?task=OPEN-1`]);
    await user.click(await screen.findByRole("button", { name: "关闭" }));
    await waitFor(() => expect(where()).toBe(list));
    await user.click(screen.getByRole("button", { name: "history-back" }));
    await waitFor(() => expect(where()).toBe(`/o/${ORG}/inbox`));
  });

  it("抽屉已打开时切到另一张任务卡片：replace，关闭一次回到列表", async () => {
    const user = userEvent.setup();
    renderAt([`/o/${ORG}/inbox`, list]);
    await user.click(
      await screen.findByRole("button", { name: /OPEN-1 任务标题 1/ }),
    );
    await waitFor(() => expect(where()).toBe(`${list}?task=OPEN-1`));
    await user.click(screen.getByRole("button", { name: /OPEN-2 任务标题 2/ }));
    await waitFor(() => expect(where()).toBe(`${list}?task=OPEN-2`));
    await user.click(await screen.findByRole("button", { name: "关闭" }));
    await waitFor(() => expect(where()).toBe(list));
  });
});

describe("非模态抽屉", () => {
  it("没有 aria-modal，主导航仍可点击", async () => {
    const user = userEvent.setup();
    renderAt([`${list}?task=OPEN-1`]);
    const dialog = await screen.findByRole("dialog");
    expect(dialog).not.toHaveAttribute("aria-modal");
    expect(dialog).toHaveAccessibleName(/OPEN-1/);
    await user.click(screen.getByRole("link", { name: "全部设置" }));
    await waitFor(() => expect(where()).toMatch(/\/settings\//));
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("焦点在抽屉内按 Esc 关闭；在标题输入框里按 Esc 不关闭", async () => {
    const user = userEvent.setup();
    renderAt([`${list}?task=OPEN-1`]);
    const title = await screen.findByRole("textbox", { name: "任务标题" });
    await user.click(title);
    await user.keyboard("{Escape}");
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    screen.getByRole("dialog").focus();
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("其他使用 Drawer 的地方默认仍是模态", () => {
    render(
      <Drawer open onClose={() => undefined} title="电脑详情">
        内容
      </Drawer>,
    );
    expect(screen.getByRole("dialog")).toHaveAttribute("aria-modal", "true");
  });
});

describe("对话记录与 AI 面板", () => {
  it("页签显示数量与会话列表；点击进入全页并打开 AI 面板；可关闭", async () => {
    const user = userEvent.setup();
    renderAt([`${list}?task=OPEN-1`], conversations());
    const tab = await screen.findByRole("tab", { name: /对话记录\s*1/ });
    await user.click(tab);
    const rows = await screen.findByRole("list", { name: "对话记录" });
    await user.click(
      within(rows).getByRole("button", { name: "打开会话 熊猫" }),
    );
    await waitFor(() => expect(where()).toBe(panel));
    const aside = await screen.findByRole("complementary", {
      name: "AI 面板",
    });
    expect(within(aside).getByText("熊猫")).toBeInTheDocument();
    expect(
      within(aside).queryByRole("link", { name: "在对话中打开" }),
    ).not.toBeInTheDocument();
    await user.click(
      within(aside).getByRole("button", { name: "关闭 AI 面板" }),
    );
    await waitFor(() => expect(where()).toBe(page));
  });

  it("刷新后任务与 AI 面板都恢复", async () => {
    renderAt([panel], conversations());
    expect(
      await screen.findByRole("textbox", { name: "任务标题" }),
    ).toBeInTheDocument();
    expect(
      await screen.findByRole("complementary", { name: "AI 面板" }),
    ).toBeInTheDocument();
  });

  it("AI 面板的输入框同样有 Runtime / 模型 / 权限模式就地切换", async () => {
    const user = userEvent.setup();
    const server = renderAt(
      [panel],
      conversations((call) =>
        call.method === "GET" &&
        call.path === `${o}/conversations/c9/override-options`
          ? json(200, {
              computer_online: true,
              runtimes: [
                {
                  provider: "codex",
                  runnable: true,
                  models: [],
                  approval_modes: ["FOLLOW_RUNTIME", "READ_ONLY"],
                },
              ],
            })
          : call.method === "PATCH" &&
              call.path === `${o}/conversations/c9/overrides`
            ? json(200, summary("c9", { task_id: "task-1" }))
            : undefined,
      ),
    );
    const aside = await screen.findByRole("complementary", {
      name: "AI 面板",
    });
    await user.click(
      await within(aside).findByRole("button", {
        name: "权限模式：跟随 Runtime",
      }),
    );
    await user.click(
      within(await screen.findByRole("menu")).getByRole("menuitemradio", {
        name: /只读/,
      }),
    );
    await waitFor(() =>
      expect(
        server.calls.find(
          (c) =>
            c.method === "PATCH" &&
            c.path === `${o}/conversations/c9/overrides`,
        )?.body,
      ).toEqual({ approval_mode: "READ_ONLY", expected_version: 0 }),
    );
  });

  it.each(["REUSE", "BASELINE"] as const)(
    "发起任务对话：%s 随创建提交，首条消息重试复用已建会话",
    async (mode) => {
      const user = userEvent.setup();
      let id = "",
        attempts = 0;
      const server = renderAt(
        [panel],
        conversations((call) => {
          if (call.path.endsWith("/conversation-workspace-options"))
            return json(200, {
              directories: ["qa"],
              git_directories: ["qa"],
              supported: true,
              can_reuse: true,
              can_set_baseline: true,
              device_id: "d1",
              recent_directories: [],
            });
          if (
            call.method === "GET" &&
            call.path === o + "/spaces/" + SPACE + "/agents"
          )
            return json(200, {
              agents: [{ id: "a1", name: "熊猫", archived_at: null }],
            });
          if (call.method === "POST" && call.path === o + "/conversations") {
            id = (call.body as { id: string }).id;
            return json(201, summary(id, { task_id: "task-1" }));
          }
          if (id && call.path === o + "/conversations/" + id)
            return json(200, summary(id, { task_id: "task-1" }));
          if (
            id &&
            call.method === "POST" &&
            call.path === o + "/conversations/" + id + "/messages"
          ) {
            attempts++;
            return attempts === 1
              ? json(503, { error: { code: "UNAVAILABLE" } })
              : json(201, { id: "m1", seq: 1 });
          }
          return undefined;
        }),
      );
      const aside = await screen.findByRole("complementary", {
        name: "AI 面板",
      });
      await user.click(
        await within(aside).findByRole("button", { name: "更多操作" }),
      );
      await user.click(
        await screen.findByRole("menuitem", { name: "创建新对话" }),
      );
      const d = await screen.findByRole("dialog", { name: "发起新对话" }),
        start = within(d).getByRole("button", { name: "发起对话" });
      expect(start).toBeDisabled();
      await user.click(
        within(d).getByRole("switch", { name: "复用当前对话的工作目录" }),
      );
      if (mode === "BASELINE") {
        await user.click(
          within(d).getByRole("switch", { name: "指定代码的基线分支" }),
        );
        await user.type(
          within(d).getByRole("textbox", { name: "基线分支（可选）" }),
          "release",
        );
      }
      await user.type(
        within(d).getByRole("textbox", { name: "发送消息" }),
        "检查任务",
      );
      await waitFor(() => expect(start).toBeEnabled());
      await user.click(start);
      await waitFor(() => expect(attempts).toBe(1));
      expect(within(d).getByRole("textbox", { name: "发送消息" })).toHaveValue(
        "检查任务",
      );
      await user.click(start);
      await waitFor(() => expect(where()).toContain("conversationId=" + id));
      expect(
        server.calls.filter(
          (c) => c.method === "POST" && c.path === o + "/conversations",
        ),
      ).toHaveLength(1);
      expect(
        server.calls.find(
          (call) =>
            call.method === "POST" && call.path === o + "/conversations",
        )?.body,
      ).toMatchObject({
        workspace_selection: {
          mode,
          source_conversation_id: "c9",
          ...(mode === "BASELINE" ? { base_ref: "release" } : {}),
        },
      });
      const messages = server.calls.filter(
        (c) =>
          c.method === "POST" &&
          c.path === o + "/conversations/" + id + "/messages",
      );
      expect(messages).toHaveLength(2);
      expect(messages[0]?.body).toMatchObject({
        mentions: [{ agent_id: "a1" }],
      });
      expect(messages[1]?.body).toEqual(messages[0]?.body);
    },
  );

  it("没有会话：显示空状态和召唤入口", async () => {
    const user = userEvent.setup();
    renderAt([page]);
    await user.click(await screen.findByRole("tab", { name: /对话记录/ }));
    expect(
      await screen.findByText("这个任务还没有对话记录"),
    ).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: "召唤 Agent" }).length).toBe(
      2,
    );
  });

  it("会话不属于该任务：不展示内容并提示，面板可关闭", async () => {
    const user = userEvent.setup();
    renderAt([`${page}?view=chat&conversationId=c8`], conversations());
    const aside = await screen.findByRole("complementary", {
      name: "AI 面板",
    });
    expect(
      await within(aside).findByText("该会话不属于这个任务"),
    ).toBeInTheDocument();
    await user.click(
      within(aside).getByRole("button", { name: "关闭 AI 面板" }),
    );
    await waitFor(() => expect(where()).toBe(page));
  });

  it("会话不存在：显示找不到，面板可关闭", async () => {
    renderAt([`${page}?view=chat&conversationId=nope`], conversations());
    expect(await screen.findByText("对话不存在或你没有访问权限")).toBeTruthy();
  });
});

describe("召唤 Agent", () => {
  const agents: Override = (call) =>
    call.path === `${o}/spaces/${SPACE}/agents`
      ? json(200, {
          agents: [
            { id: "a1", name: "熊猫", avatar: null, archived_at: undefined },
          ],
        })
      : undefined;

  it("请求体是 { agent_id, task_id }，成功后打开 AI 面板", async () => {
    const user = userEvent.setup();
    const server = renderAt(
      [page],
      conversations((call) => {
        if (call.method === "POST" && call.path === `${o}/conversations`)
          return json(201, summary("c9", { task_id: "task-1" }));
        return agents(call);
      }),
    );
    await user.click(await screen.findByRole("button", { name: "召唤 Agent" }));
    // 召唤菜单分两级，先进入「选择 Agent 新开对话」。
    await user.click(
      screen.getByRole("menuitem", { name: "选择 Agent 新开对话" }),
    );
    await user.click(await screen.findByRole("menuitem", { name: /熊猫/ }));
    await waitFor(() => expect(where()).toBe(panel));
    const post = server.calls.find(
      (c) => c.method === "POST" && c.path === `${o}/conversations`,
    )!;
    expect(post.body).toMatchObject({
      agent_id: "a1",
      task_id: "task-1",
      visibility: "SHARED",
    });
  });

  it("403 时给出提示，不打开面板", async () => {
    const user = userEvent.setup();
    renderAt(
      [page],
      conversations((call) => {
        if (call.method === "POST" && call.path === `${o}/conversations`)
          return json(403, {
            error: { code: "FORBIDDEN", message: "x", request_id: "r" },
          });
        return agents(call);
      }),
    );
    await user.click(await screen.findByRole("button", { name: "召唤 Agent" }));
    // 召唤菜单分两级，先进入「选择 Agent 新开对话」。
    await user.click(
      screen.getByRole("menuitem", { name: "选择 Agent 新开对话" }),
    );
    await user.click(await screen.findByRole("menuitem", { name: /熊猫/ }));
    expect(
      await screen.findByText("你没有权限执行这个操作"),
    ).toBeInTheDocument();
    expect(where()).toBe(page);
  });

  it("进行中按钮禁用，不会连发", async () => {
    const user = userEvent.setup();
    let release: () => void = () => undefined;
    const gate = new Promise<void>((resolve) => (release = resolve));
    const server = renderAt([page], (call) => {
      if (call.method === "POST" && call.path === `${o}/conversations`)
        return json(201, summary("c9", { task_id: "task-1" }));
      return conversations(agents)(call);
    });
    void gate;
    await user.click(await screen.findByRole("button", { name: "召唤 Agent" }));
    // 召唤菜单分两级，先进入「选择 Agent 新开对话」。
    await user.click(
      screen.getByRole("menuitem", { name: "选择 Agent 新开对话" }),
    );
    const item = await screen.findByRole("menuitem", { name: /熊猫/ });
    await user.dblClick(item);
    await waitFor(() => expect(where()).toBe(panel));
    release();
    expect(
      server.calls.filter(
        (c) => c.method === "POST" && c.path === `${o}/conversations`,
      ).length,
    ).toBe(1);
  });

  it("空间已停用的只读任务不显示召唤入口", async () => {
    renderAt([page], (call) => {
      if (call.path === `${o}/spaces`) return json(200, { spaces: [] });
      if (call.path === `${o}/spaces/${SPACE}`)
        return json(200, {
          id: SPACE,
          status: "retired",
          name: "默认空间",
          features: {
            views: false,
            projects: false,
            iterations: false,
            triage: false,
          },
        });
      return undefined;
    });
    await screen.findByRole("textbox", { name: "任务标题" });
    await waitFor(() =>
      expect(screen.queryByRole("button", { name: "召唤 Agent" })).toBeNull(),
    );
  });
});

describe("通知与主导航记忆", () => {
  it("同时带任务和会话的通知：进入任务全页并打开 AI 面板", async () => {
    const user = userEvent.setup();
    renderAt(
      [`/o/${ORG}/inbox`],
      conversations((call) => {
        if (call.path === `${o}/inbox` && call.method === "GET")
          return json(200, {
            groups: [
              {
                id: "g1",
                notification_id: "n1",
                kind: "MENTION",
                task_id: "task-1",
                task_display_id: "open-1",
                task_title: "修复登录",
                task_status_category: "TODO",
                task_type_icon: null,
                conversation_id: "c9",
                space_id: SPACE,
                space_name: "默认空间",
                safe_summary: "请看一下",
                count: 1,
                unread: false,
                latest_at: "2026-10-28T00:00:00Z",
              },
            ],
            counts: {
              inbox: 1,
              inbox_unread: 0,
              inbox_important_unread: 0,
              inbox_other_unread: 0,
              later: 0,
              cleared: 0,
            },
          });
        return undefined;
      }),
    );
    const row = (await screen.findByText("修复登录")).closest("li")!;
    await user.click(within(row).getAllByRole("button")[0]!);
    await waitFor(() => expect(where()).toBe(panel));
  });

  it("在全页打开 AI 面板后切到设置，再点「首页」回到同一地址", async () => {
    const user = userEvent.setup();
    renderAt([panel], conversations());
    await screen.findByRole("complementary", { name: "AI 面板" });
    await user.click(screen.getByRole("link", { name: "全部设置" }));
    await waitFor(() => expect(where()).toMatch(/\/settings\//));
    await user.click(screen.getByRole("link", { name: "首页" }));
    await waitFor(() => expect(where()).toBe(panel));
  });
});
