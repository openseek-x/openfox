import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type {
  InboxGroup,
  NotificationChannel,
  NotificationChannelName,
} from "@openfox/api";
import type { Translate } from "@openfox/i18n";

import { App, createQueryClient } from "../App";
import { keys } from "../context";
import { backend, json, ORG, type Override } from "../tasks/test-backend";
import {
  alertChannel,
  eventForKind,
  pickNewNotifications,
  shouldAlert,
} from "./NotificationWatcher";
import { stepPath } from "./GetStartedPage";
import { featureText } from "./BetaFeaturesPage";

// 个人访问令牌、通知、开始使用与内测功能。

const apiErr = (status: number, code: string) =>
  json(status, { error: { code, message: code, request_id: "r" } });

const TOKEN_ROW = {
  id: "t1",
  name: "旧的 CLI",
  prefix: "ofx_pat_aa11",
  created_at: "2026-10-01T08:00:00Z",
  expires_at: null,
  last_used_at: null,
};

const channel = (
  name: NotificationChannelName,
  patch: Partial<NotificationChannel> = {},
): NotificationChannel => ({
  channel: name,
  available: true,
  enabled: true,
  disabled_events: [],
  customized: false,
  ...patch,
});

const group = (patch: Partial<InboxGroup>): InboxGroup =>
  ({
    id: "g1",
    notification_id: "n1",
    kind: "TASK_ASSIGNED",
    task_type_icon: null,
    safe_summary: "林小北 指派了一个任务",
    count: 1,
    unread: true,
    latest_at: new Date(Date.now() + 60_000).toISOString(),
    ...patch,
  }) as InboxGroup;

/** 默认账号数据：可指定访问级别。其余请求交给 route，未命中时走测试后端的默认数据。 */
function defaults(access: "standard" | "beta", call: { path: string }) {
  if (call.path === "/v1/me")
    return json(200, {
      id: "u1",
      email: "lin@openfox.dev",
      full_name: "林小北",
      bio: "",
      avatar_url: null,
      access_tier: access,
    });
  if (call.path === "/v1/me/preferences")
    return json(200, {
      theme: "light",
      locale: null,
      outline_color: 0,
      open_session_on_assign: true,
      open_session_on_quick_command: true,
      send_shortcut: "enter",
      command_shortcuts: {},
      version: 1,
    });
  return undefined;
}

function renderAt(
  path: string,
  route: Override = () => undefined,
  access: "standard" | "beta" = "standard",
) {
  const server = backend((call) => route(call) ?? defaults(access, call));
  const queryClient = createQueryClient();
  queryClient.setDefaultOptions({ queries: { retry: false } });
  render(
    <MemoryRouter initialEntries={[path]}>
      <App api={server.api} queryClient={queryClient} locale="zh-CN" />
    </MemoryRouter>,
  );
  return { server, queryClient };
}

const tokens = `/o/${ORG}/settings/tokens`;
const notifications = `/o/${ORG}/settings/notifications`;
const getStarted = `/o/${ORG}/settings/get-started`;
const betaFeatures = `/o/${ORG}/settings/beta-features`;
const channelsPath = "/v1/me/notification-channels";

beforeEach(() => {
  localStorage.setItem(`openfox:onboarding:${ORG}`, JSON.stringify({}));
  // jsdom 没有 matchMedia：系统主题按浅色处理
  window.matchMedia = vi.fn((query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addListener: vi.fn(),
    removeListener: vi.fn(),
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    dispatchEvent: vi.fn(),
  })) as unknown as typeof window.matchMedia;
});

afterEach(() => {
  localStorage.clear();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("个人访问令牌", () => {
  it("没有令牌时显示空态", async () => {
    renderAt(tokens, (call) =>
      call.path === "/v1/me/tokens" && call.method === "GET"
        ? json(200, { tokens: [] })
        : undefined,
    );
    expect(await screen.findByText("暂无令牌")).toBeInTheDocument();
  });

  it("创建令牌：请求体、明文只显示一次，复制并关闭后明文消失并刷新列表", async () => {
    const user = userEvent.setup();
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      value: { writeText },
      configurable: true,
    });
    const list = [TOKEN_ROW];
    const { server } = renderAt(tokens, (call) => {
      if (call.path === "/v1/me/tokens" && call.method === "GET")
        return json(200, { tokens: list });
      if (call.path === "/v1/me/tokens" && call.method === "POST") {
        const token = {
          ...TOKEN_ROW,
          id: "t2",
          name: "CLI",
          prefix: "ofx_pat_7f3a",
        };
        list.unshift(token);
        return json(201, { token, secret: "ofx_pat_7f3a9c" });
      }
      return undefined;
    });
    await user.click(await screen.findByRole("button", { name: "创建令牌" }));
    await user.type(screen.getByRole("textbox", { name: "名称" }), "CLI");
    await user.click(screen.getByRole("button", { name: "保存" }));

    await waitFor(() =>
      expect(server.find("POST", "/v1/me/tokens")[0]?.body).toEqual({
        name: "CLI",
        expires_in_days: null,
      }),
    );
    expect(await screen.findByText("ofx_pat_7f3a9c")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "复制并关闭" }));
    await waitFor(() =>
      expect(writeText).toHaveBeenCalledWith("ofx_pat_7f3a9c"),
    );
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
    expect(screen.queryByText("ofx_pat_7f3a9c")).not.toBeInTheDocument();
    expect(await screen.findByText("CLI")).toBeInTheDocument();
    expect(server.find("GET", "/v1/me/tokens").length).toBeGreaterThan(1);
  });

  it("令牌数量达到上限时提示，并保留已填内容", async () => {
    const user = userEvent.setup();
    renderAt(tokens, (call) => {
      if (call.path === "/v1/me/tokens" && call.method === "GET")
        return json(200, { tokens: [TOKEN_ROW] });
      if (call.path === "/v1/me/tokens" && call.method === "POST")
        return apiErr(409, "TOKEN_LIMIT");
      return undefined;
    });
    await user.click(await screen.findByRole("button", { name: "创建令牌" }));
    await user.type(screen.getByRole("textbox", { name: "名称" }), "CI");
    await user.click(screen.getByRole("button", { name: "保存" }));
    expect(await screen.findByText("令牌数量已达上限")).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "名称" })).toHaveValue("CI");
  });

  it("吊销前二次确认，成功后提示", async () => {
    const user = userEvent.setup();
    const { server } = renderAt(tokens, (call) => {
      if (call.path === "/v1/me/tokens" && call.method === "GET")
        return json(200, { tokens: [TOKEN_ROW] });
      if (call.path === "/v1/me/tokens/t1" && call.method === "DELETE")
        return new Response(null, { status: 204 });
      return undefined;
    });
    await user.click(await screen.findByRole("button", { name: "令牌操作" }));
    await user.click(screen.getByRole("menuitem", { name: "删除" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText("吊销令牌")).toBeInTheDocument();
    expect(
      within(dialog).getByText(
        "该令牌将被永久吊销且不再可用。此操作不可撤销。",
      ),
    ).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "吊销" }));
    await waitFor(() =>
      expect(server.find("DELETE", "/v1/me/tokens/t1")).toHaveLength(1),
    );
    expect(await screen.findByText("已吊销令牌")).toBeInTheDocument();
  });
});

describe("通知设置", () => {
  const channels =
    (desktop: NotificationChannel, browser: NotificationChannel) =>
    (call: { path: string; method: string }) =>
      call.path === channelsPath && call.method === "GET"
        ? json(200, {
            channels: [
              desktop,
              browser,
              channel("email", {
                enabled: false,
                direct_messages: false,
              }),
            ],
          })
        : undefined;

  it("列表显示渠道状态文案，邮件默认关闭，移动端即将推出", async () => {
    renderAt(
      notifications,
      channels(
        channel("desktop"),
        channel("browser", {
          disabled_events: ["status", "automation"],
          customized: true,
        }),
      ),
    );
    expect(await screen.findByText("已对全部通知开启")).toBeInTheDocument();
    expect(
      screen.getByText("已开启：指派、评论与回复 等 3 项"),
    ).toBeInTheDocument();
    expect(screen.getAllByText("即将推出")).toHaveLength(1);
    expect(screen.getByRole("button", { name: /邮件.*已关闭/ })).toBeVisible();
    expect(screen.getByText("未连接 Slack 工作区")).toBeInTheDocument();
  });

  it("邮件未配置 SMTP 时不可开启", async () => {
    const user = userEvent.setup();
    const { server } = renderAt(notifications, (call) =>
      call.path === channelsPath && call.method === "GET"
        ? json(200, {
            channels: [
              channel("desktop"),
              channel("browser"),
              channel("email", {
                enabled: false,
                available: false,
                direct_messages: false,
              }),
            ],
          })
        : undefined,
    );
    await user.click(
      await screen.findByRole("button", {
        name: /邮件.*管理员尚未配置邮件发送/,
      }),
    );
    expect(screen.getByText(/服务器尚未配置 SMTP/)).toBeVisible();
    expect(screen.getByRole("switch", { name: "发送邮件通知" })).toBeDisabled();
    expect(server.find("PUT", `${channelsPath}/email`)).toHaveLength(0);
  });

  it("已开启邮件后 SMTP 不可用仍允许关闭", async () => {
    const user = userEvent.setup();
    const { server } = renderAt(notifications, (call) => {
      if (call.path === channelsPath && call.method === "GET")
        return json(200, {
          channels: [
            channel("desktop"),
            channel("browser"),
            channel("email", {
              enabled: true,
              available: false,
              direct_messages: false,
            }),
          ],
        });
      if (call.path === `${channelsPath}/email` && call.method === "PUT")
        return json(
          200,
          channel("email", {
            enabled: false,
            available: false,
            direct_messages: false,
          }),
        );
      return undefined;
    });
    await user.click(
      await screen.findByRole("button", {
        name: /邮件.*管理员尚未配置邮件发送/,
      }),
    );
    const toggle = screen.getByRole("switch", { name: "发送邮件通知" });
    expect(toggle).toBeEnabled();
    await user.click(toggle);
    await waitFor(() =>
      expect(server.find("PUT", `${channelsPath}/email`)[0]?.body).toEqual({
        enabled: false,
        disabled_events: [],
        direct_messages: false,
      }),
    );
  });

  it("邮件需显式开启，真人私聊可单独开启", async () => {
    const user = userEvent.setup();
    let email = channel("email", {
      enabled: false,
      direct_messages: false,
    });
    const { server } = renderAt(notifications, (call) => {
      if (call.path === channelsPath && call.method === "GET")
        return json(200, {
          channels: [channel("desktop"), channel("browser"), email],
        });
      if (call.path === `${channelsPath}/email` && call.method === "PUT") {
        email = { ...email, ...(call.body as object), customized: true };
        return json(200, email);
      }
      return undefined;
    });
    await user.click(
      await screen.findByRole("button", { name: /邮件.*已关闭/ }),
    );
    expect(
      screen.getByRole("switch", { name: "发送邮件通知" }),
    ).not.toBeChecked();
    await user.click(screen.getByRole("switch", { name: "发送邮件通知" }));
    await waitFor(() =>
      expect(server.find("PUT", `${channelsPath}/email`)[0]?.body).toEqual({
        enabled: true,
        disabled_events: [],
        direct_messages: false,
      }),
    );
    await user.click(screen.getByRole("switch", { name: "真人对话" }));
    await waitFor(() =>
      expect(server.find("PUT", `${channelsPath}/email`)[1]?.body).toEqual({
        enabled: true,
        disabled_events: [],
        direct_messages: true,
      }),
    );
  });

  it("企业微信选定个人机器人后仍需显式开启通知", async () => {
    const user = userEvent.setup();
    let wecom = channel("wecom", {
      enabled: false,
      direct_messages: false,
    });
    const { server } = renderAt(notifications, (call) => {
      if (call.path === `/v1/organizations/${ORG}/integrations/wecom/personal`)
        return json(200, {
          bindings: [
            {
              bot_id: "b1",
              bot_name: "需求助手",
              connection_id: "p1",
              external_id: "user-1",
              notification_target: true,
            },
          ],
        });
      if (call.path === channelsPath && call.method === "GET")
        return json(200, {
          channels: [channel("desktop"), channel("browser"), wecom],
        });
      if (call.path === `${channelsPath}/wecom` && call.method === "PUT") {
        wecom = { ...wecom, ...(call.body as object), customized: true };
        return json(200, wecom);
      }
      return undefined;
    });
    await user.click(
      await screen.findByRole("button", { name: /企业微信.*已关闭/ }),
    );
    expect(screen.getByText(/需求助手/)).toBeVisible();
    await user.click(screen.getByRole("switch", { name: "发送企业微信通知" }));
    await waitFor(() =>
      expect(server.find("PUT", `${channelsPath}/wecom`)[0]?.body).toEqual({
        enabled: true,
        disabled_events: [],
        direct_messages: false,
      }),
    );
  });

  it("进入桌面端详情，关闭总开关后事件类型禁用", async () => {
    const user = userEvent.setup();
    const { server } = renderAt(notifications, (call) => {
      if (call.path === channelsPath && call.method === "GET")
        return json(200, {
          channels: [channel("desktop"), channel("browser")],
        });
      if (call.path === `${channelsPath}/desktop` && call.method === "PUT")
        return json(200, {
          ...channel("desktop", { enabled: false }),
          customized: true,
        });
      return undefined;
    });
    await user.click(await screen.findByText("桌面端"));
    const master = await screen.findByRole("switch", {
      name: "显示桌面通知",
    });
    await user.click(master);
    await waitFor(() =>
      expect(server.find("PUT", `${channelsPath}/desktop`)[0]?.body).toEqual({
        enabled: false,
        disabled_events: [],
      }),
    );
    await waitFor(() =>
      expect(screen.getByRole("switch", { name: "指派" })).toBeDisabled(),
    );
  });

  it("事件开关请求体为整体替换的 disabled_events", async () => {
    const user = userEvent.setup();
    const { server } = renderAt(notifications, (call) => {
      if (call.path === channelsPath && call.method === "GET")
        return json(200, {
          channels: [channel("desktop"), channel("browser")],
        });
      if (call.path === `${channelsPath}/desktop` && call.method === "PUT")
        return json(200, {
          ...channel("desktop", { disabled_events: ["assign"] }),
          customized: true,
        });
      return undefined;
    });
    await user.click(await screen.findByText("桌面端"));
    await user.click(await screen.findByRole("switch", { name: "指派" }));
    await waitFor(() =>
      expect(server.find("PUT", `${channelsPath}/desktop`)[0]?.body).toEqual({
        enabled: true,
        disabled_events: ["assign"],
      }),
    );
  });

  it("保存失败时回滚开关并提示", async () => {
    const user = userEvent.setup();
    renderAt(notifications, (call) => {
      if (call.path === channelsPath && call.method === "GET")
        return json(200, {
          channels: [channel("desktop"), channel("browser")],
        });
      if (call.path === `${channelsPath}/desktop` && call.method === "PUT")
        return apiErr(500, "INTERNAL");
      return undefined;
    });
    await user.click(await screen.findByText("桌面端"));
    const assign = await screen.findByRole("switch", { name: "指派" });
    expect(assign).toBeChecked();
    await user.click(assign);
    expect(await screen.findByText("更新通知设置失败")).toBeInTheDocument();
    await waitFor(() =>
      expect(screen.getByRole("switch", { name: "指派" })).toBeChecked(),
    );
  });

  it("恢复默认设置调用 DELETE 并提示", async () => {
    const user = userEvent.setup();
    const { server } = renderAt(notifications, (call) => {
      if (call.path === channelsPath && call.method === "GET")
        return json(200, {
          channels: [channel("desktop"), channel("browser")],
        });
      if (call.path === `${channelsPath}/desktop` && call.method === "DELETE")
        return json(200, channel("desktop"));
      return undefined;
    });
    await user.click(await screen.findByText("桌面端"));
    await user.click(
      await screen.findByRole("button", { name: "恢复默认设置" }),
    );
    await waitFor(() =>
      expect(server.find("DELETE", `${channelsPath}/desktop`)).toHaveLength(1),
    );
    expect(await screen.findByText("已恢复通知默认设置")).toBeInTheDocument();
  });

  describe("浏览器权限", () => {
    const stubNotification = (permission: NotificationPermission) => {
      const requestPermission = vi.fn(async () => "granted" as const);
      class FakeNotification {
        static permission = permission;
        static requestPermission = requestPermission;
      }
      vi.stubGlobal("Notification", FakeNotification);
      return requestPermission;
    };
    const browserRoute = (call: { path: string; method: string }) =>
      call.path === channelsPath && call.method === "GET"
        ? json(200, { channels: [channel("desktop"), channel("browser")] })
        : undefined;

    it("未授权显示启用按钮，点击后请求权限", async () => {
      const user = userEvent.setup();
      const request = stubNotification("default");
      renderAt(`${notifications}`, browserRoute);
      await user.click(await screen.findByText("浏览器"));
      await user.click(await screen.findByRole("button", { name: "启用" }));
      await waitFor(() => expect(request).toHaveBeenCalled());
      expect(await screen.findByText("已启用")).toBeInTheDocument();
    });

    it("已授权显示已启用徽标", async () => {
      stubNotification("granted");
      renderAt(notifications, browserRoute);
      fireEvent.click(await screen.findByText("浏览器"));
      expect(
        await screen.findByText("当前浏览器可以显示原生通知横幅。"),
      ).toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "启用" })).toBeNull();
    });

    it("被拦截时显示说明且没有按钮", async () => {
      stubNotification("denied");
      renderAt(notifications, browserRoute);
      fireEvent.click(await screen.findByText("浏览器"));
      expect(
        await screen.findByText(
          "通知已被浏览器拦截。请在浏览器设置中为本站点开启通知权限。",
        ),
      ).toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "启用" })).toBeNull();
    });
  });
});

describe("收到新通知时弹出", () => {
  /** 记录创建过的原生通知，权限与前台状态由各用例设置。 */
  function stubNotification(permission: NotificationPermission) {
    const created: Array<{ title: string; options?: NotificationOptions }> = [];
    class FakeNotification {
      static permission = permission;
      static requestPermission = vi.fn(async () => permission);
      onclick: (() => void) | null = null;
      close = vi.fn();
      constructor(title: string, options?: NotificationOptions) {
        created.push({ title, options });
      }
    }
    vi.stubGlobal("Notification", FakeNotification);
    return created;
  }

  it("只对页面打开后新到、且渠道与事件类型都开启的通知弹出，并按 id 去重", async () => {
    const created = stubNotification("granted");
    vi.spyOn(document, "hasFocus").mockReturnValue(false);
    let groups: InboxGroup[] = [];
    const { queryClient } = renderAt(`/o/${ORG}/inbox`, (call) => {
      if (call.path === channelsPath && call.method === "GET")
        return json(200, {
          channels: [
            channel("desktop"),
            channel("browser", { disabled_events: ["mention"] }),
          ],
        });
      if (call.path === `/v1/organizations/${ORG}/inbox`)
        return json(200, {
          groups,
          counts: {
            inbox: groups.length,
            inbox_unread: groups.length,
            inbox_important_unread: 0,
            inbox_other_unread: 0,
            later: 0,
            cleared: 0,
          },
        });
      return undefined;
    });
    // 页面刚打开时已有的通知不弹出（这里先给一个空列表，等待初始拉取与渠道加载完成）
    await waitFor(() =>
      expect(queryClient.getQueryData(keys.inboxStats(ORG))).toBeDefined(),
    );
    await waitFor(() =>
      expect(queryClient.getQueryData(keys.notificationChannels)).toBeDefined(),
    );

    groups = [
      group({ notification_id: "n-mention", kind: "MENTION", id: "g-m" }),
      group({
        notification_id: "n-comment",
        kind: "TASK_COMMENT",
        id: "g-c",
        safe_summary: "林小北 评论了你的任务",
        task_title: "修复登录页",
      }),
    ];
    await queryClient.invalidateQueries({ queryKey: keys.inboxStats(ORG) });
    await waitFor(() => expect(created).toHaveLength(1));
    expect(created[0]?.title).toBe("林小北 评论了你的任务");
    expect(created[0]?.options?.body).toBe("修复登录页");

    // 同一个通知再次到达不重复弹出
    await queryClient.invalidateQueries({ queryKey: keys.inboxStats(ORG) });
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(created).toHaveLength(1);
  });

  it("没有授权或页面在前台时不弹出", async () => {
    const created = stubNotification("default");
    vi.spyOn(document, "hasFocus").mockReturnValue(true);
    let groups: InboxGroup[] = [];
    const { queryClient } = renderAt(`/o/${ORG}/inbox`, (call) => {
      if (call.path === channelsPath && call.method === "GET")
        return json(200, {
          channels: [channel("desktop"), channel("browser")],
        });
      if (call.path === `/v1/organizations/${ORG}/inbox`)
        return json(200, {
          groups,
          counts: {
            inbox: 0,
            inbox_unread: 0,
            inbox_important_unread: 0,
            inbox_other_unread: 0,
            later: 0,
            cleared: 0,
          },
        });
      return undefined;
    });
    await waitFor(() =>
      expect(queryClient.getQueryData(keys.inboxStats(ORG))).toBeDefined(),
    );
    await waitFor(() =>
      expect(queryClient.getQueryData(keys.notificationChannels)).toBeDefined(),
    );
    groups = [group({ notification_id: "n-assign" })];
    await queryClient.invalidateQueries({ queryKey: keys.inboxStats(ORG) });
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(created).toHaveLength(0);
  });
});

describe("纯函数：通知弹出判断", () => {
  it("收件箱类型映射到通知事件", () => {
    expect(eventForKind("TASK_ASSIGNED")).toBe("assign");
    expect(eventForKind("TRIAGE_NEW")).toBe("assign");
    expect(eventForKind("TASK_COMMENT")).toBe("comment");
    expect(eventForKind("REPLY")).toBe("comment");
    expect(eventForKind("MENTION")).toBe("mention");
    expect(eventForKind("RUN_FAILED")).toBe("agent");
    expect(eventForKind("HANDOFF")).toBe("agent");
    expect(eventForKind("HANDOFF_REVIEW")).toBe("agent");
  });

  it("只挑出未读、未见过、创建于打开之后的通知，并记入已见", () => {
    const seen = new Set<string>(["old"]);
    const openedAt = Date.parse("2026-10-30T10:00:00Z");
    const picked = pickNewNotifications(
      [
        group({ notification_id: "old", latest_at: "2026-10-30T11:00:00Z" }),
        group({ notification_id: "early", latest_at: "2026-10-30T09:00:00Z" }),
        group({ notification_id: "read", unread: false }),
        group({ notification_id: "fresh", latest_at: "2026-10-30T11:00:00Z" }),
      ],
      seen,
      openedAt,
    );
    expect(picked.map((item) => item.notification_id)).toEqual(["fresh"]);
    expect(seen.has("early")).toBe(true);
    expect(seen.has("fresh")).toBe(true);
  });

  it("渠道关闭、事件关闭、未授权或前台时不弹出", () => {
    const base = { permission: "granted" as const, focused: false };
    const on = channel("browser");
    expect(shouldAlert(group({}), { ...base, channel: on })).toBe(true);
    expect(
      shouldAlert(group({}), {
        ...base,
        channel: channel("browser", { enabled: false }),
      }),
    ).toBe(false);
    expect(
      shouldAlert(group({}), {
        ...base,
        channel: channel("browser", { disabled_events: ["assign"] }),
      }),
    ).toBe(false);
    expect(
      shouldAlert(group({}), { ...base, permission: "default", channel: on }),
    ).toBe(false);
    expect(
      shouldAlert(group({}), { ...base, focused: true, channel: on }),
    ).toBe(false);
    expect(alertChannel([on])).toBe(on);
  });
});

describe("开始使用", () => {
  const progress = {
    score: 3,
    total: 6,
    categories: [
      {
        key: "get_started",
        steps: [
          {
            key: "connect_machine",
            points: 1,
            completed: true,
            available: true,
          },
          { key: "create_agent", points: 2, completed: true, available: true },
          { key: "create_task", points: 2, completed: false, available: true },
        ],
      },
      {
        key: "collaboration",
        steps: [
          {
            key: "invite_member",
            points: 1,
            completed: false,
            available: true,
          },
          {
            key: "install_skill",
            points: 2,
            completed: false,
            available: false,
          },
        ],
      },
    ],
  };
  const route = (call: { path: string; method: string }) =>
    call.path === `/v1/organizations/${ORG}/get-started`
      ? json(200, progress)
      : undefined;

  it("左栏显示已得分与总分", async () => {
    renderAt(getStarted, route);
    expect(
      await screen.findByRole("img", { name: "已得分 3 / 6" }),
    ).toBeInTheDocument();
    expect(screen.getByText("开始使用 OpenFox")).toBeInTheDocument();
  });

  it("默认只展开第一个分类", async () => {
    renderAt(getStarted, route);
    expect(await screen.findByText("连接一台电脑")).toBeInTheDocument();
    expect(screen.queryByText("邀请团队成员")).toBeNull();
  });

  it("?guide=advanced 展开有未完成可用步骤的分类", async () => {
    renderAt(`${getStarted}?guide=advanced`, route);
    expect(await screen.findByText("邀请团队成员")).toBeInTheDocument();
    expect(screen.getByText("即将开放")).toBeInTheDocument();
  });

  it("?guide=first 只展开开始使用", async () => {
    renderAt(`${getStarted}?guide=first`, route);
    expect(await screen.findByText("连接一台电脑")).toBeInTheDocument();
    expect(screen.queryByText("邀请团队成员")).toBeNull();
  });

  it("已完成的步骤只显示说明，未完成的可用步骤显示 CTA", async () => {
    const user = userEvent.setup();
    renderAt(getStarted, route);
    await user.click(await screen.findByText("创建你的第一个 Agent"));
    expect(screen.queryByRole("button", { name: "创建 Agent" })).toBeNull();
    await user.click(screen.getByText("创建第一个任务"));
    expect(
      screen.getByRole("button", { name: "新建任务" }),
    ).toBeInTheDocument();
  });

  it("设置导航显示开始使用的已完成 / 可用数", async () => {
    renderAt(`/o/${ORG}/settings/profile`, route);
    const link = await screen.findByRole("link", { name: /开始使用/ });
    expect(within(link).getByText("2/3")).toBeInTheDocument();
  });

  it("CTA 目标：找不到页面的跳设置首页，空间相关的没有空间时跳空间列表", () => {
    expect(stepPath("create_agent", ORG, undefined)).toBe(`/o/${ORG}/agents`);
    expect(stepPath("create_view", ORG, "s1")).toBe(
      `/o/${ORG}/spaces/s1/views/new`,
    );
    expect(stepPath("create_view", ORG, undefined)).toBe(
      `/o/${ORG}/settings/spaces`,
    );
    expect(stepPath("install_skill", ORG, "s1")).toBe(`/o/${ORG}/settings`);
    expect(stepPath("connect_jira_or_tapd", ORG, "s1")).toBe(
      `/o/${ORG}/settings/import-export`,
    );
    expect(stepPath("connect_feishu", ORG, "s1")).toBe(
      `/o/${ORG}/settings/integrations/feishu`,
    );
    expect(stepPath("create_webhook_automation", ORG, "s1")).toBe(
      `/o/${ORG}/spaces/s1/automations`,
    );
    expect(stepPath("run_automation", ORG, undefined)).toBe(
      `/o/${ORG}/settings/spaces`,
    );
    expect(stepPath("connect_git", ORG, "s1")).toBe(
      `/o/${ORG}/settings/spaces/s1/repos`,
    );
  });

  it("隐藏侧栏向导只写入本机 localStorage", async () => {
    const user = userEvent.setup();
    renderAt(getStarted, route);
    await user.click(await screen.findByRole("button", { name: "设置向导" }));
    await user.click(screen.getByRole("menuitem", { name: "隐藏侧边栏向导" }));
    expect(localStorage.getItem("openfox:advanced-guide-hidden")).toBe("1");
  });
});

describe("内测功能开关", () => {
  it("标准账号不显示导航项，访问地址回到个人资料", async () => {
    renderAt(betaFeatures, () => undefined, "standard");
    expect(
      await screen.findByRole("textbox", { name: "全名" }),
    ).toBeInTheDocument();
    expect(screen.queryByText("内测功能开关")).toBeNull();
  });

  it("内测账号显示空态", async () => {
    renderAt(
      betaFeatures,
      (call) =>
        call.path === "/v1/me/beta-features"
          ? json(200, { features: [] })
          : undefined,
      "beta",
    );
    expect(await screen.findByText("暂无可用的内测功能")).toBeInTheDocument();
    expect(
      screen.getByText("新的内测功能准备好后，会显示在这里供你选择开启。"),
    ).toBeInTheDocument();
  });

  it("开关失败时回滚并提示；名称取不到时显示 key", async () => {
    const user = userEvent.setup();
    renderAt(
      betaFeatures,
      (call) => {
        if (call.path === "/v1/me/beta-features" && call.method === "GET")
          return json(200, {
            features: [{ key: "x-feature", enabled: false }],
          });
        if (call.path === "/v1/me/beta-features/x-feature")
          return apiErr(500, "INTERNAL");
        return undefined;
      },
      "beta",
    );
    const toggle = await screen.findByRole("switch", { name: "x-feature" });
    expect(toggle).not.toBeChecked();
    await user.click(toggle);
    expect(await screen.findByText("更新内测功能失败")).toBeInTheDocument();
    await waitFor(() =>
      expect(
        screen.getByRole("switch", { name: "x-feature" }),
      ).not.toBeChecked(),
    );
  });

  it("文案缺失时返回 undefined（页面退回显示 key）", () => {
    const t: Translate = (key) => key;
    expect(featureText(t, "x", "name")).toBeUndefined();
  });
});
