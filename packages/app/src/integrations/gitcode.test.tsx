import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { json, renderSettings, ts, type Route } from "../settings/testing";
const base = "/v1/organizations/o1/integrations",
  conn = `${base}/connections/c1/gitcode`;
const repository = {
  id: "r1",
  connection_id: "c1",
  sync_link_id: "s1",
  remote_repository_id: "91",
  path: "team/repo",
  visibility: "private",
  active: true,
  hook_generation: 1,
  hook_id: "311",
  hook_state: "ACTIVE",
  error_code: "",
  issue_write_status: "UNVERIFIED",
  comment_write_status: "UNVERIFIED",
  version: 9,
  watermark: ts,
};
const link = {
  id: "s1",
  connection_id: "c1",
  space_id: "space1",
  space_name: "Space",
  external_project_id: "team/repo",
  external_project_name: "team/repo",
  direction: "INBOUND",
  status: "ACTIVE",
  status_reason: "",
  created_at: ts,
  version: 7,
  repository_url: "https://atomgit.com/team/repo",
  repository_private: true,
  last_synced_at: null,
  backfill: { status: "none", imported: 0, error: "" },
  outbound: { pending: 0, failed: 0 },
};
const config = {
  version: 6,
  subject: "opaque-user",
  login: "renamed",
  organization_login: "team",
  repositories: [repository],
  links: [link],
  work: [
    {
      id: "w1",
      repository_id: "r1",
      kind: "ISSUE_COMMENT",
      state: "UNKNOWN",
      remote_id: "",
      error_code: "result_unknown",
      next_attempt_at: null,
    },
  ],
  next_repository_after: "",
  next_work_before: "",
};
function route(manage = true, custom?: Route, disconnected = false): Route {
  return (call) => {
    const result = custom?.(call);
    if (result) return result;
    if (call.path === `${base}/gitcode`)
      return json(200, {
        provider: {
          id: "gitcode",
          name: "GitCode / AtomGit",
          category: "git",
          availability: "available",
          template: "B",
          capabilities: ["sync", "import", "pr_link"],
          configured: true,
          bindable: true,
          summary: { connections: 1, status: "ok" },
        },
        can_manage: manage,
        connections: [
          {
            id: "c1",
            provider: "gitcode",
            scope: "ORGANIZATION",
            external_id: "73",
            display_name: "Team",
            account_kind: "organization",
            status: disconnected ? "DISCONNECTED" : "ACTIVE",
            version: 2,
            created_at: ts,
            updated_at: ts,
          },
        ],
        personal_connections: [],
        sync_links: [],
      });
    if (call.path === `${base}/gitcode/settings`)
      return json(200, {
        provider: "gitcode",
        version: 0,
        can_manage: manage,
        updated_at: null,
        settings: {
          link_pull_requests: true,
          link_commits: false,
          pr_backlink_private: true,
          pr_backlink_public: false,
          issue_backlink_private: true,
          issue_backlink_public: false,
          branch_format: "username/identifier-title",
        },
      });
    if (call.path === `${conn}/configuration`) return json(200, config);
    return undefined;
  };
}
describe("GitCode organization UI", () => {
  it("shows changed repository scope and prevents blind resume", async () => {
    const calls = renderSettings(
      "/o/o1/settings/integrations/gitcode",
      route(true, (call) =>
        call.path === `${conn}/configuration`
          ? json(200, {
              ...config,
              repositories: [
                { ...repository, error_code: "repository_changed" },
              ],
              links: [
                {
                  ...link,
                  status: "PAUSED",
                  status_reason: "repository_changed",
                },
              ],
            })
          : undefined,
      ),
    );
    expect(
      await screen.findByText(/远端仓库身份或公开范围已变化/),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "恢复" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "移除同步" })).toBeEnabled();
    expect(calls.some((c) => c.method !== "GET")).toBe(false);
  });
  it("keeps member read-only without loading PAT configuration or personal paths", async () => {
    const calls = renderSettings(
      "/o/o1/settings/integrations/gitcode",
      route(false),
      "MEMBER",
    );
    expect(
      await screen.findByRole("switch", { name: "关联 Pull Request" }),
    ).toBeDisabled();
    expect(
      screen.queryByRole("button", { name: "连接组织" }),
    ).not.toBeInTheDocument();
    expect(
      calls.some(
        (c) => c.path.includes("/connections/") || c.path.includes("/personal"),
      ),
    ).toBe(false);
  });
  it("UNKNOWN reconciliation is explicit, uses current connection CAS and never offers resend", async () => {
    const user = userEvent.setup();
    const calls = renderSettings(
      "/o/o1/settings/integrations/gitcode",
      route(true, (call) =>
        call.path === `${conn}/work/w1/reconcile` ? json(204, null) : undefined,
      ),
    );
    expect(await screen.findByText(/结果未知：只核对/)).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /重发/ }),
    ).not.toBeInTheDocument();
    expect(calls.filter((c) => c.method !== "GET")).toHaveLength(0);
    await user.click(screen.getByRole("button", { name: "重新核对" }));
    await waitFor(() =>
      expect(
        calls.find((c) => c.path.endsWith("/work/w1/reconcile"))?.body,
      ).toEqual({ version: 6 }),
    );
  });
  it("requires explicit bidirectional confirmation and passes link version, not parent or repository version", async () => {
    const user = userEvent.setup();
    const calls = renderSettings(
      "/o/o1/settings/integrations/gitcode",
      route(true, (call) =>
        call.path === `${base}/sync-links/s1`
          ? json(200, { ...link, direction: "BIDIRECTIONAL", version: 8 })
          : undefined,
      ),
    );
    await user.click(
      await screen.findByRole("button", { name: "改变同步方向" }),
    );
    const dialog = screen.getByRole("dialog", { name: "启用双向同步？" });
    expect(within(dialog).getByText(/不会回写负责人/)).toBeInTheDocument();
    expect(calls.some((c) => c.method === "PATCH")).toBe(false);
    await user.click(within(dialog).getByRole("button", { name: "确认" }));
    await waitFor(() =>
      expect(calls.find((c) => c.method === "PATCH")?.body).toEqual({
        version: 7,
        direction: "BIDIRECTIONAL",
      }),
    );
  });
  it("keeps cleanup IDs visible after disconnect without recovery or remote write controls", async () => {
    const calls = renderSettings(
      "/o/o1/settings/integrations/gitcode",
      route(true, undefined, true),
    );
    expect(
      await screen.findByText(/连接已断开，凭据已销毁/),
    ).toBeInTheDocument();
    expect(await screen.findByText("远端 Hook ID：311")).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "重新核对" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "轮换回调密钥" }),
    ).not.toBeInTheDocument();
    expect(calls.every((c) => c.method === "GET")).toBe(true);
  });
  it("invalidates discovered organizations when the token changes", async () => {
    const user = userEvent.setup();
    const calls = renderSettings(
      "/o/o1/settings/integrations/gitcode",
      route(true, (call) =>
        call.path === `${base}/gitcode/organizations`
          ? json(200, {
              principal: { id: "opaque", login: "holder", name: "Holder" },
              organizations: [{ id: 73, login: "team", name: "Team" }],
            })
          : undefined,
      ),
    );
    await user.click(await screen.findByRole("button", { name: "连接组织" }));
    const dialog = screen.getByRole("dialog", { name: "连接组织" });
    const token = within(dialog).getByLabelText("组织管理员 PAT");
    await user.type(token, "fixture-token");
    await user.click(
      within(dialog).getByRole("button", { name: "验证 PAT 并选择组织" }),
    );
    await within(dialog).findByText("授权主体：holder（opaque）");
    await user.type(token, "-changed");
    expect(
      within(dialog).queryByText("授权主体：holder（opaque）"),
    ).not.toBeInTheDocument();
    expect(
      within(dialog).getByRole("button", { name: "连接组织" }),
    ).toBeDisabled();
    expect(
      calls.some(
        (c) => c.path.endsWith("/gitcode/connections") && c.method === "POST",
      ),
    ).toBe(false);
  });
});
