import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { AgentEnvVarList, ContactItem } from "@openfox/api";

import {
  apiError,
  findCall,
  json,
  renderSettings,
  type Call,
  type Route,
} from "../settings/testing";
import { agent, crew, device, entry, TS } from "./fixtures";

const ORG = "/v1/organizations/o1";
const spaces = [
  {
    id: "s1",
    name: "默认空间",
    prefix: "DEF",
    visibility: "shared",
    created_at: TS,
  },
  {
    id: "s2",
    name: "OpenFox",
    prefix: "OPF",
    visibility: "shared",
    created_at: TS,
  },
];

/** 公共接口：空间列表、成员、目录、电脑等。 */
function base(extra: Route = () => undefined): Route {
  return (call) => {
    const custom = extra(call);
    if (custom) return custom;
    if (call.method !== "GET") return undefined;
    if (call.path === `${ORG}/spaces`) return json(200, { spaces });
    if (call.path === `${ORG}/members`)
      return json(200, {
        members: [
          {
            user_id: "u1",
            email: "lin@openfox.dev",
            role: "OWNER",
            status: "ACTIVE",
          },
        ],
        next_cursor: "",
      });
    if (call.path === `${ORG}/devices`)
      return json(200, { devices: [device("d1")], next_cursor: null });
    if (call.path === `${ORG}/agent-directory`)
      return json(200, { entries: [], archived_count: 0, truncated: false });
    return undefined;
  };
}

afterEach(() => {
  localStorage.clear();
  vi.useRealTimers();
});

describe("全部 Agent 目录", () => {
  const directory = {
    entries: [
      entry("a1", { name: "熊孩子", title: "AI 工程师", pinned: true }),
      entry("a2", {
        name: "架构师",
        space_id: "s2",
        space_name: "OpenFox",
        mine: false,
        owner_email: "fox@openfox.dev",
      }),
      entry("c1", {
        type: "crew",
        name: "前端小分队",
        space_id: "s2",
        space_name: "OpenFox",
        member_count: 3,
        members: [{ id: "m1", name: "小橙", kind: "CODE", title: null }],
      }),
    ],
    archived_count: 2,
    truncated: false,
  };
  const routes = base((call) =>
    call.method === "GET" && call.path === `${ORG}/agent-directory`
      ? json(200, directory)
      : undefined,
  );

  it("置顶在前、按空间分组；meta 行显示模型 · Runtime · 所有者（自己显示你）", async () => {
    renderSettings("/o/o1/agents", routes);
    const pinned = await screen.findByRole("region", { name: "置顶" });
    expect(within(pinned).getByText("熊孩子")).toBeInTheDocument();
    expect(within(pinned).getByText("默认 · Codex · 你")).toBeInTheDocument();
    const space = screen.getByRole("region", { name: "OpenFox" });
    expect(
      within(space).getByText("默认 · Codex · fox@openfox.dev"),
    ).toBeInTheDocument();
    expect(within(space).getByText("你 · 3 个成员")).toBeInTheDocument();
    expect(screen.getByText("显示已归档（2）→")).toBeInTheDocument();
  });

  it("筛选小队、搜索成员名，无匹配时显示空态文案", async () => {
    const user = userEvent.setup();
    renderSettings("/o/o1/agents", routes);
    await screen.findByText("熊孩子");
    await user.click(screen.getByRole("radio", { name: "小队" }));
    expect(screen.queryByText("熊孩子")).not.toBeInTheDocument();
    expect(screen.getByText("前端小分队")).toBeInTheDocument();
    await user.click(screen.getByRole("radio", { name: "全部" }));
    await user.type(
      screen.getByRole("searchbox", { name: "搜索 Agent 或小队" }),
      "小橙",
    );
    expect(screen.getByText("前端小分队")).toBeInTheDocument();
    expect(screen.queryByText("架构师")).not.toBeInTheDocument();
    await user.clear(screen.getByRole("searchbox"));
    await user.type(screen.getByRole("searchbox"), "设计师");
    expect(
      screen.getByText("当前筛选下没有匹配的 Agent 或小队。"),
    ).toBeInTheDocument();
  });

  it("分组可折叠", async () => {
    const user = userEvent.setup();
    renderSettings("/o/o1/agents", routes);
    await screen.findByText("架构师");
    await user.click(
      screen.getByRole("button", { name: "展开或收起 OpenFox" }),
    );
    expect(screen.queryByText("架构师")).not.toBeInTheDocument();
  });

  it("置顶失败回滚并提示「无法更新置顶状态」", async () => {
    const user = userEvent.setup();
    const calls = renderSettings(
      "/o/o1/agents",
      base((call) => {
        if (call.method === "GET" && call.path === `${ORG}/agent-directory`)
          return json(200, directory);
        if (
          call.method === "PUT" &&
          call.path === `${ORG}/agent-directory/pins`
        )
          return apiError(500, "INTERNAL");
        return undefined;
      }),
    );
    await screen.findByText("架构师");
    await user.click(screen.getByRole("button", { name: "架构师 的更多操作" }));
    await user.click(screen.getByRole("menuitem", { name: "置顶" }));
    expect(await screen.findByText("无法更新置顶状态")).toBeInTheDocument();
    expect(findCall(calls, "PUT", `${ORG}/agent-directory/pins`)?.body).toEqual(
      {
        agent_id: "a2",
        pinned: true,
      },
    );
    await waitFor(() =>
      expect(
        within(screen.getByRole("region", { name: "OpenFox" })).getByText(
          "架构师",
        ),
      ).toBeInTheDocument(),
    );
  });

  it("加载失败可重试；truncated 显示提示", async () => {
    let fail = true;
    const user = userEvent.setup();
    renderSettings(
      "/o/o1/agents",
      base((call) => {
        if (call.method === "GET" && call.path === `${ORG}/agent-directory`) {
          if (fail) return apiError(500, "INTERNAL");
          return json(200, { ...directory, truncated: true });
        }
        return undefined;
      }),
    );
    expect(await screen.findByText("无法加载 Agent 列表")).toBeInTheDocument();
    fail = false;
    await user.click(screen.getByRole("button", { name: "重试" }));
    expect(
      await screen.findByText(
        "只显示了前 500 个 Agent 和小队，可以用搜索缩小范围。",
      ),
    ).toBeInTheDocument();
  });

  it("没有 Agent 时创建小队改为「请先创建 Agent」", async () => {
    const user = userEvent.setup();
    renderSettings("/o/o1/agents", base());
    await screen.findByText("还没有 Agent");
    await user.click(screen.getByRole("button", { name: "创建小队" }));
    expect(
      await screen.findByRole("dialog", { name: "请先创建 Agent" }),
    ).toBeInTheDocument();
  });
});

describe("创建 Agent", () => {
  function routes(created: Call[], devices = [device("d1")]): Route {
    return base((call) => {
      if (call.method === "GET" && call.path === `${ORG}/devices`)
        return json(200, { devices, next_cursor: null });
      if (call.method === "POST" && call.path === `${ORG}/spaces/s1/agents`) {
        created.push(call);
        return json(201, agent("new1", { name: "段小誉", title: "前端" }));
      }
      if (call.method === "GET" && call.path === `${ORG}/spaces/s1/agents/new1`)
        return json(200, agent("new1", { name: "段小誉", title: "前端" }));
      if (
        call.method === "POST" &&
        call.path === `${ORG}/spaces/s1/agents/new1/repos`
      )
        return json(201, {});
      if (
        call.method === "GET" &&
        call.path.startsWith(`${ORG}/spaces/s1/agents/new1/`)
      )
        return json(200, {
          task_count: 0,
          run_count: 0,
          input_tokens: 0,
          output_tokens: 0,
          last_run_at: null,
        });
      return undefined;
    });
  }

  it("请求体：默认私有、model=default、工作方式映射 WORKSPACE；成功后打开设置抽屉并提示", async () => {
    const user = userEvent.setup();
    const created: Call[] = [];
    const calls = renderSettings("/o/o1/agents", routes(created));
    await screen.findByText("还没有 Agent");
    await user.click(screen.getByRole("button", { name: "创建 Agent" }));
    const dialog = await screen.findByRole("dialog", { name: "创建 Agent" });
    await user.type(
      within(dialog).getByRole("textbox", { name: "名称" }),
      "段小誉",
    );
    await user.type(
      within(dialog).getByRole("textbox", { name: "职位" }),
      "前端",
    );
    await user.click(
      within(dialog).getByRole("button", { name: "添加 Git 仓库" }),
    );
    await user.type(
      within(dialog).getByRole("textbox", { name: "仓库地址" }),
      "https://github.com/openfox/web.git",
    );
    await waitFor(() =>
      expect(
        within(dialog).getByRole("button", { name: "创建" }),
      ).toBeEnabled(),
    );
    await user.click(within(dialog).getByRole("button", { name: "创建" }));
    expect(await screen.findByText("Agent 已就绪")).toBeInTheDocument();
    expect(created).toHaveLength(1);
    const body = created[0]!.body as Record<string, unknown> & {
      config: Record<string, unknown>;
    };
    expect(body).toMatchObject({
      name: "段小誉",
      title: "前端",
      kind: "CODE",
      visibility: "PRIVATE",
    });
    expect(body.config).toMatchObject({
      computer_id: "d1",
      grant_id: "g-d1",
      runtime: "codex",
      model: "default",
      directory_mode: "WORKSPACE",
      directory_roots: [],
    });
    expect(created[0]!.headers.get("Idempotency-Key")).toBeTruthy();
    expect(
      findCall(calls, "POST", `${ORG}/spaces/s1/agents/new1/repos`)?.body,
    ).toMatchObject({ url: "https://github.com/openfox/web.git" });
    expect(
      await screen.findByRole("complementary", { name: "段小誉 的设置" }),
    ).toBeInTheDocument();
  });

  it("没有可用电脑时显示 Callout 并禁用「创建」；不可用原因写在选项里", async () => {
    const user = userEvent.setup();
    renderSettings(
      "/o/o1/agents",
      routes([], [device("d2", { online: false, name: "build-server" })]),
    );
    await screen.findByText("还没有 Agent");
    await user.click(screen.getByRole("button", { name: "创建 Agent" }));
    const dialog = await screen.findByRole("dialog", { name: "创建 Agent" });
    expect(
      await within(dialog).findByText("没有可用的电脑"),
    ).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "创建" })).toBeDisabled();
  });

  it("从设置页进入时默认共享", async () => {
    const user = userEvent.setup();
    const created: Call[] = [];
    renderSettings("/o/o1/settings/agents", routes(created));
    await user.click(await screen.findByRole("button", { name: "新建 Agent" }));
    const dialog = await screen.findByRole("dialog", { name: "创建 Agent" });
    await user.type(
      within(dialog).getByRole("textbox", { name: "名称" }),
      "段小誉",
    );
    await waitFor(() =>
      expect(
        within(dialog).getByRole("button", { name: "创建" }),
      ).toBeEnabled(),
    );
    await user.click(within(dialog).getByRole("button", { name: "创建" }));
    await waitFor(() => expect(created).toHaveLength(1));
    expect(created[0]!.body).toMatchObject({ visibility: "SHARED" });
  });
});

describe("Agent 设置抽屉", () => {
  const envList = (over: Partial<AgentEnvVarList> = {}): AgentEnvVarList => ({
    env_vars: [
      {
        name: "ANTHROPIC_BASE_URL",
        value: "https://proxy.local",
        has_value: true,
        version: 1,
        updated_at: TS,
      },
    ],
    can_view_values: true,
    secrets_available: true,
    ...over,
  });

  function drawerRoutes(
    record: Call[],
    env: AgentEnvVarList = envList(),
  ): Route {
    const directory = {
      entries: [entry("a1", { name: "熊孩子" })],
      archived_count: 0,
      truncated: false,
    };
    return base((call) => {
      const path = call.path;
      if (call.method === "GET" && path === `${ORG}/agent-directory`)
        return json(200, directory);
      if (call.method === "GET" && path === `${ORG}/spaces/s1/agents/a1`)
        return json(200, agent("a1", { name: "熊孩子" }));
      if (call.method === "GET" && path.endsWith("/a1/stats"))
        return json(200, {
          task_count: 1,
          run_count: 5,
          input_tokens: 600000,
          output_tokens: 97700,
          last_run_at: null,
        });
      if (call.method === "GET" && path.endsWith("/a1/env-vars"))
        return json(200, env);
      if (call.method === "GET" && path.endsWith("/a1/repos"))
        return json(200, { repos: [] });
      if (call.method === "PATCH" && path.endsWith("/a1/profile")) {
        record.push(call);
        return json(
          200,
          agent("a1", {
            name: (call.body as { name?: string }).name ?? "熊孩子",
            profile_version: 4,
          }),
        );
      }
      if (call.method === "POST" && path.endsWith("/a1/config-versions")) {
        record.push(call);
        return json(201, agent("a1", { name: "熊孩子", version: 6 }));
      }
      return undefined;
    });
  }

  it("触发设置的 IM 入口打开真实绑定弹框", async () => {
    const user = userEvent.setup();
    const route = drawerRoutes([]);
    renderSettings("/o/o1/agents?agentSettings=a1", (call) =>
      call.path.endsWith("/feishu/bots")
        ? json(200, {
            app_connected: true,
            assistants: [],
            agents: [],
            can_bind_agent: true,
            builtin_prompt: "",
          })
        : route(call),
    );
    const drawer = await screen.findByRole("complementary", {
      name: "熊孩子 的设置",
    });
    await user.click(
      within(drawer).getByRole("button", { name: /触发 & IM 集成/ }),
    );
    await user.click(
      await within(drawer).findByRole("button", { name: "添加 IM 集成" }),
    );
    expect(
      await screen.findByRole("dialog", { name: "添加 IM 集成" }),
    ).toBeVisible();
    expect(
      await screen.findByRole("button", { name: "创建飞书智能体" }),
    ).toBeEnabled();
    await user.click(screen.getByRole("button", { name: "创建飞书智能体" }));
    await user.keyboard("{Escape}");
    expect(screen.getByRole("dialog", { name: "添加 IM 集成" })).toBeVisible();
    expect(drawer).toBeVisible();
  });

  it("资料行内编辑带 profile_version", async () => {
    const user = userEvent.setup();
    const record: Call[] = [];
    renderSettings("/o/o1/agents?agentSettings=a1", drawerRoutes(record));
    const drawer = await screen.findByRole("complementary", {
      name: "熊孩子 的设置",
    });
    expect(await within(drawer).findByText("697.7K")).toBeInTheDocument();
    await user.click(within(drawer).getByRole("button", { name: "熊孩子" }));
    const input = within(drawer).getByRole("textbox", { name: "名称" });
    await user.clear(input);
    await user.type(input, "熊大{Enter}");
    await waitFor(() => expect(record).toHaveLength(1));
    expect(record[0]!.body).toEqual({ name: "熊大", profile_version: 3 });
  });

  it("运行设置：分钟、小时换算为秒，带 expected_agent_version", async () => {
    const user = userEvent.setup();
    const record: Call[] = [];
    renderSettings("/o/o1/agents?agentSettings=a1", drawerRoutes(record));
    const drawer = await screen.findByRole("complementary", {
      name: "熊孩子 的设置",
    });
    await user.click(within(drawer).getByRole("button", { name: /运行设置/ }));
    const minutes = within(drawer).getByRole("spinbutton", {
      name: "单任务最大运行时长",
    });
    await user.clear(minutes);
    await user.type(minutes, "120");
    const hours = within(drawer).getByRole("spinbutton", {
      name: "排队超时时间",
    });
    await user.clear(hours);
    await user.type(hours, "0.5");
    await user.click(
      within(screen.getByRole("region", { name: "运行设置" })).getByRole(
        "button",
        { name: "保存" },
      ),
    );
    await waitFor(() => expect(record).toHaveLength(1));
    expect(record[0]!.body).toMatchObject({
      run_timeout_seconds: 7200,
      queue_timeout_seconds: 1800,
      expected_agent_version: 5,
      computer_id: "d1",
      model: "default",
    });
  });

  it("运行时长超出范围时禁用保存", async () => {
    const user = userEvent.setup();
    renderSettings("/o/o1/agents?agentSettings=a1", drawerRoutes([]));
    const drawer = await screen.findByRole("complementary", {
      name: "熊孩子 的设置",
    });
    await user.click(within(drawer).getByRole("button", { name: /运行设置/ }));
    const minutes = within(drawer).getByRole("spinbutton", {
      name: "单任务最大运行时长",
    });
    await user.clear(minutes);
    await user.type(minutes, "500");
    expect(screen.getByText("请检查标红的设置")).toBeInTheDocument();
    expect(
      within(screen.getByRole("region", { name: "运行设置" })).getByRole(
        "button",
        { name: "保存" },
      ),
    ).toBeDisabled();
  });

  it("默认模型去掉 Runtime 重复默认项，具体模型仍能选择并保存", async () => {
    const user = userEvent.setup();
    const record: Call[] = [];
    const routes = drawerRoutes(record);
    const computer = device("d1");
    computer.runtimes[0]!.models = [
      { id: "default", display_name: "Default", is_default: true },
      { id: "gpt-5.1", display_name: "GPT 5.1", is_default: false },
    ];
    renderSettings("/o/o1/agents?agentSettings=a1", (call) => {
      if (call.method === "GET" && call.path === `${ORG}/devices`)
        return json(200, { devices: [computer], next_cursor: null });
      return routes(call);
    });
    const drawer = await screen.findByRole("complementary", {
      name: "熊孩子 的设置",
    });
    await user.click(
      within(drawer).getByRole("button", { name: /电脑 & Runtime/ }),
    );
    await user.click(within(drawer).getByRole("button", { name: "默认模型" }));
    const models = await screen.findByRole("listbox", { name: "默认模型" });
    const model = await within(models).findByRole("option", {
      name: "GPT 5.1",
    });
    expect(
      within(models).getAllByRole("option", { selected: true }),
    ).toHaveLength(1);
    expect(
      within(models).queryByRole("option", { name: "Default" }),
    ).not.toBeInTheDocument();
    expect(
      within(models).getByRole("option", { name: "手动设置模型" }),
    ).toBeInTheDocument();
    await user.click(model);
    await waitFor(() => expect(record).toHaveLength(1));
    expect(record[0]!.body).toMatchObject({
      model: "gpt-5.1",
      expected_agent_version: 5,
    });
  });

  it("环境变量：值默认遮盖，悬停显示原值", async () => {
    const user = userEvent.setup();
    renderSettings("/o/o1/agents?agentSettings=a1", drawerRoutes([]));
    const drawer = await screen.findByRole("complementary", {
      name: "熊孩子 的设置",
    });
    await user.click(
      within(drawer).getByRole("button", { name: /电脑 & Runtime/ }),
    );
    const cell = await within(drawer).findByTestId(
      "env-value-ANTHROPIC_BASE_URL",
    );
    expect(cell).toHaveTextContent("••••");
    await user.hover(cell);
    expect(cell).toHaveTextContent("https://proxy.local");
    await user.unhover(cell);
    expect(cell).toHaveTextContent("••••");
  });

  it("环境变量：无查看权限时显示提示且不能添加；未配置主密钥时提示", async () => {
    const user = userEvent.setup();
    renderSettings(
      "/o/o1/agents?agentSettings=a1",
      drawerRoutes(
        [],
        envList({
          can_view_values: false,
          env_vars: [
            {
              name: "TOKEN",
              value: null,
              has_value: true,
              version: 1,
              updated_at: TS,
            },
          ],
        }),
      ),
    );
    const drawer = await screen.findByRole("complementary", {
      name: "熊孩子 的设置",
    });
    await user.click(
      within(drawer).getByRole("button", { name: /电脑 & Runtime/ }),
    );
    expect(
      await within(drawer).findByText(
        "值已隐藏 —— 只有 Agent 所有者或组织管理员可以查看和编辑。",
      ),
    ).toBeInTheDocument();
    const cell = within(drawer).getByTestId("env-value-TOKEN");
    await user.hover(cell);
    expect(cell).toHaveTextContent("••••");
    const region = within(drawer).getByRole("region", {
      name: "电脑 & Runtime",
    });
    const addButtons = within(region).getAllByRole("button", { name: "添加" });
    expect(addButtons[0]).toBeDisabled();
  });

  it("未配置主密钥时禁止添加环境变量", async () => {
    const user = userEvent.setup();
    renderSettings(
      "/o/o1/agents?agentSettings=a1",
      drawerRoutes([], envList({ secrets_available: false })),
    );
    const drawer = await screen.findByRole("complementary", {
      name: "熊孩子 的设置",
    });
    await user.click(
      within(drawer).getByRole("button", { name: /电脑 & Runtime/ }),
    );
    expect(
      await within(drawer).findByText(
        "服务器未配置密钥加密，暂时不能添加或修改环境变量。",
      ),
    ).toBeInTheDocument();
    const region = within(drawer).getByRole("region", {
      name: "电脑 & Runtime",
    });
    expect(
      within(region).getAllByRole("button", { name: "添加" })[0],
    ).toBeDisabled();
  });

  it("Runtime 卡片有「测试连接」，电脑所有者可点", async () => {
    const user = userEvent.setup();
    renderSettings("/o/o1/agents?agentSettings=a1", drawerRoutes([]));
    const drawer = await screen.findByRole("complementary", {
      name: "熊孩子 的设置",
    });
    await user.click(
      within(drawer).getByRole("button", { name: /电脑 & Runtime/ }),
    );
    const region = within(drawer).getByRole("region", {
      name: "电脑 & Runtime",
    });
    const button = await within(region).findByRole("button", {
      name: "测试连接",
    });
    await waitFor(() => expect(button).toBeEnabled());
  });
});

describe("小队设置", () => {
  function routes(record: Call[], exclusiveConflict = false): Route {
    const directory = {
      entries: [
        entry("c1", { type: "crew", name: "研发小队" }),
        entry("a1", { name: "队长" }),
        entry("a2", { name: "队员" }),
      ],
      archived_count: 0,
      truncated: false,
    };
    return base((call) => {
      if (call.method === "GET" && call.path === `${ORG}/agent-directory`)
        return json(200, directory);
      if (call.method === "GET" && call.path === `${ORG}/spaces/s1/crews/c1`)
        return json(200, crew("c1", { name: "研发小队" }));
      if (
        call.method === "PATCH" &&
        call.path === `${ORG}/spaces/s1/crews/c1`
      ) {
        record.push(call);
        return json(
          200,
          crew("c1", {
            name: "研发小队",
            leader: { id: "a2", name: "队员", title: "" },
            version: 5,
          }),
        );
      }
      if (
        call.method === "PATCH" &&
        call.path === `${ORG}/spaces/s1/crews/c1/members/agents/a2`
      ) {
        record.push(call);
        return exclusiveConflict
          ? apiError(409, "AGENT_CREW_MEMBERSHIP_CONFLICT")
          : json(200, crew("c1", { name: "研发小队" }));
      }
      return undefined;
    });
  }

  it("设为队长：PATCH leader_agent_id 带 version", async () => {
    const user = userEvent.setup();
    const record: Call[] = [];
    renderSettings("/o/o1/settings/crews?crewSettings=c1", routes(record));
    const drawer = await screen.findByRole("complementary", {
      name: "小队 研发小队 的设置",
    });
    await user.click(
      within(drawer).getByRole("button", { name: "队员 的操作" }),
    );
    await user.click(screen.getByRole("menuitem", { name: "设置为队长" }));
    await waitFor(() => expect(record).toHaveLength(1));
    expect(record[0]!.body).toEqual({ leader_agent_id: "a2", version: 4 });
  });

  it("队长不能移出；专属冲突 409 时提示", async () => {
    const user = userEvent.setup();
    const record: Call[] = [];
    renderSettings(
      "/o/o1/settings/crews?crewSettings=c1",
      routes(record, true),
    );
    const drawer = await screen.findByRole("complementary", {
      name: "小队 研发小队 的设置",
    });
    await user.click(
      within(drawer).getByRole("button", { name: "队长 的操作" }),
    );
    expect(screen.getByRole("menuitem", { name: "移出小队" })).toHaveAttribute(
      "aria-disabled",
      "true",
    );
    await user.keyboard("{Escape}");
    await user.click(
      within(drawer).getByRole("button", { name: "队员 的操作" }),
    );
    await user.click(
      screen.getByRole("menuitem", { name: "设为小队专属 Agent" }),
    );
    const confirm = await screen.findByRole("dialog", {
      name: "设为小队专属 Agent？",
    });
    await user.click(within(confirm).getByRole("button", { name: "确认设置" }));
    expect(
      await screen.findByText("这个 Agent 已是其他小队的专属成员"),
    ).toBeInTheDocument();
    expect(record[0]!.body).toEqual({ exclusive: true });
  });
});

describe("成员页签", () => {
  const item = (over: Partial<ContactItem>): ContactItem => ({
    kind: "agent",
    id: "a1",
    name: "熊孩子",
    title: "AI 工程师",
    description: "",
    email: null,
    avatar: null,
    online: true,
    space_id: "s1",
    owner: { id: "u1", email: "lin@openfox.dev" },
    runtime: "codex",
    allow_assign: true,
    allow_direct_chat: true,
    created_by_me: true,
    ...over,
  });
  const contacts = {
    groups: [
      {
        kind: "SPACE",
        space_id: "s1",
        space_name: "默认空间",
        items: [
          item({
            kind: "crew",
            id: "c1",
            name: "前端小队",
            title: null,
            runtime: null,
            allow_assign: null,
            allow_direct_chat: null,
          }),
          item({}),
          item({
            id: "a2",
            name: "运维值班",
            title: "SRE",
            allow_assign: false,
            allow_direct_chat: false,
            online: false,
          }),
        ],
      },
      {
        kind: "HUMANS",
        space_id: null,
        space_name: null,
        items: [
          item({
            kind: "user",
            id: "u2",
            name: "fox@openfox.dev",
            email: "fox@openfox.dev",
            title: null,
            space_id: null,
            owner: null,
            runtime: null,
            allow_assign: null,
            allow_direct_chat: null,
            online: null,
          }),
        ],
      },
    ],
    truncated: false,
  };
  const routes = base((call) =>
    call.method === "GET" && call.path === `${ORG}/contacts`
      ? json(200, contacts)
      : undefined,
  );

  it("没有 ?contact= 时自动选中第一个 Agent", async () => {
    renderSettings("/o/o1/members", routes);
    expect(
      await screen.findByRole("heading", { name: "熊孩子 - AI 工程师" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /熊孩子/, current: true }),
    ).toBeInTheDocument();
  });

  it("不可指派 / 不可单聊时禁用并显示 Tooltip；离线头像带提示", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    renderSettings("/o/o1/members?contact=agent:a2", routes);
    const assign = await screen.findByRole("button", {
      name: "新建或指派任务",
    });
    expect(assign).toHaveAttribute("aria-disabled", "true");
    await user.hover(assign);
    await act(() => vi.advanceTimersByTimeAsync(400));
    expect(screen.getByRole("tooltip")).toHaveTextContent(
      "该 Agent 设置了不可以被指派任务",
    );
    await user.unhover(assign);
    await user.hover(screen.getByRole("button", { name: "对话" }));
    await act(() => vi.advanceTimersByTimeAsync(400));
    expect(screen.getByRole("tooltip")).toHaveTextContent(
      "该 Agent 设置了不可以单聊",
    );
  });

  it("真人只显示可用的「对话」入口", async () => {
    renderSettings("/o/o1/members?contact=user:u2", routes);
    const chat = await screen.findByRole("button", { name: "对话" });
    expect(chat).not.toHaveAttribute("aria-disabled", "true");
    expect(
      screen.queryByRole("button", { name: "新建或指派任务" }),
    ).not.toBeInTheDocument();
  });

  it("邀请：逗号与空格生成 chip，非法地址标红，有非法地址时不发送", async () => {
    const user = userEvent.setup();
    const calls = renderSettings(
      "/o/o1/members",
      base((call) => {
        if (call.method === "GET" && call.path === `${ORG}/contacts`)
          return json(200, contacts);
        if (call.method === "POST" && call.path === `${ORG}/invites`)
          return json(201, {});
        return undefined;
      }),
    );
    await screen.findByRole("heading", { name: "熊孩子 - AI 工程师" });
    await user.click(screen.getByRole("button", { name: "新建" }));
    await user.click(screen.getByRole("menuitem", { name: "邀请成员" }));
    const dialog = await screen.findByRole("dialog", { name: "邀请" });
    const input = within(dialog).getByRole("textbox", { name: "邮箱地址" });
    await user.type(input, "alice@company.com,bob@company ");
    const bad = within(dialog).getByText("bob@company");
    expect(bad.closest("[data-invalid]")).not.toBeNull();
    expect(
      within(dialog).getByText("有 1 个邮箱格式不正确"),
    ).toBeInTheDocument();
    // 有无效邮箱时点「发送邀请」只提示，不发请求
    await user.click(within(dialog).getByRole("button", { name: "发送邀请" }));
    expect(
      await screen.findByText("发送邀请前请输入有效邮箱地址"),
    ).toBeInTheDocument();
    expect(findCall(calls, "POST", `${ORG}/invites`)).toBeUndefined();
    await user.click(
      within(dialog).getByRole("button", { name: "移除 bob@company" }),
    );
    await user.click(within(dialog).getByRole("button", { name: "发送邀请" }));
    expect(await screen.findByText("邀请已发送")).toBeInTheDocument();
    expect(findCall(calls, "POST", `${ORG}/invites`)?.body).toMatchObject({
      email: "alice@company.com",
      role: "MEMBER",
    });
  });

  it("成员角色看不到「邀请成员」", async () => {
    const user = userEvent.setup();
    renderSettings("/o/o1/members", routes, "MEMBER");
    await screen.findByRole("heading", { name: "熊孩子 - AI 工程师" });
    await user.click(screen.getByRole("button", { name: "新建" }));
    expect(
      screen.queryByRole("menuitem", { name: "邀请成员" }),
    ).not.toBeInTheDocument();
  });
});
