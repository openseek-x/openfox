import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";

import type { DeviceCommand, DeviceDetail } from "@openfox/api";
import { agent, device } from "../agents/fixtures";
import {
  apiError,
  json,
  renderSettings,
  type Call,
  type Route,
} from "../settings/testing";
import { ORG, TS, chatBase } from "./fixtures";

afterEach(() => localStorage.clear());

const devicePath = `${ORG}/devices/d2`;
const storage = {
  total_bytes: 4096,
  worktree_bytes: 2048,
  worktree_count: 2,
  codex_home_bytes: 1024,
  journal_bytes: 1024,
  measured_at: TS,
  partial: false,
  categories: [{ key: "conversation", bytes: 2048, count: 2 }],
};

function setup({
  extra = () => undefined,
  overrides = {},
  bound = true,
}: {
  extra?: Route;
  overrides?: Partial<DeviceDetail>;
  bound?: boolean;
} = {}) {
  const assistant = agent("as1", {
    name: "小助手",
    builtin_kind: "ASSISTANT",
  });
  assistant.config = bound
    ? { ...assistant.config!, computer_id: "d2", grant_id: "g-d2" }
    : undefined;
  const detail: DeviceDetail = {
    ...device("d2"),
    storage,
    bound_agents: [],
    created_at: TS,
    timezone: null,
    ...overrides,
  };
  const commands = new Map<string, DeviceCommand>();
  return renderSettings(
    "/o/o1/assistant?session=new",
    chatBase((call) => {
      const custom = extra(call);
      if (custom) return custom;
      if (call.path === `${ORG}/assistant`) return json(200, { assistant });
      if (call.path === `${ORG}/assistant/sessions`)
        return json(200, { sessions: [] });
      if (call.path === devicePath) return json(200, detail);
      const commandId = call.path.replace(`${devicePath}/commands/`, "");
      if (call.method === "GET" && commands.has(commandId))
        return json(200, { command: commands.get(commandId) });
      if (call.path === `${devicePath}/commands` && call.method === "POST") {
        const input = call.body as Pick<DeviceCommand, "kind" | "params">;
        const preview = input.params.action === "preview";
        const command: DeviceCommand = {
          id:
            input.kind === "device_settings"
              ? "policy"
              : preview
                ? "preview"
                : "clean",
          device_id: "d2",
          ...input,
          status: "SUCCEEDED",
          error_code: "",
          created_at: TS,
          delivered_at: TS,
          completed_at: TS,
          expires_at: TS,
          result:
            input.kind === "device_settings"
              ? {
                  version: 1,
                  policy: {
                    enabled: false,
                    interval_hours: 1,
                    retain_days: 7,
                    repo_days: 37,
                    min_free_gib: 0,
                    max_gib: 0,
                  },
                }
              : preview
                ? { preview_id: "preview-d2", worktrees: 2, bytes: 2048 }
                : { freed_bytes: 2048 },
        };
        commands.set(command.id, command);
        return json(202, { command });
      }
      return undefined;
    }),
  );
}

async function openStorage() {
  const user = userEvent.setup();
  await user.click(await screen.findByRole("button", { name: "更多操作" }));
  await user.click(
    await screen.findByRole("menuitem", { name: "工作目录存储管理…" }),
  );
  const dialog = await screen.findByRole("dialog", {
    name: "工作目录存储管理",
  });
  return { user, dialog: within(dialog) };
}

const cleanupCalls = (calls: Call[]) =>
  calls.filter(
    (call) =>
      call.method === "POST" &&
      (call.body as { kind?: string })?.kind === "storage_cleanup",
  );

describe("小助手存储管理", () => {
  it("仅打开当前绑定电脑的统计，不查询其他电脑或执行清理", async () => {
    const calls = setup();
    expect(calls.some((call) => call.path === devicePath)).toBe(false);
    const { user, dialog } = await openStorage();
    expect(await dialog.findByText("电脑 d2")).toBeInTheDocument();
    expect(dialog.getByText("存储总占用")).toBeInTheDocument();
    expect(dialog.getByText(/不仅限于此对话/)).toBeInTheDocument();
    expect(calls.some((call) => call.path === `${ORG}/devices/d1`)).toBe(false);
    expect(
      calls.filter((call) => call.path.includes("/commands")),
    ).toHaveLength(0);
    await user.keyboard("{Escape}");
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
  });

  it("预览后才发送带同一预览凭据的清理请求", async () => {
    const calls = setup();
    const { user, dialog } = await openStorage();
    await user.click(await dialog.findByRole("button", { name: "管理存储" }));
    const manager = within(
      await screen.findByRole("dialog", { name: "管理存储" }),
    );
    await manager.findByRole("checkbox", { name: "启用自动清理" });
    expect(cleanupCalls(calls)).toHaveLength(0);
    await user.click(manager.getByRole("button", { name: "按时间清理" }));
    const filter = within(
      await screen.findByRole("dialog", { name: "按时间清理" }),
    );
    await user.click(filter.getByRole("button", { name: "预览" }));
    await filter.findByText(/可清理 2 个工作区/);
    expect(cleanupCalls(calls)).toHaveLength(1);
    expect(cleanupCalls(calls)[0]?.body).toEqual({
      kind: "storage_cleanup",
      params: {
        action: "preview",
        days: 0,
        categories: ["task", "conversation", "automation", "workflow"],
      },
    });
    await user.click(filter.getByRole("button", { name: "开始清理" }));
    await waitFor(() => expect(cleanupCalls(calls)).toHaveLength(2));
    expect(cleanupCalls(calls)[1]).toMatchObject({
      path: `${devicePath}/commands`,
      body: {
        kind: "storage_cleanup",
        params: {
          action: "clean",
          preview_id: "preview-d2",
          days: 0,
          categories: ["task", "conversation", "automation", "workflow"],
        },
      },
    });
    await waitFor(() =>
      expect(
        screen.queryByRole("dialog", { name: "按时间清理" }),
      ).not.toBeInTheDocument(),
    );
    expect(
      screen.getByRole("dialog", { name: "工作目录存储管理" }),
    ).toBeInTheDocument();
  });

  it("非电脑所有者仅查看统计，不能打开清理操作", async () => {
    const calls = setup({ overrides: { owner_user_id: "u2" } });
    const { dialog } = await openStorage();
    expect(
      await dialog.findByRole("button", { name: "管理存储" }),
    ).toBeDisabled();
    expect(dialog.getByText("存储总占用")).toBeInTheDocument();
    expect(
      calls.filter((call) => call.path.includes("/commands")),
    ).toHaveLength(0);
  });

  it("关闭预览仅退出上层弹框，不发送清理请求", async () => {
    const calls = setup();
    const { user, dialog } = await openStorage();
    await user.click(await dialog.findByRole("button", { name: "管理存储" }));
    await user.click(await screen.findByRole("button", { name: "按时间清理" }));
    await user.click(await screen.findByRole("button", { name: "预览" }));
    await screen.findByText(/可清理 2 个工作区/);
    await user.keyboard("{Escape}");
    expect(
      screen.queryByRole("dialog", { name: "按时间清理" }),
    ).not.toBeInTheDocument();
    expect(
      screen.getByRole("dialog", { name: "工作目录存储管理" }),
    ).toBeInTheDocument();
    expect(cleanupCalls(calls)).toHaveLength(1);
    expect(document.body.style.overflow).toBe("hidden");
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(document.body.style.overflow).not.toBe("hidden");
  });

  it("服务端拒绝预览时显示错误，不提供确认清理", async () => {
    const calls = setup({
      extra: (call) =>
        call.method === "POST" &&
        (call.body as { kind?: string })?.kind === "storage_cleanup"
          ? apiError(404, "NOT_FOUND")
          : undefined,
    });
    const { user, dialog } = await openStorage();
    await user.click(await dialog.findByRole("button", { name: "管理存储" }));
    await user.click(await screen.findByRole("button", { name: "按时间清理" }));
    await user.click(await screen.findByRole("button", { name: "预览" }));
    expect(
      await screen.findByText("电脑不存在，或你不是这台电脑的所有者"),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "开始清理" }),
    ).not.toBeInTheDocument();
    expect(cleanupCalls(calls)).toHaveLength(1);
  });

  it("离线设备仍展示最近统计，但不允许筛选清理", async () => {
    const calls = setup({ overrides: { online: false } });
    const { user, dialog } = await openStorage();
    await user.click(await dialog.findByRole("button", { name: "管理存储" }));
    await user.click(await screen.findByRole("button", { name: "按时间清理" }));
    expect(await screen.findByRole("button", { name: "预览" })).toBeDisabled();
    expect(
      calls.filter((call) => call.path.includes("/commands")),
    ).toHaveLength(0);
  });

  it("没有统计时显示未上报，不伪造空闲空间", async () => {
    setup({ overrides: { storage: null } });
    const { dialog } = await openStorage();
    expect(
      await dialog.findByText(/电脑还没有上报存储统计/),
    ).toBeInTheDocument();
    expect(dialog.queryByText("存储总占用")).not.toBeInTheDocument();
  });

  it.each([404, 503])(
    "设备读取失败 %s 可以重试，期间不展示清理控件",
    async (status) => {
      let fail = true;
      setup({
        extra: (call) =>
          call.path === devicePath && fail
            ? apiError(status, status === 404 ? "NOT_FOUND" : "UNAVAILABLE")
            : undefined,
      });
      const { user, dialog } = await openStorage();
      await dialog.findByRole("alert");
      expect(
        dialog.queryByRole("button", { name: "管理存储" }),
      ).not.toBeInTheDocument();
      fail = false;
      await user.click(dialog.getByRole("button", { name: "重试" }));
      expect(await dialog.findByText("电脑 d2")).toBeInTheDocument();
    },
  );

  it("未绑定电脑时禁用入口，并保留真实原因", async () => {
    const calls = setup({ bound: false });
    await userEvent.click(
      await screen.findByRole("button", { name: "更多操作" }),
    );
    expect(
      await screen.findByRole("menuitem", { name: /工作目录存储管理/ }),
    ).toHaveAttribute("aria-disabled", "true");
    expect(calls.some((call) => call.path === devicePath)).toBe(false);
  });
});
