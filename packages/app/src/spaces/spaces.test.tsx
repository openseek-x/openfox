import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { SpaceDetail } from "@openfox/api";

import {
  apiError,
  findCall,
  json,
  renderSettings,
  ts,
  type Call,
} from "../settings/testing";
import { useCanonicalDisplayId } from "../tasks/detail/TaskDetailDrawer";
import { fullOrder, reorderIds } from "./SpacesNav";
import { nextStep, previousStep } from "./create/CreateSpaceDialog";
import { moveTargets } from "./settings/DangerZone";

// 空间设置、新建向导与估时的界面测试（假后端按路径返回固定数据）。

const ORG = "/v1/organizations/o1";

function space(overrides: Partial<SpaceDetail> = {}): SpaceDetail {
  return {
    id: "s1",
    prefix: "OPEN",
    name: "默认空间",
    visibility: "shared",
    version: 3,
    created_at: ts,
    updated_at: ts,
    icon: null,
    purpose: "software_development",
    features: { views: true, projects: true, iterations: false, triage: false },
    multi_assignee: false,
    estimation: {
      scale: "notUsed",
      extended: false,
      allow_zero: false,
      count_unestimated: false,
    },
    status: "active",
    retired_at: null,
    position: 0,
    my_role: "OWNER",
    can_manage: true,
    member_count: 3,
    task_type_count: 2,
    label_count: 0,
    task_count: 0,
    ...overrides,
  };
}

const other = space({
  id: "s2",
  prefix: "DES",
  name: "设计中心",
  position: 1,
});

/** 空间设置相关接口的假后端；detail 为 s1 的当前状态（写操作可以改它）。 */
function backend(
  options: {
    detail?: SpaceDetail;
    list?: SpaceDetail[];
    impact?: Record<string, number>;
    preview?: Array<{ value: number; count: number }>;
    extra?: (call: Call) => Response | undefined;
  } = {},
) {
  const state = { detail: options.detail ?? space() };
  const route = (call: Call): Response | undefined => {
    const extra = options.extra?.(call);
    if (extra) return extra;
    const { method, path } = call;
    if (path === `${ORG}/spaces` && method === "GET")
      return json(200, {
        spaces:
          options.list ??
          (state.detail.status === "active" ? [state.detail, other] : [other]),
      });
    if (path === `${ORG}/spaces/s1` && method === "GET")
      return json(200, state.detail);
    if (path === `${ORG}/spaces/s2` && method === "GET")
      return json(200, other);
    if (path === `${ORG}/spaces/s1` && method === "PATCH") {
      state.detail = {
        ...state.detail,
        ...(call.body as object),
        version: state.detail.version + 1,
      };
      return json(200, state.detail);
    }
    if (path === `${ORG}/spaces/s1/identifier`) {
      state.detail = {
        ...state.detail,
        prefix: (call.body as { prefix: string }).prefix,
        version: state.detail.version + 1,
      };
      return json(200, state.detail);
    }
    if (path === `${ORG}/spaces/s1/repos`) return json(200, { repos: [] });
    if (path === `${ORG}/spaces/s1/impact`)
      return json(200, {
        open_tasks: 12,
        total_tasks: 128,
        projects: 0,
        active_runs: 1,
        agents: 2,
        space_grants: 0,
        ...options.impact,
      });
    if (path === `${ORG}/spaces/s1/retire`) {
      state.detail = { ...state.detail, status: "retired", retired_at: ts };
      return json(200, {
        space: state.detail,
        canceled_tasks: 12,
        moved_tasks: 0,
        canceled_runs: 1,
      });
    }
    if (path === `${ORG}/spaces/s1` && method === "DELETE")
      return json(200, {
        deleted: true,
        archived_agents: 2,
        canceled_runs: 0,
        revoked_grants: 0,
      });
    if (path === `${ORG}/spaces/s1/estimation/preview`)
      return json(200, {
        scale: call.query.get("scale"),
        allowed_values: null,
        values: (options.preview ?? []).map((item) => ({
          ...item,
          valid: false,
          suggested: null,
        })),
        unestimated_count: 0,
        total_count: 10,
      });
    if (path === `${ORG}/spaces/s1/estimation`) {
      const body = call.body as Record<string, unknown>;
      state.detail = {
        ...state.detail,
        estimation: {
          scale: body.scale as SpaceDetail["estimation"]["scale"],
          extended: Boolean(body.extended),
          allow_zero: Boolean(body.allow_zero),
          count_unestimated: Boolean(body.count_unestimated),
        },
        version: state.detail.version + 1,
      };
      return json(200, { space: state.detail, converted_tasks: 2 });
    }
    if (path === `${ORG}/task-type-templates`)
      return json(200, {
        templates: [
          template("requirement", "Requirement"),
          template("bug", "Bug"),
          template("task", "Task"),
          template("user_story", "User Story"),
        ],
      });
    if (path.startsWith(`${ORG}/space-identifiers/`)) {
      const prefix = decodeURIComponent(path.split("/").pop()!);
      const taken = prefix === "DES";
      return json(200, {
        prefix,
        valid: true,
        available: !taken,
        space_name: taken ? "设计中心" : null,
      });
    }
    return undefined;
  };
  return { route, state };
}

function template(key: string, name: string) {
  return {
    key,
    name,
    description: `${name} description`,
    icon: "bug",
    color: "#DC2626",
    purposes: ["software_development"],
  };
}

afterEach(() => {
  localStorage.clear();
  vi.useRealTimers();
});

describe("设置导航与权限", () => {
  it("空间表情通过设置接口保存，重新打开后回显", async () => {
    const user = userEvent.setup();
    const { route, state } = backend();
    const calls = renderSettings("/o/o1/settings/spaces/s1/general", route);
    await user.click(await screen.findByRole("button", { name: "选择图标" }));
    await user.click(screen.getByRole("tab", { name: "表情" }));
    await user.click(screen.getByRole("button", { name: "🚀" }));
    await waitFor(() =>
      expect(findCall(calls, "PATCH", `${ORG}/spaces/s1`)?.body).toEqual({
        icon: "emoji:🚀",
        version: 3,
      }),
    );
    expect(state.detail.icon).toBe("emoji:🚀");
    await user.click(screen.getByRole("button", { name: "选择图标" }));
    expect(screen.getByRole("button", { name: "🚀" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
  });

  it("空间图标保存失败保留原值", async () => {
    const user = userEvent.setup();
    const { route, state } = backend({
      extra: (call) =>
        call.method === "PATCH" ? apiError(409, "VERSION_CONFLICT") : undefined,
    });
    renderSettings("/o/o1/settings/spaces/s1/general", route);
    await user.click(await screen.findByRole("button", { name: "选择图标" }));
    await user.click(screen.getByRole("tab", { name: "表情" }));
    await user.click(screen.getByRole("button", { name: "🚀" }));
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "选择图标" })).toBeEnabled(),
    );
    expect(state.detail.icon).toBeNull();
    expect(screen.queryByText("🚀")).toBeNull();
  });

  it("「我的空间」分组位于全局配置之前，空间下没有二级页", async () => {
    const { route } = backend();
    renderSettings("/o/o1/settings/spaces/s1/general", route);
    const nav = await screen.findByRole("navigation", { name: "设置导航" });
    const group = await within(nav).findByRole("list", { name: "我的空间" });
    expect(
      within(group).getByRole("link", { name: "默认空间" }),
    ).toBeInTheDocument();
    expect(
      within(group).getByRole("link", { name: "设计中心" }),
    ).toBeInTheDocument();
    // 空间下没有二级页，二级页从空间概览进入
    expect(within(group).getAllByRole("link")).toHaveLength(2);
    expect(
      within(group).queryByRole("link", { name: "任务估时" }),
    ).not.toBeInTheDocument();
    // 分组在「全局配置」之前
    const text = nav.textContent ?? "";
    expect(text.indexOf("我的空间")).toBeLessThan(text.indexOf("全局配置"));
    expect(
      within(nav).getByRole("button", { name: /创建空间/ }),
    ).toBeInTheDocument();
  });

  it("没有管理权限时通用设置只读、不显示危险操作", async () => {
    const { route } = backend({
      detail: space({ my_role: "MEMBER", can_manage: false }),
    });
    renderSettings("/o/o1/settings/spaces/s1/general", route, "MEMBER");
    const name = await screen.findByRole("textbox", { name: "空间名称" });
    expect(name).toBeDisabled();
    expect(screen.getByRole("textbox", { name: "标识符" })).toBeDisabled();
    expect(
      screen.getByRole("switch", { name: "允许多个处理人" }),
    ).toBeDisabled();
    expect(
      screen.queryByRole("region", { name: "危险操作" }),
    ).not.toBeInTheDocument();
    expect(
      await screen.findByText("只有管理员和所有者可以管理代码仓库。"),
    ).toBeInTheDocument();
  });

  it("不存在的空间显示「未找到空间」", async () => {
    renderSettings("/o/o1/settings/spaces/zz/general", (call) =>
      call.path === `${ORG}/spaces/zz` ? apiError(404, "NOT_FOUND") : undefined,
    );
    expect(await screen.findByText("未找到空间")).toBeInTheDocument();
  });

  it("改名失焦即保存并提示", async () => {
    const user = userEvent.setup();
    const { route } = backend();
    const calls = renderSettings("/o/o1/settings/spaces/s1/general", route);
    const name = await screen.findByRole("textbox", { name: "空间名称" });
    await user.clear(name);
    await user.type(name, "研发中心");
    await user.tab();
    await waitFor(() =>
      expect(findCall(calls, "PATCH", `${ORG}/spaces/s1`)?.body).toEqual({
        name: "研发中心",
        version: 3,
      }),
    );
    expect(
      await screen.findByText("空间已重命名为“研发中心”。"),
    ).toBeInTheDocument();
  });
});

describe("标识符", () => {
  it("输入自动转大写并过滤，格式与冲突提示替换说明文字", async () => {
    const user = userEvent.setup();
    const { route } = backend();
    renderSettings("/o/o1/settings/spaces/s1/general", route);
    const input = await screen.findByRole("textbox", { name: "标识符" });
    await user.clear(input);
    await user.type(input, "e-");
    expect(input).toHaveValue("E");
    expect(
      screen.getByText("标识符必须是 2–7 位字母或数字。"),
    ).toBeInTheDocument();
    await user.type(input, "ngineering");
    expect(input).toHaveValue("ENGINEE");
    await user.clear(input);
    await user.type(input, "des");
    expect(
      screen.getByText("标识符“DES”已被 设计中心 使用。"),
    ).toBeInTheDocument();
    // 冲突时失焦不弹确认
    await user.tab();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("合法新值先确认：输入一致后才能提交，成功后提示新标识符", async () => {
    const user = userEvent.setup();
    const { route } = backend();
    const calls = renderSettings("/o/o1/settings/spaces/s1/general", route);
    const input = await screen.findByRole("textbox", { name: "标识符" });
    await user.clear(input);
    await user.type(input, "eng{Enter}");
    const dialog = await screen.findByRole("dialog", {
      name: "更改空间标识符？",
    });
    expect(
      within(dialog).getByText("OPEN-1234 -> ENG-1234"),
    ).toBeInTheDocument();
    const submit = within(dialog).getByRole("button", { name: "更改标识符" });
    expect(submit).toBeDisabled();
    const confirm = within(dialog).getByRole("textbox", {
      name: "输入新标识符确认",
    });
    await user.type(confirm, "en");
    expect(submit).toBeDisabled();
    await user.type(confirm, "g");
    expect(submit).toBeEnabled();
    await user.click(submit);
    await waitFor(() =>
      expect(
        findCall(calls, "POST", `${ORG}/spaces/s1/identifier`)?.body,
      ).toEqual({
        version: 3,
        prefix: "ENG",
      }),
    );
    expect(
      await screen.findByText("空间已更新。标识符现在是“ENG”。"),
    ).toBeInTheDocument();
  });

  it("取消确认时输入框回到原值", async () => {
    const user = userEvent.setup();
    const { route } = backend();
    renderSettings("/o/o1/settings/spaces/s1/general", route);
    const input = await screen.findByRole("textbox", { name: "标识符" });
    await user.clear(input);
    await user.type(input, "eng{Enter}");
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: "取消" }));
    expect(input).toHaveValue("OPEN");
  });
});

describe("停用空间", () => {
  it.each([
    ["cancel", undefined],
    ["keep", undefined],
    ["move", "s2"],
  ] as const)("处置方式 %s", async (disposition, target) => {
    const user = userEvent.setup();
    const { route } = backend();
    const calls = renderSettings("/o/o1/settings/spaces/s1/general", route);
    await user.click(await screen.findByRole("button", { name: "停用空间" }));
    const dialog = await screen.findByRole("dialog", {
      name: "停用空间“默认空间”？",
    });
    expect(
      await within(dialog).findByText("12 个未完成任务"),
    ).toBeInTheDocument();
    expect(
      within(dialog).getByText("未完成任务会被取消；已完成任务会保留。"),
    ).toBeInTheDocument();
    if (disposition !== "cancel") {
      await user.click(
        within(dialog).getByRole("button", { name: "未完成任务的处理方式" }),
      );
      await user.click(
        await screen.findByRole("menuitemradio", {
          name: disposition === "keep" ? "保留任务" : /移动到 设计中心/,
        }),
      );
    }
    if (disposition === "move")
      expect(
        within(dialog).getByText(/未完成任务会移动到 设计中心/),
      ).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "停用空间" }));
    await waitFor(() =>
      expect(findCall(calls, "POST", `${ORG}/spaces/s1/retire`)?.body).toEqual({
        version: 3,
        open_tasks: disposition,
        ...(target ? { target_space_id: target } : {}),
      }),
    );
    expect(
      await screen.findByText("空间“默认空间”已停用。"),
    ).toBeInTheDocument();
  });

  it("可移入的目标只有其他活跃且可读的空间", () => {
    const privateOther = space({
      id: "s3",
      visibility: "private",
      my_role: null,
    });
    const retired = space({ id: "s4", status: "retired" });
    expect(
      moveTargets([space(), other, privateOther, retired], "s1").map(
        (s) => s.id,
      ),
    ).toEqual(["s2"]);
  });

  it("已停用空间：只读横幅、可以恢复、删除可用", async () => {
    const user = userEvent.setup();
    const { route } = backend({
      detail: space({ status: "retired", retired_at: ts }),
    });
    const calls = renderSettings("/o/o1/settings/spaces/s1/general", route);
    expect(
      await screen.findByText(
        "此空间已停用且为只读。请在危险操作中恢复后再更改。",
      ),
    ).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "空间名称" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "删除空间" })).toBeEnabled();
    await user.click(screen.getByRole("button", { name: "恢复空间" }));
    await waitFor(() =>
      expect(findCall(calls, "POST", `${ORG}/spaces/s1/restore`)?.body).toEqual(
        {
          version: 3,
        },
      ),
    );
  });

  it("活跃空间的删除按钮禁用（需先停用）", async () => {
    const { route } = backend();
    renderSettings("/o/o1/settings/spaces/s1/general", route);
    expect(
      await screen.findByRole("button", { name: "删除空间" }),
    ).toBeDisabled();
    expect(
      screen.getByText("请先停用此空间；只有已停用的空间才能永久删除。"),
    ).toBeInTheDocument();
  });
});

describe("删除空间", () => {
  it("输入与空间名称完全一致后才能删除，成功后回到收件箱", async () => {
    const user = userEvent.setup();
    const { route } = backend({
      detail: space({ status: "retired", retired_at: ts }),
      extra: (call) =>
        call.path.endsWith("/inbox")
          ? json(200, { items: [], next_cursor: null })
          : undefined,
    });
    const calls = renderSettings("/o/o1/settings/spaces/s1/general", route);
    await user.click(await screen.findByRole("button", { name: "删除空间" }));
    const dialog = await screen.findByRole("dialog", {
      name: "删除空间“默认空间”？",
    });
    expect(await within(dialog).findByText("128 个任务")).toBeInTheDocument();
    expect(within(dialog).getByText("2 个 Agent 会被归档")).toBeInTheDocument();
    const submit = within(dialog).getByRole("button", { name: "删除空间" });
    const input = within(dialog).getByRole("textbox", {
      name: "输入空间名称确认",
    });
    await user.type(input, "默认空间 ");
    expect(submit).toBeDisabled();
    await user.clear(input);
    await user.type(input, "默认空间");
    expect(submit).toBeEnabled();
    await user.click(submit);
    await waitFor(() =>
      expect(findCall(calls, "DELETE", `${ORG}/spaces/s1`)?.body).toEqual({
        version: 3,
        confirm_name: "默认空间",
      }),
    );
    expect(
      await screen.findByText("空间“默认空间”已删除。"),
    ).toBeInTheDocument();
  });
});

describe("侧栏排序", () => {
  it("拖动后的顺序与提交给接口的完整顺序", () => {
    expect(reorderIds(["a", "b", "c"], "c", "a")).toEqual(["c", "a", "b"]);
    expect(reorderIds(["a", "b", "c"], "a", "a")).toBeNull();
    expect(reorderIds(["a", "b"], "a", null)).toBeNull();
    const all = [
      space({ id: "a" }),
      space({ id: "x", my_role: null }),
      space({ id: "b" }),
    ];
    // 未加入的空间按原顺序接在后面
    expect(fullOrder(all, ["b", "a"])).toEqual(["b", "a", "x"]);
  });
});

describe("任务估时", () => {
  it("固定档位刻度才显示额外 3 个开关", async () => {
    const { route } = backend({
      detail: space({
        estimation: {
          scale: "fibonacci",
          extended: false,
          allow_zero: false,
          count_unestimated: false,
        },
      }),
    });
    renderSettings("/o/o1/settings/spaces/s1/estimates", route);
    expect(
      await screen.findByRole("button", { name: "估时刻度" }),
    ).toHaveTextContent("斐波那契（1, 2, 3, 5, 8 点）");
    expect(
      screen.getByRole("switch", { name: "扩展估时刻度" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("switch", { name: "允许零估时" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("switch", { name: "未估时任务按 1 点计算" }),
    ).toBeInTheDocument();
  });

  it("故事点切到斐波那契：选择取整方式、预览后转换并应用", async () => {
    const user = userEvent.setup();
    const { route } = backend({
      detail: space({
        estimation: {
          scale: "storyPoints",
          extended: false,
          allow_zero: false,
          count_unestimated: false,
        },
      }),
      preview: [
        { value: 4, count: 3 },
        { value: 7, count: 1 },
      ],
    });
    const calls = renderSettings("/o/o1/settings/spaces/s1/estimates", route);
    expect(screen.queryByRole("switch", { name: "扩展估时刻度" })).toBeNull();
    await user.click(await screen.findByRole("button", { name: "估时刻度" }));
    await user.click(
      await screen.findByRole("menuitemradio", {
        name: "斐波那契（1, 2, 3, 5, 8 点）",
      }),
    );
    const dialog = await screen.findByRole("dialog", { name: "转换已有估时" });
    await user.click(within(dialog).getByRole("button", { name: "取整方式" }));
    await user.click(await screen.findByRole("option", { name: "向下取整" }));
    await user.click(within(dialog).getByRole("button", { name: "下一步" }));
    const preview = within(dialog).getByRole("list", { name: "预览" });
    expect(
      within(preview)
        .getAllByRole("listitem")
        .map((li) => li.textContent),
    ).toEqual(["4→3", "7→5"]);
    await user.click(
      within(dialog).getByRole("button", { name: "转换并应用" }),
    );
    await waitFor(() =>
      expect(
        findCall(calls, "PATCH", `${ORG}/spaces/s1/estimation`)?.body,
      ).toEqual({
        version: 3,
        scale: "fibonacci",
        extended: false,
        allow_zero: false,
        count_unestimated: false,
        convert: [
          { from: 4, to: 3 },
          { from: 7, to: 5 },
        ],
      }),
    );
  });

  it("能自动映射时直接保存，不弹转换", async () => {
    const user = userEvent.setup();
    const { route } = backend({
      detail: space({
        estimation: {
          scale: "fibonacci",
          extended: false,
          allow_zero: false,
          count_unestimated: false,
        },
      }),
      preview: [{ value: 3, count: 2 }],
    });
    const calls = renderSettings("/o/o1/settings/spaces/s1/estimates", route);
    await user.click(await screen.findByRole("button", { name: "估时刻度" }));
    await user.click(
      await screen.findByRole("menuitemradio", { name: /^线性/ }),
    );
    // 斐波那契第 3 档（3）对齐到线性第 3 档（3），值不变，不需要 convert
    await waitFor(() =>
      expect(
        findCall(calls, "PATCH", `${ORG}/spaces/s1/estimation`)?.body,
      ).toEqual({
        version: 3,
        scale: "linear",
        extended: false,
        allow_zero: false,
        count_unestimated: false,
      }),
    );
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });
});

describe("旧编号重定向", () => {
  function Probe({
    id,
    canonical,
    spaceId,
  }: {
    id: string;
    canonical?: string;
    spaceId?: string;
  }) {
    useCanonicalDisplayId(id, canonical, undefined, spaceId);
    const location = useLocation();
    return (
      <>
        <span data-testid="url">{location.search}</span>
        <span data-testid="path">{location.pathname}</span>
      </>
    );
  }

  it("任务已移到别的空间：从旧空间页面跳到所在空间任务页并打开新编号", async () => {
    render(
      <MemoryRouter initialEntries={["/o/o1/spaces/s1/board?task=OPEN-12"]}>
        <Routes>
          <Route
            path="*"
            element={<Probe id="OPEN-12" canonical="ENG-12" spaceId="s2" />}
          />
        </Routes>
      </MemoryRouter>,
    );
    await waitFor(() =>
      expect(screen.getByTestId("path")).toHaveTextContent(
        "/o/o1/spaces/s2/tasks",
      ),
    );
    expect(screen.getByTestId("url")).toHaveTextContent("?task=ENG-12");
  });

  it("不在空间页面（如收件箱）：只换 ?task=", async () => {
    render(
      <MemoryRouter initialEntries={["/o/o1/inbox?task=OPEN-12"]}>
        <Routes>
          <Route
            path="*"
            element={<Probe id="OPEN-12" canonical="ENG-12" spaceId="s2" />}
          />
        </Routes>
      </MemoryRouter>,
    );
    await waitFor(() =>
      expect(screen.getByTestId("url")).toHaveTextContent("?task=ENG-12"),
    );
    expect(screen.getByTestId("path")).toHaveTextContent("/o/o1/inbox");
  });

  it("按旧编号命中时把 ?task= 原地替换为当前编号", async () => {
    render(
      <MemoryRouter initialEntries={["/o/o1/spaces/s1/tasks?task=OPEN-12&x=1"]}>
        <Routes>
          <Route path="*" element={<Probe id="OPEN-12" canonical="ENG-12" />} />
        </Routes>
      </MemoryRouter>,
    );
    await waitFor(() =>
      expect(screen.getByTestId("url")).toHaveTextContent("?task=ENG-12&x=1"),
    );
  });

  it("编号未变时不改地址", () => {
    render(
      <MemoryRouter initialEntries={["/x?task=ENG-1"]}>
        <Routes>
          <Route path="*" element={<Probe id="ENG-1" canonical="eng-1" />} />
        </Routes>
      </MemoryRouter>,
    );
    expect(screen.getByTestId("url")).toHaveTextContent("?task=ENG-1");
  });
});

describe("新建空间向导", () => {
  it("步骤顺序：只有软件开发进入「设置你的空间」", () => {
    expect(nextStep("purpose", "software_development")).toBe("setup");
    expect(nextStep("purpose", "marketing")).toBe("details");
    expect(previousStep("details", "marketing")).toBe("purpose");
    expect(previousStep("details", "software_development")).toBe("setup");
  });

  it("软件开发：推荐类型 → 填写信息 → 创建并跳转到新空间任务页", async () => {
    const user = userEvent.setup();
    const created = space({ id: "s9", name: "Engineering", prefix: "ENGI" });
    const { route } = backend({
      extra: (call) =>
        call.method === "POST" && call.path === `${ORG}/spaces`
          ? json(201, created)
          : undefined,
    });
    const calls = renderSettings("/o/o1/settings/spaces?createSpace=1", route);
    const dialog = await screen.findByRole("dialog");
    expect(
      within(dialog).getByText("该空间主要做什么类型的工作？"),
    ).toBeInTheDocument();
    await user.click(within(dialog).getByRole("radio", { name: "软件开发" }));
    expect(await within(dialog).findByText("设置你的空间")).toBeInTheDocument();
    // 默认勾选前 3 个推荐类型
    expect(
      await within(dialog).findByRole("checkbox", { name: "需求" }),
    ).toBeChecked();
    expect(
      within(dialog).getByRole("checkbox", { name: "用户故事" }),
    ).not.toBeChecked();
    await user.click(
      within(dialog).getByRole("checkbox", { name: "用户故事" }),
    );
    await user.click(within(dialog).getByRole("checkbox", { name: "缺陷" }));
    // 勾选迭代先选择模式
    await user.click(within(dialog).getByRole("checkbox", { name: "迭代" }));
    const mode = await screen.findByRole("dialog", { name: "迭代模式" });
    await user.click(within(mode).getByRole("button", { name: "确定" }));
    await user.click(within(dialog).getByRole("button", { name: "继续" }));
    expect(await within(dialog).findByText("填写空间信息")).toBeInTheDocument();
    await user.type(
      within(dialog).getByRole("textbox", { name: "空间名称" }),
      "Engineering",
    );
    expect(within(dialog).getByRole("textbox", { name: "标识符" })).toHaveValue(
      "ENGI",
    );
    await user.click(within(dialog).getByRole("radio", { name: /私有/ }));
    await waitFor(() =>
      expect(
        within(dialog).getByRole("button", { name: "创建空间" }),
      ).toBeEnabled(),
    );
    await user.click(within(dialog).getByRole("button", { name: "创建空间" }));
    await waitFor(() => {
      const call = findCall(calls, "POST", `${ORG}/spaces`);
      expect(call?.body).toEqual({
        name: "Engineering",
        prefix: "ENGI",
        visibility: "private",
        purpose: "software_development",
        features: {
          views: true,
          projects: true,
          iterations: true,
          triage: false,
        },
        template_keys: ["requirement", "task", "user_story"],
      });
      expect(call?.headers.get("Idempotency-Key")).toBeTruthy();
    });
    expect(
      await screen.findByText("空间“Engineering”已创建。"),
    ).toBeInTheDocument();
  });

  it("其他工作类型直接填写信息；标识符被占用时提示并禁止创建", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    const { route } = backend();
    renderSettings("/o/o1/settings/spaces?createSpace=1", route);
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("radio", { name: "市场营销" }));
    expect(await within(dialog).findByText("填写空间信息")).toBeInTheDocument();
    await user.type(
      within(dialog).getByRole("textbox", { name: "空间名称" }),
      "设计",
    );
    const prefix = within(dialog).getByRole("textbox", { name: "标识符" });
    await user.type(prefix, "des");
    await act(async () => {
      vi.advanceTimersByTime(500);
    });
    expect(
      await within(dialog).findByText("标识符“DES”已被占用。"),
    ).toBeInTheDocument();
    expect(
      within(dialog).getByRole("button", { name: "创建空间" }),
    ).toBeDisabled();
  });

  it("普通成员看不到创建入口", async () => {
    const { route } = backend({
      detail: space({ my_role: "MEMBER", can_manage: false }),
    });
    renderSettings("/o/o1/settings/spaces?createSpace=1", route, "MEMBER");
    expect(
      await screen.findByRole("heading", { name: "空间" }),
    ).toBeInTheDocument();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "创建空间" }),
    ).not.toBeInTheDocument();
  });
});
