import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, useLocation } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { App, createQueryClient } from "../App";
import { summary } from "../conversations/fixtures";
import {
  backend,
  json,
  makeTask,
  ORG,
  SPACE,
  type Override,
} from "./test-backend";

// 窄宽度任务详情：属性按钮行、「更多属性」浮层、会话标签；宽时保持原布局。

const o = `/v1/organizations/${ORG}`;
const page = `/o/${ORG}/tasks/OPEN-1`;
const panel = `${page}?view=chat&conversationId=c9`;

function Probe() {
  const location = useLocation();
  return (
    <output data-testid="where">{location.pathname + location.search}</output>
  );
}

/** 任务 1 绑定的会话：c9（当前打开）在前，另有若干条。 */
function conversations(ids: string[], extra?: Override): Override {
  return (call) => {
    const custom = extra?.(call);
    if (custom) return custom;
    if (call.method === "GET" && call.path === `${o}/conversations`)
      return json(200, {
        conversations: call.query.get("task_id")
          ? ids.map((id) =>
              summary(id, { task_id: "task-1", status: "REVIEW" }),
            )
          : [],
        next_cursor: null,
      });
    const one = new RegExp(`^${o}/conversations/(c\\d+)$`).exec(call.path);
    if (call.method === "GET" && one)
      return json(200, summary(one[1]!, { task_id: "task-1" }));
    if (call.method === "GET" && call.path.endsWith("/messages"))
      return json(200, { messages: [], next_cursor: 0 });
    return undefined;
  };
}

function renderAt(entry: string, override?: Override) {
  const server = backend(override);
  const queryClient = createQueryClient();
  queryClient.setDefaultOptions({ queries: { retry: false } });
  render(
    <MemoryRouter initialEntries={[entry]}>
      <App api={server.api} queryClient={queryClient} locale="zh-CN" />
      <Probe />
    </MemoryRouter>,
  );
  return server;
}

/** 模拟 ResizeObserver：observe 时同步回报给定宽度。 */
function mockWidth(width: number) {
  class FakeResizeObserver {
    constructor(private readonly callback: ResizeObserverCallback) {}
    observe(target: Element) {
      this.callback(
        [{ target, contentRect: { width } } as unknown as ResizeObserverEntry],
        this as unknown as ResizeObserver,
      );
    }
    unobserve() {}
    disconnect() {}
  }
  vi.stubGlobal("ResizeObserver", FakeResizeObserver);
}

beforeEach(() => {
  localStorage.setItem(`openfox:onboarding:${ORG}`, JSON.stringify({}));
});
afterEach(() => {
  vi.unstubAllGlobals();
  localStorage.clear();
  sessionStorage.clear();
});

const titleBox = () => screen.findByRole("textbox", { name: "任务标题" });

describe("任务详情 · 宽窄判断", () => {
  it("宽（量到 900）：保留右栏，没有属性按钮行与会话标签", async () => {
    mockWidth(900);
    renderAt(panel, conversations(["c9"]));
    await titleBox();
    expect(
      await screen.findByRole("complementary", { name: "属性" }),
    ).toBeInTheDocument();
    const properties = screen.getByRole("complementary", { name: "属性" });
    for (const name of ["处理人", "状态", "优先级"])
      expect(
        within(properties).getByRole("group", { name }),
      ).toBeInTheDocument();
    expect(within(properties).queryByText("创建人")).not.toBeInTheDocument();
    expect(within(properties).queryByText("创建时间")).not.toBeInTheDocument();
    expect(
      screen.queryByRole("group", { name: "本任务会话" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "更多属性" }),
    ).toBeInTheDocument();
  });

  it("窄（量到 600）：不渲染右栏，属性按钮行按顺序带 aria-label", async () => {
    mockWidth(600);
    renderAt(panel, conversations(["c9"]));
    await titleBox();
    expect(
      screen.queryByRole("complementary", { name: "属性" }),
    ).not.toBeInTheDocument();
    const bar = await screen.findByRole("group", { name: "属性" });
    const names = within(bar)
      .getAllByRole("button")
      .map((b) => b.getAttribute("aria-label"));
    expect(names).toEqual([
      "处理人: 无处理人",
      "状态: 待处理",
      "优先级: 无优先级",
      "标签: 无标签",
      "更多属性",
    ]);
    // 召唤 Agent 在标题上方
    expect(
      screen.getByRole("button", { name: "召唤 Agent" }),
    ).toBeInTheDocument();
  });
});

describe("任务描述下方操作", () => {
  it.each([600, 900])(
    "宽度 %i：空区块不占位，仍能关联任务并显示结果",
    async (width) => {
      mockWidth(width);
      const user = userEvent.setup();
      let linked = false;
      const relation = {
        id: "r1",
        key: "RELATES",
        type_id: "builtin:RELATES",
        direction: "OUTWARD",
        task: {
          id: "task-2",
          display_id: "open-2",
          title: "任务标题 2",
          status_category: "TODO",
        },
      };
      const server = renderAt(page, (call) => {
        if (call.path.endsWith("/attachments") && call.method === "GET")
          return json(200, { attachments: [] });
        if (call.path.endsWith("/tasks/task-1/relations")) {
          if (call.method === "POST") {
            linked = true;
            return json(201, relation);
          }
          return json(200, { relations: linked ? [relation] : [] });
        }
        return undefined;
      });
      await titleBox();
      for (const name of ["子任务", "关联", "附件"])
        expect(screen.queryByRole("region", { name })).not.toBeInTheDocument();
      expect(screen.getByRole("button", { name: "添加子任务" })).toBeEnabled();
      expect(screen.getByRole("button", { name: "添加附件" })).toBeEnabled();
      await user.click(
        screen.getByRole("button", { name: "任务描述：更多操作" }),
      );
      await user.click(
        await screen.findByRole("menuitem", { name: "添加关联任务" }),
      );
      const dialog = await screen.findByRole("dialog", {
        name: "选择要关联的任务",
      });
      await user.click(
        await within(dialog).findByRole("button", {
          name: /OPEN-2\s*任务标题 2/,
        }),
      );
      await within(
        await screen.findByRole("region", { name: "关联" }),
      ).findByRole("button", { name: /OPEN-2\s*任务标题 2/ });
      expect(
        server.find("POST", "/tasks/task-1/relations")[0]!.body,
      ).toMatchObject({
        type_id: "builtin:BLOCKS",
        target_task_id: "task-2",
        direction: "INWARD",
      });
      expect(
        screen.queryByRole("dialog", { name: "选择要关联的任务" }),
      ).not.toBeInTheDocument();
    },
  );
});

describe("窄布局 · 属性按钮行写入", () => {
  it("切换到邀请保留执行 Agent，再清除人员时只清掉邀请", async () => {
    mockWidth(600);
    const user = userEvent.setup();
    let task = makeTask(1, "st1", {
      assignees: [
        { kind: "HUMAN", id: "u2", name: "原成员" },
        { kind: "AGENT", id: "a1", name: "执行 Agent" },
      ],
    });
    const server = renderAt(page, (call) => {
      if (call.path.endsWith("/invites"))
        return json(200, {
          invitations: [
            {
              id: "iv",
              email: "future@example.test",
              role: "MEMBER",
              status: "PENDING",
              expires_at: "2099-01-01",
            },
          ],
        });
      if (call.path.endsWith("/tasks/by-display-id/OPEN-1"))
        return json(200, { ...task, description_reactions: [] });
      if (call.method === "PATCH" && call.path.endsWith("/tasks/task-1")) {
        const { assignees } = call.body as { assignees: typeof task.assignees };
        task = {
          ...task,
          version: task.version + 1,
          assignees: assignees.map((a) => ({
            ...a,
            name:
              a.kind === "INVITATION" ? "future@example.test" : "执行 Agent",
          })),
        };
        return json(200, task);
      }
      return undefined;
    });
    await titleBox();
    await user.click(screen.getByRole("button", { name: /^处理人: / }));
    await user.click(
      await screen.findByRole("option", { name: /future@example.test/ }),
    );
    await waitFor(() =>
      expect(server.find("PATCH", "/tasks/task-1")[0]?.body).toMatchObject({
        assignees: [
          { kind: "INVITATION", id: "iv" },
          { kind: "AGENT", id: "a1" },
        ],
      }),
    );
    expect(
      await screen.findByRole("button", {
        name: /^处理人: future@example.test/,
      }),
    ).toHaveTextContent("待接受");
    await user.click(
      screen.getByRole("button", { name: /^处理人: future@example.test/ }),
    );
    await user.click(await screen.findByRole("option", { name: "无处理人" }));
    await waitFor(() =>
      expect(server.find("PATCH", "/tasks/task-1")[1]?.body).toMatchObject({
        assignees: [{ kind: "AGENT", id: "a1" }],
      }),
    );
  });
  it.each([600, 900])(
    "宽度 %i：标签选择保留单选分组互斥和移除规则",
    async (width) => {
      mockWidth(width);
      const user = userEvent.setup();
      const labels = [
        {
          id: "l1",
          name: "前端",
          color: "#238636",
          group_id: "area",
          space_id: SPACE,
          archived_at: null,
        },
        {
          id: "l2",
          name: "后端",
          color: "#2563eb",
          group_id: "area",
          space_id: SPACE,
          archived_at: null,
        },
        {
          id: "l3",
          name: "需复核",
          color: "#ca8a04",
          group_id: null,
          space_id: SPACE,
          archived_at: null,
        },
      ];
      let task = makeTask(1, "st1");
      const server = renderAt(page, (call) => {
        if (call.path === `${o}/labels`) return json(200, { labels });
        if (call.path.endsWith("/tasks/by-display-id/OPEN-1"))
          return json(200, { ...task, description_reactions: [] });
        if (call.method === "PATCH" && call.path.endsWith("/tasks/task-1")) {
          const ids = (call.body as { label_ids: string[] }).label_ids;
          task = {
            ...task,
            label_items: labels.filter((label) => ids.includes(label.id)),
            version: task.version + 1,
          };
          return json(200, task);
        }
        return undefined;
      });
      await titleBox();
      await user.click(
        await screen.findByRole("button", {
          name: width === 600 ? "标签: 无标签" : "标签",
        }),
      );
      for (const [name, expected] of [
        ["前端", ["l1"]],
        ["需复核", ["l1", "l3"]],
        ["后端", ["l3", "l2"]],
        ["后端", ["l3"]],
      ] as const) {
        const count = server.find("PATCH", "/tasks/task-1").length;
        await user.click(await screen.findByRole("option", { name }));
        await waitFor(() =>
          expect(server.find("PATCH", "/tasks/task-1")).toHaveLength(count + 1),
        );
        expect(
          server.find("PATCH", "/tasks/task-1").at(-1)!.body,
        ).toMatchObject({ label_ids: [...expected] });
        await waitFor(() =>
          expect(screen.getByRole("option", { name })).toHaveAttribute(
            "aria-selected",
            expected.some(
              (id) =>
                id === (name === "前端" ? "l1" : name === "后端" ? "l2" : "l3"),
            )
              ? "true"
              : "false",
          ),
        );
      }
    },
  );

  it("处理人 / 状态 / 优先级按钮打开同一选择器并写入", async () => {
    mockWidth(600);
    const user = userEvent.setup();
    const server = renderAt(panel, conversations(["c9"]));
    await titleBox();
    const patches = () => server.find("PATCH", "/tasks/task-1");

    await user.click(
      await screen.findByRole("button", { name: "处理人: 无处理人" }),
    );
    await user.click(
      await screen.findByRole("option", { name: /wang@openfox\.dev/ }),
    );
    await waitFor(() => expect(patches()).toHaveLength(1));
    expect(patches()[0]!.body).toMatchObject({
      assignees: [{ kind: "HUMAN", id: "u2" }],
    });

    await user.click(screen.getByRole("button", { name: /^状态: / }));
    await user.click(await screen.findByRole("option", { name: "进行中" }));
    await waitFor(() => expect(patches()).toHaveLength(2));
    expect(patches()[1]!.body).toMatchObject({ status_id: "st2" });

    await user.click(screen.getByRole("button", { name: /^优先级: / }));
    await user.click(await screen.findByRole("option", { name: "高" }));
    await waitFor(() => expect(patches()).toHaveLength(3));
    expect(patches()[2]!.body).toMatchObject({ priority: "HIGH" });
  });

  it("「更多属性」不重复按钮行和会话，仍能改开始日期；里层日期浮层不会关掉外层", async () => {
    mockWidth(600);
    const user = userEvent.setup();
    const server = renderAt(panel, conversations(["c9"]));
    await titleBox();
    await user.click(await screen.findByRole("button", { name: "更多属性" }));
    const popover = await screen.findByRole("dialog", { name: "更多属性" });
    for (const name of [
      "处理人",
      "执行 Agent",
      "状态",
      "优先级",
      "专项",
      "标签",
    ])
      expect(
        within(popover).queryByRole("group", { name }),
      ).not.toBeInTheDocument();
    expect(
      within(popover).queryByText("该任务类型没有绑定字段"),
    ).not.toBeInTheDocument();
    expect(within(popover).queryByText("创建人")).not.toBeInTheDocument();
    expect(within(popover).queryByText("创建时间")).not.toBeInTheDocument();
    const start = within(
      within(popover).getByRole("group", { name: "开始日期" }),
    ).getByRole("button");
    await user.click(start);
    await user.click(await screen.findByRole("menuitem", { name: /今天/ }));
    await waitFor(() =>
      expect(
        server
          .find("PATCH", "/tasks/task-1")
          .some(
            (c) =>
              typeof (c.body as { start_date?: unknown }).start_date ===
              "string",
          ),
      ).toBe(true),
    );
    expect(
      screen.getByRole("dialog", { name: "更多属性" }),
    ).toBeInTheDocument();
    // 会话已经在标题上方列出，补充属性中不再重复。
    expect(
      within(popover).queryByRole("button", { name: "打开会话 熊猫" }),
    ).not.toBeInTheDocument();
    expect(
      within(popover).queryByRole("button", { name: "召唤 Agent" }),
    ).not.toBeInTheDocument();
  });
});

describe("窄布局 · 会话标签", () => {
  it("当前会话按下；超过 2 个收进更多（当前会话总直接显示）；点击切换会话", async () => {
    mockWidth(600);
    const user = userEvent.setup();
    // 列表按最近排序（时间相同按 id 降序）：c9、c5、c3、c2、c1；当前打开的是排在最后的 c1
    renderAt(
      `${page}?view=chat&conversationId=c1`,
      conversations(["c1", "c2", "c3", "c9", "c5"]),
    );
    await titleBox();
    const tabs = await screen.findByRole("group", { name: "本任务会话" });
    // 2 个标签 + 更多按钮
    await waitFor(() =>
      expect(within(tabs).getAllByRole("button")).toHaveLength(3),
    );
    const pressed = within(tabs).getByRole("button", { pressed: true });
    expect(pressed).toHaveTextContent("熊猫 · 会话 c1");
    const c9 = within(tabs).getByRole("button", { name: /会话 c9/ });
    expect(c9).toHaveAttribute("aria-pressed", "false");
    expect(within(tabs).queryByRole("button", { name: /会话 c5/ })).toBeNull();
    await user.click(within(tabs).getByRole("button", { name: "更多 (3)" }));
    expect(
      await screen.findByRole("menuitem", { name: /会话 c3/ }),
    ).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: /会话 c2/ })).toBeVisible();
    await user.keyboard("{Escape}");
    await user.click(c9);
    await waitFor(() =>
      expect(screen.getByTestId("where")).toHaveTextContent(
        `${page}?view=chat&conversationId=c9`,
      ),
    );
    expect(
      within(screen.getByRole("group", { name: "本任务会话" })).getByRole(
        "button",
        { pressed: true },
      ),
    ).toHaveTextContent("会话 c9");
  });
});

/** s1 已停用：不在活跃空间列表里，详情返回 retired（启用专项，验证专项按钮同样禁用）。 */
const retired: Override = (call) => {
  if (call.path === `${o}/spaces`) return json(200, { spaces: [] });
  if (call.path === `${o}/spaces/${SPACE}`)
    return json(200, {
      id: SPACE,
      prefix: "OPEN",
      name: "默认空间",
      visibility: "shared",
      version: 2,
      created_at: "2026-10-01T00:00:00Z",
      updated_at: "2026-10-01T00:00:00Z",
      icon: null,
      purpose: "software_development",
      features: {
        views: true,
        projects: true,
        iterations: false,
        triage: false,
      },
      multi_assignee: false,
      estimation: {
        scale: "notUsed",
        extended: false,
        allow_zero: false,
        count_unestimated: false,
      },
      status: "retired",
      retired_at: "2026-10-28T00:00:00Z",
      position: null,
      my_role: "OWNER",
      can_manage: true,
      member_count: 1,
      task_type_count: 1,
      label_count: 0,
      task_count: 0,
    });
  return undefined;
};

describe("窄布局 · 只读空间", () => {
  it("属性按钮禁用并提示原因，没有「召唤 Agent」", async () => {
    mockWidth(600);
    renderAt(page, conversations([], retired));
    await titleBox();
    const bar = await screen.findByRole("group", { name: "属性" });
    const status = within(bar).getByRole("button", { name: /^状态: / });
    await waitFor(() => expect(status).toBeDisabled());
    for (const name of [/^处理人: /, /^优先级: /, /^标签: /])
      expect(within(bar).getByRole("button", { name })).toBeDisabled();
    expect(
      await within(bar).findByRole("button", { name: /^专项: / }),
    ).toBeDisabled();
    expect(
      within(bar).getAllByLabelText("此空间已停用，只能查看。").length,
    ).toBeGreaterThanOrEqual(5);
    expect(
      screen.queryByRole("button", { name: "召唤 Agent" }),
    ).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "更多属性" }));
    const more = screen.getByRole("dialog", { name: "更多属性" });
    expect(
      within(more).queryByRole("group", { name: "专项" }),
    ).not.toBeInTheDocument();
    expect(
      within(within(more).getByRole("group", { name: "开始日期" })).getByRole(
        "button",
      ),
    ).toBeDisabled();
  });
});

describe("任务会话卡片与宽栏", () => {
  it("动态按创建时间插入真实摘要，原会话跳转保留 ID", async () => {
    mockWidth(600);
    const user = userEvent.setup();
    renderAt(
      page,
      conversations(["c9"], (call) => {
        if (call.path === `${o}/conversations` && call.query.has("task_id"))
          return json(200, {
            conversations: [
              summary("c9", {
                task_id: "task-1",
                recent_messages: [
                  {
                    seq: 3,
                    author_kind: "USER",
                    author_name: "林",
                    preview: "检查验收结果",
                    created_at: "2026-10-29T08:00:00Z",
                  },
                  {
                    seq: 4,
                    author_kind: "AGENT",
                    author_name: "熊猫",
                    preview: "验收完成",
                    created_at: "2026-10-29T08:01:00Z",
                  },
                ],
              }),
            ],
            next_cursor: null,
          });
        if (call.path.endsWith("/timeline"))
          return json(200, {
            items: [
              {
                type: "activity",
                activity: {
                  id: "before",
                  kind: "task.created",
                  actor_name: "林",
                  created_at: "2026-10-28T08:00:00Z",
                  safe_diff: {},
                },
              },
              {
                type: "activity",
                activity: {
                  id: "after",
                  kind: "task.description_changed",
                  actor_name: "林",
                  created_at: "2026-10-30T08:00:00Z",
                  safe_diff: {},
                },
              },
            ],
            next_cursor: null,
          });
        return undefined;
      }),
    );
    const activity = await screen.findByRole("list", { name: "动态" });
    const card = await within(activity).findByRole("button", {
      name: "打开会话 熊猫",
    });
    const items = within(activity).getAllByRole("listitem");
    expect(items[0]).toHaveTextContent("创建了这个任务");
    expect(items[1]).toContainElement(card);
    expect(items[2]).toHaveTextContent("修改了描述");
    expect(card).toHaveAccessibleDescription(/检查验收结果/);
    expect(card).toHaveAccessibleDescription(/验收完成/);
    expect(card).not.toHaveAccessibleDescription(/摘要 c9/);
    await user.click(card);
    expect(screen.getByTestId("where")).toHaveTextContent("conversationId=c9");
  });

  it("记录分页失败保留已加载卡片，重试后去重并显示下一页", async () => {
    mockWidth(600);
    const user = userEvent.setup();
    let nextAttempts = 0;
    const server = renderAt(page, (call) => {
      if (call.path !== `${o}/conversations` || !call.query.has("task_id"))
        return undefined;
      if (!call.query.has("cursor"))
        return json(200, {
          conversations: [summary("c1")],
          next_cursor: "next",
        });
      nextAttempts++;
      return nextAttempts === 1
        ? json(500, { error: { code: "INTERNAL_ERROR", message: "failed" } })
        : json(200, {
            conversations: [summary("c1"), summary("c2")],
            next_cursor: null,
          });
    });
    await user.click(await screen.findByRole("tab", { name: /对话记录/ }));
    expect(await screen.findByText("摘要 c1")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "加载更多对话" }));
    const error = await screen.findByRole("alert", { name: "对话记录" });
    expect(screen.getByText("摘要 c1")).toBeInTheDocument();
    await user.click(within(error).getByRole("button", { name: "重试" }));
    expect(await screen.findByText("摘要 c2")).toBeInTheDocument();
    expect(screen.getAllByText("摘要 c1")).toHaveLength(1);
    expect(
      server.calls.filter((call) => call.query.get("cursor") === "next"),
    ).toHaveLength(2);
    expect(screen.queryByRole("button", { name: "加载更多对话" })).toBeNull();
  });

  it("初次加载失败显示重试，不把失败误报为没有记录", async () => {
    mockWidth(600);
    const user = userEvent.setup();
    renderAt(page, (call) =>
      call.path === `${o}/conversations`
        ? json(500, { error: { code: "INTERNAL_ERROR", message: "failed" } })
        : undefined,
    );
    await user.click(await screen.findByRole("tab", { name: /对话记录/ }));
    expect(
      await screen.findByRole("alert", { name: "对话记录" }),
    ).toBeInTheDocument();
    expect(screen.queryByText("这个任务还没有对话记录")).toBeNull();
  });

  it("宽栏折叠后恢复，开始日期在更多属性保存，截止日期不重复", async () => {
    mockWidth(900);
    const user = userEvent.setup();
    const server = renderAt(page);
    const sidebar = await screen.findByRole("complementary", { name: "属性" });
    const toggle = within(sidebar).getByRole("button", {
      name: "属性",
    });
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    expect(
      within(sidebar).queryByRole("group", { name: "开始日期" }),
    ).toBeNull();
    await user.click(toggle);
    expect(
      within(sidebar).queryByRole("button", { name: "处理人" }),
    ).toBeNull();
    await user.click(toggle);
    expect(
      within(sidebar).getByRole("button", { name: "处理人" }),
    ).toBeInTheDocument();
    await user.click(within(sidebar).getByRole("button", { name: "更多属性" }));
    const more = await screen.findByRole("dialog", { name: "更多属性" });
    expect(within(more).queryByRole("group", { name: "截止日期" })).toBeNull();
    const start = within(more).getByRole("group", { name: "开始日期" });
    await user.click(within(start).getByRole("button"));
    await user.click(await screen.findByRole("menuitem", { name: /今天/ }));
    await waitFor(() =>
      expect(server.find("PATCH", "/tasks/task-1")).toHaveLength(1),
    );
    expect(server.find("PATCH", "/tasks/task-1")[0]?.body).toHaveProperty(
      "start_date",
    );
  });
});

describe("任务用量和对话标识", () => {
  it.each([
    [2, "293", "读取 28.9k，写入 0"],
    [1, "≥ 293", "读取 ≥ 28.9k，写入 ≥ 0"],
    [0, "—", "—"],
  ] as const)(
    "显示完整会话数并区分已上报的 %i 次运行",
    async (reported, input, cache) => {
      mockWidth(900);
      const user = userEvent.setup();
      renderAt(
        page,
        conversations(["c9"], (call) =>
          call.path === o + "/conversations" && call.query.get("task_id")
            ? json(200, {
                conversations: [summary("c9", { task_id: "task-1" })],
                next_cursor: "next",
                task_stats: {
                  conversation_count: 51,
                  run_count: 2,
                  reported_runs: reported,
                  input: 293,
                  output: 7,
                  cache_read: 28900,
                  cache_write: 0,
                },
              })
            : undefined,
        ),
      );
      const trigger = await screen.findByRole("button", { name: "Token 用量" });
      const values = within(trigger.closest("section")!)
        .getAllByRole("definition")
        .map((x) => x.textContent);
      expect(values).toEqual([
        input,
        reported === 2 ? "7" : reported === 1 ? "≥ 7" : "—",
        cache,
        "2",
      ]);
      expect(
        screen.getByRole("tab", { name: /对话记录\s*51/ }),
      ).toBeInTheDocument();
      await user.click(trigger);
      expect(trigger).toHaveAttribute("aria-expanded", "false");
      expect(
        within(trigger.closest("section")!).queryByRole("definition"),
      ).toBeNull();
    },
  );

  it("从状态操作标记和清除，在查看详情时才打开原会话", async () => {
    mockWidth(900);
    const user = userEvent.setup();
    let item = summary("c9", { task_id: "task-1", status_mark: null });
    const server = renderAt(
      page,
      conversations(["c9"], (call) => {
        if (call.path === o + "/conversations" && call.query.get("task_id"))
          return json(200, { conversations: [item], next_cursor: null });
        if (call.path !== o + "/conversations/c9") return undefined;
        if (call.method === "PATCH") {
          const { status_mark } = call.body as {
            status_mark: "DISCARDED" | "DONE" | "NONE";
          };
          item = {
            ...item,
            status_mark: status_mark === "NONE" ? null : status_mark,
          };
        }
        return json(200, item);
      }),
    );
    const props = await screen.findByRole("complementary", { name: "属性" });
    const trigger = await within(props).findByRole("button", {
      name: "状态操作",
    });
    for (const [action, expected] of [
      ["标记为「已完成」", "已完成"],
      ["标记为「已废弃」", "已废弃"],
      ["移除状态标识", ""],
    ]) {
      await user.click(trigger);
      await user.click(await screen.findByRole("menuitem", { name: action }));
      await waitFor(() => expect(trigger.textContent).toBe(expected));
      expect(screen.getByTestId("where")).not.toHaveTextContent(
        "conversationId",
      );
    }
    await user.click(trigger);
    await user.click(await screen.findByRole("menuitem", { name: "查看详情" }));
    await waitFor(() =>
      expect(screen.getByTestId("where")).toHaveTextContent(
        "conversationId=c9",
      ),
    );
    expect(server.find("PATCH", "/conversations/c9")).toHaveLength(3);
    expect(
      await screen.findByRole("button", { name: "标记对话状态" }),
    ).toBeInTheDocument();
  });
});
