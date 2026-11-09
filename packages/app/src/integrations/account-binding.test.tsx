import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { act } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  apiError,
  findCall,
  json,
  renderSettings,
  ts,
  type Route,
} from "../settings/testing";

// 第三方账号绑定页：14 行状态、四种绑定方式、Jira 对话框、断开确认。

const list = "/v1/organizations/o1/me/account-bindings";
const jiraPath = `${list}/jira`;
const o = "/v1/organizations/o1/integrations";

const ids = [
  ["wechat", "微信"],
  ["github", "GitHub"],
  ["gitlab_cn", "极狐 GitLab.cn"],
  ["gitlab", "GitLab 自部署版"],
  ["gitee", "Gitee"],
  ["jira", "Jira"],
  ["tapd", "TAPD"],
  ["zentao", "禅道"],
  ["pingcode", "PingCode"],
  ["teambition", "Teambition"],
  ["slack", "Slack"],
  ["feishu", "飞书"],
  ["lark", "Lark"],
  ["dingtalk", "钉钉"],
] as const;

const binding = (extra: Record<string, unknown> = {}) => ({
  connection_id: "c1",
  display_name: "lin",
  detail: "",
  avatar_url: "",
  created_at: ts,
  version: 4,
  ...extra,
});

/** 14 行；ready / bound 指定哪些可用与已绑定；instances 给 GitLab 系行的实例列表。 */
function view(
  opts: {
    ready?: string[];
    bound?: Record<string, unknown>;
    sites?: Array<{ site_url: string; deployment: string; host: string }>;
    instances?: Record<string, unknown[]>;
  } = {},
) {
  const ready = opts.ready ?? [];
  return {
    providers: ids.map(([id, name]) => {
      const available = [
        "wechat",
        "github",
        "gitlab_cn",
        "gitlab",
        "gitee",
        "jira",
        "zentao",
        "pingcode",
        "feishu",
      ].includes(id);
      return {
        id,
        name,
        availability: available ? "available" : "coming_soon",
        method: !available
          ? "none"
          : id === "jira"
            ? "form"
            : id === "wechat" || id === "zentao" || id === "pingcode"
              ? "link"
              : "oauth",
        organization_ready: ready.includes(id),
        binding: opts.bound?.[id] ?? null,
        ...(opts.instances?.[id] ? { instances: opts.instances[id] } : {}),
      };
    }),
    jira_sites: opts.sites ?? [],
    email: "lin@openfox.dev",
  };
}

const inst = (
  connection_id: string,
  host: string,
  bound: Record<string, unknown> | null = null,
) => ({ connection_id, host, display_name: "acme", binding: bound });

const message = (data: unknown) =>
  act(() => {
    window.dispatchEvent(
      new MessageEvent("message", { data, origin: window.location.origin }),
    );
  });

const cloudSite = {
  site_url: "https://acme.atlassian.net",
  deployment: "cloud",
  host: "acme.atlassian.net",
};
const serverSite = {
  site_url: "https://jira.corp.example",
  deployment: "server",
  host: "jira.corp.example",
};

const page = (body: unknown, more?: Route): Route => {
  return (call) => {
    const custom = more?.(call);
    if (custom) return custom;
    if (call.method === "GET" && call.path === list) return json(200, body);
    return undefined;
  };
};
const row = (id: string) => screen.findByTestId(`binding-${id}`);

let openSpy: ReturnType<typeof vi.spyOn>;
beforeEach(() => {
  localStorage.clear();
  openSpy = vi
    .spyOn(window, "open")
    .mockReturnValue({ closed: false } as unknown as Window);
});
afterEach(() => vi.restoreAllMocks());

const open = "/o/o1/settings/account-binding";

describe("第三方账号绑定页", () => {
  it("加载中显示骨架，之后 14 行各状态正确", async () => {
    renderSettings(open, page(view({ ready: ["github"] })));
    expect(await screen.findAllByTestId("binding-skeleton")).toHaveLength(14);
    expect(
      await screen.findByRole("heading", { name: "第三方账号绑定" }),
    ).toBeVisible();
    for (const [id] of ids) expect(await row(id)).toBeVisible();
    for (const id of ["tapd", "teambition", "slack", "lark", "dingtalk"])
      expect(within(await row(id)).getByText("即将推出")).toBeVisible();
    // 未 ready 的文案
    expect(
      within(await row("jira")).getByText("请先让管理员通过 Jira API 导入项目"),
    ).toBeVisible();
    expect(
      within(await row("feishu")).getByText("需先由管理员连接飞书助手"),
    ).toBeVisible();
    expect(
      within(await row("feishu")).getByRole("button", { name: "绑定" }),
    ).toBeDisabled();
    // GitHub ready：可绑定
    expect(
      within(await row("github")).getByRole("button", { name: "绑定" }),
    ).toBeEnabled();
    // 微信：不做登录，只有「管理」入口，没有「绑定」
    const wechat = within(await row("wechat"));
    expect(wechat.getByRole("button", { name: "管理" })).toBeVisible();
    expect(wechat.queryByRole("button", { name: "绑定" })).toBeNull();
    expect(wechat.queryByText(/微信扫码登录/)).toBeNull();
    expect(
      within(await row("zentao")).getByRole("button", { name: "管理" }),
    ).toBeEnabled();
    expect(
      within(await row("pingcode")).getByRole("button", { name: "管理" }),
    ).toBeEnabled();
  });

  it("Lark、钉钉可用且管理员已连接时显示用途说明，不露出文案键", async () => {
    const body = view({ ready: ["lark", "dingtalk"] });
    body.providers = body.providers.map((p) =>
      p.id === "lark" || p.id === "dingtalk"
        ? { ...p, availability: "available", method: "oauth" }
        : p,
    );
    renderSettings(open, page(body));
    expect(
      within(await row("lark")).getByText(/Lark 机器人会私聊给你发送/),
    ).toBeVisible();
    expect(
      within(await row("dingtalk")).getByText(/钉钉机器人会私聊给你发送/),
    ).toBeVisible();
    expect(screen.queryByText(/accountBinding\./)).toBeNull();
  });

  it("GitHub 未 ready 显示提示且没有绑定按钮", async () => {
    renderSettings(open, page(view()));
    const github = await row("github");
    expect(within(github).getByText("请先让管理员绑定 GitHub")).toBeVisible();
    expect(within(github).queryByRole("button")).toBeNull();
  });

  it("禅道个人绑定显示状态并可进入管理页", async () => {
    const user = userEvent.setup();
    renderSettings(
      open,
      page(
        view({ bound: { zentao: binding({ display_name: "lin-zen" }) } }),
        (call) =>
          call.method === "GET" && call.path === `${o}/zentao`
            ? json(200, {
                provider: {
                  id: "zentao",
                  name: "禅道",
                  template: "A",
                  availability: "available",
                  capabilities: ["import", "personal_account"],
                  bindable: false,
                },
                can_manage: true,
                connections: [],
                personal_connections: [],
                sync_links: [],
              })
            : undefined,
      ),
    );
    const zentao = within(await row("zentao"));
    expect(zentao.getByText("lin-zen")).toBeVisible();
    await user.click(zentao.getByRole("button", { name: "管理" }));
    expect(await screen.findByRole("heading", { name: "禅道" })).toBeVisible();
  });

  it("GitHub 弹窗授权：发起 personal，收到 message 后刷新并提示", async () => {
    const user = userEvent.setup();
    let bound: Record<string, unknown> = {};
    const calls = renderSettings(
      open,
      page(view({ ready: ["github"] }), (call) => {
        if (call.method === "GET" && call.path === list)
          return json(200, view({ ready: ["github"], bound }));
        if (call.method === "POST" && call.path === `${o}/github/authorize`)
          return json(200, {
            authorize_url: "https://github.example/authorize",
            session_id: "s1",
            expires_at: ts,
          });
        return undefined;
      }),
    );
    await user.click(
      within(await row("github")).getByRole("button", { name: "绑定" }),
    );
    await waitFor(() => expect(openSpy).toHaveBeenCalledTimes(1));
    expect(findCall(calls, "POST", `${o}/github/authorize`)?.body).toEqual({
      purpose: "personal",
    });
    expect(await screen.findByText("正在等待授权完成……")).toBeVisible();
    bound = { github: binding() };
    act(() => {
      window.dispatchEvent(
        new MessageEvent("message", {
          origin: window.location.origin,
          data: {
            type: "openfox:oauth",
            provider: "github",
            session: "s1",
            status: "ok",
            reason: "",
          },
        }),
      );
    });
    expect(
      await within(await row("github")).findByRole("button", {
        name: /已连接/,
      }),
    ).toBeVisible();
  });

  it("飞书授权走飞书接口", async () => {
    const user = userEvent.setup();
    renderSettings(
      open,
      page(view({ ready: ["feishu"] }), (call) =>
        call.method === "POST" && call.path === `${o}/feishu/authorize`
          ? json(200, {
              authorize_url: "https://feishu.example/a",
              session_id: "f1",
              expires_at: ts,
            })
          : undefined,
      ),
    );
    await user.click(
      within(await row("feishu")).getByRole("button", { name: "绑定" }),
    );
    await waitFor(() =>
      expect(openSpy.mock.calls[0]?.[0]).toBe("https://feishu.example/a"),
    );
  });

  it("授权失败的 message 提示错误", async () => {
    const user = userEvent.setup();
    renderSettings(
      open,
      page(view({ ready: ["github"] }), (call) =>
        call.method === "POST" && call.path === `${o}/github/authorize`
          ? json(200, {
              authorize_url: "https://github.example/a",
              session_id: "s9",
              expires_at: ts,
            })
          : undefined,
      ),
    );
    await user.click(
      within(await row("github")).getByRole("button", { name: "绑定" }),
    );
    await screen.findByText("正在等待授权完成……");
    act(() => {
      window.dispatchEvent(
        new MessageEvent("message", {
          origin: window.location.origin,
          data: {
            type: "openfox:oauth",
            provider: "github",
            session: "s9",
            status: "error",
            reason: "access_denied",
          },
        }),
      );
    });
    await waitFor(() =>
      expect(screen.queryByText("正在等待授权完成……")).toBeNull(),
    );
  });
});

describe("Jira 绑定对话框", () => {
  const jiraView = (sites = [cloudSite], bound: Record<string, unknown> = {}) =>
    view({ ready: ["jira"], sites, bound });

  async function openDialog(route: Route, sites = [cloudSite]) {
    const user = userEvent.setup();
    const calls = renderSettings(open, page(jiraView(sites), route));
    await user.click(
      within(await row("jira")).getByRole("button", { name: "绑定" }),
    );
    const dialog = await screen.findByRole("dialog");
    return { user, calls, dialog };
  }

  it("Cloud：默认填 OpenFox 邮箱，提交成功提示并显示关联数", async () => {
    const { user, calls, dialog } = await openDialog((call) =>
      call.method === "PUT" && call.path === jiraPath
        ? json(200, {
            binding: binding({ display_name: "Lin" }),
            linked_accounts: 2,
            reassigned_tasks: 1,
            skipped_accounts: 0,
          })
        : undefined,
    );
    expect(
      within(dialog).getByText(
        "将你的个人 Jira 账号绑定到 acme.atlassian.net，让同步任务与评论以你本人身份写回。",
      ),
    ).toBeVisible();
    expect(within(dialog).getByLabelText("Jira 邮箱")).toHaveValue(
      "lin@openfox.dev",
    );
    expect(
      within(dialog).getByText("令牌只用来确认你的身份，OpenFox 不会保存。"),
    ).toBeVisible();
    const submit = within(dialog).getByRole("button", { name: "连接" });
    expect(submit).toBeDisabled();
    await user.type(within(dialog).getByLabelText("API Token"), "tok-1");
    await user.click(submit);
    await waitFor(() =>
      expect(findCall(calls, "PUT", jiraPath)?.body).toEqual({
        site_url: "https://acme.atlassian.net",
        deployment: "cloud",
        email: "lin@openfox.dev",
        api_token: "tok-1",
      }),
    );
    expect(
      await screen.findByText("已自动关联 2 个 Jira 外部账号"),
    ).toBeVisible();
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("Server：只填个人访问令牌，不带邮箱", async () => {
    const { user, calls, dialog } = await openDialog(
      (call) =>
        call.method === "PUT" && call.path === jiraPath
          ? json(200, {
              binding: binding(),
              linked_accounts: 0,
              reassigned_tasks: 0,
              skipped_accounts: 0,
            })
          : undefined,
      [serverSite],
    );
    expect(within(dialog).queryByLabelText("Jira 邮箱")).toBeNull();
    await user.type(within(dialog).getByLabelText("个人访问令牌"), "pat-1");
    await user.click(within(dialog).getByRole("button", { name: "连接" }));
    await waitFor(() =>
      expect(findCall(calls, "PUT", jiraPath)?.body).toEqual({
        site_url: "https://jira.corp.example",
        deployment: "server",
        api_token: "pat-1",
      }),
    );
    // 关联数为 0 时不提示
    expect(screen.queryByText(/已自动关联/)).toBeNull();
  });

  it.each([
    [422, "JIRA_AUTH_FAILED", "Jira 凭证验证失败，请检查后重试。"],
    [422, "JIRA_EMAIL_MISMATCH", "Jira 邮箱必须与你的 OpenFox 邮箱一致。"],
    [409, "JIRA_ACCOUNT_IN_USE", "这个 Jira 账号已经绑定到本组织的其他成员。"],
    [502, "JIRA_UNAVAILABLE", "暂时连不上 Jira，请稍后重试。"],
    [400, "JIRA_BLOCKED_ADDRESS", "这个 Jira 地址不允许访问。"],
    [400, "JIRA_SITE_UNKNOWN", "这个 Jira 站点不在本组织的导入记录里。"],
    [500, "INTERNAL", "连接失败，请重试"],
  ])("错误 %i %s 显示对应文案", async (status, code, text) => {
    const { user, dialog } = await openDialog((call) =>
      call.method === "PUT" && call.path === jiraPath
        ? apiError(status, code)
        : undefined,
    );
    await user.type(within(dialog).getByLabelText("API Token"), "bad");
    await user.click(within(dialog).getByRole("button", { name: "连接" }));
    expect(await within(dialog).findByRole("alert")).toHaveTextContent(text);
    // 对话框保持打开，令牌保留以便修改重试
    expect(screen.getByRole("dialog")).toBeVisible();
  });

  it("多个站点时先选站点", async () => {
    const { dialog } = await openDialog(
      () => undefined,
      [cloudSite, serverSite],
    );
    expect(within(dialog).getByText("Jira 站点")).toBeVisible();
  });
});

describe("已绑定与断开", () => {
  it("Jira 菜单有更新与断开，断开需确认并调用断开接口", async () => {
    const user = userEvent.setup();
    const bound = {
      jira: binding({ display_name: "Lin", detail: "acme.atlassian.net" }),
    };
    const calls = renderSettings(
      open,
      page(view({ ready: ["jira"], sites: [cloudSite], bound }), (call) =>
        call.method === "POST" && call.path === `${o}/connections/c1/disconnect`
          ? json(200, { id: "c1" })
          : undefined,
      ),
    );
    const jira = await row("jira");
    expect(within(jira).getByText("Lin · acme.atlassian.net")).toBeVisible();
    await user.click(within(jira).getByRole("button", { name: /已连接/ }));
    expect(await screen.findByRole("menuitem", { name: "更新" })).toBeVisible();
    await user.click(screen.getByRole("menuitem", { name: "断开连接" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText("断开 Jira 连接？")).toBeVisible();
    // 取消不请求
    await user.click(within(dialog).getByRole("button", { name: "取消" }));
    expect(
      findCall(calls, "POST", `${o}/connections/c1/disconnect`),
    ).toBeUndefined();
    await user.click(within(jira).getByRole("button", { name: /已连接/ }));
    await user.click(await screen.findByRole("menuitem", { name: "断开连接" }));
    await user.click(
      within(await screen.findByRole("dialog")).getByRole("button", {
        name: "断开连接",
      }),
    );
    await waitFor(() =>
      expect(
        findCall(calls, "POST", `${o}/connections/c1/disconnect`)?.body,
      ).toEqual({ version: 4 }),
    );
  });

  it("GitHub 已连接菜单只有断开连接", async () => {
    const user = userEvent.setup();
    renderSettings(
      open,
      page(view({ ready: ["github"], bound: { github: binding() } })),
    );
    await user.click(
      within(await row("github")).getByRole("button", { name: /已连接/ }),
    );
    expect(
      await screen.findByRole("menuitem", { name: "断开连接" }),
    ).toBeVisible();
    expect(screen.queryByRole("menuitem", { name: "更新" })).toBeNull();
  });

  it("Git 三行紧跟 GitHub，未 ready 时提示管理员先绑定，不露出文案键", async () => {
    renderSettings(open, page(view()));
    await row("gitee");
    const rows = (await screen.findAllByTestId(/^binding-/)).map((el) =>
      el.getAttribute("data-testid"),
    );
    expect(rows.slice(0, 5)).toEqual([
      "binding-wechat",
      "binding-github",
      "binding-gitlab_cn",
      "binding-gitlab",
      "binding-gitee",
    ]);
    expect(rows).toHaveLength(14);
    for (const [id, text] of [
      ["gitlab_cn", "请先让管理员绑定极狐 GitLab.cn"],
      ["gitlab", "请先让管理员绑定 GitLab 自部署版"],
      ["gitee", "请先让管理员绑定 Gitee"],
    ] as const) {
      const r = await row(id);
      expect(within(r).getByText(text)).toBeVisible();
      expect(within(r).queryByRole("button")).toBeNull();
    }
    expect(screen.queryByText(/accountBinding\./)).toBeNull();
  });

  it("Git 三行 ready 后显示用途说明，不露出文案键", async () => {
    renderSettings(
      open,
      page(
        view({
          ready: ["gitlab_cn", "gitlab", "gitee"],
          instances: {
            gitlab_cn: [inst("i1", "jihulab.com")],
            gitlab: [inst("i2", "git.a.test")],
          },
        }),
      ),
    );
    expect(
      within(await row("gitlab_cn")).getByText(/每个实例需要分别绑定/),
    ).toBeVisible();
    expect(
      within(await row("gitee")).getByText(
        /Gitee 上的 Issue、评论和 Pull Request/,
      ),
    ).toBeVisible();
    expect(screen.queryByText(/accountBinding\./)).toBeNull();
  });

  it("GitLab 单实例未绑定：直接授权，带 connection_id", async () => {
    const user = userEvent.setup();
    const calls = renderSettings(
      open,
      page(
        view({
          ready: ["gitlab"],
          instances: { gitlab: [inst("i2", "git.a.test")] },
        }),
        (call) =>
          call.method === "POST" && call.path === `${o}/gitlab/authorize`
            ? json(200, {
                authorize_url: "https://git.a.test/oauth/authorize",
                session_id: "gs1",
                expires_at: ts,
              })
            : undefined,
      ),
    );
    await user.click(
      within(await row("gitlab")).getByRole("button", { name: "绑定" }),
    );
    expect(screen.queryByRole("menuitem")).toBeNull();
    await waitFor(() =>
      expect(findCall(calls, "POST", `${o}/gitlab/authorize`)?.body).toEqual({
        purpose: "personal",
        connection_id: "i2",
      }),
    );
    await waitFor(() => expect(openSpy).toHaveBeenCalledTimes(1));
    expect(openSpy.mock.calls[0]![0]).toBe(
      "https://git.a.test/oauth/authorize",
    );
    // 回执（provider 为 gitlab）后刷新并提示
    message({
      type: "openfox:oauth",
      provider: "gitlab",
      session: "gs1",
      status: "ok",
      reason: "",
    });
    expect(await screen.findByText("已连接")).toBeVisible();
  });

  it("GitLab 多个实例：先选实例，再按所选实例授权", async () => {
    const user = userEvent.setup();
    const calls = renderSettings(
      open,
      page(
        view({
          ready: ["gitlab"],
          instances: {
            gitlab: [inst("i1", "git.a.test"), inst("i2", "git.b.test")],
          },
        }),
        (call) =>
          call.method === "POST" && call.path === `${o}/gitlab/authorize`
            ? json(200, {
                authorize_url: "https://git.b.test/a",
                session_id: "gs2",
                expires_at: ts,
              })
            : undefined,
      ),
    );
    await user.click(
      within(await row("gitlab")).getByRole("button", { name: "绑定" }),
    );
    expect(findCall(calls, "POST", `${o}/gitlab/authorize`)).toBeUndefined();
    await user.click(
      await screen.findByRole("menuitem", { name: "git.b.test" }),
    );
    await waitFor(() =>
      expect(findCall(calls, "POST", `${o}/gitlab/authorize`)?.body).toEqual({
        purpose: "personal",
        connection_id: "i2",
      }),
    );
  });

  it("GitLab 多实例已绑定：显示实例数，菜单可断开其一并可关联其他实例", async () => {
    const user = userEvent.setup();
    const calls = renderSettings(
      open,
      page(
        view({
          ready: ["gitlab"],
          bound: {
            gitlab: binding({
              connection_id: "p1",
              display_name: "lin-a",
              detail: "2 个实例",
              version: 7,
            }),
          },
          instances: {
            gitlab: [
              inst(
                "i1",
                "git.a.test",
                binding({
                  connection_id: "p1",
                  display_name: "lin-a",
                  detail: "git.a.test",
                  version: 7,
                }),
              ),
              inst(
                "i2",
                "git.b.test",
                binding({
                  connection_id: "p2",
                  display_name: "lin-b",
                  detail: "git.b.test",
                  version: 3,
                }),
              ),
              inst("i3", "git.c.test"),
            ],
          },
        }),
        (call) =>
          call.method === "POST" &&
          call.path === `${o}/connections/p2/disconnect`
            ? json(200, { id: "p2" })
            : undefined,
      ),
    );
    const r = await row("gitlab");
    expect(within(r).getByText("lin-a · 2 个实例")).toBeVisible();
    await user.click(within(r).getByRole("button", { name: /已连接/ }));
    expect(
      await screen.findByRole("menuitem", { name: "断开 git.a.test" }),
    ).toBeVisible();
    expect(
      screen.getByRole("menuitem", { name: "关联其他实例" }),
    ).toBeVisible();
    await user.click(screen.getByRole("menuitem", { name: "断开 git.b.test" }));
    const dialog = await screen.findByRole("dialog");
    expect(
      within(dialog).getByText("断开 GitLab 自部署版 · git.b.test 连接？"),
    ).toBeVisible();
    await user.click(within(dialog).getByRole("button", { name: "断开连接" }));
    await waitFor(() =>
      expect(
        findCall(calls, "POST", `${o}/connections/p2/disconnect`)?.body,
      ).toEqual({ version: 3 }),
    );
    expect(
      findCall(calls, "POST", `${o}/connections/p1/disconnect`),
    ).toBeUndefined();
  });

  it("GitLab 已绑定且还有一个未绑定实例：菜单里是「关联其他实例」", async () => {
    const user = userEvent.setup();
    const calls = renderSettings(
      open,
      page(
        view({
          ready: ["gitlab_cn"],
          bound: { gitlab_cn: binding({ detail: "jihulab.com" }) },
          instances: {
            gitlab_cn: [
              inst("i1", "jihulab.com", binding({ detail: "jihulab.com" })),
              inst("i9", "jh.corp.test"),
            ],
          },
        }),
        (call) =>
          call.method === "POST" && call.path === `${o}/gitlab_cn/authorize`
            ? json(200, {
                authorize_url: "https://jh.corp.test/a",
                session_id: "gs3",
                expires_at: ts,
              })
            : undefined,
      ),
    );
    await user.click(
      within(await row("gitlab_cn")).getByRole("button", { name: /已连接/ }),
    );
    await user.click(
      await screen.findByRole("menuitem", { name: "关联其他实例" }),
    );
    await waitFor(() =>
      expect(findCall(calls, "POST", `${o}/gitlab_cn/authorize`)?.body).toEqual(
        {
          purpose: "personal",
          connection_id: "i9",
        },
      ),
    );
  });

  it("Gitee 授权：与 GitHub 一样发起 personal（不带 connection_id），已连接菜单只有断开", async () => {
    const user = userEvent.setup();
    const calls = renderSettings(
      open,
      page(view({ ready: ["gitee"] }), (call) =>
        call.method === "POST" && call.path === `${o}/gitee/authorize`
          ? json(200, {
              authorize_url: "https://gitee.com/oauth/authorize",
              session_id: "ge1",
              expires_at: ts,
            })
          : undefined,
      ),
    );
    await user.click(
      within(await row("gitee")).getByRole("button", { name: "绑定" }),
    );
    await waitFor(() =>
      expect(findCall(calls, "POST", `${o}/gitee/authorize`)?.body).toEqual({
        purpose: "personal",
      }),
    );
    expect(await screen.findByText("正在等待授权完成……")).toBeVisible();
  });

  it("Gitee 已连接菜单只有断开连接", async () => {
    const user = userEvent.setup();
    renderSettings(
      open,
      page(
        view({
          ready: ["gitee"],
          bound: { gitee: binding({ display_name: "lin-ge" }) },
        }),
      ),
    );
    await user.click(
      within(await row("gitee")).getByRole("button", { name: /已连接/ }),
    );
    expect(
      await screen.findByRole("menuitem", { name: "断开连接" }),
    ).toBeVisible();
    expect(screen.queryByRole("menuitem", { name: "更新" })).toBeNull();
  });

  it("个人设置导航里有「第三方账号绑定」", async () => {
    renderSettings(open, page(view()));
    expect(
      await screen.findByRole("link", { name: "第三方账号绑定" }),
    ).toBeVisible();
  });
});
