import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { App, createQueryClient } from "../../App";
import {
  backend,
  json,
  makeTask,
  ORG,
  SPACE,
  type Override,
} from "../test-backend";

function renderTaskDetail(
  taskOverrides: Record<string, unknown> = {},
  extra?: Override,
) {
  const task = {
    ...makeTask(1, "st1", taskOverrides),
    description_reactions: [],
  };
  const server = backend((call) => {
    const custom = extra?.(call);
    if (custom) return custom;
    if (call.method === "GET" && call.path.endsWith("/by-display-id/OPEN-1"))
      return json(200, task);
    if (call.method === "PATCH" && call.path.endsWith("/tasks/task-1"))
      return json(200, { ...task, ...call.body!, version: task.version + 1 });
    return undefined;
  });
  const queryClient = createQueryClient();
  queryClient.setDefaultOptions({ queries: { retry: false } });
  render(
    <MemoryRouter initialEntries={[`/o/${ORG}/tasks/OPEN-1`]}>
      <App api={server.api} queryClient={queryClient} locale="zh-CN" />
    </MemoryRouter>,
  );
  return server;
}

beforeEach(() => {
  localStorage.setItem(`openfox:onboarding:${ORG}`, JSON.stringify({}));
});

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  localStorage.clear();
});

describe("任务详情行内编辑框（无内部滚动条自动撑开）", () => {
  it("标题与超长多行描述均采用 field-sizing-content 与 overflow-hidden，不再按 20 行封顶", async () => {
    const thirtyLines = Array.from(
      { length: 30 },
      (_, i) => `第 ${i + 1} 行任务说明`,
    ).join("\n");
    renderTaskDetail({
      title: "OPE-19 黑盒：报告评论发布测试（C-001 C-002）",
      description: thirtyLines,
    });

    const title = await screen.findByRole("textbox", { name: "任务标题" });
    const desc = screen.getByRole("textbox", { name: "任务描述" });

    for (const field of [title, desc]) {
      expect(field.tagName).toBe("TEXTAREA");
      expect(field).toHaveAttribute("rows", "1");
      expect(field).toHaveClass("overflow-hidden");
      expect(field).toHaveClass("field-sizing-content");
      expect(field).toHaveClass("w-full");
      expect(field).toHaveClass("min-w-0");
      expect(field).toHaveClass("resize-none");
    }
    expect(title).toHaveClass("min-h-8");
    expect(desc).toHaveClass("min-h-[60px]");
    expect(desc).toHaveValue(thirtyLines);
  });

  it("在不支持 field-sizing: content 的浏览器下按内容高度同步、响应宽度变化并保持外层滚动位置", async () => {
    vi.stubGlobal("CSS", {
      supports: vi.fn(() => false),
    });

    // 记录 ResizeObserver 回调，便于验证容器宽度变化触发重算且同宽不重复触发
    const observed = new Map<Element, ResizeObserverCallback>();
    class FakeResizeObserver {
      private cb: ResizeObserverCallback;
      constructor(cb: ResizeObserverCallback) {
        this.cb = cb;
      }
      observe(target: Element) {
        observed.set(target, this.cb);
      }
      unobserve(target: Element) {
        observed.delete(target);
      }
      disconnect() {
        for (const [el, cb] of observed.entries()) {
          if (cb === this.cb) observed.delete(el);
        }
      }
    }
    vi.stubGlobal("ResizeObserver", FakeResizeObserver);

    let mockDescWidth = 700;
    let mockDescScrollHeight = 160;
    const origScrollHeight = Object.getOwnPropertyDescriptor(
      HTMLElement.prototype,
      "scrollHeight",
    );
    const origClientWidth = Object.getOwnPropertyDescriptor(
      HTMLElement.prototype,
      "clientWidth",
    );
    vi.spyOn(HTMLElement.prototype, "scrollHeight", "get").mockImplementation(
      function (this: HTMLElement) {
        if (this.getAttribute("aria-label") === "任务描述")
          return mockDescScrollHeight;
        if (this.getAttribute("aria-label") === "任务标题") return 32;
        return origScrollHeight?.get ? origScrollHeight.get.call(this) : 0;
      },
    );
    vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockImplementation(
      function (this: HTMLElement) {
        if (this.getAttribute("aria-label") === "任务描述")
          return mockDescWidth;
        return origClientWidth?.get ? origClientWidth.get.call(this) : 0;
      },
    );

    const user = userEvent.setup();
    renderTaskDetail({ description: "初始多行内容" });

    const title = (await screen.findByRole("textbox", {
      name: "任务标题",
    })) as HTMLTextAreaElement;
    const desc = screen.getByRole("textbox", {
      name: "任务描述",
    }) as HTMLTextAreaElement;

    expect(title.style.height).toBe("32px");
    expect(desc.style.height).toBe("160px");

    // 模拟外层主内容区已经向下滚动，编辑描述增高时需保持原 scrollTop
    const scroller = desc.closest(".overflow-y-auto") as HTMLElement;
    expect(scroller).not.toBeNull();
    vi.spyOn(window, "getComputedStyle").mockImplementation((el) => {
      if (el === scroller) {
        return {
          overflowY: "auto",
          borderTopWidth: "0px",
          borderBottomWidth: "0px",
        } as CSSStyleDeclaration;
      }
      return {
        overflowY: "hidden",
        borderTopWidth: "0px",
        borderBottomWidth: "0px",
      } as CSSStyleDeclaration;
    });
    scroller.scrollTop = 240;

    mockDescScrollHeight = 220;
    await user.type(desc, "\n新增一行");
    expect(desc.style.height).toBe("220px");
    expect(scroller.scrollTop).toBe(240);

    // 触发同宽 ResizeObserver 不应改动已同步高度
    const resizeCb = observed.get(desc);
    expect(resizeCb).toBeDefined();
    mockDescScrollHeight = 999;
    act(() => {
      resizeCb!([], {} as ResizeObserver);
    });
    expect(desc.style.height).toBe("220px");

    // 容器变窄后（clientWidth 700 -> 360）按新折行高度重算
    mockDescWidth = 360;
    mockDescScrollHeight = 280;
    act(() => {
      resizeCb!([], {} as ResizeObserver);
    });
    expect(desc.style.height).toBe("280px");
    expect(scroller.scrollTop).toBe(240);
  });

  it("回退分支在元素未布局（scrollHeight 为 0）时清空内联高度，不锁死为 0px", async () => {
    vi.stubGlobal("CSS", {
      supports: vi.fn(() => false),
    });
    renderTaskDetail({ description: "测试隐藏态" });
    const desc = (await screen.findByRole("textbox", {
      name: "任务描述",
    })) as HTMLTextAreaElement;
    expect(desc.style.height).toBe("");
  });

  it("保留回车提交标题与描述 Esc 放弃修改", async () => {
    const user = userEvent.setup();
    const server = renderTaskDetail({ description: "原始描述" });

    const title = await screen.findByRole("textbox", { name: "任务标题" });
    await user.clear(title);
    await user.type(title, "新标题{Enter}");
    await waitFor(() =>
      expect(server.find("PATCH", "/tasks/task-1")).toHaveLength(1),
    );
    expect(server.find("PATCH", "/tasks/task-1")[0]!.body).toEqual({
      title: "新标题",
      version: 1,
    });

    const desc = screen.getByRole("textbox", { name: "任务描述" });
    await user.click(desc);
    await user.type(desc, "临时草稿{Escape}");
    expect(desc).toHaveValue("原始描述");
  });

  it("停用空间只读态下标题与描述保持只读且无内部滚动条", async () => {
    const o = `/v1/organizations/${ORG}`;
    const roServer = renderTaskDetail(
      { description: "只读长描述\n第二行" },
      (call) => {
        if (call.path === `${o}/spaces`) return json(200, { spaces: [] });
        if (call.path === `${o}/spaces/${SPACE}`) {
          return json(200, {
            id: SPACE,
            name: "已停用空间",
            prefix: "OPEN",
            visibility: "shared",
            status: "retired",
            retired_at: "2026-10-01T00:00:00Z",
            version: 2,
            created_at: "2026-10-01T00:00:00Z",
            updated_at: "2026-10-01T00:00:00Z",
            icon: null,
            purpose: "software_development",
            features: {
              views: true,
              projects: true,
              iterations: false,
              triage: false,
            },
            multi_assignee: false,
            estimation: {
              scale: "notUsed",
              extended: false,
              allow_zero: false,
              count_unestimated: false,
            },
            position: null,
            my_role: "OWNER",
            can_manage: true,
            member_count: 1,
            task_type_count: 1,
            label_count: 0,
            task_count: 0,
          });
        }
        return undefined;
      },
    );

    await waitFor(() =>
      expect(screen.getByRole("textbox", { name: "任务描述" })).toHaveAttribute(
        "readonly",
      ),
    );
    const desc = screen.getByRole("textbox", { name: "任务描述" });
    const title = screen.getByRole("textbox", { name: "任务标题" });
    expect(title).toHaveAttribute("readonly");
    expect(title).toHaveClass(
      "overflow-hidden",
      "field-sizing-content",
      "min-w-0",
    );
    expect(desc).toHaveClass(
      "overflow-hidden",
      "field-sizing-content",
      "min-w-0",
    );
    expect(roServer.find("PATCH", "/tasks/task-1")).toHaveLength(0);
  });
});
