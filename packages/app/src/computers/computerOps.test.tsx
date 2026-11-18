import { cleanup, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";

import type {
  Device,
  DeviceDetail,
  DeviceOrganization,
  DeviceRuntime,
} from "@openfox/api";
import { createTranslator } from "@openfox/i18n";

import {
  apiError,
  json,
  renderSettings,
  ts,
  type Call,
} from "../settings/testing";
import { effectiveMaxSlots } from "./model";
import { registrationHasOrg } from "../desktop";

// 阶段 12e · 电脑详情：密钥轮换、撤销授权、允许的组织、并发上限、配置状态与受保护目录告警。

const ORG = "/v1/organizations/o1";
const DEVICE = `${ORG}/devices/d1`;
const path = "/o/o1/settings/computers?computer=d1";

const codex: DeviceRuntime = {
  provider: "codex",
  version: "0.46.0",
  status: "READY",
  runnable: true,
  reason: null,
  login_status: "LOGGED_IN",
  models: [],
  approval_modes: ["FOLLOW_RUNTIME"],
};

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
    daemon_version: "0.50.0",
    operating_system: "linux",
    architecture: "x86_64",
    capabilities: { key_rotation: true, multi_organization: true },
    center_epoch: 1,
    version: 3,
    settings_version: 2,
    grant_scope: "PERSONAL",
    grant_user_id: "u1",
    grant_space_id: null,
    grant_space_name: null,
    permission_version: 5,
    runtimes: [codex],
    agent_count: 0,
    active_agent_count: 0,
    running_tasks: 0,
    storage: null,
    runtimes_checked_at: null,
    local_max_runs: 2,
    effective_max_slots: 2,
    reconciling_runs: 0,
    ...overrides,
  };
}

function detail(overrides: Partial<DeviceDetail> = {}): DeviceDetail {
  return {
    ...device(),
    bound_agents: [],
    created_at: ts,
    timezone: "Asia/Shanghai",
    ...overrides,
  };
}

function org(overrides: Partial<DeviceOrganization> = {}): DeviceOrganization {
  return {
    organization_id: "o1",
    slug: "lin",
    name: "林的团队",
    role: "OWNER",
    grant_status: "ACTIVE",
    grant_scope: "PERSONAL",
    current: true,
    rejoin_blocked: false,
    can_join: false,
    can_revoke: false,
    can_remove: false,
    ...overrides,
  };
}

/** 详情 + 允许的组织；其余交给各测试自己的路由 */
const routes =
  (
    value: DeviceDetail,
    extra: (call: Call) => Response | undefined = () => undefined,
    organizations: DeviceOrganization[] = [org()],
  ) =>
  (call: Call): Response | undefined => {
    const custom = extra(call);
    if (custom) return custom;
    if (call.method === "GET" && call.path === DEVICE) return json(200, value);
    if (call.method === "GET" && call.path === `${DEVICE}/organizations`)
      return json(200, { owner: value.owner_user_id === "u1", organizations });
    return undefined;
  };

const command = (overrides: Record<string, unknown> = {}) => ({
  command: {
    id: "c1",
    device_id: "d1",
    kind: "key_rotate",
    params: {},
    status: "SUCCEEDED",
    result: { key_id: "k2", previous_key_id: "k1", resumed: false },
    error_code: "",
    created_at: ts,
    delivered_at: ts,
    completed_at: ts,
    expires_at: ts,
    ...overrides,
  },
});

describe("密钥轮换", () => {
  it("所有者：先确认（说明旧密钥何时失效），再下发 key_rotate 并显示结果", async () => {
    const calls = renderSettings(
      path,
      routes(detail(), (call) =>
        call.path.startsWith(`${DEVICE}/commands`)
          ? json(call.method === "POST" ? 202 : 200, command())
          : undefined,
      ),
    );
    const button = await screen.findByRole("button", { name: "轮换密钥" });
    expect(button).toBeEnabled();
    await userEvent.click(button);
    const dialog = await screen.findByRole("dialog", {
      name: "轮换这台电脑的密钥？",
    });
    expect(
      within(dialog).getByText(
        "旧密钥在新密钥落盘后才失效，期间不会断开连接。",
      ),
    ).toBeInTheDocument();
    // 确认前不下发
    expect(calls.some((c) => c.path === `${DEVICE}/commands`)).toBe(false);
    await userEvent.click(
      within(dialog).getByRole("button", { name: "轮换密钥" }),
    );
    await waitFor(() =>
      expect(
        calls.find(
          (c) => c.method === "POST" && c.path === `${DEVICE}/commands`,
        )?.body,
      ).toEqual({ kind: "key_rotate", params: {} }),
    );
    expect(await screen.findByText("密钥已轮换")).toBeInTheDocument();
    expect(await screen.findByText("当前密钥：k2")).toBeInTheDocument();
  });

  it("命令失败：按 error_code 显示 KEY_ROTATION_FAILED 的提示", async () => {
    renderSettings(
      path,
      routes(detail(), (call) =>
        call.path.startsWith(`${DEVICE}/commands`)
          ? json(
              200,
              command({
                status: "FAILED",
                result: null,
                error_code: "KEY_ROTATION_FAILED",
              }),
            )
          : undefined,
      ),
    );
    await userEvent.click(
      await screen.findByRole("button", { name: "轮换密钥" }),
    );
    const dialog = await screen.findByRole("dialog");
    await userEvent.click(
      within(dialog).getByRole("button", { name: "轮换密钥" }),
    );
    expect(
      await screen.findByText("密钥轮换失败，旧密钥仍然有效，请稍后重试"),
    ).toBeInTheDocument();
  });

  it("中心返回能力不支持：提示升级 daemon 或用 CLI", async () => {
    renderSettings(
      path,
      routes(detail(), (call) =>
        call.method === "POST" && call.path === `${DEVICE}/commands`
          ? apiError(409, "DEVICE_CAPABILITY_UNSUPPORTED")
          : undefined,
      ),
    );
    await userEvent.click(
      await screen.findByRole("button", { name: "轮换密钥" }),
    );
    const dialog = await screen.findByRole("dialog");
    await userEvent.click(
      within(dialog).getByRole("button", { name: "轮换密钥" }),
    );
    expect(
      await screen.findByText(/当前 daemon 不支持在线轮换/),
    ).toBeInTheDocument();
  });

  it("daemon 没有 key_rotation 能力位：按钮禁用并提示升级或用 CLI", async () => {
    renderSettings(path, routes(detail({ capabilities: {} })));
    const button = await screen.findByRole("button", { name: "轮换密钥" });
    expect(button).toBeDisabled();
    expect(
      screen.getByText(
        "当前 daemon 不支持在线轮换，请升级 daemon 或在本机运行 openfox computer rotate",
      ),
    ).toBeInTheDocument();
  });

  it("非所有者：不显示轮换密钥", async () => {
    renderSettings(
      path,
      routes(detail({ owner_user_id: "u2", owner_email: "x@openfox.dev" })),
    );
    await screen.findByRole("heading", { name: "build-server-01" });
    await screen.findByRole("group", { name: "并发上限" });
    expect(screen.queryByRole("button", { name: "轮换密钥" })).toBeNull();
    expect(screen.queryByText("设备密钥")).toBeNull();
  });
});

describe("撤销授权", () => {
  it("组织所有者：菜单里有撤销授权，确认框说明 AUTHORIZATION_REVOKED，确认后调用 revoke 并离开详情", async () => {
    const calls = renderSettings(
      path,
      routes(
        detail({ owner_user_id: "u2", owner_email: "wang@openfox.dev" }),
        (call) =>
          call.method === "POST" && call.path === `${DEVICE}/revoke`
            ? json(204)
            : undefined,
      ),
    );
    await userEvent.click(
      await screen.findByRole("button", { name: "更多操作" }),
    );
    await userEvent.click(
      await screen.findByRole("menuitem", { name: "撤销授权" }),
    );
    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent("AUTHORIZATION_REVOKED");
    expect(calls.some((c) => c.path === `${DEVICE}/revoke`)).toBe(false);
    await userEvent.click(
      within(dialog).getByRole("button", { name: "撤销授权" }),
    );
    await waitFor(() =>
      expect(
        calls.some((c) => c.method === "POST" && c.path === `${DEVICE}/revoke`),
      ).toBe(true),
    );
    expect(await screen.findByText("已撤销这台电脑的授权")).toBeInTheDocument();
  });

  it("取消确认：不调用 revoke", async () => {
    const calls = renderSettings(path, routes(detail()));
    await userEvent.click(
      await screen.findByRole("button", { name: "更多操作" }),
    );
    await userEvent.click(
      await screen.findByRole("menuitem", { name: "撤销授权" }),
    );
    await userEvent.click(
      within(await screen.findByRole("dialog")).getByRole("button", {
        name: "取消",
      }),
    );
    expect(calls.some((c) => c.path === `${DEVICE}/revoke`)).toBe(false);
  });

  it("组织管理员也能撤销；普通成员（即使是电脑所有者）看不到撤销授权", async () => {
    renderSettings(path, routes(detail()), "ADMIN");
    await userEvent.click(
      await screen.findByRole("button", { name: "更多操作" }),
    );
    expect(
      await screen.findByRole("menuitem", { name: "撤销授权" }),
    ).toBeInTheDocument();
    cleanup();
    renderSettings(path, routes(detail()), "MEMBER");
    await userEvent.click(
      await screen.findByRole("button", { name: "更多操作" }),
    );
    await screen.findByRole("menuitem", { name: "从当前组织移除" });
    expect(screen.queryByRole("menuitem", { name: "撤销授权" })).toBeNull();
  });
});

describe("允许的组织", () => {
  const organizations = [
    org({ can_remove: true }),
    org({
      organization_id: "o2",
      slug: "ops",
      name: "运维组",
      role: "MEMBER",
      grant_status: null,
      grant_scope: null,
      current: false,
      can_join: true,
    }),
    org({
      organization_id: "o3",
      slug: "lab",
      name: "实验室",
      role: "ADMIN",
      current: false,
      can_revoke: true,
      can_remove: true,
    }),
  ];

  it("所有者：列出全部组织；加入另一个组织前弹信任说明，路径用目标组织", async () => {
    const calls = renderSettings(
      path,
      routes(
        detail(),
        (call) =>
          call.path === "/v1/organizations/o2/devices/d1/join"
            ? json(201, { device: device(), created: true })
            : undefined,
        organizations,
      ),
    );
    const list = await screen.findByRole("list", { name: "允许的组织" });
    const rows = within(list).getAllByRole("listitem");
    expect(rows).toHaveLength(3);
    expect(within(rows[0]!).getByText("当前")).toBeInTheDocument();
    expect(within(rows[0]!).getByText("已授权")).toBeInTheDocument();
    expect(within(rows[1]!).getByText("未加入")).toBeInTheDocument();
    // 没有 can_join 的行不显示「加入」
    expect(within(list).getAllByRole("button", { name: /^加入/ })).toHaveLength(
      1,
    );
    await userEvent.click(
      within(rows[1]!).getByRole("button", { name: "加入 运维组" }),
    );
    // 加入前弹基线的信任说明（addOrganization 变体）：必须勾选「我已了解」才能确认
    const dialog = await screen.findByRole("dialog", {
      name: "把这台电脑添加到另一个组织？",
    });
    expect(dialog).toHaveTextContent("工作目录隔离不是安全沙箱。");
    expect(dialog).toHaveTextContent(
      "共享对象触发的运行以本机用户身份执行，可读取该用户可读的文件。",
    );
    const confirm = within(dialog).getByRole("button", { name: "确认添加" });
    expect(confirm).toBeDisabled();
    expect(calls.some((c) => c.path.endsWith("/join"))).toBe(false);
    await userEvent.click(
      within(dialog).getByRole("checkbox", { name: "我已了解" }),
    );
    await userEvent.click(confirm);
    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.method === "POST" &&
            c.path === "/v1/organizations/o2/devices/d1/join",
        ),
      ).toBe(true),
    );
    expect(await screen.findByText("电脑已加入 运维组")).toBeInTheDocument();
  });
  it("一行同时可撤销与可移出：只显示「移出组织」，不显示撤销", async () => {
    renderSettings(path, routes(detail(), undefined, organizations));
    const list = await screen.findByRole("list", { name: "允许的组织" });
    const rows = within(list).getAllByRole("listitem");
    expect(
      within(rows[2]!).getByRole("button", { name: "从 实验室 移出" }),
    ).toHaveTextContent("移出组织");
    expect(within(list).queryByRole("button", { name: /^撤销/ })).toBeNull();
    // 只可撤销、不可移出的行（接口语义上保留）才显示撤销
    cleanup();
    renderSettings(
      path,
      routes(detail(), undefined, [
        org({ can_revoke: true, can_remove: false }),
      ]),
    );
    const only = await screen.findByRole("list", { name: "允许的组织" });
    expect(
      within(only).getByRole("button", { name: "撤销 林的团队 的授权" }),
    ).toBeInTheDocument();
    expect(within(only).queryByRole("button", { name: /移出$/ })).toBeNull();
  });

  it("移出另一个组织：确认后调用那个组织的 remove-from-organization", async () => {
    const calls = renderSettings(
      path,
      routes(
        detail(),
        (call) =>
          call.path ===
          "/v1/organizations/o3/devices/d1/remove-from-organization"
            ? json(200, { archived_agents: 0, canceled_runs: 0 })
            : undefined,
        organizations,
      ),
    );
    const list = await screen.findByRole("list", { name: "允许的组织" });
    await userEvent.click(
      within(list).getByRole("button", { name: "从 实验室 移出" }),
    );
    const dialog = await screen.findByRole("dialog", {
      name: "把电脑从 实验室 移出？",
    });
    expect(dialog).toHaveTextContent("Agent 会被归档");
    await userEvent.click(
      within(dialog).getByRole("button", { name: "移出组织" }),
    );
    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.method === "POST" &&
            c.path ===
              "/v1/organizations/o3/devices/d1/remove-from-organization",
        ),
      ).toBe(true),
    );
  });

  it("非所有者：只看到当前组织一行，没有加入 / 撤销 / 移出", async () => {
    renderSettings(
      path,
      routes(
        detail({ owner_user_id: "u2", owner_email: "x@openfox.dev" }),
        undefined,
        [org({ role: "MEMBER" })],
      ),
      "MEMBER",
    );
    const list = await screen.findByRole("list", { name: "允许的组织" });
    expect(within(list).getAllByRole("listitem")).toHaveLength(1);
    expect(within(list).getByText("林的团队")).toBeInTheDocument();
    expect(within(list).queryByRole("button")).toBeNull();
  });
});

describe("被管理员移除后不能自行加回", () => {
  it("所有者：该行显示「管理员已移除」且没有加入按钮", async () => {
    renderSettings(
      path,
      routes(detail(), undefined, [
        org({ can_remove: true }),
        org({
          organization_id: "o2",
          name: "运维组",
          role: "MEMBER",
          grant_status: "REVOKED",
          current: false,
          rejoin_blocked: true,
          can_join: false,
        }),
      ]),
    );
    const list = await screen.findByRole("list", { name: "允许的组织" });
    const row = within(list).getAllByRole("listitem")[1]!;
    expect(within(row).getByText("管理员已移除")).toBeInTheDocument();
    expect(within(row).queryByText("已撤销")).toBeNull();
    expect(within(row).queryByRole("button", { name: /^加入/ })).toBeNull();
  });

  it("加入时中心返回 DEVICE_REMOVED_BY_ADMIN：提示需要管理员重新允许", async () => {
    renderSettings(
      path,
      routes(
        detail(),
        (call) =>
          call.path === "/v1/organizations/o2/devices/d1/join"
            ? apiError(409, "DEVICE_REMOVED_BY_ADMIN")
            : undefined,
        [
          org(),
          org({
            organization_id: "o2",
            name: "运维组",
            role: "MEMBER",
            grant_status: null,
            grant_scope: null,
            current: false,
            can_join: true,
          }),
        ],
      ),
    );
    const list = await screen.findByRole("list", { name: "允许的组织" });
    await userEvent.click(
      within(list).getByRole("button", { name: "加入 运维组" }),
    );
    const dialog = await screen.findByRole("dialog");
    await userEvent.click(
      within(dialog).getByRole("checkbox", { name: "我已了解" }),
    );
    await userEvent.click(
      within(dialog).getByRole("button", { name: "确认添加" }),
    );
    expect(
      await screen.findByText(
        "该组织的管理员已移除这台电脑，需要管理员重新允许",
      ),
    ).toBeInTheDocument();
  });

  const removed = {
    devices: [
      {
        device_id: "d9",
        name: "gpu-box",
        owner_email: "wang@openfox.dev",
        removed_at: ts,
      },
    ],
  };

  it("电脑列表页：管理员看到被移除的电脑，点「重新允许」调用 allow-rejoin", async () => {
    let allowed = false;
    const calls = renderSettings(
      "/o/o1/settings/computers",
      (call) => {
        if (call.method === "GET" && call.path === `${ORG}/devices`)
          return json(200, { devices: [], next_cursor: null });
        if (call.path === `${ORG}/removed-devices`)
          return json(200, allowed ? { devices: [] } : removed);
        if (call.path === `${ORG}/devices/d9/allow-rejoin`) {
          allowed = true;
          return json(204);
        }
        return undefined;
      },
      "ADMIN",
    );
    const list = await screen.findByRole("list", {
      name: "被管理员移除的电脑",
    });
    expect(within(list).getByText("gpu-box")).toBeInTheDocument();
    expect(within(list).getByText(/wang@openfox\.dev/)).toBeInTheDocument();
    await userEvent.click(
      within(list).getByRole("button", { name: "重新允许 gpu-box" }),
    );
    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.method === "POST" && c.path === `${ORG}/devices/d9/allow-rejoin`,
        ),
      ).toBe(true),
    );
    expect(
      await screen.findByText("已重新允许，电脑所有者现在可以把它加入本组织"),
    ).toBeInTheDocument();
    await waitFor(() =>
      expect(
        screen.queryByRole("list", { name: "被管理员移除的电脑" }),
      ).toBeNull(),
    );
  });

  it("普通成员：不请求也不显示被移除的电脑", async () => {
    const calls = renderSettings(
      "/o/o1/settings/computers",
      (call) => {
        if (call.method === "GET" && call.path === `${ORG}/devices`)
          return json(200, { devices: [], next_cursor: null });
        if (call.path === `${ORG}/removed-devices`) return json(200, removed);
        return undefined;
      },
      "MEMBER",
    );
    await screen.findByRole("heading", { name: "电脑" });
    await waitFor(() =>
      expect(calls.some((c) => c.path === `${ORG}/devices`)).toBe(true),
    );
    expect(calls.some((c) => c.path === `${ORG}/removed-devices`)).toBe(false);
    expect(
      screen.queryByRole("list", { name: "被管理员移除的电脑" }),
    ).toBeNull();
  });
});

describe("并发上限、配置状态与告警", () => {
  it("同时显示中心上限、本机上限与生效值；概览按生效值显示", async () => {
    renderSettings(path, routes(detail()));
    const limits = await screen.findByRole("group", { name: "并发上限" });
    expect(within(limits).getByText("中心上限")).toBeInTheDocument();
    expect(within(limits).getByTestId("limit-center")).toHaveTextContent("4");
    expect(within(limits).getByText("本机上限")).toBeInTheDocument();
    expect(within(limits).getByTestId("limit-local")).toHaveTextContent("2");
    expect(within(limits).getByText("生效")).toBeInTheDocument();
    expect(within(limits).getByTestId("limit-effective")).toHaveTextContent(
      "2",
    );
    expect(screen.getByText("1 / 2")).toBeInTheDocument();
  });

  it("旧 daemon 未上报本机上限：显示 —，生效值等于中心上限", async () => {
    renderSettings(
      path,
      routes(detail({ local_max_runs: null, effective_max_slots: 4 })),
    );
    const limits = await screen.findByRole("group", { name: "并发上限" });
    expect(within(limits).getByTestId("limit-local")).toHaveTextContent("—");
    expect(within(limits).getByTestId("limit-effective")).toHaveTextContent(
      "4",
    );
  });

  it("生效并发上限的推算：后端未返回时取较小值", () => {
    expect(effectiveMaxSlots({ max_slots: 4, local_max_runs: 2 })).toBe(2);
    expect(effectiveMaxSlots({ max_slots: 4, local_max_runs: null })).toBe(4);
    expect(effectiveMaxSlots({ max_slots: 4 })).toBe(4);
    expect(
      effectiveMaxSlots({
        max_slots: 4,
        local_max_runs: 8,
        effective_max_slots: 3,
      }),
    ).toBe(3);
  });

  it("配置状态：旧 daemon 不显示", async () => {
    renderSettings(path, routes(detail({ runtime_config_status: null })));
    await screen.findByRole("group", { name: "并发上限" });
    expect(screen.queryByText("运行配置")).toBeNull();
    expect(screen.queryByText(/^已应用 v/)).toBeNull();
  });

  it.each([
    [
      "应用失败",
      { applied_version: 6, restart_required: true, error: "INVALID_POLICY" },
    ],
    ["需重启", { applied_version: 6, restart_required: true, error: null }],
    ["已应用 v7", { applied_version: 7, restart_required: false, error: null }],
  ] as const)("配置状态徽标：%s", async (label, status) => {
    renderSettings(
      path,
      routes(detail({ runtime_config_status: { ...status, checked_at: ts } })),
    );
    expect(await screen.findByText(label)).toBeInTheDocument();
    for (const other of ["应用失败", "需重启", "已应用 v7"])
      if (other !== label) expect(screen.queryByText(other)).toBeNull();
  });

  it("注册根命中受保护目录名单：显示告警与目录", async () => {
    renderSettings(
      path,
      routes(
        detail({
          capabilities: {
            key_rotation: true,
            protected_directory_roots: ["/Users/lin/.ssh", "/etc"],
          },
        }),
      ),
    );
    expect(
      await screen.findByText("部分注册目录命中受保护目录名单"),
    ).toBeInTheDocument();
    expect(screen.getByText("/Users/lin/.ssh")).toBeInTheDocument();
    expect(screen.getByText("/etc")).toBeInTheDocument();
  });

  it("没有受保护目录时不显示告警", async () => {
    renderSettings(path, routes(detail()));
    await screen.findByRole("group", { name: "并发上限" });
    expect(screen.queryByText("部分注册目录命中受保护目录名单")).toBeNull();
  });
});

describe("中英文文案", () => {
  it("并发上限与受保护目录告警有中英文", () => {
    const zh = createTranslator("zh-CN");
    const en = createTranslator("en-US");
    expect(zh("ops.limits.effective")).toBe("生效");
    expect(en("ops.limits.effective")).toBe("Effective");
    expect(en("ops.limits.center")).toBe("Center limit");
    expect(en("ops.limits.local")).toBe("Local limit");
    expect(zh("ops.orgs.remove")).toBe("移出组织");
    expect(en("ops.orgs.remove")).toBe("Remove from organization");
    expect(en("ops.orgs.title")).toBe("Allowed organizations");
    expect(zh("ops.orgs.removedByAdmin")).toBe(
      "该组织的管理员已移除这台电脑，需要管理员重新允许",
    );
    expect(en("ops.orgs.removedByAdmin")).toBe(
      "An admin of that organization removed this computer; an admin must allow it again",
    );
    expect(zh("ops.removed.allow")).toBe("重新允许");
    expect(en("ops.removed.allow")).toBe("Allow again");
    expect(zh("ops.rotate.action")).toBe("轮换密钥");
    expect(en("ops.rotate.action")).toBe("Rotate key");
    expect(en("ops.rotate.failed.KEY_ROTATION_FAILED")).toContain(
      "old key is still valid",
    );
    expect(zh("ops.revoke.action")).toBe("撤销授权");
    expect(en("ops.revoke.action")).toBe("Revoke access");
    expect(zh("ops.revoke.desc")).toContain("AUTHORIZATION_REVOKED");
    expect(en("ops.revoke.desc")).toContain("AUTHORIZATION_REVOKED");
    expect(zh("ops.config.restartRequired")).toBe("需重启");
    expect(en("ops.config.restartRequired")).toBe("Restart required");
    expect(en("ops.config.failed")).toBe("Apply failed");
    expect(en("ops.config.applied", { version: 3 })).toBe("Applied v3");
    expect(en("ops.protectedRoots.title")).toBe(
      "Some registered directories are on the protected list",
    );
  });
});

describe("本机注册的多组织判定", () => {
  it("旧版 CLI 只有 organization_id；新版带 organization_ids 时任一组织都算本机注册", () => {
    const legacy = { organization_id: "o1" };
    expect(registrationHasOrg(legacy, "o1")).toBe(true);
    expect(registrationHasOrg(legacy, "o2")).toBe(false);
    const multi = { organization_id: "o1", organization_ids: ["o1", "o2"] };
    expect(registrationHasOrg(multi, "o2")).toBe(true);
    expect(registrationHasOrg(multi, "o3")).toBe(false);
  });
});
