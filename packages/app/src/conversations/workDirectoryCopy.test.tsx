import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { agent } from "../agents/fixtures";
import { json, renderSettings } from "../settings/testing";
import { ORG, TS, chatBase, summary } from "./fixtures";

afterEach(() => {
  vi.unstubAllGlobals();
  localStorage.clear();
});

function setup(bridge?: object, empty = false) {
  if (bridge)
    vi.stubGlobal("openfoxDesktop", {
      getLocalStatus: async () => ({
        daemon: { state: "STOPPED" },
        runtime: { runtime: "codex", runnable: false },
        registration: { registered: false, reason: "not_registered" },
      }),
      ...bridge,
    });
  const assistant = agent("as1", {
    name: "小助手",
    builtin_kind: "ASSISTANT",
    visibility: "PRIVATE",
  });
  const sessions = empty
    ? []
    : ["c1", "c2"].map((id) => ({
        id,
        space_id: "s1",
        agent_id: "as1",
        title: `会话 ${id}`,
        lifecycle: "OPEN",
        version: 1,
        created_at: TS,
        updated_at: TS,
      }));
  return renderSettings(
    empty ? "/o/o1/assistant?session=new" : "/o/o1/assistant?session=c1",
    chatBase((call) => {
      if (call.path === `${ORG}/assistant`) return json(200, { assistant });
      if (call.path === `${ORG}/assistant/sessions`)
        return json(200, { sessions });
      if (
        [`${ORG}/conversations/c1`, `${ORG}/conversations/c2`].includes(
          call.path,
        )
      )
        return json(
          200,
          summary(call.path.endsWith("c1") ? "c1" : "c2", { agent_id: "as1" }),
        );
      return undefined;
    }),
  );
}
async function openMenu() {
  const user = userEvent.setup();
  await user.click(await screen.findByRole("button", { name: "更多操作" }));
  return {
    user,
    item: await screen.findByRole("menuitem", { name: "复制工作目录路径" }),
  };
}

describe("真实工作目录复制入口", () => {
  it.each([undefined, {}])(
    "浏览器和旧桌面端不可用，不伪装已完成",
    async (bridge) => {
      setup(bridge);
      const { item } = await openMenu();
      expect(item).toHaveAttribute("aria-disabled", "true");
    },
  );
  it("没有会话不能查询目录", async () => {
    const copyWorkDirectory = vi.fn();
    setup(
      {
        copyWorkDirectory,
        cancelWorkDirectoryCopy: vi.fn().mockResolvedValue(undefined),
      },
      true,
    );
    const { item } = await openMenu();
    expect(item).toHaveAttribute("aria-disabled", "true");
    expect(copyWorkDirectory).not.toHaveBeenCalled();
  });
  it("只传会话标识，成功后通知，不向网页返回或猜测路径", async () => {
    const copyWorkDirectory = vi.fn().mockResolvedValue({ ok: true });
    setup({
      copyWorkDirectory,
      cancelWorkDirectoryCopy: vi.fn().mockResolvedValue(undefined),
    });
    const { user, item } = await openMenu();
    await user.click(item);
    await screen.findByText("已复制工作目录路径");
    expect(copyWorkDirectory).toHaveBeenCalledWith({
      orgId: "o1",
      conversationId: "c1",
      requestId: expect.any(String),
    });
  });
  it.each([
    ["offline", "执行电脑当前离线"],
    [
      "workspace_unavailable",
      "没有可用的实际工作目录记录，目录可能尚未准备好或已被清理",
    ],
    ["clipboard_failed", "无法写入剪贴板"],
    ["forbidden", "需要有权查看会话，并且是执行电脑的所有者"],
    ["timeout", "电脑响应超时，请重试"],
  ])("%s 显示原因而不是复制成功", async (reason, message) => {
    setup({
      copyWorkDirectory: vi.fn().mockResolvedValue({ ok: false, reason }),
      cancelWorkDirectoryCopy: vi.fn().mockResolvedValue(undefined),
    });
    const { user, item } = await openMenu();
    await user.click(item);
    await screen.findByText(message);
    expect(screen.queryByText("已复制工作目录路径")).not.toBeInTheDocument();
  });
  it("待处理时禁止重复点击，切换会话取消并忽略迟到结果", async () => {
    let finish!: (result: { ok: true }) => void;
    const copyWorkDirectory = vi.fn(
      () =>
        new Promise<{ ok: true }>((resolve) => {
          finish = resolve;
        }),
    );
    const cancelWorkDirectoryCopy = vi.fn().mockResolvedValue(undefined);
    setup({ copyWorkDirectory, cancelWorkDirectoryCopy });
    const { user, item } = await openMenu();
    await user.click(item);
    const reopened = await openMenu();
    expect(reopened.item).toHaveAttribute("aria-disabled", "true");
    await user.keyboard("{Escape}");
    const header = screen
      .getByRole("button", { name: "更多操作" })
      .closest("header")!;
    await user.click(within(header).getByRole("button", { name: "会话记录" }));
    await user.click(await screen.findByText("会话 c2"));
    await waitFor(() => expect(cancelWorkDirectoryCopy).toHaveBeenCalled());
    finish({ ok: true });
    await waitFor(() =>
      expect(screen.queryByText("已复制工作目录路径")).not.toBeInTheDocument(),
    );
  });
});
