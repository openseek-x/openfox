import { QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { createApi, HttpClient } from "@openfox/api";
import { I18nProvider } from "@openfox/i18n";
import { ToastProvider } from "@openfox/ui";

import { agent, entry } from "../agents/fixtures";
import { AgentSettingsHost } from "../agents/settings/AgentSettingsDrawer";
import { createQueryClient } from "../App";
import { ApiProvider } from "../context";
import { CreateTaskProvider } from "../tasks/create/CreateTaskProvider";
import { SearchLauncher } from "./SearchLauncher";

// D08 / D27：搜索面板打开任务结果时在当前页打开任务抽屉（保留原查询参数），关闭后回到原页。

const ORG = "/v1/organizations/o1";

const json = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });

const task = (id: string, displayId: string, title: string) => ({
  type: "task",
  items: [
    { id, title, subtitle: "产品", space_id: "s1", display_id: displayId },
  ],
});

/** 按搜索词返回结果：「登录」→ PRD-12，「部署」→ PRD-13 */
function searchResult(q: string | null) {
  if (q === "部署") return { groups: [task("t2", "prd-13", "修复部署")] };
  return { groups: [task("t1", "prd-12", "修复登录")] };
}

function Where() {
  const location = useLocation();
  return (
    <output aria-label="路径">{location.pathname + location.search}</output>
  );
}

function setup(path: string, options: { agentDrawer?: boolean } = {}) {
  const transport = vi.fn((input: RequestInfo | URL) => {
    const url = new URL(String(input), "http://localhost");
    const p = url.pathname.replace(/^\/api/, "");
    if (p.endsWith("/search"))
      return Promise.resolve(
        json(200, searchResult(url.searchParams.get("q"))),
      );
    if (p === "/v1/me/organizations")
      return Promise.resolve(
        json(200, {
          organizations: [
            {
              id: "o1",
              slug: "o",
              name: "组织",
              role: "OWNER",
              created_at: "2026-10-20T08:00:00Z",
            },
          ],
        }),
      );
    if (p === `${ORG}/spaces`)
      return Promise.resolve(json(200, { spaces: [] }));
    if (p === `${ORG}/agent-directory`)
      return Promise.resolve(
        json(200, {
          entries: [entry("a1", { name: "熊孩子" })],
          archived_count: 0,
          truncated: false,
        }),
      );
    if (p === `${ORG}/spaces/s1/agents/a1`)
      return Promise.resolve(json(200, agent("a1", { name: "熊孩子" })));
    return Promise.resolve(
      json(404, {
        error: { code: "NOT_FOUND", message: "x", request_id: "r" },
      }),
    );
  });
  const queryClient = createQueryClient();
  queryClient.setDefaultOptions({ queries: { retry: false } });
  render(
    <I18nProvider initialLocale="zh-CN">
      <QueryClientProvider client={queryClient}>
        <ApiProvider
          api={createApi(
            new HttpClient("/api", transport as unknown as typeof fetch),
          )}
        >
          <ToastProvider>
            <MemoryRouter initialEntries={[path]}>
              <Routes>
                {/* 与 OrgLayout 一致：任务抽屉、搜索、Agent 抽屉都挂在组织路由下 */}
                <Route
                  path="/o/:orgId/*"
                  element={
                    <CreateTaskProvider orgId="o1">
                      <SearchLauncher orgId="o1" />
                      <Where />
                      {options.agentDrawer && <AgentSettingsHost orgId="o1" />}
                    </CreateTaskProvider>
                  }
                />
              </Routes>
            </MemoryRouter>
          </ToastProvider>
        </ApiProvider>
      </QueryClientProvider>
    </I18nProvider>,
  );
}

const where = () => screen.getByLabelText("路径").textContent;

/** 打开搜索面板、输入并选中第一条任务结果（click 或 Enter）。 */
async function openFromSearch(
  user: ReturnType<typeof userEvent.setup>,
  query: string,
  title: string,
  how: "click" | "enter" = "click",
) {
  await user.click(screen.getByRole("button", { name: /^搜索/ }));
  const input = await screen.findByRole("combobox", { name: "搜索关键词" });
  await user.type(input, query);
  const option = await screen.findByRole("option", {
    name: new RegExp(title),
  });
  if (how === "click") await user.click(option);
  else await user.keyboard("{Enter}");
  await waitFor(() =>
    expect(screen.queryByRole("dialog", { name: "搜索" })).toBeNull(),
  );
}

/** 当前的任务抽屉（非模态 dialog）。 */
const taskDrawer = () => screen.findByRole("dialog");

async function closeTaskDrawer(user: ReturnType<typeof userEvent.setup>) {
  const drawer = await taskDrawer();
  await user.click(within(drawer).getAllByRole("button", { name: "关闭" })[0]!);
}

afterEach(() => localStorage.clear());

describe("搜索结果打开任务：在当前页打开抽屉", () => {
  it.each([
    ["设置页 · 电脑详情", "/o/o1/settings/computers?computer=d1"],
    ["设置页 · 成员设置", "/o/o1/settings/members?role=ADMIN"],
    ["小助手", "/o/o1/assistant?conversation=c1"],
    ["Agent 对话", "/o/o1/chat/c1?run=r1"],
    ["成员页", "/o/o1/members?contact=agent%3Aa1"],
  ])("%s：保留原查询参数，关闭后回到原页", async (_, path) => {
    const user = userEvent.setup();
    setup(path);
    await openFromSearch(user, "登录", "修复登录");
    expect(where()).toBe(`${path}&task=PRD-12`);
    expect(await taskDrawer()).toBeInTheDocument();
    await closeTaskDrawer(user);
    await waitFor(() => expect(where()).toBe(path));
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("回车打开同样在当前页；没有查询参数的页面直接加 ?task=", async () => {
    const user = userEvent.setup();
    setup("/o/o1/inbox");
    await openFromSearch(user, "登录", "修复登录", "enter");
    expect(where()).toBe("/o/o1/inbox?task=PRD-12");
    await closeTaskDrawer(user);
    await waitFor(() => expect(where()).toBe("/o/o1/inbox"));
  });

  it("抽屉已开着别的任务时切换（replace），关闭一次就回到原页", async () => {
    const user = userEvent.setup();
    setup("/o/o1/members?contact=user%3Au2");
    await openFromSearch(user, "登录", "修复登录");
    expect(where()).toBe("/o/o1/members?contact=user%3Au2&task=PRD-12");
    await openFromSearch(user, "部署", "修复部署");
    expect(where()).toBe("/o/o1/members?contact=user%3Au2&task=PRD-13");
    // 只有一层抽屉
    expect(screen.getAllByRole("dialog")).toHaveLength(1);
    await closeTaskDrawer(user);
    await waitFor(() =>
      expect(where()).toBe("/o/o1/members?contact=user%3Au2"),
    );
  });

  it("在任务全页上：跳到目标任务的全页，不叠抽屉", async () => {
    const user = userEvent.setup();
    setup("/o/o1/tasks/PRD-1");
    await openFromSearch(user, "登录", "修复登录");
    expect(where()).toBe("/o/o1/tasks/PRD-12");
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("Agent 目录且 Agent 设置抽屉开着：任务抽屉叠在上面，Esc 先关任务抽屉，Agent 抽屉还在", async () => {
    const user = userEvent.setup();
    const path = "/o/o1/agents?agentSettings=a1&agentPanel=full&agentTab=tasks";
    setup(path, { agentDrawer: true });
    const agentDrawer = await screen.findByRole("complementary", {
      name: "熊孩子 的设置",
    });
    await openFromSearch(user, "登录", "修复登录");
    expect(where()).toBe(`${path}&task=PRD-12`);
    const drawer = await taskDrawer();
    // 任务抽屉在 Agent 抽屉之上（z-40 > z-30）
    expect(drawer.parentElement).toHaveClass("z-40");
    expect(agentDrawer).toHaveClass("z-30");
    // 焦点在任务抽屉里：Esc 只关任务抽屉
    drawer.focus();
    await user.keyboard("{Escape}");
    await waitFor(() => expect(where()).toBe(path));
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(
      screen.getByRole("complementary", { name: "熊孩子 的设置" }),
    ).toBeInTheDocument();
    // 再按 Esc 才关 Agent 抽屉
    await user.keyboard("{Escape}");
    await waitFor(() => expect(where()).toBe("/o/o1/agents"));
  });

  it("Agent 抽屉开着、焦点不在任务抽屉里时，Esc 不越过任务抽屉去关 Agent 抽屉", async () => {
    const user = userEvent.setup();
    const path = "/o/o1/agents?agentSettings=a1";
    setup(path, { agentDrawer: true });
    await screen.findByRole("complementary", { name: "熊孩子 的设置" });
    await openFromSearch(user, "登录", "修复登录");
    await taskDrawer();
    (document.activeElement as HTMLElement | null)?.blur();
    await user.keyboard("{Escape}");
    expect(where()).toBe(`${path}&task=PRD-12`);
    expect(
      screen.getByRole("complementary", { name: "熊孩子 的设置" }),
    ).toBeInTheDocument();
  });
});
