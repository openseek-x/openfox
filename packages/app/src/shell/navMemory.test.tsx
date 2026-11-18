import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, useLocation, useNavigate } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { createApi, HttpClient } from "@openfox/api";

import { App, createQueryClient } from "../App";
import { summary } from "../conversations/fixtures";
import {
  clearNavMemory,
  moduleOf,
  readNavMemory,
  recordNav,
  sanitizeNavTarget,
} from "./navMemory";

type Handler = (url: string) => Response;
type Extra = (url: string) => Response | undefined;

const json = (status: number, body: unknown) =>
  new Response(status === 204 ? null : JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });

const orgs = ["o1", "o2"].map((id) => ({
  id,
  slug: id,
  name: `组织${id}`,
  role: "OWNER",
  created_at: "2026-10-28T00:00:00Z",
}));

const agent = (id: string) => ({
  kind: "agent",
  id,
  name: `Agent ${id}`,
  title: null,
  description: "",
  email: null,
  avatar: null,
  online: null,
  space_id: null,
  owner: null,
  runtime: null,
  allow_assign: null,
  allow_direct_chat: null,
  created_by_me: false,
});

/** 其余接口一律 500：页面停留在原地址，不触发「对象不存在」的回退。 */
function baseHandler(extra?: Extra): Handler {
  return (url) => {
    const hit = extra?.(url);
    if (hit) return hit;
    if (url.endsWith("/v1/me"))
      return json(200, { id: "u1", email: "lin@openfox.dev" });
    if (url.endsWith("/v1/me/organizations"))
      return json(200, { organizations: orgs });
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
    if (url.endsWith("/contacts"))
      return json(200, {
        truncated: false,
        groups: [
          {
            kind: "UNASSIGNED",
            space_id: null,
            space_name: null,
            items: [agent("a1"), agent("a2")],
          },
        ],
      });
    return json(500, {});
  };
}

let probe = "";
function Probe() {
  const location = useLocation();
  const navigate = useNavigate();
  probe = location.pathname + location.search;
  return <button onClick={() => navigate(-1)}>测试后退</button>;
}

function renderAt(path: string, handler: Handler) {
  const api = createApi(
    new HttpClient("/api", ((input: RequestInfo | URL) =>
      Promise.resolve(handler(String(input)))) as unknown as typeof fetch),
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

const rail = (name: string) => screen.findByRole("link", { name });

afterEach(() => {
  cleanup();
  sessionStorage.clear();
  localStorage.clear();
});

function skipOnboarding() {
  for (const id of ["o1", "o2"])
    localStorage.setItem(`openfox:onboarding:${id}`, JSON.stringify({}));
}

describe("导航记忆：纯函数", () => {
  it("按地址划分模块，组织外地址不属于任何模块", () => {
    expect(moduleOf("o1", "/o/o1/chat/c1")).toBe("chat");
    expect(moduleOf("o1", "/o/o1/members")).toBe("members");
    expect(moduleOf("o1", "/o/o1/settings/agents")).toBe("settings");
    expect(moduleOf("o1", "/o/o1/inbox")).toBe("home");
    expect(moduleOf("o1", "/o/o1/spaces/s1/tasks")).toBe("home");
    expect(moduleOf("o1", "/o/o1/agents")).toBe("home");
    expect(moduleOf("o1", "/o/o1/chatty")).toBe("home");
    expect(moduleOf("o1", "/o/o2/inbox")).toBeNull();
    expect(moduleOf("o1", "/login")).toBeNull();
    expect(moduleOf("o1", "/o/o10/inbox")).toBeNull();
  });

  it("非法或跨组织的存储内容被忽略", () => {
    for (const bad of [
      "//evil.example/o/o1/inbox",
      "https://evil.example/o/o1/inbox",
      "/o/o2/inbox",
      "/o/o1/inbox#x",
      "/o/o1/../o2/inbox",
      "/o/o1/\\evil",
      "/o/o1/settings/agents", // 模块不符
      "",
    ])
      expect(sanitizeNavTarget("o1", "home", bad)).toBeNull();
    sessionStorage.setItem("openfox:nav:o1:home", "https://evil.example");
    expect(readNavMemory("o1", "home")).toBeNull();
    sessionStorage.setItem("openfox:nav:o1:home", "/o/o2/inbox");
    expect(readNavMemory("o1", "home")).toBeNull();
    sessionStorage.setItem("openfox:nav:o1:home", "/o/o1/my-tasks?task=A-1");
    expect(readNavMemory("o1", "home")).toBe("/o/o1/my-tasks?task=A-1");
  });

  it("首页对话与对话页签分别记忆；rail 参数只影响对话路由", () => {
    recordNav("o1", "/o/o1/chat/c1");
    recordNav("o1", "/o/o1/chat/c2?rail=home");
    expect(readNavMemory("o1", "chat")).toBe("/o/o1/chat/c1");
    expect(readNavMemory("o1", "home")).toBe("/o/o1/chat/c2?rail=home");
    expect(
      sanitizeNavTarget("o1", "chat", "/o/o1/chat/c2?rail=home"),
    ).toBeNull();
    expect(moduleOf("o1", "/o/o1/settings/agents", "?rail=home")).toBe(
      "settings",
    );
    expect(moduleOf("o1", "/o/o1/chat/c1", "?rail=unknown")).toBe("chat");
  });

  it("记录按组织隔离，清空时全部删除；sessionStorage 不可用时当作没有记录", () => {
    recordNav("o1", "/o/o1/spaces/s1/tasks?task=A-1");
    recordNav("o2", "/o/o2/inbox");
    expect(readNavMemory("o1", "home")).toBe("/o/o1/spaces/s1/tasks?task=A-1");
    expect(readNavMemory("o2", "home")).toBe("/o/o2/inbox");
    recordNav("o1", "/o/o2/inbox"); // 跨组织写入被拒
    expect(readNavMemory("o1", "home")).toBe("/o/o1/spaces/s1/tasks?task=A-1");
    clearNavMemory();
    expect(readNavMemory("o1", "home")).toBeNull();
    expect(readNavMemory("o2", "home")).toBeNull();
    const spy = vi
      .spyOn(Storage.prototype, "getItem")
      .mockImplementation(() => {
        throw new Error("denied");
      });
    expect(readNavMemory("o1", "home")).toBeNull();
    spy.mockRestore();
  });
});

describe("导航记忆：主导航", () => {
  const homeSidebar = () =>
    screen.getByRole("navigation", { name: "页面导航" });
  const conversationHandler = baseHandler((url) => {
    const path = new URL(url, "http://localhost").pathname;
    if (path.endsWith("/conversations"))
      return json(200, {
        conversations: [summary("c1"), summary("c2")],
        next_cursor: null,
      });
    if (/\/conversations\/c[12]$/.test(path))
      return json(200, summary(path.endsWith("c1") ? "c1" : "c2"));
    if (path.endsWith("/artifacts")) return json(200, { artifacts: [] });
    if (path.endsWith("/messages"))
      return json(200, { messages: [], next_before: null });
    if (path.endsWith("/direct-chats"))
      return json(200, {
        chats: [
          {
            id: "d1",
            peer: { id: "u2", name: "同事" },
            last_seq: 0,
            read_seq: 0,
            unread_count: 0,
            last_body: "你好",
            updated_at: "2026-11-05T00:00:00Z",
          },
        ],
        next_before: "",
      });
    return undefined;
  });

  it.each([
    ["会话 c1", "/o/o1/chat/c1?rail=home"],
    ["同事", "/o/o1/chat/direct/d1?rail=home"],
  ])(
    "首页打开 %s 保留首页，显式切换对话后能返回；后退与重新挂载保留上下文",
    async (name, target) => {
      skipOnboarding();
      const user = userEvent.setup();
      renderAt("/o/o1/inbox", conversationHandler);
      await rail("首页");
      await user.click(
        await within(homeSidebar()).findByRole("link", {
          name: new RegExp(name),
        }),
      );
      await waitFor(() => expect(probe).toBe(target));
      expect(await rail("首页")).toHaveAttribute("aria-current", "page");
      expect(
        within(homeSidebar()).getByRole("link", { name: new RegExp(name) }),
      ).toHaveAttribute("aria-current", "page");
      expect(
        screen.queryByRole("navigation", { name: "对话" }),
      ).not.toBeInTheDocument();
      await user.click(await rail("对话"));
      expect(probe).toBe("/o/o1/chat");
      expect(
        await screen.findByRole("navigation", { name: "对话" }),
      ).toBeInTheDocument();
      await user.click(await rail("首页"));
      expect(probe).toBe(target);
      await user.click(screen.getByRole("button", { name: "测试后退" }));
      expect(probe).toBe("/o/o1/chat");
      expect(await rail("对话")).toHaveAttribute("aria-current", "page");
      await user.click(screen.getByRole("button", { name: "测试后退" }));
      expect(probe).toBe(target);
      cleanup();
      renderAt(target, conversationHandler);
      expect(await rail("首页")).toHaveAttribute("aria-current", "page");
      expect(homeSidebar()).toBeInTheDocument();
    },
  );

  it("首页会话的标题切换和新建会话延续首页上下文", async () => {
    skipOnboarding();
    const user = userEvent.setup();
    renderAt("/o/o1/chat/c1?rail=home", conversationHandler);
    await user.click(await screen.findByRole("button", { name: "切换会话" }));
    await user.click(
      await screen.findByRole("menuitemradio", { name: "会话 c2" }),
    );
    expect(probe).toBe("/o/o1/chat/c2?rail=home");
    expect(await rail("首页")).toHaveAttribute("aria-current", "page");
    await user.click(await screen.findByRole("button", { name: "新建会话" }));
    expect(probe).toBe("/o/o1/chat?agent=a1&rail=home");
    expect(homeSidebar()).toBeInTheDocument();
  });

  it.each(["c1", "direct/d1"])(
    "首页失效会话 %s 回退首页记忆，不覆盖对话页签",
    async (id) => {
      skipOnboarding();
      recordNav("o1", "/o/o1/chat/c2");
      renderAt(
        `/o/o1/chat/${id}?rail=home`,
        baseHandler((url) =>
          /\/(conversations\/c1|direct-chats\/d1)$/.test(url)
            ? json(404, { error: { code: "NOT_FOUND", message: "x" } })
            : undefined,
        ),
      );
      await waitFor(() =>
        expect(readNavMemory("o1", "home")).toBe("/o/o1/inbox"),
      );
      expect(readNavMemory("o1", "chat")).toBe("/o/o1/chat/c2");
    },
  );

  it("任务 A → 设置 / Agents → 首页回到任务 A，全部设置回到 Agents 子页", async () => {
    skipOnboarding();
    const user = userEvent.setup();
    renderAt("/o/o1/spaces/s1/tasks?task=OPE-2", baseHandler());
    await rail("全部设置");
    await user.click(await rail("全部设置"));
    await waitFor(() => expect(probe).toBe("/o/o1/settings/labels"));
    await user.click(await screen.findByRole("link", { name: /Agents/ }));
    await waitFor(() => expect(probe).toBe("/o/o1/settings/agents"));
    await user.click(await rail("首页"));
    expect(probe).toBe("/o/o1/spaces/s1/tasks?task=OPE-2");
    await user.click(await rail("全部设置"));
    expect(probe).toBe("/o/o1/settings/agents");
  });

  it("成员 B → 其他模块 → 成员，仍选中 B", async () => {
    skipOnboarding();
    const user = userEvent.setup();
    renderAt("/o/o1/members?contact=agent:a2", baseHandler());
    await rail("成员");
    await user.click(await rail("首页"));
    expect(probe).toBe("/o/o1/inbox");
    await user.click(await rail("成员"));
    expect(probe).toBe("/o/o1/members?contact=agent:a2");
  });

  it("记住的联系人已不存在时，兜底选中第一个 Agent", async () => {
    skipOnboarding();
    renderAt("/o/o1/members?contact=agent:gone", baseHandler());
    await waitFor(() => expect(probe).toBe("/o/o1/members?contact=agent%3Aa1"));
  });

  it("切换组织后记录不串", async () => {
    skipOnboarding();
    const user = userEvent.setup();
    recordNav("o2", "/o/o2/my-tasks");
    renderAt("/o/o1/spaces/s1/tasks?task=OPE-2", baseHandler());
    await rail("全部设置");
    expect(readNavMemory("o1", "home")).toBe(
      "/o/o1/spaces/s1/tasks?task=OPE-2",
    );
    await user.click(await screen.findByRole("button", { name: /组织o1/ }));
    await user.click(
      await screen.findByRole("menuitemradio", { name: /组织o2/ }),
    );
    await waitFor(() => expect(probe).toBe("/o/o2/inbox"));
    await user.click(await rail("全部设置"));
    await user.click(await rail("首页"));
    expect(probe).toBe("/o/o2/inbox");
    expect(readNavMemory("o2", "home")).toBe("/o/o2/inbox");
    expect(readNavMemory("o1", "home")).toBe(
      "/o/o1/spaces/s1/tasks?task=OPE-2",
    );
  });

  it("退出登录后记录清空", async () => {
    skipOnboarding();
    const user = userEvent.setup();
    renderAt(
      "/o/o1/inbox",
      baseHandler((u) =>
        u.endsWith("/auth/logout") ? json(204, null) : undefined,
      ),
    );
    await rail("全部设置");
    expect(readNavMemory("o1", "home")).toBe("/o/o1/inbox");
    await user.click(screen.getByRole("button", { name: "lin@openfox.dev" }));
    await user.click(await screen.findByRole("menuitem", { name: "退出登录" }));
    await waitFor(() => expect(probe).toBe("/login"));
    expect(readNavMemory("o1", "home")).toBeNull();
  });

  it("高亮按当前地址所属模块判断", async () => {
    skipOnboarding();
    const user = userEvent.setup();
    renderAt("/o/o1/inbox", baseHandler());
    expect(await rail("首页")).toHaveAttribute("aria-current", "page");
    await user.click(await rail("全部设置"));
    expect(await rail("全部设置")).toHaveAttribute("aria-current", "page");
    expect(await rail("首页")).not.toHaveAttribute("aria-current");
    // 「首页」链接此时指向记住的收件箱；去空间任务页后高亮仍在首页
    expect(await rail("首页")).toHaveAttribute("href", "/o/o1/inbox");
    await user.click(await rail("首页"));
    expect(await rail("首页")).toHaveAttribute("aria-current", "page");
  });

  it("空间任务页高亮首页；在模块内点本模块按钮不动", async () => {
    skipOnboarding();
    const user = userEvent.setup();
    renderAt("/o/o1/spaces/s1/tasks?task=OPE-2", baseHandler());
    const home = await rail("首页");
    expect(home).toHaveAttribute("aria-current", "page");
    expect(home).toHaveAttribute("href", "/o/o1/spaces/s1/tasks?task=OPE-2");
    await user.click(home);
    expect(probe).toBe("/o/o1/spaces/s1/tasks?task=OPE-2");
  });

  it("任务已不存在（404）时记录改回去掉 ?task= 的地址", async () => {
    skipOnboarding();
    const user = userEvent.setup();
    renderAt(
      "/o/o1/spaces/s1/tasks?task=GONE-1",
      baseHandler((u) =>
        u.includes("/tasks/by-display-id") || u.includes("GONE-1")
          ? json(404, {
              error: { code: "NOT_FOUND", message: "x", request_id: "r" },
            })
          : undefined,
      ),
    );
    await rail("全部设置");
    await waitFor(() =>
      expect(readNavMemory("o1", "home")).toBe("/o/o1/spaces/s1/tasks"),
    );
    await user.click(await rail("全部设置"));
    await user.click(await rail("首页"));
    expect(probe).toBe("/o/o1/spaces/s1/tasks");
  });

  it("未接入 / 不存在的设置子页不记入记忆", async () => {
    skipOnboarding();
    renderAt("/o/o1/settings/no-such-page", baseHandler());
    await rail("全部设置");
    await waitFor(() => expect(readNavMemory("o1", "settings")).toBeNull());
  });
});
