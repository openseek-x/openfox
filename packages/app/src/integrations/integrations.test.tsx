import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it } from "vitest";

import {
  apiError,
  findCall,
  json,
  renderSettings,
  ts,
  type Route,
} from "../settings/testing";

// 外部应用集成总览与详情。

const o = "/v1/organizations/o1/integrations";

const provider = (
  id: string,
  name: string,
  category: string,
  template: string,
  extra: Record<string, unknown> = {},
) => ({
  id,
  name,
  category,
  availability: "available",
  template,
  capabilities: [],
  configured: true,
  bindable: false,
  summary: { connections: 0, status: "none" },
  ...extra,
});

const catalog = (
  jira: Record<string, unknown> = {},
  github: Record<string, unknown> = {},
) => [
  provider("jira", "Jira", "task", "A", {
    capabilities: ["sync", "import", "personal_account"],
    ...jira,
  }),
  provider("linear", "Linear", "task", "A", { availability: "coming_soon" }),
  provider("asana", "Asana", "task", "A", { availability: "coming_soon" }),
  provider("tapd", "TAPD", "task", "A", { availability: "coming_soon" }),
  provider("github", "GitHub", "git", "B", {
    capabilities: ["sync", "pr_link", "personal_account"],
    ...github,
  }),
  provider("feishu", "飞书", "collaboration", "C1", {
    capabilities: ["im_notify", "task_assistant", "agent_binding"],
  }),
];

const connection = (extra: Record<string, unknown> = {}) => ({
  id: "c1",
  provider: "jira",
  scope: "ORGANIZATION",
  external_id: "acme.atlassian.net",
  display_name: "acme.atlassian.net",
  account_kind: "instance",
  status: "ACTIVE",
  status_reason: "",
  credentials_hint: "",
  created_by: "u1",
  created_by_email: "lin@openfox.dev",
  created_at: ts,
  updated_at: ts,
  disconnected_at: null,
  version: 4,
  ...extra,
});

function overview(providers: unknown[], canManage = true): Route {
  return (call) =>
    call.method === "GET" && call.path === o
      ? json(200, { providers, can_manage: canManage })
      : undefined;
}

describe("外部应用集成总览", () => {
  beforeEach(() => localStorage.clear());

  it("按分组展示，可用在前，即将推出不可点，同一服务商在多个分组出现", async () => {
    renderSettings("/o/o1/settings/integrations", overview(catalog()));
    expect(
      await screen.findByRole("heading", { name: "任务管理工具" }),
    ).toBeVisible();
    // 任务管理：Jira、GitHub issues（附加入口）在前，Linear/Asana/TAPD 在后
    const task = screen.getByRole("region", { name: "任务管理工具" });
    const labels = within(task)
      .getAllByTestId(/^provider-/)
      .map((el) => el.textContent);
    expect(labels[0]).toContain("Jira");
    expect(labels[1]).toContain("GitHub issues");
    expect(labels[2]).toContain("Linear");
    // 即将推出：无链接、有徽标
    const linear = within(task).getByTestId("provider-task:linear");
    expect(linear.tagName).not.toBe("A");
    expect(within(linear).getByText("即将推出")).toBeVisible();
    expect(within(task).getByTestId("provider-task:jira")).toHaveAttribute(
      "href",
      "/o/o1/settings/integrations/jira",
    );
    // GitHub 同时在 Git 组
    const git = screen.getByRole("region", { name: "Git 仓库" });
    expect(within(git).getByTestId("provider-git:github")).toBeVisible();
    // 没有已启用分组
    expect(screen.queryByRole("region", { name: "已启用" })).toBeNull();
  });

  it("已启用分组展示副文案与状态点", async () => {
    renderSettings(
      "/o/o1/settings/integrations",
      overview(
        catalog(
          { summary: { connections: 2, status: "ok" } },
          { summary: { connections: 1, status: "attention" } },
        ),
      ),
    );
    const enabled = await screen.findByRole("region", { name: "已启用" });
    expect(within(enabled).getByText("已绑定 2 个 Jira 实例")).toBeVisible();
    expect(within(enabled).getByText("已绑定 1 个 GitHub 组织")).toBeVisible();
    const dots = within(enabled).getAllByRole("img");
    expect(dots[0]).toHaveAccessibleName("正常");
    expect(dots[1]).toHaveAccessibleName("需处理");
  });
});

const detail = (
  providerId: string,
  extra: Record<string, unknown> = {},
  canManage = true,
) => {
  const p = catalog().find((item) => item.id === providerId)!;
  return {
    provider: p,
    can_manage: canManage,
    connections: [],
    personal_connections: [],
    sync_links: [],
    ...extra,
  };
};

const detailRoute =
  (id: string, body: unknown, more?: Route): Route =>
  (call) =>
    call.method === "GET" && call.path === `${o}/${id}`
      ? json(200, body)
      : more?.(call);

describe("外部应用集成详情", () => {
  beforeEach(() => localStorage.clear());

  it("模板 A：绑定、个人账号、同步与导入分区，空态，绑定按钮禁用", async () => {
    renderSettings(
      "/o/o1/settings/integrations/jira",
      detailRoute("jira", detail("jira")),
    );
    expect(await screen.findByRole("heading", { name: "Jira" })).toBeVisible();
    expect(screen.getByRole("region", { name: "绑定" })).toBeVisible();
    expect(
      screen.getByRole("region", { name: "我的 Jira 账号" }),
    ).toBeVisible();
    expect(
      screen.getByRole("region", { name: "同步 & 导入 Issue 数据" }),
    ).toBeVisible();
    expect(screen.getByText("还没有项目-空间连接")).toBeVisible();
    expect(screen.getByRole("button", { name: "绑定" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "新建同步" })).toBeEnabled();
    expect(
      screen.getByRole("button", { name: "导入 Issue 数据" }),
    ).toBeEnabled();
  });

  it("Jira 新建同步进入 API 导入向导", async () => {
    const user = userEvent.setup();
    renderSettings(
      "/o/o1/settings/integrations/jira",
      detailRoute("jira", detail("jira")),
    );
    await user.click(await screen.findByRole("button", { name: "新建同步" }));
    expect(await screen.findByRole("button", { name: "下一步" })).toBeVisible();
  });

  it("禅道新建同步进入项目导入向导", async () => {
    const user = userEvent.setup();
    renderSettings(
      "/o/o1/settings/integrations/zentao",
      detailRoute("zentao", {
        ...detail("jira"),
        provider: provider("zentao", "禅道", "task", "A", {
          capabilities: ["sync", "import", "personal_account"],
        }),
      }),
    );
    await user.click(await screen.findByRole("button", { name: "新建同步" }));
    expect(await screen.findByRole("button", { name: "继续" })).toBeVisible();
  });

  it("模板 B：GitHub 未配置时提示，分区齐全", async () => {
    const body = detail("github");
    body.provider.configured = false;
    renderSettings(
      "/o/o1/settings/integrations/github",
      detailRoute("github", body),
    );
    expect(await screen.findByText("服务器尚未配置 GitHub 应用")).toBeVisible();
    for (const name of ["同步 Issue 数据", "Git PR & Commit 关联", "更多设置"])
      expect(screen.getByRole("region", { name })).toBeVisible();
  });

  it("模板 C1：飞书只有禁用说明分区", async () => {
    renderSettings(
      "/o/o1/settings/integrations/feishu",
      detailRoute("feishu", detail("feishu")),
    );
    expect(
      await screen.findByRole("region", { name: "任务助手" }),
    ).toBeVisible();
    expect(screen.getByRole("region", { name: "绑定 Agents" })).toBeVisible();
  });

  it("即将推出的服务商显示空态", async () => {
    const body = detail("jira");
    body.provider = catalog().find((item) => item.id === "linear")!;
    renderSettings(
      "/o/o1/settings/integrations/linear",
      detailRoute("linear", body),
    );
    expect(await screen.findByText("即将推出")).toBeVisible();
    expect(screen.queryByRole("region", { name: "绑定" })).toBeNull();
  });

  it("非管理员只读，没有断开菜单", async () => {
    renderSettings(
      "/o/o1/settings/integrations/jira",
      detailRoute(
        "jira",
        detail("jira", { connections: [connection()] }, false),
      ),
      "MEMBER",
    );
    expect(
      await screen.findByText("只有组织所有者和管理员能绑定 Jira"),
    ).toBeVisible();
    expect(screen.getByText("acme.atlassian.net")).toBeVisible();
    expect(screen.queryByRole("button", { name: /更多操作/ })).toBeNull();
  });

  it("断开组织连接：短语不匹配按钮禁用，输入正确后调用接口", async () => {
    const user = userEvent.setup();
    const calls = renderSettings(
      "/o/o1/settings/integrations/jira",
      detailRoute(
        "jira",
        detail("jira", { connections: [connection()] }),
        (call) =>
          call.method === "POST" &&
          call.path === `${o}/connections/c1/disconnect`
            ? json(200, connection({ status: "DISCONNECTED", version: 5 }))
            : undefined,
      ),
    );
    expect(
      await screen.findByText("由 lin@openfox.dev 启用于 2026-10-20"),
    ).toBeVisible();
    expect(screen.getByText("已绑定")).toBeVisible();
    await user.click(
      screen.getByRole("button", { name: "acme.atlassian.net 的更多操作" }),
    );
    await user.click(screen.getByRole("menuitem", { name: "断开" }));
    const dialog = screen.getByRole("dialog");
    const confirm = within(dialog).getByRole("button", { name: "断开" });
    expect(confirm).toBeDisabled();
    const input = within(dialog).getByRole("textbox", { name: "确认短语" });
    await user.type(input, "断开 Jir");
    expect(confirm).toBeDisabled();
    await user.type(input, "a");
    expect(confirm).toBeEnabled();
    await user.click(confirm);
    await waitFor(() =>
      expect(
        findCall(calls, "POST", `${o}/connections/c1/disconnect`)?.body,
      ).toEqual({ version: 4, confirm_phrase: "断开 Jira" }),
    );
    // 成功后详情被重新拉取
    await waitFor(() =>
      expect(
        calls.filter((c) => c.method === "GET" && c.path === `${o}/jira`)
          .length,
      ).toBeGreaterThan(1),
    );
  });

  it("个人连接断开不需要确认短语", async () => {
    const user = userEvent.setup();
    const calls = renderSettings(
      "/o/o1/settings/integrations/jira",
      detailRoute(
        "jira",
        detail("jira", {
          personal_connections: [
            connection({
              id: "p1",
              scope: "PERSONAL",
              display_name: "我的 Jira",
              external_id: "lin@openfox.dev",
              version: 2,
            }),
          ],
        }),
        (call) =>
          call.method === "POST" &&
          call.path === `${o}/connections/p1/disconnect`
            ? json(200, connection({ id: "p1", status: "DISCONNECTED" }))
            : undefined,
      ),
    );
    await user.click(
      await screen.findByRole("button", { name: "我的 Jira 的更多操作" }),
    );
    await user.click(screen.getByRole("menuitem", { name: "断开" }));
    await user.click(
      within(screen.getByRole("dialog")).getByRole("button", { name: "断开" }),
    );
    await waitFor(() =>
      expect(
        findCall(calls, "POST", `${o}/connections/p1/disconnect`)?.body,
      ).toEqual({ version: 2 }),
    );
  });

  it("版本冲突时提示并关闭弹窗", async () => {
    const user = userEvent.setup();
    renderSettings(
      "/o/o1/settings/integrations/jira",
      detailRoute(
        "jira",
        detail("jira", { connections: [connection()] }),
        (call) =>
          call.method === "POST"
            ? apiError(409, "VERSION_CONFLICT")
            : undefined,
      ),
    );
    await user.click(
      await screen.findByRole("button", {
        name: "acme.atlassian.net 的更多操作",
      }),
    );
    await user.click(screen.getByRole("menuitem", { name: "断开" }));
    await user.type(
      screen.getByRole("textbox", { name: "确认短语" }),
      "断开 Jira",
    );
    await user.click(
      within(screen.getByRole("dialog")).getByRole("button", { name: "断开" }),
    );
    expect(await screen.findByText("内容已被他人修改，已刷新")).toBeVisible();
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });
});
