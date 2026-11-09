import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { App, createQueryClient } from "../App";
import {
  backend,
  json,
  ORG,
  SPACE,
  type Call,
  type Override,
} from "../tasks/test-backend";
import { apiFormValid, buildApiRequest, EMPTY_API_FORM } from "./model";

// Jira API 导入向导、CSV 新建空间、回滚框与来源显示。

const o = `/v1/organizations/${ORG}`;
const TS = "2026-10-20T08:00:00Z";
const SECRET = "tok-super-secret-123";
const page = `/o/${ORG}/settings/import-export`;

const space = {
  id: SPACE,
  prefix: "OPEN",
  name: "默认空间",
  visibility: "shared",
  version: 1,
  created_at: TS,
  features: { views: false, projects: false, iterations: false, triage: false },
  status: "active",
  my_role: "OWNER",
  can_manage: true,
};

const options = {
  import_open: true,
  import_done: true,
  migrated_label: false,
  overwrite: false,
  import_sprints: false,
  keep_numbers: false,
};

const apiJob = (extra: Record<string, unknown> = {}) => ({
  id: "a1",
  space_id: SPACE,
  space_name: "默认空间",
  new_space: null,
  space_created: false,
  fetch: { fetched: 0, total: 250 },
  source: "jira_cloud",
  source_label: "acme.atlassian.net",
  file_name: "",
  status: "DRAFT",
  phase: "fetching",
  operation_id: "op_a1",
  total_count: 0,
  processed_count: 0,
  created_count: 0,
  updated_count: 0,
  skipped_count: 0,
  failed_count: 0,
  warnings: [],
  error_code: "",
  error_message: "",
  created_by: "u1",
  created_by_email: "lin@openfox.dev",
  created_at: TS,
  started_at: null,
  finished_at: null,
  rolled_back_at: null,
  retry_at: null,
  version: 1,
  config_version: 1,
  rollback: { eligible: false, reason: "not_finished", deadline: null },
  config: { columns: [], statuses: [], users: [], options },
  ...extra,
});

const analysis = (
  caps: Record<string, unknown> = {},
  user: Record<string, unknown> = {},
) => ({
  capabilities: {
    sprints: false,
    keep_numbers: false,
    sprint_count: 0,
    ...caps,
  },
  statuses: [
    { source: "Done", count: 2, done: true, suggested_status_id: null },
  ],
  users: [
    {
      source_id: "acc-ann",
      name: "Ann",
      email: "ann@x.com",
      suggest_reason: "email",
      count: 3,
      suggested_user_id: "u2",
      ...user,
    },
  ],
  types: [{ source: "Bug", count: 1, type_id: null }],
  counts: { total: 250, open: 100, done: 150, invalid: 0 },
  overwrite: { existing: 0, modified: 0 },
  members: { member: 1, invite: 0, external: 0, skip: 0 },
  issues: [],
});

const ready = (
  caps: Record<string, unknown> = {},
  extra = {},
  user: Record<string, unknown> = {},
) =>
  apiJob({
    phase: "",
    version: 2,
    fetch: { fetched: 250, total: 250 },
    analysis: analysis(caps, user),
    ...extra,
  });

function compose(...routes: Override[]): Override {
  return (call) => {
    for (const route of routes) {
      const response = route(call);
      if (response) return response;
    }
    return undefined;
  };
}

const base: Override = (call) => {
  if (call.method === "GET" && call.path === `${o}/imports`)
    return json(200, {
      imports: [],
      next_cursor: "",
      has_active: false,
      can_manage: true,
      total: 0,
    });
  if (call.path === `${o}/spaces`) return json(200, { spaces: [space] });
  if (call.path === `${o}/members` && call.method === "GET")
    return json(200, {
      members: [
        {
          user_id: "u2",
          email: "ann@x.com",
          role: "MEMBER",
          status: "ACTIVE",
          authorization_version: 1,
          created_at: TS,
          spaces: [],
        },
      ],
      next_cursor: "",
    });
  return undefined;
};

function renderAt(path: string, ...routes: Override[]) {
  const server = backend(compose(...routes, base));
  const queryClient = createQueryClient();
  queryClient.setDefaultOptions({ queries: { retry: false } });
  render(
    <MemoryRouter initialEntries={[path]}>
      <App api={server.api} queryClient={queryClient} locale="zh-CN" />
    </MemoryRouter>,
  );
  return server;
}

const apiPage = `${page}?source=jira&method=api`;

async function pick(
  user: ReturnType<typeof userEvent.setup>,
  name: string,
  option: string,
) {
  await user.click(screen.getByRole("button", { name }));
  await user.click(await screen.findByRole("option", { name: option }));
}

/** 填完 Cloud 配置表单（目标空间选已有空间）。 */
async function fillCloud(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByRole("button", { name: "下一步" }));
  await user.type(
    await screen.findByLabelText("站点地址"),
    "https://acme.atlassian.net",
  );
  await user.type(screen.getByLabelText("邮箱"), "lin@acme.com");
  await user.type(screen.getByLabelText("API 令牌"), SECRET);
  await user.type(screen.getByLabelText("项目 Key"), "proj");
  await pick(user, "目标空间", "默认空间");
}

beforeEach(() => {
  localStorage.setItem(`openfox:onboarding:${ORG}`, JSON.stringify({}));
});
afterEach(() => localStorage.clear());

describe("API 配置的纯函数", () => {
  it("校验站点与凭证；请求体按部署方式组装；前缀默认取项目 Key 大写", () => {
    const form = {
      ...EMPTY_API_FORM,
      siteUrl: "https://acme.atlassian.net",
      email: "a@b.com",
      secret: "s",
      projectKey: "proj",
    };
    expect(apiFormValid(form)).toBe(true);
    expect(
      apiFormValid({ ...form, siteUrl: "http://acme.atlassian.net" }),
    ).toBe(false);
    expect(
      apiFormValid({ ...form, siteUrl: "https://u:p@acme.atlassian.net" }),
    ).toBe(false);
    expect(apiFormValid({ ...form, secret: "" })).toBe(false);
    expect(
      buildApiRequest(form, {
        spaceId: "__new_space__",
        newSpace: { name: "新空间", prefix: "", visibility: "SHARED" },
      }),
    ).toEqual({
      deployment: "cloud",
      site_url: "https://acme.atlassian.net",
      auth: { kind: "api_token", email: "a@b.com", secret: "s" },
      project_key: "proj",
      new_space: { name: "新空间", prefix: "PROJ", visibility: "SHARED" },
    });
    const server = {
      ...form,
      deployment: "server" as const,
      serverAuth: "password" as const,
      username: "admin",
      jql: " status = Open ",
    };
    expect(
      buildApiRequest(server, {
        spaceId: "s1",
        newSpace: { name: "", prefix: "", visibility: "PRIVATE" },
      }),
    ).toMatchObject({
      deployment: "server",
      auth: { kind: "password", username: "admin", secret: "s" },
      jql: "status = Open",
      space_id: "s1",
    });
  });
});

describe("Jira API 向导", () => {
  it("默认进入 CSV 向导，上传步骤直接可用", async () => {
    const user = userEvent.setup();
    renderAt(`${page}?source=jira`);
    expect(await screen.findByRole("button", { name: "下一步" })).toBeEnabled();
    expect(
      screen.getByRole("region", { name: "导入 Jira 数据" }),
    ).toBeVisible();
    // 默认是 CSV 向导
    await user.click(screen.getByRole("button", { name: "下一步" }));
    expect(await screen.findByText("把 CSV 文件拖到这里")).toBeVisible();
  });

  it("配置校验：站点必须 https；私有化部署切换认证方式和字段", async () => {
    const user = userEvent.setup();
    renderAt(apiPage);
    await user.click(await screen.findByRole("button", { name: "下一步" }));
    const submit = await screen.findByRole("button", { name: "验证并获取" });
    expect(submit).toBeDisabled();
    await user.type(screen.getByLabelText("站点地址"), "http://acme.com");
    expect(screen.getByText("站点地址需要以 https:// 开头")).toBeVisible();
    // Cloud 字段
    expect(screen.getByLabelText("邮箱")).toBeVisible();
    expect(screen.getByLabelText("API 令牌")).toBeVisible();
    await user.click(screen.getByRole("radio", { name: "私有化部署" }));
    expect(screen.queryByLabelText("邮箱")).toBeNull();
    expect(screen.getByLabelText("个人访问令牌")).toBeVisible();
    await pick(user, "认证方式", "用户名加密码");
    expect(screen.getByLabelText("用户名")).toBeVisible();
    expect(screen.getByLabelText("密码")).toBeVisible();
    // 「使用内网代理」禁用
    expect(screen.getByRole("switch", { name: "使用内网代理" })).toBeDisabled();
    expect(
      screen.getByText("令牌仅提交一次，加密存储，接口不会返回"),
    ).toBeVisible();
  });

  it("新建空间：前缀默认取项目 Key 大写，请求带 new_space 而不带 space_id", async () => {
    const user = userEvent.setup();
    const creates: Call[] = [];
    renderAt(apiPage, (call) => {
      if (call.path === `${o}/imports/jira-api` && call.method === "POST") {
        creates.push(call);
        return json(
          201,
          apiJob({
            space_id: null,
            space_name: "新空间",
            new_space: { name: "新空间", prefix: "PROJ", visibility: "SHARED" },
          }),
        );
      }
      if (call.path === `${o}/imports/a1` && call.method === "GET")
        return json(200, ready({ keep_numbers: true }));
      return undefined;
    });
    await user.click(await screen.findByRole("button", { name: "下一步" }));
    await user.type(
      await screen.findByLabelText("站点地址"),
      "https://acme.atlassian.net",
    );
    await user.type(screen.getByLabelText("邮箱"), "lin@acme.com");
    await user.type(screen.getByLabelText("API 令牌"), SECRET);
    await user.type(screen.getByLabelText("项目 Key"), "proj");
    await pick(user, "目标空间", "新建空间…");
    const submit = screen.getByRole("button", { name: "验证并获取" });
    expect(submit).toBeDisabled();
    await user.type(screen.getByLabelText("空间名称"), "新空间");
    expect(screen.getByLabelText("空间前缀")).toHaveValue("PROJ");
    await user.click(screen.getByRole("radio", { name: "共享" }));
    expect(submit).toBeEnabled();
    await user.click(submit);
    await waitFor(() => expect(creates).toHaveLength(1));
    expect(creates[0]!.body).toEqual({
      deployment: "cloud",
      site_url: "https://acme.atlassian.net",
      auth: { kind: "api_token", email: "lin@acme.com", secret: SECRET },
      project_key: "proj",
      new_space: { name: "新空间", prefix: "PROJ", visibility: "SHARED" },
    });
  });

  it("连接失败：按 reason 显示标题、排查步骤和可复制的操作编号，表单里的令牌被清空", async () => {
    const user = userEvent.setup();
    const clipboard = vi.fn(() => Promise.resolve());
    Object.defineProperty(navigator, "clipboard", {
      value: { writeText: clipboard },
      configurable: true,
    });
    const reasons = ["auth_failed", "blocked_address"];
    renderAt(apiPage, (call) => {
      if (call.path === `${o}/imports/jira-api` && call.method === "POST")
        return json(400, {
          error: {
            code: "JIRA_CONNECTION_FAILED",
            message: "x",
            request_id: "op_conn1",
            details: { reason: reasons.shift(), status: 401 },
          },
        });
      return undefined;
    });
    await fillCloud(user);
    await user.click(screen.getByRole("button", { name: "验证并获取" }));
    expect(await screen.findByText("凭证验证失败")).toBeVisible();
    expect(screen.getByText("确认令牌没有过期，账号没有被停用")).toBeVisible();
    await user.click(
      screen.getByRole("button", { name: "复制操作编号 op_conn1" }),
    );
    expect(clipboard).toHaveBeenCalledWith("op_conn1");
    expect(screen.getByLabelText("API 令牌")).toHaveValue("");

    await user.type(screen.getByLabelText("API 令牌"), SECRET);
    await user.click(screen.getByRole("button", { name: "验证并获取" }));
    expect(await screen.findByText("地址被拦截")).toBeVisible();
    expect(screen.queryByText("凭证验证失败")).toBeNull();
  });

  it("503 SECRETS_UNAVAILABLE 提示服务器未配置加密密钥", async () => {
    const user = userEvent.setup();
    renderAt(apiPage, (call) => {
      if (call.path === `${o}/imports/jira-api`)
        return json(503, {
          error: { code: "SECRETS_UNAVAILABLE", message: "x", request_id: "r" },
        });
      return undefined;
    });
    await fillCloud(user);
    await user.click(screen.getByRole("button", { name: "验证并获取" }));
    expect(
      await screen.findByText("服务器未配置加密密钥，暂不能用 API 凭证导入"),
    ).toBeVisible();
  });

  it("检查：拉取中轮询进度，拉完后显示统计；之后的请求体里没有令牌", async () => {
    const user = userEvent.setup();
    const polls = { n: 0 };
    const puts: Call[] = [];
    renderAt(apiPage, (call) => {
      if (call.path === `${o}/imports/jira-api` && call.method === "POST")
        return json(201, apiJob());
      if (call.path === `${o}/imports/a1` && call.method === "GET") {
        polls.n++;
        return polls.n === 1
          ? json(200, apiJob({ fetch: { fetched: 100, total: 250 } }))
          : json(200, ready({ sprints: true, sprint_count: 2 }));
      }
      if (call.path === `${o}/imports/a1/config` && call.method === "PUT") {
        puts.push(call);
        return json(
          200,
          ready({ sprints: true, sprint_count: 2 }, { config_version: 2 }),
        );
      }
      return undefined;
    });
    await fillCloud(user);
    await user.click(screen.getByRole("button", { name: "验证并获取" }));
    expect(await screen.findByText("已获取 100 / 250 条")).toBeVisible();
    expect(screen.getByRole("button", { name: "下一步" })).toBeDisabled();
    // 2 秒后的下一次轮询拉完
    await waitFor(() => expect(screen.getByText("Sprint")).toBeVisible(), {
      timeout: 5000,
    });
    expect(screen.getByText("250")).toBeVisible();
    expect(screen.getByRole("button", { name: "下一步" })).toBeEnabled();
    await user.click(screen.getByRole("button", { name: "下一步" }));
    // 选择任务
    expect(
      await screen.findByText("选择任务", { selector: "h2" }),
    ).toBeVisible();
    await user.click(
      screen.getByRole("switch", { name: "导入已完成的任务（150）" }),
    );
    await user.click(screen.getByRole("button", { name: "下一步" }));
    await waitFor(() => expect(puts).toHaveLength(1));
    expect(
      (puts[0]!.body as { config: { options: Record<string, boolean> } }).config
        .options,
    ).toMatchObject({ import_open: true, import_done: false });
    expect(JSON.stringify(puts[0]!.body)).not.toContain(SECRET);
  }, 15000);

  it("没有总数时只显示已获取数", async () => {
    const user = userEvent.setup();
    renderAt(apiPage, (call) => {
      if (call.path === `${o}/imports/jira-api`)
        return json(201, apiJob({ fetch: { fetched: 40, total: null } }));
      if (call.path === `${o}/imports/a1`)
        return json(200, apiJob({ fetch: { fetched: 40, total: null } }));
      return undefined;
    });
    await fillCloud(user);
    await user.click(screen.getByRole("button", { name: "验证并获取" }));
    expect(await screen.findByText("已获取 40 条")).toBeVisible();
  });

  it("拉取失败：显示错误面板，返回修改配置会丢弃草稿并回到配置", async () => {
    const user = userEvent.setup();
    const cancels: Call[] = [];
    renderAt(apiPage, (call) => {
      if (call.path === `${o}/imports/jira-api`) return json(201, apiJob());
      if (call.path === `${o}/imports/a1` && call.method === "GET")
        return json(
          200,
          apiJob({
            status: "DISCARDED",
            version: 2,
            error_code: "JIRA_FETCH_FAILED",
            error_message: "Jira 持续返回 500",
          }),
        );
      if (call.path === `${o}/imports/a1/cancel`) {
        cancels.push(call);
        return json(200, apiJob({ status: "DISCARDED" }));
      }
      return undefined;
    });
    await fillCloud(user);
    await user.click(screen.getByRole("button", { name: "验证并获取" }));
    expect(await screen.findByText("Jira 持续返回 500")).toBeVisible();
    expect(screen.getByText("获取失败")).toBeVisible();
    await user.click(screen.getByRole("button", { name: "返回修改配置" }));
    expect(await screen.findByLabelText("站点地址")).toHaveValue(
      "https://acme.atlassian.net",
    );
    // 令牌不会被保留
    expect(screen.getByLabelText("API 令牌")).toHaveValue("");
  });

  it("选项：迭代与保留票号按 capabilities 启用；确认页显示新建空间", async () => {
    const user = userEvent.setup();
    const puts: Call[] = [];
    const newSpace = { name: "新空间", prefix: "PROJ", visibility: "PRIVATE" };
    const withCaps = (caps: Record<string, unknown>) =>
      ready(caps, { space_id: null, new_space: newSpace, config_version: 2 });
    renderAt(apiPage, (call) => {
      if (call.path === `${o}/imports/jira-api`)
        return json(
          201,
          withCaps({ sprints: true, keep_numbers: true, sprint_count: 2 }),
        );
      if (call.path === `${o}/imports/a1/config` && call.method === "PUT") {
        puts.push(call);
        return json(
          200,
          withCaps({ sprints: true, keep_numbers: true, sprint_count: 2 }),
        );
      }
      if (call.path === `${o}/imports/a1` && call.method === "GET")
        return json(
          200,
          withCaps({ sprints: true, keep_numbers: true, sprint_count: 2 }),
        );
      return undefined;
    });
    await fillCloud(user);
    await user.click(screen.getByRole("button", { name: "验证并获取" }));
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "下一步" })).toBeEnabled(),
    );
    for (let i = 0; i < 4; i++)
      await user.click(await screen.findByRole("button", { name: "下一步" }));
    expect(
      await screen.findByText("导入选项", { selector: "h2" }),
    ).toBeVisible();
    expect(screen.getByRole("switch", { name: "导入迭代" })).toBeEnabled();
    expect(screen.getByRole("switch", { name: "保留票号" })).toBeEnabled();
    expect(
      screen.getByRole("switch", { name: "导入后与 Jira 双向同步" }),
    ).toBeDisabled();
    await user.click(screen.getByRole("switch", { name: "保留票号" }));
    await user.click(screen.getByRole("switch", { name: "导入迭代" }));
    await user.click(screen.getByRole("button", { name: "下一步" }));
    expect(await screen.findByText("新建空间 新空间（PROJ）")).toBeVisible();
    expect(
      (puts.at(-1)!.body as { config: { options: Record<string, boolean> } })
        .config.options,
    ).toMatchObject({ keep_numbers: true, import_sprints: true });
  }, 15000);

  it("仅在明确开启同步后提交本草稿的已验证账号票据", async () => {
    const user = userEvent.setup();
    const requests: Call[] = [];
    const enabled = () => ready({ project_sync: true });
    renderAt(apiPage, (call) => {
      if (call.path === `${o}/imports/jira-api`)
        return json(201, { ...enabled(), sync_ticket: "sealed-draft-only" });
      if (
        call.path === `${o}/imports/a1/config` ||
        call.path === `${o}/imports/a1`
      )
        return json(200, enabled());
      if (call.path === `${o}/imports/a1/start`) {
        requests.push(call);
        return json(200, apiJob({ status: "QUEUED" }));
      }
      return undefined;
    });
    await fillCloud(user);
    await user.click(screen.getByRole("button", { name: "验证并获取" }));
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "下一步" })).toBeEnabled(),
    );
    for (let i = 0; i < 4; i++)
      await user.click(await screen.findByRole("button", { name: "下一步" }));
    const toggle = screen.getByRole("switch", {
      name: "导入后与 Jira 双向同步",
    });
    expect(toggle).not.toBeChecked();
    await user.click(toggle);
    await user.click(screen.getByRole("button", { name: "下一步" }));
    await user.click(await screen.findByRole("button", { name: "开始导入" }));
    await waitFor(() => expect(requests).toHaveLength(1));
    expect(requests[0]?.body).toMatchObject({
      sync_ticket: "sealed-draft-only",
    });
    expect(JSON.stringify(requests[0]?.body)).not.toContain(SECRET);
  }, 15000);

  it("从新建同步入口进入时预选持续同步，仍须确认导入", async () => {
    const user = userEvent.setup();
    const enabled = () => ready({ project_sync: true });
    const requests: Call[] = [];
    renderAt(`${apiPage}&sync=1`, (call) => {
      if (call.path === `${o}/imports/jira-api`)
        return json(201, { ...enabled(), sync_ticket: "sealed-draft-only" });
      if (
        call.path === `${o}/imports/a1/config` ||
        call.path === `${o}/imports/a1`
      )
        return json(200, enabled());
      if (call.path === `${o}/imports/a1/start`) {
        requests.push(call);
        return json(200, apiJob({ status: "QUEUED" }));
      }
      return undefined;
    });
    await fillCloud(user);
    await user.click(screen.getByRole("button", { name: "验证并获取" }));
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "下一步" })).toBeEnabled(),
    );
    for (let index = 0; index < 4; index++)
      await user.click(await screen.findByRole("button", { name: "下一步" }));
    expect(
      screen.getByRole("switch", { name: "导入后与 Jira 双向同步" }),
    ).toBeChecked();
    expect(requests).toHaveLength(0);
    await user.click(screen.getByRole("button", { name: "下一步" }));
    await user.click(await screen.findByRole("button", { name: "开始导入" }));
    await waitFor(() => expect(requests).toHaveLength(1));
    expect(requests[0]?.body).toMatchObject({
      sync_ticket: "sealed-draft-only",
    });
  }, 15000);

  it("选项：capabilities 为 false 时迭代与保留票号禁用", async () => {
    const user = userEvent.setup();
    renderAt(apiPage, (call) => {
      if (call.path === `${o}/imports/jira-api`) return json(201, ready());
      if (call.path === `${o}/imports/a1/config`)
        return json(200, ready({}, { config_version: 2 }));
      if (call.path === `${o}/imports/a1`) return json(200, ready());
      return undefined;
    });
    await fillCloud(user);
    await user.click(screen.getByRole("button", { name: "验证并获取" }));
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "下一步" })).toBeEnabled(),
    );
    for (let i = 0; i < 4; i++)
      await user.click(await screen.findByRole("button", { name: "下一步" }));
    expect(
      await screen.findByText("导入选项", { selector: "h2" }),
    ).toBeVisible();
    expect(screen.getByRole("switch", { name: "导入迭代" })).toBeDisabled();
    expect(screen.getByRole("switch", { name: "保留票号" })).toBeDisabled();
    // API 导入的「导入未完成 / 已完成」在选择任务步骤里
    expect(screen.queryByRole("switch", { name: "导入未完成任务" })).toBeNull();
  }, 15000);

  it("映射用户：suggest_reason 为 email 时显示「邮箱匹配」，为 name 时不显示", async () => {
    const user = userEvent.setup();
    renderAt(apiPage, (call) => {
      if (call.path === `${o}/imports/jira-api`) return json(201, ready());
      if (call.path === `${o}/imports/a1/config`)
        return json(200, ready({}, { config_version: 2 }));
      if (call.path === `${o}/imports/a1`) return json(200, ready());
      return undefined;
    });
    await fillCloud(user);
    await user.click(screen.getByRole("button", { name: "验证并获取" }));
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "下一步" })).toBeEnabled(),
    );
    for (let i = 0; i < 3; i++)
      await user.click(await screen.findByRole("button", { name: "下一步" }));
    expect(
      await screen.findByText("映射用户", { selector: "h2" }),
    ).toBeVisible();
    expect(screen.getByText("邮箱匹配")).toBeVisible();
    expect(
      screen.getByRole("button", { name: "Ann 对应的成员" }),
    ).toHaveTextContent("ann@x.com");
  }, 15000);

  const byName = (extra: Record<string, unknown> = {}) =>
    ready({}, extra, { suggest_reason: "name" });

  it("映射用户：suggest_reason 为 verified 时显示「本人已验证」", async () => {
    const user = userEvent.setup();
    const verified = (extra: Record<string, unknown> = {}) =>
      ready({}, extra, { suggest_reason: "verified" });
    renderAt(apiPage, (call) => {
      if (call.path === `${o}/imports/jira-api`) return json(201, verified());
      if (call.path === `${o}/imports/a1/config`)
        return json(200, verified({ config_version: 2 }));
      if (call.path === `${o}/imports/a1`) return json(200, verified());
      return undefined;
    });
    await fillCloud(user);
    await user.click(screen.getByRole("button", { name: "验证并获取" }));
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "下一步" })).toBeEnabled(),
    );
    for (let i = 0; i < 3; i++)
      await user.click(await screen.findByRole("button", { name: "下一步" }));
    expect(await screen.findByText("本人已验证")).toBeVisible();
    expect(screen.queryByText("邮箱匹配")).toBeNull();
  }, 15000);

  it("映射用户：suggest_reason 为 name 时不显示「邮箱匹配」", async () => {
    const user = userEvent.setup();
    renderAt(apiPage, (call) => {
      if (call.path === `${o}/imports/jira-api`) return json(201, byName());
      if (call.path === `${o}/imports/a1/config`)
        return json(200, byName({ config_version: 2 }));
      if (call.path === `${o}/imports/a1`) return json(200, byName());
      return undefined;
    });
    await fillCloud(user);
    await user.click(screen.getByRole("button", { name: "验证并获取" }));
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "下一步" })).toBeEnabled(),
    );
    for (let i = 0; i < 3; i++)
      await user.click(await screen.findByRole("button", { name: "下一步" }));
    expect(
      await screen.findByText("映射用户", { selector: "h2" }),
    ).toBeVisible();
    expect(screen.queryByText("邮箱匹配")).toBeNull();
    expect(
      screen.getByRole("button", { name: "Ann 对应的成员" }),
    ).toHaveTextContent("ann@x.com");
  }, 15000);
});

describe("CSV 向导 · 新建空间", () => {
  it("上传时带 new_space_*，没填前缀则不带前缀字段", async () => {
    const user = userEvent.setup();
    const uploads: Call[] = [];
    renderAt(`${page}?source=jira`, (call) => {
      if (call.path === `${o}/imports/jira-csv` && call.method === "POST") {
        uploads.push(call);
        return json(
          201,
          apiJob({
            id: "d1",
            source: "jira_csv",
            source_label: "Jira CSV",
            file_name: "jira.csv",
            phase: "",
            fetch: null,
            space_id: null,
            new_space: { name: "新空间", visibility: "PRIVATE" },
            preview: {
              columns: [
                {
                  index: 0,
                  header: "Summary",
                  samples: ["a"],
                  suggested_field: "title",
                },
              ],
              total_rows: 1,
              importable_rows: 1,
              skipped_rows: 0,
              failed_rows: 0,
              row_errors: [],
            },
          }),
        );
      }
      return undefined;
    });
    await user.click(await screen.findByRole("button", { name: "下一步" }));
    await pick(user, "目标空间", "新建空间…");
    expect(screen.getByText("留空则使用文件中票号的前缀")).toBeVisible();
    await user.type(screen.getByLabelText("空间名称"), "新空间");
    await user.upload(
      screen.getByLabelText("选择 CSV 文件"),
      new File(["a,b"], "jira.csv", { type: "text/csv" }),
    );
    await user.click(screen.getByRole("button", { name: "上传并预览" }));
    await waitFor(() => expect(uploads).toHaveLength(1));
    const form = uploads[0]!.body as FormData;
    expect(form.get("new_space_name")).toBe("新空间");
    expect(form.get("new_space_visibility")).toBe("PRIVATE");
    expect(form.has("new_space_prefix")).toBe(false);
    expect(form.has("space_id")).toBe(false);
  });
});

describe("导入记录与回滚框", () => {
  const record = (extra: Record<string, unknown>) =>
    apiJob({
      id: "j1",
      status: "SUCCEEDED",
      phase: "",
      fetch: null,
      version: 3,
      created_count: 8,
      rollback: { eligible: true, reason: "", deadline: TS },
      ...extra,
    });

  it("API 导入记录显示主机名作为来源", async () => {
    renderAt(page, (call) => {
      if (call.method === "GET" && call.path === `${o}/imports`)
        return json(200, {
          imports: [record({})],
          next_cursor: "",
          has_active: false,
          can_manage: true,
          total: 1,
        });
      return undefined;
    });
    const row = await screen.findByTestId("import-j1");
    expect(
      within(row).getByText("acme.atlassian.net → 默认空间"),
    ).toBeVisible();
  });

  it("重新导入 API 记录：预填部署方式、站点、项目、JQL、认证方式与用户，不预填凭证", async () => {
    const user = userEvent.setup();
    renderAt(page, (call) => {
      if (call.method === "GET" && call.path === `${o}/imports`)
        return json(200, {
          imports: [
            record({
              source: "jira_server",
              source_label: "jira.example.com/jira",
              api: {
                deployment: "server",
                site_url: "https://jira.example.com/jira",
                project_key: "PROJ",
                jql: "status = Open",
                auth_kind: "password",
                auth_user: "admin",
              },
            }),
          ],
          next_cursor: "",
          has_active: false,
          can_manage: true,
          total: 1,
        });
      return undefined;
    });
    const row = await screen.findByTestId("import-j1");
    await user.click(within(row).getByRole("button", { name: "更多操作" }));
    await user.click(screen.getByRole("menuitem", { name: "重新导入" }));
    await user.click(await screen.findByRole("button", { name: "下一步" }));
    expect(await screen.findByLabelText("站点地址")).toHaveValue(
      "https://jira.example.com/jira",
    );
    expect(screen.getByRole("radio", { name: "私有化部署" })).toBeChecked();
    expect(screen.getByLabelText("项目 Key")).toHaveValue("PROJ");
    expect(screen.getByLabelText("JQL（可选）")).toHaveValue("status = Open");
    expect(screen.getByLabelText("认证方式")).toHaveTextContent("用户名加密码");
    expect(screen.getByLabelText("用户名")).toHaveValue("admin");
    expect(screen.getByLabelText("密码")).toHaveValue("");
  });

  it("回滚框：说明迭代数，新建空间的导入补一句空间保留", async () => {
    const user = userEvent.setup();
    renderAt(page, (call) => {
      if (call.method === "GET" && call.path === `${o}/imports`)
        return json(200, {
          imports: [record({ space_created: true })],
          next_cursor: "",
          has_active: false,
          can_manage: true,
          total: 1,
        });
      if (call.path === `${o}/imports/j1/rollback`)
        return json(200, {
          eligible: true,
          reason: "",
          deadline: TS,
          created_count: 8,
          modified_total: 0,
          modified: [],
          cleanup: { statuses: 1, labels: 0, iterations: 2 },
          space_created: true,
        });
      return undefined;
    });
    const row = await screen.findByTestId("import-j1");
    await user.click(within(row).getByRole("button", { name: "更多操作" }));
    await user.click(screen.getByRole("menuitem", { name: "删除导入内容" }));
    const dialog = await screen.findByRole("dialog");
    expect(await within(dialog).findByText(/并删除 2 个迭代/)).toBeVisible();
    expect(
      within(dialog).getByText(
        "空间「默认空间」是本次导入新建的，回滚后保留（可在空间设置里删除）",
      ),
    ).toBeVisible();
  });
});
