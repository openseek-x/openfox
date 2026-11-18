import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { json, renderSettings, ts, type Route } from "../settings/testing";
const base = "/v1/organizations/o1/integrations/bitbucket",
  conn = `${base}/connections/c1`,
  page = "/o/o1/settings/integrations/bitbucket";
const connection = {
  id: "c1",
  provider: "bitbucket",
  scope: "ORGANIZATION",
  external_id: "w1",
  display_name: "Design",
  status: "ACTIVE",
  version: 3,
  created_at: ts,
  updated_at: ts,
  bitbucket_delegating_user: "Alex",
  bitbucket_delegating_user_id: "u-external",
};
const selection = {
  id: "s1",
  connection_id: "c1",
  workspace_id: "w1",
  repository_id: "r1",
  full_name: "design/app",
  url: "https://bitbucket.org/design/app",
  is_private: true,
  space_ids: ["space1"],
  pull_requests: true,
  commits: false,
  backlink_private: true,
  backlink_public: false,
  status: "ACTIVE",
  error_code: "",
  hook_ready: false,
  version: 7,
};
const hook = {
  connection_id: "c1",
  payload_url: "",
  content_type: "application/json",
  secret: "",
  events: ["pullrequest:created", "repo:push"],
  last_delivery_at: null,
  recent: [],
  version: 3,
};
function route(
  extra: Route = () => undefined,
  selections: unknown[] = [],
): Route {
  return (c) =>
    extra(c) ??
    (c.path === base
      ? json(200, {
          provider: {
            id: "bitbucket",
            name: "Bitbucket",
            template: "B",
            category: "code",
            availability: "available",
            configured: true,
            bindable: true,
            capabilities: ["pr_link"],
            summary: { connections: 1, status: "ok" },
          },
          can_manage: true,
          connections: [connection],
          personal_connections: [],
          sync_links: [],
        })
      : c.path === `${conn}/jobs`
        ? json(200, { jobs: [] })
        : c.path === `${conn}/repositories`
          ? json(200, { repositories: selections })
          : c.path === "/v1/organizations/o1/spaces"
            ? json(200, {
                spaces: [
                  { id: "space1", name: "Engineering", status: "ACTIVE" },
                ],
              })
            : undefined);
}
async function open() {
  await userEvent.click(
    await screen.findByRole("button", { name: "仓库与 Webhook" }),
  );
  return await screen.findByRole("dialog", { name: "仓库与 Webhook" });
}
describe("Bitbucket repository and manual Webhook controls", () => {
  beforeEach(() => localStorage.clear());
  afterEach(() => vi.restoreAllMocks());
  it("uses safe defaults, loads a second page and requires sharing and sender consent", async () => {
    const calls = renderSettings(
      page,
      route((c) =>
        c.path === `${conn}/repositories/available`
          ? json(200, {
              repositories: [
                {
                  uuid: c.query.get("cursor") ? "r2" : "r1",
                  full_name: c.query.get("cursor")
                    ? "design/second"
                    : "design/app",
                  is_private: true,
                },
              ],
              next_cursor: c.query.get("cursor") ? "" : "sealed-next",
              loaded: c.query.get("cursor") ? 2 : 1,
              limit_reached: false,
            })
          : c.path === `${conn}/repositories` && c.method === "PUT"
            ? json(200, selection)
            : undefined,
      ),
    );
    const user = userEvent.setup();
    await open();
    await user.click(screen.getByRole("button", { name: "选择仓库" }));
    const editor = await screen.findByRole("dialog", { name: "仓库关联设置" });
    expect(within(editor).getByLabelText("关联 Pull Request")).toBeChecked();
    expect(within(editor).getByLabelText(/关联提交/)).not.toBeChecked();
    expect(
      within(editor).getByLabelText("在私有仓库 PR 发送任务回链"),
    ).toBeChecked();
    expect(
      within(editor).getByLabelText("在公开仓库 PR 发送任务回链"),
    ).not.toBeChecked();
    await user.click(
      await within(editor).findByRole("button", { name: "加载更多仓库" }),
    );
    await within(editor).findByText("已加载 2 个仓库");
    await user.click(within(editor).getByLabelText("Bitbucket 仓库"));
    await user.click(
      screen.getByRole("option", { name: "design/second · 私有仓库" }),
    );
    await user.click(within(editor).getByLabelText("Engineering"));
    const save = within(editor).getByRole("button", { name: "保存" });
    expect(save).toBeDisabled();
    await user.click(within(editor).getByLabelText(/我同意这些空间的成员/));
    expect(save).toBeDisabled();
    await user.click(
      within(editor).getByLabelText(/我确认使用组织授权账号 Alex/),
    );
    await user.click(save);
    await waitFor(() =>
      expect(calls.find((c) => c.method === "PUT")?.body).toEqual({
        repository_id: "r2",
        connection_version: 3,
        version: 0,
        space_ids: ["space1"],
        pull_requests: true,
        commits: false,
        backlink_private: true,
        backlink_public: false,
        confirm_metadata: true,
        confirm_sender: true,
        confirm_public: false,
      }),
    );
    expect(calls.some((c) => c.path.endsWith("/webhook"))).toBe(false);
  });
  it("requires additional explicit public disclosure consent on every save", async () => {
    const calls = renderSettings(
      page,
      route(
        (c) =>
          c.path === `${conn}/repositories` && c.method === "PUT"
            ? json(200, selection)
            : undefined,
        [selection],
      ),
    );
    const user = userEvent.setup();
    await open();
    await user.click(
      await screen.findByRole("button", { name: "仓库关联设置" }),
    );
    const editor = await screen.findByRole("dialog", { name: "仓库关联设置" });
    await user.click(
      within(editor).getByLabelText("在公开仓库 PR 发送任务回链"),
    );
    await user.click(within(editor).getByLabelText(/我同意这些空间的成员/));
    await user.click(within(editor).getByLabelText(/我确认使用组织授权账号/));
    const save = within(editor).getByRole("button", { name: "保存" });
    expect(save).toBeDisabled();
    await user.click(within(editor).getByLabelText(/我明确同意将任务编号/));
    await user.click(save);
    await waitFor(() =>
      expect(calls.find((c) => c.method === "PUT")?.body).toMatchObject({
        version: 7,
        connection_version: 3,
        backlink_public: true,
        confirm_public: true,
      }),
    );
  });
  it("viewing does not generate a hook; explicit confirmation rotates the current parent version", async () => {
    const calls = renderSettings(
      page,
      route((c) =>
        c.path === `${conn}/webhook`
          ? json(200, hook)
          : c.path === `${conn}/webhook/rotate`
            ? json(200, {
                ...hook,
                version: 4,
                payload_url:
                  "https://center.example/api/v1/hooks/git/bitbucket/fixture-token",
                secret: "fixture-secret",
              })
            : undefined,
      ),
    );
    const user = userEvent.setup();
    await open();
    expect(calls.some((c) => c.path.endsWith("/webhook"))).toBe(false);
    await user.click(
      screen.getByRole("button", { name: "查看手工 Webhook 配置" }),
    );
    const generate = await screen.findByRole("button", {
      name: "生成回调地址与密钥",
    });
    expect(generate).toBeDisabled();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
    await user.click(screen.getByLabelText(/我理解影响，并将手工配置/));
    await user.click(generate);
    await waitFor(() =>
      expect(
        calls.find((c) => c.path.endsWith("/webhook/rotate"))?.body,
      ).toEqual({ version: 3, confirm: true }),
    );
    expect(await screen.findByLabelText("签名 Secret")).toHaveAttribute(
      "type",
      "password",
    );
    expect(screen.getByText(/QUEUED 表示已持久接收/)).toBeVisible();
  });
  it("requires explicit remove confirmation and uses the selection version", async () => {
    const calls = renderSettings(
      page,
      route(
        (c) =>
          c.method === "DELETE"
            ? json(200, { ...selection, status: "REMOVED" })
            : undefined,
        [selection],
      ),
    );
    const user = userEvent.setup();
    await open();
    await user.click(await screen.findByRole("button", { name: "移除仓库" }));
    const remove = screen.getByRole("button", { name: "确认移除" });
    expect(remove).toBeDisabled();
    await user.click(screen.getByLabelText(/我确认移除此仓库/));
    await user.click(remove);
    await waitFor(() =>
      expect(calls.find((c) => c.method === "DELETE")?.body).toEqual({
        version: 7,
      }),
    );
  });
  it("shows explicit server truncation without offering an invented next page", async () => {
    renderSettings(
      page,
      route((c) =>
        c.path === `${conn}/repositories/available`
          ? json(200, {
              repositories: [],
              next_cursor: "",
              loaded: 1000,
              limit_reached: true,
            })
          : undefined,
      ),
    );
    await open();
    await userEvent.click(screen.getByRole("button", { name: "选择仓库" }));
    expect(
      await screen.findByText(/本次浏览已达 1000 项或 20 页上限/),
    ).toBeVisible();
    expect(screen.queryByRole("button", { name: "加载更多仓库" })).toBeNull();
  });
});
