import { cleanup, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { Device } from "@openfox/api";

import { desktopBridge, type DesktopLocalStatus } from "../desktop";
import {
  apiError,
  json,
  renderSettings,
  ts,
  type Call,
} from "../settings/testing";
import {
  registrationView,
  resetAutoRegisterAttempts,
} from "./localRegistration";

// 桌面端自动注册本机：未注册时签发个人令牌并交给桥接注册；已注册、其他组织、旧配置各自的卡片提示。

const TOKEN = "ofx_v2_dGVzdA";

function status(
  registration: DesktopLocalStatus["registration"],
  cli: NonNullable<DesktopLocalStatus["cli"]>["state"] = "installed",
): DesktopLocalStatus {
  return {
    daemon: { state: "RUNNING" },
    runtime: { runtime: "codex", runnable: true },
    registration,
    cli: { state: cli },
    machine_name: "lin-mbp",
  };
}

function installBridge(initial: DesktopLocalStatus, ok = true) {
  let current = initial;
  const registerComputer = vi.fn(
    (_token: string, _options?: { force?: boolean }) => {
      if (ok)
        current = {
          ...current,
          registration: {
            registered: true,
            organization_id: "o1",
            device_id: "d-new",
          },
        };
      return Promise.resolve(
        ok
          ? ({ ok: true } as const)
          : ({ ok: false, reason: "center_unavailable" } as const),
      );
    },
  );
  (globalThis as { openfoxDesktop?: unknown }).openfoxDesktop = {
    getLocalStatus: () => Promise.resolve(current),
    daemonAction: vi.fn(),
    pickDirectory: vi.fn(),
    registerComputer,
  };
  return registerComputer;
}

const device = (id: string): Device => ({
  id,
  grant_id: `g-${id}`,
  owner_user_id: "u1",
  owner_email: "lin@openfox.dev",
  name: "lin-mbp",
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
  operating_system: "darwin",
  architecture: "arm64",
  capabilities: {},
  center_epoch: 1,
  version: 1,
  settings_version: 1,
  grant_scope: "PERSONAL",
  grant_user_id: "u1",
  grant_space_id: null,
  grant_space_name: null,
  permission_version: 1,
  runtimes: [],
  agent_count: 0,
  active_agent_count: 0,
  running_tasks: 0,
  storage: null,
  runtimes_checked_at: null,
});

const route =
  (devices: Device[]) =>
  (call: Call): Response | undefined => {
    if (call.method === "GET" && call.path === "/v1/organizations/o1/devices")
      return json(200, { devices, next_cursor: null });
    if (
      call.method === "POST" &&
      call.path === "/v1/organizations/o1/devices/registration-tokens"
    )
      return json(201, {
        install_token: TOKEN,
        command: `openfox computer register --install-token ${TOKEN}`,
        expires_at: "2099-01-01T00:00:00Z",
      });
    return undefined;
  };

afterEach(() => {
  delete (globalThis as { openfoxDesktop?: unknown }).openfoxDesktop;
  resetAutoRegisterAttempts();
});

describe("本机注册与组织的关系", () => {
  it("未注册 / 本组织 / 已被删除 / 其他组织 / 未知", () => {
    expect(registrationView(undefined, "o1", undefined)).toEqual({
      kind: "unknown",
    });
    expect(
      registrationView(
        status({ registered: false, reason: "not_registered" }),
        "o1",
        [],
      ),
    ).toEqual({ kind: "not_registered" });
    expect(
      registrationView(
        status({ registered: false, reason: "unavailable" }),
        "o1",
        [],
      ),
    ).toEqual({ kind: "unknown" });
    const mine = status({
      registered: true,
      organization_id: "o1",
      device_id: "d1",
    });
    expect(registrationView(mine, "o1", ["d1"])).toEqual({
      kind: "this_org",
      deviceId: "d1",
      stale: false,
    });
    expect(registrationView(mine, "o1", ["d2"])).toMatchObject({
      stale: true,
    });
    // 电脑列表还没加载时不能判定为已删除
    expect(registrationView(mine, "o1", undefined)).toMatchObject({
      stale: false,
    });
    expect(registrationView(mine, "o2", ["d1"])).toEqual({
      kind: "other_org",
      organizationId: "o1",
    });
  });
});

describe("本机注册的中心地址与服务端是否一致", () => {
  const mine = (center_url?: string) =>
    status({
      registered: true,
      organization_id: "o1",
      device_id: "d1",
      center_url,
    });
  const mismatch = (local: string | undefined, server: string | undefined) =>
    (
      registrationView(mine(local), "o1", ["d1"], server) as {
        staleCenterUrl?: string;
      }
    ).staleCenterUrl;

  it("忽略末尾斜杠、默认端口与大小写", () => {
    const server = "https://203.0.113.42";
    expect(mismatch("https://203.0.113.42/", server)).toBeUndefined();
    expect(mismatch("https://203.0.113.42:443", server)).toBeUndefined();
    expect(
      mismatch("HTTPS://Example.COM", "https://example.com"),
    ).toBeUndefined();
    expect(mismatch("http://a.test:80/", "http://a.test")).toBeUndefined();
  });

  it("scheme、host、端口任一不同视为已变更，并带出本机注册的旧地址", () => {
    expect(mismatch("http://127.0.0.1:18080", "https://203.0.113.42")).toBe(
      "http://127.0.0.1:18080",
    );
    expect(mismatch("http://a.test", "https://a.test")).toBe("http://a.test");
    expect(mismatch("https://a.test:8443", "https://a.test")).toBe(
      "https://a.test:8443",
    );
    expect(mismatch("https://b.test", "https://a.test")).toBe("https://b.test");
  });

  it("任一方缺少或无法解析地址时不判定为不一致", () => {
    expect(mismatch(undefined, "https://a.test")).toBeUndefined();
    expect(mismatch("http://127.0.0.1:18080", undefined)).toBeUndefined();
    expect(mismatch("not a url", "https://a.test")).toBeUndefined();
    expect(mismatch("ftp://a.test", "https://a.test")).toBeUndefined();
    expect(mismatch("", "")).toBeUndefined();
  });
});

describe("本机注册地址已变更 / 连不上服务器的卡片", () => {
  const centerRoute =
    (center: string | null, devices: Device[] = [device("d1")]) =>
    (call: Call): Response | undefined => {
      if (
        call.method === "GET" &&
        call.path === "/v1/organizations/o1/devices/center"
      )
        return center === null
          ? json(500, { error: { code: "INTERNAL_ERROR" } })
          : json(200, { center_url: center });
      return route(devices)(call);
    };
  const registered = (center_url?: string) =>
    ({
      registered: true,
      organization_id: "o1",
      device_id: "d1",
      center_url,
    }) as const;

  it("地址已变更：提示旧地址，点击重新连接走 force: true，且不显示「可用」", async () => {
    const register = installBridge(
      status(registered("http://127.0.0.1:18080")),
    );
    renderSettings(
      "/o/o1/settings/computers",
      centerRoute("https://203.0.113.42"),
    );
    const local = await screen.findByRole("region", { name: "这台电脑" });
    expect(
      await within(local).findByText(
        "这台电脑连接的是旧的服务器地址（http://127.0.0.1:18080），需要重新连接到当前服务器。",
      ),
    ).toBeInTheDocument();
    expect(within(local).queryByText("可用")).toBeNull();
    expect(register).not.toHaveBeenCalled();
    await userEvent.click(
      within(local).getByRole("button", { name: "重新连接" }),
    );
    await waitFor(() =>
      expect(register).toHaveBeenCalledWith(TOKEN, { force: true }),
    );
  });

  it.each([
    ["地址一致（末尾斜杠）", "https://203.0.113.42/", "https://203.0.113.42"],
    ["接口失败", "http://127.0.0.1:18080", null],
    ["旧版桌面端没有地址", undefined, "https://203.0.113.42"],
  ])("%s：不提示，仍是可用", async (_name, local, center) => {
    installBridge(status(registered(local)));
    renderSettings("/o/o1/settings/computers", centerRoute(center));
    const card = await screen.findByRole("region", { name: "这台电脑" });
    expect(await within(card).findByText("可用")).toBeInTheDocument();
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(within(card).queryByText(/旧的服务器地址/)).toBeNull();
  });

  it.each(["RECONNECTING", "DISCONNECTED"])(
    "本机服务运行但 %s：不显示可用，显示连不上服务器",
    async (remote_state) => {
      installBridge({
        ...status(registered("https://203.0.113.42")),
        daemon: { state: "RUNNING", remote_state },
      });
      renderSettings(
        "/o/o1/settings/computers",
        centerRoute("https://203.0.113.42"),
      );
      const local = await screen.findByRole("region", { name: "这台电脑" });
      expect(
        await within(local).findByText("连不上服务器"),
      ).toBeInTheDocument();
      expect(within(local).queryByText("可用")).toBeNull();
      expect(
        within(local).getByText(/本机服务在运行，但连不上服务器/),
      ).toBeInTheDocument();
    },
  );

  it("CONNECTED 或刚进入 CONNECTING 仍显示可用", async () => {
    installBridge({
      ...status(registered("https://203.0.113.42")),
      daemon: { state: "RUNNING", remote_state: "CONNECTING" },
    });
    renderSettings(
      "/o/o1/settings/computers",
      centerRoute("https://203.0.113.42"),
    );
    const local = await screen.findByRole("region", { name: "这台电脑" });
    expect(await within(local).findByText("可用")).toBeInTheDocument();
  });
});

describe("电脑详情页「修复并重连」", () => {
  const offline = (id: string) => ({
    ...device(id),
    online: false,
    runtimes: [],
    bound_agents: [],
    created_at: ts,
  });
  const detailRoute = (call: Call): Response | undefined => {
    if (
      call.method === "GET" &&
      call.path === "/v1/organizations/o1/devices/d1"
    )
      return json(200, offline("d1"));
    return route([device("d1")])(call);
  };

  it("本机：按钮可用，点击走强制重新注册", async () => {
    const register = installBridge(
      status({
        registered: true,
        organization_id: "o1",
        device_id: "d1",
      }),
    );
    renderSettings("/o/o1/settings/computers?computer=d1", detailRoute);
    const button = await screen.findByRole("button", { name: "修复并重连" });
    await waitFor(() => expect(button).toBeEnabled());
    await userEvent.click(button);
    await waitFor(() =>
      expect(register).toHaveBeenCalledWith(TOKEN, { force: true }),
    );
  });

  it("其他电脑或浏览器里：禁用并提示只能在本机客户端修复", async () => {
    installBridge(
      status({
        registered: true,
        organization_id: "o1",
        device_id: "d-other",
      }),
    );
    renderSettings("/o/o1/settings/computers?computer=d1", detailRoute);
    expect(
      await screen.findByRole("button", { name: "修复并重连" }),
    ).toBeDisabled();
    await userEvent.hover(
      screen.getByRole("button", { name: "修复并重连" }).parentElement!,
    );
    expect(
      await screen.findByText("只能在这台电脑上的 OpenFox 客户端里修复"),
    ).toBeInTheDocument();
  });
});

describe("桌面端自动注册本机", () => {
  it("未注册时自动签发个人令牌并注册，只尝试一次", async () => {
    const register = installBridge(
      status({ registered: false, reason: "not_registered" }),
    );
    const calls = renderSettings(
      "/o/o1/settings/computers",
      route([device("d-new")]),
    );
    await waitFor(() => expect(register).toHaveBeenCalledTimes(1));
    expect(register).toHaveBeenCalledWith(TOKEN, { force: false });
    const issue = calls.filter((call) =>
      call.path.endsWith("/devices/registration-tokens"),
    );
    expect(issue).toHaveLength(1);
    expect(issue[0]!.body).toEqual({ name: "lin-mbp", scope: "PERSONAL" });
    expect(await screen.findByText("已自动连接这台电脑")).toBeInTheDocument();
    // 注册后本机状态刷新为已注册，不会再次尝试
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(register).toHaveBeenCalledTimes(1);
  });

  it("注册后电脑还没上线时短间隔刷新电脑列表，直到上线", async () => {
    installBridge(status({ registered: false, reason: "not_registered" }));
    let listed = 0;
    const calls = renderSettings("/o/o1/settings/computers", (call) => {
      if (
        call.method === "GET" &&
        call.path === "/v1/organizations/o1/devices"
      ) {
        listed++;
        // 前两次（含注册前的加载）离线，之后上线
        return json(200, {
          devices: [{ ...device("d-new"), online: listed > 2 }],
          next_cursor: null,
        });
      }
      return route([])(call);
    });
    expect(await screen.findByText("已自动连接这台电脑")).toBeInTheDocument();
    await waitFor(
      () => {
        const lists = calls.filter(
          (call) => call.path === "/v1/organizations/o1/devices",
        );
        expect(lists.length).toBeGreaterThanOrEqual(3);
      },
      { timeout: 8000 },
    );
    const before = listed;
    // 上线后停止短间隔刷新（继续轮询的话 4.5 秒内至少还会再拉两次）
    await new Promise((resolve) => setTimeout(resolve, 4500));
    expect(listed - before).toBeLessThanOrEqual(1);
  }, 20_000);

  it("离开注册组件后停止上线轮询", async () => {
    installBridge(status({ registered: false, reason: "not_registered" }));
    renderSettings("/o/o1/settings/computers", route([]));
    expect(await screen.findByText("已自动连接这台电脑")).toBeInTheDocument();
    const probe = vi.spyOn(desktopBridge()!, "getLocalStatus");
    cleanup();
    await new Promise((resolve) => setTimeout(resolve, 2200));
    expect(probe).not.toHaveBeenCalled();
  });

  it("注册成功后状态读取失败被收敛，不重复注册或产生未处理异常", async () => {
    const register = installBridge(
      status({ registered: false, reason: "not_registered" }),
    );
    renderSettings("/o/o1/settings/computers", route([]));
    expect(await screen.findByText("已自动连接这台电脑")).toBeInTheDocument();
    const probe = vi
      .spyOn(desktopBridge()!, "getLocalStatus")
      .mockRejectedValue(new Error("bridge closed"));
    await waitFor(() => expect(probe).toHaveBeenCalled(), { timeout: 3000 });
    expect(register).toHaveBeenCalledTimes(1);
  });

  it("已注册、成员身份为访客、或自带 CLI 缺失时不自动注册", async () => {
    const register = installBridge(
      status({ registered: false, reason: "not_registered" }, "missing"),
    );
    renderSettings("/o/o1/settings/computers", route([]));
    const local = await screen.findByRole("region", { name: "这台电脑" });
    expect(
      await within(local).findByText(/没有找到桌面端自带的 OpenFox CLI/),
    ).toBeInTheDocument();
    expect(register).not.toHaveBeenCalled();
  });

  it("失败后卡片显示原因并可重试", async () => {
    const register = installBridge(
      status({ registered: false, reason: "not_registered" }),
      false,
    );
    renderSettings("/o/o1/settings/computers", route([]));
    const local = await screen.findByRole("region", { name: "这台电脑" });
    expect(await within(local).findByRole("alert")).toHaveTextContent(
      "连不上 OpenFox 中心，请检查网络后重试",
    );
    await userEvent.click(within(local).getByRole("button", { name: "重试" }));
    await waitFor(() => expect(register).toHaveBeenCalledTimes(2));
  });

  it("本机配置还在但服务端已删除这台电脑：提示并可强制重新连接", async () => {
    const register = installBridge(
      status({ registered: true, organization_id: "o1", device_id: "d-old" }),
    );
    renderSettings("/o/o1/settings/computers", route([device("d-other")]));
    const local = await screen.findByRole("region", { name: "这台电脑" });
    expect(
      await within(local).findByText(
        "这台电脑已从组织中移除，本机还保留着旧的连接配置。",
      ),
    ).toBeInTheDocument();
    expect(register).not.toHaveBeenCalled();
    await userEvent.click(
      within(local).getByRole("button", { name: "重新连接" }),
    );
    await waitFor(() =>
      expect(register).toHaveBeenCalledWith(TOKEN, { force: true }),
    );
  });

  it("已连接到其他组织：只提示，不自动注册", async () => {
    const register = installBridge(
      status({ registered: true, organization_id: "o9", device_id: "d9" }),
    );
    renderSettings("/o/o1/settings/computers", route([]));
    const local = await screen.findByRole("region", { name: "这台电脑" });
    expect(
      await within(local).findByText(/这台电脑已连接到其他组织/),
    ).toBeInTheDocument();
    expect(register).not.toHaveBeenCalled();
  });
});

describe("重新连接后把旧记录上的 Agent 迁到新记录（8g）", () => {
  const REPLACE = "/v1/organizations/o1/devices/d-new/replace";
  const replaceRoute =
    (reply: () => Response) =>
    (call: Call): Response | undefined =>
      call.method === "POST" && call.path === REPLACE
        ? reply()
        : route([device("d-other")])(call);
  const staleBridge = () =>
    installBridge(
      status({ registered: true, organization_id: "o1", device_id: "d-old" }),
    );
  const reconnect = async () => {
    const local = await screen.findByRole("region", { name: "这台电脑" });
    await userEvent.click(
      await within(local).findByRole("button", { name: "重新连接" }),
    );
  };
  const replaceCalls = (calls: Call[]) =>
    calls.filter((call) => call.path.endsWith("/replace"));

  it("强制重连成功后用新旧记录 ID 调用迁移接口，并提示迁走的数量", async () => {
    const register = staleBridge();
    const calls = renderSettings(
      "/o/o1/settings/computers",
      replaceRoute(() =>
        json(200, {
          device_id: "d-new",
          replaced_device_id: "d-old",
          moved_agents: 2,
          canceled_runs: 1,
        }),
      ),
    );
    await reconnect();
    await waitFor(() =>
      expect(register).toHaveBeenCalledWith(TOKEN, { force: true }),
    );
    expect(
      await screen.findByText("已把 2 个 Agent 迁到这台电脑"),
    ).toBeInTheDocument();
    expect(replaceCalls(calls)).toHaveLength(1);
    expect(replaceCalls(calls)[0]!.body).toEqual({
      replaced_device_id: "d-old",
    });
  });

  it("迁走 0 个 Agent：不额外提示", async () => {
    staleBridge();
    const calls = renderSettings(
      "/o/o1/settings/computers",
      replaceRoute(() =>
        json(200, {
          device_id: "d-new",
          replaced_device_id: "d-old",
          moved_agents: 0,
          canceled_runs: 0,
        }),
      ),
    );
    await reconnect();
    expect(await screen.findByText("已自动连接这台电脑")).toBeInTheDocument();
    await waitFor(() => expect(replaceCalls(calls)).toHaveLength(1));
    expect(screen.queryByText(/个 Agent 迁到这台电脑/)).toBeNull();
    expect(screen.queryByText(/没有迁过来/)).toBeNull();
  });

  it("404：旧记录不在这个服务器上或已处理过，静默", async () => {
    staleBridge();
    const calls = renderSettings(
      "/o/o1/settings/computers",
      replaceRoute(() => apiError(404, "NOT_FOUND")),
    );
    await reconnect();
    expect(await screen.findByText("已自动连接这台电脑")).toBeInTheDocument();
    await waitFor(() => expect(replaceCalls(calls)).toHaveLength(1));
    expect(screen.queryByText(/没有迁过来/)).toBeNull();
  });

  it("409 等其他错误：重新连接仍算成功，另外提示可以去旧电脑详情页迁移", async () => {
    staleBridge();
    renderSettings(
      "/o/o1/settings/computers",
      replaceRoute(() => apiError(409, "DEVICE_ONLINE")),
    );
    await reconnect();
    expect(await screen.findByText("已自动连接这台电脑")).toBeInTheDocument();
    expect(
      await screen.findByText(
        "旧电脑上的 Agent 没有迁过来，可以在旧电脑的详情页里迁移",
      ),
    ).toBeInTheDocument();
  });

  it("非强制注册（本机尚未注册）：不调用迁移接口", async () => {
    const register = installBridge(
      status({ registered: false, reason: "not_registered" }),
    );
    const calls = renderSettings("/o/o1/settings/computers", route([]));
    await waitFor(() =>
      expect(register).toHaveBeenCalledWith(TOKEN, { force: false }),
    );
    await screen.findByText("已自动连接这台电脑");
    expect(replaceCalls(calls)).toHaveLength(0);
  });
});

describe("旧电脑详情页「迁移到这台电脑」（8g）", () => {
  const offlineOld = (overrides: Record<string, unknown> = {}) => ({
    ...device("d-old"),
    online: false,
    runtimes: [],
    agent_count: 3,
    bound_agents: [],
    created_at: ts,
    ...overrides,
  });
  const routes =
    (old: ReturnType<typeof offlineOld>, list: Device[]) =>
    (call: Call): Response | undefined => {
      if (call.method === "GET" && call.path === "/v1/organizations/o1/devices")
        return json(200, { devices: list, next_cursor: null });
      if (
        call.method === "GET" &&
        call.path === "/v1/organizations/o1/devices/d-old"
      )
        return json(200, old);
      if (
        call.method === "GET" &&
        call.path === "/v1/organizations/o1/devices/d-new"
      )
        return json(200, {
          ...device("d-new"),
          bound_agents: [],
          created_at: ts,
        });
      return undefined;
    };
  const localNew = (org = "o1", id = "d-new") =>
    status({ registered: true, organization_id: org, device_id: id });
  const open = "/o/o1/settings/computers?computer=d-old";
  const button = () => screen.queryByRole("button", { name: "迁移到这台电脑" });

  it("满足条件：出现按钮，确认后调用接口、提示并跳转到本机详情页", async () => {
    installBridge(localNew());
    const calls = renderSettings(open, (call) => {
      if (call.method === "POST" && call.path.endsWith("/d-new/replace"))
        return json(200, {
          device_id: "d-new",
          replaced_device_id: "d-old",
          moved_agents: 3,
          canceled_runs: 0,
        });
      return routes(offlineOld(), [device("d-old"), device("d-new")])(call);
    });
    await userEvent.click(
      await screen.findByRole("button", { name: "迁移到这台电脑" }),
    );
    const dialog = await screen.findByRole("dialog", {
      name: "把 Agent 迁到这台电脑？",
    });
    expect(
      within(dialog).getByText(
        "「lin-mbp」上的 3 个 Agent 会改用这台电脑运行，旧电脑会移出本组织。旧电脑上排队或进行中的任务会被取消，需要重新发送。",
      ),
    ).toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole("button", { name: "迁移" }));
    expect(
      await screen.findByText("已把 3 个 Agent 迁到这台电脑"),
    ).toBeInTheDocument();
    const replace = calls.find((call) => call.path.endsWith("/replace"))!;
    expect(replace.path).toBe("/v1/organizations/o1/devices/d-new/replace");
    expect(replace.body).toEqual({ replaced_device_id: "d-old" });
    // 跳转到本机（新记录）的详情页
    await waitFor(() =>
      expect(
        calls.some(
          (call) =>
            call.method === "GET" &&
            call.path === "/v1/organizations/o1/devices/d-new",
        ),
      ).toBe(true),
    );
  });

  it("失败按错误码提示", async () => {
    installBridge(localNew());
    renderSettings(open, (call) => {
      if (call.method === "POST" && call.path.endsWith("/d-new/replace"))
        return apiError(409, "GRANT_SCOPE_MISMATCH");
      return routes(offlineOld(), [device("d-old"), device("d-new")])(call);
    });
    await userEvent.click(
      await screen.findByRole("button", { name: "迁移到这台电脑" }),
    );
    const dialog = await screen.findByRole("dialog");
    await userEvent.click(within(dialog).getByRole("button", { name: "迁移" }));
    expect(
      await screen.findByText(
        "两台电脑的使用范围不同，请在 Agent 设置里手动改电脑",
      ),
    ).toBeInTheDocument();
  });

  it.each([
    [
      "本机注册在其他组织",
      localNew("o9"),
      offlineOld(),
      [device("d-old"), device("d-new")],
    ],
    [
      "本机就是当前这台电脑",
      localNew("o1", "d-old"),
      offlineOld(),
      [device("d-old")],
    ],
    [
      "本机记录不在本组织电脑列表里",
      localNew(),
      offlineOld(),
      [device("d-old")],
    ],
    [
      "当前这台电脑在线",
      localNew(),
      offlineOld({ online: true }),
      [device("d-old"), device("d-new")],
    ],
    [
      "当前这台电脑不属于当前用户",
      localNew(),
      offlineOld({ owner_user_id: "u2" }),
      [device("d-old"), device("d-new")],
    ],
  ])("%s：不显示按钮", async (_name, bridgeStatus, old, list) => {
    installBridge(bridgeStatus);
    const calls = renderSettings(open, routes(old, list));
    // 离线时等「修复并重连」出现；在线时等概览加载完
    await screen.findAllByText(old.online ? "概览" : "电脑处于离线状态");
    await waitFor(() =>
      expect(
        calls.some((call) => call.path === "/v1/organizations/o1/devices"),
      ).toBe(true),
    );
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(button()).toBeNull();
  });

  it("浏览器里（没有桌面端桥接）：不显示按钮", async () => {
    renderSettings(
      open,
      routes(offlineOld(), [device("d-old"), device("d-new")]),
    );
    await screen.findByText("电脑处于离线状态");
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(button()).toBeNull();
  });
});
