import { act, render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { I18nProvider } from "@openfox/i18n";

import { json, renderSettings } from "../settings/testing";
import { useArtifactPanel } from "./artifacts/params";
import { ORG, chatBase, summary } from "./fixtures";
import { DraftStorageStatus } from "./WorkspacePersistence";
import {
  WorkspaceStore,
  bindWorkspaceStore,
  openWorkspaceDB,
} from "./workspaceStorage";

afterEach(() => {
  localStorage.clear();
  vi.useRealTimers();
});

describe("OPE-24 对话顶栏、本地保存状态与空会话引导", () => {
  it("对话顶栏关键控件禁止折行，Agent 名称带完整 title", async () => {
    const longAgentName = "超级无敌长的全栈前端工程师智能体名称";
    const convo = summary("c1", {
      title: "修复窄屏顶栏折行问题",
      status: "WORKING",
      agent: {
        id: "a1",
        name: longAgentName,
        title: "前端",
        avatar: null,
        online: true,
      },
      task: null,
      run: {
        id: "r1",
        state: "RUNNING",
        wait_reason: "",
        started_at: new Date().toISOString(),
        finished_at: null,
      },
    });
    renderSettings(
      "/o/o1/chat/c1",
      chatBase((call) => {
        if (call.path === `${ORG}/conversations`)
          return json(200, { conversations: [convo], next_cursor: null });
        if (call.path === `${ORG}/conversations/c1`) return json(200, convo);
        if (call.path === `${ORG}/conversations/c1/messages`)
          return json(200, { messages: [], next_cursor: 0 });
        return undefined;
      }),
    );

    const main = await screen.findByRole("main");
    const agentName = await within(main).findByText(longAgentName);
    expect(agentName).toHaveAttribute("title", longAgentName);
    const settingsBtn = within(main).getByRole("button", { name: "设置" });
    expect(settingsBtn.className).toContain("whitespace-nowrap");
    expect(settingsBtn.className).toContain("shrink-0");
    const statusBadge = within(main).getByText(/工作中/);
    expect(statusBadge.className).toContain("whitespace-nowrap");
    expect(statusBadge.className).toContain("shrink-0");
  });

  it("空会话挂载不向本地存储写入空 workspace 记录", async () => {
    const store = new WorkspaceStore(
      JSON.stringify(["u-empty", "o-empty"]),
      await openWorkspaceDB(),
    );
    await store.hydrate();
    bindWorkspaceStore(store);

    function Harness() {
      const panel = useArtifactPanel("c-never-saved");
      return <output>{panel.open ? "open" : "closed"}</output>;
    }

    render(
      <MemoryRouter initialEntries={["/o/o1/chat/c-never-saved"]}>
        <Harness />
      </MemoryRouter>,
    );

    expect(screen.getByRole("status")).toHaveTextContent("closed");
    expect(store.read("workspace:c-never-saved")).toBeUndefined();
    expect(store.status).toBe("saved");
    await store.close();
  });

  it("DraftStorageStatus 忽略视图态保存提示，仅在真实草稿保存后短显「已保存到本机」并自动淡出", async () => {
    const store = new WorkspaceStore(
      JSON.stringify(["u-status", "o-status"]),
      await openWorkspaceDB(),
    );
    await store.hydrate();
    bindWorkspaceStore(store);

    render(
      <I18nProvider initialLocale="zh-CN">
        <DraftStorageStatus />
      </I18nProvider>,
    );

    // 视图态写入不显示「正在保存到本机…」
    act(() => {
      store.write("workspace:c1", { tabs: ["list"], selected: "list" });
    });
    expect(store.status).toBe("saving");
    expect(store.hasDirtyContent).toBe(false);
    expect(screen.queryByRole("status")).not.toBeInTheDocument();

    await act(async () => {
      await store.flush();
    });
    expect(screen.queryByText("已保存到本机")).not.toBeInTheDocument();

    // 真实草稿写入才显示保存中与保存完成提示
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    act(() => {
      store.write("draft:c1", {
        body: "用户输入的草稿",
        picked: [],
        files: [],
      });
    });
    expect(store.hasDirtyContent).toBe(true);
    expect(screen.getByRole("status")).toHaveTextContent("正在保存到本机…");

    // 真实刷盘完成：先恢复真实计时器清理待写状态，再观察完成态自动淡出
    vi.useRealTimers();
    await act(async () => {
      await store.flush();
    });
    expect(screen.getByRole("status")).toHaveTextContent("已保存到本机");

    // 1.5s 后自动隐藏（再次写入并模拟完成以测试定时器）
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    act(() => {
      store.write("draft:c1", { body: "第二版草稿", picked: [], files: [] });
    });
    expect(screen.getByRole("status")).toHaveTextContent("正在保存到本机…");
    act(() => {
      // 清空待刷队列避免 fake timer 下触发真实 IDB 事务
      (store as unknown as { dirty: Set<string> }).dirty.clear();
      store.status = "saved";
      store.listeners.forEach((fn) => fn());
    });
    expect(screen.getByRole("status")).toHaveTextContent("已保存到本机");

    act(() => {
      vi.advanceTimersByTime(1600);
    });
    expect(screen.queryByRole("status")).not.toBeInTheDocument();

    // 失败/冲突始终以 alert 提示
    act(() => {
      store.status = "failed";
      store.listeners.forEach((fn) => fn());
    });
    expect(screen.getByRole("alert")).toHaveTextContent(
      "草稿尚未保存到本机，请保持窗口打开并重试。可能是存储空间不足。",
    );

    vi.useRealTimers();
    await store.close();
  });

  it("对话面板空态只显示提示文案，新建入口统一留在标题栏", async () => {
    renderSettings(
      "/o/o1/chat",
      chatBase((call) => {
        if (call.path === `${ORG}/conversations`)
          return json(200, { conversations: [], next_cursor: null });
        return undefined;
      }),
    );
    const nav = await screen.findByRole("navigation", { name: "对话" });
    const emptyText = await within(nav).findByText("暂无 Agent 会话");
    expect(emptyText).toBeVisible();
    expect(
      within(nav).getAllByRole("button", { name: "新建对话" }),
    ).toHaveLength(1);
  });
});
