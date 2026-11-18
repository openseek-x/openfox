import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type {
  ConversationWorkspaceOptions,
  ConversationWorkspaceSelection,
} from "@openfox/api";
import { agent } from "../agents/fixtures";
import { json, renderSettings } from "../settings/testing";
import { ORG, chatBase, message, summary } from "./fixtures";

afterEach(() => {
  vi.unstubAllGlobals();
  localStorage.clear();
});
const defaults: ConversationWorkspaceOptions = {
  device_id: "d1",
  directories: ["source", "docs"],
  git_directories: ["source"],
  supported: true,
  can_reuse: false,
  can_set_baseline: false,
  can_manage_directory: false,
  recent_directories: ["source/old"],
};

function setup(
  path: string,
  options: Partial<ConversationWorkspaceOptions> = {},
) {
  const available = { ...defaults, ...options };
  let chosen: ConversationWorkspaceSelection | undefined;
  return renderSettings(
    path,
    chatBase((call) => {
      if (call.path === ORG + "/conversation-workspace-options")
        return json(200, available);
      if (
        call.path === ORG + "/conversation-workspace-recents" &&
        call.method === "DELETE"
      ) {
        available.recent_directories = [];
        return new Response(null, { status: 204 });
      }
      if (call.path === ORG + "/assistant")
        return json(200, {
          assistant: agent("as1", {
            name: "小助手",
            builtin_kind: "ASSISTANT",
          }),
        });
      if (call.path === ORG + "/assistant/sessions")
        return json(200, { sessions: [] });
      if (call.path === ORG + "/conversations" && call.method === "POST") {
        const input = call.body as {
          id: string;
          workspace_selection?: ConversationWorkspaceSelection;
        };
        chosen = input.workspace_selection;
        return json(201, summary(input.id, { workspace_selection: chosen }));
      }
      if (call.path === ORG + "/conversations")
        return json(200, { conversations: [], next_cursor: null });
      if (call.path.endsWith("/messages"))
        return call.method === "POST"
          ? json(201, message(1, { body: "检查" }))
          : json(200, { messages: [], next_cursor: 0 });
      const detail = /\/conversations\/([^/]+)$/.exec(call.path);
      if (detail)
        return json(200, summary(detail[1]!, { workspace_selection: chosen }));
      return undefined;
    }),
  );
}
const chip = () => screen.findByRole("button", { name: "工作目录" });
async function openManual(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await chip());
  const manual = await screen.findByRole("radio", { name: /手动指定工作目录/ });
  await waitFor(() => expect(manual).toBeEnabled());
  await user.click(manual);
}
describe("会话工作目录", () => {
  it.each(["/o/o1/chat?agent=a2", "/o/o1/assistant?session=new"])(
    "网页草稿 %s：只列获准根、提交 Git 基线，建成后锁定",
    async (path) => {
      const user = userEvent.setup(),
        calls = setup(path);
      await openManual(user);
      expect(screen.queryByText("去修改")).toBeNull();
      expect(
        screen.queryByPlaceholderText("输入绝对路径，回车确认"),
      ).toBeNull();
      const select = screen.getByRole("combobox", {
        name: "选择这台电脑上的文件夹",
      });
      expect(
        within(select)
          .getAllByRole("option")
          .map((o) => o.textContent),
      ).toEqual(["选择这台电脑上的文件夹", "source", "docs"]);
      await user.selectOptions(select, "source");
      const modal = await screen.findByRole("dialog", {
        name: "如何使用这个目录",
      });
      expect(
        within(modal).getByRole("button", { name: /直接在该目录运行/ }),
      ).toHaveAttribute("aria-pressed", "true");
      await user.click(
        within(modal).getByRole("button", { name: /Worktree 隔离运行/ }),
      );
      await user.selectOptions(
        within(modal).getByRole("combobox", { name: "基线分支选择" }),
        "manual",
      );
      await user.type(
        within(modal).getByRole("textbox", { name: /基线分支/ }),
        "release",
      );
      await user.click(within(modal).getByRole("button", { name: "确定" }));
      await user.type(
        screen.getByRole("textbox", { name: "输入消息" }),
        "检查{Enter}",
      );
      await waitFor(() =>
        expect(
          calls.find(
            (c) => c.method === "POST" && c.path === ORG + "/conversations",
          )?.body,
        ).toMatchObject({
          workspace_selection: {
            mode: "WORKTREE",
            directory: "source",
            base_ref: "release",
          },
        }),
      );
      await waitFor(() =>
        expect(
          calls.find((c) => c.method === "POST" && c.path.endsWith("/messages"))
            ?.body,
        ).toMatchObject({
          mentions: [{ agent_id: path.includes("assistant") ? "as1" : "a2" }],
        }),
      );
      await waitFor(async () =>
        expect(await chip()).toHaveAttribute("aria-disabled", "true"),
      );
      await user.click(await chip());
      expect(screen.queryByRole("dialog", { name: "设置工作目录" })).toBeNull();
    },
  );

  it("最近使用移除、非 Git 禁用 worktree，切回默认后创建不带覆盖", async () => {
    const user = userEvent.setup(),
      calls = setup("/o/o1/chat?agent=a1");
    await openManual(user);
    await user.click(screen.getByRole("button", { name: "从最近使用中移除" }));
    await waitFor(() => expect(screen.queryByText("old")).toBeNull());
    expect(calls.find((c) => c.method === "DELETE")?.body).toEqual({
      directory: "source/old",
    });
    await user.selectOptions(
      screen.getByRole("combobox", { name: "选择这台电脑上的文件夹" }),
      "docs",
    );
    expect(await chip()).toHaveTextContent("docs");
    expect(
      screen.queryByRole("dialog", { name: "如何使用这个目录" }),
    ).toBeNull();
    await user.click(await chip());
    expect(
      await screen.findByRole("radio", { name: /Worktree/ }),
    ).toBeDisabled();
    await user.click(
      screen.getByRole("radio", { name: /跟随 Agent 默认设置/ }),
    );
    await user.type(
      screen.getByRole("textbox", { name: "输入消息" }),
      "检查{Enter}",
    );
    await waitFor(() =>
      expect(
        calls.find(
          (c) => c.method === "POST" && c.path === ORG + "/conversations",
        ),
      ).toBeDefined(),
    );
    expect(
      calls.find(
        (c) => c.method === "POST" && c.path === ORG + "/conversations",
      )?.body,
    ).not.toHaveProperty("workspace_selection");
  });

  it("本机路径由登记设备桥检查，拒绝的路径不保存，允许的子目录只按逻辑名提交", async () => {
    const resolve = vi
      .fn()
      .mockResolvedValueOnce({ ok: false, reason: "outside_allowed_roots" })
      .mockResolvedValueOnce({
        ok: true,
        directory: "docs/sub",
        path: "/tmp/approved/sub",
        git: false,
        worktree: false,
      });
    vi.stubGlobal("openfoxDesktop", {
      resolveWorkDirectory: resolve,
      getLocalStatus: async () => ({
        registration: {
          registered: true,
          organization_id: "o1",
          device_id: "d1",
        },
        daemon: { state: "RUNNING" },
        runtime: { runtime: "codex", runnable: true },
      }),
    });
    const user = userEvent.setup(),
      calls = setup("/o/o1/chat?agent=a1");
    await openManual(user);
    const field = screen.getByPlaceholderText("输入绝对路径，回车确认");
    await user.type(field, "relative{Enter}");
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "工作目录必须是绝对路径",
    );
    expect(resolve).not.toHaveBeenCalled();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog", { name: "设置工作目录" })).toBeNull();
    await openManual(user);
    await user.type(
      screen.getByPlaceholderText("输入绝对路径，回车确认"),
      "/tmp/denied{Enter}",
    );
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "请选择这台电脑上已登记",
    );
    await user.click(screen.getByRole("button", { name: "浏览" }));
    await waitFor(async () =>
      expect(await chip()).toHaveAttribute(
        "title",
        "工作目录：/tmp/approved/sub",
      ),
    );
    expect(
      screen.queryByRole("dialog", { name: "如何使用这个目录" }),
    ).toBeNull();
    await user.type(
      screen.getByRole("textbox", { name: "输入消息" }),
      "检查{Enter}",
    );
    await waitFor(() =>
      expect(
        calls.find(
          (c) => c.method === "POST" && c.path === ORG + "/conversations",
        )?.body,
      ).toMatchObject({
        workspace_selection: { mode: "DIRECT", directory: "docs/sub" },
      }),
    );
    expect(JSON.stringify(calls.map((c) => c.body))).not.toContain("/tmp/");
    expect(resolve).toHaveBeenCalledWith(
      expect.objectContaining({ agentId: "a1", deviceId: "d1", browse: true }),
    );
  });

  it("点最近使用的 Git 目录直接沿用串行，再打开可切换 worktree", async () => {
    const user = userEvent.setup(),
      calls = setup("/o/o1/chat?agent=a1", { recent_directories: ["source"] });
    await openManual(user);
    await user.click(screen.getByRole("button", { name: "source" }));
    expect(await chip()).toHaveTextContent("source");
    expect(
      screen.queryByRole("dialog", { name: "如何使用这个目录" }),
    ).toBeNull();
    await user.click(await chip());
    const mode = await screen.findByRole("radio", { name: /Worktree/ });
    await user.click(mode);
    await user.selectOptions(
      screen.getByRole("combobox", { name: "基线分支选择" }),
      "manual",
    );
    await user.type(
      screen.getByRole("textbox", { name: "基线分支" }),
      "refs/tags/no",
    );
    expect(screen.getByRole("textbox", { name: "基线分支" })).toHaveAttribute(
      "aria-invalid",
      "true",
    );
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog", { name: "设置工作目录" })).toBeNull();
    await user.type(
      screen.getByRole("textbox", { name: "输入消息" }),
      "检查{Enter}",
    );
    await waitFor(() =>
      expect(
        calls.find(
          (c) => c.method === "POST" && c.path === ORG + "/conversations",
        )?.body,
      ).toMatchObject({
        workspace_selection: { mode: "WORKTREE", directory: "source" },
      }),
    );
  });

  it("可管理的独立 Agent 从去修改直接定位到工作目录所在的运行设置", async () => {
    const user = userEvent.setup();
    setup("/o/o1/chat?agent=a1", { can_manage_directory: true });
    await user.click(await chip());
    await user.click(await screen.findByRole("button", { name: "去修改" }));
    expect(
      await screen.findByRole("region", { name: "运行设置" }),
    ).toBeVisible();
  });

  it("无获准根说明原因，只有服务端给出管理权限时提供去修改", async () => {
    const user = userEvent.setup();
    setup("/o/o1/assistant?session=new", {
      directories: [],
      can_manage_directory: true,
    });
    await user.click(await chip());
    expect(
      await screen.findByText("这台电脑还没有此 Agent 可以使用的已登记目录。"),
    ).toBeVisible();
    expect(
      screen.getByRole("radio", { name: /手动指定工作目录/ }),
    ).toBeDisabled();
    expect(screen.getByRole("button", { name: "去修改" })).toBeVisible();
  });
});
