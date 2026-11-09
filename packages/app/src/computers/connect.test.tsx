import { QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  createApi,
  HttpClient,
  type Device,
  type DeviceCommand,
  type DeviceDetail,
} from "@openfox/api";
import { I18nProvider } from "@openfox/i18n";
import { ToastProvider } from "@openfox/ui";

import {
  RuntimeTestButton,
  runtimeTestBlock,
} from "../agents/settings/RuntimeTest";
import { isReservedEnvName } from "../agents/model";
import { createQueryClient } from "../App";
import { ApiProvider } from "../context";
import {
  apiError,
  json,
  renderSettings,
  ts,
  type Call,
  type Route,
} from "../settings/testing";
import { COMMAND_POLL_MS } from "./commands";
import { CONNECT_CLOSE_MS, CONNECT_POLL_MS } from "./ConnectComputer";
import { formatBytes, formatCountdown } from "./model";

// 连接新电脑、存储清理、远程命令、测试连接、防止休眠

const ORG = "/v1/organizations/o1";
const INSTALL_TOKEN = "ofx_v2_eyJ2IjoyLCJjIjoiaHR0cHM6Ly94Iiwicy";

function device(overrides: Partial<Device> = {}): Device {
  return {
    id: "d1",
    grant_id: "g1",
    owner_user_id: "u1",
    owner_email: "lin@openfox.dev",
    name: "build-server-01",
    install_id: null,
    status: "ACTIVE",
    grant_status: "ACTIVE",
    online: true,
    generation: 1,
    max_slots: 4,
    reserved_slots: 0,
    last_seen_at: ts,
    protocol_major: 1,
    protocol_minor: 0,
    daemon_version: "0.42.1",
    operating_system: "linux",
    architecture: "x86_64",
    capabilities: {},
    center_epoch: 1,
    version: 1,
    settings_version: 1,
    grant_scope: "ORGANIZATION",
    grant_user_id: null,
    grant_space_id: null,
    grant_space_name: null,
    permission_version: 1,
    runtimes: [
      {
        provider: "codex",
        version: "0.46.0",
        status: "READY",
        runnable: true,
        reason: null,
        login_status: "LOGGED_IN",
        models: [],
        approval_modes: ["FOLLOW_RUNTIME"],
      },
    ],
    agent_count: 0,
    active_agent_count: 0,
    running_tasks: 0,
    storage: null,
    runtimes_checked_at: null,
    ...overrides,
  };
}

function detail(overrides: Partial<DeviceDetail> = {}): DeviceDetail {
  return {
    ...device(),
    bound_agents: [],
    created_at: ts,
    timezone: null,
    ...overrides,
  };
}

function command(overrides: Partial<DeviceCommand> = {}): DeviceCommand {
  return {
    id: "c1",
    device_id: "d1",
    kind: "storage_cleanup",
    params: {},
    status: "PENDING",
    result: null,
    error_code: "",
    created_at: new Date().toISOString(),
    delivered_at: null,
    completed_at: null,
    expires_at: new Date(Date.now() + 120_000).toISOString(),
    ...overrides,
  };
}

/** 签发接口：每次签发返回不同的令牌，10 分钟后过期 */
function issueRoute(issued: Call[], status = 201): Route {
  return (call) => {
    if (
      call.method !== "POST" ||
      call.path !== `${ORG}/devices/registration-tokens`
    )
      return undefined;
    issued.push(call);
    if (status !== 201) return apiError(status, "FORBIDDEN");
    const n = issued.length;
    const body = call.body as { scope?: string };
    return json(201, {
      id: `t${n}`,
      token: `secret-${n}`,
      install_token: `${INSTALL_TOKEN}${n}`,
      command: `openfox computer register --install-token ${INSTALL_TOKEN}${n}`,
      scope: body.scope ?? "PERSONAL",
      expires_at: new Date(Date.now() + 10 * 60_000).toISOString(),
    });
  };
}

const combine =
  (...routes: Route[]): Route =>
  (call) => {
    for (const route of routes) {
      const response = route(call);
      if (response) return response;
    }
    return undefined;
  };

function devicesRoute(list: () => Device[]): Route {
  return (call) =>
    call.method === "GET" && call.path === `${ORG}/devices`
      ? json(200, { devices: list(), next_cursor: null })
      : undefined;
}

const user = () =>
  userEvent.setup({ advanceTimers: (ms) => vi.advanceTimersByTime(ms) });

beforeEach(() => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
});

afterEach(() => {
  vi.useRealTimers();
  delete (globalThis as { openfoxDesktop?: unknown }).openfoxDesktop;
  localStorage.clear();
});

async function openConnect() {
  const button = await screen.findByRole("button", { name: "连接新电脑" });
  await user().click(button);
  return screen.findByRole("dialog", { name: "连接新电脑" });
}

describe("格式化", () => {
  it("字节数与倒计时", () => {
    expect(formatBytes(0, "zh-CN")).toBe("0 B");
    expect(formatBytes(1536, "zh-CN")).toBe("1.5 KB");
    expect(formatBytes(1.2 * 1024 ** 3, "zh-CN")).toBe("1.2 GB");
    expect(formatCountdown(600_000)).toBe("10:00");
    expect(formatCountdown(61_001)).toBe("1:02");
    expect(formatCountdown(-5)).toBe("0:00");
  });
});

describe("保留环境变量", () => {
  it("精确名与前缀命中，大小写按原样匹配", () => {
    for (const name of [
      "CODEX_HOME",
      "HOME",
      "PATH",
      "OPENFOX_TOKEN",
      "CODEX_X",
      "DYLD_INSERT_LIBRARIES",
      "LD_PRELOAD",
    ])
      expect(isReservedEnvName(name), name).toBe(true);
    for (const name of [
      "GITHUB_TOKEN",
      "home",
      "PATHS",
      "MY_HOME",
      "ld_preload",
    ])
      expect(isReservedEnvName(name), name).toBe(false);
  });
});

describe("连接新电脑弹窗", () => {
  it("普通成员只能选个人：组织项禁用并说明，签发请求 scope = PERSONAL", async () => {
    const issued: Call[] = [];
    renderSettings(
      "/o/o1/settings/computers",
      combine(
        issueRoute(issued),
        devicesRoute(() => []),
      ),
      "MEMBER",
    );
    const dialog = await openConnect();
    const org = await within(dialog).findByRole("radio", { name: /整个组织/ });
    await waitFor(() => expect(org).toBeDisabled());
    expect(
      within(dialog).getByText("只有组织所有者或管理员可以连接组织电脑"),
    ).toBeInTheDocument();
    expect(
      within(dialog).getByRole("radio", { name: /仅我自己/ }),
    ).toBeChecked();
    await user().click(
      within(dialog).getByRole("button", { name: "生成连接命令" }),
    );
    await waitFor(() => expect(issued).toHaveLength(1));
    expect(issued[0]!.body).toEqual({ name: "我的电脑", scope: "PERSONAL" });
  });

  it("所有者可以选组织；访客看不到签发入口", async () => {
    const issued: Call[] = [];
    renderSettings(
      "/o/o1/settings/computers",
      combine(
        issueRoute(issued),
        devicesRoute(() => []),
      ),
      "OWNER",
    );
    const dialog = await openConnect();
    const org = await within(dialog).findByRole("radio", { name: /整个组织/ });
    await waitFor(() => expect(org).toBeEnabled());
    await user().click(org);
    const name = within(dialog).getByRole("textbox");
    await user().clear(name);
    await user().type(name, "gpu-box");
    await user().click(
      within(dialog).getByRole("button", { name: "生成连接命令" }),
    );
    await waitFor(() => expect(issued).toHaveLength(1));
    expect(issued[0]!.body).toEqual({ name: "gpu-box", scope: "ORGANIZATION" });
  });

  it("访客：提示不能连接电脑", async () => {
    renderSettings(
      "/o/o1/settings/computers",
      devicesRoute(() => []),
      "GUEST" as never,
    );
    const dialog = await openConnect();
    expect(
      await within(dialog).findByText("访客不能连接电脑，请联系组织管理员。"),
    ).toBeInTheDocument();
    expect(
      within(dialog).queryByRole("button", { name: "生成连接命令" }),
    ).toBeNull();
  });

  it("签发失败 403：提示没有权限", async () => {
    renderSettings(
      "/o/o1/settings/computers",
      combine(
        issueRoute([], 403),
        devicesRoute(() => []),
      ),
      "MEMBER",
    );
    const dialog = await openConnect();
    await user().click(
      await within(dialog).findByRole("button", { name: "生成连接命令" }),
    );
    expect(
      await screen.findByText("你没有权限连接这个范围的电脑"),
    ).toBeInTheDocument();
  });

  it("复制命令：写入剪贴板，令牌不写 localStorage", async () => {
    renderSettings(
      "/o/o1/settings/computers",
      combine(
        issueRoute([]),
        devicesRoute(() => []),
      ),
    );
    // user-event 会接管 navigator.clipboard，在 setup 之后再监听
    const u = user();
    const writeText = vi
      .spyOn(navigator.clipboard, "writeText")
      .mockResolvedValue(undefined);
    await u.click(await screen.findByRole("button", { name: "连接新电脑" }));
    const dialog = await screen.findByRole("dialog", { name: "连接新电脑" });
    await u.click(
      await within(dialog).findByRole("button", { name: "生成连接命令" }),
    );
    const expected = `openfox computer register --install-token ${INSTALL_TOKEN}1`;
    expect(
      await within(dialog).findByTestId("connect-command"),
    ).toHaveTextContent(expected);
    await u.click(within(dialog).getByRole("button", { name: "复制命令" }));
    expect(writeText).toHaveBeenCalledWith(expected);
    expect(await screen.findByText("已复制连接命令")).toBeInTheDocument();
    for (let i = 0; i < localStorage.length; i++) {
      const key = localStorage.key(i)!;
      expect(localStorage.getItem(key) ?? "").not.toContain(INSTALL_TOKEN);
    }
  });

  it("倒计时到 0 后隐藏命令，重新生成得到新命令", async () => {
    const issued: Call[] = [];
    renderSettings(
      "/o/o1/settings/computers",
      combine(
        issueRoute(issued),
        devicesRoute(() => []),
      ),
    );
    const dialog = await openConnect();
    await user().click(
      await within(dialog).findByRole("button", { name: "生成连接命令" }),
    );
    expect(
      await within(dialog).findByTestId("connect-countdown"),
    ).toHaveTextContent(/10:00 后过期|9:59 后过期/);
    await act(() => vi.advanceTimersByTimeAsync(5 * 60_000));
    expect(within(dialog).getByTestId("connect-countdown")).toHaveTextContent(
      /5:00 后过期|4:59 后过期/,
    );
    await act(() => vi.advanceTimersByTimeAsync(5 * 60_000 + 1000));
    expect(await within(dialog).findByText("命令已过期")).toBeInTheDocument();
    expect(within(dialog).queryByTestId("connect-command")).toBeNull();
    await user().click(
      within(dialog).getByRole("button", { name: "重新生成" }),
    );
    await waitFor(() => expect(issued).toHaveLength(2));
    expect(
      await within(dialog).findByTestId("connect-command"),
    ).toHaveTextContent(`${INSTALL_TOKEN}2`);
    expect(within(dialog).queryByText("命令已过期")).toBeNull();
  });

  it("新电脑上线后显示成功并自动关闭；已有电脑不算", async () => {
    let list: Device[] = [device({ id: "old", name: "旧电脑" })];
    renderSettings(
      "/o/o1/settings/computers",
      combine(
        issueRoute([]),
        devicesRoute(() => list),
      ),
    );
    const dialog = await openConnect();
    await user().click(
      await within(dialog).findByRole("button", { name: "生成连接命令" }),
    );
    expect(
      await within(dialog).findByText("正在等待电脑上线…"),
    ).toBeInTheDocument();
    // 新电脑已注册但还没心跳：不算连接成功
    list = [...list, device({ id: "new", name: "新电脑", online: false })];
    await act(() => vi.advanceTimersByTimeAsync(CONNECT_POLL_MS + 100));
    expect(within(dialog).queryByText("电脑已连接")).toBeNull();
    list = [list[0]!, device({ id: "new", name: "新电脑", online: true })];
    await act(() => vi.advanceTimersByTimeAsync(CONNECT_POLL_MS + 100));
    expect(await within(dialog).findByText("电脑已连接")).toBeInTheDocument();
    expect(
      within(dialog).getByText("「新电脑」已上线，窗口即将关闭。"),
    ).toBeInTheDocument();
    await act(() => vi.advanceTimersByTimeAsync(CONNECT_CLOSE_MS + 100));
    await waitFor(() =>
      expect(screen.queryByRole("dialog", { name: "连接新电脑" })).toBeNull(),
    );
  });

  it("桌面端：在这台电脑上连接，把安装令牌交给桥接", async () => {
    const registerComputer = vi.fn(() => Promise.resolve({ ok: true }));
    (globalThis as { openfoxDesktop?: unknown }).openfoxDesktop = {
      getLocalStatus: () =>
        Promise.resolve({
          daemon: { state: "STOPPED" },
          runtime: { runtime: "codex", runnable: false },
        }),
      daemonAction: vi.fn(),
      pickDirectory: vi.fn(),
      registerComputer,
    };
    renderSettings(
      "/o/o1/settings/computers",
      combine(
        issueRoute([]),
        devicesRoute(() => []),
      ),
    );
    const dialog = await openConnect();
    await user().click(
      await within(dialog).findByRole("button", { name: "生成连接命令" }),
    );
    await user().click(
      await within(dialog).findByRole("button", { name: "在这台电脑上连接" }),
    );
    expect(registerComputer).toHaveBeenCalledWith(`${INSTALL_TOKEN}1`);
    expect(
      await screen.findByText("本机已注册，正在等待上线"),
    ).toBeInTheDocument();
  });

  it("桌面端注册失败：只按原因代码提示", async () => {
    (globalThis as { openfoxDesktop?: unknown }).openfoxDesktop = {
      getLocalStatus: () =>
        Promise.resolve({
          daemon: { state: "STOPPED" },
          runtime: { runtime: "codex", runnable: false },
        }),
      daemonAction: vi.fn(),
      pickDirectory: vi.fn(),
      registerComputer: () =>
        Promise.resolve({ ok: false, reason: "already_registered" }),
    };
    renderSettings(
      "/o/o1/settings/computers",
      combine(
        issueRoute([]),
        devicesRoute(() => []),
      ),
    );
    const dialog = await openConnect();
    await user().click(
      await within(dialog).findByRole("button", { name: "生成连接命令" }),
    );
    await user().click(
      await within(dialog).findByRole("button", { name: "在这台电脑上连接" }),
    );
    expect(
      await screen.findByText(/这台电脑已经连接过 OpenFox/),
    ).toBeInTheDocument();
  });
});

describe("电脑详情 · 存储与重新检测", () => {
  const storage = {
    total_bytes: 18.4 * 1024 ** 3,
    worktree_bytes: 12 * 1024 ** 3,
    worktree_count: 7,
    codex_home_bytes: 1.2 * 1024 ** 3,
    journal_bytes: 300 * 1024 ** 2,
    measured_at: ts,
    partial: true,
    categories: [],
  };

  function detailRoute(
    value: DeviceDetail,
    commands: Call[],
    sequence: DeviceCommand[],
  ): Route {
    let polls = 0;
    return (call) => {
      if (call.method === "GET" && call.path === `${ORG}/devices/d1`)
        return json(200, value);
      if (
        call.method === "POST" &&
        call.path === `${ORG}/devices/d1/commands`
      ) {
        if ((call.body as { kind?: string })?.kind === "device_settings") {
          return json(202, {
            command: command({
              id: "read-policy",
              kind: "device_settings",
              status: "SUCCEEDED",
              params: { storage: true },
              result: {
                version: 1,
                policy: {
                  enabled: false,
                  interval_hours: 1,
                  retain_days: 7,
                  min_free_gib: 10,
                  max_gib: 0,
                },
              },
            }),
          });
        }
        commands.push(call);
        return json(202, { command: sequence[0] });
      }
      if (
        call.method === "GET" &&
        call.path.startsWith(`${ORG}/devices/d1/commands/`)
      ) {
        if (call.path.endsWith("/read-policy"))
          return json(200, {
            command: command({
              id: "read-policy",
              kind: "device_settings",
              status: "SUCCEEDED",
              params: { storage: true },
              result: {
                version: 1,
                policy: {
                  enabled: false,
                  interval_hours: 1,
                  retain_days: 7,
                  min_free_gib: 10,
                  max_gib: 0,
                },
              },
            }),
          });
        commands.push(call);
        const next = sequence[Math.min(++polls, sequence.length - 1)]!;
        return json(200, { command: next });
      }
      return undefined;
    };
  }

  it("显示存储统计与 partial 提示；清理前确认删除范围，轮询到终态后显示释放空间并停止", async () => {
    const calls: Call[] = [];
    const done = command({
      status: "SUCCEEDED",
      result: {
        freed_bytes: 1.5 * 1024 ** 3,
        removed: { worktrees: 3, codex_homes: 2, journal_rows: 120 },
        skipped_active: 1,
        skipped_dirty: 2,
      },
    });
    renderSettings(
      "/o/o1/settings/computers?computer=d1",
      detailRoute(detail({ storage }), calls, [
        command(),
        command({ status: "DELIVERED" }),
        done,
      ]),
    );
    expect(await screen.findByText("18.4 GB")).toBeInTheDocument();
    expect(screen.getByText("7 个 git worktree")).toBeInTheDocument();
    expect(screen.getByText("1.2 GB")).toBeInTheDocument();
    expect(
      screen.getByText("文件过多，本次统计提前结束，实际占用可能更大。"),
    ).toBeInTheDocument();

    await user().click(screen.getByRole("button", { name: "管理存储" }));
    await user().click(
      await screen.findByRole("button", { name: "清理过期数据" }),
    );
    const dialog = await screen.findByRole("dialog", {
      name: "清理这台电脑的存储？",
    });
    expect(
      within(dialog).getByText(/已结束且超过保留期的任务工作区/),
    ).toBeInTheDocument();
    expect(
      within(dialog).getByText(/过期的 Runtime 临时目录/),
    ).toBeInTheDocument();
    expect(
      within(dialog).getByText("超过保留期的运行记录"),
    ).toBeInTheDocument();
    expect(
      within(dialog).getByText(/有未提交改动的工作区会保留/),
    ).toBeInTheDocument();
    expect(calls).toHaveLength(0);
    await user().click(
      within(dialog).getByRole("button", { name: "开始清理" }),
    );
    await waitFor(() => expect(calls.length).toBeGreaterThan(0));
    expect(calls[0]!.method).toBe("POST");
    expect(calls[0]!.body).toEqual({ kind: "storage_cleanup", params: {} });
    // 进行中：按钮加载并禁用
    await waitFor(() =>
      expect(screen.getByRole("button", { name: /管理存储/ })).toBeDisabled(),
    );
    await act(() => vi.advanceTimersByTimeAsync(COMMAND_POLL_MS * 3));
    expect(
      await screen.findByText("清理完成，释放了 1.5 GB"),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        /上次清理释放 1.5 GB：工作区 3 个、临时目录 2 个、运行记录 120 条；跳过进行中的 1 项，保留有未提交改动的工作区 2 个/,
      ),
    ).toBeInTheDocument();
    const polled = calls.length;
    await act(() => vi.advanceTimersByTimeAsync(COMMAND_POLL_MS * 5));
    expect(calls.length).toBe(polled);
    expect(screen.getByRole("button", { name: "管理存储" })).toBeEnabled();
  });

  it("按类型占用：有 categories 时显示占用条与图例（未知键并入其他），没有时退回总量", async () => {
    const withCategories = {
      ...storage,
      categories: [
        { key: "cache", bytes: 2 * 1024 ** 3, count: 0 },
        { key: "task", bytes: 9 * 1024 ** 3, count: 5 },
        { key: "conversation", bytes: 4 * 1024 ** 3, count: 2 },
        { key: "mystery", bytes: 1 * 1024 ** 3, count: 0 },
        { key: "other", bytes: 0.5 * 1024 ** 3, count: 0 },
      ],
    };
    renderSettings(
      "/o/o1/settings/computers?computer=d1",
      detailRoute(detail({ storage: withCategories }), [], [command()]),
    );
    const usage = await screen.findByTestId("storage-usage");
    expect(within(usage).getByText(/可清理缓存 2 GB/)).toBeInTheDocument();
    expect(within(usage).getByText(/任务 9 GB/)).toBeInTheDocument();
    expect(within(usage).getByText(/对话 4 GB/)).toBeInTheDocument();
    expect(within(usage).getByText(/其他 1.5 GB/)).toBeInTheDocument();
    expect(within(usage).queryByText(/自动化/)).toBeNull();
    expect(within(usage).getByRole("img", { name: "占用分布" })).toBeVisible();
    // 原有总量行仍然存在
    expect(screen.getByText("18.4 GB")).toBeInTheDocument();
  });

  it("没有 categories 时不显示占用条", async () => {
    renderSettings(
      "/o/o1/settings/computers?computer=d1",
      detailRoute(detail({ storage }), [], [command()]),
    );
    expect(await screen.findByText("18.4 GB")).toBeInTheDocument();
    expect(screen.queryByTestId("storage-usage")).toBeNull();
  });

  it("非所有者：清理与重新检测禁用", async () => {
    renderSettings(
      "/o/o1/settings/computers?computer=d1",
      detailRoute(detail({ storage, owner_user_id: "u2" }), [], [command()]),
    );
    expect(await screen.findByText("18.4 GB")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "管理存储" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "重新检测" })).toBeDisabled();
  });

  it("重新检测：发起 runtime_probe，过期时提示电脑没有响应", async () => {
    const calls: Call[] = [];
    renderSettings(
      "/o/o1/settings/computers?computer=d1",
      detailRoute(detail(), calls, [
        command({ kind: "runtime_probe" }),
        command({ kind: "runtime_probe", status: "EXPIRED" }),
      ]),
    );
    await user().click(await screen.findByRole("button", { name: "重新检测" }));
    await waitFor(() =>
      expect(calls[0]?.body).toEqual({ kind: "runtime_probe", params: {} }),
    );
    await act(() => vi.advanceTimersByTimeAsync(COMMAND_POLL_MS + 100));
    expect(
      await screen.findByText("电脑没有响应，可能已离线"),
    ).toBeInTheDocument();
  });
});

describe("Agent 设置 · 测试连接", () => {
  it("禁用规则：非所有者 / 不支持的 Runtime / 离线", () => {
    expect(runtimeTestBlock(device(), "codex", "u1")).toBeNull();
    expect(runtimeTestBlock(device(), "codex", "u2")).toBe("ownerOnly");
    expect(runtimeTestBlock(device(), "codex", undefined)).toBe("ownerOnly");
    expect(runtimeTestBlock(undefined, "codex", "u1")).toBe("ownerOnly");
    expect(runtimeTestBlock(device(), "gemini-cli", "u1")).toBe("unsupported");
    expect(runtimeTestBlock(device({ online: false }), "codex", "u1")).toBe(
      "offline",
    );
  });

  function renderTest(
    props: { meId?: string; online?: boolean; model?: string },
    route: Route,
  ) {
    const calls: Call[] = [];
    const transport = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), "http://localhost");
      const raw = init?.body;
      const call: Call = {
        method: init?.method ?? "GET",
        path: url.pathname.replace(/^\/api/, ""),
        query: url.searchParams,
        body:
          typeof raw === "string" ? (JSON.parse(raw) as unknown) : undefined,
        headers: new Headers(init?.headers),
      };
      calls.push(call);
      return Promise.resolve(route(call) ?? apiError(404, "NOT_FOUND"));
    });
    const queryClient = createQueryClient();
    queryClient.setDefaultOptions({ queries: { retry: false } });
    const view = render(
      <I18nProvider initialLocale="zh-CN">
        <QueryClientProvider client={queryClient}>
          <ApiProvider
            api={createApi(
              new HttpClient("/api", transport as unknown as typeof fetch),
            )}
          >
            <ToastProvider>
              <RuntimeTestButton
                orgId="o1"
                device={device({ online: props.online ?? true })}
                provider="codex"
                model={props.model ?? "gpt-5-codex"}
                meId={props.meId}
              />
            </ToastProvider>
          </ApiProvider>
        </QueryClientProvider>
      </I18nProvider>,
    );
    return { calls, view };
  }

  it("非所有者禁用，并用 Tooltip 说明", async () => {
    renderTest({ meId: "u2" }, () => undefined);
    const button = screen.getByRole("button", { name: "测试连接" });
    expect(button).toBeDisabled();
    await user().hover(button.parentElement!);
    expect(
      await screen.findByText("只有电脑所有者可以测试连接"),
    ).toBeInTheDocument();
  });

  it("所有者：发起 runtime_test（provider + 当前模型），成功显示延迟", async () => {
    const test = (status: DeviceCommand["status"], result: unknown = null) =>
      command({ kind: "runtime_test", status, result: result as never });
    let polls = 0;
    const { calls } = renderTest({ meId: "u1" }, (call) => {
      if (call.method === "POST")
        return json(202, { command: test("PENDING") });
      if (call.method === "GET")
        return json(200, {
          command:
            ++polls < 2
              ? test("DELIVERED")
              : test("SUCCEEDED", {
                  provider: "codex",
                  ok: true,
                  login_status: "LOGGED_IN",
                  model: "gpt-5-codex",
                  model_available: true,
                  latency_ms: 412,
                  message: "",
                }),
        });
      return undefined;
    });
    await user().click(screen.getByRole("button", { name: "测试连接" }));
    await waitFor(() =>
      expect(calls[0]?.body).toEqual({
        kind: "runtime_test",
        params: { provider: "codex", model: "gpt-5-codex" },
      }),
    );
    await act(() => vi.advanceTimersByTimeAsync(COMMAND_POLL_MS * 3));
    expect(await screen.findByText("已连接 · 412 ms")).toBeInTheDocument();
  });

  it("组件卸载后停止轮询；进行中按钮禁用", async () => {
    const { calls, view } = renderTest(
      { meId: "u1", model: "default" },
      (call) =>
        call.method === "POST" || call.method === "GET"
          ? json(call.method === "POST" ? 202 : 200, {
              command: command({ kind: "runtime_test", status: "DELIVERED" }),
            })
          : undefined,
    );
    await user().click(screen.getByRole("button", { name: "测试连接" }));
    await waitFor(() =>
      expect(calls[0]?.body).toEqual({
        kind: "runtime_test",
        params: { provider: "codex" },
      }),
    );
    await waitFor(() =>
      expect(screen.getByRole("button", { name: /测试连接/ })).toBeDisabled(),
    );
    await act(() => vi.advanceTimersByTimeAsync(COMMAND_POLL_MS * 3 + 100));
    const polled = calls.filter((call) => call.method === "GET").length;
    expect(polled).toBeGreaterThanOrEqual(2);
    view.unmount();
    await act(() => vi.advanceTimersByTimeAsync(COMMAND_POLL_MS * 5));
    expect(calls.filter((call) => call.method === "GET").length).toBe(polled);
  });
});

describe("本机卡片 · 防止休眠", () => {
  it("显示开关并调用桥接 setPreventSleep", async () => {
    const setPreventSleep = vi.fn((enabled: boolean) =>
      Promise.resolve({ enabled, active: enabled }),
    );
    (globalThis as { openfoxDesktop?: unknown }).openfoxDesktop = {
      getLocalStatus: () =>
        Promise.resolve({
          daemon: { state: "RUNNING", active_runs: 1 },
          runtime: { runtime: "codex", runnable: true },
          prevent_sleep: { enabled: false, active: false },
        }),
      daemonAction: vi.fn(),
      pickDirectory: vi.fn(),
      setPreventSleep,
    };
    renderSettings(
      "/o/o1/settings/computers",
      devicesRoute(() => []),
    );
    const local = await screen.findByRole("region", { name: "这台电脑" });
    const toggle = await within(local).findByRole("switch", {
      name: "防止休眠",
    });
    expect(toggle).toHaveAttribute("aria-checked", "false");
    await user().click(toggle);
    expect(setPreventSleep).toHaveBeenCalledWith(true);
    expect(await within(local).findByText("正在阻止休眠")).toBeInTheDocument();
    expect(toggle).toHaveAttribute("aria-checked", "true");
  });

  it("旧版桌面端没有 prevent_sleep 时不显示开关", async () => {
    (globalThis as { openfoxDesktop?: unknown }).openfoxDesktop = {
      getLocalStatus: () =>
        Promise.resolve({
          daemon: { state: "RUNNING" },
          runtime: { runtime: "codex", runnable: true },
        }),
      daemonAction: vi.fn(),
      pickDirectory: vi.fn(),
    };
    renderSettings(
      "/o/o1/settings/computers",
      devicesRoute(() => []),
    );
    const local = await screen.findByRole("region", { name: "这台电脑" });
    await within(local).findByText("可用");
    expect(within(local).queryByRole("switch")).toBeNull();
  });
});
