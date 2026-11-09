import { QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { createApi, HttpClient, type SpaceFeatures } from "@openfox/api";
import { I18nProvider } from "@openfox/i18n";

import { createQueryClient } from "../App";
import { ApiProvider } from "../context";
import { SpaceMoreMenu } from "./SpaceMoreMenu";
import { SpaceTaskNav } from "./SpaceTaskNav";

const BASE = "/o/o1/spaces/s1";
const ALL: SpaceFeatures = {
  views: true,
  projects: true,
  iterations: true,
  triage: true,
};

function Location() {
  return <output aria-label="当前路径">{useLocation().pathname}</output>;
}

function renderNav(path = `${BASE}/tasks`, features: SpaceFeatures = ALL) {
  const transport = vi.fn((input: RequestInfo | URL) => {
    const url = new URL(String(input), "http://localhost");
    const body = url.pathname.endsWith("/views")
      ? { views: [] }
      : { error: { code: "NOT_FOUND", message: "not found" } };
    return Promise.resolve(
      new Response(JSON.stringify(body), {
        status: url.pathname.endsWith("/views") ? 200 : 404,
        headers: { "Content-Type": "application/json" },
      }),
    );
  });
  const queryClient = createQueryClient();
  queryClient.setDefaultOptions({ queries: { retry: false } });
  return render(
    <I18nProvider initialLocale="zh-CN">
      <QueryClientProvider client={queryClient}>
        <ApiProvider
          api={createApi(
            new HttpClient("/api", transport as unknown as typeof fetch),
          )}
        >
          <MemoryRouter initialEntries={[path]}>
            <Routes>
              <Route
                path="*"
                element={
                  <>
                    <SpaceTaskNav orgId="o1" spaceId="s1" features={features} />
                    <SpaceMoreMenu orgId="o1" spaceId="s1" />
                    <Location />
                  </>
                }
              />
            </Routes>
          </MemoryRouter>
        </ApiProvider>
      </QueryClientProvider>
    </I18nProvider>,
  );
}

const navLinks = () =>
  screen.queryAllByRole("link").map((link) => link.textContent);

afterEach(() => localStorage.clear());

describe("侧栏「任务」下一级", () => {
  it("默认只固定迭代与视图，其余从「更多…」进入", async () => {
    const user = userEvent.setup();
    renderNav();
    expect(navLinks()).toEqual(["迭代", "视图"]);
    expect(screen.getByRole("link", { name: "迭代" })).toHaveAttribute(
      "href",
      `${BASE}/iterations`,
    );

    await user.click(screen.getByRole("button", { name: "更多…" }));
    const menu = screen.getByRole("menu", { name: "任务导航" });
    expect(
      within(menu)
        .getAllByRole("menuitem")
        .map((item) => item.textContent),
    ).toEqual(["迭代", "分拣台", "视图", "专项"]);

    await user.click(within(menu).getByRole("menuitem", { name: "分拣台" }));
    expect(screen.getByRole("status", { name: "当前路径" })).toHaveTextContent(
      `${BASE}/triage`,
    );
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    // 打开的是未固定的子页：临时显示这一项
    expect(navLinks()).toEqual(["迭代", "分拣台", "视图"]);
  });

  it("固定 / 取消固定写入本机，并按空间区分", async () => {
    const user = userEvent.setup();
    const view = renderNav();
    await user.click(screen.getByRole("button", { name: "更多…" }));
    await user.click(
      screen.getByRole("button", { name: "固定「专项」到侧栏" }),
    );
    await user.click(
      screen.getByRole("button", { name: "从侧栏取消固定「迭代」" }),
    );
    expect(navLinks()).toEqual(["视图", "专项"]);
    expect(localStorage.getItem("openfox:task-nav-pinned:s1")).toBe(
      JSON.stringify(["views", "projects"]),
    );

    view.unmount();
    renderNav();
    expect(navLinks()).toEqual(["视图", "专项"]);
  });

  it("即使功能未开启也可从菜单打开分拣台，并独立固定到侧栏", async () => {
    const user = userEvent.setup();
    renderNav(`${BASE}/tasks`, {
      views: false,
      projects: false,
      iterations: false,
      triage: false,
    });
    await user.click(screen.getByRole("button", { name: "更多…" }));
    expect(
      screen.getAllByRole("menuitem").map((item) => item.textContent),
    ).toEqual(["迭代", "分拣台", "视图", "专项"]);
    await user.click(
      screen.getByRole("button", { name: "固定「分拣台」到侧栏" }),
    );
    expect(
      screen.getByRole("button", { name: "从侧栏取消固定「分拣台」" }),
    ).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("status", { name: "当前路径" })).toHaveTextContent(
      `${BASE}/tasks`,
    );
    await user.click(screen.getByRole("menuitem", { name: "分拣台" }));
    expect(screen.getByRole("status", { name: "当前路径" })).toHaveTextContent(
      `${BASE}/triage`,
    );
    expect(screen.getByRole("link", { name: "分拣台" })).toHaveAttribute(
      "aria-current",
      "page",
    );
  });

  it("已有固定记录和回收站旧地址不会把最近删除加回导航", async () => {
    const user = userEvent.setup();
    localStorage.setItem(
      "openfox:task-nav-pinned:s1",
      JSON.stringify(["views", "trash"]),
    );
    renderNav(`${BASE}/trash`);
    expect(navLinks()).toEqual(["视图"]);
    await user.click(screen.getByRole("button", { name: "更多…" }));
    expect(
      screen.queryByRole("menuitem", { name: "最近删除" }),
    ).not.toBeInTheDocument();
  });

  it("本机记录损坏时回到默认固定项", () => {
    localStorage.setItem("openfox:task-nav-pinned:s1", "{坏数据");
    renderNav();
    expect(navLinks()).toEqual(["迭代", "视图"]);
  });
});

describe("侧栏空间「更多」", () => {
  it("列出空间设置的三个入口并跳到对应设置页", async () => {
    const user = userEvent.setup();
    renderNav();
    await user.click(screen.getByRole("button", { name: "更多" }));
    const menu = screen.getByRole("menu", { name: "空间更多操作" });
    expect(within(menu).getByText("空间设置")).toBeInTheDocument();
    expect(
      within(menu)
        .getAllByRole("menuitem")
        .map((item) => item.textContent),
    ).toEqual(["任务标签", "任务类型 & 状态", "更多设置"]);

    await user.click(
      within(menu).getByRole("menuitem", { name: "任务类型 & 状态" }),
    );
    expect(screen.getByRole("status", { name: "当前路径" })).toHaveTextContent(
      "/o/o1/settings/spaces/s1/task-types",
    );
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "更多" }));
    await user.click(screen.getByRole("menuitem", { name: "更多设置" }));
    expect(screen.getByRole("status", { name: "当前路径" })).toHaveTextContent(
      "/o/o1/settings/spaces/s1/general",
    );
  });
});
