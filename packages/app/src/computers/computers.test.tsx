import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";

import type { Device, DeviceDetail, DeviceRuntime } from "@openfox/api";

import {
  apiError,
  json,
  renderSettings,
  ts,
  type Call,
} from "../settings/testing";
import {
  computerStatus,
  railStatus,
  sortComputers,
  visibilityText,
} from "./model";

const codex = (runnable = true): DeviceRuntime => ({
  provider: "codex",
  version: "0.46.0",
  status: runnable ? "READY" : "NEEDS_LOGIN",
  runnable,
  reason: null,
  login_status: runnable ? "LOGGED_IN" : "LOGGED_OUT",
  models: [],
  approval_modes: ["FOLLOW_RUNTIME"],
});

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
    reserved_slots: 1,
    last_seen_at: ts,
    protocol_major: 1,
    protocol_minor: 0,
    daemon_version: "0.42.1",
    operating_system: "linux",
    architecture: "x86_64",
    capabilities: {},
    center_epoch: 1,
    version: 3,
    settings_version: 2,
    grant_scope: "ORGANIZATION",
    grant_user_id: null,
    grant_space_id: null,
    grant_space_name: null,
    permission_version: 5,
    runtimes: [codex()],
    agent_count: 2,
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
    bound_agents: [
      {
        id: "a1",
        name: "熊孩子",
        title: "AI Agent开发工程师",
        space_id: "s1",
        online: true,
        owner_email: "lin@openfox.dev",
        visibility: "SHARED",
      },
    ],
    created_at: ts,
    timezone: "Asia/Shanghai",
    ...overrides,
  };
}

const listRoute =
  (devices: Device[]) =>
  (call: Call): Response | undefined => {
    if (call.method === "GET" && call.path === "/v1/organizations/o1/devices")
      return json(200, { devices, next_cursor: null });
    return undefined;
  };

afterEach(() => {
  delete (globalThis as { openfoxDesktop?: unknown }).openfoxDesktop;
});

describe("电脑模型", () => {
  it("状态：在线且有可运行 Runtime / 在线无可运行 Runtime / 离线", () => {
    expect(computerStatus(device())).toBe("online");
    expect(computerStatus(device({ runtimes: [] }))).toBe("noRuntime");
    expect(computerStatus(device({ runtimes: [codex(false)] }))).toBe(
      "noRuntime",
    );
    expect(computerStatus(device({ online: false }))).toBe("offline");
  });

  it("侧栏状态点：至少一台可用为绿，有在线但 Runtime 不可用为黄，否则为灰", () => {
    expect(
      railStatus([
        device({ online: false }),
        device({ runtimes: [] }),
        device(),
      ]),
    ).toBe("online");
    expect(
      railStatus([device({ online: false }), device({ runtimes: [] })]),
    ).toBe("noRuntime");
    expect(railStatus([device({ online: false })])).toBe("offline");
    expect(railStatus([])).toBe("offline");
  });

  it("排序：自己的在前 → 所有者 → 状态 → 名称", () => {
    const list = [
      device({ id: "1", name: "b", owner_user_id: "u2", owner_email: "a@x" }),
      device({ id: "2", name: "z", owner_user_id: "u1", online: false }),
      device({ id: "3", name: "a", owner_user_id: "u2", owner_email: "a@x" }),
      device({
        id: "4",
        name: "a",
        owner_user_id: "u2",
        owner_email: "a@x",
        online: false,
      }),
      device({ id: "5", name: "c", owner_user_id: "u3", owner_email: "0@x" }),
    ];
    expect(sortComputers(list, "u1").map((item) => item.id)).toEqual([
      "2",
      "5",
      "3",
      "1",
      "4",
    ]);
  });

  it("可见性文案：私有 / 组织 / 空间名", () => {
    const t = (key: string) => key;
    expect(
      visibilityText(device({ grant_scope: "PERSONAL" }), t as never),
    ).toBe("computers.visibility.private");
    expect(
      visibilityText(
        device({ grant_scope: "SPACE", grant_space_name: "默认空间" }),
        t as never,
      ),
    ).toBe("默认空间");
  });
});

describe("电脑设置页 · 列表", () => {
  it("按共享 / 私有分区，渲染状态、Runtime 与可见性；网页端不显示本地分区", async () => {
    renderSettings(
      "/o/o1/settings/computers",
      listRoute([
        device(),
        device({
          id: "d2",
          name: "gpu-box",
          architecture: "arm64",
          runtimes: [],
          grant_scope: "SPACE",
          grant_space_id: "s1",
          grant_space_name: "默认空间",
          owner_user_id: "u2",
          owner_email: "xiao@openfox.dev",
        }),
        device({
          id: "d3",
          name: "林晓的 Windows 台式机",
          operating_system: "windows",
          architecture: "x64",
          online: false,
          grant_scope: "PERSONAL",
          grant_user_id: "u1",
        }),
      ]),
    );
    await screen.findByText("build-server-01");
    const shared = screen.getByRole("region", { name: "共享" });
    const rows = within(shared).getAllByRole("row").slice(1);
    expect(rows).toHaveLength(2);
    expect(within(rows[0]!).getByText("build-server-01")).toBeInTheDocument();
    expect(within(rows[0]!).getByText("Linux (x86_64)")).toBeInTheDocument();
    expect(within(rows[0]!).getByText("在线")).toBeInTheDocument();
    expect(within(rows[0]!).getByText("组织")).toBeInTheDocument();
    expect(within(rows[1]!).getByText("gpu-box")).toBeInTheDocument();
    // Runtime 列与状态列都显示「Runtime 未检测到」
    expect(within(rows[1]!).getAllByText("Runtime 未检测到")).toHaveLength(2);
    expect(within(rows[1]!).getByText("默认空间")).toBeInTheDocument();

    const personal = screen.getByRole("region", { name: "私有" });
    expect(
      within(personal).getByText("林晓的 Windows 台式机"),
    ).toBeInTheDocument();
    expect(within(personal).getByText("Windows (x64)")).toBeInTheDocument();
    expect(within(personal).getByText("离线")).toBeInTheDocument();
    const personalRow = within(personal).getAllByRole("row")[1]!;
    expect(within(personalRow).getByText("私有")).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "本地" })).toBeNull();
    // 连接新电脑已开放
    expect(screen.getByRole("button", { name: "连接新电脑" })).toBeEnabled();
  });

  it("没有电脑时显示两个分区的空态", async () => {
    renderSettings("/o/o1/settings/computers", listRoute([]));
    expect(await screen.findByText("还没有共享远程电脑")).toBeInTheDocument();
    expect(screen.getByText("还没有私有远程电脑")).toBeInTheDocument();
  });

  it("桌面端显示本机卡片，停止服务前确认，调用桥接 stop", async () => {
    const actions: string[] = [];
    const running = {
      daemon: { state: "RUNNING" },
      runtime: { runtime: "codex", runnable: true },
    };
    (globalThis as { openfoxDesktop?: unknown }).openfoxDesktop = {
      getLocalStatus: () => Promise.resolve(running),
      daemonAction: (action: string) => {
        actions.push(action);
        return Promise.resolve({
          daemon: { state: "STOPPED" },
          runtime: running.runtime,
        });
      },
      pickDirectory: () => Promise.resolve({ ok: false, reason: "cancelled" }),
    };
    renderSettings("/o/o1/settings/computers", listRoute([]));
    const local = await screen.findByRole("region", { name: "这台电脑" });
    expect(await within(local).findByText("可用")).toBeInTheDocument();
    expect(
      within(local).getByText("正在此处运行 · 1 个 Runtime 可执行任务。"),
    ).toBeInTheDocument();
    await userEvent.click(
      within(local).getByRole("button", { name: "停止服务" }),
    );
    const dialog = await screen.findByRole("dialog", {
      name: "停止 OpenFox 守护进程服务？",
    });
    expect(actions).toEqual([]);
    await userEvent.click(
      within(dialog).getByRole("button", { name: "停止服务" }),
    );
    await waitFor(() => expect(actions).toEqual(["stop"]));
    expect(await within(local).findByText("已停止")).toBeInTheDocument();
    expect(within(local).getByRole("button", { name: "启动" })).toBeEnabled();
  });

  it("点击行进入详情整页", async () => {
    const calls = renderSettings("/o/o1/settings/computers", (call) => {
      if (call.path === "/v1/organizations/o1/devices/d1")
        return json(200, detail());
      return listRoute([device()])(call);
    });
    await userEvent.click(await screen.findByText("build-server-01"));
    expect(
      await screen.findByRole("heading", { name: "build-server-01" }),
    ).toBeInTheDocument();
    expect(
      calls.some((c) => c.path === "/v1/organizations/o1/devices/d1"),
    ).toBe(true);
  });
});

describe("电脑详情整页", () => {
  const path = "/o/o1/settings/computers?computer=d1";

  it("在线：显示 CLI 说明、概览、Runtime、Agent 与电脑信息；未检测到的 Runtime 不出现", async () => {
    renderSettings(path, (call) =>
      call.path === "/v1/organizations/o1/devices/d1"
        ? json(200, detail())
        : undefined,
    );
    expect(
      await screen.findByRole("heading", { name: "build-server-01" }),
    ).toBeInTheDocument();
    expect(screen.getByText("openfox daemon restart")).toBeInTheDocument();
    expect(screen.getByText("可用 1 / 1")).toBeInTheDocument();
    expect(screen.getByText("在线 2 / 2")).toBeInTheDocument();
    expect(screen.getByText("1 / 4")).toBeInTheDocument();
    expect(screen.getByText("Codex")).toBeInTheDocument();
    // 其他三种 Runtime 未上报时显示「未检测到」与各自的安装指南入口
    expect(screen.queryByText("支持更多 Runtime")).toBeNull();
    expect(screen.getByText("Claude Code")).toBeInTheDocument();
    expect(screen.getByText("OpenCode")).toBeInTheDocument();
    expect(screen.getByText("pi")).toBeInTheDocument();
    expect(screen.getAllByText("未检测到")).toHaveLength(3);
    expect(screen.getByText("熊孩子")).toBeInTheDocument();
    expect(screen.getByText("0.42.1")).toBeInTheDocument();
    expect(screen.getByText("Asia/Shanghai")).toBeInTheDocument();
    // 未上报存储时显示空态
    expect(
      screen.getByText("电脑还没有上报存储统计（每 10 分钟统计一次）。"),
    ).toBeInTheDocument();
  });

  it("离线：显示离线提示与最后活跃，「修复并重连」禁用；codex 未上报时可打开安装指南", async () => {
    renderSettings(path, (call) =>
      call.path === "/v1/organizations/o1/devices/d1"
        ? json(200, detail({ online: false, runtimes: [] }))
        : undefined,
    );
    expect(await screen.findByText("电脑处于离线状态")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "修复并重连" })).toBeDisabled();
    expect(screen.getByText("最后活跃")).toBeInTheDocument();
    await userEvent.click(
      screen.getByRole("button", { name: "查看安装指南 · Codex" }),
    );
    const guide = await screen.findByRole("dialog", { name: "手动安装指南" });
    expect(
      within(guide).getByText("npm install -g @openai/codex"),
    ).toBeInTheDocument();
    await userEvent.click(
      within(guide).getAllByRole("button", { name: "关闭" }).at(-1)!,
    );
    await userEvent.click(
      screen.getByRole("button", { name: "查看安装指南 · Claude Code" }),
    );
    const claudeGuide = await screen.findByRole("dialog", {
      name: "手动安装指南",
    });
    expect(
      within(claudeGuide).getByText("npm install -g @anthropic-ai/claude-code"),
    ).toBeInTheDocument();
  });

  it("重命名：PATCH 带 settings_version；409 时提示并重新拉取", async () => {
    let gets = 0;
    let patches = 0;
    const calls = renderSettings(path, (call) => {
      if (call.path !== "/v1/organizations/o1/devices/d1") return undefined;
      if (call.method === "GET") {
        gets += 1;
        return json(200, detail());
      }
      if (call.method === "PATCH") {
        patches += 1;
        return patches === 1
          ? json(200, device({ name: "mac-mini-studio", settings_version: 3 }))
          : apiError(409, "VERSION_CONFLICT");
      }
      return undefined;
    });
    await screen.findByRole("heading", { name: "build-server-01" });
    await userEvent.click(screen.getByRole("button", { name: "重命名" }));
    const input = screen.getByRole("textbox", { name: "电脑名称" });
    await userEvent.clear(input);
    await userEvent.type(input, "mac-mini-studio{Enter}");
    await waitFor(() => expect(patches).toBe(1));
    const first = calls.find((c) => c.method === "PATCH");
    expect(first?.body).toEqual({
      name: "mac-mini-studio",
      settings_version: 2,
    });
    expect(
      await screen.findByRole("heading", { name: "mac-mini-studio" }),
    ).toBeInTheDocument();

    // 第二次改名：服务端版本冲突
    const before = gets;
    await userEvent.click(screen.getByRole("button", { name: "重命名" }));
    const again = screen.getByRole("textbox", { name: "电脑名称" });
    await userEvent.clear(again);
    await userEvent.type(again, "other{Enter}");
    await waitFor(() => expect(patches).toBe(2));
    expect(calls.filter((c) => c.method === "PATCH")[1]?.body).toEqual({
      name: "other",
      settings_version: 3,
    });
    expect(
      await screen.findByText("内容已被他人修改，已刷新"),
    ).toBeInTheDocument();
    await waitFor(() => expect(gets).toBeGreaterThan(before));
  });

  it("最大并发：所有者可改，PATCH 带 settings_version", async () => {
    const calls = renderSettings(path, (call) => {
      if (call.path !== "/v1/organizations/o1/devices/d1") return undefined;
      if (call.method === "PATCH")
        return json(200, device({ max_slots: 8, settings_version: 3 }));
      return json(200, detail());
    });
    const input = await screen.findByRole("textbox", {
      name: "最大并发运行任务数",
    });
    await userEvent.clear(input);
    await userEvent.type(input, "8{Enter}");
    await waitFor(() =>
      expect(calls.find((c) => c.method === "PATCH")?.body).toEqual({
        max_slots: 8,
        settings_version: 2,
      }),
    );
  });

  it("移除机器：有阻断 Agent 时列出名字并禁用确认", async () => {
    const calls = renderSettings(path, (call) => {
      if (call.path === "/v1/organizations/o1/devices/d1/removal-blockers")
        return json(200, {
          agents: [
            { id: "a1", name: "熊孩子" },
            { id: "a2", name: "代码审查员" },
          ],
          count: 2,
        });
      if (call.path === "/v1/organizations/o1/devices/d1")
        return json(200, detail());
      return undefined;
    });
    await screen.findByRole("heading", { name: "build-server-01" });
    await userEvent.click(screen.getByRole("button", { name: "更多操作" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "移除机器" }));
    const dialog = await screen.findByRole("dialog", { name: "移除机器" });
    expect(
      await within(dialog).findByText("2 个活跃 Agent 阻止移除。"),
    ).toBeInTheDocument();
    expect(within(dialog).getByText("熊孩子、代码审查员")).toBeInTheDocument();
    expect(
      within(dialog).getByRole("button", { name: "移除机器" }),
    ).toBeDisabled();
    expect(calls.some((c) => c.method === "DELETE")).toBe(false);
  });

  it("移除机器：没有阻断时调用 DELETE 并回到列表", async () => {
    const calls = renderSettings(path, (call) => {
      if (call.path === "/v1/organizations/o1/devices/d1/removal-blockers")
        return json(200, { agents: [], count: 0 });
      if (call.path === "/v1/organizations/o1/devices/d1")
        return call.method === "DELETE" ? json(204) : json(200, detail());
      return listRoute([])(call);
    });
    await screen.findByRole("heading", { name: "build-server-01" });
    await userEvent.click(screen.getByRole("button", { name: "更多操作" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "移除机器" }));
    const dialog = await screen.findByRole("dialog", { name: "移除机器" });
    const confirm = within(dialog).getByRole("button", { name: "移除机器" });
    await waitFor(() => expect(confirm).toBeEnabled());
    await userEvent.click(confirm);
    expect(await screen.findByText("已移除机器")).toBeInTheDocument();
    expect(calls.some((c) => c.method === "DELETE")).toBe(true);
    expect(await screen.findByText("还没有共享远程电脑")).toBeInTheDocument();
  });

  it("从当前组织移除：确认后调用接口并提示归档与取消数量", async () => {
    renderSettings(path, (call) => {
      if (
        call.method === "POST" &&
        call.path === "/v1/organizations/o1/devices/d1/remove-from-organization"
      )
        return json(200, {
          archived_agents: 2,
          canceled_runs: 1,
          affected_tasks: 1,
        });
      if (call.path === "/v1/organizations/o1/devices/d1")
        return json(200, detail());
      return listRoute([])(call);
    });
    await screen.findByRole("heading", { name: "build-server-01" });
    await userEvent.click(screen.getByRole("button", { name: "更多操作" }));
    await userEvent.click(
      screen.getByRole("menuitem", { name: "从当前组织移除" }),
    );
    const dialog = await screen.findByRole("dialog", {
      name: "从当前组织移除这台电脑？",
    });
    await userEvent.click(
      within(dialog).getByRole("button", { name: "从当前组织移除" }),
    );
    expect(
      await screen.findByText(
        "已从当前组织移除：归档 2 个 Agent，取消 1 个运行",
      ),
    ).toBeInTheDocument();
  });

  it("解绑全部：确认按钮为「解绑全部」，调用 unbind-agents", async () => {
    const calls = renderSettings(path, (call) => {
      if (call.path === "/v1/organizations/o1/devices/d1/unbind-agents")
        return json(200, {
          archived_agents: 2,
          canceled_runs: 0,
          affected_tasks: 0,
        });
      if (call.path === "/v1/organizations/o1/devices/d1")
        return json(200, detail());
      return undefined;
    });
    await screen.findByRole("heading", { name: "build-server-01" });
    await userEvent.click(screen.getByRole("button", { name: "解绑全部" }));
    const dialog = await screen.findByRole("dialog", {
      name: "解绑全部 Agent 运行时？",
    });
    await userEvent.click(
      within(dialog).getByRole("button", { name: "解绑全部" }),
    );
    await waitFor(() =>
      expect(
        calls.find(
          (c) => c.path === "/v1/organizations/o1/devices/d1/unbind-agents",
        )?.body,
      ).toEqual({}),
    );
    expect(
      await screen.findByText("已解绑 2 个 Agent，取消 0 个运行"),
    ).toBeInTheDocument();
  });

  it("可见性：改为组织 / 指定空间时的请求体", async () => {
    const calls = renderSettings(path, (call) => {
      if (call.path === "/v1/organizations/o1/spaces")
        return json(200, {
          spaces: [
            {
              id: "s1",
              prefix: "DEF",
              name: "默认空间",
              visibility: "shared",
              version: 1,
              created_at: ts,
            },
          ],
        });
      if (call.path === "/v1/organizations/o1/devices/d1/scope")
        return json(
          200,
          device({
            grant_scope: "SPACE",
            grant_space_id: "s1",
            permission_version: 6,
          }),
        );
      if (call.path === "/v1/organizations/o1/devices/d1")
        return json(
          200,
          detail({ grant_scope: "PERSONAL", grant_user_id: "u1" }),
        );
      return undefined;
    });
    await screen.findByRole("heading", { name: "build-server-01" });
    await userEvent.click(screen.getByRole("button", { name: "可见性：私有" }));
    await userEvent.click(
      await screen.findByRole("menuitemradio", { name: /默认空间/ }),
    );
    await waitFor(() =>
      expect(
        calls.find((c) => c.path === "/v1/organizations/o1/devices/d1/scope")
          ?.body,
      ).toEqual({ scope: "SPACE", space_id: "s1", version: 5 }),
    );
  });

  it("可见性：非所有者只读，不显示重命名", async () => {
    renderSettings(path, (call) =>
      call.path === "/v1/organizations/o1/devices/d1"
        ? json(
            200,
            detail({ owner_user_id: "u2", owner_email: "x@openfox.dev" }),
          )
        : undefined,
    );
    await screen.findByRole("heading", { name: "build-server-01" });
    expect(screen.queryByRole("button", { name: "重命名" })).toBeNull();
    expect(screen.queryByRole("button", { name: /可见性：/ })).toBeNull();
    expect(screen.queryByRole("button", { name: "解绑全部" })).toBeNull();
    expect(
      screen.getByRole("textbox", { name: "最大并发运行任务数" }),
    ).toBeDisabled();
  });
});

describe("侧栏电脑入口", () => {
  const cases: Array<[string, Device[], string]> = [
    ["绿", [device({ online: false }), device({ id: "d2" })], "online"],
    ["黄", [device({ runtimes: [codex(false)] })], "noRuntime"],
    ["灰", [device({ online: false })], "offline"],
  ];
  it.each(cases)("状态点为%s", async (_label, devices, status) => {
    renderSettings("/o/o1/inbox", listRoute(devices));
    await waitFor(() =>
      expect(screen.getByRole("button", { name: /^电脑：/ })).toHaveAttribute(
        "data-status",
        status,
      ),
    );
  });

  it("点击打开电脑列表，点击电脑打开只读详情抽屉", async () => {
    renderSettings("/o/o1/inbox", (call) => {
      if (call.path === "/v1/organizations/o1/devices/d3")
        return json(
          200,
          detail({
            id: "d3",
            name: "Mac mini（家里）",
            operating_system: "darwin",
            grant_scope: "PERSONAL",
            online: false,
          }),
        );
      return listRoute([
        device(),
        device({
          id: "d3",
          name: "Mac mini（家里）",
          operating_system: "darwin",
          grant_scope: "PERSONAL",
          online: false,
        }),
      ])(call);
    });
    const button = await screen.findByRole("button", { name: /^电脑：/ });
    await userEvent.click(button);
    const popover = await screen.findByRole("dialog", { name: "电脑列表" });
    expect(
      within(popover).getByRole("region", { name: "共享" }),
    ).toBeInTheDocument();
    const personal = within(popover).getByRole("region", { name: "私有" });
    expect(within(popover).queryByRole("region", { name: "本机" })).toBeNull();
    await userEvent.click(within(personal).getByText("Mac mini（家里）"));
    const drawer = await screen.findByRole("dialog", { name: "电脑详情" });
    expect(
      await within(drawer).findByText("电脑处于离线状态"),
    ).toBeInTheDocument();
    expect(
      within(drawer).getByRole("button", { name: "修复并重连" }),
    ).toBeDisabled();
    expect(screen.queryByRole("dialog", { name: "电脑列表" })).toBeNull();
  });

  it("没有电脑时弹层显示空态", async () => {
    renderSettings("/o/o1/inbox", listRoute([]));
    await userEvent.click(
      await screen.findByRole("button", { name: /^电脑：/ }),
    );
    expect(await screen.findByText("暂无可用电脑")).toBeInTheDocument();
  });
});
