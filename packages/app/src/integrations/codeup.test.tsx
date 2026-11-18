import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import {
  apiError,
  json,
  renderSettings,
  ts,
  type Route,
} from "../settings/testing";
const base = "/v1/organizations/o1/integrations",
  conn = `${base}/connections/c1`;
const configuration = {
  version: 3,
  repositories: [],
  work: [],
  has_more: false,
};
function route(manage = true, more?: Route): Route {
  return (call) => {
    const custom = more?.(call);
    if (custom) return custom;
    if (call.path === `${base}/codeup`)
      return json(200, {
        provider: {
          id: "codeup",
          name: "Codeup",
          category: "git",
          availability: "available",
          template: "B",
          capabilities: ["pr_link"],
          configured: true,
          bindable: true,
          summary: { connections: 1, status: "ok" },
        },
        can_manage: manage,
        connections: [
          {
            id: "c1",
            provider: "codeup",
            scope: "ORGANIZATION",
            external_id: "fixture",
            display_name: "fixture",
            status: "ACTIVE",
            version: 3,
            created_at: ts,
            updated_at: ts,
          },
        ],
        personal_connections: [],
        sync_links: [],
      });
    if (call.path === `${base}/codeup/settings`)
      return json(200, {
        provider: "codeup",
        can_manage: manage,
        version: 0,
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
    if (call.path === `${conn}/codeup/configuration`)
      return json(200, configuration);
    return undefined;
  };
}
describe("Codeup explicit opt-in", () => {
  it("does not request secrets or repositories until opened, and member cannot open configuration", async () => {
    const calls = renderSettings(
      "/o/o1/settings/integrations/codeup",
      route(false),
      "MEMBER",
    );
    expect(
      await screen.findByRole("switch", {
        name: "关联 Codeup Pull Request",
      }),
    ).toBeDisabled();
    expect(
      screen.queryByRole("button", { name: "查看 Webhook 配置" }),
    ).not.toBeInTheDocument();
    expect(calls.some((c) => c.path.includes("/connections/"))).toBe(false);
  });
  it("CAS save conflict remains visible; capability denial does not claim token revocation", async () => {
    const user = userEvent.setup();
    let conflict = false;
    const calls = renderSettings(
      "/o/o1/settings/integrations/codeup",
      route(true, (call) => {
        if (
          call.path === `${conn}/codeup/repositories` &&
          call.method === "GET"
        )
          return json(200, {
            repositories: [
              {
                external_id: "123",
                full_name: "team/repo",
                name: "repo",
                private: true,
                html_url: "https://codeup.aliyun.com/team/repo",
              },
            ],
            has_more: false,
          });
        if (
          call.path === `${conn}/codeup/repositories` &&
          call.method === "PUT"
        ) {
          conflict = true;
          return apiError(409, "VERSION_CONFLICT");
        }
        if (call.path === `${conn}/codeup/configuration` && conflict)
          return json(200, { ...configuration, version: 4 });
        if (call.path === `${conn}/webhook`)
          return apiError(403, "CODEUP_CAPABILITY_DENIED");
        return undefined;
      }),
    );
    await screen.findByText(/已选择 0 个仓库/);
    expect(
      calls.some(
        (c) =>
          c.path.endsWith("/webhook") ||
          c.path === `${conn}/codeup/repositories`,
      ),
    ).toBe(false);
    await user.click(screen.getByRole("button", { name: "读取可访问仓库" }));
    await user.click(
      await screen.findByRole("checkbox", { name: "team/repo" }),
    );
    await user.click(screen.getByRole("button", { name: "保存仓库选择" }));
    await waitFor(() =>
      expect(calls.find((c) => c.method === "PUT")?.body).toEqual({
        version: 3,
        repositories: [{ id: "123", path: "team/repo" }],
      }),
    );
    await screen.findAllByText("内容已被他人修改，已刷新");
    await user.click(screen.getByRole("button", { name: "查看 Webhook 配置" }));
    await screen.findByText(/账号令牌未因此标记撤销/);
  });
  it("pages older work without exposing comment bodies or requesting platform repositories", async () => {
    const user = userEvent.setup();
    const work = (number: number) => ({
      repository_id: "123",
      repository_path: "cloud/repo",
      number,
      terminal: false,
      last_checked_at: null,
      next_check_at: null,
      error_code: "",
      backlink_state: "NONE",
    });
    const calls = renderSettings(
      "/o/o1/settings/integrations/codeup",
      route(true, (call) => {
        if (call.path === `${conn}/codeup/configuration`)
          return json(200, {
            ...configuration,
            work: [work(52)],
            has_more: true,
            next_cursor: "cursor52",
          });
        if (call.path === `${conn}/codeup/work`)
          return json(200, {
            work: [work(51)],
            has_more: false,
            next_cursor: "",
          });
        return undefined;
      }),
    );
    await screen.findByRole("link", { name: "cloud/repo #52" });
    await user.click(screen.getByRole("button", { name: "显示更早的 MR" }));
    await screen.findByRole("link", { name: "cloud/repo #51" });
    expect(
      screen.queryByRole("link", { name: "cloud/repo #52" }),
    ).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "上一页" }));
    await screen.findByRole("link", { name: "cloud/repo #52" });
    expect(
      calls.some(
        (c) =>
          c.path === `${conn}/codeup/repositories` ||
          c.path.endsWith("/webhook"),
      ),
    ).toBe(false);
  });
});
