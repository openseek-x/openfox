import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { ApiError, createApi, HttpClient } from "@openfox/api";
import { createTranslator } from "@openfox/i18n";

import { App, createQueryClient } from "./App";
import { formatWait, safeNext } from "./auth/LoginPage";
import { defaultTaskTypes } from "./onboarding/state";
import { suggestSlug } from "./onboarding/WelcomePage";

type Handler = (url: string, init: RequestInit) => Response | Promise<Response>;

/** 带 Retry-After 的 429 响应。 */
function limited(code: string, retryAfter: string): Response {
  return new Response(
    JSON.stringify({ error: { code, message: "x", request_id: "r" } }),
    {
      status: 429,
      headers: {
        "Content-Type": "application/json",
        "Retry-After": retryAfter,
      },
    },
  );
}

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function apiWith(handler: Handler) {
  const transport = vi.fn((input: RequestInfo | URL, init?: RequestInit) =>
    Promise.resolve(handler(String(input), init ?? {})),
  );
  return {
    api: createApi(
      new HttpClient("/api", transport as unknown as typeof fetch),
    ),
    transport,
  };
}

function renderAt(
  path: string,
  handler: Handler,
  locale: "zh-CN" | "en-US" = "zh-CN",
) {
  const { api, transport } = apiWith(handler);
  const queryClient = createQueryClient();
  queryClient.setDefaultOptions({ queries: { retry: false } });
  render(
    <MemoryRouter initialEntries={[path]}>
      <App api={api} queryClient={queryClient} locale={locale} />
    </MemoryRouter>,
  );
  return transport;
}

const unauthenticated = json(401, {
  error: {
    code: "UNAUTHENTICATED",
    message: "authentication is required",
    request_id: "r",
  },
});

afterEach(() => {
  localStorage.clear();
});

describe("纯函数", () => {
  it("只接受站内跳转地址", () => {
    expect(safeNext("/o/1/inbox")).toBe("/o/1/inbox");
    expect(safeNext("//evil.example")).toBeNull();
    expect(safeNext("/\\evil.example")).toBeNull();
    expect(safeNext("https://evil.example")).toBeNull();
    expect(safeNext(null)).toBeNull();
  });

  it("按邮箱前缀生成组织 URL", () => {
    expect(suggestSlug("Lin.Wang@example.com")).toBe("lin-wang-team");
    expect(suggestSlug("ab@example.com")).toBe("my-team");
  });

  it("按工作类型预选任务类型", () => {
    expect(defaultTaskTypes("software")).toEqual([
      "requirement",
      "bug",
      "task",
      "story",
    ]);
    expect(defaultTaskTypes("marketing")).toEqual(["requirement", "task"]);
  });
});

describe("登录", () => {
  it("未登录访问根路径会跳到登录页", async () => {
    renderAt("/", (url) =>
      url.endsWith("/v1/me") ? unauthenticated : json(404, {}),
    );
    expect(
      await screen.findByRole("heading", { name: "👋 欢迎使用 OpenFox" }),
    ).toBeInTheDocument();
  });

  it("邮箱格式错误时不发请求", async () => {
    const user = userEvent.setup();
    const transport = renderAt("/login", () => json(404, {}));
    await user.type(screen.getByLabelText("邮箱"), "lin@openfox");
    await user.click(screen.getByRole("button", { name: "继续" }));
    expect(await screen.findByText("请输入有效邮箱")).toBeInTheDocument();
    expect(transport).not.toHaveBeenCalledWith(
      expect.stringContaining("/auth/email/request"),
      expect.anything(),
    );
  });

  it("当天验证码已达上限（429）时在表单里常驻提示，改邮箱后清除", async () => {
    const user = userEvent.setup();
    renderAt("/login", (url) =>
      url.endsWith("/v1/auth/email/request")
        ? json(429, {
            error: { code: "EMAIL_DAILY_LIMIT", message: "x", request_id: "r" },
          })
        : url.endsWith("/v1/me")
          ? unauthenticated
          : json(404, {}),
    );
    await user.type(screen.getByLabelText("邮箱"), "lin@openfox.dev");
    await user.click(screen.getByRole("button", { name: "继续" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "今天发送验证码的次数已达上限，请稍后再试",
    );
    // 仍停留在邮箱步骤
    expect(screen.getByLabelText("邮箱")).toBeInTheDocument();
    await user.type(screen.getByLabelText("邮箱"), "x");
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("按 Retry-After 格式化剩余时间", () => {
    const t = createTranslator("zh-CN");
    expect(formatWait(t, 1)).toBe("1 秒");
    expect(formatWait(t, 45_000)).toBe("45 秒");
    expect(formatWait(t, 120_000)).toBe("2 分钟");
    expect(formatWait(t, 125_000)).toBe("2 分 5 秒");
    expect(formatWait(t, 3_600_000)).toBe("1 小时");
    expect(formatWait(t, 3_700_000)).toBe("1 小时 2 分");
    expect(formatWait(t, 5 * 3_600_000)).toBe("5 小时");
  });

  it("每日上限带 Retry-After 时显示剩余时间", async () => {
    const user = userEvent.setup();
    renderAt("/login", (url) =>
      url.endsWith("/v1/auth/email/request")
        ? limited("EMAIL_DAILY_LIMIT", "3700")
        : url.endsWith("/v1/me")
          ? unauthenticated
          : json(404, {}),
    );
    await user.type(screen.getByLabelText("邮箱"), "lin@openfox.dev");
    await user.click(screen.getByRole("button", { name: "继续" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "今天发送验证码的次数已达上限，请 1 小时 2 分后再试",
    );
  });

  it("按 IP 限流的倒计时到了自动清除提示", async () => {
    const user = userEvent.setup();
    renderAt("/login", (url) =>
      url.endsWith("/v1/auth/email/request")
        ? limited("RATE_LIMITED", "2")
        : url.endsWith("/v1/me")
          ? unauthenticated
          : json(404, {}),
    );
    await user.type(screen.getByLabelText("邮箱"), "lin@openfox.dev");
    await user.click(screen.getByRole("button", { name: "继续" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      /请求太频繁，请 [12] 秒后再试/,
    );
    await waitFor(
      () => expect(screen.queryByRole("alert")).not.toBeInTheDocument(),
      { timeout: 4000 },
    );
  });

  it("验证码尝试次数过多时在验证码下方显示剩余时间", async () => {
    const user = userEvent.setup();
    renderAt("/login", (url) => {
      if (url.endsWith("/v1/auth/email/request"))
        return json(202, { expires_at: "2026-10-28T10:10:00Z" });
      if (url.endsWith("/v1/auth/email/verify"))
        return limited("EMAIL_DAILY_LIMIT", "600");
      if (url.endsWith("/v1/me")) return unauthenticated;
      return json(404, {});
    });
    await user.type(screen.getByLabelText("邮箱"), "lin@openfox.dev");
    await user.click(screen.getByRole("button", { name: "继续" }));
    await screen.findByText("已发送验证码至 lin@openfox.dev");
    await user.type(screen.getByLabelText("第 1 位"), "999999");
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "尝试次数过多，请 10 分钟后再试",
    );
  });

  it("按 IP 限流（429 RATE_LIMITED）时提示请求太频繁，而不是网络错误", async () => {
    const user = userEvent.setup();
    renderAt("/login", (url) =>
      url.endsWith("/v1/auth/email/request")
        ? json(429, {
            error: { code: "RATE_LIMITED", message: "x", request_id: "r" },
          })
        : url.endsWith("/v1/me")
          ? unauthenticated
          : json(404, {}),
    );
    await user.type(screen.getByLabelText("邮箱"), "lin@openfox.dev");
    await user.click(screen.getByRole("button", { name: "继续" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "请求太频繁，请稍后再试",
    );
  });

  it("在验证码步骤重新发送达到上限时，提示显示在验证码下方", async () => {
    const user = userEvent.setup();
    let requests = 0;
    renderAt("/login", (url) => {
      if (url.endsWith("/v1/auth/email/request")) {
        requests += 1;
        // 第一次：冷却中（Retry-After 1 秒），直接进入验证码步骤，1 秒后可以重新发送；第二次：达到每日上限
        return requests === 1
          ? new Response(
              JSON.stringify({
                error: {
                  code: "EMAIL_CODE_RATE_LIMITED",
                  message: "x",
                  request_id: "r",
                },
              }),
              {
                status: 429,
                headers: {
                  "Content-Type": "application/json",
                  "Retry-After": "1",
                },
              },
            )
          : json(429, {
              error: {
                code: "EMAIL_DAILY_LIMIT",
                message: "x",
                request_id: "r",
              },
            });
      }
      if (url.endsWith("/v1/me")) return unauthenticated;
      return json(404, {});
    });
    await user.type(screen.getByLabelText("邮箱"), "lin@openfox.dev");
    await user.click(screen.getByRole("button", { name: "继续" }));
    await user.click(
      await screen.findByRole(
        "button",
        { name: "重新发送验证码" },
        { timeout: 3000 },
      ),
    );
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "今天发送验证码的次数已达上限，请稍后再试",
    );
    expect(
      screen.getByText("已发送验证码至 lin@openfox.dev"),
    ).toBeInTheDocument();
  });

  it("发送验证码、输错提示、输对后进入首登", async () => {
    const user = userEvent.setup();
    let verified = false;
    renderAt("/login", (url, init) => {
      if (url.endsWith("/v1/auth/email/request"))
        return json(202, { expires_at: "2026-10-28T10:10:00Z" });
      if (url.endsWith("/v1/auth/email/verify")) {
        const body = JSON.parse(String(init.body)) as { code: string };
        if (body.code !== "123456")
          return json(401, {
            error: {
              code: "INVALID_CREDENTIALS",
              message: "x",
              request_id: "r",
            },
          });
        verified = true;
        return json(201, {
          user_id: "u1",
          expires_at: "2026-10-29T00:00:00Z",
          new_user: true,
        });
      }
      if (url.endsWith("/v1/me/organizations"))
        return json(200, { organizations: [] });
      if (url.endsWith("/v1/me"))
        return verified
          ? json(200, { id: "u1", email: "lin@openfox.dev" })
          : unauthenticated;
      return json(404, {});
    });
    await user.type(screen.getByLabelText("邮箱"), "lin@openfox.dev");
    await user.click(screen.getByRole("button", { name: "继续" }));
    expect(
      await screen.findByText("已发送验证码至 lin@openfox.dev"),
    ).toBeInTheDocument();
    expect(screen.getByText(/s 后可重新发送/)).toBeInTheDocument();

    await user.type(screen.getByLabelText("第 1 位"), "999999");
    expect(await screen.findByText("验证码无效或已过期")).toBeInTheDocument();

    await user.click(screen.getByLabelText("第 1 位"));
    for (let i = 0; i < 6; i++) await user.keyboard("{Backspace}");
    await user.type(screen.getByLabelText("第 1 位"), "123456");
    expect(
      await screen.findByRole("heading", { name: "创建组织" }),
    ).toBeInTheDocument();
  });

  it("切换为英文后界面文案随之变化并记住选择", async () => {
    const user = userEvent.setup();
    renderAt("/login", () => json(404, {}));
    await user.click(screen.getByRole("button", { name: "语言" }));
    await user.click(screen.getByRole("option", { name: "English" }));
    expect(
      await screen.findByRole("heading", { name: "👋 Welcome to OpenFox" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Continue" }),
    ).toBeInTheDocument();
    expect(localStorage.getItem("openfox:locale")).toBe("en-US");
    expect(document.documentElement.lang).toBe("en-US");
  });
});

describe("创建组织", () => {
  it("URL 被占用时在字段下提示，并带幂等键提交", async () => {
    const user = userEvent.setup();
    const keys: string[] = [];
    renderAt("/welcome", (url, init) => {
      if (url.endsWith("/v1/me"))
        return json(200, { id: "u1", email: "lin@openfox.dev" });
      if (url.endsWith("/v1/me/organizations"))
        return json(200, { organizations: [] });
      if (url.endsWith("/v1/organizations") && init.method === "POST") {
        keys.push(new Headers(init.headers).get("Idempotency-Key") ?? "");
        return json(409, {
          error: { code: "SLUG_TAKEN", message: "x", request_id: "r" },
        });
      }
      return json(404, {});
    });
    await user.type(await screen.findByLabelText("组织名称"), "林的团队");
    await user.click(screen.getByRole("button", { name: "创建" }));
    expect(
      await screen.findByText("这个 URL 已被使用，请换一个"),
    ).toBeInTheDocument();
    expect(keys).toHaveLength(1);
    expect(keys[0]).toMatch(/^[a-f0-9]{32}$/);
  });
});

describe("接口客户端", () => {
  it("写请求带 CSRF 头，错误转成 ApiError 并带上 Retry-After", async () => {
    document.cookie = "openfox_csrf=tok123";
    const { api, transport } = apiWith(
      () =>
        new Response(
          JSON.stringify({
            error: {
              code: "EMAIL_CODE_RATE_LIMITED",
              message: "wait",
              request_id: "r1",
            },
          }),
          {
            status: 429,
            headers: { "Retry-After": "42" },
          },
        ),
    );
    const error = await api.auth
      .requestEmailCode("a@b.co")
      .catch((e: unknown) => e);
    expect(error).toBeInstanceOf(ApiError);
    expect((error as ApiError).code).toBe("EMAIL_CODE_RATE_LIMITED");
    expect((error as ApiError).retryAfter).toBe(42);
    const init = transport.mock.calls[0]![1]!;
    expect(new Headers(init.headers).get("X-CSRF-Token")).toBe("tok123");
    document.cookie = "openfox_csrf=; max-age=0";
  });

  it("拒绝非 /v1/ 路径", () => {
    expect(() => new HttpClient().url("/admin")).toThrow();
  });
});

describe("应用外壳", () => {
  it("显示组织、空间与收件箱未读数；首登向导弹出", async () => {
    localStorage.setItem(
      "openfox:onboarding:o1",
      JSON.stringify({ step: "work-type" }),
    );
    renderAt("/o/o1", (url) => {
      if (url.endsWith("/v1/me"))
        return json(200, { id: "u1", email: "lin@openfox.dev" });
      if (url.endsWith("/v1/me/organizations"))
        return json(200, {
          organizations: [
            {
              id: "o1",
              slug: "lin",
              name: "林的团队",
              role: "OWNER",
              created_at: "2026-10-28T00:00:00Z",
            },
          ],
        });
      if (url.includes("/inbox/stats"))
        return json(200, {
          counts: {
            inbox: 3,
            inbox_unread: 3,
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
              // 侧栏只列已加入的空间
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
      return json(404, {});
    });
    expect(await screen.findByText("林的团队")).toBeInTheDocument();
    expect(await screen.findByText("默认空间")).toBeInTheDocument();
    await waitFor(() =>
      expect(screen.getByRole("link", { name: /收件箱/ })).toHaveTextContent(
        "3",
      ),
    );
    expect(
      screen.getByRole("heading", { name: "该空间主要做什么类型的工作？" }),
    ).toBeInTheDocument();
    // 收件箱已接入真实数据：页签显示未读组数
    await waitFor(() =>
      expect(screen.getByRole("main")).toHaveTextContent("3 条未读"),
    );
  });

  const orgHandler: Handler = (url) => {
    if (url.endsWith("/v1/me"))
      return json(200, { id: "u1", email: "lin@openfox.dev" });
    if (url.endsWith("/v1/me/organizations"))
      return json(200, {
        organizations: [
          {
            id: "o1",
            slug: "lin",
            name: "林的团队",
            role: "OWNER",
            created_at: "2026-10-28T00:00:00Z",
          },
        ],
      });
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
    if (url.endsWith("/spaces")) return json(200, { spaces: [] });
    if (url.includes("/conversations"))
      return json(200, { conversations: [], next_cursor: null });
    return json(404, {});
  };

  it("设置页：全局配置子路由显示对应页面，导入与导出显示导入页", async () => {
    localStorage.setItem("openfox:onboarding:o1", JSON.stringify({}));
    renderAt("/o/o1/settings/relation-types", orgHandler);
    const nav = await screen.findByRole("navigation", { name: "设置导航" });
    expect(nav).toHaveTextContent("任务标签");
    expect(nav).toHaveTextContent("自定义字段");
    expect(
      await screen.findByRole("heading", { name: "任务关联类型" }),
    ).toBeInTheDocument();
    await userEvent.click(screen.getByRole("link", { name: "导入与导出" }));
    expect(
      await screen.findByRole("heading", { name: "导入与导出" }),
    ).toBeInTheDocument();
  });

  it("侧栏空间可以折叠，折叠状态按组织存在本机", async () => {
    localStorage.setItem("openfox:onboarding:o1", JSON.stringify({}));
    const withSpace: Handler = (url, init) =>
      url.endsWith("/spaces")
        ? json(200, {
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
          })
        : orgHandler(url, init);
    renderAt("/o/o1", withSpace);
    const toggle = await screen.findByRole("button", { name: "默认空间" });
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByRole("link", { name: "任务" })).toHaveAttribute(
      "href",
      "/o/o1/spaces/s1/tasks",
    );
    await userEvent.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByRole("link", { name: "任务" })).toBeNull();
    expect(localStorage.getItem("openfox:spaces-collapsed:o1")).toBe(
      JSON.stringify(["s1"]),
    );
    await userEvent.click(toggle);
    expect(screen.getByRole("link", { name: "任务" })).toBeInTheDocument();
    expect(localStorage.getItem("openfox:spaces-collapsed:o1")).toBe("[]");
  });

  it("桌面端标题栏：macOS 给红黄绿按钮留位，显示后退 / 前进 / 刷新；网页端不显示", async () => {
    localStorage.setItem("openfox:onboarding:o1", JSON.stringify({}));
    renderAt("/o/o1/inbox", orgHandler);
    expect(await screen.findByText("林的团队")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "后退" })).toBeNull();
    cleanup();

    (globalThis as { openfoxDesktop?: unknown }).openfoxDesktop = {
      platform: "darwin",
      getLocalStatus: vi.fn(() => new Promise(() => undefined)),
      daemonAction: vi.fn(),
      pickDirectory: vi.fn(),
    };
    try {
      renderAt("/o/o1/inbox", orgHandler);
      const org = await screen.findByText("林的团队");
      const header = org.closest("header")!;
      expect(header.className).toContain("pl-[92px]");
      expect(header.className).toContain("[-webkit-app-region:drag]");
      for (const name of ["后退", "前进", "刷新"])
        expect(
          within(header).getByRole("button", { name }),
        ).toBeInTheDocument();
    } finally {
      delete (globalThis as { openfoxDesktop?: unknown }).openfoxDesktop;
    }
  });

  it("设置页入口默认进入任务标签", async () => {
    localStorage.setItem("openfox:onboarding:o1", JSON.stringify({}));
    renderAt("/o/o1/settings", orgHandler);
    expect(
      await screen.findByRole("heading", { name: "任务标签" }),
    ).toBeInTheDocument();
  });
});
