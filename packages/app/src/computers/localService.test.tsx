import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { DeviceDetail } from "@openfox/api";
import type {
  DesktopLocalStatus,
  DesktopServiceSettings,
  DesktopMachineProxy,
} from "../desktop";
import { json, renderSettings, ts, type Call } from "../settings/testing";
import { isLocalDevice } from "./localRegistration";

const centerUrl = "https://center.example/base";
const target = { orgId: "o1", deviceId: "d1", centerUrl };
const status: DesktopLocalStatus = {
  daemon: { state: "RUNNING" },
  runtime: { runtime: "codex", runnable: true },
  registration: {
    registered: true,
    organization_id: "o1",
    device_id: "d1",
    center_url: centerUrl,
  },
};
const device: DeviceDetail = {
  id: "d1",
  grant_id: "g1",
  owner_user_id: "u1",
  owner_email: "lin@openfox.dev",
  name: "local-mac",
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
  bound_agents: [],
  created_at: ts,
  timezone: null,
};
function route(owner = "u1") {
  return (call: Call) => {
    if (call.path.endsWith("/devices/center"))
      return json(200, { center_url: centerUrl });
    if (call.path.endsWith("/devices/d1"))
      return json(200, { ...device, owner_user_id: owner });
    if (call.path.endsWith("/devices"))
      return json(200, { devices: [device], next_cursor: null });
    return undefined;
  };
}
function bridge(overrides: Record<string, unknown> = {}) {
  let settings: DesktopServiceSettings = {
    supported: true,
    installed: true,
    startup: "login",
    keep_alive: true,
  };
  const value = {
    platform: "darwin",
    getLocalStatus: vi.fn(async () => status),
    daemonAction: vi.fn(),
    pickDirectory: vi.fn(),
    getLocalService: vi.fn(async () => settings),
    setLocalStartup: vi.fn(
      async (input: { mode: "login" | "manual" }) =>
        (settings = { ...settings, startup: input.mode }),
    ),
    localDaemonAction: vi.fn(async () => ({
      ...status,
      daemon: { state: "STOPPED" },
    })),
    readLocalLogs: vi.fn(async (input: { cursor?: string }) => ({
      lines: [input.cursor ? "safe older event" : "safe newest event"],
      next_cursor: input.cursor ? null : "cursor1",
      reset: Boolean(input.cursor),
    })),
    ...overrides,
  };
  (globalThis as { openfoxDesktop?: unknown }).openfoxDesktop = value;
  return value;
}
afterEach(() => {
  delete (globalThis as { openfoxDesktop?: unknown }).openfoxDesktop;
});

describe("本机机器代理", () => {
  const initial: DesktopMachineProxy = {
    revision: 0,
    scope_token: "a".repeat(64),
    mode: "DISABLED",
    desktop_supported: true,
    source_state: "not_following",
  };
  it("保存独立代理使用版本和本机身份，状态不冒充连通通过", async () => {
    const saveMachineProxy = vi.fn(async (value) => ({
      ...initial,
      revision: 1,
      mode: value.mode,
      effective: value.effective,
      runtime: { loaded_revision: 1, connection_revision: 0 },
    }));
    bridge({ getMachineProxy: vi.fn(async () => initial), saveMachineProxy });
    renderSettings("/o/o1/settings/computers?computer=d1", route());
    const row = (await screen.findByText("机器代理")).closest(
      "div.flex",
    )!.parentElement!;
    await userEvent.click(
      await within(row).findByRole("button", { name: "设置" }),
    );
    const dialog = within(await screen.findByRole("dialog"));
    await userEvent.click(dialog.getByRole("radio", { name: "独立代理" }));
    await userEvent.type(
      dialog.getByRole("textbox", { name: "代理服务器地址" }),
      "proxy.example",
    );
    await userEvent.click(dialog.getByRole("button", { name: "保存" }));
    await waitFor(() =>
      expect(saveMachineProxy).toHaveBeenCalledWith({
        ...target,
        expected_revision: 0,
        scope_token: initial.scope_token,
        mode: "CUSTOM",
        effective: {
          mode: "proxy",
          protocol: "http",
          host: "proxy.example",
          port: 7890,
        },
      }),
    );
    expect(
      await screen.findByText(
        "配置已同步；当前中心连接等待自然重连，实际连通未验证。",
      ),
    ).toBeInTheDocument();
  });
  it("不支持来源保留最后快照，冲突不关闭草稿或显示保存成功", async () => {
    const current = {
      ...initial,
      revision: 4,
      mode: "FOLLOW_DESKTOP" as const,
      desktop_supported: false,
      source_state: "unsupported" as const,
      effective: { mode: "direct" as const },
    };
    bridge({
      getMachineProxy: vi.fn(async () => current),
      saveMachineProxy: vi.fn(async () => {
        throw Error("machine_proxy_conflict");
      }),
    });
    renderSettings("/o/o1/settings/computers?computer=d1", route());
    const row = (await screen.findByText("机器代理")).closest(
      "div.flex",
    )!.parentElement!;
    expect(
      await screen.findByText("桌面来源未同步，继续使用最后保存的快照。"),
    ).toBeInTheDocument();
    await userEvent.click(
      await within(row).findByRole("button", { name: "设置" }),
    );
    const dialog = within(await screen.findByRole("dialog"));
    expect(
      dialog.getByRole("radio", { name: "跟随本机桌面代理" }),
    ).toBeDisabled();
    expect(dialog.getByText(/最后保存的快照/)).toBeInTheDocument();
    await userEvent.click(dialog.getByRole("radio", { name: "禁用机器接管" }));
    await userEvent.click(dialog.getByRole("button", { name: "保存" }));
    expect(await dialog.findByRole("alert")).toHaveTextContent("未能保存");
    expect(screen.getByRole("dialog")).toBeInTheDocument();
  });
  it("非 owner 不读取本机代理 endpoint", async () => {
    const read = vi.fn(async () => initial);
    bridge({ getMachineProxy: read, saveMachineProxy: vi.fn() });
    renderSettings("/o/o1/settings/computers?computer=d1", route("another"));
    await screen.findByText("local-mac");
    expect(screen.queryByText("机器代理")).not.toBeInTheDocument();
    expect(read).not.toHaveBeenCalled();
  });
});

describe("本机详情服务控制", () => {
  it("按设备、组织及完整中心地址严格识别本机", () => {
    expect(
      isLocalDevice(status, "o1", "d1", "https://CENTER.example:443/base/"),
    ).toBe(true);
    for (const [org, id, url] of [
      ["o2", "d1", centerUrl],
      ["o1", "remote", centerUrl],
      ["o1", "d1", "https://center.example/other"],
      ["o1", "d1", ""],
      ["o1", "d1", "https://user:pass@center.example/base"],
    ]) {
      expect(isLocalDevice(status, org!, id!, url)).toBe(false);
    }
    expect(
      isLocalDevice(
        {
          ...status,
          registration: {
            registered: true,
            organization_id: "o1",
            device_id: "d1",
          },
        },
        "o1",
        "d1",
        centerUrl,
      ),
    ).toBe(false);
  });
  it("启动方式切换持久化并保留停止确认，失败不显示成功", async () => {
    const local = bridge();
    renderSettings("/o/o1/settings/computers?computer=d1", route());
    const select = await screen.findByRole("combobox", { name: "启动方式" });
    await userEvent.click(select);
    await userEvent.click(
      await screen.findByRole("option", { name: "手动启动" }),
    );
    await waitFor(() =>
      expect(local.setLocalStartup).toHaveBeenCalledWith({
        ...target,
        mode: "manual",
      }),
    );
    expect(select).toHaveTextContent("手动启动");
    local.setLocalStartup.mockRejectedValueOnce(Error("failed"));
    await userEvent.click(select);
    await userEvent.click(
      await screen.findByRole("option", { name: "登录系统时自动启动" }),
    );
    expect(
      await screen.findByText(
        "无法读取或修改本机服务，请检查 CLI 与注册状态后重试。",
      ),
    ).toBeInTheDocument();
    await waitFor(() => expect(select).toHaveTextContent("手动启动"));
    await userEvent.click(screen.getByRole("button", { name: "停止服务" }));
    const confirm = await screen.findByRole("dialog");
    expect(local.localDaemonAction).not.toHaveBeenCalled();
    await userEvent.click(
      within(confirm).getByRole("button", { name: "取消" }),
    );
    expect(local.localDaemonAction).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "停止服务" }));
    await userEvent.click(
      within(await screen.findByRole("dialog")).getByRole("button", {
        name: "停止服务",
      }),
    );
    await waitFor(() =>
      expect(local.localDaemonAction).toHaveBeenCalledWith({
        ...target,
        action: "stop",
      }),
    );
  });
  it("日志按游标翻页、轮转提示及重新读取最新页", async () => {
    const local = bridge();
    renderSettings("/o/o1/settings/computers?computer=d1", route());
    await userEvent.click(
      await screen.findByRole("button", { name: "查看日志" }),
    );
    const dialog = await screen.findByRole("dialog", { name: "运行日志" });
    expect(
      await within(dialog).findByText("safe newest event"),
    ).toBeInTheDocument();
    await userEvent.click(
      within(dialog).getByRole("button", { name: "更早日志" }),
    );
    expect(
      await within(dialog).findByText("safe older event"),
    ).toBeInTheDocument();
    expect(local.readLocalLogs).toHaveBeenLastCalledWith({
      ...target,
      cursor: "cursor1",
    });
    expect(within(dialog).getByRole("status")).toHaveTextContent(
      "日志已轮转或截断",
    );
    expect(
      within(dialog).getByRole("button", { name: "更早日志" }),
    ).toBeDisabled();
    await userEvent.click(
      within(dialog).getByRole("button", { name: "最新日志" }),
    );
    expect(
      await within(dialog).findByText("safe newest event"),
    ).toBeInTheDocument();
    await userEvent.click(
      within(dialog).getAllByRole("button", { name: "关闭" })[0]!,
    );
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    local.readLocalLogs.mockRejectedValueOnce(Error("read failed"));
    await userEvent.click(screen.getByRole("button", { name: "查看日志" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "无法读取或修改本机服务",
    );
  });
  it.each([
    [
      "远程设备",
      {
        getLocalStatus: async () => ({
          ...status,
          registration: {
            registered: true,
            organization_id: "o1",
            device_id: "remote",
            center_url: centerUrl,
          },
        }),
      },
      "u1",
    ],
    [
      "其他中心",
      {
        getLocalStatus: async () => ({
          ...status,
          registration: {
            registered: true,
            organization_id: "o1",
            device_id: "d1",
            center_url: "https://other.example",
          },
        }),
      },
      "u1",
    ],
    ["旧桥接", { getLocalService: undefined }, "u1"],
    ["非 macOS", { platform: "linux" }, "u1"],
    ["非所有者", {}, "someone"],
  ] as const)("%s 不显示控制及日志入口", async (_name, overrides, owner) => {
    const local = bridge(overrides);
    renderSettings("/o/o1/settings/computers?computer=d1", route(owner));
    await screen.findByText("启动方式");
    await waitFor(() =>
      expect(screen.queryByText("加载中…")).not.toBeInTheDocument(),
    );
    expect(
      screen.queryByRole("combobox", { name: "启动方式" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "查看日志" }),
    ).not.toBeInTheDocument();
    expect(local.setLocalStartup).not.toHaveBeenCalled();
    expect(local.readLocalLogs).not.toHaveBeenCalled();
  });
});
