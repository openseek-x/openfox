import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  apiError,
  findCall,
  json,
  renderSettings,
  ts,
  type Call,
  type Route,
} from "../settings/testing";

// GitHub 详情页「同步 Issue 数据」：新建同步、已关联列表、编辑 / 暂停 / 移除 / 重试、Issue 回链开关。

const o = "/v1/organizations/o1/integrations";
const PATH = "/o/o1/settings/integrations/github";

const githubProvider = {
  id: "github",
  name: "GitHub",
  category: "git",
  availability: "available",
  template: "B",
  capabilities: ["sync", "pr_link", "personal_account"],
  configured: true,
  bindable: true,
  summary: { connections: 1, status: "ok" },
};

const conn = (extra: Record<string, unknown> = {}) => ({
  id: "c1",
  provider: "github",
  scope: "ORGANIZATION",
  external_id: "openfox-dev",
  display_name: "openfox-dev",
  account_kind: "organization",
  status: "ACTIVE",
  status_reason: "",
  credentials_hint: "",
  created_by: "u1",
  created_by_email: "lin@openfox.dev",
  created_at: ts,
  updated_at: ts,
  disconnected_at: null,
  avatar_url: "",
  webhook_last_delivery_at: null,
  version: 3,
  ...extra,
});

const link = (extra: Record<string, unknown> = {}) => ({
  id: "l1",
  connection_id: "c1",
  space_id: "s1",
  space_name: "产品研发",
  external_project_id: "openfox-dev/web",
  external_project_name: "openfox-dev/web",
  direction: "INBOUND",
  status: "ACTIVE",
  status_reason: "",
  created_at: ts,
  version: 4,
  repository_url: "https://github.com/openfox-dev/web",
  repository_private: false,
  last_synced_at: null,
  backfill: { status: "none", imported: 0, error: "" },
  outbound: { pending: 0, failed: 0 },
  ...extra,
});

const detail = (extra: Record<string, unknown> = {}, canManage = true) => ({
  provider: githubProvider,
  can_manage: canManage,
  connections: [conn()],
  personal_connections: [],
  sync_links: [],
  ...extra,
});

const settingsBody = (extra: Record<string, unknown> = {}) => ({
  provider: "github",
  settings: {
    link_pull_requests: true,
    link_commits: false,
    pr_backlink_private: true,
    pr_backlink_public: false,
    issue_backlink_private: true,
    issue_backlink_public: false,
    branch_format: "username/identifier-title",
    ...extra,
  },
  can_manage: true,
  version: 2,
  updated_at: ts,
});

const space = (id: string, name: string, extra = {}) => ({
  id,
  prefix: id.toUpperCase(),
  name,
  visibility: "shared",
  version: 1,
  created_at: ts,
  status: "active",
  my_role: "OWNER",
  can_manage: true,
  ...extra,
});

const repo = (name: string, extra: Record<string, unknown> = {}) => ({
  full_name: `openfox-dev/${name}`,
  name,
  private: false,
  html_url: `https://github.com/openfox-dev/${name}`,
  linked_space_id: null,
  linked_space_name: null,
  ...extra,
});

/** 详情、设置、空间按默认值返回；more 优先。 */
function route(body: unknown, more?: Route): Route {
  return (call) => {
    const custom = more?.(call);
    if (custom) return custom;
    if (call.method === "GET" && call.path === `${o}/github`)
      return json(200, body);
    if (call.method === "GET" && call.path === `${o}/github/settings`)
      return json(200, settingsBody());
    if (call.method === "GET" && call.path === "/v1/organizations/o1/spaces")
      return json(200, {
        spaces: [
          space("s1", "产品研发"),
          space("s2", "市场运营", { my_role: "MEMBER", can_manage: false }),
          space("s3", "未加入", { my_role: null, can_manage: false }),
          space("s4", "已停用", { status: "retired" }),
        ],
      });
    return undefined;
  };
}

const reposPath = `${o}/connections/c1/repositories`;
const isRepos = (call: Call) =>
  call.method === "GET" && call.path === reposPath;

beforeEach(() => localStorage.clear());
afterEach(() => vi.restoreAllMocks());

describe("新建同步对话框", () => {
  it("选择仓库、空间、方向并导入已有 Issue，成功后提示配置 Webhook 事件", async () => {
    const user = userEvent.setup();
    const calls = renderSettings(
      PATH,
      route(detail(), (call) => {
        if (isRepos(call))
          return json(200, {
            repositories: [
              repo("web"),
              repo("api", { private: true }),
              repo("docs", {
                linked_space_id: "s2",
                linked_space_name: "市场运营",
              }),
            ],
            has_more: false,
          });
        if (call.method === "POST" && call.path === `${o}/sync-links`)
          return json(201, link({ direction: "BIDIRECTIONAL" }));
        if (
          call.method === "GET" &&
          call.path === `${o}/connections/c1/webhook`
        )
          return json(200, {
            connection_id: "c1",
            payload_url: "https://x.example/hook",
            content_type: "application/json",
            secret: "s",
            events: ["pull_request", "push", "issues", "issue_comment"],
            last_delivery_at: null,
            recent: [],
            version: 3,
          });
        return undefined;
      }),
    );
    await user.click(await screen.findByRole("button", { name: "新建同步" }));
    const dialog = await screen.findByRole("dialog");
    // 已关联仓库禁用并标注空间；私有仓库带锁图标
    const docs = await within(dialog).findByRole("radio", {
      name: "openfox-dev/docs",
    });
    expect(docs).toBeDisabled();
    expect(within(dialog).getByText("已关联到 市场运营")).toBeVisible();
    expect(within(dialog).getByLabelText("私有仓库")).toBeVisible();
    const submit = within(dialog).getByRole("button", { name: "开始同步" });
    expect(submit).toBeDisabled();

    await user.click(
      within(dialog).getByRole("radio", { name: "openfox-dev/web" }),
    );
    // 空间只列出成员可写的：未加入、已停用的不出现
    await user.click(
      within(dialog).getByRole("button", { name: "选择目标空间" }),
    );
    expect(screen.queryByRole("option", { name: "未加入" })).toBeNull();
    expect(screen.queryByRole("option", { name: "已停用" })).toBeNull();
    expect(screen.getByRole("option", { name: "市场运营" })).toBeVisible();
    await user.click(screen.getByRole("option", { name: "产品研发" }));
    await user.click(
      within(dialog).getByRole("radio", { name: "GitHub ↔ OpenFox" }),
    );
    expect(
      within(dialog).getByText(/OpenFox 中新建的任务会自动在仓库里创建 Issue/),
    ).toBeVisible();
    expect(
      within(dialog).getByRole("checkbox", { name: "导入已有的未关闭 Issue" }),
    ).not.toBeChecked();
    await user.click(
      within(dialog).getByRole("checkbox", { name: "导入已有的未关闭 Issue" }),
    );
    expect(
      within(dialog).getByText(/最多回填 5,000 条未关闭的 Issue/),
    ).toBeVisible();
    expect(
      within(dialog).getByText(/没有独立导入记录或预览；移除同步后/),
    ).toBeVisible();
    await user.click(submit);

    await waitFor(() =>
      expect(findCall(calls, "POST", `${o}/sync-links`)?.body).toEqual({
        connection_id: "c1",
        repository: "openfox-dev/web",
        space_id: "s1",
        direction: "BIDIRECTIONAL",
        import_existing: true,
      }),
    );
    expect(
      await screen.findByText(
        /记得在 GitHub Webhook 里勾选 Issues 与 Issue comments 事件/,
      ),
    ).toBeVisible();
    await user.click(screen.getByRole("button", { name: "查看 Webhook 指引" }));
    expect(await screen.findByLabelText("Payload URL")).toBeVisible();
  });

  it("导入入口自动打开并默认提交已有 Issue，普通入口仍默认不导入", async () => {
    const user = userEvent.setup();
    const calls = renderSettings(
      `${PATH}?import=existing`,
      route(detail(), (call) => {
        if (isRepos(call))
          return json(200, {
            repositories: [repo("web")],
            has_more: false,
          });
        if (call.method === "POST" && call.path === `${o}/sync-links`)
          return json(201, link());
        return undefined;
      }),
    );
    const dialog = await screen.findByRole("dialog");
    expect(
      within(dialog).getByRole("checkbox", { name: "导入已有的未关闭 Issue" }),
    ).toBeChecked();
    await user.click(
      await within(dialog).findByRole("radio", { name: "openfox-dev/web" }),
    );
    await user.click(
      within(dialog).getByRole("button", { name: "选择目标空间" }),
    );
    await user.click(screen.getByRole("option", { name: "产品研发" }));
    await user.click(within(dialog).getByRole("button", { name: "开始同步" }));
    await waitFor(() =>
      expect(findCall(calls, "POST", `${o}/sync-links`)?.body).toEqual({
        connection_id: "c1",
        repository: "openfox-dev/web",
        space_id: "s1",
        direction: "INBOUND",
        import_existing: true,
      }),
    );
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    await user.click(screen.getByRole("button", { name: "新建同步" }));
    expect(
      within(await screen.findByRole("dialog")).getByRole("checkbox", {
        name: "导入已有的未关闭 Issue",
      }),
    ).not.toBeChecked();
  });

  it("搜索词与页码随请求发出，下一页由 has_more 控制", async () => {
    const user = userEvent.setup();
    const calls = renderSettings(
      PATH,
      route(detail(), (call) =>
        isRepos(call)
          ? json(200, {
              repositories: [repo("web")],
              has_more: call.query.get("page") === "1" && !call.query.get("q"),
            })
          : undefined,
      ),
    );
    await user.click(await screen.findByRole("button", { name: "新建同步" }));
    const dialog = await screen.findByRole("dialog");
    await within(dialog).findByRole("radio", { name: "openfox-dev/web" });
    const prev = within(dialog).getByRole("button", { name: "上一页" });
    const next = within(dialog).getByRole("button", { name: "下一页" });
    expect(prev).toBeDisabled();
    await user.click(next);
    await waitFor(() =>
      expect(calls.some((c) => isRepos(c) && c.query.get("page") === "2")).toBe(
        true,
      ),
    );
    await user.type(within(dialog).getByLabelText("搜索仓库"), "we");
    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            isRepos(c) &&
            c.query.get("q") === "we" &&
            c.query.get("page") === "1",
        ),
      ).toBe(true),
    );
  });

  it.each([
    [409, "SYNC_LINK_EXISTS", "这个仓库已经关联到其他空间"],
    [409, "SYNC_BIDIRECTIONAL_EXISTS", "该空间已经有一个双向同步"],
    [409, "INTEGRATION_NEEDS_REAUTH", "GitHub 授权已失效，请先在上方重新授权"],
    [502, "UPSTREAM_UNAVAILABLE", "暂时无法访问已连接的服务，请稍后重试"],
  ])("创建失败 %s %s 显示中文原因", async (status, code, text) => {
    const user = userEvent.setup();
    renderSettings(
      PATH,
      route(detail(), (call) => {
        if (isRepos(call))
          return json(200, { repositories: [repo("web")], has_more: false });
        if (call.method === "POST" && call.path === `${o}/sync-links`)
          return apiError(status, code);
        return undefined;
      }),
    );
    await user.click(await screen.findByRole("button", { name: "新建同步" }));
    const dialog = await screen.findByRole("dialog");
    await user.click(
      await within(dialog).findByRole("radio", { name: "openfox-dev/web" }),
    );
    await user.click(
      within(dialog).getByRole("button", { name: "选择目标空间" }),
    );
    await user.click(screen.getByRole("option", { name: "产品研发" }));
    await user.click(within(dialog).getByRole("button", { name: "开始同步" }));
    expect(await within(dialog).findByText(text)).toBeVisible();
  });

  it("没有未断开的组织连接时按钮禁用；非管理员禁用", async () => {
    renderSettings(
      PATH,
      route(detail({ connections: [conn({ status: "DISCONNECTED" })] })),
    );
    expect(
      await screen.findByRole("button", { name: "新建同步" }),
    ).toBeDisabled();
  });

  it("非管理员只读：按钮禁用，没有行菜单与重试", async () => {
    renderSettings(
      PATH,
      route(
        detail(
          {
            sync_links: [link({ outbound: { pending: 0, failed: 2 } })],
          },
          false,
        ),
      ),
      "MEMBER",
    );
    expect(
      await screen.findByRole("button", { name: "新建同步" }),
    ).toBeDisabled();
    expect(screen.getByText("2 项未能同步到 GitHub")).toBeVisible();
    expect(screen.queryByRole("button", { name: "重试" })).toBeNull();
    expect(
      screen.queryByRole("button", { name: "openfox-dev/web 的更多操作" }),
    ).toBeNull();
  });
});

describe("已关联仓库列表", () => {
  it("显示仓库外链、私有锁、方向徽标、空间、状态与最近同步", async () => {
    renderSettings(
      PATH,
      route(
        detail({
          sync_links: [
            link({
              direction: "BIDIRECTIONAL",
              repository_private: true,
              last_synced_at: "2026-10-30T08:00:00Z",
            }),
            link({
              id: "l2",
              external_project_name: "openfox-dev/api",
              space_name: "市场运营",
              status: "PAUSED",
            }),
          ],
        }),
      ),
    );
    const web = await screen.findByRole("link", { name: "openfox-dev/web" });
    expect(web).toHaveAttribute("href", "https://github.com/openfox-dev/web");
    expect(web).toHaveAttribute("target", "_blank");
    expect(screen.getByLabelText("私有仓库")).toBeVisible();
    expect(screen.getByText("GitHub ↔ OpenFox")).toBeVisible();
    expect(screen.getByText("GitHub → OpenFox")).toBeVisible();
    // 侧栏也会出现空间名，这里只确认列表行里有
    expect(screen.getAllByText("产品研发").length).toBeGreaterThan(0);
    expect(screen.getAllByText("市场运营").length).toBeGreaterThan(0);
    expect(screen.getByText("同步中")).toBeVisible();
    expect(screen.getByText("已暂停")).toBeVisible();
    expect(screen.getByText(/^最近同步 /)).toBeVisible();
    expect(screen.getByText("尚未同步")).toBeVisible();
  });

  it("导入进度每 5 秒刷新详情；失败时显示错误", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    let imported = 3;
    const calls = renderSettings(
      PATH,
      route(detail(), (call) =>
        call.method === "GET" && call.path === `${o}/github`
          ? json(
              200,
              detail({
                sync_links: [
                  link({
                    backfill: {
                      status: imported >= 10 ? "failed" : "running",
                      imported,
                      error: imported >= 10 ? "GitHub 限流" : "",
                    },
                  }),
                ],
              }),
            )
          : undefined,
      ),
    );
    expect(
      await screen.findByText("正在导入已有 Issue · 已导入 3 个"),
    ).toBeVisible();
    const before = calls.filter((c) => c.path === `${o}/github`).length;
    imported = 10;
    await vi.advanceTimersByTimeAsync(5200);
    expect(
      await screen.findByText("导入已有 Issue 失败：GitHub 限流"),
    ).toBeVisible();
    expect(
      calls.filter((c) => c.path === `${o}/github`).length,
    ).toBeGreaterThan(before);
    vi.useRealTimers();
  });

  it("出站失败显示数量，点击重试调用 retry", async () => {
    const user = userEvent.setup();
    const calls = renderSettings(
      PATH,
      route(
        detail({ sync_links: [link({ outbound: { pending: 1, failed: 3 } })] }),
        (call) =>
          call.method === "POST" && call.path === `${o}/sync-links/l1/retry`
            ? json(200, { requeued: 3 })
            : undefined,
      ),
    );
    expect(await screen.findByText("3 项未能同步到 GitHub")).toBeVisible();
    await user.click(screen.getByRole("button", { name: "重试" }));
    await waitFor(() =>
      expect(findCall(calls, "POST", `${o}/sync-links/l1/retry`)).toBeDefined(),
    );
  });

  it("行菜单：编辑方向、暂停与恢复、移除二次确认", async () => {
    const user = userEvent.setup();
    const calls = renderSettings(
      PATH,
      route(detail({ sync_links: [link()] }), (call) => {
        if (call.method === "PATCH" && call.path === `${o}/sync-links/l1`)
          return json(200, link());
        if (call.method === "POST" && call.path === `${o}/sync-links/l1/remove`)
          return json(204);
        return undefined;
      }),
    );
    const menu = "openfox-dev/web 的更多操作";
    // 编辑
    await user.click(await screen.findByRole("button", { name: menu }));
    await user.click(screen.getByRole("menuitem", { name: "编辑" }));
    const edit = await screen.findByRole("dialog");
    expect(within(edit).getByRole("button", { name: "保存" })).toBeDisabled();
    await user.click(
      within(edit).getByRole("radio", { name: "GitHub ↔ OpenFox" }),
    );
    await user.click(within(edit).getByRole("button", { name: "保存" }));
    await waitFor(() =>
      expect(findCall(calls, "PATCH", `${o}/sync-links/l1`)?.body).toEqual({
        version: 4,
        direction: "BIDIRECTIONAL",
      }),
    );
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    // 暂停
    await user.click(screen.getByRole("button", { name: menu }));
    await user.click(screen.getByRole("menuitem", { name: "暂停" }));
    await waitFor(() =>
      expect(calls.filter((c) => c.method === "PATCH").at(-1)?.body).toEqual({
        version: 4,
        status: "PAUSED",
      }),
    );
    // 移除：先确认文案，取消不发请求
    await user.click(screen.getByRole("button", { name: menu }));
    await user.click(screen.getByRole("menuitem", { name: "移除" }));
    const confirm = await screen.findByRole("dialog");
    expect(
      within(confirm).getByText("移除后任务和 Issue 都保留，只是不再同步"),
    ).toBeVisible();
    await user.click(within(confirm).getByRole("button", { name: "取消" }));
    expect(
      findCall(calls, "POST", `${o}/sync-links/l1/remove`),
    ).toBeUndefined();
    await user.click(screen.getByRole("button", { name: menu }));
    await user.click(screen.getByRole("menuitem", { name: "移除" }));
    await user.click(
      within(await screen.findByRole("dialog")).getByRole("button", {
        name: "移除",
      }),
    );
    await waitFor(() =>
      expect(
        findCall(calls, "POST", `${o}/sync-links/l1/remove`)?.body,
      ).toEqual({ version: 4 }),
    );
  });

  it("已暂停的同步菜单显示恢复", async () => {
    const user = userEvent.setup();
    const calls = renderSettings(
      PATH,
      route(detail({ sync_links: [link({ status: "PAUSED" })] }), (call) =>
        call.method === "PATCH" ? json(200, link()) : undefined,
      ),
    );
    await user.click(
      await screen.findByRole("button", { name: "openfox-dev/web 的更多操作" }),
    );
    await user.click(screen.getByRole("menuitem", { name: "恢复" }));
    await waitFor(() =>
      expect(findCall(calls, "PATCH", `${o}/sync-links/l1`)?.body).toEqual({
        version: 4,
        status: "ACTIVE",
      }),
    );
  });
});

describe("Issue 回链设置", () => {
  it("在同步区内：私有默认开、公开默认关；切换后保存 issue_backlink_*", async () => {
    const user = userEvent.setup();
    const calls = renderSettings(PATH, route(detail()));
    const sync = await screen.findByRole("region", {
      name: "同步 Issue 数据",
    });
    // 开关在同步区内，PR 设置区里不再有 Issue 回链
    const priv = await within(sync).findByRole("switch", { name: "私有仓库" });
    const pub = within(sync).getByRole("switch", { name: "公开仓库" });
    expect(
      within(sync).getByText("在同步成任务的 GitHub issue 下评论一条任务链接"),
    ).toBeVisible();
    expect(priv).toBeChecked();
    expect(pub).not.toBeChecked();
    expect(screen.getAllByText("Issue 回链")).toHaveLength(1);
    await user.click(pub);
    await waitFor(() =>
      expect(
        (
          findCall(calls, "PUT", `${o}/github/settings`)?.body as {
            settings: Record<string, unknown>;
          }
        ).settings,
      ).toMatchObject({
        issue_backlink_private: true,
        issue_backlink_public: true,
        pr_backlink_public: false,
      }),
    );
  });
});
