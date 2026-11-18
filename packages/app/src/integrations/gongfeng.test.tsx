import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import {
  apiError,
  json,
  renderSettings,
  ts,
  type Route,
} from "../settings/testing";
import {
  gongfengPrivacy,
  gongfengPushValid,
  gongfengRepositoryURL,
  gongfengWindowValid,
} from "./gongfeng-model";
import type { GongfengProject } from "@openfox/api";

const base = "/v1/organizations/o1/integrations",
  connectionPath = `${base}/connections/c1`,
  gf = `${connectionPath}/gongfeng`;
const connection = {
  id: "c1",
  provider: "gongfeng",
  scope: "ORGANIZATION",
  external_id: "42",
  display_name: "fixture",
  status: "ACTIVE",
  version: 3,
  created_at: ts,
  updated_at: ts,
};
const repository = {
  id: "r1",
  connection_id: "c1",
  sync_link_id: "l1",
  project_id: 55,
  path: "team/repo",
  private: true,
  generation: 7,
  status: "ACTIVE",
  status_reason: "",
  hook_id: 90,
  hook_state: "ACTIVE",
  repository_url: "https://git.tencent.com/team/repo",
  hook_generation: 1,
  hook_credential_generation: 1,
  mr_enabled: true,
  commit_enabled: false,
  backlink_enabled: true,
  history_requested: false,
  excluded_confidential: 2,
  progress: {},
};
const project: GongfengProject = {
  id: 55,
  name: "repo",
  path_with_namespace: "team/repo",
  type: "GIT",
  web_url: "https://untrusted.invalid/ignore",
  archived: false,
  issues_enabled: true,
  visibility_level: 0,
  public: false,
};
const config = {
  version: 3,
  repositories: [repository],
  has_more: false,
  next_cursor: "",
};
function route(
  options: {
    manage?: boolean;
    disconnected?: boolean;
    more?: Route;
    noConnections?: boolean;
  } = {},
): Route {
  return (call) => {
    const response = options.more?.(call);
    if (response) return response;
    if (call.path === `${base}/gongfeng`)
      return json(200, {
        provider: {
          id: "gongfeng",
          name: "腾讯工蜂",
          category: "git",
          availability: "available",
          template: "B",
          capabilities: ["import", "sync", "pr_link"],
          configured: true,
          bindable: true,
          summary: { connections: 1, status: "ok" },
        },
        can_manage: options.manage !== false,
        connections: options.noConnections
          ? []
          : [
              {
                ...connection,
                status: options.disconnected ? "DISCONNECTED" : "ACTIVE",
              },
            ],
        personal_connections: [],
        sync_links: [
          {
            id: "l1",
            connection_id: "c1",
            space_id: "s1",
            space_name: "工作空间",
            direction: "INBOUND",
            backfill: { status: "NONE" },
          },
        ],
      });
    if (call.path === `${gf}/configuration`) return json(200, config);
    if (call.path === `${gf}/work`)
      return json(200, { work: [], has_more: false, next_cursor: "" });
    if (call.path === `${gf}/projects`)
      return json(200, {
        projects: [project],
        next_page: 0,
        connection_version: 3,
      });
    if (call.path === "/v1/organizations/o1/spaces")
      return json(200, { spaces: [{ id: "s1", name: "工作空间" }] });
    return undefined;
  };
}
const path = "/o/o1/settings/integrations/gongfeng";
async function choose(
  user: ReturnType<typeof userEvent.setup>,
  label: string,
  option: string,
) {
  await user.click(screen.getByLabelText(label));
  await user.click(await screen.findByRole("option", { name: option }));
}

describe("Gongfeng management UI", () => {
  it("does not load management endpoints for a read-only member", async () => {
    const calls = renderSettings(path, route({ manage: false }), "MEMBER");
    await screen.findByText(/只有组织所有者和管理员/);
    expect(
      screen.queryByRole("button", { name: "绑定腾讯工蜂" }),
    ).not.toBeInTheDocument();
    expect(calls.some((c) => c.path.includes("/connections/"))).toBe(false);
    expect(
      calls.some(
        (c) => c.path.includes("/oauth") || c.path.includes("/settings"),
      ),
    ).toBe(false);
  });

  it("retains disconnected cleanup metadata, uses a safe repository URL and makes no platform requests", async () => {
    const calls = renderSettings(path, route({ disconnected: true }));
    await screen.findByText("远端 Hook ID");
    expect(screen.getByText("90")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "team/repo" })).toHaveAttribute(
      "href",
      "https://git.tencent.com/team/repo",
    );
    expect(
      screen.queryByRole("button", { name: "更新访问令牌" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "添加仓库同步" }),
    ).not.toBeInTheDocument();
    expect(
      calls
        .filter((c) => c.path.startsWith(connectionPath))
        .map((c) => c.path)
        .sort(),
    ).toEqual([`${gf}/configuration`, `${gf}/work`].sort());
  });

  it("saves a PAT without echoing it and clears the field when reopened", async () => {
    const user = userEvent.setup();
    const calls = renderSettings(
      path,
      route({
        noConnections: true,
        more: (c) =>
          c.path === `${base}/gongfeng/token`
            ? json(201, connection)
            : undefined,
      }),
    );
    await user.click(
      await screen.findByRole("button", { name: "绑定腾讯工蜂" }),
    );
    expect(screen.getByLabelText("个人访问令牌（PAT）")).toHaveAttribute(
      "type",
      "password",
    );
    await user.type(
      screen.getByLabelText("个人访问令牌（PAT）"),
      "fixture-pat",
    );
    await user.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
    expect(calls.find((c) => c.path.endsWith("/gongfeng/token"))?.body).toEqual(
      { access_token: "fixture-pat" },
    );
    await user.click(screen.getByRole("button", { name: "绑定腾讯工蜂" }));
    expect(screen.getByLabelText("个人访问令牌（PAT）")).toHaveValue("");
  });

  it("requires explicit Hook consent and sends approved private-repository defaults", async () => {
    const user = userEvent.setup();
    const calls = renderSettings(
      path,
      route({
        more: (c) =>
          c.path === `${base}/gongfeng/repositories`
            ? json(201, {
                ...repository,
                status: "PAUSED",
                hook_state: "CREATE_PENDING",
              })
            : undefined,
      }),
    );
    await screen.findByText("远端 Hook ID");
    expect(calls.some((c) => c.path === `${gf}/projects`)).toBe(false);
    await user.click(screen.getByRole("button", { name: "添加仓库同步" }));
    await user.click(screen.getByRole("button", { name: "读取可访问仓库" }));
    await screen.findByLabelText("选择仓库");
    await choose(user, "选择仓库", "team/repo");
    await choose(user, "目标空间", "工作空间");
    expect(
      screen.getByRole("checkbox", { name: "关联合并请求" }),
    ).toBeChecked();
    expect(
      screen.getByRole("checkbox", { name: "关联提交" }),
    ).not.toBeChecked();
    expect(
      screen.getByRole("checkbox", { name: "发送任务回链评论" }),
    ).toBeChecked();
    expect(
      screen.getByRole("checkbox", { name: "导入现有 Issue" }),
    ).not.toBeChecked();
    expect(screen.getByRole("button", { name: "创建同步" })).toBeDisabled();
    await user.click(
      screen.getByRole("checkbox", { name: "我确认创建并激活远端 Webhook" }),
    );
    await user.click(screen.getByRole("button", { name: "创建同步" }));
    await screen.findByText("请求已保存。请刷新查看 Hook 激活与业务处理结果。");
    expect(
      calls.find((c) => c.path === `${base}/gongfeng/repositories`)?.body,
    ).toEqual({
      connection_id: "c1",
      connection_version: 3,
      project_id: 55,
      space_id: "s1",
      direction: "INBOUND",
      import_existing: false,
      mr_enabled: true,
      commit_enabled: false,
      backlink_enabled: true,
    });
    expect(
      calls.some(
        (c) => c.path.includes("/settings") || c.path.includes("/webhook"),
      ),
    ).toBe(false);
  });

  it("pages projects and defaults non-private backlinks off; unknown privacy cannot enable them", async () => {
    const user = userEvent.setup();
    const calls = renderSettings(
      path,
      route({
        more: (c) =>
          c.path === `${gf}/projects`
            ? json(200, {
                projects: [
                  c.query.get("page") === "2"
                    ? {
                        ...project,
                        id: 56,
                        path_with_namespace: "team/unknown",
                        visibility_level: null,
                      }
                    : { ...project, visibility_level: 20, public: true },
                ],
                next_page: c.query.get("page") === "2" ? 0 : 2,
                connection_version: 3,
              })
            : undefined,
      }),
    );
    await screen.findByText("远端 Hook ID");
    await user.click(screen.getByRole("button", { name: "添加仓库同步" }));
    await user.click(screen.getByRole("button", { name: "读取可访问仓库" }));
    await screen.findByLabelText("选择仓库");
    await choose(user, "选择仓库", "team/repo");
    expect(
      screen.getByRole("checkbox", { name: "发送任务回链评论" }),
    ).not.toBeChecked();
    await screen.findByText(/非私有仓库的回链/);
    await user.click(
      within(screen.getByRole("dialog")).getByRole("button", {
        name: "下一页",
      }),
    );
    await waitFor(() =>
      expect(
        calls.some(
          (c) => c.path === `${gf}/projects` && c.query.get("page") === "2",
        ),
      ).toBe(true),
    );
    await choose(user, "选择仓库", "team/unknown");
    expect(
      screen.getByRole("checkbox", { name: "发送任务回链评论" }),
    ).toBeDisabled();
  });

  it("keeps edit draft on 409, refreshes both CAS values and sends again only on a new click", async () => {
    const user = userEvent.setup();
    let conflicts = 0;
    const calls = renderSettings(
      path,
      route({
        more: (c) => {
          if (c.path === `${gf}/repositories/r1` && c.method === "PATCH") {
            conflicts++;
            return conflicts === 1
              ? apiError(409, "VERSION_CONFLICT")
              : json(200, {
                  ...repository,
                  generation: 9,
                  commit_enabled: true,
                });
          }
          if (c.path === `${gf}/configuration` && conflicts)
            return json(200, {
              ...config,
              version: 4,
              repositories: [{ ...repository, generation: 8 }],
            });
          return undefined;
        },
      }),
    );
    await user.click(await screen.findByRole("button", { name: "编辑同步" }));
    await user.click(screen.getByRole("checkbox", { name: "关联提交" }));
    await user.click(screen.getByRole("button", { name: "保存" }));
    await screen.findByText(/已刷新版本并保留草稿/);
    await waitFor(() =>
      expect(
        calls.filter((c) => c.path === `${gf}/configuration`).length,
      ).toBeGreaterThan(1),
    );
    expect(screen.getByRole("checkbox", { name: "关联提交" })).toBeChecked();
    expect(conflicts).toBe(1);
    await user.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
    expect(
      calls.filter((c) => c.method === "PATCH").map((c) => c.body),
    ).toEqual([
      {
        connection_version: 3,
        generation: 7,
        direction: "INBOUND",
        mr_enabled: true,
        commit_enabled: true,
        backlink_enabled: true,
      },
      {
        connection_version: 4,
        generation: 8,
        direction: "INBOUND",
        mr_enabled: true,
        commit_enabled: true,
        backlink_enabled: true,
      },
    ]);
  });

  it("shows uncertain operations separately from completion and pages work using the cursor", async () => {
    const user = userEvent.setup();
    const calls = renderSettings(
      path,
      route({
        more: (c) => {
          if (c.path === `${gf}/configuration`)
            return json(200, {
              ...config,
              repositories: [
                { ...repository, hook_state: "UNKNOWN", status: "PAUSED" },
              ],
            });
          if (c.path === `${gf}/work`)
            return json(200, {
              work: [
                {
                  id: "w1",
                  repository_id: "r1",
                  kind: "ISSUE",
                  object_key: c.query.get("after") ? "older" : "55/1",
                  status: "RUNNING",
                  error_code: "RESULT_UNKNOWN",
                  backlink_state: "UNKNOWN",
                  requested_generation: 2,
                  completed_generation: 1,
                  next_attempt_at: ts,
                },
              ],
              has_more: !c.query.get("after"),
              next_cursor: c.query.get("after") ? "" : "cursor1",
            });
          return undefined;
        },
      }),
    );
    await screen.findByText("请求版本 2 / 已完成版本 1");
    expect(screen.getAllByText("结果待核对；不会盲目重复发送。")).toHaveLength(
      2,
    );
    expect(screen.getByText("回链状态：UNKNOWN")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "下一页" }));
    await screen.findByText("ISSUE · older");
    expect(
      calls.some(
        (c) => c.path === `${gf}/work` && c.query.get("after") === "cursor1",
      ),
    ).toBe(true);
    expect(calls.filter((c) => c.method !== "GET")).toHaveLength(0);
  });

  it("retains remove dialog on unknown outcome and retries reconciliation explicitly", async () => {
    const user = userEvent.setup();
    const calls = renderSettings(
      path,
      route({
        more: (c) => {
          if (c.path === `${gf}/repositories/r1/remove`)
            return apiError(409, "GONGFENG_RESULT_UNKNOWN");
          if (c.path === `${gf}/repositories/r1/retry`)
            return json(200, repository);
          return undefined;
        },
      }),
    );
    await user.click(await screen.findByRole("button", { name: "移除同步" }));
    await user.click(
      within(screen.getByRole("dialog")).getByRole("button", { name: "确认" }),
    );
    await within(screen.getByRole("dialog")).findByText(
      "结果待核对；不会盲目重复发送。",
    );
    expect(calls.filter((c) => c.path.endsWith("/remove"))).toHaveLength(1);
    await user.click(
      within(screen.getByRole("dialog")).getByRole("button", { name: "取消" }),
    );
    await user.click(screen.getByRole("button", { name: "重试核对" }));
    await waitFor(() =>
      expect(calls.find((c) => c.path.endsWith("/retry"))?.body).toEqual({
        connection_version: 3,
        generation: 7,
      }),
    );
  });

  it("requires a valid recheck range and respects disabled commit support", async () => {
    const user = userEvent.setup();
    const calls = renderSettings(
      path,
      route({
        more: (c) =>
          c.path.endsWith("/repositories/r1/reconcile")
            ? json(200, repository)
            : undefined,
      }),
    );
    await user.click(await screen.findByRole("button", { name: "补验事件" }));
    const dialog = within(screen.getByRole("dialog"));
    await user.click(dialog.getByRole("button", { name: "补验事件" }));
    await dialog.findByText(/请输入有效 RFC3339/);
    expect(calls.some((c) => c.path.endsWith("/reconcile"))).toBe(false);
    await user.click(screen.getByLabelText("补验类型"));
    expect(screen.getByRole("option", { name: "提交范围" })).toHaveAttribute(
      "aria-disabled",
      "true",
    );
    await user.click(screen.getByRole("option", { name: "Issue" }));
    await user.type(
      screen.getByLabelText("开始时间（RFC3339）"),
      "2026-01-01T00:00:00Z",
    );
    await user.type(
      screen.getByLabelText("结束时间（RFC3339）"),
      "2026-01-02T00:00:00Z",
    );
    await user.click(dialog.getByRole("button", { name: "补验事件" }));
    await waitFor(() =>
      expect(calls.find((c) => c.path.endsWith("/reconcile"))?.body).toEqual({
        connection_version: 3,
        generation: 7,
        mode: "issues",
        from: "2026-01-01T00:00:00Z",
        to: "2026-01-02T00:00:00Z",
      }),
    );
  });
});

describe("Gongfeng connection lifecycle", () => {
  it("rotates only after confirming the event gap and uses the configuration version", async () => {
    const user = userEvent.setup();
    const calls = renderSettings(
      path,
      route({
        more: (c) => {
          if (c.path === `${gf}/configuration`)
            return json(200, { ...config, version: 4 });
          if (c.path === `${gf}/rotate-webhook`)
            return json(200, { ...connection, version: 5 });
          return undefined;
        },
      }),
    );
    await screen.findByText("远端 Hook ID");
    await user.click(screen.getByRole("button", { name: "轮换 Webhook 入口" }));
    const dialog = within(screen.getByRole("dialog"));
    await dialog.findByText(/旧入口立即失效/);
    expect(calls.some((c) => c.path.endsWith("/rotate-webhook"))).toBe(false);
    await user.click(dialog.getByRole("button", { name: "确认" }));
    await waitFor(() =>
      expect(
        calls.find((c) => c.path.endsWith("/rotate-webhook"))?.body,
      ).toEqual({ version: 4 }),
    );
  });
  it("requires the explicit disconnect phrase and preserves Hook cleanup information", async () => {
    const user = userEvent.setup();
    let disconnected = false;
    const original = route({ disconnected: true });
    const calls = renderSettings(
      path,
      route({
        more: (c) => {
          if (c.path === `${connectionPath}/disconnect`) {
            disconnected = true;
            return json(200, {
              ...connection,
              status: "DISCONNECTED",
              version: 4,
            });
          }
          if (disconnected && c.path === `${base}/gongfeng`) return original(c);
          return undefined;
        },
      }),
    );
    await screen.findByText("远端 Hook ID");
    await user.click(screen.getByRole("button", { name: "断开" }));
    const dialog = within(screen.getByRole("dialog"));
    await dialog.findByText(/远端 Hook 不会自动删除/);
    expect(dialog.getByRole("button", { name: "断开" })).toBeDisabled();
    await user.type(dialog.getByRole("textbox"), "断开 腾讯工蜂");
    await user.click(dialog.getByRole("button", { name: "断开" }));
    await screen.findByText(/连接已断开。以下仅为保留/);
    expect(screen.getByText("90")).toBeInTheDocument();
    expect(calls.find((c) => c.path.endsWith("/disconnect"))?.body).toEqual({
      version: 3,
      confirm_phrase: "断开 腾讯工蜂",
    });
    expect(
      screen.queryByRole("button", { name: "更新访问令牌" }),
    ).not.toBeInTheDocument();
  });
  it("pages repository configuration and sends pause/resume with the current generation", async () => {
    const user = userEvent.setup();
    let paused = false;
    const calls = renderSettings(
      path,
      route({
        more: (c) => {
          if (c.path === `${gf}/configuration`)
            return json(200, {
              ...config,
              repositories: [
                {
                  ...repository,
                  path: c.query.get("after") ? "team/older" : "team/repo",
                  status: paused ? "PAUSED" : "ACTIVE",
                  generation: paused ? 8 : 7,
                },
              ],
              has_more: !c.query.get("after"),
              next_cursor: c.query.get("after") ? "" : "repository-cursor",
            });
          if (c.path === `${gf}/repositories/r1` && c.method === "PATCH") {
            paused = !paused;
            return json(200, {
              ...repository,
              status: paused ? "PAUSED" : "ACTIVE",
              generation: 8,
            });
          }
          return undefined;
        },
      }),
    );
    await user.click(await screen.findByRole("button", { name: "暂停同步" }));
    await user.click(await screen.findByRole("button", { name: "恢复同步" }));
    await screen.findByRole("button", { name: "暂停同步" });
    expect(
      calls.filter((c) => c.method === "PATCH").map((c) => c.body),
    ).toEqual([
      { connection_version: 3, generation: 7, paused: true },
      { connection_version: 3, generation: 8, paused: false },
    ]);
    await user.click(screen.getByRole("button", { name: "下一页" }));
    await screen.findByRole("link", { name: "team/older" });
    expect(
      calls.some(
        (c) =>
          c.path === `${gf}/configuration` &&
          c.query.get("after") === "repository-cursor",
      ),
    ).toBe(true);
  });
});

describe("Gongfeng reconnect and scan progress", () => {
  it("reconnects only after an explicit same-connection PAT submission", async () => {
    const user = userEvent.setup();
    const calls = renderSettings(
      path,
      route({
        disconnected: true,
        more: (c) =>
          c.path === `${base}/gongfeng/token`
            ? json(200, { ...connection, version: 4 })
            : undefined,
      }),
    );
    await screen.findByText("远端 Hook ID");
    expect(calls.some((c) => c.method !== "GET")).toBe(false);
    await user.click(screen.getByRole("button", { name: "重新连接" }));
    await screen.findByText(/重新更新并激活已选仓库的远端 Hook/);
    await user.type(
      screen.getByLabelText("个人访问令牌（PAT）"),
      "replacement-fixture",
    );
    await user.click(
      within(screen.getByRole("dialog")).getByRole("button", { name: "保存" }),
    );
    await waitFor(() =>
      expect(
        calls.find((c) => c.path === `${base}/gongfeng/token`)?.body,
      ).toEqual({
        connection_id: "c1",
        version: 3,
        access_token: "replacement-fixture",
      }),
    );
    expect(calls.some((c) => c.path.endsWith("/projects"))).toBe(false);
  });
  it("does not equate complete scanning with successful imports", async () => {
    renderSettings(
      path,
      route({
        more: (c) =>
          c.path === `${gf}/configuration`
            ? json(200, {
                ...config,
                repositories: [
                  {
                    ...repository,
                    progress: {
                      issues: {
                        status: "complete",
                        pass: 2,
                        page: 0,
                        count: 2,
                        first_count: 2,
                        history: true,
                        error_code: "",
                      },
                    },
                  },
                ],
              })
            : undefined,
      }),
    );
    await screen.findByText("扫描完成");
    await screen.findByText(/扫描完成不代表所有对象已导入或处理成功/);
    expect(
      screen.getByText("Issue：第 2 轮，第 0 页；本轮扫描 2，首轮扫描 2。"),
    ).toBeInTheDocument();
    expect(screen.queryByText("导入完成")).not.toBeInTheDocument();
  });
});

describe("Gongfeng input boundaries", () => {
  it("constructs only official repository links", () => {
    expect(gongfengRepositoryURL("team/repo")).toBe(
      "https://git.tencent.com/team/repo",
    );
    for (const path of [
      "//evil.example/x",
      "a/../b",
      "a/%2e%2e/b",
      "a\\b/c",
      "a/b?x=1",
      "https://evil.example/x",
      "a/",
    ])
      expect(gongfengRepositoryURL(path)).toBeUndefined();
  });
  it("requires agreeing explicit privacy fields", () => {
    expect(gongfengPrivacy(project)).toBe(true);
    expect(gongfengPrivacy({ ...project, public: true })).toBeNull();
    expect(gongfengPrivacy({ ...project, visibility_level: 10 })).toBe(false);
    expect(gongfengPrivacy({ ...project, visibility_level: null })).toBeNull();
  });
  it("validates recheck timezones and complete nonzero SHAs", () => {
    expect(
      gongfengWindowValid("2026-01-01T00:00:00Z", "2026-01-02T00:00:00Z"),
    ).toBe(true);
    expect(
      gongfengWindowValid("2026-01-01T00:00:00", "2026-01-02T00:00:00Z"),
    ).toBe(false);
    expect(
      gongfengWindowValid("2026-01-02T00:00:00Z", "2026-01-01T00:00:00Z"),
    ).toBe(false);
    expect(
      gongfengPushValid("a".repeat(40), "b".repeat(40), "refs/heads/main"),
    ).toBe(true);
    expect(
      gongfengPushValid("0".repeat(40), "b".repeat(40), "refs/heads/main"),
    ).toBe(false);
    expect(gongfengPushValid("a".repeat(40), "b".repeat(40), "main")).toBe(
      false,
    );
  });
});
