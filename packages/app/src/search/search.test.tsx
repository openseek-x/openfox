import { QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { createApi, HttpClient } from "@openfox/api";
import { I18nProvider } from "@openfox/i18n";
import { ToastProvider } from "@openfox/ui";

import { createQueryClient } from "../App";
import { ApiProvider, keys } from "../context";
import { CreateTaskProvider } from "../tasks/create/CreateTaskProvider";
import { SearchLauncher } from "./SearchLauncher";
import { matchesShortcut, resultPath } from "./model";

interface Call {
  path: string;
  query: URLSearchParams;
}

const json = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });

const SPACES = [
  {
    id: "s1",
    name: "产品",
    prefix: "PRD",
    my_role: "OWNER",
    visibility: "shared",
    status: "active",
  },
  {
    id: "s2",
    name: "研发",
    prefix: "ENG",
    my_role: "MEMBER",
    visibility: "shared",
    status: "active",
  },
];

const CONTACTS = {
  groups: [
    {
      kind: "SPACE",
      space_id: "s1",
      space_name: "产品",
      items: [
        {
          kind: "agent",
          id: "a1",
          name: "审阅员",
          title: "代码审阅",
          space_id: "s1",
        },
      ],
    },
    {
      kind: "HUMANS",
      space_id: null,
      space_name: null,
      items: [
        {
          kind: "user",
          id: "u2",
          name: "王五",
          email: "wang@openfox.dev",
          space_id: null,
        },
      ],
    },
  ],
  truncated: false,
};

const RESULT = {
  groups: [
    {
      type: "task",
      items: [
        {
          id: "t1",
          title: "修复登录",
          subtitle: "产品",
          space_id: "s1",
          display_id: "prd-12",
        },
      ],
    },
    {
      type: "project",
      items: [
        { id: "p1", title: "发布专项", subtitle: "产品", space_id: "s1" },
      ],
    },
    { type: "view", items: [] },
    {
      type: "agent",
      items: [{ id: "a1", title: "审阅员", subtitle: "产品", space_id: "s1" }],
    },
    {
      type: "person",
      items: [{ id: "u2", title: "王五", subtitle: "wang@openfox.dev" }],
    },
    {
      type: "space",
      items: [{ id: "s1", title: "产品", subtitle: "PRD", space_id: "s1" }],
    },
    {
      type: "doc",
      items: [
        { id: "d1", title: "需求文档", subtitle: "产品", space_id: "s1" },
      ],
    },
  ],
};

type Handler = (call: Call) => Response | Promise<Response> | undefined;

function Where() {
  const location = useLocation();
  return (
    <output aria-label="路径">{location.pathname + location.search}</output>
  );
}

function setup(
  options: {
    search?: Handler;
    role?: string;
    client?: ReturnType<typeof createQueryClient>;
    invitations?: unknown[];
    preferences?: unknown;
    iterations?: unknown;
    locale?: "zh-CN" | "en-US";
    path?: string;
  } = {},
) {
  const calls: Call[] = [];
  const transport = vi.fn((input: RequestInfo | URL) => {
    const url = new URL(String(input), "http://localhost");
    const call: Call = {
      path: url.pathname.replace(/^\/api/, ""),
      query: url.searchParams,
    };
    calls.push(call);
    if (call.path.endsWith("/search")) {
      return Promise.resolve(
        options.search?.(call) ?? json(200, { groups: [] }),
      );
    }
    if (call.path === "/v1/me")
      return Promise.resolve(
        json(200, {
          id: "u1",
          email: "member@example.test",
          avatar_url: "/avatar/u1",
        }),
      );
    if (call.path === "/v1/me/organizations")
      return Promise.resolve(
        json(200, {
          organizations: [
            {
              id: "o1",
              slug: "o",
              name: "组织",
              role: options.role ?? "OWNER",
              created_at: "2026-10-20T08:00:00Z",
            },
          ],
        }),
      );
    if (call.path === "/v1/organizations/o1/spaces")
      return Promise.resolve(json(200, { spaces: SPACES }));
    if (call.path === "/v1/organizations/o1/contacts")
      return Promise.resolve(json(200, CONTACTS));
    if (call.path.endsWith("/invites"))
      return Promise.resolve(json(200, { invitations: options.invitations ?? [] }));
    if (call.path.endsWith("/members"))
      return Promise.resolve(json(200, { members: [], next_cursor: "" }));
    if (call.path.endsWith("/agent-directory"))
      return Promise.resolve(json(200, { entries: [] }));
    if (call.path === "/v1/me/preferences" && options.preferences)
      return Promise.resolve(json(200, options.preferences));
    if (call.path.endsWith("/iterations") && options.iterations)
      return Promise.resolve(json(200, options.iterations));
    return Promise.resolve(
      json(404, {
        error: { code: "NOT_FOUND", message: "x", request_id: "r" },
      }),
    );
  });
  const queryClient = options.client ?? createQueryClient();
  queryClient.setDefaultOptions({ queries: { retry: false } });
  render(
    <I18nProvider initialLocale={options.locale ?? "zh-CN"}>
      <QueryClientProvider client={queryClient}>
        <ApiProvider
          api={createApi(
            new HttpClient("/api", transport as unknown as typeof fetch),
          )}
        >
          <ToastProvider>
            <MemoryRouter initialEntries={[options.path ?? "/o/o1/inbox"]}>
              <CreateTaskProvider orgId="o1">
                <Routes>
                  <Route
                    path="*"
                    element={
                      <>
                        <SearchLauncher orgId="o1" />
                        <Where />
                      </>
                    }
                  />
                </Routes>
              </CreateTaskProvider>
            </MemoryRouter>
          </ToastProvider>
        </ApiProvider>
      </QueryClientProvider>
    </I18nProvider>,
  );
  return calls;
}

const searchCalls = (calls: Call[]) =>
  calls.filter((call) => call.path.endsWith("/search"));
const where = () => screen.getByLabelText("路径").textContent;
const openButton = () => screen.getByRole("button", { name: /^搜索/ });
const paletteInput = () =>
  screen.findByRole("combobox", { name: "搜索关键词" });

afterEach(() => localStorage.clear());

describe("命令面板：打开与快捷键", () => {
  it("顶栏搜索按钮可用，点击打开，Esc 关闭并把焦点还给按钮", async () => {
    const user = userEvent.setup();
    setup();
    const button = openButton();
    expect(button).toBeEnabled();
    await user.click(button);
    const dialog = await screen.findByRole("dialog", { name: "搜索" });
    expect(
      within(dialog).getByText("搜索页面、任务和专项"),
    ).toBeInTheDocument();
    expect(await paletteInput()).toHaveFocus();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(button).toHaveFocus();
  });

  it("Ctrl+K 打开，再按一次关闭；已有其他对话框时不响应", async () => {
    const user = userEvent.setup();
    setup();
    await user.keyboard("{Control>}k{/Control}");
    expect(await screen.findByRole("dialog", { name: "搜索" })).toBeVisible();
    await user.keyboard("{Control>}k{/Control}");
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("按账号偏好里自定义的快捷键打开，默认键不再响应", async () => {
    const user = userEvent.setup();
    setup({
      preferences: {
        theme: "system",
        send_shortcut: "enter",
        command_shortcuts: { command_menu: "mod+j" },
        version: 1,
      },
    });
    await waitFor(() => expect(screen.getByText("Ctrl J")).toBeInTheDocument());
    await user.keyboard("{Control>}k{/Control}");
    expect(screen.queryByRole("dialog")).toBeNull();
    await user.keyboard("{Control>}j{/Control}");
    expect(await screen.findByRole("dialog", { name: "搜索" })).toBeVisible();
  });

  it("matchesShortcut：修饰键必须完全一致", () => {
    const event = (init: Partial<KeyboardEvent>) =>
      ({
        key: "k",
        code: "KeyK",
        metaKey: false,
        ctrlKey: false,
        shiftKey: false,
        altKey: false,
        ...init,
      }) as KeyboardEvent;
    expect(matchesShortcut(event({ metaKey: true }), "mod+k", true)).toBe(true);
    expect(matchesShortcut(event({ ctrlKey: true }), "mod+k", true)).toBe(
      false,
    );
    expect(
      matchesShortcut(event({ ctrlKey: true, shiftKey: true }), "mod+k", false),
    ).toBe(false);
    expect(
      matchesShortcut(
        event({ ctrlKey: true, shiftKey: true, key: "K" }),
        "mod+shift+k",
        false,
      ),
    ).toBe(true);
    expect(matchesShortcut(event({}), "mod+k", false)).toBe(false);
  });
});

describe("命令面板：搜索请求与结果", () => {
  it("输入防抖约 300ms，只对最终输入发一次请求", async () => {
    const user = userEvent.setup();
    const calls = setup({ search: () => json(200, RESULT) });
    await user.click(openButton());
    await user.type(await paletteInput(), "登录");
    expect(searchCalls(calls)).toHaveLength(0);
    await waitFor(() => expect(searchCalls(calls)).toHaveLength(1));
    const call = searchCalls(calls)[0]!;
    expect(call.query.get("q")).toBe("登录");
    expect(call.query.get("limit")).toBe("8");
    expect(call.query.get("offset")).toBe("0");
    expect(call.query.getAll("types")).toContain("message");
    expect(call.query.getAll("types")).not.toContain("doc");
  });

  it("结果按类分组显示，任务带编号", async () => {
    const user = userEvent.setup();
    setup({ search: () => json(200, RESULT) });
    await user.click(openButton());
    await user.type(await paletteInput(), "产");
    const list = await screen.findByRole("listbox", { name: "搜索结果" });
    for (const label of ["任务", "专项", "Agents", "人员", "空间"])
      expect(
        await within(list).findByRole("group", { name: label }),
      ).toBeVisible();
    // 没有结果的类别不显示分组
    expect(within(list).queryByRole("group", { name: "视图" })).toBeNull();
    expect(within(list).getByText("PRD-12")).toBeInTheDocument();
    expect(within(list).getByText("修复登录")).toBeInTheDocument();
  });

  it("只进入一个类型，可在行首退格退出并保留搜索词", async () => {
    const user = userEvent.setup();
    const calls = setup({ search: () => json(200, RESULT) });
    await user.click(openButton());
    const input = await paletteInput();
    await user.type(input, "x");
    await waitFor(() => expect(searchCalls(calls)).toHaveLength(1));
    await user.click(screen.getByRole("button", { name: "任务" }));
    await waitFor(() =>
      expect(searchCalls(calls).at(-1)!.query.getAll("types")).toEqual([
        "task",
      ]),
    );
    expect(searchCalls(calls).at(-1)!.query.get("limit")).toBe("50");
    expect(screen.queryByRole("group", { name: "搜索类别" })).toBeNull();
    expect(input).toHaveAttribute("placeholder", "搜索任务…");
    await user.keyboard("{Home}{Backspace}");
    expect(input).toHaveValue("x");
    await user.click(screen.getByRole("button", { name: "对话" }));
    await waitFor(() =>
      expect(searchCalls(calls).at(-1)!.query.getAll("types")).toEqual([
        "conversation",
        "message",
      ]),
    );
    expect(screen.queryByRole("button", { name: "按空间筛选" })).toBeNull();
  });

  it("按空间单选、复选，记住范围；切到对话时不带空间参数", async () => {
    const user = userEvent.setup();
    const calls = setup({ search: () => json(200, RESULT) });
    await user.click(openButton());
    await user.type(await paletteInput(), "x");
    await waitFor(() => expect(searchCalls(calls)).toHaveLength(1));
    await user.click(screen.getByRole("button", { name: "按空间筛选" }));
    await user.click(
      await screen.findByRole("menuitemcheckbox", { name: "产品" }),
    );
    expect(
      screen.getByRole("menuitemcheckbox", { name: "全部空间" }),
    ).toHaveAttribute("aria-checked", "mixed");
    await user.click(screen.getByRole("button", { name: "只选研发" }));
    await waitFor(() =>
      expect(searchCalls(calls).at(-1)!.query.getAll("space_ids")).toEqual([
        "s2",
      ]),
    );
    await user.click(screen.getByRole("button", { name: "对话" }));
    await waitFor(() =>
      expect(searchCalls(calls).at(-1)!.query.has("space_ids")).toBe(false),
    );
    await user.keyboard("{Escape}");
    await user.click(openButton());
    expect(
      screen.getByRole("button", { name: "按空间筛选" }),
    ).toHaveTextContent("研发");
  });

  it("Tab 进入控件，空间菜单内按方向键选择；Esc 先关菜单再关搜索", async () => {
    const user = userEvent.setup();
    setup();
    await user.click(openButton());
    expect(await paletteInput()).toHaveFocus();
    await user.tab();
    expect(screen.getByRole("button", { name: "关闭搜索" })).toHaveFocus();
    await user.tab();
    const scope = screen.getByRole("button", { name: "按空间筛选" });
    expect(scope).toHaveFocus();
    await user.keyboard("{Enter}{ArrowDown}{ArrowDown}");
    const item = await screen.findByRole("menuitemcheckbox", { name: "产品" });
    expect(item).toHaveFocus();
    await user.keyboard(" ");
    expect(item).toHaveAttribute("aria-checked", "false");
    // Native Space activates a focused checkbox button. Escape returns to its trigger.
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("menu")).toBeNull();
    expect(scope).toHaveFocus();
    expect(screen.getByRole("dialog", { name: "搜索" })).toBeVisible();
    await user.keyboard("{Escape}");
    expect(openButton()).toHaveFocus();
  });

  it("无结果的查询也能用回车加入历史，重复大小写只保留最近一条", async () => {
    const user = userEvent.setup();
    setup();
    await user.click(openButton());
    const input = await paletteInput();
    for (const query of ["zxq", "ZXQ"]) {
      await user.clear(input);
      await user.type(input, query + "{Enter}");
      await screen.findByText("未找到结果。");
    }
    await user.keyboard("{Escape}");
    await user.click(openButton());
    expect(
      await screen.findByRole("button", { name: "搜索 ZXQ" }),
    ).toBeVisible();
    expect(screen.queryByRole("button", { name: "搜索 zxq" })).toBeNull();
  });

  it("关键词可以打开已有页面", async () => {
    const user = userEvent.setup();
    setup();
    await user.click(openButton());
    await user.type(await paletteInput(), "收件");
    const pages = await screen.findByRole("group", { name: "页面" });
    await user.click(within(pages).getByRole("option", { name: "收件箱" }));
    expect(where()).toBe("/o/o1/inbox");
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("搜索历史保存类型和关键词，选择后恢复，清空需确认", async () => {
    const user = userEvent.setup();
    setup({ search: () => json(200, RESULT) });
    await user.click(openButton());
    await user.click(screen.getByRole("button", { name: "任务" }));
    await user.type(await paletteInput(), "登录");
    await user.click(await screen.findByRole("option", { name: /修复登录/ }));
    await user.click(openButton());
    await user.click(await screen.findByRole("button", { name: "搜索 登录" }));
    expect(await paletteInput()).toHaveValue("登录");
    expect(screen.getByRole("button", { name: "退出任务搜索" })).toBeVisible();
    await user.click(screen.getByRole("button", { name: "清除" }));
    await user.click(screen.getByRole("button", { name: "清空搜索历史" }));
    await user.click(screen.getByRole("button", { name: "取消" }));
    expect(screen.getByRole("button", { name: "搜索 登录" })).toBeVisible();
    await user.click(screen.getByRole("button", { name: "清空搜索历史" }));
    await user.click(screen.getByRole("button", { name: "清空" }));
    expect(screen.queryByRole("button", { name: "搜索 登录" })).toBeNull();
  });

  it("受邀处理人历史只存 ID；同一账号降权后不查询名单或从缓存显示邮箱，仍可移除条件", async () => {
    const user = userEvent.setup();
    const email = "pending@example.test";
    const client = createQueryClient();
    const options = {
      client,
      role: "OWNER",
      invitations: [
        {
          id: "iv1",
          email,
          role: "MEMBER",
          status: "PENDING",
          expires_at: "2099-01-01",
          space_ids: ["s1"],
        },
      ],
      search: (call: Call) =>
        options.role === "MEMBER" && call.query.has("assignee")
          ? json(403, {
              error: {
                code: "FORBIDDEN",
                message: "Forbidden",
                request_id: "r",
              },
            })
          : json(200, RESULT),
    };
    const calls = setup(options);
    const filter = () => screen.getByRole("button", { name: "处理人" });
    const invites = () =>
      calls.filter((call) => call.path.endsWith("/invites")).length;
    await user.click(openButton());
    await user.click(screen.getByRole("button", { name: "任务" }));
    await user.click(filter());
    await user.click(
      await screen.findByRole("option", { name: /pending@example.test/ }),
    );
    await user.type(await paletteInput(), "登录");
    await waitFor(() =>
      expect(searchCalls(calls).at(-1)?.query.get("q")).toBe("登录"),
    );
    expect(searchCalls(calls).at(-1)?.query.getAll("assignee")).toEqual([
      "invitation:iv1",
    ]);
    await user.click(await screen.findByRole("option", { name: /修复登录/ }));
    const history = localStorage.getItem("openfox:search-history:o1:u1");
    expect(history).toContain("invitation:iv1");
    expect(history).not.toContain(email);
    const restore = async () => {
      await user.click(openButton());
      await user.click(
        await screen.findByRole("button", { name: "搜索 登录" }),
      );
    };
    await restore();
    await waitFor(() => expect(filter()).toHaveTextContent(email));
    await user.keyboard("{Escape}");

    // The same account still has its previous query cache and personal history.
    options.role = "MEMBER";
    client.setQueryData(keys.organizations, [{ id: "o1", role: options.role }]);
    const before = invites();
    await restore();
    await user.click(filter());
    await screen.findByRole("option", { name: /无处理人/ });
    expect(screen.queryByText(email)).toBeNull();
    expect(
      screen.queryByRole("option", { name: /pending@example.test/ }),
    ).toBeNull();
    expect(invites()).toBe(before);
    await user.keyboard("{Escape}");
    await screen.findByText("搜索失败，请重试");
    expect(screen.queryByRole("option", { name: /修复登录/ })).toBeNull();
    await user.click(screen.getByRole("button", { name: "移除处理人筛选" }));
    await waitFor(() =>
      expect(searchCalls(calls).at(-1)?.query.has("assignee")).toBe(false),
    );
  });

  it("会话与消息分组，查看更多只推进该组，消息定位到原文", async () => {
    const user = userEvent.setup();
    const calls = setup({
      search: ({ query }) =>
        json(200, {
          groups:
            query.get("offset") === "1"
              ? [
                  {
                    type: "message",
                    total: 2,
                    items: [
                      {
                        id: "m2",
                        message_id: "m2",
                        conversation_id: "c1",
                        conversation_kind: "direct",
                        title: "同事",
                        subtitle: "找命中第二条",
                      },
                    ],
                  },
                ]
              : [
                  {
                    type: "conversation",
                    total: 1,
                    items: [
                      {
                        id: "c1",
                        conversation_id: "c1",
                        conversation_kind: "direct",
                        title: "对话标题",
                        subtitle: "",
                      },
                    ],
                  },
                  {
                    type: "message",
                    total: 2,
                    items: [
                      {
                        id: "m1",
                        sender_kind: "user",
                        sender_id: "u1",
                        sender_name: "本人",
                        occurred_at: "2026-11-05T12:00:00Z",
                        message_id: "m1",
                        conversation_id: "c1",
                        conversation_kind: "direct",
                        title: "同事",
                        subtitle: "第一条命中",
                      },
                    ],
                  },
                ],
        }),
    });
    await user.click(openButton());
    await user.click(screen.getByRole("button", { name: "对话" }));
    await user.type(await paletteInput(), "命中");
    expect(await screen.findByText("共 3 个结果")).toBeVisible();
    const group = screen.getByRole("group", { name: "消息" });
    expect(within(group).getByText("命中", { selector: "mark" })).toBeVisible();
    expect(group.querySelector("time")?.dateTime).toBe("2026-11-05T12:00:00Z");
    expect(group.querySelector("img")?.getAttribute("src")).toBe("/avatar/u1");
    await user.click(within(group).getByRole("option", { name: "查看更多" }));
    await user.click(
      await within(group).findByRole("option", { name: /第二条/ }),
    );
    expect(searchCalls(calls).at(-1)!.query.getAll("types")).toEqual([
      "message",
    ]);
    expect(where()).toBe("/o/o1/chat/direct/c1?message=m2");
  });

  it("加载中、空结果、出错各有状态，出错可重试", async () => {
    const user = userEvent.setup();
    let mode: "slow" | "empty" | "fail" = "slow";
    let release: (() => void) | undefined;
    const calls = setup({
      search: () => {
        if (mode === "slow")
          return new Promise<Response>((resolve) => {
            release = () => resolve(json(200, { groups: [] }));
          }) as unknown as Response;
        if (mode === "fail")
          return json(500, {
            error: { code: "X", message: "x", request_id: "r" },
          });
        return json(200, { groups: [{ type: "task", items: [] }] });
      },
    });
    await user.click(openButton());
    await user.type(await paletteInput(), "zzz");
    expect(await screen.findByText("加载中…")).toBeInTheDocument();
    mode = "empty";
    release?.();
    expect(await screen.findByText("未找到结果。")).toBeInTheDocument();
    await user.type(await paletteInput(), "q");
    mode = "fail";
    expect(await screen.findByText("搜索失败，请重试")).toBeInTheDocument();
    mode = "empty";
    await user.click(screen.getByRole("button", { name: "重试" }));
    expect(await screen.findByText("未找到结果。")).toBeInTheDocument();
    expect(searchCalls(calls).length).toBeGreaterThanOrEqual(3);
  });
});

describe("命令面板：键盘与跳转", () => {
  it("上下键选择、回车打开：第二项是人员，跳到联系人页", async () => {
    const user = userEvent.setup();
    setup({ search: () => json(200, RESULT) });
    await user.click(openButton());
    const input = await paletteInput();
    await user.type(input, "产");
    await screen.findByText("发布专项");
    const options = () =>
      within(screen.getByRole("listbox")).getAllByRole("option");
    expect(options()[0]).toHaveAttribute("aria-selected", "true");
    await user.keyboard("{ArrowDown}");
    expect(options()[1]).toHaveAttribute("aria-selected", "true");
    await user.keyboard("{ArrowUp}{ArrowUp}");
    // 向上越过第一项回到最后一项（循环）
    expect(options()[options().length - 1]).toHaveAttribute(
      "aria-selected",
      "true",
    );
    await user.keyboard("{ArrowDown}{ArrowDown}{Enter}");
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(where()).toBe("/o/o1/members?contact=user:u2");
  });

  it("各类结果的跳转地址", () => {
    const base = { space_id: "s1", display_id: undefined };
    expect(
      resultPath("o1", "task", {
        id: "t1",
        space_id: "s1",
        display_id: "prd-12",
      }),
    ).toBe("/o/o1/spaces/s1/tasks?task=PRD-12");
    expect(resultPath("o1", "project", { id: "p1", ...base })).toBe(
      "/o/o1/spaces/s1/projects/p1",
    );
    expect(resultPath("o1", "view", { id: "v1", ...base })).toBe(
      "/o/o1/spaces/s1/views/v1",
    );
    expect(resultPath("o1", "doc", { id: "d1", ...base })).toBe(
      "/o/o1/spaces/s1/docs/d1",
    );
    expect(resultPath("o1", "space", { id: "s1", ...base })).toBe(
      "/o/o1/spaces/s1/tasks",
    );
    // 9a · D31：有空间的 Agent 打开设置抽屉，没有空间才回退成员页
    expect(resultPath("o1", "agent", { id: "a1", ...base })).toBe(
      "/o/o1/agents?agentSettings=a1",
    );
    expect(
      resultPath("o1", "agent", {
        id: "a1",
        space_id: undefined,
        display_id: undefined,
      }),
    ).toBe("/o/o1/members?contact=agent:a1");
    expect(
      resultPath("o1", "person", {
        id: "u2",
        space_id: undefined,
        display_id: undefined,
      }),
    ).toBe("/o/o1/members?contact=user:u2");
    expect(
      resultPath("o1", "project", {
        id: "p1",
        space_id: undefined,
        display_id: undefined,
      }),
    ).toBeNull();
  });

  it("点击任务结果：在当前页带 ?task= 打开抽屉（不再跳到空间任务页）", async () => {
    const user = userEvent.setup();
    setup({ search: () => json(200, RESULT) });
    await user.click(openButton());
    await user.type(await paletteInput(), "登录");
    await user.click(await screen.findByRole("option", { name: /修复登录/ }));
    expect(where()).toBe("/o/o1/inbox?task=PRD-12");
  });

  it("点击 Agent 结果：打开 Agent 设置抽屉（9a · D31）", async () => {
    const user = userEvent.setup();
    setup({ search: () => json(200, RESULT) });
    await user.click(openButton());
    await user.type(await paletteInput(), "审阅");
    await user.click(await screen.findByRole("option", { name: /审阅员/ }));
    expect(where()).toBe("/o/o1/agents?agentSettings=a1");
  });

  it("改完输入马上回车：等本次输入的结果到了再打开，不打开上一次的结果", async () => {
    const user = userEvent.setup();
    const other = {
      groups: [
        {
          type: "task",
          items: [
            {
              id: "t2",
              title: "另一个任务",
              subtitle: "产品",
              space_id: "s1",
              display_id: "prd-13",
            },
          ],
        },
      ],
    };
    setup({
      search: (call) =>
        json(200, call.query.get("q") === "prd-13" ? other : RESULT),
    });
    await user.click(openButton());
    const input = await paletteInput();
    await user.type(input, "登录");
    await screen.findByRole("option", { name: /修复登录/ });
    // 列表里还是「登录」的结果时改输入并立即回车
    await user.clear(input);
    await user.type(input, "prd-13{Enter}");
    await waitFor(() => expect(where()).toBe("/o/o1/inbox?task=PRD-13"));
  });

  it("人员与 Agent 跳到成员页对应联系人", async () => {
    const user = userEvent.setup();
    setup({ search: () => json(200, RESULT) });
    await user.click(openButton());
    await user.type(await paletteInput(), "王");
    await user.click(await screen.findByRole("option", { name: /王五/ }));
    expect(where()).toBe("/o/o1/members?contact=user:u2");
  });
});

describe("命令面板：空输入的建议与命令", () => {
  it("显示人员、命令、空间、Agent 建议，不发搜索请求", async () => {
    const user = userEvent.setup();
    const calls = setup();
    await user.click(openButton());
    for (const label of ["人员", "命令", "空间", "Agents"])
      expect(await screen.findByRole("group", { name: label })).toBeVisible();
    expect(await screen.findByRole("option", { name: /王五/ })).toBeVisible();
    expect(screen.getByRole("option", { name: /审阅员/ })).toBeVisible();
    expect(
      within(screen.getByRole("listbox")).getByRole("option", { name: /研发/ }),
    ).toBeVisible();
    for (const name of [
      "新建任务",
      "新建空间",
      "前往空间",
      "前往迭代",
      "前往当前迭代",
    ])
      expect(screen.getByRole("option", { name })).toBeVisible();
    // 现有入口不支持的命令不显示
    expect(screen.queryByText("前往即将开始的迭代")).toBeNull();
    expect(screen.queryByText("新建专项")).toBeNull();
    expect(screen.queryByText("前往归档")).toBeNull();
    expect(searchCalls(calls)).toHaveLength(0);
  });

  it("非管理员没有「新建空间」", async () => {
    const user = userEvent.setup();
    setup({ role: "MEMBER" });
    await user.click(openButton());
    await screen.findByRole("option", { name: "新建任务" });
    expect(screen.queryByRole("option", { name: "新建空间" })).toBeNull();
  });

  it("新建空间：地址带 ?createSpace=1 并关闭面板", async () => {
    const user = userEvent.setup();
    setup();
    await user.click(openButton());
    await user.click(await screen.findByRole("option", { name: "新建空间" }));
    expect(screen.queryByRole("dialog", { name: "搜索" })).toBeNull();
    expect(where()).toBe("/o/o1/inbox?createSpace=1");
  });

  it("新建任务：关闭面板并打开新建任务对话框", async () => {
    const user = userEvent.setup();
    setup();
    await user.click(openButton());
    await user.click(await screen.findByRole("option", { name: "新建任务" }));
    await waitFor(() =>
      expect(screen.queryByRole("dialog", { name: "搜索" })).toBeNull(),
    );
    expect(await screen.findByRole("dialog")).toBeVisible();
  });

  it("前往空间：留在面板里并切到「空间」类别", async () => {
    const user = userEvent.setup();
    setup();
    await user.click(openButton());
    await user.click(await screen.findByRole("option", { name: "前往空间" }));
    expect(screen.getByRole("dialog", { name: "搜索" })).toBeVisible();
    expect(screen.getByRole("button", { name: "退出空间搜索" })).toBeVisible();
    await user.click(
      await within(screen.getByRole("listbox")).findByRole("option", {
        name: /研发/,
      }),
    );
    expect(where()).toBe("/o/o1/spaces/s2/tasks");
  });

  it("前往迭代：不在空间页时用第一个空间；在空间页用当前空间", async () => {
    const user = userEvent.setup();
    setup({ path: "/o/o1/spaces/s2/tasks" });
    await user.click(openButton());
    await user.click(await screen.findByRole("option", { name: "前往迭代" }));
    expect(where()).toBe("/o/o1/spaces/s2/iterations");
  });

  it("前往当前迭代：有进行中的迭代就跳转，没有就提示", async () => {
    const user = userEvent.setup();
    setup({ iterations: { iterations: [{ id: "i9" }], next_cursor: "" } });
    await user.click(openButton());
    await user.click(
      await screen.findByRole("option", { name: "前往当前迭代" }),
    );
    await waitFor(() => expect(where()).toBe("/o/o1/spaces/s1/iterations/i9"));
  });

  it("前往当前迭代：没有进行中的迭代时给出提示且不跳转", async () => {
    const user = userEvent.setup();
    setup({ iterations: { iterations: [], next_cursor: "" } });
    await user.click(openButton());
    await user.click(
      await screen.findByRole("option", { name: "前往当前迭代" }),
    );
    expect(
      await screen.findByText("该空间没有进行中的迭代"),
    ).toBeInTheDocument();
    expect(where()).toBe("/o/o1/inbox");
  });

  it("输入关键词时命令按名称过滤", async () => {
    const user = userEvent.setup();
    setup({ search: () => json(200, { groups: [] }) });
    await user.click(openButton());
    await user.type(await paletteInput(), "迭代");
    expect(
      await screen.findByRole("option", { name: "前往迭代" }),
    ).toBeVisible();
    await waitFor(() =>
      expect(screen.queryByRole("option", { name: "新建任务" })).toBeNull(),
    );
  });
});

describe("命令面板：文案", () => {
  it.each(["zh-CN", "en-US"] as const)("%s 下不露出文案键", async (locale) => {
    const user = userEvent.setup();
    setup({ locale, search: () => json(200, RESULT) });
    await user.click(screen.getByRole("button", { name: /Search|搜索/ }));
    await screen.findByRole("dialog");
    expect(document.body.textContent).not.toMatch(
      /commandPalette\.|shell\.search/,
    );
    const input = await screen.findByRole("combobox", {
      name: /Search keywords|搜索关键词/,
    });
    await user.type(input, "x");
    await screen.findByRole("listbox");
    await waitFor(() => expect(document.body.textContent).toMatch(/PRD-12/));
    expect(document.body.textContent).not.toMatch(
      /commandPalette\.|shell\.search/,
    );
  });
});
