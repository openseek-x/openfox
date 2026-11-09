import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, useLocation } from "react-router-dom";
import { afterEach, describe, expect, it } from "vitest";

import { createApi, HttpClient } from "@openfox/api";
import { dictionaries } from "@openfox/i18n";

import { App, createQueryClient } from "../App";
import { readLayoutMode, railItemsFor, writeLayoutMode } from "./layoutMode";

// 自定义导航（任务优先 / 对话优先）与侧栏进阶指引就地展开。

type Handler = (url: string) => Response;

const json = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });

const GUIDE_KEYS = [
  "connect_jira_or_tapd",
  "connect_feishu",
  "connect_git",
  "create_crew",
  "create_automation",
  "invite_member",
];

/** 「开始使用」接口：六个进阶步骤分散在不同分类里，done 指定哪些已完成。 */
function progress(done: string[]) {
  const step = (key: string, points: number) => ({
    key,
    points,
    completed: done.includes(key),
    available: true,
  });
  return {
    score: done.length * 10,
    total: 60,
    categories: [
      {
        key: "get_started",
        steps: [step("connect_jira_or_tapd", 10), step("create_agent", 10)],
      },
      {
        key: "collaboration",
        steps: [step("invite_member", 10), step("connect_feishu", 10)],
      },
      {
        key: "agents",
        steps: [step("create_crew", 10)],
      },
      { key: "space_management", steps: [step("connect_git", 10)] },
      { key: "automations", steps: [step("create_automation", 10)] },
    ],
  };
}

function handler(options: { role?: string; done?: string[] } = {}): Handler {
  const role = options.role ?? "OWNER";
  return (url) => {
    if (url.endsWith("/v1/me"))
      return json(200, { id: "u1", email: "lin@openfox.dev" });
    if (url.endsWith("/v1/me/organizations"))
      return json(200, {
        organizations: [
          {
            id: "o1",
            slug: "o1",
            name: "组织一",
            role,
            created_at: "2026-10-28T00:00:00Z",
          },
        ],
      });
    if (url.endsWith("/get-started"))
      return json(200, progress(options.done ?? []));
    if (url.includes("/inbox/stats"))
      return json(200, {
        counts: {
          inbox: 0,
          inbox_unread: 0,
          inbox_important_unread: 0,
          inbox_other_unread: 0,
          later: 0,
          cleared: 0,
        },
      });
    if (url.endsWith("/spaces"))
      return json(200, {
        spaces: [
          {
            id: "s1",
            prefix: "open",
            name: "默认空间",
            visibility: "shared",
            version: 1,
            created_at: "2026-10-28T00:00:00Z",
            my_role: "OWNER",
            features: {
              views: false,
              projects: false,
              iterations: false,
              triage: false,
            },
          },
        ],
      });
    if (url.includes("/conversations"))
      return json(200, { conversations: [], next_cursor: null });
    return json(500, {});
  };
}

let probe = "";
function Probe() {
  const location = useLocation();
  probe = location.pathname + location.search;
  return null;
}

function renderAt(path: string, h: Handler) {
  localStorage.setItem("openfox:onboarding:o1", JSON.stringify({}));
  const api = createApi(
    new HttpClient("/api", ((input: RequestInfo | URL) =>
      Promise.resolve(h(String(input)))) as unknown as typeof fetch),
  );
  const queryClient = createQueryClient();
  queryClient.setDefaultOptions({ queries: { retry: false } });
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Probe />
      <App api={api} queryClient={queryClient} locale="zh-CN" />
    </MemoryRouter>,
  );
}

afterEach(() => {
  cleanup();
  sessionStorage.clear();
  localStorage.clear();
});

const railLink = (name: string) =>
  within(screen.getByRole("navigation", { name: "主导航" })).getByRole("link", {
    name,
  });

/** 主导航里的链接顺序（只取 aria-label）。 */
function railOrder(): string[] {
  const rail = screen.getByRole("navigation", { name: "主导航" });
  return within(rail)
    .getAllByRole("link")
    .map((link) => link.getAttribute("aria-label") ?? "");
}

describe("布局模式：纯函数", () => {
  it("任务优先 / 对话优先的顺序、名称与图标", () => {
    expect(
      railItemsFor("task").map((i) => [i.module, i.label, i.icon]),
    ).toEqual([
      ["home", "shell.rail.home", "home"],
      ["chat", "shell.rail.chat", "messages"],
      ["members", "shell.rail.members", "users"],
      ["settings", "shell.rail.settings", "hexagon-cog"],
    ]);
    expect(
      railItemsFor("chat").map((i) => [i.module, i.label, i.icon]),
    ).toEqual([
      ["chat", "shell.rail.chat", "messages"],
      ["home", "shell.rail.tasks", "square-check"],
      ["members", "shell.rail.members", "users"],
      ["settings", "shell.rail.settings", "hexagon-cog"],
    ]);
  });

  it("未知或缺失的存储值回到任务优先", () => {
    expect(readLayoutMode()).toBe("task");
    localStorage.setItem("openfox:layout-mode", "weird");
    expect(readLayoutMode()).toBe("task");
    writeLayoutMode("chat");
    expect(readLayoutMode()).toBe("chat");
    writeLayoutMode("task");
    expect(localStorage.getItem("openfox:layout-mode")).toBeNull();
  });
});

describe("自定义导航", () => {
  it("默认任务优先；切到对话优先立即换序，刷新（重新挂载）后保留", async () => {
    const user = userEvent.setup();
    renderAt("/o/o1/inbox", handler());
    await screen.findByRole("link", { name: "首页" });
    expect(railOrder()).toEqual(["首页", "对话", "成员", "全部设置"]);

    await user.click(screen.getByRole("button", { name: "自定义导航" }));
    const group = screen.getByRole("group", { name: "布局模式" });
    expect(
      within(group).getByRole("radio", { name: "任务优先" }),
    ).toBeChecked();
    await user.click(within(group).getByRole("radio", { name: "对话优先" }));
    expect(railOrder()).toEqual(["对话", "任务", "成员", "全部设置"]);
    expect(localStorage.getItem("openfox:layout-mode")).toBe("chat");

    cleanup();
    renderAt("/o/o1/inbox", handler());
    await screen.findByRole("navigation", { name: "主导航" });
    await waitFor(() => railLink("任务"));
    expect(railOrder()).toEqual(["对话", "任务", "成员", "全部设置"]);
  });

  it("按钮有可访问名称；单选可用键盘操作", async () => {
    const user = userEvent.setup();
    renderAt("/o/o1/inbox", handler());
    const button = await screen.findByRole("button", { name: "自定义导航" });
    button.focus();
    await user.keyboard("{Enter}");
    const task = screen.getByRole("radio", { name: "任务优先" });
    task.focus();
    await user.keyboard("{ArrowDown}");
    expect(screen.getByRole("radio", { name: "对话优先" })).toBeChecked();
    expect(railOrder()[0]).toBe("对话");
    await user.keyboard("{Escape}");
    expect(
      screen.queryByRole("group", { name: "布局模式" }),
    ).not.toBeInTheDocument();
  });

  it("对话优先下点「任务」回到记住的 home 地址；默认落点仍是收件箱", async () => {
    localStorage.setItem("openfox:layout-mode", "chat");
    const user = userEvent.setup();
    renderAt("/o/o1/spaces/s1/tasks?task=A-1", handler());
    await screen.findByRole("navigation", { name: "主导航" });
    await waitFor(() => railLink("任务"));
    await user.click(railLink("全部设置"));
    await waitFor(() => expect(probe).toBe("/o/o1/settings/labels"));
    await user.click(railLink("任务"));
    await waitFor(() => expect(probe).toBe("/o/o1/spaces/s1/tasks?task=A-1"));
    await user.click(railLink("对话"));
    await waitFor(() => expect(probe).toBe("/o/o1/chat"));
  });

  it("组织根地址在对话优先下仍落到收件箱", async () => {
    localStorage.setItem("openfox:layout-mode", "chat");
    renderAt("/o/o1", handler());
    await waitFor(() => expect(probe).toBe("/o/o1/inbox"));
  });
});

describe("侧栏进阶指引", () => {
  const sidebar = () => screen.getByRole("navigation", { name: "页面导航" });

  it("默认收起；展开 / 收起并记在本机，进度与数据源一致", async () => {
    const user = userEvent.setup();
    renderAt(
      "/o/o1/inbox",
      handler({ done: ["connect_git", "invite_member", "create_agent"] }),
    );
    const toggle = await screen.findByRole("button", { name: "展开进阶指引" });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    // create_agent 不在六项里，不计入
    await waitFor(() =>
      expect(within(sidebar()).getByText("2/6")).toBeInTheDocument(),
    );
    expect(
      within(sidebar()).queryByText("设置 Git 提交和 PR 自动关联到任务"),
    ).not.toBeInTheDocument();

    await user.click(toggle);
    expect(
      screen.getByRole("button", { name: "收起进阶指引" }),
    ).toHaveAttribute("aria-expanded", "true");
    expect(localStorage.getItem("openfox:advanced-guide-open")).toBe("1");
    expect(
      within(sidebar()).getByText("设置 Git 提交和 PR 自动关联到任务"),
    ).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "收起进阶指引" }));
    expect(localStorage.getItem("openfox:advanced-guide-open")).toBeNull();
  });

  it("展开后六项的跳转地址、已完成标记与「查看全部」", async () => {
    localStorage.setItem("openfox:advanced-guide-open", "1");
    renderAt("/o/o1/inbox", handler({ done: ["create_crew"] }));
    await screen.findByRole("button", { name: "收起进阶指引" });
    const hrefs: Array<[string, string]> = [
      ["导入 Jira、TAPD、禅道等平台里的任务", "/o/o1/settings/import-export"],
      ["集成飞书、钉钉、企业微信等 IM 平台", "/o/o1/settings/integrations"],
      ["设置 Git 提交和 PR 自动关联到任务", "/o/o1/settings/integrations"],
      ["创建一个 Agent 小队", "/o/o1/settings/crews"],
      ["创建一个 Agent 自动化", "/o/o1/spaces/s1/automations"],
      ["邀请团队成员", "/o/o1/settings/members"],
    ];
    for (const [name, href] of hrefs) {
      const link = await within(sidebar()).findByRole("link", {
        name: new RegExp(name),
      });
      expect(link).toHaveAttribute("href", href);
    }
    // 已完成项带「已完成」读屏文字，其余没有
    expect(
      within(sidebar()).getByRole("link", { name: /创建一个 Agent 小队/ }),
    ).toHaveTextContent("已完成");
    expect(
      within(sidebar()).getByRole("link", { name: /邀请团队成员/ }),
    ).not.toHaveTextContent("已完成");
    expect(
      within(sidebar()).getByRole("link", { name: "查看全部" }),
    ).toHaveAttribute("href", "/o/o1/settings/get-started?guide=advanced");
    await userEvent
      .setup()
      .click(screen.getByRole("link", { name: "查看全部" }));
    await waitFor(() =>
      expect(probe).toBe("/o/o1/settings/get-started?guide=advanced"),
    );
  });

  it("访客：需要管理权限的项禁用并说明原因，其余可点", async () => {
    localStorage.setItem("openfox:advanced-guide-open", "1");
    renderAt("/o/o1/inbox", handler({ role: "GUEST" }));
    await screen.findByRole("button", { name: "收起进阶指引" });
    await waitFor(() =>
      expect(
        within(sidebar()).getAllByText("仅所有者和管理员可用").length,
      ).toBe(3),
    );
    const invite = within(sidebar())
      .getByText("邀请团队成员")
      .closest("[role=link]");
    expect(invite).toHaveAttribute("aria-disabled", "true");
    expect(invite).not.toHaveAttribute("href");
    expect(within(sidebar()).getAllByText("访客不可用").length).toBe(3);
  });

  it("成员：只有管理类项禁用，Git / 小队 / 自动化可用", async () => {
    localStorage.setItem("openfox:advanced-guide-open", "1");
    renderAt("/o/o1/inbox", handler({ role: "MEMBER" }));
    await screen.findByRole("button", { name: "收起进阶指引" });
    await waitFor(() =>
      expect(
        within(sidebar()).getAllByText("仅所有者和管理员可用").length,
      ).toBe(3),
    );
    expect(
      within(sidebar()).getByRole("link", { name: /创建一个 Agent 小队/ }),
    ).toHaveAttribute("href", "/o/o1/settings/crews");
    expect(within(sidebar()).queryByText("访客不可用")).not.toBeInTheDocument();
  });

  it("隐藏开关仍然生效", async () => {
    localStorage.setItem("openfox:advanced-guide-hidden", "1");
    localStorage.setItem("openfox:advanced-guide-open", "1");
    renderAt("/o/o1/inbox", handler());
    await screen.findByRole("link", { name: "首页" });
    await screen.findByText("社区 & 帮助文档");
    expect(
      screen.queryByRole("button", { name: /进阶指引/ }),
    ).not.toBeInTheDocument();
  });
});

describe("文案", () => {
  it("新增的 shell 文案键中英文都有，渲染结果不露出键名", async () => {
    const keys = [
      "rail.tasks",
      "guide.expand",
      "guide.collapse",
      "guide.progress",
      "guide.import",
      "guide.im",
      "guide.git",
      "guide.crew",
      "guide.automation",
      "guide.invite",
      "guide.done",
      "guide.needAdmin",
      "guide.needMember",
      "guide.viewAll",
    ];
    for (const locale of ["zh-CN", "en-US"] as const) {
      const shell = (dictionaries[locale] as unknown as Record<string, unknown>)
        .shell as Record<string, unknown>;
      for (const key of keys) {
        const value = key
          .split(".")
          .reduce<unknown>(
            (o, k) => (o as Record<string, unknown>)?.[k],
            shell,
          );
        expect(typeof value, `${locale} shell.${key}`).toBe("string");
      }
    }
    localStorage.setItem("openfox:layout-mode", "chat");
    localStorage.setItem("openfox:advanced-guide-open", "1");
    renderAt("/o/o1/inbox", handler({ role: "GUEST" }));
    await screen.findByRole("button", { name: "收起进阶指引" });
    await waitFor(() =>
      expect(within(sidebar2()).getByText("邀请团队成员")).toBeInTheDocument(),
    );
    expect(document.body.textContent).not.toMatch(/\bshell\.[a-zA-Z.]+/);
    expect(GUIDE_KEYS).toHaveLength(6);
  });
});

function sidebar2() {
  return screen.getByRole("navigation", { name: "页面导航" });
}
